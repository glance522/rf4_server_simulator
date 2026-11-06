package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func newLocationTestServer(t *testing.T, db *store.DB) *Server {
	t.Helper()
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "location-test"}}, logging.Discard(), nil)
	srv.SetDB(db)
	return srv
}

func locationTestSession(srv *Server, uid int64) *Session {
	return &Session{ID: 1, Userid: uid, Srv: srv, state: map[string]any{}, log: logging.Discard()}
}

func locationTestCtx(sess *Session, tree *proto.Value) *Ctx {
	return &Ctx{Sess: sess, Main: 1, Sub: 13, Tree: tree, DecodeOK: true, Log: logging.Discard()}
}

func decodeLocator(t *testing.T, srv *Server, body []byte) (level, scene string) {
	t.Helper()
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatalf("1/13 响应解码失败: %v", err)
	}
	if dec.Kind != proto.KindObj || dec.Class != "cgbaffjpbkg" {
		t.Fatalf("1/13 响应应为 206 cgbaffjpbkg，实得 %s/%s", dec.Kind, dec.Class)
	}
	dobField := dec.Field(0)
	dob := dobField.Tree()
	if dob == nil || dob.FieldCount() == 0 {
		t.Fatal("1/13 响应缺少 dob")
	}
	keyField := dob.Field(0)
	sceneField := dec.Field(1)
	return keyField.StrOr(), sceneField.StrOr()
}

// 首次玩家没有位置记录时必须回出生图；1/14 成功后，同进程重连和
// 进程重启式新 Server 都要从 profile 恢复上次地图。
func TestPlayerLocationFirstLoginTravelReconnectAndRestart(t *testing.T) {
	db := connectTestDB(t)
	const uid int64 = 1000001
	srv := newLocationTestServer(t, db)

	body, ok := hEnterMap(locationTestCtx(locationTestSession(srv, uid), proto.Args524()))
	if !ok {
		t.Fatal("首次 1/13 未处理")
	}
	level, scene := decodeLocator(t, srv, body)
	if level != spawnLevelKey || scene != spawnScene {
		t.Fatalf("首次登录应回出生图，实得 %s/%s", level, scene)
	}

	// 1/13 若携带合法地图 key，也应把这次成功进入原子写回。
	reqLevel := "level_003_viiveri_lake"
	body, ok = hEnterMap(locationTestCtx(locationTestSession(srv, uid), proto.Args524(proto.StrVal(reqLevel))))
	if !ok {
		t.Fatal("带地图 key 的 1/13 未处理")
	}
	level, _ = decodeLocator(t, srv, body)
	if level != reqLevel {
		t.Fatalf("1/13 切图后=%q，期望 %q", level, reqLevel)
	}
	if loc, err := db.GetPlayerLocation(context.Background(), uid); err != nil || loc == nil || loc.LevelKey != reqLevel {
		t.Fatalf("1/13 切图未落库: loc=%+v err=%v", loc, err)
	}

	target := "level_019_american_pond"
	travelCtx := locationTestCtx(locationTestSession(srv, uid),
		proto.Args524(proto.StrVal(target), proto.U32(42), proto.U32(99)))
	travelCtx.Main, travelCtx.Sub = 1, 14
	if _, ok := hTravel(travelCtx); !ok {
		t.Fatal("1/14 未处理")
	}
	loc, err := db.GetPlayerLocation(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	if loc == nil || loc.LevelKey != target || loc.Spot != 42 {
		t.Fatalf("1/14 未持久化目标位置: %+v", loc)
	}
	if got := srv.currentLevel(uid); got != target {
		t.Fatalf("同进程 currentLevel=%q，期望 %q", got, target)
	}

	// 同进程新连接：1/13 空请求必须返回上次地图。
	body, ok = hEnterMap(locationTestCtx(locationTestSession(srv, uid), proto.Args524()))
	if !ok {
		t.Fatal("重连 1/13 未处理")
	}
	level, _ = decodeLocator(t, srv, body)
	if level != target {
		t.Fatalf("同进程重连回 %q，期望 %q", level, target)
	}

	// 模拟进程重启：全新 Server 无 levelMap，只能从 DB 恢复。
	restarted := newLocationTestServer(t, db)
	body, ok = hEnterMap(locationTestCtx(locationTestSession(restarted, uid), proto.Args524()))
	if !ok {
		t.Fatal("重启后 1/13 未处理")
	}
	level, _ = decodeLocator(t, restarted, body)
	if level != target {
		t.Fatalf("重启后登录回 %q，期望 %q", level, target)
	}
	if got := restarted.currentLevel(uid); got != target {
		t.Fatalf("重启后 currentLevel=%q，期望 %q", got, target)
	}
}

// 切图必须按目标地图所需银币扣款，同图重入与免费地图不重复收费。
func TestTravelDeductsMapSilver(t *testing.T) {
	db := connectTestDB(t)
	const uid int64 = 1000002
	srv := newLocationTestServer(t, db)
	if _, err := db.EnsureProfile(context.Background(), uid); err != nil {
		t.Fatal(err)
	}
	const start int64 = 10000
	if _, err := db.SQL.Exec(`UPDATE profile SET silver=? WHERE uid=?`, start, uid); err != nil {
		t.Fatal(err)
	}
	target := "level_004_torfyanoe_ozero"
	cost := placeTravelCost(target)
	if cost <= 0 {
		t.Fatalf("测试地图未配置银币价: %s", target)
	}
	travel := func(level string) {
		t.Helper()
		ctx := locationTestCtx(locationTestSession(srv, uid),
			proto.Args524(proto.StrVal(level), proto.U32(7)))
		ctx.Main, ctx.Sub = 1, 14
		if _, ok := hTravel(ctx); !ok {
			t.Fatalf("1/14 切图未处理: %s", level)
		}
	}
	silverOf := func() int64 {
		t.Helper()
		var v int64
		if err := db.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=?`, uid).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	travel(target)
	if got := silverOf(); got != start-cost {
		t.Fatalf("切图未扣银币: silver=%d want=%d", got, start-cost)
	}
	var txnCount int
	if err := db.SQL.QueryRow(
		`SELECT COUNT(*) FROM txn WHERE uid=? AND note='place_travel' AND silver_delta=?`,
		uid, -cost).Scan(&txnCount); err != nil {
		t.Fatal(err)
	}
	if txnCount != 1 {
		t.Fatalf("切图流水应为 1 条，实得 %d", txnCount)
	}
	if loc, err := db.GetPlayerLocation(context.Background(), uid); err != nil ||
		loc == nil || loc.LevelKey != target {
		t.Fatalf("扣费后未切到目标地图: loc=%+v err=%v", loc, err)
	}

	travel(target)                    // 同图重入，客户端重新定位不算新旅行
	travel("level_019_american_pond") // 免费地图
	if got := silverOf(); got != start-cost {
		t.Fatalf("同图/免费地图不应再扣费: silver=%d want=%d", got, start-cost)
	}
}

// 银币不足时回业务失败，既不扣款也不切图。
func TestTravelInsufficientSilverStaysOnMap(t *testing.T) {
	db := connectTestDB(t)
	const uid int64 = 1000003
	srv := newLocationTestServer(t, db)
	if _, err := db.EnsureProfile(context.Background(), uid); err != nil {
		t.Fatal(err)
	}
	target := "level_012_north_sea"
	cost := placeTravelCost(target)
	if cost <= 0 {
		t.Fatalf("测试地图未配置银币价: %s", target)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET silver=? WHERE uid=?`, cost-1, uid); err != nil {
		t.Fatal(err)
	}
	ctx := locationTestCtx(locationTestSession(srv, uid), proto.Args524(proto.StrVal(target)))
	ctx.Main, ctx.Sub = 1, 14
	body, ok := hTravel(ctx)
	if !ok || len(body) != 0 {
		t.Fatalf("银币不足应回业务失败: ok=%v body=%x", ok, body)
	}
	var silver int64
	if err := db.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=?`, uid).Scan(&silver); err != nil {
		t.Fatal(err)
	}
	if silver != cost-1 {
		t.Fatalf("余额不足仍扣款: silver=%d want=%d", silver, cost-1)
	}
	if got := srv.currentLevel(uid); got == target {
		t.Fatal("余额不足不应切换地图")
	}
}

// 1/13 带地图 key 同样算一次切图，也要扣该地图的银币。
func TestEnterMapWithKeyDeductsSilver(t *testing.T) {
	db := connectTestDB(t)
	const uid int64 = 1000005
	srv := newLocationTestServer(t, db)
	if _, err := db.EnsureProfile(context.Background(), uid); err != nil {
		t.Fatal(err)
	}
	target := "level_010_belaya"
	cost := placeTravelCost(target)
	if cost <= 0 {
		t.Fatalf("测试地图未配置银币价: %s", target)
	}
	const start int64 = 5000
	if _, err := db.SQL.Exec(`UPDATE profile SET silver=? WHERE uid=?`, start, uid); err != nil {
		t.Fatal(err)
	}
	ctx := locationTestCtx(locationTestSession(srv, uid), proto.Args524(proto.StrVal(target)))
	if body, ok := hEnterMap(ctx); !ok || len(body) == 0 {
		t.Fatalf("1/13 未处理: ok=%v", ok)
	}
	var silver int64
	if err := db.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=?`, uid).Scan(&silver); err != nil {
		t.Fatal(err)
	}
	if silver != start-cost {
		t.Fatalf("1/13 切图未扣银币: silver=%d want=%d", silver, start-cost)
	}
	// 同一地图的登录恢复（不带 key）不得再次扣费。
	ctx = locationTestCtx(locationTestSession(srv, uid), proto.Args524())
	if _, ok := hEnterMap(ctx); !ok {
		t.Fatal("1/13 登录恢复未处理")
	}
	if err := db.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=?`, uid).Scan(&silver); err != nil {
		t.Fatal(err)
	}
	if silver != start-cost {
		t.Fatalf("登录恢复不应再扣费: silver=%d want=%d", silver, start-cost)
	}
}

// 已金币买断（无限通行）的地图切图不再收银币。
func TestTravelBuyoutPlaceIsFree(t *testing.T) {
	db := connectTestDB(t)
	const uid int64 = 1000004
	srv := newLocationTestServer(t, db)
	if _, err := db.EnsureProfile(context.Background(), uid); err != nil {
		t.Fatal(err)
	}
	target := "level_006_ladoga"
	cost := placeTravelCost(target)
	if cost <= 0 {
		t.Fatalf("测试地图未配置银币价: %s", target)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET silver=? WHERE uid=?`, cost+100, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.PlaceAccessInsert(context.Background(), uid, target, "buyout"); err != nil {
		t.Fatal(err)
	}
	ctx := locationTestCtx(locationTestSession(srv, uid), proto.Args524(proto.StrVal(target)))
	ctx.Main, ctx.Sub = 1, 14
	if _, ok := hTravel(ctx); !ok {
		t.Fatal("1/14 未处理")
	}
	var silver int64
	if err := db.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=?`, uid).Scan(&silver); err != nil {
		t.Fatal(err)
	}
	if silver != cost+100 {
		t.Fatalf("已买断地图不应收旅费: silver=%d want=%d", silver, cost+100)
	}
}

// 数据库中的地图 key 无效时，1/13 必须返回出生图并修正记录。
func TestPlayerLocationInvalidMapFallsBackToSpawn(t *testing.T) {
	db := connectTestDB(t)
	const uid int64 = 1000001
	if err := db.SetPlayerLocation(context.Background(), uid, "level_does_not_exist", "bad_scene", 7); err != nil {
		t.Fatal(err)
	}
	srv := newLocationTestServer(t, db)
	body, ok := hEnterMap(locationTestCtx(locationTestSession(srv, uid), proto.Args524()))
	if !ok {
		t.Fatal("1/13 未处理")
	}
	level, scene := decodeLocator(t, srv, body)
	if level != spawnLevelKey || scene != spawnScene {
		t.Fatalf("无效地图应回出生图，实得 %s/%s", level, scene)
	}
	loc, err := db.GetPlayerLocation(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	if loc == nil || loc.LevelKey != spawnLevelKey {
		t.Fatalf("无效地图未被修正: %+v", loc)
	}
}
