package gameserver

import (
	"context"
	"net"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/tunnel"
)

// enableLootBox 打开总开关（默认关闭，测试里显式开启）。
func enableLootBox(t *testing.T, srv *Server, db *store.DB) {
	t.Helper()
	if _, err := db.SQL.Exec(`INSERT INTO tunables(key,value) VALUES('loot_box_enabled','1')
		ON CONFLICT(key) DO UPDATE SET value='1'`); err != nil {
		t.Fatalf("开启礼包: %v", err)
	}
	srv.tunMu.Lock()
	srv.tunCache = nil
	srv.tunMu.Unlock()
}

func waitLootItemCount(t *testing.T, db *store.DB, uid int64, itemID string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var got int
		if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM item WHERE uid=? AND item_id=?`, uid, itemID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("礼包物品数量=%d，期望 %d", got, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// 礼包：概率 1.0 时必开，奖励落库并返回物品 ID。
func TestLootBoxAlwaysOpensWhenChanceOne(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	sess := newProbeSess(srv)
	enableLootBox(t, srv, db)

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.SQL.Exec(q, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	mustExec(`INSERT INTO item_def(numeric_id,item_id,category,kind,brand,attrs_json) VALUES(900001,'loot_worm',27,'bait','','{}')`)
	mustExec(`INSERT INTO item_catalog(item_id,name,name_zh,kind,numeric_id) VALUES('loot_worm','Loot Worm','礼包红虫','bait',900001)`)
	mustExec(`INSERT INTO shop_item(store_id,item_id,silver,gold,stock) VALUES('fishing_store_01','loot_worm',100,0,10)`)
	mustExec(`INSERT INTO kv(k,v) VALUES('loot_box_defs','[{"id":"test_box","name":"测试礼包","chance":1.0}]')`)

	got := srv.rollLootBox(ctx, sess, &RolledFish{Species: "c.roach"})
	if got != "loot_worm" {
		t.Fatalf("应开出 loot_worm，实得 %q", got)
	}
	var n int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM item WHERE uid=? AND item_id='loot_worm'`, sess.Userid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("奖励应落库 1 件，实得 %d", n)
	}
}

// 配置了 rewards 时按权重抽取，不再去商店随机。
func TestLootBoxUsesConfiguredRewards(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	sess := newProbeSess(srv)
	enableLootBox(t, srv, db)

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.SQL.Exec(q, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	// 用计件类（27 饵料）验证数量：非计件物品每件一个实例，数量恒为 1。
	mustExec(`INSERT INTO item_def(numeric_id,item_id,category,kind,brand,attrs_json) VALUES(900002,'loot_bait',27,'bait','','{}')`)
	mustExec(`INSERT INTO item_catalog(item_id,name,name_zh,kind,numeric_id) VALUES('loot_bait','Loot Bait','礼包饵料','bait',900002)`)
	mustExec(`INSERT INTO kv(k,v) VALUES('loot_box_defs','[{"id":"b","name":"礼包","chance":1.0,"rewards":[{"item_id":"loot_bait","qty":2,"weight":100}]}]')`)

	got := srv.rollLootBox(ctx, sess, nil)
	if got != "loot_bait" {
		t.Fatalf("应开出 loot_bait，实得 %q", got)
	}
	var qty int64
	if err := db.SQL.QueryRow(`SELECT qty FROM item WHERE uid=? AND item_id='loot_bait'`, sess.Userid).Scan(&qty); err != nil {
		t.Fatal(err)
	}
	if qty != 2 {
		t.Fatalf("数量应为 2，实得 %d", qty)
	}
}

// 关闭开关后不触发。
func TestLootBoxDisabledByTunable(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	sess := newProbeSess(srv)
	if _, err := db.SQL.Exec(`INSERT INTO tunables(key,value) VALUES('loot_box_enabled','0') ON CONFLICT(key) DO UPDATE SET value='0'`); err != nil {
		t.Fatal(err)
	}
	srv.tunMu.Lock()
	srv.tunCache = nil
	srv.tunMu.Unlock()
	if got := srv.rollLootBox(ctx, sess, nil); got != "" {
		t.Fatalf("关闭后不应开箱，实得 %q", got)
	}
}

// 商店随机池必须排除套盒/礼物袋，以及没有权威 wire 映射、无法正确物化的类别。
func TestLootBoxShopPickFiltersUnsupportedCategories(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.SQL.Exec(q, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	mustExec(`DELETE FROM shop_item`)
	mustExec(`INSERT INTO item_def(numeric_id,item_id,category,kind,brand,attrs_json) VALUES(900003,'loot_only_bundle',55,'bundle','','{}')`)
	mustExec(`INSERT INTO item_def(numeric_id,item_id,category,kind,brand,attrs_json) VALUES(900004,'loot_only_gift',73,'gift','','{}')`)
	mustExec(`INSERT INTO item_def(numeric_id,item_id,category,kind,brand,attrs_json) VALUES(900005,'loot_only_wallet',43,'wallet','','{}')`)
	mustExec(`INSERT INTO item_def(numeric_id,item_id,category,kind,brand,attrs_json) VALUES(900006,'loot_allowed',27,'bait','','{}')`)
	mustExec(`INSERT INTO shop_item(store_id,item_id,silver,gold,stock) VALUES
		('s','loot_only_bundle',10,0,5),
		('s','loot_only_gift',10,0,5),
		('s','loot_only_wallet',10,0,5),
		('s','loot_allowed',10,0,5)`)
	for i := 0; i < 60; i++ {
		id, _, _ := srv.pickRandomShopItem(ctx)
		if id != "loot_allowed" {
			t.Fatalf("随机池只能返回可物化商品，实得 %q", id)
		}
	}
}

// 配置奖励与载体也必须走同一套资格校验。
func TestLootBoxConfiguredRewardsFilterUnsupportedItems(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	sess := newProbeSess(srv)
	enableLootBox(t, srv, db)

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.SQL.Exec(q, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	mustExec(`INSERT INTO item_def(numeric_id,item_id,category,kind,brand,attrs_json) VALUES(900007,'loot_bad_gift',73,'gift','','{}')`)
	mustExec(`INSERT INTO item_def(numeric_id,item_id,category,kind,brand,attrs_json) VALUES(900008,'loot_bad_wallet',43,'wallet','','{}')`)
	mustExec(`INSERT INTO item_def(numeric_id,item_id,category,kind,brand,attrs_json) VALUES(900009,'loot_good_bait',27,'bait','','{}')`)
	mustExec(`INSERT INTO kv(k,v) VALUES('loot_box_defs','[{"id":"b","name":"礼包","chance":1.0,"carrier_item":"loot_bad_gift","rewards":[{"item_id":"loot_bad_gift","qty":1,"weight":100},{"item_id":"loot_bad_wallet","qty":1,"weight":100},{"item_id":"loot_good_bait","qty":1,"weight":1}]}]')`)

	if got := srv.rollLootBox(ctx, sess, nil); got != "loot_good_bait" {
		t.Fatalf("应跳过不可发放配置并开出 loot_good_bait，实得 %q", got)
	}
	for _, itemID := range []string{"loot_bad_gift", "loot_bad_wallet"} {
		var n int
		if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM item WHERE uid=? AND item_id=?`, sess.Userid, itemID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("不可发放物品 %s 不应落库，实得 %d 件", itemID, n)
		}
	}
}

// 14/5 重放不得重复发礼包。
func TestLootBoxAwardsOnceOnKeptReplay(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	enableLootBox(t, srv, db)

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.SQL.Exec(q, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	mustExec(`INSERT INTO item_def(numeric_id,item_id,category,kind,brand,attrs_json) VALUES(900010,'loot_replay',27,'bait','','{}')`)
	mustExec(`INSERT INTO kv(k,v) VALUES('loot_box_defs','[{"id":"b","name":"礼包","chance":1.0,"rewards":[{"item_id":"loot_replay","qty":1,"weight":100}]}]')`)

	fish := &RolledFish{
		FishGUID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Species: "c.roach", Grade: 1,
		WeightG: 186, LengthM: 0.1981, LocLevel: "level_019_american_pond",
	}
	if r := srv.settleCatchXP(ctx, uid, fish, 1); r == nil {
		t.Fatal("settleCatchXP 返回 nil")
	}
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.Pending = fish
	st.Loc, st.Spot, st.Bait = "loc19_03", 123, "worm_01"
	st.mu.Unlock()
	sess := newProbeSess(srv)
	tree := rigRequestTree(t, proto.Guid("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"), proto.Guid(fish.FishGUID))
	keepCtx := func() *Ctx {
		return &Ctx{Sess: sess, Main: 14, Sub: 5, Tree: tree, DecodeOK: true, Log: logging.Discard()}
	}

	if body, ok := srv.fishingKeep(keepCtx()); !ok || len(body) == 0 {
		t.Fatal("首次 14/5 未处理")
	}
	waitLootItemCount(t, db, uid, "loot_replay", 1)

	if body, ok := srv.fishingKeep(keepCtx()); !ok || len(body) == 0 {
		t.Fatal("重复 14/5 未处理")
	}
	time.Sleep(150 * time.Millisecond)

	var got int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM item WHERE uid=? AND item_id='loot_replay'`, uid).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("重复 14/5 后礼包物品应为 1 件，实得 %d", got)
	}
}

// TestLootBoxPushesEveryCouponRow 复审 P2-6：礼包开出多张券时，每一张券行
// 都要推一条 4/22 背包增量。旧实现只推 AddToStack 返回的最后一行。
func TestLootBoxPushesEveryCouponRow(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	if _, err := db.SQL.Exec(`INSERT OR IGNORE INTO item_def(numeric_id,item_id,category,kind,brand,attrs_json)
		VALUES(4825,'coupon_chuchelo',74,'coupon','','{}')`); err != nil {
		t.Fatal(err)
	}
	def, err := db.GetItemDefByItemID(ctx, couponChucheloItemID)
	if err != nil || def == nil {
		t.Skip("静态库无券定义")
	}

	serverNet, peerNet := net.Pipe()
	t.Cleanup(func() { _ = serverNet.Close(); _ = peerNet.Close() })
	_ = serverNet.SetDeadline(time.Now().Add(10 * time.Second))
	_ = peerNet.SetDeadline(time.Now().Add(10 * time.Second))
	ready := make(chan *tunnel.Conn, 1)
	go func() {
		conn, err := tunnel.HandshakeServer(serverNet, srv.cfg.Tunnel.PSK)
		if err == nil {
			ready <- conn
		}
	}()
	peer, err := tunnel.HandshakeClient(peerNet, srv.cfg.Tunnel.PSK)
	if err != nil {
		t.Fatal(err)
	}
	var conn *tunnel.Conn
	select {
	case conn = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("handshake 超时")
	}
	uid := int64(2100040)
	sess := newSession(srv, 1, "loot-coupon-push", uid, conn)
	srv.mu.Lock()
	srv.sessions[1] = sess
	srv.mu.Unlock()

	// 后台持续读帧：net.Pipe 是同步写，没有读端会把 Push 卡死。
	frames := make(chan *proto.Envelope, 64)
	go func() {
		for {
			_, _, payload, err := peer.ReadRecord()
			if err != nil {
				return
			}
			if env, ok := proto.ParseEnvelope(payload); ok {
				frames <- env
			}
		}
	}()

	box := lootBoxDef{ID: "coupon_box", Name: "券礼包", Chance: 1.0,
		Rewards: []lootBoxReward{{ItemID: couponChucheloItemID, Qty: 3, Weight: 100}}}
	if got := srv.grantLootBox(ctx, sess, box, nil, "测试"); got != couponChucheloItemID {
		t.Fatalf("应开出券，实得 %q", got)
	}

	// grantLootBox 末尾还有一条 24/27 聊天推送，只数 4/22 背包增量。
	updates := 0
	deadline := time.After(2 * time.Second)
	collect := true
	for collect {
		select {
		case env := <-frames:
			if env.Main == 4 && env.Sub == 22 {
				updates++
				if updates == 3 {
					collect = false
				}
			}
		case <-deadline:
			collect = false
		}
	}
	if updates != 3 {
		t.Fatalf("应推 3 条 4/22（每张券一行），实得 %d", updates)
	}
	if r, q := couponRowCount(t, srv, uid); r != 3 || q != 3 {
		t.Fatalf("应落 3 行 3 张，实得 %d 行 %d 张", r, q)
	}
}
