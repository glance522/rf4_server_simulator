package gameserver

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func waterGridForTest(g *fishQueryRegions) *fishQueryWaterGrid {
	return &fishQueryWaterGrid{Bounds: append([]float64(nil), g.worldBounds[:]...), MinX: g.minCellX, MinZ: g.minCellZ,
		Width: g.width, Height: g.height, Samples: make([]int, len(g.areas))}
}

func TestFishQueryWaterParseAndRead(t *testing.T) {
	g, err := newFishQueryRegions("test", fishQueryRegionScene{OriginX: -50, OriginZ: -20, Size: 80}, time.Time{}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	makeTable := func() fishQueryWaterTable {
		return fishQueryWaterTable{SchemaVersion: 1, CellSize: 40, Sampling: "center_corners_any",
			Maps: map[string]*fishQueryWaterGrid{"test": waterGridForTest(g)}}
	}
	for name, mutate := range map[string]func(*fishQueryWaterTable){
		"valid":        func(t *fishQueryWaterTable) { t.Maps["test"].Samples[0] = 31 },
		"version":      func(t *fishQueryWaterTable) { t.SchemaVersion++ },
		"cell_size":    func(t *fishQueryWaterTable) { t.CellSize = 20 },
		"sampling":     func(t *fishQueryWaterTable) { t.Sampling = "all" },
		"empty":        func(t *fishQueryWaterTable) { t.Maps = nil },
		"null_grid":    func(t *fishQueryWaterTable) { t.Maps["test"] = nil },
		"bounds_size":  func(t *fishQueryWaterTable) { t.Maps["test"].Bounds = nil },
		"bounds_order": func(t *fishQueryWaterTable) { t.Maps["test"].Bounds[2] = -100 },
		"bounds_grid":  func(t *fishQueryWaterTable) { t.Maps["test"].Bounds[0] -= 40 },
		"min_x":        func(t *fishQueryWaterTable) { t.Maps["test"].MinX++ },
		"width":        func(t *fishQueryWaterTable) { t.Maps["test"].Width = 77 },
		"height":       func(t *fishQueryWaterTable) { t.Maps["test"].Height = 0 },
		"length":       func(t *fishQueryWaterTable) { t.Maps["test"].Samples = nil },
		"negative_bit": func(t *fishQueryWaterTable) { t.Maps["test"].Samples[0] = -1 },
		"extra_bit":    func(t *fishQueryWaterTable) { t.Maps["test"].Samples[0] = 32 },
	} {
		t.Run(name, func(t *testing.T) {
			table := makeTable()
			mutate(&table)
			data, err := json.Marshal(table)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := parseFishQueryWater(data)
			if name == "valid" {
				if err != nil || !reflect.DeepEqual(parsed, &table) {
					t.Fatalf("round trip: %+v %v", parsed, err)
				}
			} else if !errors.Is(err, errFishQueryWaterInvalid) {
				t.Fatalf("invalid table accepted: %+v %v", parsed, err)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "water.json")
	if _, err := readFishQueryWater(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
	valid, _ := json.Marshal(makeTable())
	for _, data := range [][]byte{valid, []byte("{bad"), []byte("null"), []byte(strings.Repeat(" ", (2<<20)+1))} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		_, err := readFishQueryWater(path)
		if string(data) == string(valid) {
			if err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(err, errFishQueryWaterInvalid) {
			t.Fatalf("invalid file accepted: %v", err)
		}
	}
}

func TestFishQueryWaterAnyPointKeepsOriginalArea(t *testing.T) {
	scene := fishQueryRegionScene{OriginX: -50, OriginZ: -20, Size: 80, Paper: &fishQueryPaperMap{OffsetX: -3, OffsetZ: 2, Scale: 1, Width: 75, Height: 80}}
	g, err := newFishQueryRegions("test", scene, time.Time{}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	areas, bounds := slices.Clone(g.areas), slices.Clone(g.bounds)
	grid := waterGridForTest(g)
	copy(grid.Samples, []int{0, 1, 2, 4, 8, 16})
	if err := g.filterWater(grid); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(g.areas, areas[1:6]) || !slices.Equal(g.bounds, bounds[1:6]) || g.scanned != 5 || !g.waterFiltered {
		t.Fatalf("five-point rule shifted or lost cells: %+v", g)
	}
	if err := g.shortlist(t.Context(), "target", map[string]float64{"target": 3, "other": 8}); err != nil {
		t.Fatal(err)
	}
	q := &fishQueryCapture{fish: "target", biteProbability: .6, acceptance: .5, gradeShare: 1,
		regionalWeights: []fishQueryRegionalWeight{{"target", 3, .5}, {"other", 8, .8}}}
	r, err := g.best(t.Context(), q)
	if err != nil || r == nil {
		t.Fatalf("no best wet cell: %v %v", r, err)
	}
	index := slices.Index(g.bounds, *r)
	if index < 0 || g.areas[index] == areas[0] {
		t.Fatalf("land cell returned: %+v", r)
	}
	// The mask removes choices, not probability mass inside a retained cell.
	target := 3 * distributionMultiplier(g.areas[index], "target", g.now)
	other := 8 * distributionMultiplier(g.areas[index], "other", g.now)
	want := .6 * target / (target + other) * .5
	if math.Abs(q.chance-want) > 1e-12 {
		t.Fatalf("retained-cell probability changed: %v want %v", q.chance, want)
	}
}

func TestFishQueryWaterMismatchEmptyAndDisabled(t *testing.T) {
	newGrid := func(enabled bool) *fishQueryRegions {
		g, err := newFishQueryRegions("test", fishQueryRegionScene{Size: 80}, time.Time{}, enabled, nil)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	for _, mutate := range []func(*fishQueryWaterGrid){
		func(g *fishQueryWaterGrid) { g.MinZ++ },
		func(g *fishQueryWaterGrid) { g.Bounds[2]-- },
		func(g *fishQueryWaterGrid) { g.Bounds[0] = math.NaN() },
	} {
		g := newGrid(true)
		grid := waterGridForTest(g)
		areas, bounds := slices.Clone(g.areas), slices.Clone(g.bounds)
		mutate(grid)
		if err := g.filterWater(grid); !errors.Is(err, errFishQueryWaterInvalid) {
			t.Fatalf("mismatch accepted: %v", err)
		}
		if !slices.Equal(areas, g.areas) || !slices.Equal(bounds, g.bounds) || g.waterFiltered {
			t.Fatal("mismatch partially mutated regions")
		}
	}
	g := newGrid(true)
	if err := g.filterWater(nil); err != nil || g.waterFiltered || g.scanned != 4 {
		t.Fatal("missing map changed query")
	}
	grid := waterGridForTest(g)
	if _, err := g.distributionFactors(t.Context(), "target"); err != nil {
		t.Fatal(err)
	}
	if err := g.filterWater(grid); !errors.Is(err, errFishQueryWaterInvalid) {
		t.Fatal("filtering after scoring accepted")
	}
	g = newGrid(true)
	if err := g.filterWater(waterGridForTest(g)); err != nil {
		t.Fatal(err)
	}
	if err := g.shortlist(t.Context(), "target", map[string]float64{"target": 1}); err != nil {
		t.Fatal(err)
	}
	q := &fishQueryCapture{fish: "target", biteProbability: 1, acceptance: 1, gradeShare: 1,
		regionalWeights: []fishQueryRegionalWeight{{"target", 1, 1}}}
	if r, err := g.best(t.Context(), q); err != nil || r != nil || q.chance != 0 || g.scanned != 0 || !g.waterFiltered {
		t.Fatalf("all-land grid invented a result: %v %v %+v", r, err, q)
	}
	g = newGrid(false)
	if err := g.filterWater(grid); err != nil || g.waterFiltered || g.scanned != 0 {
		t.Fatal("disabled distribution attempted water filtering")
	}
}

func TestFishQueryWaterIntegration(t *testing.T) {
	// The table is process-cached. Use an isolated test process to exercise the
	// real loader without changing the singleton underneath unrelated tests.
	mode := os.Getenv("RF4_TEST_WATER_CHILD")
	if mode == "" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		for _, mode := range []string{"wet", "dry", "missing_map", "missing_file"} {
			t.Run(mode, func(t *testing.T) {
				cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestFishQueryWaterIntegration$", "-test.timeout=30s")
				cmd.Env = append(os.Environ(), "RF4_TEST_WATER_CHILD="+mode)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("integration: %v\n%s", err, output)
				}
			})
		}
		return
	}
	s, db := seedBaitWeightTest(t)
	const uid int64 = 1000001
	const level = "level_015_tunguska"
	if _, err := db.EnsureProfile(t.Context(), uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE loc_alias SET alias=? WHERE alias='level_900_rules'`, level); err != nil {
		t.Fatal(err)
	}
	if err := db.TunableSet(t.Context(), "fish_distribution_enabled", "1", "test"); err != nil {
		t.Fatal(err)
	}
	s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
	g, err := newFishQueryRegions("rules-test", fishQueryRegionScenes[level], time.Time{}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	grid := waterGridForTest(g)
	// Choose a cell containing the fixture fish in both adjacent refreshes.
	// The water-loader test must not depend on the real-world fish-school hour.
	index := -1
	now := time.Now()
	for i, area := range g.areas {
		if distributionMultiplier(area, "c.roach", now) > 0 &&
			distributionMultiplier(area, "c.roach", now.Add(10*time.Minute)) > 0 {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("fixture has no populated cell across the refresh boundary")
	}
	if mode != "dry" {
		grid.Samples[index] = 16 // A corner alone is sufficient.
	}
	key := level
	if mode == "missing_map" {
		key = "another_map"
	}
	table := fishQueryWaterTable{SchemaVersion: 1, CellSize: 40, Sampling: "center_corners_any", Maps: map[string]*fishQueryWaterGrid{key: grid}}
	data, err := json.Marshal(table)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "water.json")
	t.Setenv("RF4_FISH_QUERY_WATER_FILE", path)
	if mode != "missing_file" {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := s.QueryFish(t.Context(), FishQueryRequest{UID: uid, Fish: "c.roach", Map: level, Bait: "rule_worm", Grade: 1})
	if mode == "missing_file" {
		if !errors.Is(err, errFishQueryWaterInvalid) {
			t.Fatalf("explicit missing data silently ignored: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if mode == "dry" {
		if !result.WaterFiltered || result.Regions != 0 || len(result.Plans) != 0 {
			t.Fatalf("all-land results: %+v", result)
		}
		return
	}
	if len(result.Plans) == 0 {
		t.Fatal("no query plans")
	}
	reply := formatFishQueryReply("常见拟鲤", result)
	if mode == "missing_map" {
		if result.WaterFiltered || result.Regions != len(g.areas) || !strings.Contains(strings.Join(result.Notes, "\n"), "缺少本图水陆数据") {
			t.Fatalf("missing map misrepresented: %+v %s", result, reply)
		}
		return
	}
	if !result.WaterFiltered || result.Regions != 1 || result.CandidateRegions != 1 || !strings.Contains(strings.Join(result.Notes, "\n"), "五点做水陆粗筛") {
		t.Fatalf("water filtering not before shortlist: %+v %s", result, reply)
	}
	for _, p := range result.Plans {
		if p.Region == nil || *p.Region != g.bounds[index] {
			t.Fatalf("query escaped the only retained cell: %+v", p)
		}
	}
}
