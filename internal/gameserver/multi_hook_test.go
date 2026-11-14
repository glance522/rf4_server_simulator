package gameserver

import (
	"math"
	"strings"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// setMultiHookTunableForTest 直接写 tunable 缓存，避免测试依赖数据库里的运行参数。
func setMultiHookTunableForTest(t *testing.T, s *Server, key string, v float64) {
	t.Helper()
	s.tunMu.Lock()
	if s.tunCache == nil {
		s.tunCache = map[string]float64{}
	}
	s.tunCache[key] = v
	s.tunAt = time.Now()
	s.tunMu.Unlock()
}

// 关闭开关时必须完全等价于旧行为：单鱼、不读额外参数。
func TestMultiHookConfigDefaultsToSingleFish(t *testing.T) {
	s := &Server{}
	cfg := s.multiHookConfig()
	if cfg.Enabled {
		t.Fatal("multi_hook_bite 缺省应关闭")
	}
	if cfg.MaxTotal != 1 {
		t.Fatalf("关闭时最多鱼数应为 1，得到 %d", cfg.MaxTotal)
	}
}

// 总拉力预算：Σf8 超过 κ × 最弱环时按体重占比压缩，压缩后落回预算内。
func TestMultiHookPullBudgetShrinksToBudget(t *testing.T) {
	s := &Server{}
	fish := []*RolledFish{
		{WeightG: 10000, Force: 100},
		{WeightG: 20000, Force: 200},
		{WeightG: 30000, Force: 300},
	}
	s.applyMultiHookPullBudget(fish, 10, 0.8)
	want := 0.8 * 10 * newtonPerKgf
	total := 0.0
	for _, f := range fish {
		total += f.Force
	}
	if math.Abs(total-want) > 0.35 {
		t.Fatalf("压缩后总拉力应约 %.2f N，得到 %.2f N", want, total)
	}
	// 1:2:3 的体重占比应保持
	if math.Abs(fish[0].Force*3-fish[2].Force) > 0.5 {
		t.Fatalf("应按体重占比分配，得到 %.3f / %.3f", fish[0].Force, fish[2].Force)
	}
}

// 未超预算时不得改写原始拉力。
func TestMultiHookPullBudgetLeavesRoomWhenUnder(t *testing.T) {
	s := &Server{}
	fish := []*RolledFish{
		{WeightG: 1000, Force: 10},
		{WeightG: 1000, Force: 10},
	}
	s.applyMultiHookPullBudget(fish, 100, 0.8)
	for _, f := range fish {
		if f.Force != 10 {
			t.Fatalf("未超预算不应改写拉力，得到 %.3f", f.Force)
		}
	}
}

// 单条鱼不参与合力预算（没有第二尾就没有合力问题）。
func TestMultiHookPullBudgetIgnoresSingleFish(t *testing.T) {
	s := &Server{}
	fish := []*RolledFish{{WeightG: 50000, Force: 500}}
	s.applyMultiHookPullBudget(fish, 1, 0.8)
	if fish[0].Force != 500 {
		t.Fatalf("单鱼不应被压缩，得到 %.3f", fish[0].Force)
	}
}

// 队列提升：队首接管 Pending，已推过的鱼不再重推，最后一条把 HoldMulti 清掉。
func TestPromoteExtraLockedAdvancesQueue(t *testing.T) {
	st := &FishState{}
	a := &RolledFish{FishGUID: "aa", HookGUID: "h1", HookConfirmed: true}
	b := &RolledFish{FishGUID: "bb", HookGUID: "h2"}
	st.Extra = []*RolledFish{a, b}

	if got := promoteExtraLocked(st); got != a {
		t.Fatalf("应提升队首，得到 %v", got)
	}
	if st.Pending != a || !st.PushSent || !st.Hooked || st.Resolved {
		t.Fatalf("提升后状态不对：pending=%v pushSent=%v hooked=%v resolved=%v",
			st.Pending == a, st.PushSent, st.Hooked, st.Resolved)
	}
	if !st.HoldMulti {
		t.Fatal("队列里还有鱼时应保持 HoldMulti")
	}
	if got := promoteExtraLocked(st); got != b {
		t.Fatalf("应提升第二条，得到 %v", got)
	}
	if st.HoldMulti {
		t.Fatal("队列空了以后应清掉 HoldMulti")
	}
	if st.Hooked {
		t.Fatal("未确认中钩的第二条不应置 Hooked")
	}
	if promoteExtraLocked(st) != nil {
		t.Fatal("空队列应返回 nil")
	}
}

// 14/11 不假设参数布局：任何位置的 GUID 命中队列都要认领。
func TestExtraIndexByAnyGUID(t *testing.T) {
	st := &FishState{Extra: []*RolledFish{
		{FishGUID: "1111"}, {FishGUID: "2222"},
	}}
	if i := extraIndexByAnyGUID(st, []string{"rod-guid", "2222"}); i != 1 {
		t.Fatalf("应在第 2 条命中，得到 %d", i)
	}
	if i := extraIndexByAnyGUID(st, []string{"rod-guid"}); i != -1 {
		t.Fatalf("不匹配时应返回 -1，得到 %d", i)
	}
}

// 移除额外鱼：中间的删掉后其余保持顺序，清空时同步清 HoldMulti。
func TestRemoveExtraAt(t *testing.T) {
	st := &FishState{HoldMulti: true}
	a := &RolledFish{FishGUID: "aa"}
	b := &RolledFish{FishGUID: "bb"}
	c := &RolledFish{FishGUID: "cc"}
	st.Extra = []*RolledFish{a, b, c}
	if got := removeExtraAt(st, 1); got != b {
		t.Fatalf("应移除 bb，得到 %v", got)
	}
	if len(st.Extra) != 2 || st.Extra[0] != a || st.Extra[1] != c {
		t.Fatalf("移除后顺序不对：%v", st.Extra)
	}
	removeExtraAt(st, 0)
	removeExtraAt(st, 0)
	if len(st.Extra) != 0 || st.HoldMulti {
		t.Fatalf("清空后应无 HoldMulti，得到 len=%d hold=%v", len(st.Extra), st.HoldMulti)
	}
}

// 接管后必须重派鱼。
//
// 实机 2026-11-13 15:20:53：主鱼报 f0=2 脱钩，服务端立刻接管队列里的
// sebastes_n，但 promoteExtraLocked 认为它"当初作为额外鱼推过 14/14"
// 而不再发任何包。客户端在脱钩播报时已经把钩上的鱼模型和力度条清掉了，
// 于是玩家看到「脱钩后不显示鱼、钩上没模型，收完线却突然上来一条」。
func TestRepushPromotedAfterTakeoverResetsPushSent(t *testing.T) {
	t.Parallel()
	srv, _ := newRigTestServer(t)
	sess := &Session{ID: 1, Userid: 1000001, Srv: srv, log: logging.Discard()}

	next := &RolledFish{FishGUID: "bbbbbbbb", HookGUID: "hook-b"}
	st := &FishState{
		GearGuid:   "rod-1",
		CastActive: true,
		InWater:    true,
		Pending:    &RolledFish{FishGUID: "aaaaaaaa", HookGUID: "hook-a"},
		Extra:      []*RolledFish{next},
	}

	if got := srv.promoteAfterActiveGone(st); got != next {
		t.Fatalf("应接管队首，得到 %v", got)
	}
	if !st.PushSent {
		t.Fatal("提升后默认视为已推，避免旧路径重复派鱼")
	}

	// 没有通信通道，Push14_14 不会成功；此时 PushSent 必须落回 false，
	// 交给 6s 重推 timer，否则这条鱼永远只存在于服务端。
	srv.repushPromotedAfterTakeover(sess, st)
	st.mu.Lock()
	sent := st.PushSent
	st.cancelPendingTimerLocked()
	st.mu.Unlock()
	if sent {
		t.Fatal("推送未成功时不得保留 PushSent=true，否则客户端永远收不到这尾鱼")
	}
}

// 空会话/空状态不得 panic，也不能改状态。
func TestRepushPromotedAfterTakeoverNoop(t *testing.T) {
	t.Parallel()
	srv, _ := newRigTestServer(t)
	st := &FishState{CastActive: true, Pending: &RolledFish{FishGUID: "cc"}}
	srv.repushPromotedAfterTakeover(nil, st)
	if !st.PushSent && st.Pending == nil {
		t.Fatal("空会话调用不应动状态")
	}
	srv.repushPromotedAfterTakeover(nil, nil)
}

// 收竿时本杆所有的鱼必须一次结算完。
//
// 实机 2026-11-13 15:40/15:41 两次收竿：4 条鱼都发过 14/14，14/4 只结算了
// Pending，剩下的 3 条要等 14/5 之后"接管"，可客户端收竿后 1.2 秒就开始算
// 下一次抛投（15:40:29 收竿、15:40:31 发 14/1），接管包只是让客户端回了一条
// 14/12，力度条和钩上模型都不会再出现——玩家看到的就是"每次拉上来只有一条"。
func TestReelInSettlesWholeMultiHookCatch(t *testing.T) {
	t.Parallel()
	s, db := newRigTestServer(t)
	sess := newProbeSess(s)
	uid := sess.Userid
	if _, err := db.EnsureProfile(t.Context(), uid); err != nil {
		t.Fatal(err)
	}

	main := &RolledFish{FishGUID: strings.Repeat("a", 32), Species: "perch", Grade: 2, WeightG: 640}
	extra1 := &RolledFish{FishGUID: strings.Repeat("b", 32), Species: "perch", Grade: 1, WeightG: 320}
	extra2 := &RolledFish{FishGUID: strings.Repeat("c", 32), Species: "perch", Grade: 1, WeightG: 210}
	st := s.stateFor(uid)
	st.mu.Lock()
	st.Pending = main
	st.Hooked = true
	st.Extra = []*RolledFish{extra1, extra2}
	st.mu.Unlock()

	body, handled := s.fishingEndSettle(&Ctx{Sess: sess, CallID: 1, Main: 14, Sub: 4,
		ExpectResponse: true, Log: logging.Discard()})
	if !handled || len(body) == 0 {
		t.Fatal("结算未正常返回")
	}

	st.mu.Lock()
	extraLeft, batch := len(st.Extra), len(st.Batch)
	hold := st.HoldMulti
	st.mu.Unlock()
	if extraLeft != 0 || hold {
		t.Fatalf("收竿后多钩队列应清空且不再 HoldMulti，得到 extra=%d hold=%v", extraLeft, hold)
	}
	if batch != 2 {
		t.Fatalf("收竿后应有 2 条待入护，得到 %d", batch)
	}
	var logged int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM catch_log WHERE uid=?`, uid).Scan(&logged); err != nil {
		t.Fatal(err)
	}
	if logged != 3 {
		t.Fatalf("三条鱼都应写入钓获记录，得到 %d", logged)
	}

	// 14/5 入护：主鱼的选择带着另外两条一起走。
	if _, ok := s.fishingKeep(&Ctx{Sess: sess, Main: 14, Sub: 5, Tree: proto.Args524(proto.Guid(main.FishGUID)),
		ExpectResponse: true, Log: logging.Discard()}); !ok {
		t.Fatal("入护未正常返回")
	}
	var kept int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM keepnet_items WHERE uid=?`, uid).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 3 {
		t.Fatalf("三条鱼都应入护，得到 %d", kept)
	}
}

// 主鱼咬钩未刺中就收竿：本杆没有有效渔获，额外鱼必须一起脱落，
// 不能被 14/5 当成"入护"结掉。
func TestReelInWithoutHookDropsExtras(t *testing.T) {
	t.Parallel()
	s, db := newRigTestServer(t)
	sess := newProbeSess(s)
	uid := sess.Userid
	if _, err := db.EnsureProfile(t.Context(), uid); err != nil {
		t.Fatal(err)
	}
	st := s.stateFor(uid)
	st.mu.Lock()
	st.Pending = &RolledFish{FishGUID: strings.Repeat("d", 32), Species: "perch", WeightG: 400}
	st.Hooked = false
	st.Extra = []*RolledFish{{FishGUID: strings.Repeat("e", 32), Species: "perch", WeightG: 200}}
	st.mu.Unlock()

	if _, ok := s.fishingEndSettle(&Ctx{Sess: sess, CallID: 1, Main: 14, Sub: 4,
		ExpectResponse: true, Log: logging.Discard()}); !ok {
		t.Fatal("结算未正常返回")
	}
	st.mu.Lock()
	extraLeft, batch := len(st.Extra), len(st.Batch)
	st.mu.Unlock()
	if extraLeft != 0 || batch != 0 {
		t.Fatalf("未刺中收竿不应留下额外鱼，得到 extra=%d batch=%d", extraLeft, batch)
	}
	var logged int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM catch_log WHERE uid=?`, uid).Scan(&logged); err != nil {
		t.Fatal(err)
	}
	if logged != 0 {
		t.Fatalf("未刺中不应产生钓获记录，得到 %d", logged)
	}
}

// 额外鱼的实体必须等到主鱼刺鱼确认之后再派。
//
// 实机 2026-11-13 16:38：三条鱼同时在咬口阶段收到 14/14，客户端只对主鱼回了
// 14/11，另外两条一直停在 14/12 f0=6（咬口态待刺鱼），钩上没有模型、也不演，
// 玩家看到的永远是"钩上只有一条鱼"。客户端 Fish 状态机是 1(咬口)→2(搏鱼)，
// 只有刺鱼之后才把鱼挂到 f23 指定的钩上，所以派发时机要挪到刺鱼之后。
func TestExtrasDeferredUntilStrike(t *testing.T) {
	t.Parallel()
	s, db := newRigTestServer(t)
	// 本用例验证"要派实体时的时机"，实体派发默认是关的，这里显式打开。
	setMultiHookTunableForTest(t, s, "multi_hook_extra_entity", 1)
	sess, gc := newTunnelProbe(t, s)
	uid := sess.Userid
	if _, err := db.EnsureProfile(t.Context(), uid); err != nil {
		t.Fatal(err)
	}

	st := s.stateFor(uid)
	main := &RolledFish{FishGUID: strings.Repeat("a", 32), HookGUID: strings.Repeat("1", 32),
		Species: "perch", Grade: 1, WeightG: 800}
	extra := &RolledFish{FishGUID: strings.Repeat("b", 32), HookGUID: strings.Repeat("2", 32),
		Species: "perch", Grade: 1, WeightG: 300}
	st.mu.Lock()
	st.GearGuid = strings.Repeat("3", 32)
	st.CastActive = true
	st.InWater = true
	st.Pending = main
	st.Extra = []*RolledFish{extra}
	gear, gen := st.GearGuid, st.castGeneration
	st.mu.Unlock()

	if !s.extraPushDeferredOnStrike() {
		t.Fatal("默认应推迟到刺鱼后派发")
	}
	s.deferExtrasUntilStrike(st, gear)
	st.mu.Lock()
	held := len(st.Extra)
	st.mu.Unlock()
	if held != 1 {
		t.Fatalf("咬口阶段不该把额外鱼派掉，队列应剩 1 条，得到 %d", held)
	}

	// 刺鱼确认后补派：应收到 14/14，且这条鱼要留在队列里等 14/4 结算。
	s.pushExtrasAtStrike(sess, st, gear, 0, gen)
	env := awaitPushEnvelope(t, gc)
	if env.Main != 14 || env.Sub != 14 {
		t.Fatalf("应补派 14/14，得到 %d/%d", env.Main, env.Sub)
	}
	st.mu.Lock()
	left := len(st.Extra)
	st.mu.Unlock()
	if left != 1 {
		t.Fatalf("派发成功的额外鱼应留在队列等结算，得到 %d", left)
	}
}

// 刺鱼后补派被客户端拒收，也不能把渔获一起丢掉：标记 NoEntity 保留结算。
func TestRejectedExtraKeepsCatchWhenDeferred(t *testing.T) {
	t.Parallel()
	s, _ := newRigTestServer(t)
	extra := &RolledFish{FishGUID: strings.Repeat("c", 32), HookGUID: strings.Repeat("4", 32),
		Species: "perch", WeightG: 250}
	st := &FishState{GearGuid: strings.Repeat("5", 32), CastActive: true,
		Pending: &RolledFish{FishGUID: strings.Repeat("d", 32), HookGUID: strings.Repeat("6", 32)},
		Extra:   []*RolledFish{extra}}

	if !s.extraPushDeferredOnStrike() {
		t.Fatal("默认应推迟到刺鱼后派发")
	}
	st.mu.Lock()
	st.Extra[0].NoEntity = true
	st.mu.Unlock()

	// 客户端拒收后不再重派实体，但这条鱼必须还留在队列里等 14/4 结算。
	if got := len(st.Extra); got != 1 {
		t.Fatalf("拒收应保留队列，得到 %d", got)
	}
	if !st.Extra[0].NoEntity {
		t.Fatal("应标记 NoEntity，避免重派死循环")
	}
}

// 默认不给额外鱼派实体：客户端只有一个鱼模型（实测一杆两条 14/14 只 new 了
// 一个 Fish 实例，Update/FixedUpdate 也只命中那一条），派了既看不见，还会把
// 正在搏的主鱼挤出槽位、被当成脱钩丢掉。渔获统一留给 14/4 的 Batch 结算。
func TestExtrasNotPushedByDefault(t *testing.T) {
	t.Parallel()
	s, _ := newRigTestServer(t)
	sess, gc := newTunnelProbe(t, s)
	st := &FishState{GearGuid: strings.Repeat("7", 32), CastActive: true, Loc: "loc12_15",
		Pending: &RolledFish{FishGUID: strings.Repeat("a", 32), HookGUID: strings.Repeat("1", 32),
			Species: "perch", WeightG: 800},
		Extra: []*RolledFish{{FishGUID: strings.Repeat("b", 32), HookGUID: strings.Repeat("2", 32),
			Species: "perch", WeightG: 300}}}

	if s.extraEntityPushEnabled() {
		t.Fatal("额外鱼实体默认应关闭")
	}
	s.pushExtrasAtStrike(sess, st, st.GearGuid, 0, st.castGeneration)

	got := make(chan *proto.Envelope, 1)
	go func() {
		_, _, payload, err := gc.ReadRecord()
		if err != nil {
			return
		}
		if env, ok := proto.ParseEnvelope(payload); ok {
			got <- env
		}
	}()
	select {
	case env := <-got:
		t.Fatalf("默认不应给客户端推包，收到 %d/%d", env.Main, env.Sub)
	case <-time.After(300 * time.Millisecond):
	}

	st.mu.Lock()
	left := len(st.Extra)
	st.mu.Unlock()
	if left != 1 {
		t.Fatalf("不派实体也必须把鱼留在队列等 14/4 结算，得到 %d", left)
	}
}

// 多钩入护汇总播报：客户端只画一尾鱼、结算卡也只有一尾的位置，玩家至少
// 要知道这杆另外几尾进了护网、一共多重。
func TestMultiHookKeepChatText(t *testing.T) {
	t.Parallel()
	got := multiHookKeepChat([]*store.KeepnetEntry{{WeightG: 2794}, {WeightG: 2178}, {WeightG: 627}})
	if !strings.Contains(got, "另入护 3 尾") || !strings.Contains(got, "合计 5.60 kg") {
		t.Fatalf("播报文案不符，得到 %q", got)
	}
	if got := multiHookKeepChat(nil); !strings.Contains(got, "0 尾") || !strings.Contains(got, "0.00 kg") {
		t.Fatalf("空列表也要给出确定文案，得到 %q", got)
	}
}
