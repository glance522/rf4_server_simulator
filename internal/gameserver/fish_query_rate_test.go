package gameserver

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

func TestMeanBiteDelay(t *testing.T) {
	for _, tc := range []struct {
		name                string
		cfg                 biteDelaySettings
		lure                bool
		weather, speed      float64
		lmin, lmax, lureCap float64
		want                float64
	}{
		{"fixed", biteDelaySettings{fixed: 2}, false, 100, .1, 0, 0, 0, 2},
		{"fixed-floor", biteDelaySettings{fixed: .001}, true, 100, .1, 0, 0, 0, .05},
		{"ordinary-cap-before-scale", biteDelaySettings{maxDelay: 5, scale: .5}, false, 1, 1, 0, 0, 0, 2.5},
		{"lure-uniform", biteDelaySettings{scale: 1}, true, 1, 1, 1.5, 6, 0, 3.75},
		{"lure-cap", biteDelaySettings{scale: 1, lureMax: 3}, true, 1, 1, 1.5, 6, 3, 2.75},
		{"lure-weather-skill", biteDelaySettings{scale: 1}, true, 2, .5, 1.5, 6, 0, 3.75},
		{"lure-final-floor", biteDelaySettings{scale: .01}, true, 1, 1, 1.5, 6, 0, .8},
		{"lure-mid-window", biteDelaySettings{scale: 1}, true, 1, 1, 10, 60, 0, 35},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := meanBiteDelay(tc.cfg, tc.lure, tc.weather, tc.speed, tc.lmin, tc.lmax, tc.lureCap); math.Abs(got-tc.want) > 1e-10 {
				t.Fatalf("mean=%g want=%g", got, tc.want)
			}
		})
	}
	// Clipping and expectation do not commute.
	cfg := biteDelaySettings{maxDelay: 20, scale: .7}
	var want float64
	for _, x := range officialPushDelayEligible {
		want += standardBiteDelay(x*1.1, .9, 20, .7)
	}
	want /= float64(len(officialPushDelayEligible))
	if got := meanBiteDelay(cfg, false, 1.1, .9, 0, 0, 0); math.Abs(got-want) > 1e-10 {
		t.Fatalf("%g != %g", got, want)
	}
}

func TestFishQueryRateAndRanking(t *testing.T) {
	a, b := FishQueryPlan{Chance: .45}, FishQueryPlan{Chance: .2}
	a.setTheoreticalRate(10)
	b.setTheoreticalRate(2)
	if a.TheoreticalPerMinute != 2.7 || a.TheoreticalPerHour != 162 || !fishQueryPlanBetter(b, a) {
		t.Fatalf("wrong conversion/ranking: a=%+v b=%+v", a, b)
	}
	for _, delay := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		p := FishQueryPlan{Chance: .5}
		p.setTheoreticalRate(delay)
		if p.TheoreticalPerHour != 0 {
			t.Fatalf("invalid delay produced rate: %+v", p)
		}
	}
}

func TestFishQueryDelayUsesWaterOverride(t *testing.T) {
	s, db := seedBaitWeightTest(t)
	setWeatherTestTunable(t, s, "bite_delay_fixed", "10")
	st := &FishState{Loc: "level_900_rules", Bait: "rule_worm", Bonus: neutralFishingSkillBonus()}
	if got := s.fishQueryMeanDelay(st, fishingWeather{}, time.Now(), neutralFishPaceState()); got != 10 {
		t.Fatal(got)
	}
	if _, err := db.SQL.Exec(`INSERT OR REPLACE INTO fish_bite_setting(loc,bite_probability,bite_delay_fixed,bite_delay_scale,bite_delay_max,bite_delay_lure_max) VALUES('rules-test',1,2,1,20,6)`); err != nil {
		t.Fatal(err)
	}
	if got := s.fishQueryMeanDelay(st, fishingWeather{}, time.Now(), neutralFishPaceState()); got != 2 {
		t.Fatal(got)
	}
}

func TestFishQueryGradeRatioMatchesSampling(t *testing.T) {
	t.Setenv("GODEBUG", "randseednop=0")
	s, _ := seedBaitWeightTest(t)
	setWeatherTestTunable(t, s, "grade_ratio_enabled", "1")
	v, star, blue := int64(100), int64(1000), int64(2000)
	lines := cachedGradeLines{val: &v, trophy: &star, blue: &blue}
	for _, capG := range []float64{0, 1200, 999.5, 20} {
		model, ok := s.gradeRatioModel(lines, 1.2, 1.25, capG)
		if !ok {
			t.Fatal("unexpected fallback")
		}
		for _, minimum := range []int64{1, 100, 1000, 2000} {
			rand.Seed(17)
			const n = 12000
			var matched int
			for range n {
				w, _ := s.sampleWeightByGradeRatio(lines, 1.2, 1.25, capG)
				if w >= minimum {
					matched++
				}
			}
			want, got := model.survival(minimum), float64(matched)/n
			if math.Abs(got-want) > .015 {
				t.Fatalf("cap=%g minimum=%d estimate=%g observed=%g", capG, minimum, want, got)
			}
		}
	}
}
