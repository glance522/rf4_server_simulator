package gameserver

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/tunnel"
)

// Session 一条游戏会话（对应网关侧一条客户端连接）。
//
// 所有出站信封经 sendMu 串行化：分配推送 call_id、构造信封和发送在同一锁内完成，
// 保证客户端看到的 call_id 严格递增，并与网关的隧道路由顺序一致。
type Session struct {
	// keepaliveTicks 保活循环计数，用于低频任务（船票清理）。
	keepaliveTicks int64
	// startedAt 本会话登录时刻。客户端世界时钟从登录起按 24 倍速推进，
	// 船票倒计时的"起始游戏时间"就是按它算的。
	startedAt time.Time
	ID        uint64 // 网关分配的会话号
	Token     string
	Userid    int64
	Srv       *Server

	tunnel *tunnel.Conn
	sendMu sync.Mutex
	// call_id 由 Server 按 token 共享分配，见 nextCallID；Session 不单独计数。

	closed   atomic.Bool
	stopCh   chan struct{} // 关闭信号（保活循环等监听）
	stopOnce sync.Once

	// fishPushMu / fishPushGUID / fishPushAt 抑制同一尾鱼的重复 14/14 推送：
	// 同一 GUID 短时间内推两次会让客户端重建鱼实体，模型与结算对不上。
	fishPushMu   sync.Mutex
	fishPushGUID string
	fishPushAt   time.Time

	stateMu      sync.Mutex
	state        map[string]any // 会话级业务状态（选图、抛竿状态等）
	weatherMu    sync.RWMutex
	weatherPlans map[string]fishingWeatherPlan
	happyMu      sync.RWMutex
	happyOpMu    sync.Mutex
	happyStatus  store.HappyHourStatus

	log *slog.Logger
}

func newSession(srv *Server, id uint64, token string, userid int64, c *tunnel.Conn) *Session {
	return &Session{
		ID:        id,
		Token:     token,
		Userid:    userid,
		Srv:       srv,
		tunnel:    c,
		state:     map[string]any{},
		startedAt: time.Now(),
		stopCh:    make(chan struct{}),
		log:       srv.log.With("userid", userid, "sid", id),
	}
}

// Closed 会话是否已关闭。
func (s *Session) Closed() bool { return s.closed.Load() }

// Done 会话关闭通知。
func (s *Session) Done() <-chan struct{} { return s.stopCh }

// SetState / GetState 会话状态存取（业务系统共享）。
func (s *Session) SetState(key string, v any) {
	s.stateMu.Lock()
	s.state[key] = v
	s.stateMu.Unlock()
}

func (s *Session) GetState(key string) (any, bool) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	v, ok := s.state[key]
	return v, ok
}

// Table 运行时 ops 表便捷入口。
func (s *Session) Table() *proto.OpsTable { return s.Srv.table }

// SendEnvelope 发送一个完整明文信封（响应已带 call_id；推送由 Push 构造）。
func (s *Session) SendEnvelope(env []byte) {
	if s.closed.Load() {
		return
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if err := s.tunnel.WriteRecord(tunnel.TypeData, s.ID, env); err != nil {
		s.log.Warn("隧道发送失败", "err", err)
		s.Close("tunnel_send_failed")
	}
}

// Push 服务器主动推送（marker -1 信封 + 尾字节 0x01）。
// 返回分配的 call_id；被守卫拒发时返回 0。
//
// 服务端推送 body 的顶层必须是 args，并符合 command_contract 的 push_args 声明。
// 形状不符时只丢弃该推送，避免破坏客户端通道。
func (s *Session) Push(main, sub uint8, body *proto.Value) uint32 {
	if s.closed.Load() || s.tunnel == nil {
		return 0
	}
	bodyBytes, err := proto.EncodeValue(body, s.Srv.table)
	if err != nil {
		s.log.Error("推送编码失败", "cmd", cmdKey(main, sub), "err", err)
		return 0
	}
	if len(bodyBytes) == 0 || bodyBytes[0] != 0x03 {
		gsPushRejected.Inc()
		s.log.Error("拒发推送：body 顶层不是 args(0x03)",
			"cmd", cmdKey(main, sub), "first_byte", firstByte(bodyBytes))
		return 0
	}
	if reason := checkPushArgsStrict(main, sub, body); reason != "" {
		gsPushRejected.Inc()
		s.log.Error("拒发推送：形状与合同不符", "cmd", cmdKey(main, sub), "reason", reason)
		return 0
	}

	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.closed.Load() {
		return 0
	}
	callID := uint32(s.Srv.nextCallID(s.Token))
	env := proto.BuildRequestEnvelope(callID, main, sub, append(bodyBytes, 0x01))
	if traceCmds {
		s.log.Info("TRACE 推", "cmd", cmdKey(main, sub), "len", len(bodyBytes),
			"hex", traceHexHead(bodyBytes, 160))
	}
	if err := s.tunnel.WriteRecord(tunnel.TypeData, s.ID, env); err != nil {
		s.log.Warn("推送发送失败", "cmd", cmdKey(main, sub), "err", err)
		s.Close("tunnel_send_failed")
		return 0
	}
	gsPushes.Inc()
	return callID
}

// PushRaw 以已编码 body（bytes）推送（跳过 args 守卫的调用方需自证形状合法）。
func (s *Session) PushRaw(main, sub uint8, bodyBytes []byte) uint32 {
	if s.closed.Load() || len(bodyBytes) == 0 || bodyBytes[0] != 0x03 {
		return 0
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	callID := uint32(s.Srv.nextCallID(s.Token))
	env := proto.BuildRequestEnvelope(callID, main, sub, append(bodyBytes, 0x01))
	if err := s.tunnel.WriteRecord(tunnel.TypeData, s.ID, env); err != nil {
		return 0
	}
	gsPushes.Inc()
	return callID
}

// callSeqKey 按 token 存推送序号；用哈希避免 token 原文落 kv 键名。
func callSeqKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "callseq:" + hex.EncodeToString(sum[:])
}

// restoreCallSeq 从 KV 恢复该 token 已持久化的 call_id 峰值。
func (s *Server) restoreCallSeq(token string) {
	if s.DB == nil || token == "" {
		return
	}
	v, found, err := s.DB.KVGet(s.dbCtx(), callSeqKey(token))
	if err != nil || !found {
		return
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return
	}
	s.callMu.Lock()
	if n > s.callSeq[token] {
		s.callSeq[token] = n
	}
	s.callMu.Unlock()
}

// nextCallID 在 Server 级按 token 原子递增并持久化峰值，
// 避免会话接管或进程重启后序号回退。
func (s *Server) nextCallID(token string) uint64 {
	s.callMu.Lock()
	defer s.callMu.Unlock()
	n := s.callSeq[token] + 1
	s.callSeq[token] = n
	if s.DB != nil && token != "" {
		_ = s.DB.KVSet(s.dbCtx(), callSeqKey(token), strconv.FormatUint(n, 10))
	}
	return n
}

// startKeepalive 启动 2/2 世界时钟保活循环，首包在 0.5 秒后发送。
// 客户端根据 2/2 推算世界日号，长时间无下行会断开连接。
func (s *Session) startKeepalive() {
	s.updateDrinkBonuses(time.Now())
	interval := s.Srv.cfg.KeepaliveInterval()
	go func() {
		timer := time.NewTimer(500 * time.Millisecond)
		defer timer.Stop()
		for {
			select {
			case <-s.stopCh:
				return
			case <-timer.C:
				if s.closed.Load() {
					return
				}
				s.updateHappyHour(time.Now(), false)
				pushKeepalive(s)
				s.updateDrinkBonuses(time.Now())
				// 每 6 拍（约 30 秒）刷新一次已租用船票的剩余分钟，
				// 让客户端的到期提示随时间递减（客户端自身不会重算）。
				// 每 6 拍（约 30 秒）清理一次已到期的租用船票。
				// 注意：只做"到期移除"，不反复重建物品，避免客户端 UI 卡死。
				s.keepaliveTicks++
				if s.keepaliveTicks%6 == 0 {
					s.expireRentalTickets()
				}
				timer.Reset(interval)
			}
		}
	}()
}

// Close 关闭会话（幂等）。
func (s *Session) Close(reason string) {
	if s.closed.CompareAndSwap(false, true) {
		s.updateHappyHour(time.Now(), true)
		s.updateDrinkBonuses(time.Now())
		s.log.Info("会话关闭", "reason", reason)
		s.stopOnce.Do(func() { close(s.stopCh) })
		s.Srv.removeSession(s)
	}
}

func firstByte(b []byte) int {
	if len(b) == 0 {
		return -1
	}
	return int(b[0])
}
