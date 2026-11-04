package gameserver

import (
	"math"
	"strings"
)

const (
	fishingWeatherUnknown = -1
	fishingWeatherClear   = iota - 1
	fishingWeatherFewClouds
	fishingWeatherCloudy
	fishingWeatherRain
	fishingWeatherHeavyRain
	fishingWeatherThunderstorm
)

// 气温、天气和钓深只近似影响吃口；以下数值是本地玩法设定，不是实测水温。
func fishWeatherActivity(species string, weather fishingWeather, hour int64, depthCM int64) float64 {
	if !weather.known {
		return 1
	}
	surface := 1 / (1 + math.Max(0, float64(depthCM))/400)
	hour = (hour%24 + 24) % 24
	daylight := (1 + math.Cos((float64(hour)-12)*math.Pi/12)) / 2
	activity := 1.0
	if low, high, known := fishWeatherTemperatureBand(species); known && finiteWeatherValue(weather.airTemp) {
		distance := math.Max(0, math.Max(low-weather.airTemp, weather.airTemp-high))
		thermal := 0.2 + 0.8*math.Exp(-math.Pow(distance/9, 2))
		activity *= 1 - (1-thermal)*(0.65+0.35*surface)
		if finiteWeatherValue(weather.previousAirTemp) {
			change := math.Abs(weather.airTemp - weather.previousAirTemp)
			activity *= 1 - 0.25*(1-math.Exp(-change/5))*(0.5+0.5*surface)
		}
	}
	// kind 来自客户端同一小时计划的实际晴雨；未识别的分类不修正吃口。
	switch weather.kind {
	case fishingWeatherClear:
		activity *= 0.98 - 0.10*daylight*surface
	case fishingWeatherFewClouds:
		activity *= 0.98 - 0.04*daylight*surface
	case fishingWeatherCloudy:
		activity *= 0.98
	case fishingWeatherRain:
		activity *= 1
	case fishingWeatherHeavyRain:
		activity *= 0.68 + 0.10*(1-surface)
	case fishingWeatherThunderstorm:
		activity *= 0.45 + 0.15*(1-surface)
	}
	if finiteWeatherValue(weather.wind) && weather.wind >= 0 {
		breeze := 0.94 + 0.06*math.Exp(-math.Pow((weather.wind-3)/3, 2))
		strongWind := math.Max(0, weather.wind-6) / 8
		windActivity := breeze * (0.6 + 0.4*math.Exp(-strongWind*strongWind))
		activity *= 1 - (1-windActivity)*surface
	}
	if finiteWeatherValue(weather.fog) {
		activity *= 1 - 0.08*clampFloat(weather.fog, 0, 1)*surface*(0.5+0.5*daylight)
	}
	return clampFloat(activity, 0.1, 1)
}

func finiteWeatherValue(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

// 搏鱼模板不等同于温度习性；未列出的鱼种保持中性气温响应。
func fishWeatherTemperatureBand(species string) (low, high float64, known bool) {
	for _, suffix := range []string{"_albino", "_ghost", "_red"} {
		if strings.HasSuffix(species, suffix) {
			return fishWeatherTemperatureBand(strings.TrimSuffix(species, suffix))
		}
	}
	switch species {
	case "a_salmon", "l_salmon", "k_salmon", "chavycha", "keta", "kijuch", "nerka", "gorbusha", "taimen", "riv_brown_trout", "riv_rb_trout", "sevan_trout", "gc_smelt", "mikija", "lenok_o", "lenok_t",
		"a_char", "chartus_kuoriius", "dr_char", "golets_lev", "kunja", "malma", "neiva", "palia_kr", "palia_lud", "salvel_l",
		"a_omul", "b_omul", "chir", "muksun", "nelma", "peled", "ripus", "sig", "sig_black", "sig_kuor", "sig_ludoga", "sig_o_l", "sig_ostr", "sig_p", "sig_valaam", "sig_vouksa", "sv_sig", "syb_vendace", "tugun", "valyok", "vendace", "wfish", "burbot":
		return 4, 15, true
	case "carp", "common_carp", "c.carp", "m_carp", "l_carp", "r_carp", "n_carp", "g_carp", "b_carp", "bh_carp", "crucian", "karasekarp", "din_l_carp", "din_m_carp", "din_n_carp", "midori_g",
		"hi_utsri", "kohaku", "m_goshiki", "n_asagi", "o_ogon", "yotsushiro", "tench", "g_tench", "kw_tench", "rudd", "w_catfish", "w_catfish_a", "a_catfish", "ch_catfish", "bp_bass":
		return 18, 27, true
	case "n.pike", "perch", "e.pikeperch", "walleye", "asp", "sm_bass", "lm_b_bass", "sp_bass":
		return 10, 23, true
	}
	return 0, 0, false
}
