package supervisor

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// metricsBody 返回一段最小的 /metrics 文本，含 rf4_build_info。
func metricsBody(version, buildts, commit, source string) string {
	return "# HELP rf4_build_info 构建信息\n" +
		`rf4_build_info{process="gameserver",version="` + version + `",buildts="` + buildts +
		`",commit="` + commit + `",source="` + source + `"} 1` + "\n"
}

// newMetricsServer 起一个只服务 /metrics 的假观测端口。
func newMetricsServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// hostOf 把 httptest 的 URL 转成 fetchBuild 要的 host:port。
func hostOf(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	return strings.TrimPrefix(srv.URL, "http://")
}

func TestFetchBuildParsesSelfReportedIdentity(t *testing.T) {
	srv := newMetricsServer(t, metricsBody("20260101090000-test", "20260101010000", "abc1234", "/repo/.agents/worktrees/feature-x"))
	bi := fetchBuild(hostOf(t, srv))
	if bi == nil {
		t.Fatal("fetchBuild returned nil, want identity")
	}
	if bi.Version != "20260101090000-test" {
		t.Fatalf("version=%q", bi.Version)
	}
	if bi.BuildTS != "20260101010000" || bi.Commit != "abc1234" {
		t.Fatalf("buildts=%q commit=%q", bi.BuildTS, bi.Commit)
	}
	if !strings.Contains(bi.Source, "worktrees/feature-x") {
		t.Fatalf("source=%q", bi.Source)
	}
}

// 老构建的 /metrics 没有这条指标，必须返回 nil —— 而不是
// 一个 Version 为空的 BuildInfo（那会被面板当成“版本正常”）。
func TestFetchBuildWithoutMetricIsUnknown(t *testing.T) {
	srv := newMetricsServer(t, "# no build info here\nrf4_uptime_seconds 12\n")
	if bi := fetchBuild(hostOf(t, srv)); bi != nil {
		t.Fatalf("got %+v, want nil", bi)
	}
}

func TestFetchBuildNon200IsUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if bi := fetchBuild(hostOf(t, srv)); bi != nil {
		t.Fatalf("got %+v, want nil", bi)
	}
}

func TestFetchBuildUnreachableIsUnknown(t *testing.T) {
	// 127.0.0.1:1 正常关闭，连接会被拒绝。
	if bi := fetchBuild("127.0.0.1:1"); bi != nil {
		t.Fatalf("got %+v, want nil", bi)
	}
}

// RefreshBuilds 把每个进程自己的自报身份填进 Status，面板与 /api/status 才看得到。
func TestRefreshBuildsFillsStatusPerProcess(t *testing.T) {
	gsSrv := newMetricsServer(t, metricsBody("v4-gameserver", "20261112030000", "abc1234", "D:/wt/a"))
	gwSrv := newMetricsServer(t, metricsBody("v4-gateway", "20261112025900", "abc1234", "D:/wt/a"))
	s := New(t.TempDir(), "config.yaml", "")

	// 默认的 9501/9502 在本机可能真的在跑，那会把测试变成
	// 「依赖本机有没有起服务」的假绿/假红，所以全部改指假端口。
	s.mu.Lock()
	// readBuild/Status 只在 running 态读、报身份；测试要显式置为运行中，
	// 否则门禁会直接跳过，测不到「按进程各取各的身份」。
	s.procs["gameserver"].state = StateRunning
	s.procs["gateway"].state = StateRunning
	s.procs["gameserver"].spec.ObsPort = hostOf(t, gsSrv)
	s.procs["gateway"].spec.ObsPort = hostOf(t, gwSrv)
	s.mu.Unlock()

	s.RefreshBuilds()

	status := s.Status()
	if len(status) != 2 || status[0].Name != "gameserver" || status[1].Name != "gateway" {
		t.Fatalf("unexpected status %+v", status)
	}
	if status[0].RunningBuild == nil {
		t.Fatal("gameserver.RunningBuild = nil, want identity")
	}
	if got := status[0].RunningBuild.Version; got != "v4-gameserver" {
		t.Fatalf("gameserver version=%q", got)
	}
	if status[1].RunningBuild == nil {
		t.Fatal("gateway.RunningBuild = nil, want identity")
	}
	// 关键：不能把 gameserver 的身份张冠李戴到 gateway 头上。
	if got := status[1].RunningBuild.Version; got != "v4-gateway" {
		t.Fatalf("gateway version=%q", got)
	}
}

// 端口不通的进程保持 nil，不能被别的进程的身份污染。
func TestRefreshBuildsLeavesUnreachableUnknown(t *testing.T) {
	s := New(t.TempDir(), "config.yaml", "")
	s.mu.Lock()
	for _, p := range s.procs {
		p.state = StateRunning // 让 readBuild 真的去连一次失败端口
		p.spec.ObsPort = "127.0.0.1:1"
	}
	s.mu.Unlock()

	s.RefreshBuilds()

	for _, p := range s.Status() {
		if p.RunningBuild != nil {
			t.Fatalf("%s.RunningBuild = %+v, want nil", p.Name, p.RunningBuild)
		}
	}
}

// exeChanged 的核心判据：bin/ 被别的构建覆盖后，磁盘文件与启动时那份
// 不一致，但进程还在跑旧代码。
func TestExeChangedDetectsReplacedBinary(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "gameserver.exe")
	if err := os.WriteFile(exe, []byte("old build"), 0o755); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}

	s := New(dir, "config.yaml", "")
	s.mu.Lock()
	p := s.procs["gameserver"]
	p.state = StateRunning
	p.exeSize = fi.Size()
	p.exeMod = fi.ModTime()
	s.mu.Unlock()

	if s.exeChanged(p) {
		t.Fatal("exeChanged=true right after launch, want false")
	}

	// 模拟另一个 worktree 跑了 build.ps1 把 bin/ 覆盖掉。
	if err := os.WriteFile(exe, []byte("a different build, quite a bit larger"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !s.exeChanged(p) {
		t.Fatal("exeChanged=false after the binary was replaced, want true")
	}

	// 同一构建重新启动后指纹刷新，告警要能自愈。
	fi2, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	p.exeSize = fi2.Size()
	p.exeMod = fi2.ModTime()
	s.mu.Unlock()
	if s.exeChanged(p) {
		t.Fatal("exeChanged=true after relaunch, want false")
	}
}

// 未运行 / 从未启动时不能误报：停着的进程当然“磁盘和内存不一致”。
func TestExeChangedIgnoresStoppedProcess(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gameserver.exe"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := New(dir, "config.yaml", "")
	s.mu.Lock()
	p := s.procs["gameserver"]
	p.state = StateStopped
	p.exeSize = 1
	p.exeMod = time.Now().Add(-time.Hour)
	s.mu.Unlock()
	if s.exeChanged(p) {
		t.Fatal("exeChanged=true for stopped process, want false")
	}
}

// 停止/失败/退避等非 running 状态一律不暴露 running_build：
// 只判断 p.build != nil 会让停掉的进程继续显示上一次自报的旧版本。
func TestStatusHidesRunningBuildUnlessRunning(t *testing.T) {
	s := New(t.TempDir(), "config.yaml", "")
	p := s.procs["gameserver"]
	s.mu.Lock()
	p.buildMu.Lock()
	p.build = &BuildInfo{Version: "v-old", BuildTS: "t", Commit: "c", Source: "s"}
	p.buildRead = time.Now()
	p.buildMu.Unlock()
	p.state = StateRunning
	s.mu.Unlock()

	if got := s.Status()[0].RunningBuild; got == nil || got.Version != "v-old" {
		t.Fatalf("running process should expose identity, got %+v", got)
	}

	for _, state := range []ProcState{StateStopped, StateStarting, StateBackoff, StateFailed} {
		s.mu.Lock()
		p.state = state
		s.mu.Unlock()
		if got := s.Status()[0].RunningBuild; got != nil {
			t.Fatalf("state=%s: RunningBuild = %+v, want nil", state, got)
		}
	}
}

// 非 running 进程不该再去 fetch 观测端口：端口可能已被复用，
// 读回来的身份不属于这个进程。
func TestReadBuildSkipsNonRunningProcess(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(metricsBody("v", "t", "c", "s")))
	}))
	defer srv.Close()

	s := New(t.TempDir(), "config.yaml", "")
	s.mu.Lock()
	p := s.procs["gameserver"]
	p.state = StateStopped
	p.spec.ObsPort = hostOf(t, srv)
	s.mu.Unlock()

	s.readBuild(p)

	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("readBuild fetched %d times for a stopped process, want 0", got)
	}
	p.buildMu.Lock()
	defer p.buildMu.Unlock()
	if p.build != nil {
		t.Fatalf("stopped process got build %+v, want nil", p.build)
	}
}

// StopOne 下达停止指令时立即作废缓存身份，不留到下一次 10 秒刷新。
func TestStopOneClearsRunningBuild(t *testing.T) {
	s := New(t.TempDir(), "config.yaml", "")
	p := s.procs["gameserver"]
	p.buildMu.Lock()
	p.build = &BuildInfo{Version: "v-old"}
	p.buildRead = time.Now()
	p.buildMu.Unlock()

	s.StopOne("gameserver")

	p.buildMu.Lock()
	defer p.buildMu.Unlock()
	if p.build != nil {
		t.Fatalf("StopOne left stale build %+v, want nil", p.build)
	}
	if !p.buildRead.IsZero() {
		t.Fatalf("StopOne left buildRead=%v, want zero", p.buildRead)
	}
}
