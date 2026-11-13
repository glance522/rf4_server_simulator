package store

import (
	"context"
	"path/filepath"
	"testing"
)

func openSocialTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "social.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	return db
}

func insertCatchForTest(t *testing.T, db *DB, uid int64, loc string, spot int64, fish string, weight int64, grade int64, bait string) {
	t.Helper()
	_, err := db.SQL.Exec(`
		INSERT INTO catch_log(ts,loc,spot,fish_key,name_zh,weight_g,length_m,source,
		                      bait,grade,xp,silver,loc_level,uid)
		VALUES(?,?,?,?,?,?,?,'keep',?,?,?,?,?,?)`,
		float64(100+weight), loc, spot, fish, fish, weight, 0.3, bait, grade, 5, 10, loc, uid)
	if err != nil {
		t.Fatal(err)
	}
}

func TestAchievementPersistenceIsIdempotent(t *testing.T) {
	db := openSocialTestDB(t)
	ctx := context.Background()

	defs, err := db.AchievementDefinitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 130 || defs[0] != 101 || defs[len(defs)-1] != 422 {
		t.Fatalf("unexpected definitions: len=%d first=%d last=%d", len(defs), defs[0], defs[len(defs)-1])
	}
	if err := db.SetAchievementProgress(ctx, 42, 101, 0.4); err != nil {
		t.Fatal(err)
	}
	if err := db.UnlockAchievement(ctx, 42, 101, 0.6, 100); err != nil {
		t.Fatal(err)
	}
	if err := db.UnlockAchievement(ctx, 42, 101, 0.8, 200); err != nil {
		t.Fatal(err)
	}
	states, err := db.PlayerAchievementStates(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	st := states[101]
	if !st.Unlocked || st.Claimed || st.Progress != 0.8 || st.TS != 100 {
		t.Fatalf("unexpected state: %+v", st)
	}
	ok, err := db.ClaimAchievement(ctx, 42, 101)
	if err != nil || !ok {
		t.Fatalf("first claim: ok=%v err=%v", ok, err)
	}
	ok, err = db.ClaimAchievement(ctx, 42, 101)
	if err != nil || ok {
		t.Fatalf("second claim: ok=%v err=%v", ok, err)
	}
}

func TestCatchRecordsAndBestByWater(t *testing.T) {
	db := openSocialTestDB(t)
	ctx := context.Background()
	insertCatchForTest(t, db, 7, "level_a", 1, "c.roach", 100, 1, "worm")
	insertCatchForTest(t, db, 7, "level_a", 2, "c.roach", 300, 2, "worm")
	insertCatchForTest(t, db, 7, "level_b", 1, "perch", 900, 3, "spinner")
	rows, err := db.PlayerCatchRecords(ctx, 7)
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	best, err := db.BestCatchByWater(ctx, 7, "level_a")
	if err != nil || best == nil || best.WeightG != 300 || best.FishKey != "c.roach" {
		t.Fatalf("best=%+v err=%v", best, err)
	}
	missing, err := db.BestCatchByWater(ctx, 7, "missing")
	if err != nil || missing != nil {
		t.Fatalf("missing=%+v err=%v", missing, err)
	}
}

func TestNamedFishListFilterByPlace(t *testing.T) {
	db := openSocialTestDB(t)
	ctx := context.Background()
	rows := []NamedFish{
		{FishID: "f1", Species: "chan_m", Name: "Blue", Owner: "a", Place: "loc16_29", WeightG: 3235, Grade: 4, TS: 20},
		{FishID: "f2", Species: "perch", Name: "Green", Owner: "b", Place: "loc16_31", WeightG: 900, Grade: 4, TS: 10},
	}
	for i := range rows {
		if err := db.NamedFishInsert(ctx, &rows[i]); err != nil {
			t.Fatal(err)
		}
	}
	all, err := db.NamedFishList(ctx, "")
	if err != nil || len(all) != 2 || all[0].FishID != "f1" {
		t.Fatalf("all=%+v err=%v", all, err)
	}
	one, err := db.NamedFishList(ctx, "loc16_31")
	if err != nil || len(one) != 1 || one[0].FishID != "f2" {
		t.Fatalf("filtered=%+v err=%v", one, err)
	}
}
