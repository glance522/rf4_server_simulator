package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func cafeTestFish(t *testing.T, db *DB, uid int64, guid string, weight int64, species, level string) {
	t.Helper()
	_, err := db.SQL.Exec(`INSERT INTO keepnet_items(echo_guid,uid,species,name_zh,weight_g,length_m,cat,keep_loc,keep_spot,keep_bait,keep_level,ts) VALUES(?,?,?,'',?,0.2,2,'loc1_01',0,'worm',?,1)`, guid, uid, species, weight, level)
	if err != nil {
		t.Fatal(err)
	}
}

func cafeTestOrders(t *testing.T, db *DB, now int64) []*CafeOrder {
	t.Helper()
	orders, err := db.GetCafeOrders(context.Background(), 1, "cafe1", "level_001_lake", now, 100, []CafeOrderTemplate{
		{Species: "ruffe", MinWeightG: 100, Quantity: 2, RewardCents: 1000, PeriodSeconds: 100, PhaseSeconds: 5},
		{Species: "perch", MinWeightG: 300, Quantity: 1, RewardCents: 2000, PeriodSeconds: 300, PhaseSeconds: 20, OfferSize: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	return orders
}

func TestCafeOrdersIndependentDeadlinesPersistAndRotate(t *testing.T) {
	db := openIntegrityTestDB(t)
	ctx := context.Background()
	for _, uid := range []int64{1, 2} {
		if _, err := db.EnsureProfile(ctx, uid); err != nil {
			t.Fatal(err)
		}
	}
	first := cafeTestOrders(t, db, 110)
	if first[0].ExpiresAt != 205 || first[1].ExpiresAt != 320 {
		t.Fatalf("截止时间未错开: %+v", first)
	}
	again := cafeTestOrders(t, db, 204)
	for i := range first {
		if *first[i] != *again[i] {
			t.Fatalf("刷新改动了订单 %d", i)
		}
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	again = cafeTestOrders(t, db, 205)
	if again[0].ID == first[0].ID || again[1].ID != first[1].ID {
		t.Fatal("应只替换到期订单")
	}
	if _, err := db.SubmitCafeOrder(ctx, 1, "cafe1", first[0].ID, nil, 205); !errors.Is(err, ErrCafeOrderExpired) {
		t.Fatalf("到期应拒绝交付: %v", err)
	}
	other, err := db.GetCafeOrders(ctx, 2, "cafe1", "level_001_lake", 205, 100, []CafeOrderTemplate{
		{Species: "fake", MinWeightG: 1, Quantity: 1, RewardCents: 1, PeriodSeconds: 100, PhaseSeconds: 5},
		{Species: "fake", MinWeightG: 1, Quantity: 1, RewardCents: 1, PeriodSeconds: 300, PhaseSeconds: 20},
	})
	if err != nil || other[0].ID != again[0].ID || other[0].Species != again[0].Species {
		t.Fatalf("其他玩家不共享订单: %+v %v", other, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(db.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Migrate(); err != nil {
		t.Fatal(err)
	}
	resumed := cafeTestOrders(t, reopened, 206)
	for i := range again {
		if *again[i] != *resumed[i] {
			t.Fatalf("重启后订单发生变化: %d", i)
		}
	}
}

func TestCafeSubmitAtomicAndIdempotent(t *testing.T) {
	db := openIntegrityTestDB(t)
	ctx := context.Background()
	if _, err := db.EnsureProfile(ctx, 1); err != nil {
		t.Fatal(err)
	}
	order := cafeTestOrders(t, db, 110)[0]
	guids := []string{fmt.Sprintf("%032x", 11), fmt.Sprintf("%032x", 12)}
	for _, g := range guids {
		cafeTestFish(t, db, 1, g, 100, "ruffe", "level_001_lake")
	}
	p0, _ := db.GetProfile(ctx, 1)
	// 事务最后写回执失败时，已执行的扣鱼和付款也必须回滚。
	if _, err := db.SQL.Exec(`CREATE TRIGGER fail_cafe BEFORE INSERT ON cafe_order_completion BEGIN SELECT RAISE(ABORT,'receipt failed'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SubmitCafeOrder(ctx, 1, "cafe1", order.ID, guids, 111); err == nil {
		t.Fatal("故障注入未触发")
	}
	assertCount(t, db, `SELECT COUNT(*) FROM keepnet_items`, 2)
	assertCount(t, db, `SELECT COUNT(*) FROM txn WHERE kind='cafe_order'`, 0)
	p1, _ := db.GetProfile(ctx, 1)
	if p0.Silver != p1.Silver {
		t.Fatal("失败留下了收入")
	}
	if _, err := db.SQL.Exec(`DROP TRIGGER fail_cafe`); err != nil {
		t.Fatal(err)
	}
	result, err := db.SubmitCafeOrder(ctx, 1, "cafe1", order.ID, guids, 111)
	if err != nil || result.Replayed || len(result.RemovedGUIDs) != 2 || result.RewardCents != 1000 {
		t.Fatalf("结算错误: %+v %v", result, err)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM keepnet_items`, 0)
	p1, _ = db.GetProfile(ctx, 1)
	if p1.Silver != p0.Silver+1000 {
		t.Fatal("收入不符")
	}
	result, err = db.SubmitCafeOrder(ctx, 1, "cafe1", order.ID, guids, 112)
	if err != nil || !result.Replayed {
		t.Fatalf("重试错误: %+v %v", result, err)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM txn WHERE kind='cafe_order'`, 1)
	p2, _ := db.GetProfile(ctx, 1)
	if p2.Silver != p1.Silver {
		t.Fatal("重复付款")
	}
	orders := cafeTestOrders(t, db, 112)
	if !orders[0].Completed || orders[1].Completed {
		t.Fatal("完成状态未持久化")
	}
}

func TestCafeSubmitRejectsInvalidFishAndLocks(t *testing.T) {
	cases := []string{"duplicate", "foreign", "species", "weight", "level", "count", "locked", "scene"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			db := openIntegrityTestDB(t)
			ctx := context.Background()
			for _, uid := range []int64{1, 2} {
				if _, err := db.EnsureProfile(ctx, uid); err != nil {
					t.Fatal(err)
				}
			}
			order := cafeTestOrders(t, db, 110)[0]
			g1, g2 := fmt.Sprintf("%032x", 21), fmt.Sprintf("%032x", 22)
			owner, weight, species, level := int64(1), int64(100), "ruffe", "level_001_lake"
			switch name {
			case "foreign":
				owner = 2
			case "species":
				species = "perch"
			case "weight":
				weight = 99
			case "level":
				level = "level_002_kirzah_river"
			}
			cafeTestFish(t, db, owner, g1, weight, species, level)
			cafeTestFish(t, db, 1, g2, 100, "ruffe", "level_001_lake")
			gs := []string{g1, g2}
			scene := "cafe1"
			if name == "duplicate" {
				gs = []string{g1, g1}
			}
			if name == "count" {
				gs = []string{g1}
			}
			if name == "scene" {
				scene = "cafe2"
			}
			if name == "locked" {
				if _, err := db.CreateMarketOffers(ctx, 1, fmt.Sprintf("%032x", 30), []MarketOfferCreateRequest{{FishGUID: g1, PriceCents: 50}}); err != nil {
					t.Fatal(err)
				}
			}
			p0, _ := db.GetProfile(ctx, 1)
			if _, err := db.SubmitCafeOrder(ctx, 1, scene, order.ID, gs, 111); err == nil {
				t.Fatal("无效交付被接受")
			}
			assertCount(t, db, `SELECT COUNT(*) FROM keepnet_items`, 2)
			assertCount(t, db, `SELECT COUNT(*) FROM cafe_order_completion`, 0)
			p1, _ := db.GetProfile(ctx, 1)
			if p1.Silver != p0.Silver {
				t.Fatal("拒绝交付仍付款")
			}
		})
	}
}

func TestCafeMigrationPreservesOldSaveAndRollsBack(t *testing.T) {
	db := openCatchHistoryDBAtVersion(t, 20)
	if _, err := db.EnsureProfile(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	cafeTestFish(t, db, 1, fmt.Sprintf("%032x", 99), 100, "ruffe", "level_001_lake")
	if _, err := db.SQL.Exec(`CREATE TRIGGER fail_cafe_migration BEFORE INSERT ON schema_migrations WHEN NEW.name='migrations/00021_cafe_orders.sql' BEGIN SELECT RAISE(ABORT,'migration failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err == nil {
		t.Fatal("迁移应失败")
	}
	assertCount(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE name IN ('cafe_order','cafe_order_completion')`, 0)
	if _, err := db.SQL.Exec(`DROP TRIGGER fail_cafe_migration`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.Migrate(); err != nil {
			t.Fatal(err)
		}
	}
	assertCount(t, db, `SELECT COUNT(*) FROM keepnet_items`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE name='migrations/00021_cafe_orders.sql'`, 1)
	if err := ValidateTableOwnership(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO cafe_order(order_id,scene,level_key,batch_start,period_seconds,ordinal,species,min_weight_g,quantity,reward_cents,expires_at,offer_size) VALUES('x','a','b',0,1,0,'ruffe',0,1,1,1,1)`); err == nil {
		t.Fatal("无效重量未被约束")
	}
}

func TestCafePricingMigrationKeepsOldQuotesReceiptsAndWallet(t *testing.T) {
	db := openCatchHistoryDBAtVersion(t, 22)
	ctx := context.Background()
	if _, err := db.EnsureProfile(ctx, 1); err != nil {
		t.Fatal(err)
	}
	const old = "abcdef00000000000000000000000001"
	if _, err := db.SQL.Exec(`INSERT INTO cafe_order(order_id,scene,level_key,batch_start,period_seconds,ordinal,species,min_weight_g,quantity,reward_cents,expires_at) VALUES(?,'cafe1','level_001_lake',0,1000,0,'ruffe',100,1,1234,1000)`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO cafe_order_completion VALUES(1,?,1234,'[]',100)`, old); err != nil {
		t.Fatal(err)
	}
	p0, _ := db.GetProfile(ctx, 1)
	if _, err := db.SQL.Exec(`CREATE TRIGGER fail_cafe_version BEFORE INSERT ON schema_migrations WHEN NEW.name='migrations/00030_cafe_pricing_version.sql' BEGIN SELECT RAISE(ABORT,'migration unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err == nil {
		t.Fatal("迁移应回滚")
	}
	assertCount(t, db, `SELECT COUNT(*) FROM pragma_table_info('cafe_order') WHERE name='pricing_version'`, 0)
	assertCount(t, db, `SELECT COUNT(*) FROM cafe_order_completion`, 1)
	if _, err := db.SQL.Exec(`DROP TRIGGER fail_cafe_version`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.Migrate(); err != nil {
			t.Fatal(err)
		}
	}
	assertCount(t, db, `SELECT COUNT(*) FROM cafe_order WHERE pricing_version=1 AND reward_cents=1234`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM cafe_order_completion WHERE reward_cents=1234`, 1)
	rows, err := db.SQL.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	broken := rows.Next()
	rows.Close()
	if broken {
		t.Fatal("迁移损坏外键")
	}
	orders, err := db.GetCafeOrders(ctx, 1, "cafe1", "level_001_lake", 110, 1000, []CafeOrderTemplate{{Species: "ruffe", MinWeightG: 100, Quantity: 1, RewardCents: 2000, PricingVersion: 2}})
	if err != nil || orders[0].ID == old || orders[0].Completed || orders[0].RewardCents != 2000 {
		t.Fatalf("新规则未生成新报价: %+v %v", orders, err)
	}
	res, err := db.SubmitCafeOrder(ctx, 1, "cafe1", old, nil, 120)
	if err != nil || !res.Replayed || res.RewardCents != 1234 {
		t.Fatalf("旧回执失效: %+v %v", res, err)
	}
	p1, _ := db.GetProfile(ctx, 1)
	if p0.Silver != p1.Silver {
		t.Fatal("升级或重试改了钱包")
	}
}

func TestCafeLegacyPricingNumberDoesNotSkipReelMigrations(t *testing.T) {
	for _, legacy := range []int{23, 25, 27, 29} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			db := openCatchHistoryDBAtVersion(t, 22)
			ctx := context.Background()
			if _, err := db.EnsureProfile(ctx, 1); err != nil {
				t.Fatal(err)
			}
			if _, err := db.SQL.Exec(`INSERT INTO cafe_order(order_id,scene,level_key,batch_start,period_seconds,ordinal,species,min_weight_g,quantity,reward_cents,expires_at) VALUES('abc','cafe1','level_001_lake',0,1000,0,'ruffe',100,1,1234,1000)`); err != nil {
				t.Fatal(err)
			}
			body, err := migrationsFS.ReadFile("migrations/00030_cafe_pricing_version.sql")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.SQL.Exec(string(body)); err != nil {
				t.Fatal(err)
			}
			if _, err = db.SQL.Exec(fmt.Sprintf(`UPDATE cafe_order SET pricing_version=2; INSERT INTO schema_migrations VALUES(%d,'migrations/%05d_cafe_pricing_version.sql',0)`, legacy, legacy)); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err = db.Migrate(); err != nil {
					t.Fatal(err)
				}
			}
			assertCount(t, db, `SELECT COUNT(*) FROM cafe_order WHERE pricing_version=2 AND reward_cents=1234`, 1)
			assertCount(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=23 AND name='migrations/00023_reel_performance.sql'`, 1)
			assertCount(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=24 AND name='migrations/00024_reel_bearing_oil.sql'`, 1)
			assertCount(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=30 AND name='migrations/00030_cafe_pricing_version.sql'`, 1)
			if err := ValidateTableOwnership(ctx, db); err != nil {
				t.Fatal(err)
			}
			assertCount(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=25 AND name='migrations/00025_reel_cast_friction.sql'`, 1)
			assertCount(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=26 AND name='migrations/00026_player_condition.sql'`, 1)
		})
	}
}
