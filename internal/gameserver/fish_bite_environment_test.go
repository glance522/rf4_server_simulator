package gameserver

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFishEcologyAssetCoversAllSpeciesAndActions(t *testing.T) {
	var profiles []fishEcologyProfile
	if err := json.Unmarshal(fishEcologyDefsJSON, &profiles); err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 252 {
		t.Fatalf("生态资产=%d，期望 252", len(profiles))
	}
	seen := map[string]bool{}
	for _, profile := range profiles {
		if profile.FishKey == "" || seen[profile.FishKey] {
			t.Fatalf("鱼种 key 为空或重复: %q", profile.FishKey)
		}
		seen[profile.FishKey] = true
		if profile.ProfileVersion != fishEcologyProfileVersion {
			t.Errorf("%s profile_version=%d", profile.FishKey, profile.ProfileVersion)
		}
		if profile.RarityTier < 0 || profile.RarityTier > 3 {
			t.Errorf("%s rarity_tier=%d", profile.FishKey, profile.RarityTier)
		}
		if !(0 <= profile.LayerMin && profile.LayerMin < profile.LayerPref &&
			profile.LayerPref < profile.LayerMax && profile.LayerMax <= 1) {
			t.Errorf("%s 水层范围非法: %+v", profile.FishKey, profile)
		}
		if !(profile.TempMinC < profile.TempPrefC && profile.TempPrefC < profile.TempMaxC) {
			t.Errorf("%s 温度范围非法: %+v", profile.FishKey, profile)
		}
		for name, raw := range map[string]string{
			"diet": profile.DietJSON, "weather": profile.WeatherJSON,
			"action": profile.ActionJSON, "substrate": profile.SubstrateJSON,
			"color": profile.ColorJSON,
		} {
			if len(fishEcologyJSONMap(raw)) == 0 {
				t.Errorf("%s %s 矩阵为空", profile.FishKey, name)
			}
		}
		actions := fishEcologyJSONMap(profile.ActionJSON)
		if len(actions) != len(actionNamesForTest()) {
			t.Errorf("%s 动作数=%d，期望 %d", profile.FishKey, len(actions), len(actionNamesForTest()))
		}
	}
}

func TestEveryLureMotionBitHasDistinctAction(t *testing.T) {
	seen := map[string]uint32{}
	// 0x8000 与 0x100000 是淡水抽停候选位，实机未触发，保持不映射；
	// 0x40000 已在海图铁板钓组上对位为 little_jerk。
	unknown := map[uint]bool{15: true, 20: true}
	for bit := uint(0); bit <= 22; bit++ {
		mask := uint32(1) << bit
		action := classifyLureMotion(mask)
		if unknown[bit] {
			if action != "" {
				t.Errorf("未证实动作位 0x%X 不应映射为 %q", mask, action)
			}
			continue
		}
		if action == "" {
			t.Errorf("动作位 0x%X 未识别", mask)
			continue
		}
		if previous, ok := seen[action]; ok {
			t.Errorf("动作 %q 同时对应 0x%X 和 0x%X", action, previous, mask)
		}
		seen[action] = mask
	}
	if want := 23 - len(unknown); len(seen) != want {
		t.Fatalf("已识别动作=%d，期望 %d", len(seen), want)
	}
}

func TestWaterTemperatureUsesSeasonDepthAndLag(t *testing.T) {
	profile := defaultFishEcology("carp")
	summer := time.Unix(worldDayEpochUnix+120*24*realSecPerGameHour+12*realSecPerGameHour, 0)
	winter := time.Unix(worldDayEpochUnix+300*24*realSecPerGameHour+12*realSecPerGameHour, 0)
	weather := fishingWeather{known: true, airTemp: 18, previousAirTemp: 18, kind: fishingWeatherCloudy, wind: 3}
	shallow := fishRollContext{rigType: 1, depthCM: 50, now: summer, weather: weather}
	deep := shallow
	deep.now = winter
	deep.depthCM = 500
	summerShallow := fishWaterConditionsFor(profile, shallow)
	winterDeep := fishWaterConditionsFor(profile, deep)
	if summerShallow.WaterTempC <= winterDeep.WaterTempC {
		t.Fatalf("夏季浅水 %.2f 应高于冬季深水 %.2f", summerShallow.WaterTempC, winterDeep.WaterTempC)
	}
	if summerShallow.Clarity <= 0 || summerShallow.Light <= 0 || summerShallow.Turbulence < 0 {
		t.Fatalf("水环境字段非法: %+v", summerShallow)
	}
}

func TestLureLightColorAndSkillAffectBiteWeight(t *testing.T) {
	profile := fishEcologyProfile{
		ColorJSON:  `{"silver":1.45,"dark":0.55}`,
		Aggression: 1.2,
	}
	c := fishRollContext{
		rigType: 5, lureLayer: .45, lureAction: "twitch",
		runtime: &fishRollRuntime{
			lureKnown: true,
			lure: lureAttributes{
				Type: "jerkbait", Color: "silver", ColorKnown: true, Reflectivity: 1,
				Vibration: .8, WeightG: 18, DiveDepth: .45,
			},
		},
	}
	brightWater := fishWaterConditions{Light: .85, Clarity: .85, Turbulence: .35}
	darkWater := fishWaterConditions{Light: .12, Clarity: .35, Turbulence: .35}
	bright := (&Server{}).fishLureWeight(profile, c, brightWater)
	c.runtime.lure.Color = "dark"
	c.runtime.lure.Reflectivity = .25
	dark := (&Server{}).fishLureWeight(profile, c, darkWater)
	if math.Abs(bright-dark) < .05 {
		t.Fatalf("银亮清水与暗色浑水权重没有差异: bright=%f dark=%f", bright, dark)
	}

	neutral := fishSkillEnvironmentWeight("n.pike", fishRollContext{rigType: 5})
	skilled := fishSkillEnvironmentWeight("n.pike", fishRollContext{
		rigType: 5,
		skill: fishingSkillBonus{
			BiteSpeed: .7, EcologyInterest: .12, LureTolerance: .14,
			WeatherAdaptation: .1, LayerAccuracy: .1, PredatorBoost: .16,
		},
	})
	if skilled <= neutral {
		t.Fatalf("专项技能未提高鱼口: neutral=%f skilled=%f", neutral, skilled)
	}
}

func TestSpotEnvironmentUsesStableSubstrateCurrentAndDepth(t *testing.T) {
	first := fishRollContext{loc: "level_012_north_sea", spot: 80, rigType: 50}
	firstEnv := fishSpotEnvironmentFor(&first)
	secondEnv := fishSpotEnvironmentFor(&first)
	if firstEnv != secondEnv {
		t.Fatalf("同一钓点环境不稳定: %+v != %+v", firstEnv, secondEnv)
	}
	if !firstEnv.Known || firstEnv.DepthM <= 0 || firstEnv.Current <= 0 ||
		firstEnv.Structure <= 0 || firstEnv.Substrate == "" {
		t.Fatalf("钓点环境不完整: %+v", firstEnv)
	}
	profile := fishEcologyProfile{
		SubstrateJSON: `{"mud":.5,"sand":1,"gravel":1,"rock":1,"weed":1}`,
		CurrentPref:   .5, Aggression: 1.2, LayerPref: .8,
	}
	if got := fishSpotBiteWeight(profile, firstEnv); got < .3 || got > 1.6 {
		t.Fatalf("钓点鱼口倍率越界: %f", got)
	}
}

func TestApplyFishSpotBiasUsesObservedSpotWithoutHardBlacklist(t *testing.T) {
	keys := []string{"perch", "c.roach", "n.pike"}
	weights := []float64{1, 1, 1}
	pref := map[string]float64{"perch": 12, "c.roach": 2}
	applyFishSpotBias(keys, weights, pref, true)
	if weights[0] <= weights[2] || weights[1] <= weights[2] {
		t.Fatalf("钓点观测鱼种应高于非观测鱼种: %+v", weights)
	}
	if weights[2] <= 0 || weights[2] >= .4 {
		t.Fatalf("非观测鱼种应保留低杂口而非封死: %f", weights[2])
	}
	neutral := []float64{1, 1, 1}
	applyFishSpotBias(keys, neutral, pref, false)
	if neutral[0] != 1 || neutral[1] != 1 || neutral[2] != 1 {
		t.Fatal("无钓点数据时不应改变权重")
	}
}

func TestFishMotionConditionWeightRecognizesJigDriftAndBoat(t *testing.T) {
	profile := fishEcologyProfile{CurrentPref: .8, Activity: 1.25, ActionJSON: `{"jig":1.2,"drift":1.1,"trolling":1.2}`}
	water := fishWaterConditions{Turbulence: .7}
	jig := fishMotionConditionWeight(profile, fishRollContext{lureAction: "jig"}, water)
	marineJig := fishMotionConditionWeight(profile, fishRollContext{lureAction: "marine_jig", rigType: 52}, water)
	drift := fishMotionConditionWeight(profile, fishRollContext{lureAction: "drift"}, water)
	boat := fishMotionConditionWeight(profile, fishRollContext{lureAction: "trolling", lureSpeed: 12}, water)
	if jig <= 1 || marineJig <= jig || drift <= 1 || boat <= 1 {
		t.Fatalf("jig/顺流/船上动作没有产生正权重: jig=%f marine=%f drift=%f boat=%f",
			jig, marineJig, drift, boat)
	}
	slowCurrent := fishWaterConditions{Turbulence: .1}
	if got := fishMotionConditionWeight(profile, fishRollContext{lureAction: "drift"}, slowCurrent); got >= 1 {
		t.Fatalf("高偏好鱼种在低流速顺流应略降: %f", got)
	}
}

func TestLureAttributesUseConfirmedTypeAndDerivedFields(t *testing.T) {
	srv, db := newRigTestServer(t)
	staticPath := filepath.Join("..", "..", "server_data", "static.sqlite3")
	if _, err := db.SQL.Exec(`ATTACH DATABASE ? AS src`, staticPath); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT OR IGNORE INTO item_def SELECT * FROM src.item_def WHERE item_id='crank_2603'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT OR IGNORE INTO item_catalog SELECT * FROM src.item_catalog WHERE item_id='crank_2603'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`DETACH DATABASE src`); err != nil {
		t.Fatal(err)
	}
	lure, ok := srv.loadLureAttributes(t.Context(), "crank_2603")
	if !ok {
		t.Fatal("crank_2603 未加载")
	}
	if lure.Type != "crank" || lure.SizeCM <= 0 || lure.WeightG <= 0 ||
		lure.DiveDepth <= 0 || lure.Reflectivity <= 0 || lure.SinkRate <= 0 {
		t.Fatalf("拟饵属性不完整: %+v", lure)
	}
	if got := lureActionTypeWeight(lure.Type, "trolling"); got <= 1 {
		t.Fatalf("crank 拖钓动作未加分: %f", got)
	}
}

func TestSkillFishEffectsAreTableDrivenAndSharedGroupsCountOnce(t *testing.T) {
	srv, db := newRigTestServer(t)
	staticPath := filepath.Join("..", "..", "server_data", "static.sqlite3")
	if _, err := db.SQL.Exec(`ATTACH DATABASE ? AS src`, staticPath); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT OR IGNORE INTO skill_def SELECT * FROM src.skill_def`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`DETACH DATABASE src`); err != nil {
		t.Fatal(err)
	}
	inserted, err := srv.ensureFishSkillEffects(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if inserted < 70 {
		t.Fatalf("鱼口技能效果只生成 %d 条", inserted)
	}

	// 生产顺序：鱼口 ensure 之后跑抛投 ensure（attachDB 同处调用），
	// 已确认纯投距的节点（docs/cast-skill-official-effects.md）其自造鱼侧行
	// 被删除。鱼口 ensure 会把行回插，所以这里完整重放一遍生产顺序：
	// 回插后再跑抛投 ensure，必须再次删干净（幂等）。
	if _, err := srv.ensureCastSkillEffects(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ensureFishSkillEffects(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ensureCastSkillEffects(t.Context()); err != nil {
		t.Fatal(err)
	}
	var castSeeds int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM skill_cast_effect_def`).Scan(&castSeeds); err != nil {
		t.Fatal(err)
	}
	if castSeeds < 25 {
		t.Fatalf("抛投技能效果只种下 %d 条", castSeeds)
	}

	// 纯投距共享组：鱼侧行不得存在。
	for _, nodes := range [][]int64{
		{514, 701, 313, 1002},
		{718, 1008, 721, 1020},
	} {
		var rows int
		query := `SELECT COUNT(*) FROM skill_fish_effect_def WHERE node_id IN (` +
			strings.TrimSuffix(strings.Repeat("?,", len(nodes)), ",") + `)`
		args := make([]any, len(nodes))
		for i, node := range nodes {
			args[i] = node
		}
		if err := db.SQL.QueryRow(query, args...).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 0 {
			t.Fatalf("纯投距技能 %v 仍残留鱼侧效果行: %d", nodes, rows)
		}
	}

	// 仍是鱼侧语义的共享组：效果签名保持一致。
	for _, nodes := range [][]int64{
		{515, 516},
	} {
		var signatures int
		query := `SELECT COUNT(DISTINCT effect_key||'/'||per_level||'/'||cap)
			FROM skill_fish_effect_def WHERE node_id IN (` +
			strings.TrimSuffix(strings.Repeat("?,", len(nodes)), ",") + `)`
		args := make([]any, len(nodes))
		for i, node := range nodes {
			args[i] = node
		}
		if err := db.SQL.QueryRow(query, args...).Scan(&signatures); err != nil {
			t.Fatal(err)
		}
		if signatures != 1 {
			t.Fatalf("共享技能 %v 效果签名不一致: %d", nodes, signatures)
		}
	}
	defs := []skillNodeDef{
		{NodeID: 515, CatID: 1, MaxLevel: 3, Enabled: true},
		{NodeID: 516, CatID: 1, MaxLevel: 3, Enabled: true},
	}
	effects := effectiveFishingEffects{
		Learned:   map[int64]int64{515: 2, 516: 2},
		GearNodes: map[int64]int64{516: 1},
	}
	bonus := neutralFishingSkillBonus()
	srv.applyFishingNodeBonuses(t.Context(), 1, &bonus, effects, defs)
	total := bonus.EcologyInterest + bonus.RareFish + bonus.LureTolerance +
		bonus.WeatherAdaptation + bonus.LayerAccuracy + bonus.WormBoost +
		bonus.PredatorBoost + bonus.SalmonidBoost + bonus.BottomBoost + bonus.FloatBoost
	var perLevel, cap float64
	if err := db.SQL.QueryRow(`SELECT per_level,cap FROM skill_fish_effect_def
		WHERE cat_id=1 AND node_id=515`).Scan(&perLevel, &cap); err != nil {
		t.Fatal(err)
	}
	want := math.Min(cap, 3*perLevel) // 学习 2 级 + 鱼竿赠 1 级
	if math.Abs(total-want) > 1e-9 {
		t.Fatalf("共享技能赠级结算错误 total=%f want=%f bonus=%+v", total, want, bonus)
	}
}

func actionNamesForTest() []string {
	return []string{
		"stir", "speed_up", "jerk", "bottom_contact", "twitch", "splash",
		"drift", "jig", "walk_dog", "water_column_motionless", "steady",
		"bottom_motionless", "surface_motionless", "bottom_move", "trolling",
		"motion_15", "stop_go", "hard_twitch", "motion_18", "vertical_dive",
		"motion_20", "vertical_ascend", "bottom_layer_move",
	}
}

func TestLoadFishSpotPreferenceMatchesChartZone(t *testing.T) {
	srv, db := newRigTestServer(t)
	seed := "INSERT INTO spot_fish(loc,spot,fish_key,catches) VALUES('chart-zone-test','120','atl_cod',9),('chart-zone-test','120:3','atl_haddock',7),('chart-zone-test','75','atl_mackerel',5)"
	if _, err := db.SQL.Exec(seed); err != nil {
		t.Fatal(err)
	}
	c := &fishRollContext{runtime: &fishRollRuntime{zoneKnown: true, zone: 120}}
	pref, hasData := srv.loadFishSpotPreference(context.Background(), "chart-zone-test", c)
	if !hasData || pref["atl_cod"] <= 0 {
		t.Fatalf("当前海图区号的观测没有生效: %v has=%v", pref, hasData)
	}
	if _, ok := pref["atl_haddock"]; ok {
		t.Fatal("攻略网格文本不该被当成海图区号观测")
	}
	if _, ok := pref["atl_mackerel"]; ok {
		t.Fatal("其他区号的观测不能带进当前区号")
	}
	if _, has := srv.loadFishSpotPreference(context.Background(), "chart-zone-test", &fishRollContext{}); has {
		t.Fatal("定位不到海图区号时不该启用钓点观测")
	}
}

// 挪威海场景名要解析到海图 level key，否则海图区号定位不到，
// 带“海图”前置条件的鱼种全被判不合格。
func TestPrepareFishRollRuntimeResolvesChartFromSceneName(t *testing.T) {
	srv, db := newRigTestServer(t)
	if _, err := db.SQL.Exec(
		`INSERT OR REPLACE INTO loc_alias(alias,loc) VALUES('level_012_north_sea','挪威海')`); err != nil {
		t.Fatal(err)
	}
	// 1300/100 落在 loc12_157 网格内（assets/sonar_grids_4_0_24799.json）。
	c := &fishRollContext{loc: "loc12_157", x: 1300, z: 100, hasPos: true, rigType: 53}
	srv.prepareFishRollRuntime(context.Background(), c, "挪威海")
	if !c.runtime.chartKnown {
		t.Fatal("挪威海场景名没能定位到海图，chartKnown 仍为 false")
	}
	if !c.runtime.zoneKnown || c.runtime.zone != 157 {
		t.Fatalf("海图区号=%d known=%v，期望 157", c.runtime.zone, c.runtime.zoneKnown)
	}
}

// 内陆湖没有声呐水域，解析到 level_002_kirzah_river 后 chartKnown 必须保持 false，
// 不能因为换了 level key 解析就把内陆湖也当成海图。
func TestPrepareFishRollRuntimeInlandHasNoChart(t *testing.T) {
	srv, db := newRigTestServer(t)
	if _, err := db.SQL.Exec(
		`INSERT OR REPLACE INTO loc_alias(alias,loc) VALUES('level_002_kirzah_river','惟有诺克河')`); err != nil {
		t.Fatal(err)
	}
	c := &fishRollContext{loc: "lvl2_01", x: 100, z: 100, hasPos: true, rigType: 1}
	srv.prepareFishRollRuntime(context.Background(), c, "惟有诺克河")
	if c.runtime.chartKnown {
		t.Fatal("内陆湖不该被判成有海图")
	}
	if c.runtime.zoneKnown {
		t.Fatal("内陆湖不该定位出海图区号")
	}
}
