package gameserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

const sonarAssetSHA256 = "19b44b49f001e1e59723b9a7ed659770acdf9ffdf0fdc35359e340dc8803fa5e"

func TestSonarGridAsset(t *testing.T) {
	normalized := bytes.ReplaceAll(sonarGridsJSON, []byte("\r\n"), []byte("\n"))
	gotHash := fmt.Sprintf("%x", sha256.Sum256(normalized))
	if gotHash != sonarAssetSHA256 {
		t.Fatalf("声呐网格资产被改写: %s, 期望 %s", gotHash, sonarAssetSHA256)
	}

	var asset sonarGridAsset
	if err := json.Unmarshal(sonarGridsJSON, &asset); err != nil {
		t.Fatal(err)
	}
	if asset.SchemaVersion != 1 || asset.Profile != "4.0.24799" || asset.MapID != "level_012_north_sea" {
		t.Fatalf("资产元数据不符: %+v", asset)
	}
	if asset.SourceLevel != "level24" ||
		asset.SourceSHA256 != "79be0a7a3254d7e28c3d39dc7e1e029b8e25eae74efda2b81e8968674886c65f" ||
		asset.CellOrder != "row-major: index=row*columns+column; rows run from topLeftZ toward -Z" {
		t.Fatalf("资产来源/格序元数据不符: %+v", asset)
	}
	if len(asset.Grids) != 273 {
		t.Fatalf("网格数=%d, 期望 273", len(asset.Grids))
	}

	grids, err := loadSonarGrids()
	if err != nil {
		t.Fatal(err)
	}
	if got := grids["loc12_01"]; got != (sonarGrid{level: "level_012_north_sea", topLeftX: 268, topLeftZ: 1148, width: 116, length: 72, cellSize: 4}) {
		t.Fatalf("loc12_01 几何=%+v", got)
	}
	if got := grids["loc12_273"]; got != (sonarGrid{level: "level_012_north_sea", topLeftX: 2480, topLeftZ: -1340, width: 470, length: 210, cellSize: 10}) {
		t.Fatalf("loc12_273 几何=%+v", got)
	}
}

func TestSonarSynthesisKnownWaterShape(t *testing.T) {
	obj, err := synthesizeSonar("loc12_01", nil, rand.New(rand.NewSource(7)))
	if err != nil {
		t.Fatal(err)
	}
	if obj.Class != sonarResponseClass || obj.TypeID != 15003 {
		t.Fatalf("响应类型=%s/%d", obj.Class, obj.TypeID)
	}
	f0 := obj.Field(0)
	if got := f0.StrOr(); got != "loc12_01" {
		t.Fatalf("f0=%q", got)
	}
	cells := obj.Field(1).Val
	if cells == nil || cells.Kind != proto.KindArgs || cells.ArgCodeStr() != sonarCellArgCode {
		t.Fatalf("f1 应为 args(%s): %#v", sonarCellArgCode, cells)
	}

	// loc12_01 = 116/4 * 72/4 = 29 * 18。
	if cells.ArgCount() != 522 {
		t.Fatalf("声呐格数=%d, 期望 522", cells.ArgCount())
	}

	nonEmpty := 0
	for idx, cell := range cells.Items {
		if cell.Kind == proto.KindNull {
			continue
		}
		nonEmpty++
		if cell.Class != sonarCellClass || cell.TypeID != 15004 {
			t.Fatalf("格 %d 类型=%s/%d", idx, cell.Class, cell.TypeID)
		}
		echoes := cell.Field(0).Val
		if echoes == nil || echoes.Kind != proto.KindArgs || echoes.ArgCodeStr() != sonarEchoArgCode {
			t.Fatalf("格 %d f0 应为 args(%s): %#v", idx, sonarEchoArgCode, echoes)
		}
		if echoes.ArgCount() < 1 || echoes.ArgCount() > 4 {
			t.Fatalf("格 %d 回声数=%d, 期望 1..4", idx, echoes.ArgCount())
		}
		for echoIdx, echo := range echoes.Items {
			if echo.Class != sonarEchoClass || echo.TypeID != 15005 {
				t.Fatalf("格 %d 回声 %d 类型=%s/%d", idx, echoIdx, echo.Class, echo.TypeID)
			}
			weightKg := float64(proto.F32FromBits(echo.Fields[0].U32()))
			depthM := float64(proto.F32FromBits(echo.Fields[1].U32()))
			if math.IsNaN(weightKg) || math.IsInf(weightKg, 0) || weightKg <= 0 ||
				math.IsNaN(depthM) || math.IsInf(depthM, 0) || depthM <= 0 {
				t.Fatalf("格 %d 回声 %d 无效: weight_kg=%f depth_m=%f", idx, echoIdx, weightKg, depthM)
			}
		}
	}
	if nonEmpty == 0 {
		t.Fatal("已知水域没有生成任何鱼群格")
	}
}

func TestSonarSynthesisWireRoundTrip(t *testing.T) {
	obj, err := synthesizeSonar("loc12_225", nil, rand.New(rand.NewSource(11)))
	if err != nil {
		t.Fatal(err)
	}
	body, err := proto.EncodeValue(obj, wiregen.Table())
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := proto.DecodeValue(body, 0, wiregen.Table())
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Class != sonarResponseClass || decoded.TypeID != 15003 {
		t.Fatalf("解码类型=%s/%d", decoded.Class, decoded.TypeID)
	}
	cells := decoded.Field(1).Val
	if cells == nil || cells.Kind != proto.KindArgs || cells.ArgCodeStr() != sonarCellArgCode || cells.ArgCount() == 0 {
		t.Fatalf("解码后声呐网格不是 args(%s): %#v", sonarCellArgCode, cells)
	}

	foundEcho := false
	for _, cell := range cells.Items {
		if cell == nil || cell.Kind == proto.KindNull {
			continue
		}
		echoes := cell.Field(0).Val
		if cell.Class != sonarCellClass || echoes == nil || echoes.Kind != proto.KindArgs ||
			echoes.ArgCodeStr() != sonarEchoArgCode || echoes.ArgCount() == 0 {
			t.Fatalf("解码后的 15004/15005 形状错误: %#v", cell)
		}
		for _, echo := range echoes.Items {
			if echo == nil || echo.Kind == proto.KindNull {
				continue
			}
			if echo.Class != sonarEchoClass || echo.TypeID != 15005 {
				t.Fatalf("解码 15005 元素错误: %s/%d", echo.Class, echo.TypeID)
			}
		}
		foundEcho = true
	}
	if !foundEcho {
		t.Fatal("线缆往返后没有保留任何 15004/15005 回声")
	}
}

func TestSonarUnknownWaterReturnsEmptyGrid(t *testing.T) {
	obj, err := synthesizeSonar("loc99_unknown", nil, rand.New(rand.NewSource(3)))
	if err != nil {
		t.Fatal(err)
	}
	if obj.Class != sonarResponseClass || obj.TypeID != 15003 {
		t.Fatalf("未知水域响应类型=%s/%d", obj.Class, obj.TypeID)
	}
	f0 := obj.Field(0)
	if got := f0.StrOr(); got != "loc99_unknown" {
		t.Fatalf("未知水域 f0=%q", got)
	}
	cells := obj.Field(1).Val
	if cells == nil || cells.Kind != proto.KindArgs || cells.ArgCodeStr() != sonarCellArgCode || cells.ArgCount() != 0 {
		t.Fatalf("未知水域应回空 args(%s): %#v", sonarCellArgCode, cells)
	}

	body, err := proto.EncodeValue(obj, wiregen.Table())
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := proto.DecodeValue(body, 0, wiregen.Table())
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Field(1).Val.Kind != proto.KindArgs || decoded.Field(1).Val.ArgCodeStr() != sonarCellArgCode ||
		decoded.Field(1).Val.ArgCount() != 0 {
		t.Fatalf("未知水域线缆解码后应为空 args(%s): %#v", sonarCellArgCode, decoded.Field(1).Val)
	}
}

func TestSonarEchoSamplingRanges(t *testing.T) {
	rng := rand.New(rand.NewSource(19))
	profile := &sonarProfile{depthMin: 2, depthMax: 6, sizeMed: 10, sizeP90: 30}
	for i := 0; i < 5000; i++ {
		weightKg, depthM := sampleSonarEcho(rng, profile)
		if weightKg < 5 || weightKg > 97 || depthM < 2 || depthM > 6 {
			t.Fatalf("档案采样越界: weight_kg=%f depth_m=%f", weightKg, depthM)
		}
	}

	rng = rand.New(rand.NewSource(23))
	for i := 0; i < 5000; i++ {
		weightKg, depthM := sampleSonarEcho(rng, nil)
		if weightKg < 0.15 || weightKg > 40 || depthM < 0.8 || depthM > 5.5 {
			t.Fatalf("无档案采样越界: weight_kg=%f depth_m=%f", weightKg, depthM)
		}
	}
}

func TestSonarEchoWeightsSpanIconBands(t *testing.T) {
	rng := rand.New(rand.NewSource(29))
	profile := &sonarProfile{depthMin: 1, depthMax: 8, sizeMed: 2, sizeP90: 20}
	var small, medium, large int
	for i := 0; i < 20000; i++ {
		weightKg := sampleSonarWeightKg(rng, profile)
		switch {
		case weightKg < 3:
			small++
		case weightKg <= 10:
			medium++
		default:
			large++
		}
	}
	if small == 0 || medium == 0 || large == 0 {
		t.Fatalf("图标档位没有齐全: small=%d medium=%d large=%d", small, medium, large)
	}
	if small <= medium {
		t.Fatalf("小鱼应占多数，实得 small=%d medium=%d large=%d", small, medium, large)
	}
}

func TestSonarSchoolWeightsUseFishGradeLines(t *testing.T) {
	q, trophy, blue := int64(500), int64(5000), int64(12000)
	school := sonarSchool{
		fishKey:      "test_fish",
		gradeWeights: [4]float64{10, 64, 20, 6},
		lines:        cachedGradeLines{val: &q, trophy: &trophy, blue: &blue},
	}
	rng := rand.New(rand.NewSource(37))
	var small, medium, large int
	for i := 0; i < 20000; i++ {
		weightKg := sampleSonarSchoolWeightKg(rng, school, nil)
		switch {
		case weightKg < 3:
			small++
		case weightKg <= 10:
			medium++
		default:
			large++
		}
	}
	if small == 0 || medium == 0 || large == 0 {
		t.Fatalf("真实档位线没有混出三种图标: small=%d medium=%d large=%d", small, medium, large)
	}
}

func TestSonarRosterSchoolsFollowZoneFishAndEcology(t *testing.T) {
	srv, db := newRigTestServer(t)
	seed := []string{
		`INSERT INTO loc_alias(alias,loc) VALUES('level_012_north_sea','sonar-test-loc')`,
		`INSERT INTO loc_wiki_fish(loc,fish_key,fish_zh) VALUES
			('sonar-test-loc','test_shallow','浅水小鱼'),
			('sonar-test-loc','test_mid','中层鱼'),
			('sonar-test-loc','test_bottom','底层大鱼')`,
		`INSERT INTO loc_fish(loc,fish_key,name_zh,catches) VALUES
			('sonar-test-loc','test_shallow','浅水小鱼',100),
			('sonar-test-loc','test_mid','中层鱼',100),
			('sonar-test-loc','test_bottom','底层大鱼',100)`,
		`INSERT INTO species_weight(fish_key,min_g,max_g,trophy_g) VALUES
			('test_shallow',20,700,300),
			('test_mid',100,6000,4000),
			('test_bottom',500,40000,25000)`,
		`INSERT INTO fish_grade_lines(fish_key,qualified_g,trophy_g,blue_trophy_g) VALUES
			('test_shallow',50,300,500),
			('test_mid',500,4000,6000),
			('test_bottom',3000,25000,35000)`,
		`INSERT INTO fish_ecology(fish_key,layer_min,layer_pref,layer_max) VALUES
			('test_shallow',0.05,0.18,0.35),
			('test_mid',0.30,0.52,0.68),
			('test_bottom',0.72,0.92,1.0)`,
	}
	for _, query := range seed {
		if _, err := db.SQL.Exec(query); err != nil {
			t.Fatalf("准备声呐鱼口夹具失败: %v", err)
		}
	}

	grids, err := loadSonarGrids()
	if err != nil {
		t.Fatal(err)
	}
	grid := grids["loc12_215"]
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	c := fishRollContext{
		uid: 1000009, loc: "loc12_215", x: 2260, z: -460, hasPos: true, spot: 7,
		rigType: 50, lureLayer: .82, lureAction: "marine_jig", now: now,
		cache: newFishingRollCache(),
		weather: fishingWeather{
			known: true, kind: fishingWeatherClear, airTemp: 18, previousAirTemp: 17, wind: 3,
		},
	}
	schools := srv.rosterSonarSchools(t.Context(), "loc12_215", grid, nil, c, rand.New(rand.NewSource(41)))
	if len(schools) < 2 {
		t.Fatalf("真实名册应生成多个鱼群，实得 %d", len(schools))
	}
	wantLayer := map[string]float64{
		"test_shallow": .18,
		"test_mid":     .52,
		"test_bottom":  .92,
	}
	seen := map[string]bool{}
	for _, school := range schools {
		if want, ok := wantLayer[school.fishKey]; !ok {
			t.Fatalf("出现了名册外的鱼种 %q", school.fishKey)
		} else {
			seen[school.fishKey] = true
			if school.depthM <= 0 || school.depthJitterM <= 0 {
				t.Fatalf("%s 深度无效: school=%+v", school.fishKey, school)
			}
			settle := srv.cachedSettleStatics(t.Context(), c, school.fishKey)
			if school.lines.val == nil || school.lines.trophy == nil || school.lines.blue == nil ||
				*school.lines.val != settle[0] || *school.lines.trophy != settle[1] || *school.lines.blue != settle[2] {
				t.Fatalf("%s 声呐未使用结算档位线: school=%v settle=%v", school.fishKey, school.lines, settle)
			}
			// 没有 sonar_profile 时以钓点水深作水域总深；深度必须落在
			// 0..总深内，并高于水面对应层。
			if school.depthM < want*2 {
				t.Fatalf("%s 深度 %.2f 与生态水层 %.2f 不符", school.fishKey, school.depthM, want)
			}
		}
	}
	if len(seen) < 2 {
		t.Fatalf("鱼群没有按名册分散鱼种: %+v", seen)
	}
}

func TestSonarSynthesisCreatesAdjacentSchoolCells(t *testing.T) {
	obj, err := synthesizeSonar("loc12_01", &sonarProfile{
		depthMin: 1, depthMax: 8, sizeMed: 2, sizeP90: 20,
	}, rand.New(rand.NewSource(31)))
	if err != nil {
		t.Fatal(err)
	}
	grid, err := loadSonarGrids()
	if err != nil {
		t.Fatal(err)
	}
	g := grid["loc12_01"]
	cols, rows := sonarGridDimensions(g)
	cells := obj.Field(1).Val.Items
	if len(cells) != cols*rows {
		t.Fatalf("格数=%d, 期望 %d", len(cells), cols*rows)
	}
	nonEmpty := func(x, y int) bool {
		cell := cells[y*cols+x]
		return cell != nil && cell.Kind != proto.KindNull
	}
	adjacent := 0
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			if !nonEmpty(x, y) {
				continue
			}
			if x+1 < cols && nonEmpty(x+1, y) {
				adjacent++
			}
			if y+1 < rows && nonEmpty(x, y+1) {
				adjacent++
			}
		}
	}
	if adjacent < 8 {
		t.Fatalf("连续鱼群格不足: 相邻非空格对=%d", adjacent)
	}
}

func TestSonarSynthesisKeepsSchoolDepthBandWithoutProfile(t *testing.T) {
	school := sonarSchool{
		centerX: 5, centerY: 5, radiusX: 2, radiusY: 2,
		depthM: 80, depthJitterM: 1, fishKey: "test",
	}
	obj, err := synthesizeSonarSchools("loc12_215", nil, []sonarSchool{school}, rand.New(rand.NewSource(7)))
	if err != nil {
		t.Fatal(err)
	}
	cells := obj.Field(1).Val.Items
	var depths []float64
	for _, cell := range cells {
		if cell == nil || cell.Kind == proto.KindNull || cell.Field(0).Val == nil {
			continue
		}
		for _, echo := range cell.Field(0).Val.Items {
			if echo == nil || echo.Field(1).Num == nil {
				continue
			}
			depths = append(depths, float64(proto.F32FromBits(uint32(*echo.Field(1).Num))))
		}
	}
	if len(depths) < 2 {
		t.Fatalf("回声数量不足: %v", depths)
	}
	minDepth, maxDepth := depths[0], depths[0]
	for _, depth := range depths {
		if depth < minDepth {
			minDepth = depth
		}
		if depth > maxDepth {
			maxDepth = depth
		}
	}
	if maxDepth-minDepth > 2.1 {
		t.Fatalf("同一鱼群深度带不连续: min=%.2f max=%.2f depths=%v", minDepth, maxDepth, depths)
	}
}

func TestSonarHandlerReturnsEmbeddedGrid(t *testing.T) {
	srv, _ := newRigTestServer(t)
	// 默认 sonar_echo=0 只回空网格，这里显式打开
	// 回声以验证网格生成逻辑本身。
	if err := srv.DB.TunableSet(context.Background(), "sonar_echo", "1", "test"); err != nil {
		t.Fatal(err)
	}
	registerDynamicProtocolHandlers(srv.dispatch)
	body, ok := hDynamicSonar(dynamicTestCtx(srv, rigRequestTree(t, proto.StrVal("loc12_01"))))
	if !ok {
		t.Fatal("1/10 声呐未处理")
	}
	decoded, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Class != sonarResponseClass || decoded.TypeID != 15003 {
		t.Fatalf("1/10 响应=%s/%d", decoded.Class, decoded.TypeID)
	}
	f0 := decoded.Field(0)
	if got := f0.StrOr(); got != "loc12_01" {
		t.Fatalf("1/10 f0=%q", got)
	}
	cells := decoded.Field(1).Val
	if cells == nil || cells.Kind != proto.KindArgs || cells.ArgCodeStr() != sonarCellArgCode || cells.ArgCount() != 522 {
		t.Fatalf("1/10 应回 loc12_01 的 522 格: %#v", cells)
	}
	if reason := srv.dispatch.checkRegisteredResponse(1, 10, body, srv.Table()); reason != "" {
		t.Fatalf("1/10 合同守卫误拒: %s", reason)
	}
}
func TestSonarProfileLookup(t *testing.T) {
	db := connectTestDB(t)
	if _, err := db.SQL.Exec(`
		INSERT INTO sonar_profile(system_id, cells, depth_min, depth_max, size_med, size_p90)
		VALUES(?,?,?,?,?,?)`, "loc12_01", 12, 1.25, 7.75, 8.5, 21.0); err != nil {
		t.Fatal(err)
	}
	srv := &Server{DB: db}
	profile := srv.sonarProfileFor(context.Background(), "loc12_01")
	if profile == nil || *profile != (sonarProfile{1.25, 7.75, 8.5, 21.0}) {
		t.Fatalf("声呐档案=%+v", profile)
	}
	if got := srv.sonarProfileFor(context.Background(), "loc12_02"); got != nil {
		t.Fatalf("无档案水域应回 nil，实得 %+v", got)
	}
}

func TestSonarChartZoneUsesGuideZoneNumbers(t *testing.T) {
	if !sonarChartLevel("level_012_north_sea") {
		t.Fatal("挪威海应有海图水域网格")
	}
	if sonarChartLevel("level_001_lake") || sonarChartLevel("") {
		t.Fatal("内陆湖不该被当成有海图")
	}
	for _, tt := range []struct {
		name     string
		x, z     float64
		wantZone int
	}{
		{"75号区", 1020, 50, 75},
		{"120号区", 3500, 700, 120},
		{"215号区", 2300, -600, 215},
	} {
		zone, ok := sonarZoneForPoint("level_012_north_sea", tt.x, tt.z)
		if !ok || zone != tt.wantZone {
			t.Fatalf("%s: 实得 zone=%d ok=%v，期望 %d", tt.name, zone, ok, tt.wantZone)
		}
		if again, _ := sonarZoneForPoint("level_012_north_sea", tt.x, tt.z); again != zone {
			t.Fatalf("%s: 同一坐标区号不稳定 %d != %d", tt.name, zone, again)
		}
	}
	if _, ok := sonarZoneForPoint("level_012_north_sea", 9000, 9000); ok {
		t.Fatal("海图网格之外不该定位到区号")
	}
	if _, ok := sonarZoneForPoint("level_001_lake", 1020, 50); ok {
		t.Fatal("内陆湖不该定位到海图区号")
	}
	if zone, ok := sonarZoneNumber("loc12_215"); !ok || zone != 215 {
		t.Fatalf("sonarZoneNumber(loc12_215)=%d,%v", zone, ok)
	}
	for _, bad := range []string{"", "loc12_", "loc12_x", "loc12_0", "tours"} {
		if _, ok := sonarZoneNumber(bad); ok {
			t.Fatalf("sonarZoneNumber(%q) 应为不可用", bad)
		}
	}
}

// 海图区号以客户端 loc12_N 为权威：局部坐标反查失败时仍能命中（2026-11-14
// 实测：玩家在 198 区，14/2 落点报 (-100,680) 局部坐标，坐标反查永不中）。
func TestSonarZoneFromLocAuthoritative(t *testing.T) {
	if n, ok := sonarZoneFromLoc("loc12_198"); !ok || n != 198 {
		t.Fatalf("loc12_198 应解析为 198, got %d %v", n, ok)
	}
	if _, ok := sonarZoneFromLoc("loc1_02"); ok {
		t.Fatal("内陆 loc 不应解析出海图区号")
	}
	if _, ok := sonarZoneFromLoc("loc12_"); ok {
		t.Fatal("空区号不应通过")
	}
}
