package gameserver

import (
	"math"
	"strings"
)

// 系数用于本地玩法平衡；自然资料只提供体型、游泳和栖息习性的依据。
type fishPerformance struct {
	pull, stamina, pullExponent, staminaExponent float64
	motion                                       string
}

func performanceForFish(species string) fishPerformance {
	p := fishPerformance{pull: 1, stamina: 1, pullExponent: 2.0 / 3, staminaExponent: 0.75}
	switch species {
	case "carp", "common_carp", "c.carp", "m_carp", "l_carp", "r_carp", "n_carp", "g_carp", "b_carp", "bh_carp":
		p = fishPerformance{pull: 1.1, stamina: 1.35, pullExponent: 2.0 / 3, staminaExponent: 0.78, motion: "c.carp"}
	case "a_salmon", "l_salmon", "k_salmon", "chavycha", "keta", "kijuch", "nerka", "gorbusha", "taimen", "riv_brown_trout", "riv_rb_trout", "sevan_trout":
		p = fishPerformance{pull: 1.25, stamina: 1.5, pullExponent: 0.70, staminaExponent: 0.80, motion: "e.chub"}
	case "beluga", "ch_beluga", "kaluga", "sterlet", "syb_sterlet", "r_sturgeon", "l_sturgeon", "p_sturgeon", "st_sturgeon", "zs_sturgeon", "osetr_balt", "osetr_ship", "w_catfish", "w_catfish_a", "a_catfish", "ch_catfish":
		p = fishPerformance{pull: 1.15, stamina: 1.8, pullExponent: 0.64, staminaExponent: 0.82, motion: "c.carp"}
	case "thynnus_t", "x_gladius", "lamna_n":
		p = fishPerformance{pull: 1.5, stamina: 2, pullExponent: 0.70, staminaExponent: 0.80, motion: "e.chub"}
	case "cetorhinus_m", "somniosus_m":
		p = fishPerformance{pull: 1.1, stamina: 2, pullExponent: 0.64, staminaExponent: 0.80, motion: "c.carp"}
	case "atl_cod", "coalfish", "pollachius", "halibut_b", "halibut_w", "hippoglos_p", "molva", "m_molva", "conger":
		p = fishPerformance{pull: 1.05, stamina: 1.35, pullExponent: 0.65, staminaExponent: 0.80, motion: "c.carp"}
	case "n.pike", "e.pikeperch", "walleye", "asp", "sm_bass", "lm_b_bass", "bp_bass":
		p = fishPerformance{pull: 1.15, stamina: 1.2, pullExponent: 0.68, staminaExponent: 0.73, motion: "perch"}
	}
	// 鲤鱼颜色变种沿用同一体型和搏鱼特性。
	if strings.HasSuffix(species, "_albino") || strings.HasSuffix(species, "_ghost") || strings.HasSuffix(species, "_red") {
		base := species[:strings.LastIndex(species, "_")]
		if base != species {
			return performanceForFish(base)
		}
	}
	return p
}

// 大个体的力和体力持续增加，但不按质量无限线性放大。
func fishMassScale(weightG, exponent float64) float64 {
	if weightG <= 1000 {
		return math.Max(0, weightG)
	}
	return 1000 * math.Pow(weightG/1000, exponent)
}

func fishBaseForce(species string, weightG float64) float64 {
	return verifiedForcePerGram * fishMassScale(weightG, performanceForFish(species).pullExponent) * performanceForFish(species).pull
}

// fishWeightForPull 将 kgf 预算还原为鱼重；协议拉力仍以牛顿发送。
func fishWeightForPull(species string, pullKg float64) float64 {
	w := pullKg * 1000 / performanceForFish(species).pull
	if w > 1000 {
		w = 1000 * math.Pow(w/1000, 1/performanceForFish(species).pullExponent)
	}
	return w
}

func (s *Server) fishForceFor(f *RolledFish) float64 {
	if f == nil {
		return 0
	}
	pull := f.Force
	if pull <= 0 {
		pull = fishBaseForce(f.Species, float64(f.WeightG))
	}
	if k := s.tunableF("fight_power_scale", 1); k > 0 {
		pull *= k
	}
	return pull
}

// sampleMatchedWeight 拒绝不匹配的重量，避免把所有大鱼裁成同一极限重量。
func (s *Server) sampleMatchedWeight(species string, sample func() float64, capKg float64, specs ...gearPhysicsSpec) int64 {
	if len(specs) > 0 && (specs[0].BrokenPart != "" || specs[0].InvalidLeader || (specs[0].LinePresent && specs[0].LineLengthM <= 0)) {
		return 0
	}
	if len(specs) > 0 && evaluateFightWater(specs[0], species, 0, 0).DepthBlocked {
		return 0
	}
	capG := s.matchedWeightCapG(species, capKg, specs...)
	if capG < 0 {
		return 0
	}
	scale := s.tunableF("fish_weight_scale", 1)
	if scale <= 0 {
		scale = 1
	}
	for range 64 {
		raw := sample()
		if raw == 0 {
			return 0
		}
		w := math.Round(raw * scale)
		if math.IsNaN(w) || math.IsInf(w, 0) || w < 1 || w >= float64(math.MaxInt64) {
			continue
		}
		if capG <= 0 || w <= capG {
			return int64(w)
		}
	}
	return 0
}

func (s *Server) matchedWeightCapG(species string, capKg float64, specs ...gearPhysicsSpec) float64 {
	capG := capKg * 1000
	if len(specs) > 0 {
		spec := specs[0]
		capG = 0
		if limit := gearBreakLimitKg(spec); limit > 0 {
			power := s.tunableF("fight_power_scale", 1)
			if power <= 0 {
				power = 1
			}
			peak := 1 + (s.fishCurvePeakRatio(species)-1)*tackleShockFactor(spec)
			capG = fishWeightForPull(species, limit*gearOverloadFactor/(power*peak))
			capG = s.waterMatchedWeightCap(spec, species, capG, power, peak)
			if capG < 0 {
				return capG
			}
		}
		if spec.HookPresent {
			hookCap := hookWeightCapG(spec.HookLengthMM, spec.HookSize) * gearOverloadFactor
			if hookCap > 0 && (capG <= 0 || hookCap < capG) {
				capG = hookCap
			}
		}
	}
	return capG
}
