package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newDrinkBonusDB(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "drink-bonus.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	return db, path
}

func refreshDrinkBonus(t *testing.T, db *DB, uid int64, category string, duration time.Duration, source string) {
	t.Helper()
	tx, err := db.SQL.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := RefreshDrinkBonusTx(context.Background(), tx, uid, category, duration, source); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func drinkBonusByCategory(t *testing.T, statuses []DrinkBonusStatus, category string) DrinkBonusStatus {
	t.Helper()
	for _, status := range statuses {
		if status.Category == category {
			return status
		}
	}
	t.Fatalf("missing drink category %q: %+v", category, statuses)
	return DrinkBonusStatus{}
}

func TestDrinkBonusOnlinePauseRestartAndSharedRunClock(t *testing.T) {
	db, path := newDrinkBonusDB(t)
	ctx := context.Background()
	start := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	refreshDrinkBonus(t, db, 101, "xp", 30*time.Minute, "first")
	refreshDrinkBonus(t, db, 101, "energy", 10*time.Minute, "coffee")
	advance := func(run string, at time.Time, online bool) []DrinkBonusStatus {
		t.Helper()
		statuses, err := db.AdvanceDrinkBonuses(ctx, 101, run, at, online, 0)
		if err != nil {
			t.Fatal(err)
		}
		return statuses
	}
	assertElapsed := func(statuses []DrinkBonusStatus, elapsed time.Duration) {
		t.Helper()
		if len(statuses) != 2 || statuses[0].Category != "energy" || statuses[1].Category != "xp" {
			t.Fatalf("unexpected status order: %+v", statuses)
		}
		for _, tc := range []struct {
			category string
			duration time.Duration
			source   string
		}{{"xp", 30 * time.Minute, "first"}, {"energy", 10 * time.Minute, "coffee"}} {
			status := drinkBonusByCategory(t, statuses, tc.category)
			if status.RemainingMillis != (tc.duration-elapsed).Milliseconds() || status.DurationMillis != tc.duration.Milliseconds() || status.SourceGUID != tc.source {
				t.Fatalf("online elapsed %v: %+v", elapsed, status)
			}
		}
	}
	assertElapsed(advance("run-a", start, true), 0)
	assertElapsed(advance("run-a", start.Add(20*time.Second), true), 20*time.Second)
	// 同一运行实例中的第二个会话在同一时刻更新，不重复扣时。
	assertElapsed(advance("run-a", start.Add(20*time.Second), true), 20*time.Second)
	assertElapsed(advance("run-a", start.Add(25*time.Second), false), 25*time.Second)
	assertElapsed(advance("run-a", start.Add(time.Hour), false), 25*time.Second)
	assertElapsed(advance("run-a", start.Add(time.Hour), true), 25*time.Second)
	assertElapsed(advance("run-a", start.Add(time.Hour+10*time.Second), true), 35*time.Second)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	db = reopened
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	assertElapsed(advance("run-b", start.Add(2*time.Hour), true), 35*time.Second)
	assertElapsed(advance("run-b", start.Add(2*time.Hour+10*time.Second), true), 45*time.Second)
}

func TestDrinkBonusGapClockRollbackExpiryAndRefresh(t *testing.T) {
	db, _ := newDrinkBonusDB(t)
	ctx, start := context.Background(), time.UnixMilli(100000)
	refreshDrinkBonus(t, db, 102, "xp", time.Minute, "old")
	steps := []struct {
		at     time.Duration
		online bool
		maxGap time.Duration
		want   int64
	}{
		{0, true, 0, 60000},
		{10 * time.Second, true, 3 * time.Second, 57000},
		{5 * time.Second, true, 3 * time.Second, 57000},
		{11 * time.Second, true, 3 * time.Second, 56000},
		{time.Hour, true, 3 * time.Second, 53000},
		{time.Hour + 2*time.Second, false, 3 * time.Second, 51000},
		{24 * time.Hour, false, 0, 51000},
		{24 * time.Hour, true, 0, 51000},
		{24*time.Hour + 51*time.Second, true, 0, 0},
	}
	for _, step := range steps {
		statuses, err := db.AdvanceDrinkBonuses(ctx, 102, "clock", start.Add(step.at), step.online, step.maxGap)
		if err != nil {
			t.Fatal(err)
		}
		if step.want == 0 {
			if len(statuses) != 0 {
				t.Fatalf("expired bonus still returned: %+v", statuses)
			}
		} else if got := drinkBonusByCategory(t, statuses, "xp").RemainingMillis; got != step.want {
			t.Fatalf("at %v remaining=%d want=%d", step.at, got, step.want)
		}
	}
	assertCount(t, db, `SELECT COUNT(*) FROM drink_bonus_effect WHERE uid=102 AND remaining_millis=0`, 1)
	refreshDrinkBonus(t, db, 102, "xp", 7500*time.Millisecond, "new")
	statuses, err := db.AdvanceDrinkBonuses(ctx, 102, "clock", start.Add(24*time.Hour+51*time.Second), true, 0)
	if err != nil {
		t.Fatal(err)
	}
	status := drinkBonusByCategory(t, statuses, "xp")
	if status.RemainingMillis != 7500 || status.DurationMillis != 7500 || status.SourceGUID != "new" {
		t.Fatalf("refresh failed: %+v", status)
	}
}

func TestDrinkBonusRefreshKeepsOtherCategoryAndUsesCallerTransaction(t *testing.T) {
	db, _ := newDrinkBonusDB(t)
	ctx, start := context.Background(), time.UnixMilli(100000)
	if _, err := db.SQL.Exec(`INSERT INTO item(guid,uid,item_id,qty) VALUES('test-drink',103,'drink',2)`); err != nil {
		t.Fatal(err)
	}
	apply := func(commit bool) {
		t.Helper()
		tx, err := db.SQL.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := AdvanceDrinkBonusesTx(ctx, tx, 103, "run", start, true, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE item SET qty=qty-1 WHERE guid='test-drink'`); err != nil {
			t.Fatal(err)
		}
		if err := RefreshDrinkBonusTx(ctx, tx, 103, "xp", 10*time.Minute, "wine"); err != nil {
			t.Fatal(err)
		}
		if err := RefreshDrinkBonusTx(ctx, tx, 103, "energy", 20*time.Minute, "coffee"); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO kv(k,v) VALUES('test-drink-receipt','saved')`); err != nil {
			t.Fatal(err)
		}
		if commit {
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		}
	}
	apply(false)
	assertCount(t, db, `SELECT COUNT(*) FROM item WHERE guid='test-drink' AND qty=2`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM drink_bonus_clock WHERE uid=103`, 0)
	assertCount(t, db, `SELECT COUNT(*) FROM drink_bonus_effect WHERE uid=103`, 0)
	assertCount(t, db, `SELECT COUNT(*) FROM kv WHERE k='test-drink-receipt'`, 0)
	apply(true)
	assertCount(t, db, `SELECT COUNT(*) FROM item WHERE guid='test-drink' AND qty=1`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM kv WHERE k='test-drink-receipt'`, 1)
	tx, err := db.SQL.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := AdvanceDrinkBonusesTx(ctx, tx, 103, "run", start.Add(20*time.Second), true, 0); err != nil {
		t.Fatal(err)
	}
	if err := RefreshDrinkBonusTx(ctx, tx, 103, "xp", 30*time.Minute, "new-wine"); err != nil {
		t.Fatal(err)
	}
	statuses, err := AdvanceDrinkBonusesTx(ctx, tx, 103, "run", start.Add(20*time.Second), true, 0)
	if err != nil {
		t.Fatal(err)
	}
	xp, energy := drinkBonusByCategory(t, statuses, "xp"), drinkBonusByCategory(t, statuses, "energy")
	if xp.RemainingMillis != (30*time.Minute).Milliseconds() || xp.SourceGUID != "new-wine" || energy.RemainingMillis != (20*time.Minute-20*time.Second).Milliseconds() || energy.SourceGUID != "coffee" {
		t.Fatalf("same-category replacement changed other bonus: %+v", statuses)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestDrinkBonusConcurrentConnectionsDoNotDoubleCount(t *testing.T) {
	db, path := newDrinkBonusDB(t)
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx, start := context.Background(), time.UnixMilli(100000)
	refreshDrinkBonus(t, db, 104, "xp", time.Minute, "wine")
	if _, err := db.AdvanceDrinkBonuses(ctx, 104, "run", start, true, 0); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	errors := make(chan error, 12)
	for i := range 12 {
		connection := db
		if i%2 != 0 {
			connection = other
		}
		workers.Go(func() {
			_, err := connection.AdvanceDrinkBonuses(ctx, 104, "run", start.Add(10*time.Second), true, 0)
			errors <- err
		})
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	statuses, err := db.AdvanceDrinkBonuses(ctx, 104, "run", start.Add(10*time.Second), true, 0)
	if err != nil || drinkBonusByCategory(t, statuses, "xp").RemainingMillis != 50000 {
		t.Fatalf("concurrent clocks double counted: %+v %v", statuses, err)
	}
}

func TestDrinkBonusMigrationEmptyUpgradeAndRollback(t *testing.T) {
	t.Run("empty_database", func(t *testing.T) {
		db, _ := newDrinkBonusDB(t)
		assertCount(t, db, `SELECT COUNT(*) FROM drink_bonus_clock`, 0)
		assertCount(t, db, `SELECT COUNT(*) FROM drink_bonus_effect`, 0)
		assertCount(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=33 AND name='migrations/00033_drink_bonus.sql'`, 1)
	})
	t.Run("version_30_preserves_save", func(t *testing.T) {
		db := openCatchHistoryDBAtVersion(t, 30)
		if _, err := db.SQL.Exec(`INSERT INTO profile(uid,name,level,xp,silver,gold) VALUES(105,'legacy',50,123456,456,789);
			INSERT INTO item(guid,uid,item_id,qty) VALUES('legacy-drink',105,'wine',5);
			INSERT INTO kv(k,v) VALUES('legacy-drink-note','keep')`); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := db.Migrate(); err != nil {
				t.Fatal(err)
			}
		}
		assertCount(t, db, `SELECT COUNT(*) FROM profile WHERE uid=105 AND level=50 AND xp=123456 AND silver=456 AND gold=789`, 1)
		assertCount(t, db, `SELECT COUNT(*) FROM item WHERE guid='legacy-drink' AND qty=5`, 1)
		assertCount(t, db, `SELECT COUNT(*) FROM kv WHERE k='legacy-drink-note' AND v='keep'`, 1)
		statuses, err := db.AdvanceDrinkBonuses(context.Background(), 105, "new-run", time.Now(), true, 0)
		if err != nil || len(statuses) != 0 {
			t.Fatalf("upgrade invented a drink bonus: %+v %v", statuses, err)
		}
	})
	t.Run("failed_upgrade_rolls_back", func(t *testing.T) {
		db := openCatchHistoryDBAtVersion(t, 30)
		if _, err := db.SQL.Exec(`CREATE TRIGGER reject_drink_migration BEFORE INSERT ON schema_migrations
			WHEN NEW.name='migrations/00033_drink_bonus.sql' BEGIN SELECT RAISE(ABORT,'migration failure'); END`); err != nil {
			t.Fatal(err)
		}
		if err := db.Migrate(); err == nil {
			t.Fatal("migration failure was ignored")
		}
		assertCount(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE name IN ('drink_bonus_clock','drink_bonus_effect')`, 0)
		if _, err := db.SQL.Exec(`DROP TRIGGER reject_drink_migration`); err != nil {
			t.Fatal(err)
		}
		if err := db.Migrate(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDrinkBonusRejectsInvalidValuesAndEnforcesConstraints(t *testing.T) {
	db, _ := newDrinkBonusDB(t)
	ctx := context.Background()
	for _, tc := range []struct {
		category string
		duration time.Duration
	}{{"", time.Minute}, {"xp", 0}, {"xp", -time.Second}, {"xp", time.Microsecond}} {
		tx, err := db.SQL.Begin()
		if err != nil {
			t.Fatal(err)
		}
		err = RefreshDrinkBonusTx(ctx, tx, 106, tc.category, tc.duration, "wine")
		_ = tx.Rollback()
		if err == nil {
			t.Fatalf("accepted invalid refresh: %+v", tc)
		}
	}
	if _, err := db.AdvanceDrinkBonuses(ctx, 106, "", time.Now(), true, 0); err == nil {
		t.Fatal("empty run key accepted")
	}
	for _, query := range []string{
		`INSERT INTO drink_bonus_effect VALUES(106,'xp',-1,1000,'wine')`,
		`INSERT INTO drink_bonus_effect VALUES(106,'xp',1001,1000,'wine')`,
		`INSERT INTO drink_bonus_effect VALUES(106,'xp',0,0,'wine')`,
		`INSERT INTO drink_bonus_effect VALUES(106,'',1000,1000,'wine')`,
		`INSERT INTO drink_bonus_effect VALUES(106,'xp',NULL,1000,'wine')`,
		`INSERT INTO drink_bonus_clock VALUES(106,'run',1000,2)`,
	} {
		if _, err := db.SQL.Exec(query); err == nil {
			t.Fatalf("constraint accepted invalid row: %s", query)
		}
	}
}

func TestCleanupDrinkUseReceipts(t *testing.T) {
	db, _ := newDrinkBonusDB(t)
	ctx := context.Background()
	if _, err := db.SQL.Exec(`
		INSERT INTO kv(k,v) VALUES
		('drink-use:old', '{"created_at":9999}'),
		('drink-use:fresh', '{"created_at":10001}'),
		('drink-use:malformed', 'not-json'),
		('unrelated', '{"created_at":1}')`); err != nil {
		t.Fatal(err)
	}
	removed, err := db.CleanupDrinkUseReceipts(ctx, time.UnixMilli(10000))
	if err != nil || removed != 2 {
		t.Fatalf("cleanup removed=%d err=%v", removed, err)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM kv WHERE k='drink-use:old' OR k='drink-use:malformed'`, 0)
	assertCount(t, db, `SELECT COUNT(*) FROM kv WHERE k='drink-use:fresh'`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM kv WHERE k='unrelated'`, 1)
}

func TestCleanupDrinkUseReceiptsRollsBack(t *testing.T) {
	db, _ := newDrinkBonusDB(t)
	ctx := context.Background()
	if _, err := db.SQL.Exec(`
		INSERT INTO kv(k,v) VALUES ('drink-use:old', '{"created_at":1}');
		CREATE TRIGGER reject_drink_receipt_cleanup BEFORE DELETE ON kv
		WHEN OLD.k GLOB 'drink-use:*' BEGIN SELECT RAISE(ABORT, 'cleanup failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CleanupDrinkUseReceipts(ctx, time.UnixMilli(10000)); err == nil {
		t.Fatal("cleanup failure was ignored")
	}
	assertCount(t, db, `SELECT COUNT(*) FROM kv WHERE k='drink-use:old'`, 1)
	if _, err := db.SQL.Exec(`DROP TRIGGER reject_drink_receipt_cleanup`); err != nil {
		t.Fatal(err)
	}
}
