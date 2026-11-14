package gameserver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
	"time"
)

func TestFishQueryRegionRankingUsesCompetition(t *testing.T) {
	g, err := newFishQueryRegions("test", fishQueryRegionScene{Size: 80}, time.Unix(1794096000, 0), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The target is four times as abundant in the first cell, but competing
	// species make its actual selection probability much worse there.
	g.factors["target"] = []float64{4, 1, 0, 0}
	g.factors["other"] = []float64{40, .1, 0, 0}
	q := &fishQueryCapture{fish: "target", biteProbability: .6, acceptance: .5, gradeShare: .2,
		regionalWeights: []fishQueryRegionalWeight{{"target", 1, .5}, {"other", 1, .8}}}
	region, err := g.best(t.Context(), q)
	if err != nil || region == nil || region.MinX != 0 || region.MinZ != 8 {
		t.Fatalf("region=%+v err=%v", region, err)
	}
	if math.Abs(q.selection-1/1.1) > 1e-12 || math.Abs(q.chance-.6/1.1*.5*.2) > 1e-12 || math.Abs(q.fishShare-.5/.58) > 1e-12 {
		t.Fatalf("wrong normalized result: %+v", q)
	}
}

func TestFishQueryRegionCaptureMatchesPointCalculation(t *testing.T) {
	s, db := seedBaitWeightTest(t)
	for key, value := range map[string]string{"fish_distribution_enabled": "1", "fish_bait_rules_enabled": "1", "fish_weather_enabled": "1", "fish_ecology_enabled": "0"} {
		if err := db.TunableSet(t.Context(), key, value, "test"); err != nil {
			t.Fatal(err)
		}
	}
	s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
	bonus := neutralFishingSkillBonus()
	bonus.TrophyLuck = .3
	c := fishRollContext{loc: "level_900_rules", now: time.Unix(1794096000, 0), cache: newFishingRollCache(), forRig: true, rigType: 1, depthCM: 150,
		weather: fishingWeather{known: true, airTemp: 18, previousAirTemp: 18, wind: 2}, skill: bonus}
	q := &fishQueryCapture{fish: "c.roach", grade: 2, regional: true}
	c.query = q
	s.rollFishWithRules(c.loc, "rule_worm", .8, bonus, c)
	scene := fishQueryRegionScene{OriginX: -120.5, OriginZ: -200.25, Size: 400,
		Paper: &fishQueryPaperMap{OffsetX: -25, OffsetZ: -40, Scale: 2, Width: 300, Height: 400}}
	g, err := newFishQueryRegions("rules-test", scene, c.now, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	region, err := g.best(t.Context(), q)
	if err != nil || region == nil || q.chance <= 0 {
		t.Fatalf("regional query=%+v region=%+v err=%v", q, region, err)
	}
	var want fishQueryCapture
	var wantRegion FishQueryRegion
	for _, area := range g.bounds {
		point := c
		point.x, point.z, point.hasPos = (area.MinX+area.MaxX)*2.5+scene.OriginX, (area.MinZ+area.MaxZ)*2.5+scene.OriginZ, true
		point.query = &fishQueryCapture{fish: q.fish, grade: q.grade}
		s.rollFishWithRules(point.loc, "rule_worm", .8, bonus, point)
		if point.query.chance > want.chance {
			want, wantRegion = *point.query, area
		}
	}
	if *region != wantRegion || math.Abs(q.chance-want.chance) > 1e-12 || math.Abs(q.selection-want.selection) > 1e-12 || math.Abs(q.fishShare-want.fishShare) > 1e-12 {
		t.Fatalf("batch differs from point-by-point production weights: got=%+v %v want=%+v %v", q, region, want, wantRegion)
	}
}

func TestFishQueryRegionsReadOnlyAndNoPointEcology(t *testing.T) {
	t.Setenv("GODEBUG", "randseednop=0")
	s, db := seedBaitWeightTest(t)
	const uid int64 = 1000001
	const mapKey = "level_015_tunguska"
	if _, err := db.EnsureProfile(t.Context(), uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE loc_alias SET alias=? WHERE alias='level_900_rules'`, mapKey); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"fish_distribution_enabled": "1", "fish_ecology_enabled": "1"} {
		if err := db.TunableSet(t.Context(), key, value, "test"); err != nil {
			t.Fatal(err)
		}
	}
	s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
	c := fishRollContext{loc: mapKey, rigType: 1, x: 425, z: 520, hasPos: true}
	s.prepareFishRollRuntime(t.Context(), &c, "rules-test")
	if !c.spotEnv.Known {
		t.Fatal("production no longer computes point ecology")
	}
	c.query = &fishQueryCapture{regional: true}
	s.prepareFishRollRuntime(t.Context(), &c, "rules-test")
	if c.spotEnv != (fishSpotEnvironment{}) {
		t.Fatal("region query retained point-specific ecology")
	}
	db.SQL.SetMaxOpenConns(1)
	if _, err := db.SQL.Exec(`PRAGMA query_only=ON`); err != nil {
		t.Fatal(err)
	}
	rand.Seed(123456)
	wantNext := rand.Int63()
	rand.Seed(123456)
	result, err := s.QueryFish(t.Context(), FishQueryRequest{UID: uid, Fish: "c.roach", Map: mapKey, Grade: 3})
	if err != nil || len(result.Plans) == 0 || result.Regions != 900 {
		t.Fatalf("query=%+v err=%v", result, err)
	}
	if rand.Int63() != wantNext {
		t.Fatal("region query consumed production RNG")
	}
	for _, plan := range result.Plans {
		if plan.CoordinateKind != "inland_region" || plan.Region == nil || plan.X != nil || plan.Z != nil || plan.Chance <= 0 || plan.Chance > 1 || len(plan.References) != 0 {
			t.Fatalf("invalid region plan: %+v", plan)
		}
	}
	if result.Evaluated > result.BaitGroups*3*32 {
		t.Fatal("fine evaluation still scans the whole map instead of a coarse region shortlist")
	}
	if result.CandidateRegions != fishQueryCoarseRegionLimit {
		t.Fatalf("fine region count=%d", result.CandidateRegions)
	}
	for i := 1; i < len(result.Plans); i++ {
		if result.Plans[i].TheoreticalPerHour > result.Plans[i-1].TheoreticalPerHour+1e-12 {
			t.Fatal("plans not ordered by final direct probability")
		}
	}
	before := s.tunableF("fish_ecology_enabled", 0)
	if before != 1 {
		t.Fatal("query changed production ecology tunable")
	}
}

func TestFishQueryUnsupportedMapDoesNotReturnReferences(t *testing.T) {
	s, db := seedBaitWeightTest(t)
	const uid int64 = 1000001
	const mapKey = "level_999_unverified"
	if _, err := db.SQL.Exec(`UPDATE loc_alias SET alias=? WHERE alias='level_900_rules'`, mapKey); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnsureProfile(t.Context(), uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO spot_fish(loc,spot,fish_key,catches) VALUES('rules-test','67:57','c.roach',5)`); err != nil {
		t.Fatal(err)
	}
	// Unsupported must be explicit even if no rod is held with caps enabled,
	// the target is not in the map roster, or a historical reference exists.
	for _, caps := range []string{"0", "1"} {
		if err := db.TunableSet(t.Context(), "fish_weight_cap", caps, "test"); err != nil {
			t.Fatal(err)
		}
		s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
		for _, fish := range []string{"c.roach", "absent-fish"} {
			if result, err := s.QueryFish(t.Context(), FishQueryRequest{UID: uid, Fish: fish, Map: mapKey}); !errors.Is(err, errFishQueryRegionMapUnsupported) || result != nil {
				t.Fatalf("unsupported map result=%+v error=%v", result, err)
			}
		}
		query := "常见拟鲤 " + mapKey
		reply := s.fishQueryReply(t.Context(), &Session{Srv: s, Userid: uid}, query)
		if !strings.Contains(reply, "暂不支持该地图") || strings.Contains(reply, "参考") || strings.Contains(reply, "67:57") {
			t.Fatalf("query=%s caps=%s unsupported reply=%s", query, caps, reply)
		}
	}
}

func TestFishQueryRegionsPreserveSeaGridQuery(t *testing.T) {
	s, db := seedBaitWeightTest(t)
	const uid int64 = 1000001
	const mapKey = "level_012_north_sea"
	if _, err := db.EnsureProfile(t.Context(), uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE loc_alias SET alias=? WHERE alias='level_900_rules'`, mapKey); err != nil {
		t.Fatal(err)
	}
	for _, distribution := range []string{"0", "1"} {
		if err := db.TunableSet(t.Context(), "fish_distribution_enabled", distribution, "test"); err != nil {
			t.Fatal(err)
		}
		s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
		result, err := s.QueryFish(t.Context(), FishQueryRequest{UID: uid, Fish: "c.roach", Map: mapKey})
		if err != nil || len(result.Plans) == 0 || result.Regions != 0 || result.CandidateRegions != 0 {
			t.Fatalf("sea query=%+v err=%v", result, err)
		}
		for _, plan := range result.Plans {
			if plan.CoordinateKind != "sea_grid" || plan.Region != nil || plan.X == nil || plan.Z == nil || plan.Zone <= 0 || !plan.OnBoat {
				t.Fatalf("sea plan replaced by inland region: %+v", plan)
			}
		}
	}
}

func TestFishQueryRegionBoundsAndCancellation(t *testing.T) {
	for key, scene := range fishQueryRegionScenes {
		g, err := newFishQueryRegions(key, scene, time.Unix(1794096000, 0), true, nil)
		want := fishQueryExpectedDisplayBounds[key]
		if err != nil || len(g.areas) != want.cells {
			t.Fatalf("%s: grid=%+v err=%v", key, g, err)
		}
		last := g.bounds[len(g.bounds)-1]
		if math.Abs(last.MaxX-want.bounds.MaxX) > 1e-9 || math.Abs(last.MaxZ-want.bounds.MaxZ) > 1e-9 {
			t.Fatalf("%s: edge area escaped visible extent: %+v", key, last)
		}
	}
	for _, invalid := range []float64{0, -1, math.NaN(), math.Inf(1), 1e7} {
		if _, err := newFishQueryRegions("test", fishQueryRegionScene{Size: invalid}, time.Time{}, true, nil); err == nil {
			t.Fatalf("accepted invalid extent %g", invalid)
		}
	}
	g, _ := newFishQueryRegions("test", fishQueryRegionScene{Size: 80}, time.Now(), false, nil)
	q := &fishQueryCapture{chance: .2}
	if region, err := g.best(t.Context(), q); err != nil || region != nil || q.chance != .2 {
		t.Fatal("disabled distribution invented a winning region or changed chance")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := g.best(cancelled, q); !errors.Is(err, context.Canceled) {
		t.Fatalf("ignored cancellation: %v", err)
	}
	g, _ = newFishQueryRegions("test", fishQueryRegionScene{Size: 80}, time.Now(), true, nil)
	if region, err := g.best(t.Context(), &fishQueryCapture{}); err != nil || region != nil {
		t.Fatal("empty capture generated a region")
	}
}

func TestFishQueryVerifiedSceneCoverage(t *testing.T) {
	// Explicit extents keep a newly enabled map from silently using another
	// scene's size. Only horizontal bounds participate in this query.
	wantSizes := map[string]float64{
		"level_001_lake":            600,
		"level_002_kirzah_river":    1000,
		"level_003_viiveri_lake":    1000,
		"level_004_torfyanoe_ozero": 400,
		"level_005_volhov":          1000,
		"level_006_ladoga":          500,
		"level_007_grass_lake":      500,
		"level_008_sura_river":      800,
		"level_009_ahtuba":          1000,
		"level_010_belaya":          500,
		"level_011_ladoga_02":       3000,
		"level_013_sev_don":         1000,
		"level_014_carp_lake":       1200,
		"level_015_tunguska":        1200,
		"level_016_yama":            1500,
		"level_018_moskitnoe":       512,
		"level_019_american_pond":   1000,
	}
	if len(fishQueryRegionScenes) != len(wantSizes) {
		t.Fatalf("supported scenes=%d, want %d", len(fishQueryRegionScenes), len(wantSizes))
	}
	for key, size := range wantSizes {
		t.Run(key, func(t *testing.T) {
			scene := fishQueryRegionScenes[key]
			if got := scene.Size; got != size {
				t.Fatalf("extent=%g, want %g", got, size)
			}
			want := fishQueryExpectedDisplayBounds[key].bounds
			if scene.Paper == nil {
				t.Fatal("supported map lacks paper-map calibration")
			}
			g, err := newFishQueryRegions(key, scene, time.Time{}, true, nil)
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			var covered float64
			for i, r := range g.bounds {
				if r.MinX < 0 || r.MinZ < 0 || r.MaxX > size/5 || r.MaxZ > size/5 ||
					r.MinX >= r.MaxX || r.MinZ >= r.MaxZ || r.MaxX-r.MinX > 8+1e-12 || r.MaxZ-r.MinZ > 8+1e-12 {
					t.Fatalf("invalid or unclipped region: %+v", r)
				}
				if r.MinX < want.MinX-1e-9 || r.MinZ < want.MinZ-1e-9 || r.MaxX > want.MaxX+1e-9 || r.MaxZ > want.MaxZ+1e-9 {
					t.Fatalf("region outside paper-map intersection: %+v, want %+v", r, want)
				}
				if seen[g.areas[i]] {
					t.Fatalf("repeated production area %s", g.areas[i])
				}
				seen[g.areas[i]] = true
				// Compare both corners inside the half-open cell with the
				// production partition, including truncated outer cells.
				for _, p := range [][2]float64{{r.MinX*5 + scene.OriginX + 1e-6, r.MinZ*5 + scene.OriginZ + 1e-6}, {r.MaxX*5 + scene.OriginX - 1e-6, r.MaxZ*5 + scene.OriginZ - 1e-6}} {
					if area := fishArea(key, fishRollContext{x: p[0], z: p[1], hasPos: true}); area != g.areas[i] {
						t.Fatalf("display region %+v maps to %s, want %s", r, area, g.areas[i])
					}
				}
				covered += (r.MaxX - r.MinX) * (r.MaxZ - r.MinZ)
			}
			wantArea := (want.MaxX - want.MinX) * (want.MaxZ - want.MinZ)
			if math.Abs(covered-wantArea) > 1e-7 {
				t.Fatalf("covered display area=%g, want %g", covered, wantArea)
			}
		})
	}
}

func TestFishQueryVerifiedMapQueries(t *testing.T) {
	for mapKey, count := range map[string]int{
		"level_001_lake":          121,
		"level_002_kirzah_river":  169,
		"level_003_viiveri_lake":  169,
		"level_006_ladoga":        169,
		"level_008_sura_river":    400,
		"level_011_ladoga_02":     5776,
		"level_018_moskitnoe":     64,
		"level_019_american_pond": 289,
	} {
		t.Run(mapKey, func(t *testing.T) {
			s, db := seedBaitWeightTest(t)
			const uid int64 = 1000001
			if _, err := db.EnsureProfile(t.Context(), uid); err != nil {
				t.Fatal(err)
			}
			if _, err := db.SQL.Exec(`UPDATE loc_alias SET alias=? WHERE alias='level_900_rules'`, mapKey); err != nil {
				t.Fatal(err)
			}
			if err := db.TunableSet(t.Context(), "fish_distribution_enabled", "1", "test"); err != nil {
				t.Fatal(err)
			}
			s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
			result, err := s.QueryFish(t.Context(), FishQueryRequest{UID: uid, Fish: "c.roach", Map: mapKey})
			if err != nil || result == nil || len(result.Plans) == 0 || result.Regions != count {
				t.Fatalf("query=%+v err=%v", result, err)
			}
			for _, plan := range result.Plans {
				if plan.Region == nil || plan.CoordinateKind != "inland_region" || len(plan.References) != 0 {
					t.Fatalf("did not use regional query: %+v", plan)
				}
			}
			if mapKey == "level_011_ladoga_02" {
				if err := db.SetPlayerLocation(t.Context(), uid, mapKey, mapKey, 11); err != nil {
					t.Fatal(err)
				}
				for _, query := range []string{"常见拟鲤 " + mapKey, "常见拟鲤"} {
					reply := s.fishQueryReply(t.Context(), &Session{Srv: s, Userid: uid}, query)
					if strings.Contains(reply, "不支持") || !strings.Contains(reply, "区域 ") {
						t.Fatalf("islands query=%s reply=%s", query, reply)
					}
				}
			}
		})
	}
}

func TestFishQueryOffsetGridAlignment(t *testing.T) {
	scene := fishQueryRegionScene{OriginX: -500, OriginZ: -500, Size: 1000}
	g, err := newFishQueryRegions("test", scene, time.Time{}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.areas) != 26*26 || g.areas[0] != "test/-13/-13" || g.bounds[0] != (FishQueryRegion{MaxX: 4, MaxZ: 4}) {
		t.Fatalf("first cell is not clipped to the world grid: %s %+v", g.areas[0], g.bounds[0])
	}
	last := len(g.areas) - 1
	if g.areas[last] != "test/12/12" || g.bounds[last] != (FishQueryRegion{MinX: 196, MinZ: 196, MaxX: 200, MaxZ: 200}) {
		t.Fatalf("last cell is not clipped to the world grid: %s %+v", g.areas[last], g.bounds[last])
	}
	for _, invalid := range []fishQueryRegionScene{
		{OriginX: math.NaN(), Size: 40}, {OriginZ: math.Inf(1), Size: 40},
		{OriginX: 1e7, Size: 40}, {OriginX: 1e7, Size: 1e-12},
		{OriginX: 1, Size: 3040}, // shifted 76-cell extent intersects 77 world cells
	} {
		for _, enabled := range []bool{false, true} {
			if _, err := newFishQueryRegions("test", invalid, time.Time{}, enabled, nil); err == nil {
				t.Fatalf("accepted invalid scene %+v", invalid)
			}
		}
	}
}

func TestFishQueryCopperLakeClippedEdges(t *testing.T) {
	g, err := newFishQueryRegions("copper", fishQueryRegionScenes["level_018_moskitnoe"], time.Time{}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	first := g.bounds[0]
	want := fishQueryExpectedDisplayBounds["level_018_moskitnoe"].bounds
	if len(g.areas) != 64 || g.areas[0] != "copper/-1/-1" || first.MaxX != 25.6 || first.MaxZ != 25.6 ||
		math.Abs(first.MinX-want.MinX) > 1e-9 || math.Abs(first.MinZ-want.MinZ) > 1e-9 {
		t.Fatalf("first cell is not clipped to paper map: %s %+v", g.areas[0], first)
	}
	last := len(g.areas) - 1
	r := g.bounds[last]
	if g.areas[last] != "copper/6/6" || r.MinX != 73.6 || r.MinZ != 73.6 ||
		math.Abs(r.MaxX-want.MaxX) > 1e-9 || math.Abs(r.MaxZ-want.MaxZ) > 1e-9 {
		t.Fatalf("last cell escapes paper map: %s %+v", g.areas[last], r)
	}
}

func TestFishQueryCopperPaperMapExcludesOutsideAreas(t *testing.T) {
	g, err := newFishQueryRegions("copper", fishQueryRegionScenes["level_018_moskitnoe"], time.Time{}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Current paper-map calibration: pixel = scene-relative world * scale + offset.
	scale := float64(float32(7.58)) * 5
	minX, minZ := -float64(float32(-906.3))/scale, -float64(float32(-884.7))/scale
	maxX, maxZ := (2048-float64(float32(-906.3)))/scale, (2048-float64(float32(-884.7)))/scale
	for i, r := range g.bounds {
		if r.MinX < minX-1e-12 || r.MinZ < minZ-1e-12 || r.MaxX > maxX+1e-12 || r.MaxZ > maxZ+1e-12 {
			t.Fatalf("region outside paper map: %s %+v", g.areas[i], r)
		}
		if g.areas[i] == "copper/-1/-3" {
			t.Fatal("previous off-map recommendation remains searchable")
		}
	}
	if len(g.areas) != 64 {
		t.Fatalf("searchable cells=%d, want 64", len(g.areas))
	}
}

func TestFishQueryUnverifiedScenesRemainUnsupported(t *testing.T) {
	s, db := seedBaitWeightTest(t)
	const uid int64 = 1000001
	if _, err := db.EnsureProfile(t.Context(), uid); err != nil {
		t.Fatal(err)
	}
	previous := "level_900_rules"
	for _, key := range []string{"level_999_unverified"} {
		if _, exists := fishQueryRegionScenes[key]; exists {
			t.Fatalf("unverified scene %s enabled", key)
		}
		if _, err := db.SQL.Exec(`UPDATE loc_alias SET alias=? WHERE alias=?`, key, previous); err != nil {
			t.Fatal(err)
		}
		previous = key
		if result, err := s.QueryFish(t.Context(), FishQueryRequest{UID: uid, Fish: "c.roach", Map: key}); result != nil || !errors.Is(err, errFishQueryRegionMapUnsupported) {
			t.Fatalf("%s: result=%+v err=%v", key, result, err)
		}
	}
}

func TestFishQueryLadogaIslandsCoordinates(t *testing.T) {
	const mapKey = "level_011_ladoga_02"
	scene, ok := fishQueryRegionScenes[mapKey]
	if !ok || scene.OriginX != -500 || scene.OriginZ != -500 || scene.Size != 3000 {
		t.Fatalf("islands must use the outer main terrain: %+v, supported=%v", scene, ok)
	}
	g, err := newFishQueryRegions(mapKey, scene, time.Time{}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.areas) != 76*76 || g.scanned != 76*76 {
		t.Fatalf("outer terrain was truncated or inner terrain counted twice: %d", len(g.areas))
	}
	if g.areas[0] != mapKey+"/-13/-13" || g.bounds[0] != (FishQueryRegion{MaxX: 4, MaxZ: 4}) {
		t.Fatalf("incorrect first area: %s %+v", g.areas[0], g.bounds[0])
	}
	last := len(g.areas) - 1
	if g.areas[last] != mapKey+"/62/62" || g.bounds[last] != (FishQueryRegion{MinX: 596, MinZ: 596, MaxX: 600, MaxZ: 600}) {
		t.Fatalf("incorrect last area: %s %+v", g.areas[last], g.bounds[last])
	}
	area := fishArea(mapKey, fishRollContext{x: 120, z: 2400, hasPos: true})
	for i, key := range g.areas {
		if key != area {
			continue
		}
		if got := formatFishQueryRegion(g.bounds[i]); got != "区域 124:580～132:588（上限不含）" {
			t.Fatalf("wrong display origin: %s", got)
		}
		return
	}
	t.Fatalf("missing outer terrain area %s", area)
}

func TestFishQueryReplyRegionRange(t *testing.T) {
	result := &FishQueryResponse{Plans: []FishQueryPlan{{MapName: "下通古斯卡河", BaitName: "蚯蚓", RigName: rigNameForType(1), Chance: .183, TheoreticalPerHour: 65.88,
		RigType: 1, CoordinateKind: "inland_region", Region: &FishQueryRegion{MinX: 80, MinZ: 96, MaxX: 88, MaxZ: 104}}}}
	reply := formatFishQueryReply("河鲈", result)
	if !strings.Contains(reply, "80:96～88:104（上限不含）") || !strings.Contains(reply, "蚯蚓 · 浮钓 · 理论 65.88条/小时") || !strings.Contains(reply, "理论速率不计搏鱼、收鱼时间。") || strings.Contains(reply, "世界落点") || strings.Contains(reply, "攻略") {
		t.Fatalf("region reply=%s", reply)
	}
}

func TestFishQueryReplyFractionalRegion(t *testing.T) {
	region := FishQueryRegion{MinX: 1.4800003051757813, MinZ: 25.38000030517578, MaxX: 9.480000305175781, MaxZ: 33.380000305175784}
	if got := formatFishQueryRegion(region); got != "区域约 1.48:25.38～9.48:33.38（上限不含）" {
		t.Fatalf("fractional region reply=%s", got)
	}
	if got := formatFishQueryRegion(FishQueryRegion{MaxX: 1.5, MaxZ: 8}); got != "区域 0:0～1.5:8（上限不含）" {
		t.Fatalf("exact region unnecessarily marked approximate: %s", got)
	}
}

func TestFishQueryMethodLabels(t *testing.T) {
	for rig, want := range map[int64]string{1: "浮钓", 12: "底钓", 5: "路亚", 50: "海钓铁板", 51: "海钓软饵", 52: "海钓鱼肉", 55: "海钓底钓"} {
		if got := fishQueryMethodLabel(FishQueryPlan{RigType: rig, RigName: rigNameForType(rig)}); got != want {
			t.Fatalf("rig %d: label=%s want=%s", rig, got, want)
		}
	}
	if got := fishQueryMethodLabel(FishQueryPlan{RigName: "custom rig"}); got != "custom rig" {
		t.Fatalf("unknown rig label lost: %s", got)
	}
}

func BenchmarkFishQueryRegionalGrid(b *testing.B) {
	rows := make([]fishQueryRegionalWeight, 60)
	weights := make(map[string]float64, len(rows))
	for i := range rows {
		rows[i] = fishQueryRegionalWeight{key: fmt.Sprintf("fish-%d", i), weight: float64(i + 1), acceptance: .8}
		weights[rows[i].key] = rows[i].weight
	}
	now := time.Unix(1794096000, 0)
	b.ReportAllocs()
	for b.Loop() {
		g, _ := newFishQueryRegions("benchmark", fishQueryRegionScene{Size: 1500}, now, true, nil)
		if err := g.shortlist(b.Context(), "fish-0", weights); err != nil {
			b.Fatal(err)
		}
		for range 72 {
			q := &fishQueryCapture{fish: "fish-0", biteProbability: .6, acceptance: .8, gradeShare: .2, regionalWeights: rows}
			if _, err := g.best(b.Context(), q); err != nil {
				b.Fatal(err)
			}
		}
	}
}
