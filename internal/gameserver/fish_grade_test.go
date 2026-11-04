package gameserver

import (
	"context"
	"path/filepath"
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func TestFishGradeRulerBoundariesAndWire(t *testing.T) {
	s, _ := newProgressSrv(t)
	ctx := context.Background()
	for _, tc := range []struct {
		species string
		lines   [3]int64
	}{
		{"perch", [3]int64{280, 1600, 2700}},
		{"c.roach", [3]int64{220, 1200, 2000}},
		{"crucian", [3]int64{300, 1900, 3200}},
		{"s.bream", [3]int64{140, 800, 1200}},
	} {
		for i, line := range tc.lines {
			for _, delta := range []int64{-1, 0, 1} {
				weight := line + delta
				want := int64(i + 2)
				if delta < 0 {
					want--
				}
				grade := s.rollGradeDB(ctx, tc.species, weight)
				if grade != want {
					t.Fatalf("%s %dg: grade=%d want=%d", tc.species, weight, grade, want)
				}
				f := &RolledFish{FishGUID: newFishGUID(), Species: tc.species, WeightG: weight, Grade: grade}
				if got := u32Of(s.buildFish27(f, "", "").Field(2).Num); got != uint32(want) {
					t.Fatalf("27 grade=%d want=%d", got, want)
				}
				for _, obj := range []*proto.Value{
					s.build813(ctx, 1000001, f, 0, 0, 0),
					s.buildSettle813(ctx, 1000001, f, &settleXpResult{}),
				} {
					for j, threshold := range tc.lines {
						if got := u32Of(obj.Field(j).Num); got != uint32(threshold) {
							t.Fatalf("ruler %s line%d=%d want=%d", tc.species, j, got, threshold)
						}
					}
					fish := obj.Field(9).Val
					if u32Of(fish.Field(1).Num) != uint32(weight) || u32Of(fish.Field(3).Num) != uint32(want) {
						t.Fatalf("settlement fish differs: %+v", fish)
					}
				}
			}
		}
	}
}

func TestFishGradeAllStaticSpecies(t *testing.T) {
	db, err := store.OpenWithStatic(filepath.Join(t.TempDir(), "grade.db"), filepath.Join("..", "..", "server_data", "static.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	s := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "grade-test"}}, logging.Discard(), nil)
	s.SetDB(db)
	ctx := context.Background()
	rows, err := db.SQL.QueryContext(ctx, `SELECT fish_key FROM species_weight`)
	if err != nil {
		t.Fatal(err)
	}
	var species []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		species = append(species, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(species) == 0 {
		t.Fatal("static fish roster is empty")
	}
	for _, key := range species {
		lines := s.settleStatics(ctx, key)
		if lines[0] <= 0 || lines[0] >= lines[1] || lines[1] >= lines[2] {
			t.Fatalf("invalid ruler %s: %v", key, lines)
		}
		for i, line := range lines[:3] {
			for _, delta := range []int64{-1, 0, 1} {
				want := int64(i + 2)
				if delta < 0 {
					want--
				}
				if got := s.rollGradeDB(ctx, key, line+delta); got != want {
					t.Fatalf("%s %dg: grade=%d want=%d ruler=%v", key, line+delta, got, want, lines)
				}
			}
		}
	}
	t.Logf("checked %d species at all three ruler boundaries", len(species))
}

func TestAdminFishUsesRealWeightAndSurvivesRestart(t *testing.T) {
	s, db := newProgressSrv(t)
	const uid = int64(1000001)
	for _, weight := range []int64{279, 280, 1599, 1600, 2699, 2700} {
		s.execAdminCmd(&AdminCommand{Op: "arm_fish", UID: uid, Species: "perch", WeightG: weight, Grade: 4})
		armed := s.stateFor(uid).Armed
		want := s.rollGradeDB(context.Background(), "perch", weight)
		if armed == nil || armed.WeightG != weight || armed.Grade != want {
			t.Fatalf("armed fish=%+v want weight=%d grade=%d", armed, weight, want)
		}
		s.execAdminCmd(&AdminCommand{Op: "add_keepnet", UID: uid, Keepnet: &store.KeepnetEntry{
			Species: "perch", WeightG: weight, Cat: 4,
		}})
	}
	entries, err := db.KeepnetList(context.Background(), uid)
	if err != nil || len(entries) != 6 {
		t.Fatalf("keepnet count=%d err=%v", len(entries), err)
	}
	for _, e := range entries {
		want := s.rollGradeDB(context.Background(), e.Species, e.WeightG)
		if e.Cat != want || u32Of(buildKeepnet25(e, "").Field(3).Num) != uint32(want) {
			t.Fatalf("keepnet fish=%+v want grade=%d", e, want)
		}
	}
	restarted := newFishingRecoveryServer(t, db)
	if got := restarted.stateFor(uid).Armed; got == nil || *got != *s.stateFor(uid).Armed {
		t.Fatalf("restart changed armed fish: %+v", got)
	}
}

func TestLuckyRollGradeUsesCappedWeight(t *testing.T) {
	s, db := newProgressSrv(t)
	for i := 0; i < 100; i++ {
		f := s.rollFishPoolsWithRules("loc1_02", 0.45, fishingSkillBonus{BiteSpeed: 1, TrophyLuck: 2})
		if f == nil || f.WeightG > 450 || f.Grade != s.rollGradeDB(t.Context(), f.Species, f.WeightG) {
			t.Fatalf("luck bypassed cap or ruler: %+v", f)
		}
	}
	for _, query := range []string{
		`INSERT INTO loc_alias(alias,loc) VALUES('level_grade','grade_area')`,
		`INSERT INTO loc_fish(loc,fish_key,catches) VALUES('grade_area','perch',1)`,
	} {
		if _, err := db.SQL.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 100; i++ {
		f := s.rollFishFromDBWithRules("level_grade", "", 0.1, fishingSkillBonus{BiteSpeed: 1, TrophyLuck: 2})
		if f == nil || f.Species != "perch" || f.WeightG > 100 || f.Grade != 1 {
			t.Fatalf("static roll luck bypassed cap or ruler: %+v", f)
		}
	}
}
