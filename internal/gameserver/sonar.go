package gameserver

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// 声呐网格资产；文件定义各水域的网格几何、单元格顺序和采样参数。
//
//go:embed assets/sonar_grids_4_0_24799.json
var sonarGridsJSON []byte

const (
	sonarResponseClass = "efnipphkbce"
	sonarCellClass     = "gphnpomceak"
	sonarEchoClass     = "inhomdofcfb"
	sonarCellArgCode   = "496"
	sonarEchoArgCode   = "497"
)

// sonarGrid 是一块水域的声呐网格几何。level 是该水域所属地图 key，
// 用来把抛竿落点限制在同一张海图里找区号。
type sonarGrid struct {
	level    string
	topLeftX float64
	topLeftZ float64
	width    float64
	length   float64
	cellSize float64
}

type sonarProfile struct {
	depthMin float64
	depthMax float64
	sizeMed  float64
	sizeP90  float64
}

type sonarGridAsset struct {
	SchemaVersion int                  `json:"schema_version"`
	Profile       string               `json:"profile"`
	MapID         string               `json:"map_id"`
	SourceLevel   string               `json:"source_level"`
	SourceSHA256  string               `json:"source_sha256"`
	CellOrder     string               `json:"cell_order"`
	Grids         map[string][]float64 `json:"grids"`
}

var (
	sonarGridsOnce sync.Once
	sonarGrids     map[string]sonarGrid
	sonarGridsErr  error
	sonarSeedSeq   atomic.Uint64
)

func loadSonarGrids() (map[string]sonarGrid, error) {
	sonarGridsOnce.Do(func() {
		var asset sonarGridAsset
		if err := json.Unmarshal(sonarGridsJSON, &asset); err != nil {
			sonarGridsErr = fmt.Errorf("解析声呐网格资产: %w", err)
			return
		}
		grids := make(map[string]sonarGrid, len(asset.Grids))
		for systemID, raw := range asset.Grids {
			if systemID == "" || len(raw) != 5 {
				continue
			}
			candidate := sonarGrid{
				topLeftX: raw[0],
				topLeftZ: raw[1],
				width:    raw[2],
				length:   raw[3],
				cellSize: raw[4],
			}
			if !finiteSonarNumber(candidate.topLeftX) || !finiteSonarNumber(candidate.topLeftZ) ||
				!finiteSonarNumber(candidate.width) || !finiteSonarNumber(candidate.length) ||
				!finiteSonarNumber(candidate.cellSize) || candidate.width <= 0 ||
				candidate.length <= 0 || candidate.cellSize <= 0 {
				continue
			}
			candidate.level = asset.MapID
			grids[systemID] = candidate
		}
		if len(grids) == 0 {
			sonarGridsErr = fmt.Errorf("声呐网格资产没有有效水域")
			return
		}
		sonarGrids = grids
	})
	return sonarGrids, sonarGridsErr
}

func finiteSonarNumber(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func sonarGridDimensions(g sonarGrid) (cols, rows int) {
	cols = int(g.width / g.cellSize)
	rows = int(g.length / g.cellSize)
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	return cols, rows
}

// sonarProfileFor 读取水域的声呐采样档案；查询失败或没有档案时返回 nil，
// 采样会使用默认区间。
func (s *Server) sonarProfileFor(ctx context.Context, systemID string) *sonarProfile {
	if s == nil || s.DB == nil || systemID == "" {
		return nil
	}
	var depthMin, depthMax, sizeMed, sizeP90 sql.NullFloat64
	err := s.DB.SQL.QueryRowContext(ctx, `
		SELECT depth_min, depth_max, size_med, size_p90
		FROM sonar_profile WHERE system_id=?`, systemID).
		Scan(&depthMin, &depthMax, &sizeMed, &sizeP90)
	if err != nil {
		return nil
	}
	return &sonarProfile{
		depthMin: sonarProfileValue(depthMin),
		depthMax: sonarProfileValue(depthMax),
		sizeMed:  sonarProfileValue(sizeMed),
		sizeP90:  sonarProfileValue(sizeP90),
	}
}

func sonarProfileValue(v sql.NullFloat64) float64 {
	if !v.Valid || !finiteSonarNumber(v.Float64) {
		return 0
	}
	return v.Float64
}

func newSonarRand() *rand.Rand {
	seed := uint64(time.Now().UnixNano()) ^ sonarSeedSeq.Add(1)
	return rand.New(rand.NewSource(int64(seed)))
}

func sonarUniform(rng *rand.Rand, low, high float64) float64 {
	return low + (high-low)*rng.Float64()
}

// sonarTriangular 返回 [low, high] 区间内的三角分布采样。
func sonarTriangular(rng *rand.Rand, low, high, mode float64) float64 {
	if high <= low || mode <= low {
		return low
	}
	if mode >= high {
		return high
	}
	c := (mode - low) / (high - low)
	u := rng.Float64()
	if u < c {
		return low + math.Sqrt(u*(high-low)*(mode-low))
	}
	return high - math.Sqrt((1-u)*(high-low)*(high-mode))
}

func sonarDepthRange(profile *sonarProfile) (low, high float64) {
	low, high = 0.8, 5.5
	if profile != nil {
		if profile.depthMin != 0 {
			low = profile.depthMin
		}
		if profile.depthMax != 0 {
			high = profile.depthMax
		}
	}
	low = math.Max(0.1, low)
	high = math.Max(low+0.5, high)
	return low, high
}

func sampleSonarWeightKg(rng *rand.Rand, profile *sonarProfile) float64 {
	if profile != nil {
		weightMedKg := profile.sizeMed
		if weightMedKg == 0 {
			weightMedKg = 4.0
		}
		weightMedKg = math.Max(0.05, weightMedKg)
		weightP90Kg := profile.sizeP90
		if weightP90Kg == 0 {
			weightP90Kg = weightMedKg * 3
		}
		weightP90Kg = math.Max(weightMedKg, weightP90Kg)
		x := rng.Float64()
		switch {
		case x < 0.80:
			return sonarUniform(rng, weightMedKg*0.5, weightMedKg*2.0)
		case x < 0.97:
			return sonarUniform(rng, weightMedKg*1.5, weightP90Kg)
		default:
			return sonarUniform(rng, weightP90Kg, math.Max(weightP90Kg, 97.0))
		}
	}

	if rng.Float64() < 0.12 {
		return sonarUniform(rng, 6, 40)
	}
	return sonarUniform(rng, 0.15, 6)
}

// sampleSonarEcho 生成一条声呐回声。f0 是客户端图标分级直接比较的鱼重 kg，
// 不是克；客户端用 3 kg / 10 kg 两档阈值选择大小图标。
func sampleSonarEcho(rng *rand.Rand, profile *sonarProfile) (weightKg, depthM float64) {
	low, high := sonarDepthRange(profile)
	depthM = sonarTriangular(rng, low, high, low+(high-low)*0.25)
	return sampleSonarWeightKg(rng, profile), depthM
}

func makeSonarEcho(weightKg, depthM float64) (*proto.Value, error) {
	return wiregen.MakeObj(sonarEchoClass, map[int]*proto.Value{
		0: proto.F32(float32(weightKg)),
		1: proto.F32(float32(depthM)),
	})
}

func makeSonarCell(echoes []*proto.Value) (*proto.Value, error) {
	if len(echoes) == 0 {
		return proto.Null(), nil
	}
	code := sonarEchoArgCode
	return wiregen.MakeObj(sonarCellClass, map[int]*proto.Value{
		0: proto.Args(&code, echoes...),
	})
}

func makeEmptySonarResponse(systemID string) (*proto.Value, error) {
	code := sonarCellArgCode
	return wiregen.MakeObj(sonarResponseClass, map[int]*proto.Value{
		0: proto.StrVal(systemID),
		1: proto.Args(&code),
	})
}

type sonarSchool struct {
	centerX int
	centerY int
	radiusX int
	radiusY int
	depthM  float64
	// depthJitterM 只让同一鱼群内的相邻格产生轻微深度起伏；鱼群主线仍
	// 由该鱼种 ecology 的 layer_pref 决定，玩家可以据此调放线深度。
	depthJitterM float64
	fishKey      string
	gradeWeights [4]float64
	lines        cachedGradeLines
}

func randomSonarSchools(cols, rows int, profile *sonarProfile, rng *rand.Rand) []sonarSchool {
	depthLow, depthHigh := sonarDepthRange(profile)
	schools := make([]sonarSchool, 0, 4)
	for clusters := rng.Intn(3) + 2; clusters > 0; clusters-- {
		radiusX := rng.Intn(4) + 1
		radiusY := rng.Intn(3) + 1
		if radiusX*2+1 > cols {
			radiusX = (cols - 1) / 2
		}
		if radiusY*2+1 > rows {
			radiusY = (rows - 1) / 2
		}
		radiusX = maxInt(1, radiusX)
		radiusY = maxInt(1, radiusY)
		centerX := radiusX + rng.Intn(maxInt(1, cols-2*radiusX))
		centerY := radiusY + rng.Intn(maxInt(1, rows-2*radiusY))
		schools = append(schools, sonarSchool{
			centerX: centerX,
			centerY: centerY,
			radiusX: radiusX,
			radiusY: radiusY,
			depthM: sonarTriangular(
				rng, depthLow, depthHigh, depthLow+(depthHigh-depthLow)*0.3,
			),
			depthJitterM: math.Max(0.08, (depthHigh-depthLow)*0.05),
		})
	}
	return schools
}

// synthesizeSonarSchools 把鱼群渲染成客户端要的连续椭圆回声带。
func synthesizeSonarSchools(systemID string, profile *sonarProfile, schools []sonarSchool,
	rng *rand.Rand) (*proto.Value, error) {
	grids, err := loadSonarGrids()
	if err != nil {
		return nil, err
	}
	grid, ok := grids[systemID]
	if !ok {
		return makeEmptySonarResponse(systemID)
	}
	if rng == nil {
		rng = newSonarRand()
	}
	cols, rows := sonarGridDimensions(grid)
	cellCount := cols * rows
	schoolAt := make(map[int]int)
	for schoolIdx, school := range schools {
		for dy := -school.radiusY; dy <= school.radiusY; dy++ {
			for dx := -school.radiusX; dx <= school.radiusX; dx++ {
				nx := (dx * dx * school.radiusY * school.radiusY) +
					(dy * dy * school.radiusX * school.radiusX)
				limit := school.radiusX * school.radiusX * school.radiusY * school.radiusY
				if nx > limit {
					continue
				}
				x, y := school.centerX+dx, school.centerY+dy
				if x < 0 || x >= cols || y < 0 || y >= rows {
					continue
				}
				schoolAt[y*cols+x] = schoolIdx
			}
		}
	}

	items := make([]*proto.Value, 0, cellCount)
	for idx := 0; idx < cellCount; idx++ {
		var echoes []*proto.Value
		if schoolIdx, ok := schoolAt[idx]; ok {
			school := schools[schoolIdx]
			echoCount := rng.Intn(3) + 1
			for echoIdx := 0; echoIdx < echoCount; echoIdx++ {
				var weightKg, depthM float64
				if echoIdx == 0 {
					weightKg = sampleSonarSchoolWeightKg(rng, school, profile)
					depthM = school.depthM
				} else {
					weightKg = sampleSonarSchoolWeightKg(rng, school, profile)
					depthM = school.depthM + sonarUniform(rng, -school.depthJitterM, school.depthJitterM)
					low, high := sonarEchoDepthBand(school)
					depthM = math.Max(low, math.Min(high, depthM))
				}
				echo, err := makeSonarEcho(weightKg, depthM)
				if err != nil {
					return nil, err
				}
				echoes = append(echoes, echo)
			}
		}
		cell, err := makeSonarCell(echoes)
		if err != nil {
			return nil, err
		}
		items = append(items, cell)
	}

	code := sonarCellArgCode
	return wiregen.MakeObj(sonarResponseClass, map[int]*proto.Value{
		0: proto.StrVal(systemID),
		1: proto.Args(&code, items...),
	})
}

// synthesizeSonar 是静态资产回退路径，只在数据库不可用或水域名册为空时使用。
func synthesizeSonar(systemID string, profile *sonarProfile, rng *rand.Rand) (*proto.Value, error) {
	if rng == nil {
		rng = newSonarRand()
	}
	grids, err := loadSonarGrids()
	if err != nil {
		return nil, err
	}
	grid, ok := grids[systemID]
	if !ok {
		return makeEmptySonarResponse(systemID)
	}
	cols, rows := sonarGridDimensions(grid)
	return synthesizeSonarSchools(systemID, profile,
		randomSonarSchools(cols, rows, profile, rng), rng)
}

func sonarDepthRangeForContext(profile *sonarProfile, c fishRollContext) (low, high float64) {
	if profile != nil {
		return sonarDepthRange(profile)
	}
	if c.spotEnv.Known && c.spotEnv.DepthM > 0 {
		return 0.5, math.Max(2, c.spotEnv.DepthM)
	}
	return sonarDepthRange(nil)
}

func sonarPlayerCell(grid sonarGrid, x, z float64) (col, row int, ok bool) {
	if x < grid.topLeftX || x >= grid.topLeftX+grid.width ||
		z > grid.topLeftZ || z <= grid.topLeftZ-grid.length {
		return 0, 0, false
	}
	col = int((x - grid.topLeftX) / grid.cellSize)
	row = int((grid.topLeftZ - z) / grid.cellSize)
	cols, rows := sonarGridDimensions(grid)
	if col < 0 || col >= cols || row < 0 || row >= rows {
		return 0, 0, false
	}
	return col, row, true
}

func fitSonarSchoolCenter(center, radius, dimension int, rng *rand.Rand) int {
	if dimension <= 1 {
		return 0
	}
	if radius > (dimension-1)/2 {
		radius = (dimension - 1) / 2
	}
	if radius < 1 {
		return 0
	}
	low, high := radius, dimension-radius-1
	if high < low {
		return dimension / 2
	}
	if center < low || center > high {
		center = low + rng.Intn(high-low+1)
	}
	return center
}

func sonarCandidateKeys(roster []*store.FishRosterRow) (keys []string, weights []float64) {
	for _, row := range roster {
		if row == nil || strings.TrimSpace(row.FishKey) == "" || row.Catches <= 0 {
			continue
		}
		keys = append(keys, row.FishKey)
		weights = append(weights, row.Catches)
	}
	return keys, weights
}

func sonarWeightedIndex(rng *rand.Rand, weights []float64) int {
	total := 0.0
	for _, weight := range weights {
		if weight > 0 && finiteSonarNumber(weight) {
			total += weight
		}
	}
	if total <= 0 {
		return rng.Intn(len(weights))
	}
	pick := rng.Float64() * total
	acc := 0.0
	last := 0
	for i, weight := range weights {
		if weight <= 0 || !finiteSonarNumber(weight) {
			continue
		}
		last = i
		acc += weight
		if pick <= acc {
			return i
		}
	}
	return last
}

func sonarGradeFromWeights(rng *rand.Rand, weights [4]float64, forceTrophy bool) int64 {
	if forceTrophy {
		weights[0], weights[1] = 0, 0
		if weights[2]+weights[3] <= 0 {
			weights[2] = 1
		}
	}
	total := 0.0
	for _, weight := range weights {
		if weight > 0 && finiteSonarNumber(weight) {
			total += weight
		}
	}
	if total <= 0 {
		return 2
	}
	pick := rng.Float64() * total
	acc := 0.0
	for i, weight := range weights {
		if weight <= 0 || !finiteSonarNumber(weight) {
			continue
		}
		acc += weight
		if pick <= acc {
			return int64(i + 1)
		}
	}
	return 4
}

func sonarTriangularLogRand(rng *rand.Rand, low, high, mode float64) float64 {
	if low <= 0 {
		low = 1
	}
	if high <= low {
		return low
	}
	if mode <= low || mode >= high {
		mode = math.Sqrt(low * high)
	}
	lowLog, highLog, modeLog := math.Log(low), math.Log(high), math.Log(mode)
	u := rng.Float64()
	split := (modeLog - lowLog) / (highLog - lowLog)
	switch {
	case u < split && modeLog > lowLog:
		return math.Exp(lowLog + math.Sqrt(u*(highLog-lowLog)*(modeLog-lowLog)))
	case highLog > modeLog:
		return math.Exp(highLog - math.Sqrt((1-u)*(highLog-lowLog)*(highLog-modeLog)))
	default:
		return mode
	}
}

func sonarSampleGradeGrams(rng *rand.Rand, lines cachedGradeLines, grade int64) float64 {
	var low, high, mode float64
	switch grade {
	case 1:
		if lines.val == nil || *lines.val <= 0 {
			return 0
		}
		q := float64(*lines.val)
		low, high, mode = math.Max(1, .15*q), .98*q, .45*q
	case 2:
		if lines.val == nil || lines.trophy == nil || *lines.trophy <= *lines.val {
			return 0
		}
		q, t := float64(*lines.val), float64(*lines.trophy)
		low, high, mode = 1.02*q, .98*t, math.Min(1.25*q, .92*t)
	case 3:
		if lines.trophy == nil || lines.blue == nil || *lines.blue <= *lines.trophy {
			return 0
		}
		t, b := float64(*lines.trophy), float64(*lines.blue)
		low, high, mode = 1.02*t, .98*b, math.Min(1.15*t, .92*b)
	case 4:
		if lines.blue == nil || *lines.blue <= 0 {
			return 0
		}
		b := float64(*lines.blue)
		low, high, mode = 1.02*b, 1.60*b, 1.10*b
	default:
		return 0
	}
	if high < low {
		return 0
	}
	return sonarTriangularLogRand(rng, low, high, mode)
}

func sonarSettlementGradeLines(statics [6]int64) cachedGradeLines {
	val, trophy, blue := statics[0], statics[1], statics[2]
	return cachedGradeLines{val: &val, trophy: &trophy, blue: &blue}
}

// sonarEchoDepthBand 保证同一鱼群的所有回波围绕学校主深度连续分布；
// 不能再用 profile 默认值二次 clamp，否则无 sonar_profile 时会把深水回波
// 压回浅水默认带。
func sonarEchoDepthBand(school sonarSchool) (low, high float64) {
	low = math.Max(0.1, school.depthM-school.depthJitterM)
	high = math.Max(low, school.depthM+school.depthJitterM)
	return low, high
}

func sampleSonarSchoolWeightKg(rng *rand.Rand, school sonarSchool, profile *sonarProfile) float64 {
	if school.fishKey != "" {
		grade := sonarGradeFromWeights(rng, school.gradeWeights, false)
		for attempts := int64(0); attempts < 4; attempts++ {
			if grams := sonarSampleGradeGrams(rng, school.lines, grade); grams > 0 {
				return math.Max(.001, grams/1000)
			}
			if attempts == 0 {
				grade--
				if grade < 1 {
					break
				}
			}
		}
	}
	return sampleSonarWeightKg(rng, profile)
}

// rosterSonarSchools 用与真实判鱼相同的静态鱼库生成鱼群：
// 水域名册给鱼种与基础权重，spot_fish/生态条件微调，fish_ecology 给水层，
// settleStatics + 当前鱼猎阶段给体重档位。这里只读，不推进咬口或保底。
func (s *Server) rosterSonarSchools(ctx context.Context, systemID string, grid sonarGrid,
	profile *sonarProfile, c fishRollContext, rng *rand.Rand) []sonarSchool {
	if s == nil || s.DB == nil || rng == nil {
		return nil
	}
	c = rollContext([]fishRollContext{c})
	if c.loc == "" {
		c.loc = systemID
	}
	statLoc := s.cachedResolveStatLoc(ctx, c, systemID)
	if statLoc == "" {
		statLoc = s.cachedResolveStatLoc(ctx, c, grid.level)
	}
	if statLoc == "" {
		return nil
	}
	zone, zoneOK := sonarZoneNumber(systemID)
	c.runtime = &fishRollRuntime{
		levelChecked: true, chartKnown: true, zone: zone, zoneKnown: zoneOK,
		sonarSystemID: systemID,
	}
	s.prepareFishRollRuntime(ctx, &c, statLoc)

	roster, err := s.cachedLocRoster(ctx, c, statLoc)
	if err != nil || len(roster) == 0 {
		return nil
	}
	keys, weights := sonarCandidateKeys(roster)
	if len(keys) == 0 {
		return nil
	}
	applyFishSpotBias(keys, weights, c.runtime.spotPref, c.runtime.spotData)
	for i, key := range keys {
		weights[i] *= s.fishBiteEnvironmentWeight(ctx, key, c)
	}

	var hunt *fishHuntState
	if c.uid > 0 {
		hunt = s.loadFishHuntState(ctx, c.uid, systemID, c.spot)
	}
	cols, rows := sonarGridDimensions(grid)
	depthLow, depthHigh := sonarDepthRangeForContext(profile, c)
	playerCol, playerRow, playerKnown := sonarPlayerCell(grid, c.x, c.z)
	if !c.hasPos {
		playerKnown = false
	}

	count := rng.Intn(3) + 2
	if count > len(keys) {
		count = len(keys)
	}
	schools := make([]sonarSchool, 0, count)
	for len(schools) < count {
		idx := sonarWeightedIndex(rng, weights)
		key := keys[idx]
		if weights[idx] <= 0 || !finiteSonarNumber(weights[idx]) {
			break
		}
		weights[idx] = 0

		eco := s.cachedFishEcology(ctx, c, key)
		layerMin := math.Max(0, math.Min(1, eco.LayerMin))
		layerPref := math.Max(0, math.Min(1, eco.LayerPref))
		layerMax := math.Max(0, math.Min(1, eco.LayerMax))
		if layerMax <= layerMin {
			layerMin, layerPref, layerMax = .25, .55, 1
		}
		if layerPref < layerMin || layerPref > layerMax {
			layerPref = (layerMin + layerMax) / 2
		}
		depthM := depthLow + layerPref*(depthHigh-depthLow)
		depthJitter := math.Max(.1, math.Min(2.5, (depthHigh-depthLow)*.025))

		readiness := s.fishHuntReadiness(ctx, key, c, hunt)
		w1, w2, w3, w4, forceTrophy := s.fishHuntWeights(
			readiness.Mode, readiness.Stage, hunt, c.skill.TrophyLuck, readiness.Comp,
		)
		gradeWeights := [4]float64{w1, w2, w3, w4}
		scale := s.tunableF("fish_weight_scale", 1)
		if scale <= 0 {
			scale = 1
		}
		if s.gradeRatioEnabled() {
			lines := s.cachedSettleStatics(ctx, c, key)
			if weights, handled := s.gradeRatioGradeWeights(sonarSettlementGradeLines(lines), 1, scale, 0); handled {
				gradeWeights = weights
				forceTrophy = false
			}
		} else if strength := s.effectiveHookSpawnMatchStrength(); strength > 0 && !forceTrophy {
			compat := s.hookSpawnCompatibilityByGrade(ctx, c, key, scale, 1)
			applyHookSpawnMatch(&w1, &w2, &w3, &w4, compat, strength)
			gradeWeights = [4]float64{w1, w2, w3, w4}
		}
		if forceTrophy {
			gradeWeights[0], gradeWeights[1] = 0, 0
		}

		traits := s.marineTraits(ctx, key, c)
		radiusX := 1 + rng.Intn(3) + int(math.Round(traits.schooling*2))
		radiusY := 1 + rng.Intn(2) + int(math.Round(traits.schooling))
		if radiusX*2+1 > cols {
			radiusX = maxInt(1, (cols-1)/2)
		}
		if radiusY*2+1 > rows {
			radiusY = maxInt(1, (rows-1)/2)
		}
		centerX := radiusX + rng.Intn(maxInt(1, cols-2*radiusX))
		centerY := radiusY + rng.Intn(maxInt(1, rows-2*radiusY))
		if len(schools) == 0 && playerKnown {
			centerX = playerCol + rng.Intn(3) - 1
			centerY = playerRow + rng.Intn(3) - 1
		}
		centerX = fitSonarSchoolCenter(centerX, radiusX, cols, rng)
		centerY = fitSonarSchoolCenter(centerY, radiusY, rows, rng)

		schools = append(schools, sonarSchool{
			centerX: centerX, centerY: centerY, radiusX: radiusX, radiusY: radiusY,
			depthM: depthM, depthJitterM: depthJitter, fishKey: key,
			gradeWeights: gradeWeights, lines: sonarSettlementGradeLines(s.cachedSettleStatics(ctx, c, key)),
		})
	}
	return schools
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// sonarChartLevel 判断该地图有没有海图网格。目前只有挪威海有声呐水域资源，
// 内陆湖没有可核对的水域矩形，返回 false 表示"这里不存在海图区号"。
func sonarChartLevel(levelKey string) bool {
	if levelKey == "" {
		return false
	}
	grids, err := loadSonarGrids()
	if err != nil {
		return false
	}
	for _, grid := range grids {
		if grid.level == levelKey {
			return true
		}
	}
	return false
}

// sonarZoneNumber 取水域 systemID（loc12_215）里的数字段。攻略里的
// "海图215号区域"和客户端的水域编号是同一个数字。
func sonarZoneNumber(systemID string) (int, bool) {
	i := strings.LastIndexByte(systemID, '_')
	if i < 0 || i+1 >= len(systemID) {
		return 0, false
	}
	zone, err := strconv.Atoi(systemID[i+1:])
	if err != nil || zone <= 0 {
		return 0, false
	}
	return zone, true
}

// sonarZoneFromLoc 客户端上报的 loc12_N 直接给出权威海图区号。
func sonarZoneFromLoc(loc string) (int, bool) {
	if !strings.HasPrefix(loc, "loc12_") {
		return 0, false
	}
	return sonarZoneNumber(loc)
}

// sonarZoneForPoint 把抛竿落点换算成海图区号。矩形重叠时取面积最小的水域，
// 结果只由静态资源决定，同一坐标重复查询稳定。落在海图之外返回 ok=false。
func sonarZoneForPoint(levelKey string, x, z float64) (int, bool) {
	if levelKey == "" {
		return 0, false
	}
	grids, err := loadSonarGrids()
	if err != nil {
		return 0, false
	}
	zone, bestArea, found := 0, math.MaxFloat64, false
	for systemID, grid := range grids {
		if grid.level != levelKey {
			continue
		}
		if x < grid.topLeftX || x >= grid.topLeftX+grid.width {
			continue
		}
		if z > grid.topLeftZ || z <= grid.topLeftZ-grid.length {
			continue
		}
		number, ok := sonarZoneNumber(systemID)
		if !ok {
			continue
		}
		area := grid.width * grid.length
		if !found || area < bestArea || (area == bestArea && number < zone) {
			zone, bestArea, found = number, area, true
		}
	}
	return zone, found
}

func (s *Server) sonarResponse(ctx context.Context, systemID string, c fishRollContext) (*proto.Value, error) {
	// 默认只回 15003 空网格；需要回波时用运行参数 sonar_echo=1 打开。
	if s.tunableF("sonar_echo", 0) < 1 {
		return makeEmptySonarResponse(systemID)
	}
	profile := s.sonarProfileFor(ctx, systemID)
	rng := newSonarRand()
	grids, err := loadSonarGrids()
	if err != nil {
		return nil, err
	}
	if grid, ok := grids[systemID]; ok {
		if schools := s.rosterSonarSchools(ctx, systemID, grid, profile, c, rng); len(schools) > 0 {
			details := make([]string, 0, len(schools))
			for _, school := range schools {
				details = append(details, fmt.Sprintf("%s@%.1fm", school.fishKey, school.depthM))
			}
			s.log.Info("声呐鱼群", "system", systemID, "schools", strings.Join(details, ","))
			return synthesizeSonarSchools(systemID, profile, schools, rng)
		}
	}
	return synthesizeSonar(systemID, profile, rng)
}
