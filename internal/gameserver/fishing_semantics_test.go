package gameserver

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func TestCastSetsCastTsAndHookedSchedulePreservesIt(t *testing.T) {
	srv, _ := newDetachTestServer(t)
	sess := newProbeSess(srv)
	st := srv.stateFor(sess.Userid)

	srv.scheduleBite(sess, st)
	st.mu.Lock()
	castTs := st.CastTs
	hooked := st.Hooked
	if st.biteTimer != nil {
		st.biteTimer.Stop()
	}
	st.mu.Unlock()
	if castTs.IsZero() {
		t.Fatal("14/2 scheduleBite 必须写入 CastTs")
	}
	if hooked {
		t.Fatal("新抛竿应 Hooked=false")
	}

	oldCast := castTs.Add(-3 * time.Second)
	st.mu.Lock()
	st.CastTs = oldCast
	st.CastActive = true
	st.Resolved = false
	st.Pending = nil
	st.Hooked = true
	st.mu.Unlock()
	srv.scheduleBiteForHooked(sess, st)
	st.mu.Lock()
	if !st.Hooked {
		t.Fatal("14/11 补判必须保留 Hooked=true")
	}
	if !st.CastTs.Equal(oldCast) {
		t.Fatalf("14/11 补判不能重置 CastTs: got=%v want=%v", st.CastTs, oldCast)
	}
	if st.biteTimer != nil {
		st.biteTimer.Stop()
	}
	st.mu.Unlock()
}

func TestFishingTickRepairKeepsPendingFish(t *testing.T) {
	srv, _ := newDetachTestServer(t)
	sess := newProbeSess(srv)
	st := srv.stateFor(sess.Userid)
	fish := &RolledFish{FishGUID: "11111111111111111111111111111111", Species: "m_scorpius"}

	st.mu.Lock()
	st.CastActive = true
	st.InWater = false
	st.NeedBite = true
	st.Pending = fish
	st.PushSent = true
	st.Hooked = true
	st.Resolved = false
	const generation = uint64(7)
	st.castGeneration = generation
	st.mu.Unlock()

	fields := make([]proto.Field, 7)
	for i := range fields {
		fields[i] = proto.Field{Op: "U32", Num: ptrU64(0)}
	}
	fields[5] = proto.Field{Op: "U32", Num: ptrU64(0x400)}
	fields[6] = proto.Field{Op: "STR", Str: strPtr("loc1_02")}
	tree := proto.Args524(proto.Obj("lobemkloehp", 0, fields...))
	if _, ok := hFishingStep(&Ctx{Sess: sess, Main: 14, Sub: 7, Tree: tree, DecodeOK: true, Log: logging.Discard()}); !ok {
		t.Fatal("14/7 未处理")
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	if st.Pending != fish || !st.PushSent || st.biteTimer != nil || st.castGeneration != generation {
		t.Fatalf("补排覆盖当前鱼: pending_same=%v pushed=%v timer=%v generation=%d->%d",
			st.Pending == fish, st.PushSent, st.biteTimer != nil, generation, st.castGeneration)
	}
	if !st.InWater || st.NeedBite {
		t.Fatalf("水中状态未正确更新: in_water=%v need_bite=%v", st.InWater, st.NeedBite)
	}
}

func TestFindObjSecondU32ForCastSpot(t *testing.T) {
	obj := proto.Obj("olieodeoaof", 0,
		proto.Field{Op: "U32", Num: ptrU64(111)},
		proto.Field{Op: "U32", Num: ptrU64(222)},
		proto.Field{Op: "STR", Str: strPtr("loc1_02")},
	)
	tree := proto.Args524(obj)
	if got := findObjNthU32(tree, "olieodeoaof", 1); got != 222 {
		t.Fatalf("spot=%d，期望第二个 U32=222", got)
	}
}

func TestFindCastActionByte(t *testing.T) {
	obj := proto.Obj("olieodeoaof", 0,
		proto.Field{Op: "U32", Num: ptrU64(uint64(proto.F32Bits(18.5)))},
		proto.Field{Op: "BYTE", Num: ptrU64(2)},
		proto.Field{Op: "BYTE", Num: ptrU64(7)},
	)
	tree := proto.Args524(obj)
	if got := findObjNthByte(tree, "olieodeoaof", 0); got != 2 {
		t.Fatalf("action=%d，期望首个 BYTE=2", got)
	}
	if got := findObjNthByte(tree, "olieodeoaof", 1); got != 7 {
		t.Fatalf("action=%d，期望第二个 BYTE=7", got)
	}
}

func TestFishingTickRecomputesSettledAndLocation(t *testing.T) {
	srv, _ := newDetachTestServer(t)
	sess := newProbeSess(srv)
	makeTick := func(anim uint64, loc string) *proto.Value {
		fields := make([]proto.Field, 7)
		fields[5] = proto.Field{Op: "U32", Num: ptrU64(anim)}
		fields[6] = proto.Field{Op: "STR", Str: strPtr(loc)}
		return proto.Args524(proto.Obj("lobemkloehp", 0, fields...))
	}
	st := srv.stateFor(sess.Userid)
	st.mu.Lock()
	st.CastActive = true
	st.mu.Unlock()
	if _, ok := hFishingStep(&Ctx{Sess: sess, Main: 14, Sub: 7,
		Tree: makeTick(0x200, "loc12_01"), DecodeOK: true, Log: logging.Discard()}); !ok {
		t.Fatal("14/7 未处理")
	}
	st.mu.Lock()
	if !st.RigSettled || !st.InWater || st.Loc != "loc12_01" {
		st.mu.Unlock()
		t.Fatalf("首帧 settled=%v in_water=%v loc=%q", st.RigSettled, st.InWater, st.Loc)
	}
	st.mu.Unlock()

	if _, ok := hFishingStep(&Ctx{Sess: sess, Main: 14, Sub: 7,
		Tree: makeTick(0, "loc12_02"), DecodeOK: true, Log: logging.Discard()}); !ok {
		t.Fatal("14/7 第二帧未处理")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.RigSettled {
		t.Fatal("动画位消失后 RigSettled 必须复位")
	}
	if st.Loc != "loc12_02" {
		t.Fatalf("14/7 loc=%q，期望 loc12_02", st.Loc)
	}
}

func TestKeepnetVersionTicksStrictlyIncreaseAndKeepMap(t *testing.T) {
	e := &store.KeepnetEntry{EchoGUID: "11111111111111111111111111111111", Species: "c.roach", WeightG: 100, LengthM: 0.2, Cat: 1, KeepLevel: "level_019_american_pond"}
	a := buildKeepnet25(e, "level_001_lake", 0, "")
	time.Sleep(2 * time.Millisecond)
	b := buildKeepnet25(e, "level_001_lake", 0, "")
	fa, fb := a.Field(22), b.Field(22)
	if fa.U64() == 0 || fb.U64() <= fa.U64() {
		t.Fatalf("25.f22 必须非 0 且严格递增: %d -> %d", fa.U64(), fb.U64())
	}
	f10 := a.Field(10)
	if got := f10.StrOr(); got != "level_019_american_pond" {
		t.Fatalf("25.f10=%q，期望捕获地图", got)
	}
}

func TestCatchLogWrittenOnceOnKeep(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	f := &RolledFish{FishGUID: "22222222222222222222222222222222", Species: "c.roach", Grade: 1, WeightG: 186, LengthM: 0.1981, LocLevel: "level_019_american_pond"}
	if r := srv.settleCatchXP(ctx, uid, f, 1); r == nil {
		t.Fatal("settleCatchXP 返回 nil")
	}
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.Pending = f
	st.Loc = "loc19_03"
	st.Spot = 123
	st.Bait = "worm_01"
	st.mu.Unlock()
	sess := newProbeSess(srv)
	tree := rigRequestTree(t, proto.Guid("33333333333333333333333333333333"), proto.Guid(f.FishGUID))
	body, ok := srv.fishingKeep(&Ctx{Sess: sess, Main: 14, Sub: 5, Tree: tree, DecodeOK: true, Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatal("14/5 未处理")
	}
	var n int
	if err := db.SQL.QueryRowContext(ctx, `SELECT COUNT(*) FROM catch_log WHERE uid=?`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("入护后 catch_log 行数=%d，期望 1", n)
	}
	var source string
	if err := db.SQL.QueryRowContext(ctx, `SELECT source FROM catch_log WHERE uid=?`, uid).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if source != "keep" {
		t.Fatalf("catch_log source=%q，期望 keep", source)
	}
}

// TestCatchLogReplayedSettleAfterKeptStaysSingleRow 钉住重放口径：
// 唯一索引 uq_catch_log_fish_identity 只到 (uid, source, catch_guid)，14/5 入护把行
// 从 'catch' 抬成 'keep' 后 'catch' 槽位空出——若无别的守卫，同一 GUID 再走一次
// 14/4 结算会插进第二条 source='catch' 行，图鉴与 321 统计翻倍。实际防线是
// catch-settlement:<uid>:<guid> 的 kv 回执（与结算同事务写入）：重放直接走回执返回，
// 不再开事务。这里把「结算 → 入护 → 重放 14/4」整条链钉死。
func TestCatchLogReplayedSettleAfterKeptStaysSingleRow(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	f := &RolledFish{FishGUID: "55555555555555555555555555555555", Species: "c.roach", Grade: 1, WeightG: 186, LengthM: 0.1981, LocLevel: "level_019_american_pond"}
	if r := srv.settleCatchXP(ctx, uid, f, 1); r == nil || r.replayed {
		t.Fatal("首次结算应真实入账")
	}
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.Pending = f
	st.Loc = "loc19_03"
	st.Spot = 123
	st.Bait = "worm_01"
	st.mu.Unlock()
	sess := newProbeSess(srv)
	tree := rigRequestTree(t, proto.Guid("66666666666666666666666666666666"), proto.Guid(f.FishGUID))
	if body, ok := srv.fishingKeep(&Ctx{Sess: sess, Main: 14, Sub: 5, Tree: tree, DecodeOK: true, Log: logging.Discard()}); !ok || len(body) == 0 {
		t.Fatal("14/5 未处理")
	}
	var xpBefore int64
	if err := db.SQL.QueryRowContext(ctx, `SELECT xp FROM profile WHERE uid=?`, uid).Scan(&xpBefore); err != nil {
		t.Fatal(err)
	}
	r := srv.settleCatchXP(ctx, uid, f, 1)
	if r == nil {
		t.Fatal("重放结算返回 nil")
	}
	if !r.replayed {
		t.Fatal("重放结算应命中 kv 回执并标记 replayed")
	}
	var n int
	var source string
	if err := db.SQL.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MAX(source),'') FROM catch_log WHERE uid=?`, uid).Scan(&n, &source); err != nil {
		t.Fatal(err)
	}
	if n != 1 || source != "keep" {
		t.Fatalf("重放后 catch_log 行数=%d source=%q，期望 1 行 keep", n, source)
	}
	var xpAfter int64
	if err := db.SQL.QueryRowContext(ctx, `SELECT xp FROM profile WHERE uid=?`, uid).Scan(&xpAfter); err != nil {
		t.Fatal(err)
	}
	if xpAfter != xpBefore {
		t.Fatalf("重放结算不得重复入账: xp %d -> %d", xpBefore, xpAfter)
	}
}

func TestFishingLeftBumpsEscapes(t *testing.T) {
	srv, db := newDetachTestServer(t)
	const uid = int64(1000001)
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.Pending = &RolledFish{FishGUID: "44444444444444444444444444444444", Species: "c.roach"}
	st.CastActive, st.Hooked = true, true
	st.mu.Unlock()
	obj := proto.Obj("lhicjpmglbo", 0, proto.Field{Op: "U32", Num: ptrU64(0)})
	tree := proto.Args524(obj)
	sess := newProbeSess(srv)
	if _, ok := hFishingLeft(&Ctx{Sess: sess, Main: 14, Sub: 12, Tree: tree, DecodeOK: true, Log: logging.Discard()}); !ok {
		t.Fatal("14/12 未处理")
	}
	var n int
	if err := db.SQL.QueryRow(`SELECT value FROM player_counters WHERE uid=? AND key='escapes'`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("escapes=%d，期望1", n)
	}
}

func TestFishingEndSettleUnhookedDoesNotCountEscape(t *testing.T) {
	srv, db := newDetachTestServer(t)
	const uid = int64(1000001)
	sess := newProbeSess(srv)
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.Pending = &RolledFish{
		FishGUID: "77777777777777777777777777777777", Species: "c.roach", Grade: 1, WeightG: 400,
	}
	st.CastActive = true
	st.Hooked = false
	st.Resolved = false
	st.mu.Unlock()

	body, ok := srv.fishingEndSettle(&Ctx{Sess: sess, Main: 14, Sub: 4, DecodeOK: true, Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatalf("14/4 未处理: ok=%v bytes=%d", ok, len(body))
	}

	var escapes int
	if err := db.SQL.QueryRow(`SELECT COALESCE((SELECT value FROM player_counters WHERE uid=? AND key='escapes'),0)`, uid).Scan(&escapes); err != nil {
		t.Fatal(err)
	}
	if escapes != 0 {
		t.Fatalf("14/4 咬钩未中钩不应计为跑鱼，escapes=%d", escapes)
	}
}

func TestFishingLeftStatus8KeepsInWaterCastAndSchedulesNextFish(t *testing.T) {
	srv, db := newDetachTestServer(t)
	const uid = int64(1000001)
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.Pending = &RolledFish{FishGUID: "55555555555555555555555555555555", Species: "c.roach"}
	st.CastActive = true
	st.InWater = true
	st.PushSent = true
	st.Hooked = false
	st.Resolved = false
	st.mu.Unlock()

	obj := proto.Obj("lhicjpmglbo", 0, proto.Field{Op: "U32", Num: ptrU64(8)})
	tree := proto.Args524(obj)
	sess := newProbeSess(srv)
	if _, ok := hFishingLeft(&Ctx{Sess: sess, Main: 14, Sub: 12, Tree: tree, DecodeOK: true, Log: logging.Discard()}); !ok {
		t.Fatal("14/12 未处理")
	}

	st.mu.Lock()
	if !st.CastActive || !st.InWater {
		t.Fatalf("鱼自然离开后钓组应仍在水中: CastActive=%v InWater=%v", st.CastActive, st.InWater)
	}
	if st.Resolved || st.Pending != nil || st.PushSent || st.Hooked {
		t.Fatalf("当前鱼状态未清干净: Resolved=%v Pending=%+v PushSent=%v Hooked=%v",
			st.Resolved, st.Pending, st.PushSent, st.Hooked)
	}
	if st.biteTimer == nil || st.BiteAt.IsZero() {
		t.Fatal("鱼自然离开后没有安排下一次判鱼")
	}
	st.biteTimer.Stop()
	st.biteTimer = nil
	st.mu.Unlock()

	var n int
	err := db.SQL.QueryRow(`SELECT value FROM player_counters WHERE uid=? AND key='escapes'`, uid).Scan(&n)
	if err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("自然离场不应计为跑鱼，escapes=%d", n)
	}
}

func TestFishingLeftStatus1KeepsInWaterCastAndSchedulesNextFish(t *testing.T) {
	srv, db := newDetachTestServer(t)
	const uid = int64(1000001)
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.Pending = &RolledFish{FishGUID: "66666666666666666666666666666666", Species: "c.roach"}
	st.CastActive = true
	st.InWater = true
	st.PushSent = true
	st.Hooked = false
	st.Resolved = false
	st.mu.Unlock()

	obj := proto.Obj("lhicjpmglbo", 0, proto.Field{Op: "U32", Num: ptrU64(1)})
	tree := proto.Args524(obj)
	sess := newProbeSess(srv)
	if _, ok := hFishingLeft(&Ctx{Sess: sess, Main: 14, Sub: 12, Tree: tree, DecodeOK: true, Log: logging.Discard()}); !ok {
		t.Fatal("14/12 未处理")
	}

	st.mu.Lock()
	if !st.CastActive || !st.InWater {
		t.Fatalf("鱼未中钩离场后钓组应仍在水中: CastActive=%v InWater=%v", st.CastActive, st.InWater)
	}
	if st.Resolved || st.Pending != nil || st.PushSent || st.Hooked {
		t.Fatalf("当前鱼状态未清干净: Resolved=%v Pending=%+v PushSent=%v Hooked=%v",
			st.Resolved, st.Pending, st.PushSent, st.Hooked)
	}
	if st.biteTimer == nil {
		t.Fatal("鱼未中钩离场后没有重新安排下一尾")
	}
	st.biteTimer.Stop()
	st.biteTimer = nil
	st.mu.Unlock()

	var escapes int
	if err := db.SQL.QueryRow(`SELECT value FROM player_counters WHERE uid=? AND key='escapes'`, uid).Scan(&escapes); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	if escapes != 0 {
		t.Fatalf("鱼未中钩离场不应计为跑鱼: escapes=%d", escapes)
	}
}

func TestFishingLeftLureEscapeReschedulesBiteDuringRetrieve(t *testing.T) {
	srv, _ := newDetachTestServer(t)
	const uid = int64(1000001)
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.GearGuid = "33333333333333333333333333333333"
	st.RigType = rigTypeLure
	st.CastActive = true
	st.InWater = true
	st.Pending = &RolledFish{FishGUID: "88888888888888888888888888888888", Species: "c.roach"}
	st.PushSent = true
	st.Hooked = true
	st.Resolved = false
	st.mu.Unlock()

	sess := newProbeSess(srv)
	srv.sessions[sess.ID] = sess
	t.Cleanup(func() { delete(srv.sessions, sess.ID) })

	tree := proto.Args524(proto.Obj("lhicjpmglbo", 0, proto.Field{Op: "U32", Num: ptrU64(8)}))
	if _, ok := hFishingLeft(&Ctx{Sess: sess, Main: 14, Sub: 12, Tree: tree, DecodeOK: true, Log: logging.Discard()}); !ok {
		t.Fatal("14/12 未处理")
	}

	st.mu.Lock()
	if !st.CastActive || !st.InWater {
		st.mu.Unlock()
		t.Fatalf("路亚脱钩后钓组仍在水中，本杆应继续: CastActive=%v InWater=%v", st.CastActive, st.InWater)
	}
	if st.Pending != nil || st.Hooked || st.Resolved || st.PushSent {
		st.mu.Unlock()
		t.Fatalf("脱钩后当前鱼状态未清干净: Pending=%+v Hooked=%v Resolved=%v PushSent=%v",
			st.Pending, st.Hooked, st.Resolved, st.PushSent)
	}
	if st.biteTimer == nil || st.BiteAt.IsZero() {
		st.mu.Unlock()
		t.Fatal("路亚脱钩后收线途中应重新排咬钩")
	}
	st.biteTimer.Stop()
	st.biteTimer = nil
	st.mu.Unlock()

	// 重连恢复同样可以补排，收线未结束不应丢窗口。
	srv.resumeFishingState(st)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.biteTimer == nil {
		t.Fatal("重连恢复后应继续排咬钩")
	}
	st.biteTimer.Stop()
	st.biteTimer = nil
}

func TestFishingLeftLureMissStillSchedulesNextFish(t *testing.T) {
	srv, _ := newDetachTestServer(t)
	const uid = int64(1000001)
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.RigType = rigTypeLure
	st.CastActive = true
	st.InWater = true
	st.Pending = &RolledFish{FishGUID: "99999999999999999999999999999999", Species: "c.roach"}
	st.PushSent = true
	st.Hooked = false
	st.Resolved = false
	st.mu.Unlock()

	sess := newProbeSess(srv)
	tree := proto.Args524(proto.Obj("lhicjpmglbo", 0, proto.Field{Op: "U32", Num: ptrU64(1)}))
	if _, ok := hFishingLeft(&Ctx{Sess: sess, Main: 14, Sub: 12, Tree: tree, DecodeOK: true, Log: logging.Discard()}); !ok {
		t.Fatal("14/12 未处理")
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.CastActive || !st.InWater {
		t.Fatalf("未中钩的鱼离场后本杆应继续: CastActive=%v InWater=%v", st.CastActive, st.InWater)
	}
	if st.biteTimer == nil || st.BiteAt.IsZero() {
		t.Fatal("未中钩的鱼离场后应继续排下一尾")
	}
	st.biteTimer.Stop()
	st.biteTimer = nil
}

func TestMailInfoShapeAndGUID(t *testing.T) {
	srv, _ := newDetachTestServer(t)
	sess := newProbeSess(srv)
	body, ok := hMailInfo(&Ctx{Sess: sess, Main: 19, Sub: 5, DecodeOK: true, Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatal("19/5 未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatalf("19/5 响应不可解码: %v", err)
	}
	if dec.Class != "pbndmldbfai" || dec.TypeID != 133 {
		t.Fatalf("19/5 顶层=%s/%d，期望 pbndmldbfai/133", dec.Class, dec.TypeID)
	}
	if g := mailIDToGUID(1); len(g) != 32 || g != "00000000000000000000000000000001" {
		t.Fatalf("mailIDToGUID(1)=%q", g)
	}
}

func strPtr(s string) *string { return &s }

func TestCastLandingPositionFromObject(t *testing.T) {
	fields := make([]proto.Field, 8)
	for i := range fields {
		fields[i] = proto.Field{Op: "U32", Num: ptrU64(0)}
	}
	fields[6] = proto.Field{Op: "U32", Num: ptrU64(uint64(proto.F32Bits(123.5)))}
	fields[7] = proto.Field{Op: "U32", Num: ptrU64(uint64(proto.F32Bits(-45.25)))}
	obj := proto.Obj("olieodeoaof", 0, fields...)
	x, z, ok := castLandingPosition(proto.Args524(obj))
	if !ok || x != 123.5 || z != -45.25 {
		t.Fatalf("landing=(%v,%v,%v), want=(123.5,-45.25,true)", x, z, ok)
	}
}

func TestFishingLeftRigNotReadyRepushesSameFish(t *testing.T) {
	for _, state := range []uint32{3, 5} {
		t.Run(fmt.Sprintf("status%d", state), func(t *testing.T) {
			srv, _ := newDetachTestServer(t)
			const uid = int64(1000001)
			st := srv.stateFor(uid)
			fish := &RolledFish{FishGUID: "77777777777777777777777777777777", Species: "c.roach"}
			st.mu.Lock()
			st.Pending = fish
			st.CastActive = true
			st.InWater = true
			st.PushSent = true
			st.Hooked = false
			st.Resolved = false
			st.mu.Unlock()

			obj := proto.Obj("lhicjpmglbo", 0, proto.Field{Op: "U32", Num: ptrU64(uint64(state))})
			tree := proto.Args524(obj)
			sess := newProbeSess(srv)
			if _, ok := hFishingLeft(&Ctx{Sess: sess, Main: 14, Sub: 12, Tree: tree, DecodeOK: true, Log: logging.Discard()}); !ok {
				t.Fatal("14/12 未处理")
			}
			st.mu.Lock()
			defer st.mu.Unlock()
			if st.Pending != fish || st.Pending.FishGUID != fish.FishGUID {
				t.Fatal("钓组未就绪时应保留并重推当前鱼")
			}
			if st.PushSent || st.Hooked || st.Resolved || !st.CastActive {
				t.Fatalf("重推状态错误: PushSent=%v Hooked=%v Resolved=%v CastActive=%v", st.PushSent, st.Hooked, st.Resolved, st.CastActive)
			}
		})
	}
}

func TestFishingLeftRigNotReadyStopsAfterRepushCap(t *testing.T) {
	srv, _ := newDetachTestServer(t)
	const uid = int64(1000001)
	st := srv.stateFor(uid)
	fish := &RolledFish{FishGUID: "88888888888888888888888888888888", Species: "c.roach"}
	st.mu.Lock()
	st.Pending = fish
	st.CastActive = true
	st.InWater = true
	st.PushSent = true
	st.Hooked = false
	st.Resolved = false
	st.PendingRepush = maxPendingRepush
	st.mu.Unlock()

	obj := proto.Obj("lhicjpmglbo", 0, proto.Field{Op: "U32", Num: ptrU64(3)})
	tree := proto.Args524(obj)
	sess := newProbeSess(srv)
	if _, ok := hFishingLeft(&Ctx{Sess: sess, Main: 14, Sub: 12, Tree: tree, DecodeOK: true, Log: logging.Discard()}); !ok {
		t.Fatal("14/12 未处理")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.Pending != nil {
		t.Fatal("超过重推上限后应放弃当前鱼，避免无限 14/14")
	}
	if st.pendingTimer != nil {
		t.Fatal("超过重推上限后不应再挂重推定时器")
	}
	if !st.Resolved {
		t.Fatal("放弃后应标记本轮已结束，等玩家重新抛竿")
	}
}

func TestFishingLeftStatus2KeepsInWaterCastAndSchedulesNextFish(t *testing.T) {
	srv, _ := newDetachTestServer(t)
	const uid = int64(1000001)
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.Pending = &RolledFish{FishGUID: "22222222222222222222222222222222", Species: "c.roach"}
	st.CastActive = true
	st.InWater = true
	st.PushSent = true
	st.Hooked = false
	st.Resolved = false
	st.mu.Unlock()

	obj := proto.Obj("lhicjpmglbo", 0, proto.Field{Op: "U32", Num: ptrU64(2)})
	tree := proto.Args524(obj)
	sess := newProbeSess(srv)
	if _, ok := hFishingLeft(&Ctx{Sess: sess, Main: 14, Sub: 12, Tree: tree, DecodeOK: true, Log: logging.Discard()}); !ok {
		t.Fatal("14/12 未处理")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.CastActive || !st.InWater || st.Resolved || st.Pending != nil || st.PushSent || st.Hooked {
		t.Fatalf("鱼离场状态错误: CastActive=%v InWater=%v Resolved=%v Pending=%+v PushSent=%v Hooked=%v",
			st.CastActive, st.InWater, st.Resolved, st.Pending, st.PushSent, st.Hooked)
	}
	if st.biteTimer == nil || st.BiteAt.IsZero() {
		t.Fatal("鱼离场后没有重新安排下一尾")
	}
	st.biteTimer.Stop()
	st.biteTimer = nil
}

// makeStepTree 构造 14/7 假帧（f5=动画位，f6=地点），与官方客户端帧同构。
func makeStepTree(anim uint64, loc string) *proto.Value {
	fields := make([]proto.Field, 7)
	for i := range fields {
		fields[i] = proto.Field{Op: "U32", Num: ptrU64(0)}
	}
	fields[5] = proto.Field{Op: "U32", Num: ptrU64(anim)}
	fields[6] = proto.Field{Op: "STR", Str: strPtr(loc)}
	return proto.Args524(proto.Obj("lobemkloehp", 0, fields...))
}

// TestFishingStepInWaterEvidenceByRigFamily 覆盖按钓组族的入水证据（官方抓包
// 2026-11-13，docs/analysis-inwater-evidence-packet-verification-2026-11-13.md）：
// 浮钓接受顺流 0x40 与下沉 0x80000（含组合 0x2040），其余钓法维持基础位族，
// 0x0 永远不是入水证据（挡岸上抛竿）。
func TestFishingStepInWaterEvidenceByRigFamily(t *testing.T) {
	srv, _ := newDetachTestServer(t)
	sess := newProbeSess(srv)

	// cast 把竿摆到“已抛竿、NeedBite 挂起”的状态，模拟定时器到点时未入水。
	cast := func(rigType int64) {
		t.Helper()
		st := srv.stateFor(sess.Userid)
		st.mu.Lock()
		st.RigType = rigType
		st.CastActive = true
		st.InWater = false
		st.NeedBite = true
		st.mu.Unlock()
	}
	step := func(anim uint64) {
		t.Helper()
		if _, ok := hFishingStep(&Ctx{Sess: sess, Main: 14, Sub: 7,
			Tree: makeStepTree(anim, "loc1_02"), DecodeOK: true, Log: logging.Discard()}); !ok {
			t.Fatalf("14/7 未处理 anim=0x%X", anim)
		}
	}
	state := func() (inWater, needBite, armed bool) {
		st := srv.stateFor(sess.Userid)
		st.mu.Lock()
		defer st.mu.Unlock()
		inWater, needBite, armed = st.InWater, st.NeedBite, st.biteTimer != nil
		if st.biteTimer != nil {
			st.biteTimer.Stop()
			st.biteTimer = nil
		}
		return
	}

	cast(1) // rig_FloatSimple
	step(0x40)
	if inWater, needBite, armed := state(); !inWater || needBite || !armed {
		t.Fatalf("浮钓 0x40 顺流应入水并补排: in_water=%v need_bite=%v armed=%v", inWater, needBite, armed)
	}

	cast(2) // rig_FloatBolognese
	step(0x2040)
	if inWater, needBite, armed := state(); !inWater || needBite || !armed {
		t.Fatalf("浮钓 0x2040（MotionOnBottom|StreamMoving）应入水: in_water=%v need_bite=%v armed=%v", inWater, needBite, armed)
	}

	cast(3) // rig_FloatSlider
	step(0x80000)
	if inWater, needBite, armed := state(); !inWater || needBite || !armed {
		t.Fatalf("浮钓 0x80000 下沉应入水: in_water=%v need_bite=%v armed=%v", inWater, needBite, armed)
	}

	cast(1)
	step(0)
	if inWater, needBite, _ := state(); inWater || !needBite {
		t.Fatalf("浮钓 0x0（岸上/无动作）不得入水: in_water=%v need_bite=%v", inWater, needBite)
	}

	cast(12) // rig_BottomSimple
	step(0x40)
	if inWater, needBite, _ := state(); inWater || !needBite {
		t.Fatalf("底钓单独 0x40 不是入水证据: in_water=%v need_bite=%v", inWater, needBite)
	}

	cast(50) // rig_MarinePilker
	step(0x880000)
	if inWater, needBite, armed := state(); !inWater || needBite || !armed {
		t.Fatalf("海钓 0x880000 下沉应入水（26 秒下沉期 14/3 触底才来）: in_water=%v need_bite=%v armed=%v", inWater, needBite, armed)
	}

	cast(50)
	step(0x800000)
	if inWater, _, _ := state(); !inWater {
		t.Fatalf("海钓 0x800000 底上移动应入水")
	}

	cast(50)
	step(0x400000)
	if inWater, _, _ := state(); !inWater {
		t.Fatalf("海钓 0x400000 底层内移动应入水")
	}

	cast(5) // rig_Lure
	step(0)
	if inWater, needBite, _ := state(); inWater || !needBite {
		t.Fatalf("路亚 0x0（线杯开静置）不得凭 f5 入水，由 14/3 兜底: in_water=%v need_bite=%v", inWater, needBite)
	}

	cast(5) // rig_Lure
	step(0x400)
	if inWater, needBite, armed := state(); !inWater || needBite || !armed {
		t.Fatalf("路亚 0x400 收线应照旧入水: in_water=%v need_bite=%v armed=%v", inWater, needBite, armed)
	}
}

// TestFishingFallLatchesInWaterAndReschedules：14/3 是客户端“钩体已在水中”
// 的一次性上报（岸上落点不发）。路亚线杯开自由下沉只有 f5=0x0，入水证据由
// 14/3 兜底并补排咬钩；打窝竿不锁存——它不钓鱼，历史上靠“永不入水”挡住
// 发鱼定时器，不能因 14/3 开始出鱼。
func TestFishingFallLatchesInWaterAndReschedules(t *testing.T) {
	srv, _ := newDetachTestServer(t)
	sess := newProbeSess(srv)

	st := srv.stateFor(sess.Userid)
	st.mu.Lock()
	st.RigType = 5 // 路亚：线杯开自由下沉，f5 保持 0x0
	st.CastActive = true
	st.InWater = false
	st.NeedBite = true
	st.mu.Unlock()

	if _, ok := hFishingFall(&Ctx{Sess: sess, Main: 14, Sub: 3, Log: logging.Discard()}); !ok {
		t.Fatal("14/3 未处理")
	}
	st.mu.Lock()
	inWater, needBite, armed := st.InWater, st.NeedBite, st.biteTimer != nil
	if st.biteTimer != nil {
		st.biteTimer.Stop()
		st.biteTimer = nil
	}
	st.mu.Unlock()
	if !inWater || needBite || !armed {
		t.Fatalf("14/3 应锁存入水并补排: in_water=%v need_bite=%v armed=%v", inWater, needBite, armed)
	}

	// 打窝竿：14/3 只扣窝料，不锁存入水（保持既有“永不入水”护法）。
	st.mu.Lock()
	st.RigType = rigTypeSpod
	st.CastActive = true
	st.InWater = false
	st.NeedBite = true
	st.mu.Unlock()
	if _, ok := hFishingFall(&Ctx{Sess: sess, Main: 14, Sub: 3, Log: logging.Discard()}); !ok {
		t.Fatal("14/3（打窝竿）未处理")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.InWater || !st.NeedBite {
		t.Fatalf("打窝竿 14/3 不得锁存入水: in_water=%v need_bite=%v", st.InWater, st.NeedBite)
	}
}
