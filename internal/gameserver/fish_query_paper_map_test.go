package gameserver

import (
	"math"
	"testing"
	"time"
)

// Expected intersections from the supported client's numeric calibration,
// rounded to 12 decimal places independently of the production conversion.
var fishQueryExpectedDisplayBounds = map[string]struct {
	cells  int
	bounds FishQueryRegion
}{
	"level_001_lake":            {121, FishQueryRegion{29.8, 33.4, 111.72, 115.32}},
	"level_002_kirzah_river":    {169, FishQueryRegion{45.000002043588, 53.000002406892, 142.523815996252, 150.523816359557}},
	"level_003_viiveri_lake":    {169, FishQueryRegion{52.641512275633, 52.594342461766, 149.245291075362, 149.198121261495}},
	"level_004_torfyanoe_ozero": {100, FishQueryRegion{0, 0, 80, 80}},
	"level_005_volhov":          {625, FishQueryRegion{0, 0, 200, 200}},
	"level_006_ladoga":          {169, FishQueryRegion{4.83412346309, 0, 100, 96.682469261798}},
	"level_007_grass_lake":      {144, FishQueryRegion{4.775330503819, 3.207048619535, 94.995595579068, 93.427313694784}},
	"level_008_sura_river":      {400, FishQueryRegion{0, 0, 160, 160}},
	"level_009_ahtuba":          {625, FishQueryRegion{0, 0, 200, 200}},
	"level_010_belaya":          {169, FishQueryRegion{0, 0, 100, 100}},
	"level_011_ladoga_02":       {5776, FishQueryRegion{0, 0, 600, 600}},
	"level_013_sev_don":         {625, FishQueryRegion{5.116278842825, 4.186046325948, 195.627898299288, 194.697665782411}},
	"level_014_carp_lake":       {625, FishQueryRegion{0, 17.619048419183, 193.619056411882, 212.666676324512}},
	"level_015_tunguska":        {900, FishQueryRegion{0, 0, 240, 240}},
	"level_016_yama":            {1444, FishQueryRegion{0, 0, 300, 300}},
	"level_018_moskitnoe":       {64, FishQueryRegion{23.912928678497, 23.343008472604, 77.949868536372, 77.379948330479}},
	"level_019_american_pond":   {289, FishQueryRegion{37.629179767611, 37.629179767611, 162.12766145431, 162.12766145431}},
}

func TestFishQueryPaperMapClipsBeforeRanking(t *testing.T) {
	for _, tc := range []struct {
		name        string
		scene       fishQueryRegionScene
		bounds      FishQueryRegion
		cells       int
		first, last string
	}{
		{"inside", fishQueryRegionScene{OriginX: -50, OriginZ: -50, Size: 200, Paper: &fishQueryPaperMap{OffsetX: -10, OffsetZ: -30, Scale: 2, Width: 160, Height: 200}}, FishQueryRegion{1, 3, 17, 23}, 9, "test/-2/-1", "test/0/1"},
		{"extends past terrain", fishQueryRegionScene{OriginX: -50, OriginZ: -50, Size: 200, Paper: &fishQueryPaperMap{OffsetX: 20, OffsetZ: 10, Scale: 1, Width: 250, Height: 250}}, FishQueryRegion{0, 0, 40, 40}, 36, "test/-2/-2", "test/3/3"},
		{"aligned edges", fishQueryRegionScene{OriginX: -40, OriginZ: -40, Size: 120, Paper: &fishQueryPaperMap{OffsetX: 0, OffsetZ: 0, Scale: 1, Width: 80, Height: 80}}, FishQueryRegion{0, 0, 16, 16}, 4, "test/-1/-1", "test/0/0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := time.Unix(1794096000, 0)
			g, err := newFishQueryRegions("test", tc.scene, at, true, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(g.areas) != tc.cells || g.scanned != tc.cells || g.areas[0] != tc.first || g.areas[tc.cells-1] != tc.last {
				t.Fatalf("wrong world grid after clipping: %+v", g)
			}
			first, last := g.bounds[0], g.bounds[tc.cells-1]
			if first.MinX != tc.bounds.MinX || first.MinZ != tc.bounds.MinZ || last.MaxX != tc.bounds.MaxX || last.MaxZ != tc.bounds.MaxZ {
				t.Fatalf("changed display origin or unclipped edges: first=%+v last=%+v", first, last)
			}
			fullScene := tc.scene
			fullScene.Paper = nil
			full, err := newFishQueryRegions("test", fullScene, at, true, nil)
			if err != nil {
				t.Fatal(err)
			}
			fullFactors, err := full.distributionFactors(t.Context(), "target")
			if err != nil {
				t.Fatal(err)
			}
			byArea := map[string]float64{}
			for i, area := range full.areas {
				byArea[area] = fullFactors[i]
			}
			if err := g.shortlist(t.Context(), "target", map[string]float64{"target": 1, "other": 2}); err != nil {
				t.Fatal(err)
			}
			for i, area := range g.areas {
				if factor, ok := byArea[area]; !ok || factor != g.factors["target"][i] {
					t.Fatalf("clipping changed production fish distribution for %s", area)
				}
				r := g.bounds[i]
				if r.MinX < tc.bounds.MinX || r.MinZ < tc.bounds.MinZ || r.MaxX > tc.bounds.MaxX || r.MaxZ > tc.bounds.MaxZ {
					t.Fatalf("shortlist restored outside area: %+v", r)
				}
			}
		})
	}
}

func TestFishQueryPaperMapInvalidBounds(t *testing.T) {
	for _, paper := range []fishQueryPaperMap{
		{OffsetX: 0, OffsetZ: 0, Scale: 0, Width: 100, Height: 100}, {OffsetX: 0, OffsetZ: 0, Scale: -1, Width: 100, Height: 100},
		{OffsetX: 0, OffsetZ: 0, Scale: float32(math.NaN()), Width: 100, Height: 100}, {OffsetX: float32(math.Inf(1)), OffsetZ: 0, Scale: 1, Width: 100, Height: 100},
		{OffsetX: 0, OffsetZ: float32(math.NaN()), Scale: 1, Width: 100, Height: 100}, {OffsetX: 0, OffsetZ: 0, Scale: 1, Width: 0, Height: 100},
		{OffsetX: 0, OffsetZ: 0, Scale: 1, Width: 100, Height: -1}, {OffsetX: 0, OffsetZ: 0, Scale: 1, Width: math.Inf(1), Height: 100}, {OffsetX: 0, OffsetZ: 0, Scale: 1, Width: 100, Height: math.NaN()},
		{OffsetX: -200, OffsetZ: 0, Scale: 1, Width: 80, Height: 80}, // only touches terrain at its outer edge
		{OffsetX: 0, OffsetZ: 90, Scale: 1, Width: 80, Height: 80},   // no overlap on Z
	} {
		for _, enabled := range []bool{false, true} {
			scene := fishQueryRegionScene{OriginX: -50, OriginZ: -50, Size: 200, Paper: &paper}
			if _, err := newFishQueryRegions("test", scene, time.Time{}, enabled, nil); err == nil {
				t.Fatalf("accepted invalid paper map %+v, enabled=%v", paper, enabled)
			}
		}
	}
}
