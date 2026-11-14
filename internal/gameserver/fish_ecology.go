package gameserver

import (
	"context"
	_ "embed"
	"encoding/json"
	"math"
	"strings"
	"sync"
	"time"
)

const fishEcologyProfileVersion = 4

//go:embed assets/fish_ecology_defs.json
var fishEcologyDefsJSON []byte

var (
	fishEcologyDefsOnce sync.Once
	fishEcologyDefs     map[string]fishEcologyProfile
)

// fishEcologyProfile 是鱼口系统使用的鱼种生态基线。水层使用 0=水面、1=水底
// 的归一化坐标，后续可叠加具体钓点水深，不把某条河的实际米数写死在鱼种上。
type fishEcologyProfile struct {
	FishKey        string  `json:"fish_key"`
	LayerMin       float64 `json:"layer_min"`
	LayerPref      float64 `json:"layer_pref"`
	LayerMax       float64 `json:"layer_max"`
	TempMinC       float64 `json:"temp_min_c"`
	TempPrefC      float64 `json:"temp_pref_c"`
	TempMaxC       float64 `json:"temp_max_c"`
	DielMask       int64   `json:"diel_mask"`
	SeasonMask     int64   `json:"season_mask"`
	DietJSON       string  `json:"diet_json"`
	WeatherJSON    string  `json:"weather_json"`
	ActionJSON     string  `json:"action_json"`
	SubstrateJSON  string  `json:"substrate_json"`
	ColorJSON      string  `json:"color_json"`
	CurrentPref    float64 `json:"current_pref"`
	Activity       float64 `json:"activity"`
	LightPref      float64 `json:"light_pref"`
	ClarityPref    float64 `json:"clarity_pref"`
	Aggression     float64 `json:"aggression"`
	RarityTier     int     `json:"rarity_tier"`
	Enabled        bool    `json:"enabled"`
	ProfileVersion int     `json:"profile_version"`
	Source         string  `json:"-"`
}

func (s *Server) ensureFishEcology(ctx context.Context) (int, error) {
	if s == nil || s.DB == nil {
		return 0, nil
	}
	rows, err := s.DB.SQL.QueryContext(ctx, `SELECT fish_key FROM species_weight`)
	if err != nil {
		return 0, err
	}
	var keys []string
	for rows.Next() {
		var key string
		if rows.Scan(&key) == nil && strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return 0, err
	}
	now := float64(time.Now().Unix())
	inserted := 0
	for _, key := range keys {
		p := defaultFishEcology(key)
		res, ierr := s.DB.SQL.ExecContext(ctx, `
			INSERT INTO fish_ecology(
				fish_key, layer_min, layer_pref, layer_max,
				temp_min_c, temp_pref_c, temp_max_c, diel_mask,
				season_mask, diet_json, weather_json, action_json, substrate_json,
				color_json, current_pref, activity, light_pref, clarity_pref,
				aggression, rarity_tier, enabled, profile_version, source, updated_ts
			) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(fish_key) DO UPDATE SET
				layer_min=excluded.layer_min, layer_pref=excluded.layer_pref,
				layer_max=excluded.layer_max, temp_min_c=excluded.temp_min_c,
				temp_pref_c=excluded.temp_pref_c, temp_max_c=excluded.temp_max_c,
				diel_mask=excluded.diel_mask, season_mask=excluded.season_mask,
				diet_json=excluded.diet_json, weather_json=excluded.weather_json,
				action_json=excluded.action_json, substrate_json=excluded.substrate_json,
				color_json=excluded.color_json, current_pref=excluded.current_pref,
				activity=excluded.activity, light_pref=excluded.light_pref,
				clarity_pref=excluded.clarity_pref, aggression=excluded.aggression,
				rarity_tier=excluded.rarity_tier,
				profile_version=excluded.profile_version, source=excluded.source,
				updated_ts=excluded.updated_ts
			WHERE fish_ecology.profile_version < excluded.profile_version`,
			p.FishKey, p.LayerMin, p.LayerPref, p.LayerMax,
			p.TempMinC, p.TempPrefC, p.TempMaxC, p.DielMask,
			p.SeasonMask, p.DietJSON, p.WeatherJSON, p.ActionJSON, p.SubstrateJSON,
			p.ColorJSON, p.CurrentPref, p.Activity, p.LightPref, p.ClarityPref,
			p.Aggression, p.RarityTier, boolInt(p.Enabled), p.ProfileVersion, p.Source, now)
		if ierr != nil {
			return inserted, ierr
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	return inserted, nil
}

func (s *Server) reloadFishEcologyCache(ctx context.Context) error {
	if s == nil || s.DB == nil {
		return nil
	}
	rows, err := s.DB.SQL.QueryContext(ctx, `
		SELECT fish_key, layer_min, layer_pref, layer_max,
		       temp_min_c, temp_pref_c, temp_max_c, diel_mask,
		       season_mask, diet_json, weather_json, action_json, substrate_json,
		       color_json, current_pref, activity, light_pref, clarity_pref,
		       aggression, rarity_tier, enabled, profile_version, source
		FROM fish_ecology`)
	if err != nil {
		return err
	}
	defer rows.Close()
	cache := make(map[string]fishEcologyProfile, 252)
	for rows.Next() {
		var p fishEcologyProfile
		var enabled int
		if err := rows.Scan(
			&p.FishKey, &p.LayerMin, &p.LayerPref, &p.LayerMax,
			&p.TempMinC, &p.TempPrefC, &p.TempMaxC, &p.DielMask,
			&p.SeasonMask, &p.DietJSON, &p.WeatherJSON, &p.ActionJSON, &p.SubstrateJSON,
			&p.ColorJSON, &p.CurrentPref, &p.Activity, &p.LightPref, &p.ClarityPref,
			&p.Aggression, &p.RarityTier, &enabled, &p.ProfileVersion, &p.Source,
		); err != nil {
			return err
		}
		p.Enabled = enabled != 0
		cache[p.FishKey] = p
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.fishEcologyMu.Lock()
	s.fishEcologyCache = cache
	s.fishEcologyCacheLoaded = true
	s.fishEcologyMu.Unlock()
	return nil
}

func (s *Server) invalidateFishEcologyCache() {
	if s == nil {
		return
	}
	s.fishEcologyMu.Lock()
	s.fishEcologyCache = nil
	s.fishEcologyCacheLoaded = false
	s.fishEcologyMu.Unlock()
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func defaultFishEcology(key string) fishEcologyProfile {
	fishEcologyDefsOnce.Do(func() {
		fishEcologyDefs = map[string]fishEcologyProfile{}
		var profiles []fishEcologyProfile
		if json.Unmarshal(fishEcologyDefsJSON, &profiles) != nil {
			return
		}
		for _, profile := range profiles {
			fishEcologyDefs[profile.FishKey] = profile
		}
	})
	if profile, ok := fishEcologyDefs[key]; ok {
		if profile.Source == "" {
			profile.Source = "local-sim:deterministic-v3"
		}
		return profile
	}
	return fishEcologyProfile{
		FishKey: key, LayerPref: .55, LayerMax: 1,
		TempMinC: 5, TempPrefC: 17, TempMaxC: 27, DielMask: 0b1111,
		SeasonMask: 0b1111, DietJSON: `{"worm":1,"plant":1,"fish":1}`,
		WeatherJSON: `{"clear":1,"cloudy":1,"rain":1}`,
		ActionJSON:  `{"steady":1,"sinking":1}`, SubstrateJSON: `{"sand":1,"mud":1}`,
		ColorJSON: `{"silver":1,"natural":1}`, CurrentPref: .5, Activity: 1,
		LightPref: .5, ClarityPref: .5, Aggression: 1, RarityTier: 1, Enabled: true,
		ProfileVersion: fishEcologyProfileVersion, Source: "local-sim:fallback",
	}
}

func (s *Server) fishEcology(ctx context.Context, key string) fishEcologyProfile {
	if s == nil || s.DB == nil {
		return defaultFishEcology(key)
	}
	s.fishEcologyMu.RLock()
	if s.fishEcologyCacheLoaded {
		profile, ok := s.fishEcologyCache[key]
		s.fishEcologyMu.RUnlock()
		if !ok {
			return defaultFishEcology(key)
		}
		return profile
	}
	s.fishEcologyMu.RUnlock()
	var p fishEcologyProfile
	var enabled, profileVersion int
	err := s.DB.SQL.QueryRowContext(ctx, `
		SELECT fish_key, layer_min, layer_pref, layer_max,
		       temp_min_c, temp_pref_c, temp_max_c, diel_mask,
		       season_mask, diet_json, weather_json, action_json, substrate_json,
		       color_json, current_pref, activity, light_pref, clarity_pref,
		       aggression, rarity_tier, enabled, profile_version, source
		FROM fish_ecology WHERE fish_key=?`, key).Scan(
		&p.FishKey, &p.LayerMin, &p.LayerPref, &p.LayerMax,
		&p.TempMinC, &p.TempPrefC, &p.TempMaxC, &p.DielMask,
		&p.SeasonMask, &p.DietJSON, &p.WeatherJSON, &p.ActionJSON, &p.SubstrateJSON,
		&p.ColorJSON, &p.CurrentPref, &p.Activity, &p.LightPref, &p.ClarityPref,
		&p.Aggression, &p.RarityTier, &enabled, &profileVersion, &p.Source)
	if err != nil {
		return defaultFishEcology(key)
	}
	p.Enabled = enabled != 0
	p.ProfileVersion = profileVersion
	return p
}

func normalizeWaterLayer(v float64) float64 {
	return math.Max(0, math.Min(1, v))
}

func (s *Server) rigWaterLayer(c fishRollContext) float64 {
	if bottomRigTypes[c.rigType] {
		return .93
	}
	if rigTypeLureFamily[c.rigType] {
		if c.lureLayer > 0 {
			return normalizeWaterLayer(c.lureLayer)
		}
		// 无运动证据时按饵的名义泳层兜底，而不是一律中层 .5——水面系饵
		// 刚入水还没上报动作时按 .5 判鱼会错过表层掠食鱼。
		if c.runtime != nil && c.runtime.lureKnown && c.runtime.lure.DiveDepth > 0 {
			return normalizeWaterLayer(c.runtime.lure.DiveDepth)
		}
		return .5
	}
	if c.depthCM > 0 {
		// 浮钓水层按落点实际水深归一；没有落点水深时维持旧的 5m 满刻度
		// （旧口径在 >5m 水域会把中层调漂读成底层）。
		span := 500.0
		if c.spotEnv.Known && c.spotEnv.DepthM > 0 {
			span = c.spotEnv.DepthM * 100
		}
		return normalizeWaterLayer(float64(c.depthCM) / span)
	}
	return .45
}

// fishLayerWeight 按当前钓法水层与鱼种范围计算软匹配权重。
// 底层杆对明显不属于底层水层的鱼保留极小杂口，而不是绝对删除。
func (s *Server) fishLayerWeight(ctx context.Context, key string, c fishRollContext) float64 {
	p := s.cachedFishEcology(ctx, c, key)
	if !p.Enabled {
		return 0
	}
	return s.fishLayerWeightProfile(p, c)
}

// fishLayerWeightProfile 按当前钓法水层与鱼种区间计算软匹配权重：
// 区间（±0.08 容差）内保留 pref 高斯峰（精确命中偏好层 ≈1.0）与 0.75 地板，
// 区间外对称指数衰减（k=2），下限 0.02 保留杂口。旧版只罚"底钓打表层鱼"
// 与"路亚比鱼深"两个方向，水面饵打底层鱼只走高斯尾巴（v4 区间收窄后
// 仍有 ~0.27），是水面系上底鱼的根因之一；对称化后衰减与方向无关。
func (s *Server) fishLayerWeightProfile(p fishEcologyProfile, c fishRollContext) float64 {
	target := s.rigWaterLayer(c)
	lo, hi := p.LayerMin, p.LayerMax
	if target >= lo-.08 && target <= hi+.08 {
		half := math.Max(.08, (hi-lo)/2)
		z := (target - p.LayerPref) / half
		return math.Max(.75, .12+.88*math.Exp(-.5*z*z))
	}
	half := math.Max(.08, (hi-lo)/2)
	out := math.Max(lo-.08-target, target-hi-.08) / half
	return math.Max(.02, .75*math.Exp(-2*out))
}

func (s *Server) fishBiteEnvironmentWeight(ctx context.Context, key string, c fishRollContext) float64 {
	if c.rigType == 0 && c.depthCM == 0 && c.lureLayer == 0 {
		return 1
	}
	if s.tunableF("fish_ecology_enabled", 0) != 1 {
		return 1
	}
	p := s.cachedFishEcology(ctx, c, key)
	if !p.Enabled {
		return 0
	}
	type weightedFactor struct {
		value     float64
		tolerance float64
	}
	factors := []weightedFactor{{
		value: s.fishLayerWeightProfile(p, c), tolerance: c.skill.LayerAccuracy,
	}}
	_, hour := worldDayAndHour(c.now.Unix())
	factors = append(factors, weightedFactor{fishDielWeight(p, hour), c.skill.EcologyInterest})
	water := fishWaterConditionsFor(p, c)
	factors = append(factors,
		weightedFactor{fishSeasonWeight(p, water.Season), c.skill.WeatherAdaptation},
		weightedFactor{fishWeatherKindWeight(p, c.weather.kind), c.skill.WeatherAdaptation},
		weightedFactor{fishWeatherExtraWeight(p, c.weather), c.skill.WeatherAdaptation},
	)
	tolerance := math.Max(2.2, (p.TempMaxC-p.TempMinC)/2)
	z := (water.WaterTempC - p.TempPrefC) / tolerance
	factors = append(factors,
		weightedFactor{.35 + .65*math.Exp(-.5*z*z), c.skill.WeatherAdaptation},
		weightedFactor{1 - math.Abs(p.ClarityPref-water.Clarity)*.28, c.skill.EcologyInterest},
		weightedFactor{1 - math.Abs(p.LightPref-water.Light)*.18, c.skill.EcologyInterest},
		weightedFactor{fishSpotBiteWeight(p, c.spotEnv), c.skill.EcologyInterest},
	)
	if c.lureAction != "" {
		if weights := fishEcologyJSONMap(p.ActionJSON); len(weights) > 0 {
			if weight, ok := fishEcologyActionWeight(weights, c.lureAction); ok {
				factors = append(factors, weightedFactor{weight, c.skill.LureTolerance})
			}
		}
	}
	factors = append(factors,
		weightedFactor{fishMotionConditionWeight(p, c, water), c.skill.LureTolerance},
	)
	factors = append(factors,
		weightedFactor{1 + c.skill.RareFish*float64(p.RarityTier)*.12, 0},
		weightedFactor{s.fishLureWeight(p, c, water), c.skill.LureTolerance},
		weightedFactor{fishSkillEnvironmentWeight(key, c), 0},
	)
	factor := 1.0
	for i, raw := range factors {
		if raw.value <= 0 || math.IsNaN(raw.value) || math.IsInf(raw.value, 0) {
			continue
		}
		value := raw.value
		if value < 1 && raw.tolerance > 0 {
			value += (1 - value) * clampFloat(raw.tolerance, 0, .45)
		}
		floor, exponent := .25, .35
		if i == 0 {
			floor, exponent = .05, .60
		}
		factor *= math.Pow(clampFloat(value, floor, 2.0), exponent)
	}
	return clampFloat(factor, .08, 2.5)
}

// activityForDiel 将游戏小时映射到夜、晨、日、昏四段。
func fishActivityForHour(hour int64) float64 {
	hour = (hour%24 + 24) % 24
	switch {
	case hour >= 5 && hour < 9:
		return 1
	case hour >= 9 && hour < 16:
		return 2
	case hour >= 16 && hour < 20:
		return 3
	default:
		return 0
	}
}

func fishDielWeight(profile fishEcologyProfile, hour int64) float64 {
	bit := uint(fishActivityForHour(hour))
	if profile.DielMask&(1<<bit) != 0 {
		return 1
	}
	return .35
}

func fishEcologyJSONMap(raw string) map[string]float64 {
	var m map[string]float64
	_ = json.Unmarshal([]byte(raw), &m)
	return m
}

// lureMotionStatusMask 覆盖客户端 Gmkegncajem 已知的 0..22 位动作状态。
// 0xD0000000/0xD1000000 的高位是钓组阶段，不属于拟饵动作。
const lureMotionStatusMask uint32 = 0x007FFFFF

// 客户端 4.0.25034 把钓组动作定义成 23 个状态：托管元数据里的动作枚举
// （fhiclnhgoge）取值 0x1..0x800000 共 23 个单位。resources.assets 偏移 0x12722da1
// 起内嵌 kRigAnimationType_* 本地化表，每条 16 个语言列、第 15 列是简体，23 个显示名
// 已全部提出：Hooking 鱼咬钩时抖竿、UniformMotion 收线、StopAndGo 搅动及静止、
// SpeedUp 加收线速度、Hanging 大动作扯动、BottomTouch 接触底部、Twitch 搐动、Splash
// 激溅、StreamMoving 顺水漂流、JigStep Jig梯阶、WalkDog 犬步、LittleJerk 轻微的抽停、
// MediumJerk 强烈的抽动、FreshWaterLittleJerk 轻度抽停、FreshWaterMediumJerk 中度抽停、
// MotionlessInWaterColumn 悬挂在水底深处、MotionlessOnBottom 在水底无移动、
// MotionlessOnWaterSurface 水面上无移动、MotionOnBottom 在水底动、VerticalDive 下沉、
// VerticalAscend 垂直上升、Trolling 拖钓、MotionInBottomLayer 底层的运动。
// 本地化表按名称排列不带位序，键名仍需实机 14/7 上报对位。
//
// 已对位的位（实机 14/7 与客户端弹字互证，客户端 4.0.25034）：
// 0x8=BottomTouch、0x10=Twitch、0x20=Splash、0x40=StreamMoving、0x80=JigStep、
// 0x100=WalkDog、0x200=MotionlessInWaterColumn、0x400=UniformMotion、0x4000=Trolling、
// 0x80000=VerticalDive；0x20000=MediumJerk、0x40000=LittleJerk，两者只在海图出现
// （loc12_27 铁板钓组 rig=50 分别弹“强烈的抽动”“轻微抽停了”，铁板垂直抖法），
// 官方英文原文是 Strong pirking / Pirking，中文名里的"抽停"与淡水抽停饵无关；
// 0x4=Hanging，旧奥挂底时独亮且不伴 0x400，官方简体名“大动作扯动”、英俄德等语言
// 均为“带停顿的收线”，按“收线中带停顿”理解，仍属于饵在水中运动。
//
// 未对位的位：0x1（权重表占位名 stir）、0x8000、0x100000（占位名 motion_15/motion_20）。
// 2026-11-06 用抽停竿（竿型 12）加抽停饵（拟饵类型 7 jerkbait）在旧奥实测得到 rig=22，
// 客户端只上报 0x400 收线、0x20 激溅和 0x100 犬步，淡水未触发 FreshWaterLittleJerk/
// FreshWaterMediumJerk；两次刺鱼也只有 14/9 抬杆，未见 14/7 新位。这三位保持中性，
// 不进攻略前置条件。
//
// 攻略前置条件要用的三个客户端动作位（kRigAnimationType_*）：
// 顺流移动、jig 阶梯，以及“饵在水中运动”对应的动到位集合。
const (
	lureMotionDriftBit uint32 = 0x40
	lureMotionJigBit   uint32 = 0x80
)

// lureMotionInWaterMask 排除浮漂/水层/底部静置和触底姿态，剩下的都是饵在
// 水里移动的状态位。0x4 Hanging 是“带停顿的收线”、0x1000 水面静置不在其中，
// 0x1/0x8000/0x100000 三个未对位位仍留在集合里，宁可多判在动也不误判静止。
const lureMotionInWaterMask uint32 = lureMotionStatusMask &^ (0x1000 | 0x200 | 0x800 | 0x8)

// lureMotionEvidence 判断本次判鱼是否真的拿到动作上报。没有上报时掩码为 0，
// 不能当成“玩家没做这个动作”。
func lureMotionEvidence(mask uint32) bool {
	return mask&lureMotionStatusMask != 0
}

// classifyLureMotion 映射客户端 kRigAnimationType_* 状态位。优先级按
// “瞬时主动作 > 水层姿态 > 收线兜底”排列，避免 0x400 收线盖住抽、跳等动作。
func classifyLureMotion(mask uint32) string {
	mask &= lureMotionStatusMask
	switch {
	case mask&0x40000 != 0:
		return "little_jerk"
	case mask&0x20000 != 0:
		return "medium_jerk"
	case mask&0x10000 != 0:
		return "stop_go"
	case mask&0x10 != 0:
		return "twitch"
	case mask&0x80 != 0:
		return "jig"
	case mask&0x100 != 0:
		return "walk_dog"
	case mask&0x400000 != 0:
		return "bottom_layer_move"
	case mask&0x4000 != 0:
		return "trolling"
	case mask&0x2000 != 0:
		return "bottom_move"
	case mask&0x80000 != 0:
		return "vertical_dive"
	case mask&0x20 != 0:
		return "splash"
	case mask&0x800 != 0:
		return "bottom_motionless"
	case mask&0x200 != 0:
		return "water_column_motionless"
	case mask&0x1000 != 0:
		return "surface_motionless"
	case mask&0x40 != 0:
		return "drift"
	case mask&0x8 != 0:
		return "bottom_contact"
	case mask&0x4 != 0:
		return "retrieval_pause"
	case mask&0x2 != 0:
		return "speed_up"
	case mask&0x200000 != 0:
		return "vertical_ascend"
	case mask&0x400 != 0:
		return "steady"
	case mask&0x1 != 0:
		return "stir"
	default:
		return ""
	}
}

// fishEcologyActionWeight 兼容旧 fish_ecology.action_json 键。已有数据库行
// 不会被 INSERT OR IGNORE 更新，因此新细分动作必须能回退到旧粗分类。
func fishEcologyActionWeight(weights map[string]float64, action string) (float64, bool) {
	if weight, ok := weights[action]; ok {
		return weight, true
	}
	aliases := map[string][]string{
		"bottom_motionless":       {"bottom", "bottom_hang"},
		"little_jerk":             {"jig", "hard_twitch", "motion_18"},
		"medium_jerk":             {"hard_twitch", "jig", "motion_18"},
		"retrieval_pause":         {"stop_go", "moving"},
		"surface_motionless":      {"surface"},
		"vertical_dive":           {"sinking", "vertical_rise"},
		"water_column_motionless": {"sinking", "bottom_hang"},
		"marine_jig":              {"jig"},
	}
	for _, candidate := range aliases[action] {
		if weight, ok := weights[candidate]; ok {
			return weight, true
		}
	}
	var candidates []string
	switch action {
	case "steady", "speed_up", "trolling", "drift", "twitch", "jerk", "hard_twitch", "stop_go",
		"little_jerk", "medium_jerk", "retrieval_pause":
		candidates = []string{"moving"}
	case "bottom_motionless", "bottom_contact", "water_column_motionless":
		candidates = []string{"bottom_motionless"}
	case "bottom_move", "bottom_layer_move":
		candidates = []string{"bottom_motionless", "moving"}
	case "jig", "walk_dog":
		candidates = []string{"moving", "bottom_motionless"}
	case "surface", "surface_motionless", "splash":
		candidates = []string{"surface_motionless", "moving"}
	case "sinking", "vertical_rise", "vertical_dive", "vertical_ascend":
		candidates = []string{"water_column_motionless", "moving"}
	case "stir":
		candidates = []string{"moving", "water_column_motionless"}
	}
	for _, candidate := range candidates {
		if weight, ok := weights[candidate]; ok {
			return weight, true
		}
	}
	return 0, false
}

// lureLayerFromMotion 由 14/7 运动位推导实时水层。dive 是当前拟饵的名义泳层
// （loadLureAttributes 的 DiveDepth），>0 时把结果钳进 dive+0.15 以内：运动位
// 是玩家操作意图，但饵的物理泳层有上限——水面系饵（dive≈0.10）的匀速收线
// 旧版会按 +0.03 无界漂到中层甚至底层，深潜铁板的跳底位也会越过物理深度。
// 水面动作位（0x1000/0x100/0x20）不受钳制：把深潜饵快收到水面是真实操作。
func lureLayerFromMotion(mask uint32, previous, dive float64) float64 {
	if previous <= 0 {
		previous = .5
	}
	mask &= lureMotionStatusMask
	layer := previous
	switch {
	case mask&0x1000 != 0:
		layer = .08
	// 犬步（WalkDog）是水面系的收线动作：没有这个分支时会落到 default，
	// 水面拟饵被当成中层（previous≈.5）判鱼，只能抽到中层鱼，客户端
	// 表现为"鱼来了但不咬"。激溅（0x20）同理，只是可能同时带收线位。
	case mask&0x100 != 0:
		layer = .06
	case mask&0x80000 != 0:
		layer = normalizeWaterLayer(previous + .08)
	case mask&0x20 != 0:
		// 激溅是主动作，可能同时带收线位 0x400 或其它水层位；必须优先于
		// 收线/静止分支，保持与 classifyLureMotion 一致。
		layer = .08
	case mask&0x200000 != 0:
		layer = normalizeWaterLayer(previous - .08)
	case mask&0x2000 != 0, mask&0x400000 != 0:
		layer = .94
	case mask&0x800 != 0:
		layer = .92
	case mask&0x200 != 0:
		layer = .55
	case mask&0x400 != 0:
		layer = normalizeWaterLayer(previous + .03)
	}
	if dive > 0 {
		layer = math.Min(layer, dive+.15)
	}
	return normalizeWaterLayer(layer)
}
