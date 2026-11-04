package gameserver

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestDrinkRulesClassifyAlcoholicDrinks(t *testing.T) {
	groups := []struct {
		category string
		peak     float64
		duration time.Duration
		items    string
	}{
		{"beer", .05, 5 * time.Minute, `
			kvas_bottle kvas_glass lb_bottle_ch_ale_ny lb_bottle_eks lb_bottle_eks_ny
			lb_bottle_krep lb_bottle_krep_ny lb_bottle_trad lb_bottle_trad_ny
			wood_bottle_cask wood_bottle_dark wood_bottle_lager
			wood_can_cask wood_can_dark wood_can_lager`},
		{"wine", .10, 10 * time.Minute, `
			AV_shamp_1l AV_shamp_1l_8 AV_shamp_1lg AV_shamp_p_6l AV_shamp_tsr
			apple_sidr g_vine_bottle rd_ice_vine rd_ice_vine25 rd_ice_vine26
			rd_red_vine rd_red_vine_2017 rd_red_vine_8 rd_white_vine
			rot_bottle_porto thermos_gwein vine_bottle_em`},
		{"spirit", .15, 15 * time.Minute, `
			XXIVA_bottle_vdka boyar_bottle_vdka gorilka hooch_corn hooch_potato
			kala_vodka kr_pennik pervach tungus_vodka vodka_glass
			zdn_bottle_cogn3 zdn_bottle_cogn5 zdn_bottle_vdka`},
		{"mead", .20, 20 * time.Minute, `
			barrel_honey pm_medovuha pm_medovuha_g pm_medovuha_g_8 sbiten`},
	}
	seen := make(map[string]bool)
	for _, group := range groups {
		for _, item := range strings.Fields(group.items) {
			if seen[item] {
				t.Fatalf("duplicate drink %q", item)
			}
			seen[item] = true
			t.Run(item, func(t *testing.T) {
				rule, ok := drinkRuleForItem(item)
				if !ok || rule.Category != group.category || rule.Peak != group.peak || rule.Duration != group.duration {
					t.Fatalf("rule = %+v, %v; want %s %g %s", rule, ok, group.category, group.peak, group.duration)
				}
			})
		}
	}
	if len(seen) != 50 {
		t.Fatalf("classified %d drinks, want 50", len(seen))
	}
}

func TestDrinkRulesExcludeFoodAndNonAlcoholicDrinks(t *testing.T) {
	for _, item := range strings.Fields(`
		ff_honey ff_pack_kefir pm_honey_l apples baked_potato pill_pack
		goldimp_bot goldimp_bot_g kala_bot tungus_bot spicy_pepper
		AV_shamp_p_a0 AV_shamp_tsr_a0 pm_medovuha_alcfree pm_medovuha_alcfree_g
		kvarta_bot kvarta_bot_g lb_bottle_ale_ny_a0 lr_dr_can lr_dr_g pl_class_bot pl_dark_bot
		ang_bottle ang_can_8 big_coffee small_coffee thermos_cacao thermos_coffee thermos_tea
		mc_bottle_cola mc_can_cola m_bottle_ny26 lb_bottle_lim lb_bottle_lim_ny
		barrel_kvas kisel_jug mors_bottle_hm pil_bottle brand_coffe
		TUNGUS_VODKA tungus_vodka_unknown unknown
	`) {
		t.Run(item, func(t *testing.T) {
			if rule, ok := drinkRuleForItem(item); ok || rule != (drinkRule{}) {
				t.Fatalf("non-whitelisted item %q has rule %+v", item, rule)
			}
		})
	}
	if _, ok := drinkRuleForItem(""); ok {
		t.Fatal("empty item has a drink rule")
	}
}

func TestDrinkRateDecayBoundaries(t *testing.T) {
	for _, category := range []string{"beer", "wine", "spirit", "mead"} {
		rule, _ := drinkRuleForCategory(category)
		t.Run(category, func(t *testing.T) {
			for _, tc := range []struct {
				name      string
				remaining time.Duration
				fraction  float64
			}{
				{"above_duration", rule.Duration * 2, 1},
				{"start", rule.Duration, 1},
				{"half_elapsed", rule.Duration / 2, 1},
				{"tail_start", rule.Duration / 4, 1},
				{"tail_half", rule.Duration / 8, .5},
				{"tail_quarter", rule.Duration / 16, .25},
				{"expired", 0, 0},
				{"negative", -time.Second, 0},
			} {
				t.Run(tc.name, func(t *testing.T) {
					got, want := drinkRate(category, tc.remaining), rule.Peak*tc.fraction
					if math.Abs(got-want) > 1e-12 || got < 0 || got > .20 {
						t.Fatalf("rate = %g, want %g", got, want)
					}
				})
			}
			before := drinkRate(category, rule.Duration/4-time.Nanosecond)
			if before >= rule.Peak || before <= 0 {
				t.Fatalf("rate immediately after decay starts = %g", before)
			}
			last := drinkRate(category, time.Nanosecond)
			if last <= 0 || last >= before {
				t.Fatalf("rate immediately before expiry = %g", last)
			}
		})
	}
	if got := drinkRate("unknown", time.Hour); got != 0 {
		t.Fatalf("unknown category rate = %g", got)
	}
}
