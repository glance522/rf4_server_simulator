package gameserver

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type fishSpotEnvironment struct {
	Known     bool
	DepthM    float64
	DistanceM float64
	Current   float64
	Structure float64
	Substrate string
}

type fishWaterConditions struct {
	Season       int
	SurfaceTempC float64
	WaterTempC   float64
	Clarity      float64
	Light        float64
	Turbulence   float64
}

type lureAttributes struct {
	ItemID       string
	Name         string
	TypeID       int64
	Type         string
	Color        string
	ColorKnown   bool
	Reflectivity float64
	Glow         float64
	Vibration    float64
	Sound        float64
	SizeCM       float64
	SizeClass    int64
	WeightG      float64
	DiveDepth    float64
	DiveDepthM   float64
	SinkRate     float64
	Attraction   int64
}

// resolveRollLevelKey 把判鱼上下文的 loc 换成海图用的 level key。
//
// c.loc 是客户端场景名（loc12_157），不是 1/12、1/14 那种目标地图 key：
// travelLevelKey 只认 map_defs.json 里的 level_* key，场景名会被判无效并回退出生图，
// 海图区号随之定位不到，“海图”前置条件整体失效。场景名必须先用 loc_alias 解析，
// 与 cafe_orders、econ_market、fishing_weather_context 一致；解析不出来再回退。
func (s *Server) resolveRollLevelKey(ctx context.Context, loc string) string {
	if s == nil || s.DB == nil || loc == "" {
		return travelLevelKey(loc)
	}
	if key := s.DB.ResolveLevelKey(ctx, loc); key != "" {
		return travelLevelKey(key)
	}
	return travelLevelKey(loc)
}

func (s *Server) prepareFishRollRuntime(ctx context.Context, c *fishRollContext, statLoc string) {
	if c == nil {
		return
	}
	if c.runtime == nil {
		c.runtime = &fishRollRuntime{}
	}
	if c.query != nil && c.query.regional {
		// A regional estimate deliberately excludes point-specific depth,
		// substrate, structure and current. Production rolls keep them all.
		c.spotEnv = fishSpotEnvironment{}
	} else if !c.spotEnv.Known {
		c.spotEnv = fishSpotEnvironmentFor(c)
	}
	if !c.runtime.levelChecked {
		c.runtime.levelChecked = true
		levelKey := s.resolveRollLevelKey(ctx, c.loc)
		c.runtime.chartKnown = sonarChartLevel(levelKey)
		if strings.HasPrefix(c.loc, "loc12_") {
			c.runtime.sonarSystemID = c.loc
		}
		if c.runtime.chartKnown {
			// 海图区号以客户端上报的 loc12_N 为权威（2026-11-14 实测：海钓 14/2
			// 落点是客户端局部坐标，不是世界坐标，按坐标反查永不命中——玩家真实
			// 在 198 区时落点报 (-100,680)，而客户端 loc 恒为 loc12_198）。
			// 坐标反查只作缺省回退。
			if n, ok := sonarZoneFromLoc(c.loc); ok {
				c.runtime.zone, c.runtime.zoneKnown = n, true
			} else if c.hasPos {
				c.runtime.zone, c.runtime.zoneKnown = sonarZoneForPoint(levelKey, c.x, c.z)
			}
		}
	}
	if c.runtime.spotPref == nil {
		c.runtime.spotPref, c.runtime.spotData = s.loadFishSpotPreference(ctx, statLoc, c)
	}
	if !c.runtime.lureKnown && c.rigType != 0 {
		for _, id := range c.baits {
			if lure, ok := s.loadLureAttributes(ctx, id); ok {
				c.runtime.lure, c.runtime.lureKnown = lure, true
				break
			}
		}
	}
}

func fishSpotEnvironmentFor(c *fishRollContext) fishSpotEnvironment {
	if c == nil {
		return fishSpotEnvironment{}
	}
	key := fmt.Sprintf("%s/%d/%.2f/%.2f", c.loc, c.spot, c.x, c.z)
	h := fishVariation(key)
	substrates := []string{"mud", "sand", "gravel", "rock", "weed"}
	substrate := substrates[int(hashFloat(key+"/substrate")*float64(len(substrates)))%len(substrates)]
	depth := 1.5 + h*18
	distance := 4 + fishVariation(key+"/distance")*70
	if (c.rigType >= 50 && c.rigType <= 60) || strings.Contains(strings.ToLower(c.loc), "sea") {
		depth = 18 + h*210
		distance *= 4
	}
	return fishSpotEnvironment{
		Known: true, DepthM: roundTo(depth, 2), DistanceM: roundTo(distance, 1),
		Current:   roundTo(.12+fishVariation(key+"/current")*.78, 3),
		Structure: roundTo(.15+fishVariation(key+"/structure")*.8, 3),
		Substrate: substrate,
	}
}

func hashFloat(s string) float64 {
	return float64(fnvHash(s)%10000) / 10000
}

func fnvHash(s string) uint64 {
	var h uint64 = 1469598103934665603
	for _, b := range []byte(s) {
		h ^= uint64(b)
		h *= 1099511628211
	}
	return h
}

// loadFishSpotPreference 读取攻略钓点观测。挪威海的点位按海图区号记录，
// 与客户端水域 systemID 的数字段同源，可以直接对上；内陆湖的点位是攻略地图
// 上的网格文本（"67:57"），换不成客户端落点坐标，这里一律不启用，避免把
// 别处的鱼种当成当前钓点的观测。
func (s *Server) loadFishSpotPreference(ctx context.Context, statLoc string, c *fishRollContext) (map[string]float64, bool) {
	out := map[string]float64{}
	if s == nil || s.DB == nil || statLoc == "" || c == nil || c.runtime == nil || !c.runtime.zoneKnown {
		return out, false
	}
	rows, err := s.DB.SQL.QueryContext(ctx,
		`SELECT spot,fish_key,catches FROM spot_fish WHERE loc=?`, statLoc)
	if err != nil {
		return out, false
	}
	defer rows.Close()
	target := strconv.Itoa(c.runtime.zone)
	matched := false
	for rows.Next() {
		var spot, fish string
		var catches int64
		if rows.Scan(&spot, &fish, &catches) != nil || catches <= 0 {
			continue
		}
		if spot == target {
			matched = true
			out[fish] += math.Sqrt(float64(catches))
		}
	}
	return out, matched
}

// applyFishSpotBias 只在静态库对该地点存在钓点观测时启用。匹配到当前钓点的
// 鱼种保留观测加权；其余鱼种仍留少量杂口，避免把攻略点位变成绝对黑名单。
func applyFishSpotBias(keys []string, weights []float64, pref map[string]float64, hasData bool) {
	if len(keys) != len(weights) || !hasData {
		return
	}
	for i, key := range keys {
		if value := pref[key]; value > 0 {
			weights[i] *= clampFloat(1+math.Log1p(value)*.16, 1, 1.75)
		} else {
			weights[i] *= .35
		}
	}
}

func (s *Server) loadLureAttributes(ctx context.Context, id string) (lureAttributes, bool) {
	if s == nil || s.DB == nil || strings.TrimSpace(id) == "" {
		return lureAttributes{}, false
	}
	var name, raw string
	var category int64
	err := s.DB.SQL.QueryRowContext(ctx, `
		SELECT COALESCE(c.name_zh,''),d.category,COALESCE(d.attrs_json,'{}')
		FROM item_def d LEFT JOIN item_catalog c ON c.item_id=d.item_id
		WHERE d.item_id=?`, id).Scan(&name, &category, &raw)
	if err != nil || category != catLure {
		return lureAttributes{}, false
	}
	var attrs map[string]any
	_ = json.Unmarshal([]byte(raw), &attrs)
	typeID := int64(0)
	if rawType, ok := attrs["mbemakijdeh"].(float64); ok {
		typeID = int64(rawType)
	}
	lower := strings.ToLower(id + " " + name)
	color, colorKnown := lureColorFamily(lower)
	lure := lureAttributes{
		ItemID: id, Name: name, TypeID: typeID, Type: lureTypeName(typeID),
		Color: color, ColorKnown: colorKnown,
		SizeCM:     lureSizeFromName(lower, typeID),
		SizeClass:  lureIntAttr(attrs, "gkfbafmbpam"),
		Attraction: lureIntAttr(attrs, "boebkpoodje"),
	}
	if size := lureFloatAttr(attrs, "hkgndoipdlc"); size > 0 {
		lure.SizeCM = clampFloat(size, 2, 45)
	}
	if weight := lureFloatAttr(attrs, "dhlkahnodoo"); weight > 0 {
		lure.WeightG = clampFloat(weight, .5, 1200)
	}
	switch lure.TypeID {
	case 1:
		lure.DiveDepth, lure.Reflectivity, lure.Vibration = .34, .92, .72
	case 2:
		lure.DiveDepth, lure.Reflectivity, lure.Vibration = .48, .88, .58
	case 3, 8, 9, 13, 14, 15, 16:
		lure.DiveDepth, lure.Reflectivity, lure.Vibration = .56, .38, .42
	case 4:
		lure.DiveDepth, lure.Reflectivity, lure.Vibration = .62, .68, .66
	case 5, 10:
		lure.DiveDepth, lure.Reflectivity, lure.Vibration = .10, .55, .72
	case 6:
		lure.DiveDepth, lure.Reflectivity, lure.Vibration = .76, .86, .54
	case 7:
		lure.DiveDepth, lure.Reflectivity, lure.Vibration = .44, .62, .58
	case 11:
		lure.DiveDepth, lure.Reflectivity, lure.Vibration = .18, .25, .18
	case 12, 18, 19, 20:
		lure.DiveDepth, lure.Reflectivity, lure.Vibration = .72, .72, .76
	case 17:
		lure.DiveDepth, lure.Reflectivity, lure.Vibration = .78, .35, .20
	}
	switch lure.Color {
	case "silver":
		lure.Reflectivity = math.Max(lure.Reflectivity, .9)
	case "gold":
		lure.Color, lure.Reflectivity = "gold", math.Max(lure.Reflectivity, .82)
	}
	if strings.Contains(lower, "flu") || strings.Contains(lower, "glow") || strings.Contains(lower, "fire") || strings.Contains(lower, "uv") {
		lure.Glow = .9
	}
	if strings.Contains(lower, "rattl") || strings.Contains(lower, "popper") || strings.Contains(lower, "vibra") {
		lure.Sound = math.Max(lure.Sound, 3)
		lure.Vibration = math.Max(lure.Vibration, .8)
	}
	switch lure.Attraction {
	case 3:
		lure.Sound = math.Max(lure.Sound, 3)
	case 4:
		lure.Glow = math.Max(lure.Glow, .9)
	}
	if lure.WeightG <= 0 {
		lure.WeightG = lureWeightFromName(lower, lure.SizeCM, lure.TypeID)
	}
	if lure.SinkRate <= 0 {
		lure.SinkRate = lureSinkRate(lure.TypeID)
	}
	return lure, true
}

func lureIntAttr(attrs map[string]any, key string) int64 {
	if value, ok := attrs[key].(float64); ok && value >= 0 {
		return int64(value)
	}
	return 0
}

func lureFloatAttr(attrs map[string]any, key string) float64 {
	value, ok := attrs[key].(float64)
	if !ok {
		return 0
	}
	if decoded, valid := decodeU32F32(value); valid {
		return decoded
	}
	return value
}

func lureSizeFromName(s string, typeID int64) float64 {
	size := firstPositiveNumber(s)
	if size <= 0 {
		switch typeID {
		case 5, 7, 10, 17:
			size = 7
		case 6:
			size = 16
		case 16:
			size = 14
		default:
			size = 9
		}
	}
	return clampFloat(size, 2, 45)
}

func lureWeightFromName(s string, sizeCM float64, typeID int64) float64 {
	if oz := ouncesInString(s); oz > 0 {
		return clampFloat(oz*28.3495, .5, 1200)
	}
	base := map[int64]float64{
		1: 10, 2: 12, 3: 5, 4: 10, 5: 8, 6: 120, 7: 18, 8: 7,
		9: 6, 10: 10, 11: 1, 12: 20, 13: 15, 14: 30, 15: 15,
		16: 6, 17: 1, 18: 40, 19: 20, 20: 14,
	}[typeID]
	if base <= 0 {
		base = 10
	}
	return clampFloat(base*math.Pow(math.Max(1, sizeCM)/10, 1.35), .5, 1200)
}

func lureSinkRate(typeID int64) float64 {
	switch typeID {
	case 5, 10:
		return .02
	case 7, 17:
		return .12
	case 9:
		return .18
	case 8, 13, 15, 16:
		return .22
	case 2, 3, 4, 12, 18, 19, 20:
		return .35
	case 6, 14:
		return .72
	default:
		return .2
	}
}

func firstPositiveNumber(s string) float64 {
	start := -1
	for i, r := range s {
		if r >= '0' && r <= '9' {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			value, err := strconv.ParseFloat(s[start:i], 64)
			if err == nil && value > 0 {
				return value
			}
			start = -1
		}
	}
	if start >= 0 {
		value, _ := strconv.ParseFloat(s[start:], 64)
		return value
	}
	return 0
}

func ouncesInString(s string) float64 {
	idx := strings.Index(s, " oz")
	if idx <= 0 {
		return 0
	}
	left := strings.TrimSpace(s[:idx])
	start := len(left)
	for start > 0 {
		c := left[start-1]
		if (c >= '0' && c <= '9') || c == '.' || c == '/' {
			start--
			continue
		}
		break
	}
	raw := left[start:]
	if strings.Contains(raw, "/") {
		num, den, ok := strings.Cut(raw, "/")
		if !ok {
			return 0
		}
		n, errN := strconv.ParseFloat(num, 64)
		d, errD := strconv.ParseFloat(den, 64)
		if errN == nil && errD == nil && d > 0 {
			return n / d
		}
		return 0
	}
	value, _ := strconv.ParseFloat(raw, 64)
	return value
}

func lureTypeName(typeID int64) string {
	switch typeID {
	case 1:
		return "spinner"
	case 2:
		return "spoon"
	case 3:
		return "foam"
	case 4:
		return "crank"
	case 5:
		return "popper"
	case 6:
		return "pilker"
	case 7:
		return "jerkbait"
	case 8:
		return "tube"
	case 9:
		return "stickbait"
	case 10:
		return "surface"
	case 11:
		return "fly"
	case 12:
		return "monojig"
	case 13:
		return "soft_eel"
	case 14:
		return "octopus"
	case 15:
		return "shrimp"
	case 16:
		return "worm"
	case 17:
		return "bead"
	case 18:
		return "jig_head"
	case 19:
		return "jig"
	case 20:
		return "spinnerbait"
	default:
		return "unknown"
	}
}

func lureColorFamily(s string) (string, bool) {
	tokens := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
	has := func(values ...string) bool {
		for _, token := range tokens {
			for _, value := range values {
				if token == value {
					return true
				}
			}
		}
		return false
	}
	switch {
	case has("black", "blk", "dark", "night"):
		return "dark", true
	case has("silver", "sil", "chrome"):
		return "silver", true
	case has("pe", "pi", "wht", "white", "nat", "natural", "cop"):
		return "natural", true
	case has("gold", "yellow", "yel", "brown"):
		return "gold", true
	case has("orange", "orn"):
		return "orange", true
	case has("red", "ros", "pink", "fire", "flu", "chart"):
		return "bright", true
	case has("blue", "blu"):
		return "blue", true
	case has("green", "grn"):
		return "green", true
	case has("purple", "violet"):
		return "silver", true
	default:
		return "", false
	}
}

func fishSeasonAt(nowUnix int64) int {
	day, _ := worldDayAndHour(nowUnix)
	day %= 365
	monthLengths := [...]int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	month := 0
	for month < len(monthLengths) && day >= int64(monthLengths[month]) {
		day -= int64(monthLengths[month])
		month++
	}
	switch month {
	case 11, 0, 1:
		return 3 // 冬
	case 2, 3, 4:
		return 0 // 春
	case 5, 6, 7:
		return 1 // 夏
	default:
		return 2 // 秋
	}
}

func fishWaterConditionsFor(profile fishEcologyProfile, c fishRollContext) fishWaterConditions {
	_, hour := worldDayAndHour(c.now.Unix())
	season := fishSeasonAt(c.now.Unix())
	seasonalAir := []float64{10, 23, 13, 1}[season]
	seasonalDeep := []float64{9, 14, 11, 4}[season]
	air := c.weather.airTemp
	if !c.weather.known || !finiteWeatherValue(air) {
		air = seasonalAir
	}
	daylight := clampFloat((1+math.Cos((float64(hour)-12)*math.Pi/12))/2, 0, 1)
	layer := .45
	switch {
	case bottomRigTypes[c.rigType]:
		layer = .93
	case rigTypeLureFamily[c.rigType]:
		layer = normalizeWaterLayer(c.lureLayer)
	case c.depthCM > 0:
		layer = normalizeWaterLayer(float64(c.depthCM) / 500)
	}
	thermalResponse := .36 + .16*clampFloat(profile.Activity, .5, 1.5)
	surface := seasonalAir + (air-seasonalAir)*thermalResponse + daylight*1.2
	if c.weather.known {
		if finiteWeatherValue(c.weather.previousAirTemp) {
			surface += (air - c.weather.previousAirTemp) * .18
		}
		surface -= math.Min(.8, c.weather.wind*.035)
	}
	deep := seasonalDeep + (surface-seasonalDeep)*.22
	waterTemp := surface*(1-layer) + deep*layer
	cloud := weatherCloudiness(c.weather.kind)
	rain := weatherRainFactor(c.weather.kind)
	clarity := clampFloat(.92-cloud*.28-rain*.34-c.weather.fog*.25, .22, 1)
	light := daylight * (1 - cloud*.55) * math.Exp(-layer*(1.7-clarity*.7))
	turbulence := clampFloat(c.weather.wind/12+rain*.35+c.spotEnv.Current*.45, 0, 1)
	return fishWaterConditions{
		Season: season, SurfaceTempC: roundTo(surface, 2), WaterTempC: roundTo(waterTemp, 2),
		Clarity: roundTo(clarity, 3), Light: roundTo(clampFloat(light, .02, 1), 3),
		Turbulence: roundTo(turbulence, 3),
	}
}

func weatherCloudiness(kind int) float64 {
	switch kind {
	case fishingWeatherClear:
		return 0
	case fishingWeatherFewClouds:
		return .18
	case fishingWeatherCloudy, fishingWeatherRain:
		return .55
	case fishingWeatherHeavyRain, fishingWeatherThunderstorm:
		return .82
	default:
		return .4
	}
}

func weatherRainFactor(kind int) float64 {
	switch kind {
	case fishingWeatherRain:
		return .35
	case fishingWeatherHeavyRain:
		return .78
	case fishingWeatherThunderstorm:
		return 1
	default:
		return 0
	}
}

func fishSeasonWeight(profile fishEcologyProfile, season int) float64 {
	if profile.SeasonMask&(1<<uint(season)) == 0 {
		return .55
	}
	return 1
}

func fishWeatherKindWeight(profile fishEcologyProfile, kind int) float64 {
	name := ""
	switch kind {
	case fishingWeatherClear:
		name = "clear"
	case fishingWeatherFewClouds:
		name = "few_clouds"
	case fishingWeatherCloudy:
		name = "cloudy"
	case fishingWeatherRain:
		name = "rain"
	case fishingWeatherHeavyRain:
		name = "heavy_rain"
	case fishingWeatherThunderstorm:
		name = "thunderstorm"
	default:
		return 1
	}
	if weight, ok := fishEcologyJSONMap(profile.WeatherJSON)[name]; ok {
		return clampFloat(weight, .4, 1.5)
	}
	return 1
}

func fishWeatherExtraWeight(profile fishEcologyProfile, weather fishingWeather) float64 {
	if !weather.known {
		return 1
	}
	weights := fishEcologyJSONMap(profile.WeatherJSON)
	factor := 1.0
	if finiteWeatherValue(weather.wind) && weather.wind > 0 {
		if weight := weights["wind"]; weight > 0 {
			factor *= 1 + (clampFloat(weight, .4, 1.5)-1)*clampFloat(weather.wind/10, 0, 1)
		}
	}
	if finiteWeatherValue(weather.fog) && weather.fog > 0 {
		if weight := weights["fog"]; weight > 0 {
			factor *= 1 + (clampFloat(weight, .4, 1.5)-1)*clampFloat(weather.fog, 0, 1)
		}
	}
	return clampFloat(factor, .55, 1.35)
}

func fishSpotBiteWeight(profile fishEcologyProfile, spot fishSpotEnvironment) float64 {
	if !spot.Known {
		return 1
	}
	substrate := fishEcologyJSONMap(profile.SubstrateJSON)[spot.Substrate]
	if substrate <= 0 {
		substrate = .75
	}
	current := 1 - math.Abs(profile.CurrentPref-spot.Current)*.32
	structure := 1 + (profile.Aggression-1)*spot.Structure*.18
	distance := 1 + (profile.Aggression-.95)*math.Log1p(spot.DistanceM)*.035
	depth := 1 + math.Min(.2, spot.DepthM/220)*profile.LayerPref
	return clampFloat(substrate*current*structure*distance*depth, .30, 1.60)
}

func fishMotionConditionWeight(profile fishEcologyProfile, c fishRollContext, water fishWaterConditions) float64 {
	factor := 1.0
	current := clampFloat(water.Turbulence, 0, 1)
	switch c.lureAction {
	case "marine_jig":
		factor *= 1 + (profile.CurrentPref-.5)*clampFloat(current-.35, 0, 1)*.25
		if c.rigType >= 50 && c.rigType <= 60 {
			factor *= 1.08
		}
	case "jig":
		factor *= 1 + (profile.CurrentPref-.5)*clampFloat(current-.35, 0, 1)*.18
	case "drift":
		if current > .5 {
			factor *= 1 + (profile.CurrentPref-.5)*.22
		} else if current < .2 && profile.CurrentPref > .5 {
			factor *= .90
		}
	case "trolling":
		if c.lureSpeed > 0 {
			factor *= 1 + math.Min(.12, float64(c.lureSpeed)*.008)
		}
		factor *= 1 + (profile.Activity-1)*.15
	}
	return clampFloat(factor, .70, 1.45)
}

func (s *Server) fishLureWeight(profile fishEcologyProfile, c fishRollContext, water fishWaterConditions) float64 {
	if c.runtime == nil || !c.runtime.lureKnown {
		return 1
	}
	lure := c.runtime.lure
	actionBonus := lureActionTypeWeight(lure.Type, c.lureAction)
	diveDepth := lure.DiveDepth
	if lure.DiveDepthM > 0 && c.spotEnv.DepthM > 0 {
		diveDepth = clampFloat(lure.DiveDepthM/c.spotEnv.DepthM, 0, 1)
	}
	depthDistance := math.Abs(normalizeWaterLayer(c.lureLayer) - diveDepth)
	depthMatch := .72 + .28*math.Exp(-depthDistance*depthDistance*6)
	colorWeight := 1.0
	if lure.ColorKnown {
		colorWeight = fishEcologyJSONMap(profile.ColorJSON)[lure.Color]
		if colorWeight <= 0 {
			colorWeight = 1
		}
	}
	reflect := 1 + (lure.Reflectivity-.5)*(water.Light-.45)*.45
	if water.Clarity < .5 && lure.Color == "dark" {
		reflect += .10
	}
	glow := 1 + lure.Glow*(1-water.Light)*.18
	vibration := 1 + (lure.Vibration-.45)*water.Turbulence*.20
	sound := 1 + math.Log1p(lure.Sound)*(water.Clarity-.45)*.06
	sizeClass := math.Max(1, float64(lure.SizeClass))
	size := 1 + math.Log1p(math.Max(1, lure.WeightG))*(profile.Aggression-1)*.035
	size *= 1 + (sizeClass-2.5)*.025
	return clampFloat(actionBonus*depthMatch*colorWeight*reflect*glow*vibration*sound*size, .28, 1.75)
}

func lureActionTypeWeight(typeName, action string) float64 {
	if action == "" {
		return 1
	}
	weights := map[string]map[string]float64{
		"spinner":     {"steady": 1.20, "speed_up": 1.12, "bottom_layer_move": 1.08, "twitch": .92},
		"spoon":       {"steady": 1.20, "speed_up": 1.10, "trolling": 1.14, "jerk": 1.04},
		"foam":        {"jig": 1.12, "twitch": 1.10, "bottom_motionless": 1.05, "bottom_move": 1.08},
		"crank":       {"steady": 1.16, "trolling": 1.18, "stop_go": 1.08, "jerk": 1.04},
		"popper":      {"walk_dog": 1.28, "surface_motionless": 1.24, "twitch": 1.14, "splash": 1.15},
		"pilker":      {"marine_jig": 1.34, "vertical_dive": 1.18, "vertical_ascend": 1.14, "bottom_layer_move": 1.10},
		"jerkbait":    {"jerk": 1.34, "hard_twitch": 1.30, "twitch": 1.20, "stop_go": 1.14},
		"tube":        {"jig": 1.18, "bottom_motionless": 1.10, "bottom_move": 1.12, "vertical_dive": 1.08},
		"stickbait":   {"walk_dog": 1.24, "twitch": 1.16, "jerk": 1.12, "stop_go": 1.10},
		"surface":     {"walk_dog": 1.22, "surface_motionless": 1.22, "splash": 1.16},
		"fly":         {"drift": 1.18, "vertical_dive": 1.12, "steady": 1.06},
		"monojig":     {"jig": 1.26, "bottom_motionless": 1.15, "vertical_dive": 1.10},
		"soft_eel":    {"jig": 1.24, "bottom_move": 1.16, "vertical_dive": 1.12},
		"octopus":     {"steady": 1.12, "drift": 1.12, "bottom_motionless": 1.10},
		"shrimp":      {"jig": 1.12, "bottom_motionless": 1.10, "vertical_dive": 1.08},
		"worm":        {"jig": 1.08, "bottom_contact": 1.10, "bottom_move": 1.08},
		"jig_head":    {"jig": 1.24, "bottom_motionless": 1.12, "bottom_move": 1.14},
		"jig":         {"jig": 1.28, "hard_twitch": 1.12, "bottom_motionless": 1.10},
		"spinnerbait": {"steady": 1.18, "speed_up": 1.10, "bottom_layer_move": 1.12},
	}
	if value, ok := weights[typeName][action]; ok {
		return value
	}
	return 1
}

func fishSkillEnvironmentWeight(key string, c fishRollContext) float64 {
	bonus := c.skill
	factor := 1.0
	sp := strings.ToLower(key)
	switch {
	case containsAny(sp, "pike", "perch", "zander", "walleye", "bass", "asp", "piksha"):
		factor += bonus.PredatorBoost * .24
	case containsAny(sp, "salmon", "trout", "char", "taimen", "lenok", "omul", "muksun", "whitefish", "sig_", "vendace"):
		factor += bonus.SalmonidBoost * .22
	case bottomRigTypes[c.rigType]:
		factor += bonus.BottomBoost * .22
	}
	if c.rigType == 1 || c.rigType == 2 || c.rigType == 3 || c.rigType == 4 || c.rigType == 21 {
		factor += bonus.WormBoost * .12
	}
	if floatRigTypes[c.rigType] {
		factor += bonus.FloatBoost * .20
	}
	return clampFloat(factor, 1, 1.35)
}

func roundTo(v float64, places int) float64 {
	scale := math.Pow10(places)
	return math.Round(v*scale) / scale
}

func containsAny(s string, parts ...string) bool {
	for _, part := range parts {
		if strings.Contains(s, part) {
			return true
		}
	}
	return false
}
