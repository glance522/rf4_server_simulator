package gameserver

import (
	"math"
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
)

func TestFishFightAnalysisKeepsFishIndependentOfGear(t *testing.T) {
	s := NewServer(config.Game{}, logging.Discard(), nil)
	s.loadFightCurves()
	for _, species := range []string{"c.roach", "a_salmon", "beluga", "thynnus_t", "cetorhinus_m", "unknown"} {
		for _, grams := range []int64{1, 1000, 100000, 2500000} {
			fish := &RolledFish{Species: species, WeightG: grams}
			weak := s.analyzeFishFight(fish, gearPhysicsSpec{RodPowerKg: 20, LineStrengthKg: 2})
			strong := s.analyzeFishFight(fish, gearPhysicsSpec{RodPowerKg: 20, LineStrengthKg: 20})
			if weak.ForceN != strong.ForceN || weak.InitialStamina != strong.InitialStamina {
				t.Fatal("gear changed the same fish's intrinsic performance")
			}
			if !isFinitePositive(weak.EndurancePerNewton) || !isFinitePositive(weak.PotentialPeak) || weak.PotentialPeak < weak.PotentialLoad {
				t.Fatalf("invalid analysis: %+v", weak)
			}
			if math.Abs(weak.PotentialLoad-strong.PotentialLoad*10) > weak.PotentialLoad*1e-9 {
				t.Fatal("gear margin does not follow actual weakest-link strength")
			}
			curves := s.buildFightCurves(species, weak.ForceN)
			peak := weak.ForceN
			for _, row := range curves.Items {
				peak = math.Max(peak, math.Max(f32At(row, 8), f32At(row, 9)))
			}
			if math.Abs(peak-weak.PeakForceN) > peak*1e-6 {
				t.Fatal("analysis disagrees with transmitted peak tension")
			}
		}
	}
}

func TestFishFightAnalysisHandlesMissingFishAndGear(t *testing.T) {
	s := NewServer(config.Game{}, logging.Discard(), nil)
	if got := s.analyzeFishFight(nil, gearPhysicsSpec{}); got != (fishFightAnalysis{}) {
		t.Fatalf("missing data invented a load: %+v", got)
	}
}
