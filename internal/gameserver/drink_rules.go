package gameserver

import "time"

// drinkRule 是本地设定的饮酒经验规则。
type drinkRule struct {
	Category string
	Peak     float64
	Duration time.Duration
}

func drinkRuleForCategory(category string) (drinkRule, bool) {
	switch category {
	case "beer":
		return drinkRule{Category: category, Peak: .05, Duration: 5 * time.Minute}, true
	case "wine":
		return drinkRule{Category: category, Peak: .10, Duration: 10 * time.Minute}, true
	case "spirit":
		return drinkRule{Category: category, Peak: .15, Duration: 15 * time.Minute}, true
	case "mead":
		return drinkRule{Category: category, Peak: .20, Duration: 20 * time.Minute}, true
	default:
		return drinkRule{}, false
	}
}

// 白名单按 4.0.25034 饮品定义和名称分类，不包含微量含酒食物或无酒精替代品。
func drinkRuleForItem(itemID string) (drinkRule, bool) {
	var category string
	switch itemID {
	case "kvas_bottle", "kvas_glass",
		"lb_bottle_ch_ale_ny", "lb_bottle_eks", "lb_bottle_eks_ny",
		"lb_bottle_krep", "lb_bottle_krep_ny", "lb_bottle_trad", "lb_bottle_trad_ny",
		"wood_bottle_cask", "wood_bottle_dark", "wood_bottle_lager",
		"wood_can_cask", "wood_can_dark", "wood_can_lager":
		category = "beer"
	case "AV_shamp_1l", "AV_shamp_1l_8", "AV_shamp_1lg", "AV_shamp_p_6l", "AV_shamp_tsr",
		"apple_sidr", "g_vine_bottle",
		"rd_ice_vine", "rd_ice_vine25", "rd_ice_vine26",
		"rd_red_vine", "rd_red_vine_2017", "rd_red_vine_8", "rd_white_vine",
		"rot_bottle_porto", "thermos_gwein", "vine_bottle_em":
		category = "wine"
	case "XXIVA_bottle_vdka", "boyar_bottle_vdka", "gorilka", "hooch_corn", "hooch_potato",
		"kala_vodka", "kr_pennik", "pervach", "tungus_vodka", "vodka_glass",
		"zdn_bottle_cogn3", "zdn_bottle_cogn5", "zdn_bottle_vdka":
		category = "spirit"
	case "barrel_honey", "pm_medovuha", "pm_medovuha_g", "pm_medovuha_g_8", "sbiten":
		category = "mead"
	default:
		return drinkRule{}, false
	}
	return drinkRuleForCategory(category)
}

// drinkRate 前 75% 时间为峰值，最后 25% 时间线性降至零。
func drinkRate(category string, remaining time.Duration) float64 {
	rule, ok := drinkRuleForCategory(category)
	if !ok || remaining <= 0 {
		return 0
	}
	tail := rule.Duration / 4
	if remaining >= tail {
		return rule.Peak
	}
	return rule.Peak * float64(remaining) / float64(tail)
}
