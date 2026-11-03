package gameserver

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func TestFishPerformanceSizeSpeciesAndForceUnits(t *testing.T) {
	s := NewServer(config.Game{}, logging.Discard(), nil)
	for _, species := range []string{"c.roach", "c.carp", "m_carp_albino", "a_salmon", "r_sturgeon", "thynnus_t", "unknown"} {
		lastForce, lastStamina := 0.0, 0.0
		for _, grams := range []int64{1, 63, 1000, 10000, 100000, 1000000, 2000000} {
			fish := &RolledFish{Species: species, WeightG: grams}
			force, stamina := s.fishForceFor(fish), s.fishStaminaFor(fish)
			if !isFinitePositive(force) || !isFinitePositive(stamina) || force <= lastForce || stamina <= lastStamina {
				t.Fatalf("%s %dg: force=%g stamina=%g", species, grams, force, stamina)
			}
			kgf := force / (verifiedForcePerGram * 1000)
			if restored := fishWeightForPull(species, kgf); math.Abs(restored-float64(grams)) > float64(grams)*1e-9 {
				t.Fatalf("%s: N/kgf conversion changed fish weight: %g", species, restored)
			}
			lastForce, lastStamina = force, stamina
		}
	}
	roach := &RolledFish{Species: "c.roach", WeightG: 1000}
	salmon := &RolledFish{Species: "a_salmon", WeightG: 1000}
	if s.fishStaminaFor(salmon) <= s.fishStaminaFor(roach) || s.fishForceFor(salmon) <= s.fishForceFor(roach) {
		t.Fatal("same-weight species lost their distinct force/stamina")
	}
	if performanceForFish("m_carp_albino") != performanceForFish("m_carp") {
		t.Fatal("colour variant changed performance")
	}
}

func isFinitePositive(v float64) bool { return v > 0 && !math.IsInf(v, 0) && !math.IsNaN(v) }

func TestFishFamilyVariablesAndGrowthBoundary(t *testing.T) {
	profiles := map[fishPerformance]bool{}
	for _, species := range []string{"c.roach", "c.carp", "a_salmon", "r_sturgeon", "thynnus_t", "cetorhinus_m", "atl_cod", "n.pike"} {
		p := performanceForFish(species)
		profiles[p] = true
		if p.pullExponent <= 0 || p.pullExponent >= 1 || p.staminaExponent <= p.pullExponent || p.staminaExponent >= 1 {
			t.Fatalf("invalid fish-family growth variables: %s %+v", species, p)
		}
		for _, exponent := range []float64{p.pullExponent, p.staminaExponent} {
			left, anchor, right := fishMassScale(999.999, exponent), fishMassScale(1000, exponent), fishMassScale(1000.001, exponent)
			if left >= anchor || right <= anchor || math.Abs(right-left) > 0.003 {
				t.Fatalf("%s discontinuous growth at 1kg", species)
			}
		}
		for _, grams := range []float64{1, 999, 1000, 1001, 100000, 3125000} {
			kgf := fishBaseForce(species, grams) / (verifiedForcePerGram * 1000)
			if restored := fishWeightForPull(species, kgf); math.Abs(restored-grams) > grams*1e-9 {
				t.Fatalf("%s inverse matching uses different variables: %g != %g", species, restored, grams)
			}
		}
	}
	if len(profiles) != 8 {
		t.Fatal("fish families unexpectedly share all function variables")
	}
}

func TestStarterRigSamplesViableFish(t *testing.T) {
	s, db := newBalanceTestServer(t)
	ctx := context.Background()
	if err := s.EnsurePlayer(ctx, testUID); err != nil {
		t.Fatal(err)
	}
	guids, err := db.ItemGUIDsByContainers(ctx, testUID, []string{"backpack", "rig"})
	if err != nil {
		t.Fatal(err)
	}
	for _, guid := range guids {
		row, _ := db.GetItemByGUID(ctx, guid)
		if row.WireClass != "cnolibojkao" {
			continue
		}
		s.ensureRig(ctx, testUID, guid)
		ids := s.rigGearIDs(ctx, testUID, guid)
		spec := s.loadGearSpecForRig(ctx, ids, guid)
		t.Logf("starter %s spec=%+v", row.ItemID, spec)
		matched := 0
		for range 10 {
			if s.rollFishForRig(ctx, testUID, guid, "loc1_02", "worm_01", neutralFishingSkillBonus()) != nil {
				matched++
			}
		}
		if matched == 0 {
			t.Fatal("starter cannot receive any fish")
		}
	}
}

func TestMatchedWeightRejectsInsteadOfClipping(t *testing.T) {
	s := NewServer(config.Game{}, logging.Discard(), nil)
	calls := 0
	got := s.sampleMatchedWeight("c.roach", func() float64 {
		calls++
		if calls == 1 {
			return 5000
		}
		return 123
	}, 0.25)
	if got != 123 || calls != 2 {
		t.Fatalf("weight=%d calls=%d", got, calls)
	}
	if got := s.sampleMatchedWeight("thynnus_t", func() float64 { return 2_000_000 }, 0.25); got != 0 {
		t.Fatalf("impossible fish was resized: %d", got)
	}
	// 最强主线 205kg 的完整钓组仍可面对 300kg 金枪鱼，入门线不行。
	strong := gearPhysicsSpec{RodPowerKg: 238.392, LineStrengthKg: 205, LeaderStrengthKg: 205,
		ReelPresent: true, ReelDragKg: 205, HookPresent: true, HookStrengthKg: 660, HookLengthMM: 80}
	if got := s.sampleMatchedWeight("thynnus_t", func() float64 { return 300000 }, 0, strong); got != 300000 {
		t.Fatalf("strong sea setup cannot receive large tuna: %d", got)
	}
	strong.LineStrengthKg = 3
	if got := s.sampleMatchedWeight("thynnus_t", func() float64 { return 300000 }, 0, strong); got != 0 {
		t.Fatal("starter line received an uncontrollable giant")
	}
}

func TestLargestFishFitExistingTopGearAndHaveFiniteStamina(t *testing.T) {
	s, db := newBalanceTestServer(t)
	ctx := context.Background()
	load := func(id string) *store.ItemDef {
		def, err := db.GetItemDefByItemID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return def
	}
	hook := load("triple_21070")
	spec := gearPhysicsSpec{RodPowerKg: rodPowerKg(load("boatr_10222_SBH80_100")),
		LineStrengthKg: lineStrengthKg(load("braid_40907")), LeaderStrengthKg: leaderStrengthKg(load("mn_lm_40809")),
		ReelPresent: true, ReelDragKg: 205, HookPresent: true, HookStrengthKg: itemDefF32(hook, gearAttrMaxLoad), HookLengthMM: itemDefF32(hook, gearAttrHookLen)}
	rows, err := db.SQL.QueryContext(ctx, `SELECT w.fish_key, COALESCE(g.blue_trophy_g,w.super_trophy_line_g) FROM species_weight w LEFT JOIN fish_grade_lines g ON g.fish_key=w.fish_key WHERE COALESCE(g.blue_trophy_g,w.super_trophy_line_g)>0`)
	if err != nil {
		t.Fatal(err)
	}
	type specimen struct {
		key   string
		grams int64
	}
	var fish []specimen
	for rows.Next() {
		var f specimen
		if err := rows.Scan(&f.key, &f.grams); err != nil {
			t.Fatal(err)
		}
		f.grams = f.grams * 5 / 4
		fish = append(fish, f)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(fish) < 200 {
		t.Fatalf("incomplete species coverage: %d", len(fish))
	}
	for _, f := range fish {
		stamina := s.fishStaminaFor(&RolledFish{Species: f.key, WeightG: f.grams})
		if !isFinitePositive(stamina) {
			t.Fatalf("%s has invalid stamina", f.key)
		}
		if f.grams < 100000 {
			continue
		}
		if got := s.sampleMatchedWeight(f.key, func() float64 { return float64(f.grams) }, 0, spec); got != f.grams {
			t.Fatalf("%s %dg cannot match existing top equipment", f.key, f.grams)
		}
	}
}

func TestFishForceCurvesAndLoadReferenceStayConsistent(t *testing.T) {
	s := NewServer(config.Game{}, logging.Discard(), nil)
	s.loadFightCurves()
	for _, species := range []string{"c.roach", "a_salmon", "r_sturgeon", "thynnus_t", "unknown"} {
		fish := &RolledFish{FishGUID: "11111111111111111111111111111111", Species: species, WeightG: 100000, Grade: 2}
		tree := s.buildFish27(fish, "", "")
		force := f32At(tree, 8)
		curves := tree.Fields[21].Val
		if curves == nil || len(curves.Items) == 0 {
			t.Fatalf("%s missing curves", species)
		}
		if base := f32At(curves.Items[0], 8); math.Abs(base-force) > force*1e-6 {
			t.Fatalf("%s force=%g base tension=%g", species, force, base)
		}
		for _, row := range curves.Items {
			for i := 1; i <= 14; i++ {
				v := f32At(row, i)
				if math.IsNaN(v) || math.IsInf(v, 0) || ((i == 2 || i == 3 || i == 8 || i == 9) && v < 0) {
					t.Fatalf("%s invalid curve field %d=%g", species, i, v)
				}
			}
			if f32At(row, 2) > f32At(row, 3) {
				t.Fatal("reversed escape-phase time interval")
			}
		}
	}
}

func TestMatchedWeightHonorsDebugScalesAndHookBoundary(t *testing.T) {
	s, db := newBalanceTestServer(t)
	ctx := context.Background()
	for k, v := range map[string]string{"fish_weight_scale": "4", "fight_power_scale": "2"} {
		if err := db.TunableSet(ctx, k, v, "test"); err != nil {
			t.Fatal(err)
		}
	}
	spec := gearPhysicsSpec{RodPowerKg: 100, LineStrengthKg: 100, HookPresent: true, HookStrengthKg: 80, HookLengthMM: 5}
	if got := s.sampleMatchedWeight("c.roach", func() float64 { return 250 }, 0, spec); got != 1000 {
		t.Fatal(got)
	}
	if got := s.sampleMatchedWeight("c.roach", func() float64 { return 1000 }, 0, spec); got != 0 {
		t.Fatal("debug bypassed hook mouth-size boundary")
	}
	spec = gearPhysicsSpec{RodPowerKg: 1, LineStrengthKg: 1}
	if got := s.sampleMatchedWeight("c.roach", func() float64 { return 200 }, 0, spec); got != 0 {
		t.Fatal("power scale bypassed load boundary")
	}
}

func TestExistingFishingJSONAndReelPerformanceSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")
	ctx := context.Background()
	var guid string
	for attempt := range 3 {
		db, err := store.OpenWithStatic(path, filepath.Join("..", "..", "server_data", "static.sqlite3"))
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Migrate(); err != nil {
			db.Close()
			t.Fatal(err)
		}
		s := NewServerForGearTest(t, db)
		if attempt == 0 {
			reel, err := s.CreateItem(ctx, testUID, "spin_5140", "backpack", 1)
			if err != nil {
				db.Close()
				t.Fatal(err)
			}
			guid = reel.GUID
			if err := db.SaveFishingState(ctx, &store.FishingState{UID: testUID, Pending: &store.FishingFish{FishGUID: "11111111111111111111111111111111", Species: "a_salmon", WeightG: 5000, Force: 42, Stamina: 1234}}); err != nil {
				db.Close()
				t.Fatal(err)
			}
		}
		if _, err := s.RebuildReelPerformance(ctx); err != nil {
			db.Close()
			t.Fatal(err)
		}
		perf, err := db.GetReelPerformance(ctx, guid)
		if err != nil || math.Abs(perf.LockedDragKg-99) > 1e-6 {
			db.Close()
			t.Fatalf("rebuild lost model rating: %+v %v", perf, err)
		}
		state, err := db.LoadFishingState(ctx, testUID)
		if err != nil || state == nil || state.Pending == nil {
			db.Close()
			t.Fatalf("pending lost: %v", err)
		}
		fish := rolledFishFromStore(state.Pending)
		if fish.Force != 42 || fish.Stamina != 1234 || fish.WeightG != 5000 {
			db.Close()
			t.Fatalf("pending changed: %+v", fish)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	var legacy store.FishingFish
	if err := json.Unmarshal([]byte(`{"species":"c.roach","weight_g":63}`), &legacy); err != nil {
		t.Fatal(err)
	}
	f := rolledFishFromStore(&legacy)
	if f.Force != 0 || f.Stamina != 0 || f.WeightG != 63 {
		t.Fatal("old JSON default changed")
	}
	if _, err := proto.EncodeValue(NewServer(config.Game{}, logging.Discard(), nil).buildFish27(f, "", ""), NewServer(config.Game{}, logging.Discard(), nil).Table()); err != nil {
		t.Fatal(err)
	}
}
