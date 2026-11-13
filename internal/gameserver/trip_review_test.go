package gameserver

// 审查回归测试（PR #171 审查意见对应项）：
//   - 1/14 凭证地图资格门（P1：无校验可绕过购票直入）
//   - 3/38 票价固定表（P1：缺失回退客户端报价，gold+1 金成交）
//   - 到期残留补偿回收 place_access（P2：删除失败后资格永久残留）
//   - tripLevelLocked 校验 remaining_sec（P2：残留 access 放行到期玩家）
//   - PlaceAccessList 只含买断（P2：房屋混入 f22）

import (
	"context"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func tripReviewCtx(srv *Server, uid int64, tree *proto.Value) *Ctx {
	sess := &Session{ID: 1, Userid: uid, Srv: srv, state: map[string]any{}, log: logging.Discard()}
	sess.startedAt = time.Now().Add(-time.Minute)
	sess.closed.Store(true)
	return &Ctx{Sess: sess, Main: 1, Sub: 14, Tree: tree, DecodeOK: true, Log: logging.Discard()}
}

// TestTravelLockedVoucherMapRejected 审查 P1 回归：无有效 trip/buyout/house
// 时 1/14 必须拒绝凭证地图，且不得改写位置。此前 hTravel 无资格校验，
// 构造请求即可直接切进钓鱼游地图。
func TestTravelLockedVoucherMapRejected(t *testing.T) {
	db := connectTestDB(t)
	const uid int64 = 1000091
	srv := newLocationTestServer(t, db)
	ctx := context.Background()

	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	target := "level_012_north_sea"
	if body, ok := hTravel(tripReviewCtx(srv, uid, proto.Args524(proto.StrVal(target)))); !ok || body != nil {
		t.Fatalf("无资格 1/14 应业务拒绝（nil body），实得 body=%v ok=%v", body != nil, ok)
	}
	if loc, err := db.GetPlayerLocation(ctx, uid); err == nil && loc != nil && loc.LevelKey == target {
		t.Fatal("拒绝后位置不应改为凭证地图")
	}
	if got := srv.currentLevel(uid); got == target {
		t.Fatal("拒绝后内存层级不应改为凭证地图")
	}

	// 购票后同一路径必须放行（先给足银币）。
	if _, err := db.SQL.Exec(`UPDATE profile SET silver=139500 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	buyCtx := &Ctx{Sess: tripReviewCtx(srv, uid, nil).Sess, Main: 3, Sub: 38,
		Tree: tripArgs(target, false, 139500), DecodeOK: true, Log: logging.Discard()}
	if body, handled := hPlaceTripTx(buyCtx); !handled || body == nil {
		t.Fatalf("购票失败: handled=%v body=%v", handled, body != nil)
	}
	if _, ok := hTravel(tripReviewCtx(srv, uid, proto.Args524(proto.StrVal(target)))); !ok {
		t.Fatal("有效钓鱼游后 1/14 应放行")
	}
	if loc, err := db.GetPlayerLocation(ctx, uid); err != nil || loc == nil || loc.LevelKey != target {
		t.Fatalf("放行后应切到凭证地图: loc=%+v err=%v", loc, err)
	}
}

// TestPlaceTripPriceStrict 审查 P1 回归：票价必须整段按固定票价表校验。
// 未收录地点、价格不符（银币/金币均含）一律拒绝，不扣款。
// 未实抓币种（票价 0 值）分支由 TestPlaceTripGoldPriceMissingRejected 单独覆盖。
func TestPlaceTripPriceStrict(t *testing.T) {
	srv, db := newEconomyTxTestServer(t)
	ctx := context.Background()
	const uid int64 = 3909
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET silver=999999, gold=999 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	silver0, gold0 := int64(999999), int64(999)

	buy := func(place string, gold bool, price uint64) bool {
		buyCtx := &Ctx{Sess: tripReviewCtx(srv, uid, nil).Sess, Main: 3, Sub: 38,
			Tree: tripArgs(place, gold, price), DecodeOK: true, Log: logging.Discard()}
		body, handled := hPlaceTripTx(buyCtx)
		return handled && body != nil
	}

	// 未收录地图（合法 key 但不在票价表）：此前按客户端报价成交。
	if buy("level_002_kirzah_river", false, 1) {
		t.Fatal("未收录地点应拒绝购买")
	}
	// 北海金币价已收录（2026-11-11 实抓 95）后，错报金币价仍必须拒绝：
	// gold=true price=1 此前被按 1 金币成交（审查 P1）。
	if buy("level_012_north_sea", true, 1) {
		t.Fatal("金币价不符应拒绝购买")
	}
	// 价格不符。
	if buy("level_018_moskitnoe", false, 29900) {
		t.Fatal("价格不符应拒绝购买")
	}
	// 余额/金币未被动过。
	var silver, gold int64
	if err := db.SQL.QueryRow(`SELECT silver, gold FROM profile WHERE uid=?`, uid).Scan(&silver, &gold); err != nil {
		t.Fatal(err)
	}
	if silver != silver0 || gold != gold0 {
		t.Fatalf("拒绝路径不应扣款: silver=%d gold=%d", silver, gold)
	}
	// 正确票价仍然成功。
	if !buy("level_018_moskitnoe", false, 29800) {
		t.Fatal("正确票价应购买成功")
	}
	remaining, _, _ := tripStateRow(t, srv, uid, "level_018_moskitnoe")
	if remaining <= 0 {
		t.Fatalf("购票未写计时状态: %.0f", remaining)
	}
}

// TestPlaceTripGoldPriceMissingRejected 审查 P2 回归（PR #436）：北海金币价
// 收录后票价表两个地点 Silver/Gold 均非零，"未实抓币种拒绝"（want<=0）分支
// 失去覆盖。临时清零铜湖金币价还原该分支：gold=true 必须拒绝且不扣款。
func TestPlaceTripGoldPriceMissingRejected(t *testing.T) {
	srv, db := newEconomyTxTestServer(t)
	ctx := context.Background()
	const uid int64 = 3915
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET silver=999999, gold=999 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	// 临时把铜湖金币价清零，模拟"金币价未实抓"的地点；测试结束恢复原表。
	// （本测试不标 t.Parallel：串行阶段改包级票价表不会与 Parallel 测试竞争。）
	orig := placeTripPrices["level_018_moskitnoe"]
	zeroed := orig
	zeroed.Gold = 0
	placeTripPrices["level_018_moskitnoe"] = zeroed
	t.Cleanup(func() { placeTripPrices["level_018_moskitnoe"] = orig })

	buyCtx := &Ctx{Sess: tripReviewCtx(srv, uid, nil).Sess, Main: 3, Sub: 38,
		Tree: tripArgs("level_018_moskitnoe", true, 45), DecodeOK: true, Log: logging.Discard()}
	if body, handled := hPlaceTripTx(buyCtx); !handled || body != nil {
		t.Fatalf("未实抓币种应业务拒绝（nil body）: handled=%v body=%v", handled, body != nil)
	}
	var silver, gold int64
	if err := db.SQL.QueryRow(`SELECT silver, gold FROM profile WHERE uid=?`, uid).Scan(&silver, &gold); err != nil {
		t.Fatal(err)
	}
	if silver != 999999 || gold != 999 {
		t.Fatalf("未实抓币种拒绝路径不应扣款: silver=%d gold=%d", silver, gold)
	}
}

// TestTripExpiryCompensationRevokesResidualAccess 审查 P2 回归：到期流程
// "状态清零成功、access 删除失败"的残留行，由补偿分支重试删除；
// tripLevelLocked 对残留行必须判为锁定。
func TestTripExpiryCompensationRevokesResidualAccess(t *testing.T) {
	srv, db := newEconomyTxTestServer(t)
	ctx0 := context.Background()
	const uid int64 = 3911
	place := "level_018_moskitnoe"
	if _, err := db.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}
	// 构造残留：状态已清零、access 行仍在。
	if err := db.UpsertTripState(ctx0, store.TripState{UID: uid, Place: place,
		RemainingSec: 0, LastConsumeTS: float64(time.Now().Add(-time.Hour).Unix()), Purchases: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO place_access(uid, place, kind, ts) VALUES(?,?,'trip',?)
		ON CONFLICT (uid, place, kind) DO NOTHING`, uid, place, float64(time.Now().Unix())); err != nil {
		t.Fatal(err)
	}

	if !srv.tripLevelLocked(ctx0, uid, place) {
		t.Fatal("残留 access（remaining=0）应判锁定")
	}

	sess := &Session{ID: 1, Userid: uid, Srv: srv, log: logging.Discard()}
	sess.startedAt = time.Now().Add(-time.Minute)
	sess.closed.Store(true)
	sess.consumeTripTimes(time.Now())
	if n := tripAccessCount(t, srv, uid, place); n != 0 {
		t.Fatalf("补偿分支应删除残留 access，实存 %d", n)
	}
	if !srv.tripLevelLocked(ctx0, uid, place) {
		t.Fatal("回收后仍应锁定")
	}
}

// TestTripLevelLockedRequiresTickingTrip 有效钓鱼游 = access 行 + 计时未清零；
// 买断与房屋照常放行。
func TestTripLevelLockedRequiresTickingTrip(t *testing.T) {
	srv, db := newEconomyTxTestServer(t)
	ctx0 := context.Background()
	const uid int64 = 3912
	place := "level_018_moskitnoe"
	if _, err := db.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}
	if !srv.tripLevelLocked(ctx0, uid, place) {
		t.Fatal("无任何访问应锁定")
	}
	// access 行但无计时状态（异常残留）：JOIN 不命中 → 锁定。
	if _, err := db.SQL.Exec(`INSERT INTO place_access(uid, place, kind, ts) VALUES(?,?,'trip',0)`, uid, place); err != nil {
		t.Fatal(err)
	}
	if !srv.tripLevelLocked(ctx0, uid, place) {
		t.Fatal("无计时状态的 access 行应锁定")
	}
	// 计时未清零 → 放行。
	if err := db.UpsertTripState(ctx0, store.TripState{UID: uid, Place: place,
		RemainingSec: 18000, LastConsumeTS: float64(time.Now().Unix()), Purchases: 1}); err != nil {
		t.Fatal(err)
	}
	if srv.tripLevelLocked(ctx0, uid, place) {
		t.Fatal("有效钓鱼游应放行")
	}
	// 清零 → 锁定（重演到期残留）。
	if _, err := db.SQL.Exec(`UPDATE trip_state SET remaining_sec=0 WHERE uid=? AND place=?`, uid, place); err != nil {
		t.Fatal(err)
	}
	if !srv.tripLevelLocked(ctx0, uid, place) {
		t.Fatal("计时清零后应锁定")
	}
	// 买断放行。
	if _, err := db.SQL.Exec(`DELETE FROM place_access WHERE uid=? AND place=? AND kind='trip'`, uid, place); err != nil {
		t.Fatal(err)
	}
	if err := db.PlaceAccessInsert(ctx0, uid, place, "buyout"); err != nil {
		t.Fatal(err)
	}
	if srv.tripLevelLocked(ctx0, uid, place) {
		t.Fatal("买断应放行")
	}
	// 房屋放行（place 存 level+"_house"）。
	if _, err := db.SQL.Exec(`DELETE FROM place_access WHERE uid=? AND place=? AND kind='buyout'`, uid, place); err != nil {
		t.Fatal(err)
	}
	if err := db.PlaceAccessInsert(ctx0, uid, place+"_house", "house"); err != nil {
		t.Fatal(err)
	}
	if srv.tripLevelLocked(ctx0, uid, place) {
		t.Fatal("房屋应放行")
	}
}

// TestPlaceAccessListBuyoutOnly 审查 P2 回归：PlaceAccessList 只返回买断，
// 房屋只走 HouseAccessList（f20）。此前房屋被换算成地图 key 混进 f22。
func TestPlaceAccessListBuyoutOnly(t *testing.T) {
	_, db := newEconomyTxTestServer(t)
	ctx := context.Background()
	const uid int64 = 3913
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.PlaceAccessInsert(ctx, uid, "level_018_moskitnoe", "buyout"); err != nil {
		t.Fatal(err)
	}
	if err := db.PlaceAccessInsert(ctx, uid, "housestore_003", "house"); err != nil {
		t.Fatal(err)
	}
	places, err := db.PlaceAccessList(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(places) != 1 || places[0] != "level_018_moskitnoe" {
		t.Fatalf("f22 应只含买断，实得 %v", places)
	}
	houses, err := db.HouseAccessList(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(houses) != 1 || houses[0] != "housestore_003" {
		t.Fatalf("f20 应含房屋，实得 %v", houses)
	}
}
