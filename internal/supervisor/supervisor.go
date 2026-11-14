// Package supervisor —— 子进程监督：按序启动、健康等待、环形日志、退避重启、启停 API。
//
// launcher（托盘/Web 面板）与命令行场景共用。子进程以同目录下的可执行文件
// 启动，stdout/stderr 经管道转发进环形缓冲（面板增量拉取）并同时落盘
// logs/<name>.log。任一进程意外退出按指数退避自动重启；显式 Stop 不重启。
package supervisor

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"rf4server/internal/buildinfo"
)

// ProcState 进程状态。
type ProcState string

const (
	StateStopped  ProcState = "stopped"
	StateStarting ProcState = "starting"
	StateRunning  ProcState = "running"
	StateBackoff  ProcState = "backoff" // 意外退出，等待重启
	StateFailed   ProcState = "failed"  // 连续失败放弃
)

const (
	ringLines      = 800 // 每进程内存日志行数
	maxBackoff     = 30 * time.Second
	healthWait     = 20 * time.Second
	maxConsecFails = 5
)

// Spec 一个受管进程的定义。
type Spec struct {
	Name     string // gameserver / gateway
	ExeName  string // 可执行文件名（相对 launcher 同目录）
	Args     []string
	ObsPort  string // 观测端口（":9502" 形式；健康检查用，空=不检查）
	LogFile  string
	Critical bool // true：该进程退出则全组停机
}

// BuildInfo 是子进程自报的构建身份（从观测端口的 rf4_build_info 读到）。
// 没跑起来或观测端口不通时为 nil——"读不到"必须与"版本是 dev"区分开。
type BuildInfo struct {
	Version string `json:"version"`
	BuildTS string `json:"buildts"`
	Commit  string `json:"commit"`
	Source  string `json:"source"`
}

// ProcInfo 面板展示的进程快照。
type ProcInfo struct {
	Name     string    `json:"name"`
	State    ProcState `json:"state"`
	PID      int       `json:"pid"`
	Restarts int       `json:"restarts"`
	UptimeS  float64   `json:"uptime_s"`
	// RunningBuild 是进程内存里真正在跑的构建；仅 StateRunning 时对外暴露。
	// nil = 还没起来、端口不通、或进程已停止/失败/退避（停了就不能再叫 running）。
	RunningBuild *BuildInfo `json:"running_build,omitempty"`
	// ExeChanged 为真表示磁盘上的可执行文件已被替换，与正在跑的这份不同
	// （重启后才会生效）。不比对的话，面板只显示 launcher 自己的版本，
	// 看不出子进程已经换了人。
	ExeChanged bool `json:"exe_changed"`
}

type proc struct {
	spec      Spec
	cmd       *exec.Cmd
	state     ProcState
	pid       int
	startedAt time.Time
	restarts  int
	fails     int

	ringMu  sync.Mutex
	ring    []string // 环形日志（含时间戳前缀）
	ringSeq int64    // 已写入总行数（增量拉取游标）

	stopReq bool // 用户显式停止
	logFile *os.File

	// exeSize/exeMod 是 launch 时刻可执行文件的指纹。磁盘上的文件
	// 之后被替换（另一个构建覆盖 bin/），两者就不再相等——重启前
	// 跑的还是旧代码，这个状态从外部看不见。
	exeSize int64
	exeMod  time.Time

	// buildMu 保护下面两个字段：从观测端口读到的构建身份与读取时刻。
	buildMu   sync.Mutex
	build     *BuildInfo
	buildRead time.Time
}

// Supervisor 全组监督器。
type Supervisor struct {
	mu       sync.Mutex
	dir      string // 可执行文件所在目录
	config   string // 传给子进程的 -config 值
	procs    map[string]*proc
	order    []string // 启动顺序
	stopping bool
	logDir   string
	onState  func() // 状态变化回调（托盘图标刷新），可为 nil

	lgMu    sync.Mutex
	lg      []string // launcher 级进度日志（StartAll 每步）
	lgSeq   int64
	LastErr string // StartAll/StartOne 的最近错误（面板展示）
}

// New 构建监督器。binDir 为子进程可执行目录；configPath 透传 -config；
// logDir 为日志落盘目录（空=不落盘）。
func New(binDir, configPath, logDir string) *Supervisor {
	s := &Supervisor{
		dir:    binDir,
		config: configPath,
		procs:  map[string]*proc{},
		logDir: logDir,
	}
	// 启动顺序即依赖顺序：gameserver（隧道服务端）→ gateway（接入）。
	// 管理后台 handler 内嵌在 launcher 中，不再作为独立监听进程启动。
	for _, sp := range []Spec{
		{Name: "gameserver", ExeName: "gameserver.exe", ObsPort: "9502", LogFile: "gameserver.log"},
		{Name: "gateway", ExeName: "gateway.exe", ObsPort: "9501", LogFile: "gateway.log"},
	} {
		sp.Args = []string{"-config", configPath}
		p := &proc{spec: sp, state: StateStopped}
		s.procs[sp.Name] = p
		s.order = append(s.order, sp.Name)
	}
	return s
}

// SetStateCallback 注册状态变化回调（托盘图标刷新）。
func (s *Supervisor) SetStateCallback(f func()) { s.onState = f }

func (s *Supervisor) notify() {
	if s.onState != nil {
		s.onState()
	}
}

// slogf 记 launcher 级进度（/api/logs?name=_launcher 增量拉取）。
func (s *Supervisor) slogf(format string, a ...any) {
	line := time.Now().Format("15:04:05") + " " + fmt.Sprintf(format, a...)
	s.lgMu.Lock()
	s.lg = append(s.lg, line)
	if len(s.lg) > ringLines {
		s.lg = s.lg[len(s.lg)-ringLines:]
	}
	s.lgSeq++
	s.lgMu.Unlock()
}

func (p *proc) logf(format string, a ...any) {
	line := time.Now().Format("15:04:05") + " " + fmt.Sprintf(format, a...)
	p.ringMu.Lock()
	p.ring = append(p.ring, line)
	if len(p.ring) > ringLines {
		p.ring = p.ring[len(p.ring)-ringLines:]
	}
	p.ringSeq++
	if p.logFile != nil {
		_, _ = p.logFile.WriteString(line + "\n")
	}
	p.ringMu.Unlock()
}

// StartAll 按序启动全部进程（每个等到健康或超时）。
func (s *Supervisor) StartAll() error {
	s.mu.Lock()
	s.stopping = false
	s.mu.Unlock()
	for _, name := range s.order {
		s.slogf("启动 %s …", name)
		if err := s.StartOne(name); err != nil {
			s.LastErr = err.Error()
			s.slogf("启动 %s 失败: %v", name, err)
			return fmt.Errorf("%s: %w", name, err)
		}
		p := s.procs[name]
		if p.spec.ObsPort != "" {
			if err := waitHealthy(p.spec.ObsPort, healthWait); err != nil {
				s.LastErr = err.Error()
				s.slogf("%s 健康检查超时: %v", name, err)
				return fmt.Errorf("%s 健康检查超时: %w", name, err)
			}
		}
		s.slogf("%s 就绪", name)
	}
	s.LastErr = ""
	s.slogf("全部进程就绪")
	return nil
}

// StartOne 启动单个进程（进入监督循环）。
func (s *Supervisor) StartOne(name string) error {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return errors.New("正在停止，拒绝启动")
	}
	p := s.procs[name]
	if p == nil {
		s.mu.Unlock()
		return fmt.Errorf("未知进程 %s", name)
	}
	if p.state == StateRunning || p.state == StateStarting {
		s.mu.Unlock()
		return nil // 已在运行
	}
	p.stopReq = false
	p.state = StateStarting
	s.mu.Unlock()

	if err := s.launch(p); err != nil {
		p.state = StateFailed
		s.slogf("launch %s 失败: %v", name, err)
		s.notify()
		return err
	}
	go s.supervise(p)
	s.notify()
	return nil
}

// launch 实际拉起子进程（日志管道 + 落盘）。
func (s *Supervisor) launch(p *proc) error {
	exe := filepath.Join(s.dir, p.spec.ExeName)
	fi, err := os.Stat(exe)
	if err != nil {
		return fmt.Errorf("找不到 %s（请确认与 launcher 同目录）", p.spec.ExeName)
	}
	if s.logDir != "" && p.logFile == nil {
		_ = os.MkdirAll(s.logDir, 0o755)
		if f, err := os.OpenFile(filepath.Join(s.logDir, p.spec.LogFile),
			os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			p.logFile = f
		}
	}
	cmd := exec.Command(exe, p.spec.Args...)
	cmd.SysProcAttr = childSysProcAttr() // Windows: 隐藏子进程控制台黑框
	// 子进程工作目录与 launcher 相同，使 config、server_data、data 等
	// 相对路径保持一致；launcher 启动时已切换到仓库根目录。
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout // 合并 stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	p.cmd = cmd
	p.pid = cmd.Process.Pid
	p.state = StateRunning
	p.startedAt = time.Now()
	p.exeSize = fi.Size()
	p.exeMod = fi.ModTime()
	p.buildMu.Lock()
	p.build = nil // 新进程，旧的身份作废
	p.buildRead = time.Time{}
	p.buildMu.Unlock()
	p.logf("[%s] 已启动 pid=%d", p.spec.Name, p.pid)

	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			p.logf("%s", sc.Text())
		}
	}()
	return nil
}

// supervise 监督循环：等待退出；意外退出按退避重启。
func (s *Supervisor) supervise(p *proc) {
	backoff := time.Second
	for {
		err := p.cmd.Wait()
		s.mu.Lock()
		if s.stopping || p.stopReq {
			p.state = StateStopped
			p.pid = 0
			s.clearBuild(p)
			s.mu.Unlock()
			p.logf("[%s] 已停止", p.spec.Name)
			s.notify()
			return
		}
		p.fails++
		p.restarts++
		consec := p.fails
		if consec > maxConsecFails {
			p.state = StateFailed
			s.clearBuild(p)
			s.mu.Unlock()
			p.logf("[%s] 意外退出（%v）；连续失败超过 %d 次，放弃重启",
				p.spec.Name, err, maxConsecFails)
			s.notify()
			return
		}
		p.state = StateBackoff
		s.clearBuild(p)
		s.mu.Unlock()
		p.logf("[%s] 意外退出（%v），第 %d 次重启（连续失败 %d）",
			p.spec.Name, err, p.restarts, consec)
		s.notify()

		time.Sleep(backoff)
		backoff = min(backoff*2, maxBackoff)

		s.mu.Lock()
		if s.stopping || p.stopReq {
			p.state = StateStopped
			p.pid = 0
			s.clearBuild(p)
			s.mu.Unlock()
			s.notify()
			return
		}
		s.mu.Unlock()
		if err := s.launch(p); err != nil {
			p.logf("[%s] 重启失败: %v", p.spec.Name, err)
			continue
		}
		s.mu.Lock()
		p.fails = 0 // 拉起成功清零连续失败
		s.mu.Unlock()
		s.notify()
	}
}

// StopOne 停止单个进程（显式停止，不自动重启）。
func (s *Supervisor) StopOne(name string) {
	s.mu.Lock()
	p := s.procs[name]
	if p == nil {
		s.mu.Unlock()
		return
	}
	p.stopReq = true
	s.clearBuild(p) // 停止指令已下达，不再对外报旧身份
	s.mu.Unlock()
	if p.cmd != nil && p.cmd.Process != nil {
		p.logf("[%s] 收到停止指令", name)
		_ = p.cmd.Process.Kill() // SQLite WAL 下安全；硬杀避免 Windows 信号复杂性
	}
}

// StopAll 停止全部（退出用）。
func (s *Supervisor) StopAll() {
	s.mu.Lock()
	s.stopping = true
	s.mu.Unlock()
	for _, name := range s.order {
		s.StopOne(name)
	}
	// 等待全部落定（最多 10s）
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		busy := false
		for _, p := range s.procs {
			if p.state == StateRunning || p.state == StateStarting || p.state == StateBackoff {
				busy = true
			}
		}
		if !busy {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// RestartOne 重启单个进程。
func (s *Supervisor) RestartOne(name string) error {
	s.StopOne(name)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if s.procs[name].state == StateStopped {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return s.StartOne(name)
}

// LastError StartAll 最近错误（空=无）。
func (s *Supervisor) LastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LastErr
}

// Status 全组快照。
func (s *Supervisor) Status() []ProcInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ProcInfo, 0, len(s.order))
	for _, name := range s.order {
		p := s.procs[name]
		uptime := 0.0
		if p.state == StateRunning && !p.startedAt.IsZero() {
			uptime = time.Since(p.startedAt).Seconds()
		}
		// 只有真正在跑的进程才配叫 running_build：停掉/退避/失败后，
		// 即使缓存里还留着上一次自报的身份，也不能再对外暴露，
		// 否则面板会在进程停止后继续显示旧版本（且端口可能已被复用）。
		p.buildMu.Lock()
		var bi *BuildInfo
		if p.state == StateRunning && p.build != nil {
			cp := *p.build
			bi = &cp
		}
		p.buildMu.Unlock()
		out = append(out, ProcInfo{
			Name: name, State: p.state, PID: p.pid,
			Restarts: p.restarts, UptimeS: uptime,
			RunningBuild: bi,
			ExeChanged:   s.exeChanged(p),
		})
	}
	return out
}

// exeChanged 磁盘上的可执行文件是否已不同于启动这份进程时的文件。
//
// 覆盖 bin/ 不会影响正在跑的进程（Windows 允许替换运行中可执行文件的目录项），
// 但重启就会换人。若不比对，唯一的变化是下次重启后日志里多一行 version，
// 没有任何「你部署的那份已经不在了」的提示。
func (s *Supervisor) exeChanged(p *proc) bool {
	if p.exeSize == 0 || p.state != StateRunning {
		return false
	}
	fi, err := os.Stat(filepath.Join(s.dir, p.spec.ExeName))
	if err != nil {
		return false
	}
	return fi.Size() != p.exeSize || !fi.ModTime().Equal(p.exeMod)
}

// clearBuild 作废进程自报身份。停止/退出路径把 state 改成非 running 时一起调用，
// 避免旧身份留在缓存里。只取 p.buildMu；与 state 变更一起用时调用方应已持有 s.mu，
// 锁顺序恒为 s.mu → p.buildMu，与 Status 一致（绝不反过来，避免 AB-BA 死锁）。
func (s *Supervisor) clearBuild(p *proc) {
	p.buildMu.Lock()
	p.build = nil
	p.buildRead = time.Time{}
	p.buildMu.Unlock()
}

// procRunning 读权威状态（p.state 由 s.mu 保护）。readBuild 在读端口前先查一次，
// 避免给停掉的进程访问可能已被复用的端口。
func (s *Supervisor) procRunning(p *proc) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return p.state == StateRunning
}

// setBuildIfRunning 在 fetch 返回后写回身份。fetch 最长 2 秒，期间进程可能
// 已退出/被停止，所以必须在 s.mu 内二次确认仍为 running 才写；把状态检查与
// 写入绑在同一把 s.mu 下，和 clearBuild/Status 的锁顺序一致（s.mu → p.buildMu），
// 杜绝「刚作废的身份又被并发写回来」的复活窗口。
func (s *Supervisor) setBuildIfRunning(p *proc, bi *BuildInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.state != StateRunning {
		return
	}
	p.buildMu.Lock()
	p.buildRead = time.Now()
	p.build = bi
	p.buildMu.Unlock()
}

// readBuild 从观测端口的 /metrics 读子进程自报的构建身份。
// 缓存 10 秒：面板每 2 秒刷新一次，不必每次都打端口。
func (s *Supervisor) readBuild(p *proc) {
	if p.spec.ObsPort == "" {
		return
	}
	// 非 running 不读：停掉的进程端口可能被系统/别的进程复用，
	// 读回来的身份不属于它，running_build 这个名字也不该给没在跑的进程。
	if !s.procRunning(p) {
		return
	}
	p.buildMu.Lock()
	fresh := time.Since(p.buildRead) < 10*time.Second
	p.buildMu.Unlock()
	if fresh {
		return
	}
	bi := fetchBuild(normalizeObs(p.spec.ObsPort))
	// 写回时二次确认状态，且状态检查与写入在同一把 s.mu 下完成。
	s.setBuildIfRunning(p, bi)
}

// fetchBuild 取一次 /metrics 并解析 rf4_build_info。失败返回 nil——
// 「读不到」和「版本是 dev」是两件事，不能都当成空字符串。
func fetchBuild(addr string) *BuildInfo {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + addr + "/metrics")
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	version, buildts, commit, source, ok := buildinfo.ParseMetric(string(body))
	if !ok || version == "" {
		// 没有这条指标，或指标在但缺 version 标签（本次改动之前的老构建）：
		// 两种都是「无法自证」，必须返回 nil 让上层显示「未知」，
		// 不能返回一个空身份被误读成「版本正常」。
		return nil
	}
	return &BuildInfo{Version: version, BuildTS: buildts, Commit: commit, Source: source}
}

// RefreshBuilds 并发刷新全部进程自报的构建身份。
// 由 launcher 的后台循环每 10 秒调一次；面板请求只读缓存，不会阻塞。
func (s *Supervisor) RefreshBuilds() {
	s.mu.Lock()
	procs := make([]*proc, 0, len(s.order))
	for _, name := range s.order {
		procs = append(procs, s.procs[name])
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, p := range procs {
		wg.Add(1)
		go func(p *proc) {
			defer wg.Done()
			s.readBuild(p)
		}(p)
	}
	wg.Wait()
}

// AllRunning 全部处于 running。
func (s *Supervisor) AllRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.procs {
		if p.state != StateRunning {
			return false
		}
	}
	return true
}

// Logs 增量取环形日志：since 为上次游标（0=从头取现有全部）。
// name="_launcher" 返回 launcher 级进度日志。
func (s *Supervisor) Logs(name string, since int64) ([]string, int64) {
	if name == "_launcher" {
		s.lgMu.Lock()
		defer s.lgMu.Unlock()
		start := int64(len(s.lg)) - (s.lgSeq - since)
		if start < 0 {
			start = 0
		}
		if start > int64(len(s.lg)) {
			start = int64(len(s.lg))
		}
		out := make([]string, int64(len(s.lg))-start)
		copy(out, s.lg[start:])
		return out, s.lgSeq
	}
	s.mu.Lock()
	p := s.procs[name]
	s.mu.Unlock()
	if p == nil {
		return nil, 0
	}
	p.ringMu.Lock()
	defer p.ringMu.Unlock()
	start := int64(len(p.ring)) - (p.ringSeq - since)
	if start < 0 {
		start = 0
	}
	if start > int64(len(p.ring)) {
		start = int64(len(p.ring))
	}
	out := make([]string, int64(len(p.ring))-start)
	copy(out, p.ring[start:])
	return out, p.ringSeq
}

// waitHealthy 轮询观测端口 /healthz。
func waitHealthy(obsPort string, timeout time.Duration) error {
	addr := normalizeObs(obsPort)
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://" + addr + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("%s: %v", addr, lastErr)
}

// normalizeObs "9502"/":9502"/"host:port" → "127.0.0.1:9502"/原样。
func normalizeObs(addr string) string {
	if !strings.Contains(addr, ":") {
		return "127.0.0.1:" + addr
	}
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	return addr
}
