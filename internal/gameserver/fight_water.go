package gameserver

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// 流速是本地玩法预设，不是地图实测；地点/钓点配置可覆盖，未知水域静水兜底。
type fightWaterContext struct {
	Kind              string   `json:"kind"`
	CurrentMPS        float64  `json:"current_mps"`
	Density           float64  `json:"density_kg_m3"`
	WaterDepthM       float64  `json:"water_depth_m"`
	FishDepthM        float64  `json:"fish_depth_m"`
	CurrentBearingDeg *float64 `json:"current_bearing_deg,omitempty"`
	PullBearingDeg    *float64 `json:"pull_bearing_deg,omitempty"`
}

var fightWaterKinds = map[string]string{
	"loc1_02": "pond", "level_018_moskitnoe": "pond", "level_019_american_pond": "pond",
	"level_001_lake": "lake", "level_003_viiveri_lake": "lake", "level_004_torfyanoe_ozero": "lake",
	"level_006_ladoga": "lake", "level_007_grass_lake": "lake", "level_011_ladoga_02": "lake", "level_014_carp_lake": "lake",
	"level_002_kirzah_river": "river", "level_005_volhov": "river", "level_008_sura_river": "river",
	"level_009_ahtuba": "river", "level_010_belaya": "river", "level_013_sev_don": "river",
	"level_015_tunguska": "river", "level_016_yama": "river", "level_012_north_sea": "sea",
}

func defaultFightWater(kind string) fightWaterContext {
	w := fightWaterContext{Kind: kind, Density: 1000}
	switch kind {
	case "lake":
		w.CurrentMPS = 0.05
	case "river":
		w.CurrentMPS = 0.35
	case "rapid":
		w.CurrentMPS = 1
	case "backwater":
		w.CurrentMPS = 0.1
	case "sea":
		w.CurrentMPS, w.Density = 0.25, 1025
	}
	return w
}

type fightWaterSpot struct {
	Spot        uint64
	BaitDepthCM int64
}

func (s *Server) fightWaterForRig(ctx context.Context, uid int64, loc string, rigType int64, spot fightWaterSpot) fightWaterContext {
	level := s.currentLevel(uid)
	kind := fightWaterKinds[loc]
	if kind == "" {
		kind = fightWaterKinds[level]
	}
	w := defaultFightWater(kind)
	var profiles map[string]json.RawMessage
	raw, _, _ := s.DB.TunableGet(ctx, "fight_water_profiles")
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &profiles); err != nil {
			s.log.Warn("水域搏鱼配置格式错误，使用默认值")
		}
	}
	for _, key := range []string{level, loc, fmt.Sprintf("%s:%d", loc, spot.Spot)} {
		if raw, ok := profiles[key]; ok {
			var tag struct {
				Kind string `json:"kind"`
			}
			if json.Unmarshal(raw, &tag) != nil {
				continue
			}
			candidate := w
			if tag.Kind != "" && tag.Kind != w.Kind {
				candidate = defaultFightWater(tag.Kind)
			}
			if json.Unmarshal(raw, &candidate) == nil && validFightWater(candidate) {
				w = candidate
			} else {
				s.log.Warn("水域搏鱼配置数值无效", "location", key)
			}
		}
	}
	// 设置的浮漂钓深不是实测水底；仅作为浮钓初始水层的估计。
	if w.FishDepthM == 0 && strings.HasPrefix(rigTypeNames[rigType], "rig_Float") {
		w.FishDepthM = math.Max(0, float64(spot.BaitDepthCM)/100)
	}
	if w.FishDepthM == 0 && (bottomRigTypes[rigType] || rigType >= 50) {
		w.FishDepthM = w.WaterDepthM
	}
	return w
}

func validFightWater(w fightWaterContext) bool {
	switch w.Kind {
	case "", "pond", "lake", "river", "rapid", "backwater", "sea":
	default:
		return false
	}
	for _, heading := range []*float64{w.CurrentBearingDeg, w.PullBearingDeg} {
		if heading != nil && (math.IsNaN(*heading) || math.IsInf(*heading, 0) || math.Abs(*heading) > 36000) {
			return false
		}
	}
	return w.CurrentMPS >= 0 && w.CurrentMPS <= 10 && w.Density >= 950 && w.Density <= 1100 &&
		w.WaterDepthM >= 0 && w.WaterDepthM <= 12000 && w.FishDepthM >= 0 && w.FishDepthM <= 12000
}

type fightWaterLoad struct {
	DragN, PressurePa, RequiredLineM float64
	AlongPullN, CrossPullN           float64
	DirectionKnown                   bool
	DepthBlocked                     bool
}

func evaluateFightWater(spec gearPhysicsSpec, species string, weightG, lengthM float64) fightWaterLoad {
	w := spec.Water
	if w.Density <= 0 {
		w.Density = 1000
	}
	depth := math.Max(0, w.FishDepthM)
	out := fightWaterLoad{RequiredLineM: depth, PressurePa: w.Density * verifiedForcePerGram * 1000 * depth}
	out.DepthBlocked = (w.WaterDepthM > 0 && depth > w.WaterDepthM) || (spec.LinePresent && depth > spec.LineLengthM)
	if w.CurrentMPS <= 0 || weightG <= 0 {
		return out
	}
	if lengthM <= 0 {
		lengthM = fishBodyLength(species, weightG, 0.0355, 1.0/3)
	}
	// 以近中性浮力椭球估算迎流面积；形状/姿态系数是玩法近似。
	volume := weightG / 1000 / 1050
	area := 1.5 * volume / math.Max(lengthM, 0.01)
	cd := 0.35
	if performanceForFish(species).pull == 1.5 {
		cd = 0.2
	} else if performanceForFish(species).stamina == 1.35 {
		cd = 0.45
	}
	lineArea := math.Max(0, spec.LineDiameterMM) / 1000 * depth
	out.DragN = 0.5 * w.Density * w.CurrentMPS * w.CurrentMPS * (cd*area + 1.2*lineArea)
	out.AlongPullN = out.DragN
	if w.CurrentBearingDeg != nil && w.PullBearingDeg != nil {
		theta := (*w.CurrentBearingDeg - *w.PullBearingDeg) * math.Pi / 180
		out.AlongPullN, out.CrossPullN = out.DragN*math.Cos(theta), out.DragN*math.Sin(theta)
		out.DirectionKnown = true
	}
	return out
}

func waterCombinedForce(forceN float64, water fightWaterLoad, allowAssist bool) float64 {
	along := water.AlongPullN
	if !allowAssist {
		// 派鱼不依赖逆流的暂时卸载，防止转向后立刻超出钓组预算。
		along = math.Max(0, along)
	}
	return math.Hypot(math.Max(0, forceN+along), water.CrossPullN)
}

// 水流占用最弱件的同一负荷预算；不把环境阻力重复写入鱼自身拉力。
func (s *Server) waterMatchedWeightCap(spec gearPhysicsSpec, species string, capG, power, peak float64) float64 {
	if spec.Water.CurrentMPS <= 0 || capG <= 0 {
		return capG
	}
	budget := gearBreakLimitKg(spec) * verifiedForcePerGram * 1000 * gearOverloadFactor
	k, p := 0.0355, 1.0/3
	if s.DB != nil {
		k, p = s.DB.SpeciesLengthKP(s.dbCtx(), species)
	}
	lo, hi := 0.0, capG
	for range 48 {
		mid := (lo + hi) / 2
		water := evaluateFightWater(spec, species, mid, fishBodyLength(species, mid, k, p))
		if water.DepthBlocked || waterCombinedForce(fishBaseForce(species, mid)*power*peak, water, false) > budget {
			hi = mid
		} else {
			lo = mid
		}
	}
	if lo < 1 {
		return -1
	}
	return lo
}
