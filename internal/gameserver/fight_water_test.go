package gameserver

import (
	"context"
	"math"
	"testing"
)

func TestFightWaterDirectionDensityAndQuadraticFlow(t *testing.T) {
	angle := 0.0
	spec := gearPhysicsSpec{LineDiameterMM: 0.4, LinePresent: true, LineLengthM: 100,
		Water: fightWaterContext{Density: 1000, CurrentMPS: 0.5, FishDepthM: 10, CurrentBearingDeg: &angle, PullBearingDeg: &angle}}
	along := evaluateFightWater(spec, "c.carp", 10000, 0.7)
	spec.Water.CurrentMPS = 1
	double := evaluateFightWater(spec, "c.carp", 10000, 0.7)
	if math.Abs(double.DragN-along.DragN*4) > 1e-9 || !double.DirectionKnown {
		t.Fatal("flow must follow relative speed squared", along, double)
	}
	spec.Water.Density = 1025
	sea := evaluateFightWater(spec, "c.carp", 10000, 0.7)
	if math.Abs(sea.DragN/double.DragN-1.025) > 1e-9 {
		t.Fatal("salt water density not used")
	}
	force := 100.0
	down := waterCombinedForce(force, sea, true)
	crossAngle, upAngle := 90.0, 180.0
	spec.Water.CurrentBearingDeg = &crossAngle
	cross := waterCombinedForce(force, evaluateFightWater(spec, "c.carp", 10000, 0.7), true)
	spec.Water.CurrentBearingDeg = &upAngle
	upWater := evaluateFightWater(spec, "c.carp", 10000, 0.7)
	up := waterCombinedForce(force, upWater, true)
	if !(down > cross && cross > force && force > up) {
		t.Fatalf("direction lost: along=%g cross=%g against=%g", down, cross, up)
	}
	if got := waterCombinedForce(force, upWater, false); got < force {
		t.Fatal("matching relies on temporary current assistance")
	}
	spec.Water.CurrentMPS = 0
	still := evaluateFightWater(spec, "c.carp", 10000, 0.7)
	if still.DragN != 0 || still.PressurePa <= 0 || waterCombinedForce(force, still, true) != force {
		t.Fatal("hydrostatic pressure was mistaken for line tension", still)
	}
}

func TestFightWaterMethodSpotOverridesAndDepthBoundary(t *testing.T) {
	s, db := newBalanceTestServer(t)
	ctx := context.Background()
	s.setLevel(testUID, "level_008_sura_river")
	profile := `{"level_008_sura_river":{"kind":"river","water_depth_m":12},"loc_test:42":{"kind":"backwater","current_mps":0.12,"water_depth_m":8,"current_bearing_deg":90,"pull_bearing_deg":0}}`
	if err := db.TunableSet(ctx, "fight_water_profiles", profile, "test"); err != nil {
		t.Fatal(err)
	}
	spot := fightWaterSpot{Spot: 42, BaitDepthCM: 150}
	float := s.fightWaterForRig(ctx, testUID, "loc_test", 1, spot)
	bottom := s.fightWaterForRig(ctx, testUID, "loc_test", 12, spot)
	if float.Kind != "backwater" || float.CurrentMPS != 0.12 || float.FishDepthM != 1.5 || bottom.FishDepthM != 8 {
		t.Fatal("method or spot profile ignored", float, bottom)
	}
	if defaultFightWater("rapid").CurrentMPS <= defaultFightWater("river").CurrentMPS || defaultFightWater("pond").CurrentMPS != 0 {
		t.Fatal("water types lost distinct inputs")
	}
	spec := gearPhysicsSpec{RodPowerKg: 20, LineStrengthKg: 10, LinePresent: true, LineLengthM: 5, Water: bottom}
	if got := s.sampleMatchedWeight("c.carp", func() float64 { return 1000 }, 0, spec); got != 0 {
		t.Fatal("fish deeper than available line still matched")
	}
	spec.LineLengthM = 100
	spec.Water.FishDepthM = 9
	if got := s.sampleMatchedWeight("c.carp", func() float64 { return 1000 }, 0, spec); got != 0 {
		t.Fatal("fish below configured water bottom still matched")
	}
	spec.Water = defaultFightWater("pond")
	stillCap := s.matchedWeightCapG("c.carp", 0, spec)
	spec.Water = defaultFightWater("rapid")
	if cap := s.matchedWeightCapG("c.carp", 0, spec); cap <= 0 || cap >= stillCap {
		t.Fatal("water load did not use gear's force budget", stillCap, cap)
	}
	if err := db.TunableSet(ctx, "fight_water_profiles", `{"level_008_sura_river":{"kind":"river","water_depth_m":12},"loc_test:42":{"kind":"sea","current_mps":-1}}`, "test"); err != nil {
		t.Fatal(err)
	}
	if fallback := s.fightWaterForRig(ctx, testUID, "loc_test", 12, spot); fallback.Kind != "river" || fallback.WaterDepthM != 12 {
		t.Fatal("invalid spot replaced valid map profile", fallback)
	}
}

func TestTwoTonneFishSharesGearWaterAndForceBudget(t *testing.T) {
	s, _ := newBalanceTestServer(t)
	s.loadFightCurves()
	spec := gearPhysicsSpec{RodPowerKg: 238.392, RodLengthM: 3, RodTaper: 0.3,
		LinePresent: true, LineStrengthKg: 205, LineLengthM: 1200, LineDiameterMM: 1,
		LeaderStrengthKg: 181, ReelPresent: true, ReelDragKg: 205, ReelBaseDragKg: 80,
		HookPresent: true, HookStrengthKg: 660, HookLengthMM: 76, Water: defaultFightWater("sea")}
	spec.Water.WaterDepthM, spec.Water.FishDepthM = 300, 100
	fish := &RolledFish{Species: "cetorhinus_m", WeightG: 2_000_000, LengthM: fishBodyLength("cetorhinus_m", 2_000_000, 0.0355, 1.0/3)}
	a := s.analyzeFishFight(fish, spec)
	if !isFinitePositive(a.InitialStamina) || a.PotentialPeak <= a.PotentialLoad || a.WaterDragN <= 0 || a.DepthBlocked {
		t.Fatal(a)
	}
	if got := s.sampleMatchedWeight(fish.Species, func() float64 { return float64(fish.WeightG) }, 0, spec); got != fish.WeightG {
		t.Fatal("existing high-end setup cannot match 2t basking shark", got, a)
	}
	weak := spec
	weak.LineStrengthKg = 5
	if got := s.sampleMatchedWeight(fish.Species, func() float64 { return float64(fish.WeightG) }, 0, weak); got != 0 {
		t.Fatal("weak mixed tackle inherited top rod's strength")
	}
	for _, rigType := range []int64{1, 2, 5, 12, 16, 50, 55} {
		spec.RigType = rigType
		if other := s.analyzeFishFight(fish, spec); other.ForceN != a.ForceN || other.InitialStamina != a.InitialStamina {
			t.Fatal("fishing method changed the same fish's intrinsic traits")
		}
	}
	t.Logf("2t basking: length=%.3fm force=%.2fN water=%.2fN load=%.3f peak=%.3f stamina=%.2f", fish.LengthM, a.ForceN, a.WaterDragN, a.PotentialLoad, a.PotentialPeak, a.InitialStamina)
}
