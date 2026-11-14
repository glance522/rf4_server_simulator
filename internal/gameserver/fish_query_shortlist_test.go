package gameserver

import (
	"context"
	"errors"
	"math"
	"slices"
	"testing"
	"time"
)

func TestFishQueryCoarseRegionScoreAndEmpty(t *testing.T) {
	g, err := newFishQueryRegions("test", fishQueryRegionScene{Size: 80}, time.Time{}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	g.factors["target"] = []float64{4, 1, 0, 0}
	g.factors["other"] = []float64{40, .1, 0, 0}
	want := []string{g.areas[1], g.areas[0]}
	if err := g.shortlist(t.Context(), "target", map[string]float64{"target": 1, "other": 1}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(g.areas, want) || g.scanned != 4 {
		t.Fatalf("coarse rank=%v scanned=%d", g.areas, g.scanned)
	}
	if err := g.shortlist(t.Context(), "target", map[string]float64{"target": 0, "other": 1}); err != nil || len(g.areas) != 0 {
		t.Fatalf("zero-weight target retained regions: %v %v", g.areas, err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := g.shortlist(cancelled, "target", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("ignored cancellation: %v", err)
	}
	g, err = newFishQueryRegions("test", fishQueryRegionScene{Size: 80}, time.Time{}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.shortlist(t.Context(), "target", map[string]float64{"target": 1}); err != nil || len(g.areas) != 0 || g.scanned != 0 {
		t.Fatalf("disabled distribution produced regions: %+v %v", g, err)
	}
}

func TestFishQueryCoarseLimitAndFineProbability(t *testing.T) {
	g, err := newFishQueryRegions("test", fishQueryRegionScene{Size: 240}, time.Time{}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	original := append([]string(nil), g.areas...)
	for _, key := range []string{"target", "other-a", "other-b"} {
		g.factors[key] = make([]float64, len(g.areas))
	}
	for i := range g.areas {
		g.factors["target"][i] = 1
		if i < fishQueryCoarseRegionLimit {
			g.factors["other-a"][i] = 1 // bait-free target share = 1/2
		} else {
			g.factors["other-b"][i] = 3 // bait-free target share = 1/4
		}
	}
	if err := g.shortlist(t.Context(), "target", map[string]float64{"target": 1, "other-a": 1, "other-b": 1}); err != nil {
		t.Fatal(err)
	}
	if g.scanned != 36 || !slices.Equal(g.areas, original[:fishQueryCoarseRegionLimit]) {
		t.Fatalf("limit or stable tie order changed: %v", g.areas)
	}
	// This bait excludes other-b. Reintroducing discarded regions would make
	// one of them win, but the fine pass must stay within the coarse shortlist.
	q := &fishQueryCapture{fish: "target", biteProbability: .6, acceptance: .5, gradeShare: .2,
		regionalWeights: []fishQueryRegionalWeight{{"target", 1, .5}, {"other-a", 5, .8}, {"other-b", 0, 1}}}
	r, err := g.best(t.Context(), q)
	if err != nil || r == nil || *r != g.bounds[0] {
		t.Fatalf("fine pass escaped shortlist: %v %v", r, err)
	}
	if math.Abs(q.chance-.01) > 1e-12 || math.Abs(q.selection-1.0/6) > 1e-12 || math.Abs(q.fishShare-.5/4.5) > 1e-12 {
		t.Fatalf("fine result is not the direct bite probability: %+v", q)
	}
}

func TestFishQueryCoarseMapIgnoresBaitAndUsesOverrides(t *testing.T) {
	s, db := seedBaitWeightTest(t)
	const uid int64 = 1000001
	const level = "level_015_tunguska"
	if _, err := db.EnsureProfile(t.Context(), uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE loc_alias SET alias=? WHERE alias='level_900_rules'`, level); err != nil {
		t.Fatal(err)
	}
	if err := db.TunableSet(t.Context(), "fish_distribution_enabled", "1", "test"); err != nil {
		t.Fatal(err)
	}
	s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
	at := time.Unix(1794096000, 0)
	queryMap := func(req FishQueryRequest) *fishQueryRegions {
		t.Helper()
		maps, err := s.fishQueryMaps(t.Context(), req, fishRollContext{cache: newFishingRollCache(), now: at})
		if err != nil || len(maps) != 1 || maps[0].regions == nil {
			t.Fatalf("maps=%+v err=%v", maps, err)
		}
		return maps[0].regions
	}
	req := FishQueryRequest{UID: uid, Fish: "c.roach", Map: level, Grade: 1, Bait: "rule_worm"}
	first := queryMap(req)
	req.Bait, req.Grade, req.RigType = "rule_lure", 4, 5
	second := queryMap(req)
	if first.scanned != 900 || len(first.areas) != fishQueryCoarseRegionLimit || !slices.Equal(first.areas, second.areas) {
		t.Fatal("bait, grade or rig affected the coarse shortlist")
	}
	if _, err := db.SQL.Exec(`INSERT INTO fish_weight_override(loc,fish_key,weight,enabled,updated_ts,updated_by)
		VALUES('rules-test','c.roach',7,1,0,'test'),('rules-test','frog',100,1,0,'test')`); err != nil {
		t.Fatal(err)
	}
	roster, err := db.LocRoster(t.Context(), "rules-test")
	if err != nil {
		t.Fatal(err)
	}
	weights := map[string]float64{}
	for _, row := range roster {
		weights[row.FishKey] = row.Catches
	}
	weights["c.roach"], weights["frog"] = 7, 100
	want, err := newFishQueryRegions("rules-test", fishQueryRegionScenes[level], at, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := want.shortlist(t.Context(), "c.roach", weights); err != nil {
		t.Fatal(err)
	}
	if got := queryMap(req); !slices.Equal(got.areas, want.areas) {
		t.Fatal("coarse stage ignored map weight overrides")
	}
	if _, err := db.SQL.Exec(`UPDATE fish_weight_override SET enabled=0 WHERE fish_key='c.roach'`); err != nil {
		t.Fatal(err)
	}
	if got := queryMap(req); len(got.areas) != 0 {
		t.Fatal("disabled target retained coarse regions")
	}
}
