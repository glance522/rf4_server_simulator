package store

import (
	"path/filepath"
	"testing"
)

func TestCampfireAndCafeMigrateEmptyDatabase(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for range 2 {
		if err := db.Migrate(); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateTableOwnership(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=29 AND name='migrations/00029_campfire_state.sql'`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=30 AND name='migrations/00030_cafe_pricing_version.sql'`, 1)
	if _, err := db.SQL.Exec(`INSERT INTO campfire_state(uid,duration_min,expires_ft,updated_ts) VALUES(1,30,12345,0)`); err != nil {
		t.Fatal(err)
	}
	state, err := db.GetCampfireState(t.Context(), 1)
	if err != nil || state == nil || state.Loc != "" || state.Spot != 0 || state.DurationMin != 30 || state.ExpiresFT != 12345 {
		t.Fatalf("campfire defaults or integer fields changed: %+v %v", state, err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO campfire_state(uid,duration_min,expires_ft,updated_ts) VALUES(2,NULL,12345,0)`); err == nil {
		t.Fatal("campfire duration accepted NULL")
	}
	state.Loc, state.Spot, state.DurationMin = "level_001_lake", 22, 60
	if err := db.UpsertCampfireState(t.Context(), *state); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnsureProfile(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	if orders := cafeTestOrders(t, db, 110); len(orders) == 0 {
		t.Fatal("cafe orders unavailable after empty migration")
	}
}

func TestCafe29UpgradePreservesSaveAndCampfireMigrationRollsBack(t *testing.T) {
	db := openCatchHistoryDBAtVersion(t, 28)
	ctx := t.Context()
	if _, err := db.EnsureProfile(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET silver=54321,gold=123,bank=456 WHERE uid=1`); err != nil {
		t.Fatal(err)
	}
	cafeTestFish(t, db, 1, "11111111111111111111111111111111", 100, "ruffe", "level_001_lake")
	if _, err := db.SQL.Exec(`INSERT INTO cafe_order(order_id,scene,level_key,batch_start,period_seconds,ordinal,species,min_weight_g,quantity,reward_cents,expires_at)
        VALUES('legacy29','cafe1','level_001_lake',0,1000,0,'ruffe',100,1,1234,1000);
        INSERT INTO cafe_order_completion VALUES(1,'legacy29',1234,'[]',100)`); err != nil {
		t.Fatal(err)
	}
	pricing, err := migrationsFS.ReadFile("migrations/00030_cafe_pricing_version.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(string(pricing)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE cafe_order SET pricing_version=2;
        INSERT INTO schema_migrations VALUES(29,'migrations/00029_cafe_pricing_version.sql',42);
        CREATE TRIGGER reject_campfire_migration BEFORE INSERT ON schema_migrations
        WHEN NEW.name='migrations/00029_campfire_state.sql'
        BEGIN SELECT RAISE(ABORT,'campfire migration failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err == nil {
		t.Fatal("failed campfire migration was accepted")
	}
	assertCount(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='campfire_state'`, 0)
	assertCount(t, db, `SELECT COUNT(*) FROM cafe_order WHERE order_id='legacy29' AND pricing_version=2 AND reward_cents=1234`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM cafe_order_completion WHERE order_id='legacy29' AND reward_cents=1234`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=30 AND name='migrations/00030_cafe_pricing_version.sql' AND applied_at=42`, 1)
	if _, err := db.SQL.Exec(`DROP TRIGGER reject_campfire_migration`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.Migrate(); err != nil {
			t.Fatal(err)
		}
	}
	assertCount(t, db, `SELECT COUNT(*) FROM profile WHERE uid=1 AND silver=54321 AND gold=123 AND bank=456`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM keepnet_items WHERE echo_guid='11111111111111111111111111111111'`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=29 AND name='migrations/00029_campfire_state.sql'`, 1)
	if err := ValidateTableOwnership(ctx, db); err != nil {
		t.Fatal(err)
	}
	replay, err := db.SubmitCafeOrder(ctx, 1, "cafe1", "legacy29", nil, 120)
	if err != nil || !replay.Replayed || replay.RewardCents != 1234 {
		t.Fatalf("cafe receipt changed after renumbering: %+v %v", replay, err)
	}
	expected := CampfireState{UID: 1, Loc: "level_001_lake", Spot: 42, DurationMin: 60, ExpiresFT: 987654321, UpdatedTS: 42}
	if err := db.UpsertCampfireState(ctx, expected); err != nil {
		t.Fatal(err)
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
	state, err := reopened.GetCampfireState(ctx, 1)
	if err != nil || state == nil || *state != expected {
		t.Fatalf("campfire state lost across restart: %+v %v", state, err)
	}
}

func TestUpstreamCampfire29KeepsStateWhenAddingCafe30(t *testing.T) {
	db := openCatchHistoryDBAtVersion(t, 29)
	ctx := t.Context()
	// 上游没有咖啡厅迁移，移除夹具中的本地订单表以复现该版本。
	if _, err := db.SQL.Exec(`DROP TABLE cafe_order_completion; DROP TABLE cafe_order;
        DELETE FROM schema_migrations WHERE name='migrations/00021_cafe_orders.sql'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnsureProfile(ctx, 1); err != nil {
		t.Fatal(err)
	}
	expected := CampfireState{UID: 1, Loc: "level_001_lake", Spot: 42, DurationMin: 60, ExpiresFT: 987654321, UpdatedTS: 123}
	if err := db.UpsertCampfireState(ctx, expected); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.Migrate(); err != nil {
			t.Fatal(err)
		}
	}
	state, err := db.GetCampfireState(ctx, 1)
	if err != nil || state == nil || *state != expected {
		t.Fatalf("cafe migration changed existing campfire: %+v %v", state, err)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=29 AND name='migrations/00029_campfire_state.sql'`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=30 AND name='migrations/00030_cafe_pricing_version.sql'`, 1)
	if orders := cafeTestOrders(t, db, 110); len(orders) == 0 {
		t.Fatal("cafe orders unavailable on upstream campfire history")
	}
	if err := ValidateTableOwnership(ctx, db); err != nil {
		t.Fatal(err)
	}
}
