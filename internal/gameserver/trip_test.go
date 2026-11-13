package gameserver

import (
	"context"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func tripTestCtx(srv *Server, uid int64, tree *proto.Value, expect bool) *Ctx {
	sess := &Session{ID: 1, Userid: uid, Srv: srv, log: logging.Discard()}
	sess.startedAt = time.Now().Add(-time.Minute)
	sess.closed.Store(true)
	return &Ctx{Sess: sess, CallID: 1, Tree: tree, DecodeOK: true, ExpectResponse: expect, Log: logging.Discard()}
}

func tripArgs(place string, gold bool, price uint64) *proto.Value {
	return proto.Args524(proto.StrVal(place), proto.Bool(gold), proto.U64(price))
}

func tripStateRow(t *testing.T, srv *Server, uid int64, place string) (float64, float64, int64) {
	t.Helper()
	var remaining, lastConsume float64
	var purchases int64
	if err := srv.DB.SQL.QueryRow(
		`SELECT remaining_sec, last_consume_ts, purchases FROM trip_state WHERE uid=? AND place=?`,
		uid, place).Scan(&remaining, &lastConsume, &purchases); err != nil {
		t.Fatalf("读 trip_state: %v", err)
	}
	return remaining, lastConsume, purchases
}

func tripAccessCount(t *testing.T, srv *Server, uid int64, place string) int {
	t.Helper()
	var n int
	if err := srv.DB.SQL.QueryRow(
		`SELECT COUNT(*) FROM place_access WHERE uid=? AND place=? AND kind='trip'`,
		uid, place).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// profileVouchers 取档案 307 f23（钓鱼游数组）。
func profileVouchers(t *testing.T, srv *Server, uid int64) []*proto.Value {
	t.Helper()
	p, err := srv.BuildProfile307(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Fields) <= 23 {
		t.Fatalf("307 字段数不足: %d", len(p.Fields))
	}
	f23 := p.Fields[23].Val
	if f23 == nil || f23.Kind != proto.KindArgs {
		t.Fatalf("f23 不是 args: %v", f23)
	}
	return f23.Items
}

func TestPlaceTripSilverPurchase(t *testing.T) {
	srv, _ := newEconomyTxTestServer(t)
	ctx0 := context.Background()
	uid := int64(3901)
	if _, err := srv.DB.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.DB.SQL.Exec(`UPDATE profile SET silver=50000, gold=100 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	ctx := tripTestCtx(srv, uid, tripArgs("level_018_moskitnoe", false, 29800), false)
	body, handled := hPlaceTripTx(ctx)
	if !handled || body == nil {
		t.Fatalf("购票未返回档案: handled=%v body=%v", handled, body != nil)
	}
	if v, _, err := proto.DecodeValue(body, 0, srv.Table()); err != nil || v.Kind != proto.KindObj {
		t.Fatalf("响应不是可解析的档案对象: err=%v kind=%v", err, v.Kind)
	}
	var silver int64
	if err := srv.DB.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=?`, uid).Scan(&silver); err != nil {
		t.Fatal(err)
	}
	if silver != 50000-29800 {
		t.Fatalf("银币扣款错误: %d", silver)
	}
	remaining, lastConsume, purchases := tripStateRow(t, srv, uid, "level_018_moskitnoe")
	if purchases != 1 || remaining < 17995 || remaining > 18000 {
		t.Fatalf("钓鱼游状态错误: remaining=%.0f purchases=%d", remaining, purchases)
	}
	if lastConsume == 0 {
		t.Fatal("购买即启表（客户端唯一捕获时机）: last_consume_ts=0")
	}
	if n := tripAccessCount(t, srv, uid, "level_018_moskitnoe"); n != 1 {
		t.Fatalf("place_access(kind='trip') 未写入: %d", n)
	}
	// f23 钓鱼游数组：已启表（购买即计时）应带全额时长、acnjgklbdbd=1、started=1。
	vouchers := profileVouchers(t, srv, uid)
	if len(vouchers) != 1 {
		t.Fatalf("f23 应有 1 条钓鱼游: %d", len(vouchers))
	}
	v := vouchers[0]
	if v.Fields[0].Str == nil || *v.Fields[0].Str != "level_018_moskitnoe" {
		t.Fatalf("f23 地点键错误: %v", v.Fields[0].Str)
	}
	if u64Of(v.Fields[1].Num) != 1 || u64Of(v.Fields[2].Num) != uint64(tripGameMin(tripDurationSecDefault)) || u64Of(v.Fields[3].Num) != 1 {
		t.Fatalf("f23 字段错误: count=%v dur=%v started=%v",
			v.Fields[1].Num, v.Fields[2].Num, v.Fields[3].Num)
	}
}

func TestTripTimerStartsOnEntry(t *testing.T) {
	srv, _ := newEconomyTxTestServer(t)
	ctx0 := context.Background()
	uid := int64(3906)
	place := "level_018_moskitnoe"
	if _, err := srv.DB.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.DB.SQL.Exec(`UPDATE profile SET silver=50000 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	ctx := tripTestCtx(srv, uid, tripArgs(place, false, 29800), false)
	if body, handled := hPlaceTripTx(ctx); !handled || body == nil {
		t.Fatalf("购票失败: handled=%v body=%v", handled, body != nil)
	}
	sess := &Session{ID: 1, Userid: uid, Srv: srv, log: logging.Discard()}
	sess.closed.Store(true)
	sess.startedAt = time.Now().Add(-time.Minute)

	// 购买即启表：last_consume_ts=购票时刻，在线即消耗。
	if remaining, lastConsume, _ := tripStateRow(t, srv, uid, place); remaining != 18000 || lastConsume == 0 {
		t.Fatalf("购买即启表失败: remaining=%.0f last=%.0f", remaining, lastConsume)
	}
	// 回拨 10 秒模拟在线 10 秒后消费。
	if _, err := srv.DB.SQL.Exec(
		`UPDATE trip_state SET last_consume_ts=last_consume_ts-10 WHERE uid=? AND place=?`, uid, place); err != nil {
		t.Fatal(err)
	}
	if sess.consumeTripTimes(time.Now()) {
		t.Fatal("未到期不应回收")
	}
	remaining, lastConsume, _ := tripStateRow(t, srv, uid, place)
	if remaining != 17990 || lastConsume == 0 {
		t.Fatalf("启表后应开始消耗: remaining=%.0f last=%.0f", remaining, lastConsume)
	}
	// 启表后 f23：knpoijanngk=1；编码 f2=剩余游戏分钟、f4=W+3
	// （rf4_hook 注入实测：客户端 剩余 = f2 + min(f4, W+3) − W − 3）。
	pW, err := srv.BuildProfile307W(context.Background(), uid, worldTotalGameMin(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	vouchers := pW.Fields[23].Val.Items
	if len(vouchers) != 1 {
		t.Fatalf("f23 应有 1 条钓鱼游: %d", len(vouchers))
	}
	v := vouchers[0]
	if u64Of(v.Fields[3].Num) != 1 {
		t.Fatalf("启表后 f23.knpoijanngk 应为 1: %v", v.Fields[3].Num)
	}
	remainGameMin := int64(17990 * 24 / 60)
	wNow := worldTotalGameMin(time.Now())
	if u64Of(v.Fields[2].Num) != uint64(remainGameMin) {
		t.Fatalf("f23.f2 剩余错误: %v (期望 %d)", v.Fields[2].Num, remainGameMin)
	}
	if int64(u64Of(v.Fields[4].Num)) != wNow+3 {
		t.Fatalf("f23.f4 应为 W+3: %v (期望 %d)", v.Fields[4].Num, wNow+3)
	}
	// 客户端公式：剩余 = f2 + min(f4, W+3) − W − 3，应恰等于服务端剩余。
	shown := int64(u64Of(v.Fields[2].Num)) + min(int64(u64Of(v.Fields[4].Num)), wNow+3) - wNow - 3
	if shown != remainGameMin {
		t.Fatalf("客户端剩余换算错误: %d (期望 %d)", shown, remainGameMin)
	}
}

func TestPlaceTripGoldPurchase(t *testing.T) {
	srv, _ := newEconomyTxTestServer(t)
	ctx0 := context.Background()
	uid := int64(3902)
	if _, err := srv.DB.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.DB.SQL.Exec(`UPDATE profile SET silver=100, gold=100 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	ctx := tripTestCtx(srv, uid, tripArgs("level_018_moskitnoe", true, 45), false)
	if body, handled := hPlaceTripTx(ctx); !handled || body == nil {
		t.Fatalf("金币购票未返回档案: handled=%v body=%v", handled, body != nil)
	}
	var silver, gold int64
	if err := srv.DB.SQL.QueryRow(`SELECT silver, gold FROM profile WHERE uid=?`, uid).Scan(&silver, &gold); err != nil {
		t.Fatal(err)
	}
	if gold != 100-45 {
		t.Fatalf("金币扣款错误: %d", gold)
	}
	if silver != 100 {
		t.Fatalf("金币购票不应动银币: silver=%d", silver)
	}
}

// TestPlaceTripGoldPurchaseNorthSea 挪威海金币购票：2026-11-11 23:52 实抓
// gold=true price=95 曾因票价表缺金币价被拒（"该支付方式暂未开放"）。
// 回归钉死：收录价 95 必须购票成功并按 95 金币百分位扣款。
func TestPlaceTripGoldPurchaseNorthSea(t *testing.T) {
	srv, _ := newEconomyTxTestServer(t)
	ctx0 := context.Background()
	uid := int64(3914)
	if _, err := srv.DB.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.DB.SQL.Exec(`UPDATE profile SET silver=100, gold=100 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	ctx := tripTestCtx(srv, uid, tripArgs("level_012_north_sea", true, 95), false)
	if body, handled := hPlaceTripTx(ctx); !handled || body == nil {
		t.Fatalf("挪威海金币购票未返回档案: handled=%v body=%v", handled, body != nil)
	}
	var silver, gold int64
	if err := srv.DB.SQL.QueryRow(`SELECT silver, gold FROM profile WHERE uid=?`, uid).Scan(&silver, &gold); err != nil {
		t.Fatal(err)
	}
	if gold != 100-95 {
		t.Fatalf("挪威海金币扣款错误: %d", gold)
	}
	if silver != 100 {
		t.Fatalf("金币购票不应动银币: silver=%d", silver)
	}
	remaining, _, purchases := tripStateRow(t, srv, uid, "level_012_north_sea")
	if remaining != 18000 || purchases != 1 {
		t.Fatalf("挪威海购票状态错误: remaining=%.0f purchases=%d", remaining, purchases)
	}
}

func TestPlaceTripWrongPriceRejected(t *testing.T) {
	srv, _ := newEconomyTxTestServer(t)
	ctx0 := context.Background()
	uid := int64(3903)
	if _, err := srv.DB.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.DB.SQL.Exec(`UPDATE profile SET silver=50000 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	ctx := tripTestCtx(srv, uid, tripArgs("level_018_moskitnoe", false, 999), true)
	body, handled := hPlaceTripTx(ctx)
	if !handled || body != nil {
		t.Fatalf("改价购票应回业务失败: handled=%v body=%v", handled, body != nil)
	}
	var silver int64
	_ = srv.DB.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=?`, uid).Scan(&silver)
	if silver != 50000 {
		t.Fatalf("不应扣款: %d", silver)
	}
}

func TestPlaceTripPurchaseCap(t *testing.T) {
	srv, _ := newEconomyTxTestServer(t)
	ctx0 := context.Background()
	uid := int64(3904)
	place := "level_018_moskitnoe"
	if _, err := srv.DB.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.DB.SQL.Exec(`UPDATE profile SET silver=999999 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	if err := srv.DB.UpsertTripState(ctx0, store.TripState{UID: uid, Place: place, RemainingSec: 18000, LastConsumeTS: 1, Purchases: 3}); err != nil {
		t.Fatal(err)
	}
	ctx := tripTestCtx(srv, uid, tripArgs(place, false, 29800), true)
	if body, handled := hPlaceTripTx(ctx); !handled || body != nil {
		t.Fatalf("超限购票应回业务失败: handled=%v body=%v", handled, body != nil)
	}
	remaining, _, purchases := tripStateRow(t, srv, uid, place)
	if purchases != 3 || remaining != 18000 {
		t.Fatalf("超限后状态不应变化: remaining=%.0f purchases=%d", remaining, purchases)
	}
}

func TestConsumeTripTimesOfflinePauseAndExpiry(t *testing.T) {
	srv, _ := newEconomyTxTestServer(t)
	ctx0 := context.Background()
	uid := int64(3905)
	place := "level_018_moskitnoe"
	sess := &Session{ID: 1, Userid: uid, Srv: srv, log: logging.Discard()}
	sess.closed.Store(true)

	// 上次消费早于会话启动（离线间隔）：只对表不扣时长。
	sess.startedAt = time.Now().Add(-time.Minute)
	if err := srv.DB.UpsertTripState(ctx0, store.TripState{UID: uid, Place: place, RemainingSec: 18000,
		LastConsumeTS: float64(time.Now().Add(-8 * time.Hour).Unix()), Purchases: 1}); err != nil {
		t.Fatal(err)
	}
	if sess.consumeTripTimes(time.Now()) {
		t.Fatal("离线间隔不应触发回收")
	}
	remaining, lastConsume, purchases := tripStateRow(t, srv, uid, place)
	if remaining != 18000 || purchases != 1 || lastConsume < float64(sess.startedAt.Unix()) {
		t.Fatalf("离线对表失败: remaining=%.0f last=%.0f purchases=%d", remaining, lastConsume, purchases)
	}

	// 在线消耗 10 秒。
	if err := srv.DB.UpsertTripState(ctx0, store.TripState{UID: uid, Place: place, RemainingSec: 100,
		LastConsumeTS: float64(time.Now().Add(-10 * time.Second).Unix()), Purchases: 1}); err != nil {
		t.Fatal(err)
	}
	if sess.consumeTripTimes(time.Now()) {
		t.Fatal("未到期不应触发回收")
	}
	remaining, _, _ = tripStateRow(t, srv, uid, place)
	if remaining > 92 || remaining < 88 {
		t.Fatalf("在线扣减错误: %.0f", remaining)
	}

	// 到期：清零并回收 place_access。
	if err := srv.DB.UpsertTripState(ctx0, store.TripState{UID: uid, Place: place, RemainingSec: 5,
		LastConsumeTS: float64(time.Now().Add(-10 * time.Second).Unix()), Purchases: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.DB.SQL.Exec(`INSERT INTO place_access(uid, place, kind, ts) VALUES(?,?, 'trip', ?)
		ON CONFLICT (uid, place, kind) DO NOTHING`, uid, place, float64(time.Now().Unix())); err != nil {
		t.Fatal(err)
	}
	if !sess.consumeTripTimes(time.Now()) {
		t.Fatal("到期应触发回收")
	}
	remaining, _, _ = tripStateRow(t, srv, uid, place)
	if remaining != 0 {
		t.Fatalf("到期未清零: %.0f", remaining)
	}
	if n := tripAccessCount(t, srv, uid, place); n != 0 {
		t.Fatalf("到期未回收 place_access: %d", n)
	}
}

func TestTripDurationKVOverride(t *testing.T) {
	srv, _ := newEconomyTxTestServer(t)
	ctx0 := context.Background()
	uid := int64(3907)
	place := "level_018_moskitnoe"
	if _, err := srv.DB.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.DB.SQL.Exec(`UPDATE profile SET silver=50000 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	// kv 覆盖成 60 秒：购票发放 60s，f23 时长 60×24/60=24 游戏分钟。
	if err := srv.DB.KVSet(ctx0, tripDurationKVKey, "60"); err != nil {
		t.Fatal(err)
	}
	if srv.currentTripDurationSec(ctx0) != 60 {
		t.Fatal("kv 覆盖未生效")
	}
	ctx := tripTestCtx(srv, uid, tripArgs(place, false, 29800), false)
	if body, handled := hPlaceTripTx(ctx); !handled || body == nil {
		t.Fatalf("购票失败: handled=%v body=%v", handled, body != nil)
	}
	remaining, _, _ := tripStateRow(t, srv, uid, place)
	if remaining != 60 {
		t.Fatalf("覆盖时长未生效: %.0f", remaining)
	}
	vouchers := profileVouchers(t, srv, uid)
	if len(vouchers) != 1 || u64Of(vouchers[0].Fields[2].Num) != uint64(tripGameMin(60)) {
		t.Fatalf("f23 时长未按覆盖下发: %v", vouchers)
	}

	// 购买已启表；回拨 61 秒后消费：到期回收（不依赖 keepalive 多拍）。
	sess := &Session{ID: 1, Userid: uid, Srv: srv, log: logging.Discard()}
	sess.closed.Store(true)
	sess.startedAt = time.Now().Add(-5 * time.Minute)
	if _, err := srv.DB.SQL.Exec(
		`UPDATE trip_state SET last_consume_ts=last_consume_ts-61 WHERE uid=? AND place=?`, uid, place); err != nil {
		t.Fatal(err)
	}
	if !sess.consumeTripTimes(time.Now()) {
		t.Fatal("短时长到期应触发回收")
	}
	if n := tripAccessCount(t, srv, uid, place); n != 0 {
		t.Fatalf("到期未回收 place_access: %d", n)
	}
}

func TestTripSessionClockCalibration(t *testing.T) {
	srv, _ := newEconomyTxTestServer(t)
	ctx0 := context.Background()
	uid := int64(3908)
	place := "level_018_moskitnoe"
	if _, err := srv.DB.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}
	// 已启表、剩余 17990 秒；会话已进行 10 现实分钟（W=240 游戏分钟）。
	if err := srv.DB.UpsertTripState(ctx0, store.TripState{UID: uid, Place: place, RemainingSec: 17990,
		LastConsumeTS: float64(time.Now().Add(-10 * time.Second).Unix()), Purchases: 1}); err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: 1, Userid: uid, Srv: srv, log: logging.Discard()}
	sess.closed.Store(true)
	sess.startedAt = time.Now().Add(-10 * time.Minute)
	// W = 世界纪元总分钟（2/2 FILETIME 校准坐标），与会话无关。
	wNow := worldTotalGameMin(time.Now())
	expectW := int64(float64(time.Now().Unix()-worldDayEpochUnix) * gameMinutesPerRealSecond)
	if wNow != expectW {
		t.Fatalf("世界时钟坐标错误: %d (期望 %d)", wNow, expectW)
	}
	p, err := srv.BuildProfile307W(ctx0, uid, wNow)
	if err != nil {
		t.Fatal(err)
	}
	f23 := p.Fields[23].Val
	if len(f23.Items) != 1 {
		t.Fatalf("f23 应有 1 条: %d", len(f23.Items))
	}
	// 编码 f2=剩余游戏分钟、f4=W+3（客户端公式恰输出 f2，随 W 推进递减）。
	f2 := int64(u64Of(f23.Items[0].Fields[2].Num))
	if f2 != int64(17990*24/60) {
		t.Fatalf("f23.f2 剩余错误: %d (期望 %d)", f2, int64(17990*24/60))
	}
	f4 := int64(u64Of(f23.Items[0].Fields[4].Num))
	if f4 != wNow+3 {
		t.Fatalf("f23.f4 应为 W+3: %d (期望 %d)", f4, wNow+3)
	}
	// 客户端会话推进 5 游戏分钟：剩余 = f2 − 5，仍为正且在递减。
	shown := f2 + min(f4, wNow+5+3) - (wNow + 5) - 3
	if shown <= 0 {
		t.Fatalf("校准后会话内剩余为负: %d", shown)
	}
}

func TestTripPlaceDobMarked(t *testing.T) {
	srv, _ := newEconomyTxTestServer(t)
	ctx := context.Background()
	const uid = int64(3920)
	if _, err := srv.DB.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	dobF1 := func(key string) uint64 {
		t.Helper()
		body, err := srv.buildMapDobBody(uid, key, time.Now().Unix())
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		v, _, err := proto.DecodeValue(body, 0, srv.Table())
		if err != nil || v.Kind != proto.KindObj {
			t.Fatalf("%s dob 解码失败: %v kind=%v", key, err, v.Kind)
		}
		return u64Of(v.Fields[1].Num)
	}
	// 无活跃钓鱼游（房契/买断进入）：凭证图必须保持 0——f1=1 会让客户端以
	// 钓鱼游会话语义加载地图而 f23 无条目 → 黑屏（2026-11-06 实测）。
	if got := dobF1("level_018_moskitnoe"); got != 0 {
		t.Fatalf("无票时凭证图 dob.f1 应为 0: %d", got)
	}
	if got := dobF1("level_001_lake"); got != 0 {
		t.Fatalf("免费图 dob.f1 应为 0: %d", got)
	}
	// 有活跃钓鱼游：凭证图打标（客户端抛竿门走 f23 通行校验的前提）。
	if _, err := srv.DB.SQL.Exec(`INSERT INTO trip_state(uid, place, remaining_sec, last_consume_ts, purchases, updated_ts)
		VALUES(?, 'level_018_moskitnoe', 3600, 0, 1, 0)`, uid); err != nil {
		t.Fatal(err)
	}
	if got := dobF1("level_018_moskitnoe"); got != 1 {
		t.Fatalf("有票时凭证图 dob.f1 应为 1: %d", got)
	}
	// 其他地点仍不打标（钓鱼游只对本图生效）。
	if got := dobF1("level_012_north_sea"); got != 0 {
		t.Fatalf("未购票凭证图 dob.f1 应为 0: %d", got)
	}
}
