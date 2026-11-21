package gameserver

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"
)

type FishQueryRequest struct {
	UID     int64    `json:"uid"`
	Fish    string   `json:"fish"`
	Map     string   `json:"map"`
	Bait    string   `json:"bait"`
	Gear    string   `json:"gear"`
	Grade   int64    `json:"grade"`
	RigType int64    `json:"rig_type"`
	DepthCM int64    `json:"depth_cm"`
	X       *float64 `json:"x"`
	Z       *float64 `json:"z"`
}

type FishQueryReference struct {
	Coordinate string `json:"coordinate"`
	Catches    int64  `json:"catches"`
}

type FishQueryPlan struct {
	Map                  string               `json:"map"`
	MapName              string               `json:"map_name"`
	Scene                string               `json:"scene"`
	Bait                 string               `json:"bait"`
	BaitName             string               `json:"bait_name"`
	EquivalentBaits      int                  `json:"equivalent_baits"`
	RigType              int64                `json:"rig_type"`
	RigName              string               `json:"rig_name"`
	DepthCM              int64                `json:"depth_cm"`
	LureLayer            float64              `json:"lure_layer"`
	Action               string               `json:"action"`
	OnBoat               bool                 `json:"on_boat"`
	At                   string               `json:"at"`
	GameHour             int64                `json:"game_hour"`
	GameMinute           int64                `json:"game_minute"`
	WaitSeconds          int64                `json:"wait_seconds"`
	X                    *float64             `json:"x"`
	Z                    *float64             `json:"z"`
	Zone                 int                  `json:"zone"`
	CoordinateKind       string               `json:"coordinate_kind"`
	Region               *FishQueryRegion     `json:"region,omitempty"`
	References           []FishQueryReference `json:"references"`
	Chance               float64              `json:"chance"`
	MeanDelaySeconds     float64              `json:"mean_delay_seconds"`
	TheoreticalPerMinute float64              `json:"theoretical_per_minute"`
	TheoreticalPerHour   float64              `json:"theoretical_per_hour"`
	FishShare            float64              `json:"fish_share"`
	Selection            float64              `json:"selection"`
	Acceptance           float64              `json:"acceptance"`
	GradeShare           float64              `json:"grade_share"`
	BaitTier             int64                `json:"bait_tier"`
	WeightMode           float64              `json:"weight_mode"`
	CapKg                float64              `json:"cap_kg"`
	Weather              map[string]any       `json:"weather"`
}

type FishQueryResponse struct {
	Fish             string             `json:"fish"`
	Grade            int64              `json:"grade"`
	AsOf             string             `json:"as_of"`
	Flags            map[string]float64 `json:"flags"`
	Plans            []FishQueryPlan    `json:"plans"`
	Evaluated        int                `json:"evaluated"`
	Regions          int                `json:"regions"`
	CandidateRegions int                `json:"candidate_regions"`
	WaterFiltered    bool               `json:"water_filtered"`
	BaitItems        int                `json:"bait_items"`
	BaitGroups       int                `json:"bait_groups"`
	Maps             int                `json:"maps"`
	Complete         bool               `json:"complete"`
	Elapsed          float64            `json:"elapsed_seconds"`
	Notes            []string           `json:"notes"`
}

type fishQueryMap struct {
	id, name, scene string
	points          []fishQueryPoint
	references      []FishQueryReference
	regions         *fishQueryRegions
}
type fishQueryPoint struct {
	x, z *float64
	zone int
	kind string
}
type fishQueryBait struct {
	id, name    string
	category    int64
	equivalents int
}

// QueryFish reads candidate weights without rolling fish or modifying player data.
func (s *Server) QueryFish(ctx context.Context, req FishQueryRequest) (*FishQueryResponse, error) {
	started := time.Now()
	if s.DB == nil || req.UID <= 0 || req.Fish == "" {
		return nil, fmt.Errorf("请选择玩家和目标鱼种")
	}
	if req.Map == "" {
		return nil, fmt.Errorf("请指定地图，快速查询只计算当前时段")
	}
	if req.RigType != 0 && rigTypeNames[req.RigType] == "" {
		return nil, fmt.Errorf("不支持的钓组")
	}
	if req.Grade == 0 {
		req.Grade = 1
	}
	if req.Grade < 1 || req.Grade > 4 || req.DepthCM < 0 || req.DepthCM > 30000 {
		return nil, fmt.Errorf("等级或钓深无效")
	}
	if (req.X == nil) != (req.Z == nil) || req.X != nil && (req.Map == "" || !finiteSonarNumber(*req.X) || !finiteSonarNumber(*req.Z) || math.Abs(*req.X) > 1e7 || math.Abs(*req.Z) > 1e7) {
		return nil, fmt.Errorf("手动坐标需要同时指定地图、X 和 Z")
	}
	var exists int
	if err := s.DB.SQL.QueryRowContext(ctx, `SELECT COUNT(*) FROM profile WHERE uid=?`, req.UID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists != 1 {
		return nil, fmt.Errorf("玩家不存在")
	}
	cache := newFishingRollCache()
	base := fishRollContext{cache: cache, now: started}
	response := &FishQueryResponse{Fish: req.Fish, Grade: req.Grade, AsOf: started.UTC().Format(time.RFC3339), Flags: map[string]float64{}, Plans: []FishQueryPlan{}, Complete: true}
	for key, def := range map[string]float64{"fish_bait_rules_enabled": 0, "fish_distribution_enabled": 0, "fish_weather_enabled": 0, "fish_ecology_enabled": 0, "bait_pref_scale": 1, "bait_pref_weight_shift": 1, "fish_weight_scale": 1, "fish_weight_cap": 0, "hook_entry_cap": 0, "bite_probability": 1} {
		response.Flags[key] = s.tunableF(key, def)
	}
	maps, err := s.fishQueryMaps(ctx, req, base)
	if err != nil {
		return nil, err
	}
	response.Maps = len(maps)
	if len(maps) == 0 {
		response.Elapsed = time.Since(started).Seconds()
		response.Notes = []string{"当前地图的服务端鱼种名册没有目标鱼；可指定其他地图再查。"}
		return response, nil
	}
	capKg := 0.0
	var allowedRigs []int64
	if req.Gear != "" {
		gear, err := s.DB.GetItemByGUID(ctx, req.Gear)
		if err != nil || gear == nil || gear.UID != req.UID {
			return nil, fmt.Errorf("鱼竿不属于所选玩家")
		}
		def, err := s.DB.GetItemDef(ctx, gear.NumericID)
		if err != nil || def == nil || def.Category != gearCatRod {
			return nil, fmt.Errorf("请选择有效鱼竿")
		}
		allowedRigs = rodRigAllowed[rodDefRodType(def)]
		if req.RigType != 0 && len(allowedRigs) > 0 && !allowedContains(allowedRigs, req.RigType) {
			return nil, fmt.Errorf("所选鱼竿不支持此钓组")
		}
		capKg, _, _ = s.gearWeightCapForRig(ctx, req.UID, req.Gear)
	} else if response.Flags["fish_weight_cap"] > 0 || response.Flags["hook_entry_cap"] > 0 {
		return nil, fmt.Errorf("装备限重已开启，请先选择鱼竿")
	}
	baits, count, err := s.fishQueryBaits(ctx, req, base, response.Flags)
	if err != nil {
		return nil, err
	}
	response.BaitItems, response.BaitGroups = count, len(baits)
	bonuses := map[int64]fishingSkillBonus{}
	weatherSession := &Session{Srv: s}
	for _, loc := range maps {
		weatherSession.rememberWeather(loc.id, started.Unix())
		if loc.regions != nil {
			response.Regions += loc.regions.scanned
			response.CandidateRegions += len(loc.regions.areas)
			response.WaterFiltered = response.WaterFiltered || loc.regions.waterFiltered
		}
	}
	happy, err := s.DB.GetHappyHourStatus(ctx, req.UID, started)
	if err != nil {
		return nil, err
	}
	type weatherKey struct {
		loc string
		at  int64
	}
	weatherCache := map[weatherKey]fishingWeather{}
	names, _ := s.cachedFishNames(ctx, base)
	rule, _ := lookupFishBaitRule(names[req.Fish])
	evaluate := func(loc fishQueryMap, bait fishQueryBait, point fishQueryPoint, at time.Time, rig int64, depth int64, layer float64, action string, mask uint32) (FishQueryPlan, error) {
		c := base
		c.loc, c.baits, c.now, c.rigType, c.depthCM, c.lureLayer, c.lureAction, c.lureMask = loc.scene, []string{bait.id}, at, rig, depth, layer, action, mask
		c.onBoat, c.boatKnown = sonarChartLevel(loc.id), true
		if point.x != nil {
			c.x, c.z, c.hasPos = *point.x, *point.z, true
		}
		if _, ok := bonuses[rig]; !ok {
			bonuses[rig] = s.fishingBonus(ctx, req.UID, rig, req.Gear)
		}
		bonus := bonuses[rig]
		c.skill = bonus
		if at.Equal(started) {
			c.happyBite = happy.BiteBonus
		}
		c.forRig = response.Flags["fish_bait_rules_enabled"] == 1 || response.Flags["fish_weather_enabled"] == 1 || c.happyBite > 0
		wk := weatherKey{loc.id, at.Unix()}
		w, ok := weatherCache[wk]
		if !ok {
			w = weatherSession.weatherAt(loc.id, at)
			weatherCache[wk] = w
		}
		c.weather = w
		c.query = &fishQueryCapture{fish: req.Fish, grade: req.Grade, regional: loc.regions != nil}
		s.rollFishWithRules(loc.scene, bait.id, capKg, bonus, c)
		response.Evaluated++
		var region *FishQueryRegion
		if loc.regions != nil {
			var err error
			region, err = loc.regions.best(ctx, c.query)
			if err != nil {
				return FishQueryPlan{}, err
			}
			response.Evaluated += max(0, len(loc.regions.areas)-1)
		}
		_, hour := worldDayAndHour(at.Unix())
		minute := ((at.Unix() - worldDayEpochUnix) % realSecPerGameHour) * 60 / realSecPerGameHour
		plan := FishQueryPlan{Map: loc.id, MapName: loc.name, Scene: loc.scene, Bait: bait.id, BaitName: bait.name, EquivalentBaits: bait.equivalents, RigType: rig, RigName: rigNameForType(rig), DepthCM: depth, LureLayer: layer, Action: action, OnBoat: c.onBoat, At: at.UTC().Format(time.RFC3339), GameHour: hour, GameMinute: minute, WaitSeconds: int64(at.Sub(started).Seconds()), X: point.x, Z: point.z, Zone: point.zone, CoordinateKind: point.kind, Region: region, References: loc.references, Chance: c.query.chance, FishShare: c.query.fishShare, Selection: c.query.selection, Acceptance: c.query.acceptance, GradeShare: c.query.gradeShare, BaitTier: s.cachedBaitTiers(ctx, base, bait.id)[req.Fish], WeightMode: s.baitWeightModeForRoll(req.Fish, []string{bait.id}, base), CapKg: capKg, Weather: map[string]any{"known": w.known, "kind": w.kind, "air_temp": roundTo(w.airTemp, 1), "wind": roundTo(w.wind, 2)}}
		// Candidate bait, not the selected rod's currently equipped bait.
		state := &FishState{Loc: loc.scene, Bait: bait.id, RigType: rig, DepthCM: depth, LureLayer: layer, LureAction: action, Bonus: bonus}
		// 饵效控速：候选饵的对口分决定单轮判定概率（乘进 Chance）与
		// 底层杆（非路亚族）的平均间隔；路亚窗口在 pace 模式下不动。
		pace := s.fishQueryPaceState(ctx, base, state)
		if pace.Known {
			plan.Chance = clampFloat(plan.Chance*pace.probForRig(rig), 0, 1)
		}
		delay := s.fishQueryMeanDelay(state, w, at, pace)
		if pace.Known && !rigTypeLureFamily[rig] {
			if cfg := s.effectiveBiteDelaySettings(state); cfg.fixed <= 0 {
				delay = s.meanPaceStandardDelay(cfg, s.weatherBiteDelayFactor(state, w, at),
					bonus.BiteSpeed, pace.Tier, pace.Ground, marineRigType(rig))
			}
		}
		plan.setTheoreticalRate(delay)
		return plan, nil
	}
	var candidates []FishQueryPlan
	for _, loc := range maps {
		for _, bait := range baits {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			rigs := []int64{1, 12}
			if sonarChartLevel(loc.id) {
				rigs = []int64{55}
				if b, ok := s.cachedBaitDefinition(ctx, base, bait.id); ok && b.matches("鱼肉类") {
					rigs = append(rigs, 52)
				}
			}
			if bait.category == 17 {
				rigs = []int64{5}
				if sonarChartLevel(loc.id) {
					rigs = []int64{50, 51}
				}
			}
			if req.RigType > 0 {
				rigs = []int64{req.RigType}
			}
			for _, rig := range rigs {
				if len(allowedRigs) > 0 && !allowedContains(allowedRigs, rig) {
					continue
				}
				if !fishQueryBaitFitsRig(bait.category, rig) {
					continue
				}
				depth := req.DepthCM
				if depth == 0 {
					depth = 150
				}
				layer, action, mask := fishQueryMotion(rig, rule)
				var best FishQueryPlan
				// Every shortlisted bait sees every candidate point, including
				// points where the first chart zone has zero target probability.
				for _, point := range loc.points {
					plan, err := evaluate(loc, bait, point, started, rig, depth, layer, action, mask)
					if err != nil {
						return nil, err
					}
					if fishQueryPlanBetter(plan, best) {
						best = plan
					}
				}
				if best.TheoreticalPerHour > 0 {
					candidates = append(candidates, best)
				}
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return fishQueryPlanBetter(candidates[i], candidates[j])
	})
	seen := map[string]bool{}
	for _, plan := range candidates {
		key := fmt.Sprintf("%s/%s/%d", plan.Map, plan.Bait, plan.RigType)
		if !seen[key] {
			response.Plans = append(response.Plans, plan)
			seen[key] = true
		}
		if len(response.Plans) >= 12 {
			break
		}
	}
	response.Elapsed = time.Since(started).Seconds()
	response.Notes = []string{"理论条/小时 = 3600 × 单次目标鱼概率 ÷ 平均判鱼间隔；单竿、单饵无窝料，按当前条件折算，不是官服概率或未来一小时预测。不含客户端等待、占竿、搏鱼、收鱼、重抛及落水就位保护，非实际鱼获。", "只查指定地图、当前时段，比较天然饵及拟饵各最多24组与代表钓法；不是全饵、全天候最优解。", "海图比较最多6个有依据的落点，假设已上船；内陆默认岸钓。"}
	if maps[0].regions != nil {
		response.Notes = append(response.Notes, "区域范围采用游戏显示坐标，左闭右开；代表鱼饵落入的区域，不是角色站位或指定落点。忽略局部水深、底质、水流和结构，实际落点概率会有差异。")
		if response.WaterFiltered {
			response.Notes = append(response.Notes, "已按中心与四角五点做水陆粗筛；任一点有水即保留，混合格仍可能含陆地，不保证可达。")
		} else if maps[0].regions.enabled {
			response.Notes = append(response.Notes, "缺少本图水陆数据，未排除陆地，不保证可达。")
		}
		if !maps[0].regions.enabled {
			response.Notes = append(response.Notes, "鱼群分布未开启，不推荐区域；仅比较饵料和钓法。")
		} else {
			response.Notes = append(response.Notes, fmt.Sprintf("先忽略饵料粗筛最多%d个区域，再比较候选饵料和钓法；按理论条/小时排序，不保证全图最佳组合。", fishQueryCoarseRegionLimit))
		}
	}
	return response, nil
}

func fishQueryBaitFitsRig(category, rig int64) bool {
	if rig == 52 || rig == 54 {
		return category == 27
	}
	if rigTypeLureFamily[rig] {
		return category == 17
	}
	return category == 27
}

func fishQueryMotion(rig int64, rule fishBaitRule) (float64, string, uint32) {
	if !rigTypeLureFamily[rig] {
		return 0, "", 0
	}
	if rule.needDrift {
		return .5, "drift", lureMotionDriftBit
	}
	return .5, "jig", lureMotionJigBit
}
