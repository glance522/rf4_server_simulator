package gameserver

import (
	"context"
	"testing"
	"time"

	"rf4server/internal/store"
)

func reloadZonesForTest(t *testing.T, s *Server) {
	t.Helper()
	s.execAdminCmd(&AdminCommand{Op: "reload_fish_zones"})
}

func seedFishZoneTest(t *testing.T) (*Server, *store.DB) {
	t.Helper()
	s, db := seedBaitRuleTest(t)
	// 名册：常见拟鲤 c.roach + 青蛙 frog（seedBaitRuleTest 自带 rules-test 名册）。
	return s, db
}

func saveZoneForTest(t *testing.T, db *store.DB, z store.FishZone) {
	t.Helper()
	if err := db.SaveFishZone(context.Background(), &z, "test"); err != nil {
		t.Fatal(err)
	}
}

// 独占区：区内只出名单鱼种，区外不受影响；命中但交集为空时不出鱼、
// 也不从内置鱼池捞外图鱼。
func TestFishZoneExclusiveGridRect(t *testing.T) {
	s, db := seedFishZoneTest(t)
	saveZoneForTest(t, db, store.FishZone{
		Loc: "rules-test", Name: "拟鲤专区", Kind: "grid_rect",
		GX0: 2, GZ0: 2, GX1: 4, GZ1: 4, Mode: "exclusive", Enabled: true,
		Fish: []store.FishZoneFish{{FishKey: "c.roach", Weight: 1, Enabled: true}},
	})
	reloadZonesForTest(t, s)
	inside := fishRollContext{baits: []string{"rule_worm"}, now: time.Now(), x: 100, z: 100, hasPos: true}
	outside := inside
	outside.x, outside.z = 500, 500
	for range 32 {
		if f := s.rollFishWithRules("level_900_rules", "rule_worm", 0, fishingSkillBonus{BiteSpeed: 1}, inside); f != nil && f.Species != "c.roach" {
			t.Fatalf("exclusive zone must only roll listed fish, got %q", f.Species)
		}
	}
	sawFrog := false
	for range 64 {
		if f := s.rollFishWithRules("level_900_rules", "rule_worm", 0, fishingSkillBonus{BiteSpeed: 1}, outside); f != nil && f.Species == "frog" {
			sawFrog = true
		}
	}
	if !sawFrog {
		t.Fatal("outside the zone the map roster must be unaffected")
	}
}

// 独占区命中但名单与名册无交集：本轮无鱼，不落鱼池兜底。
func TestFishZoneExclusiveEmptySuppressesPoolFallback(t *testing.T) {
	s, db := seedFishZoneTest(t)
	saveZoneForTest(t, db, store.FishZone{
		Loc: "rules-test", Name: "空区", Kind: "grid_rect",
		GX0: 0, GZ0: 0, GX1: 9, GZ1: 9, Mode: "exclusive", Enabled: true,
		Fish: []store.FishZoneFish{{FishKey: "burbot", Weight: 1, Enabled: true}}, // 不在 rules-test 名册
	})
	reloadZonesForTest(t, s)
	c := fishRollContext{baits: []string{"rule_worm"}, now: time.Now(), x: 50, z: 50, hasPos: true}
	for range 8 {
		if f := s.rollFishWithRules("level_900_rules", "rule_worm", 0, fishingSkillBonus{BiteSpeed: 1}, c); f != nil {
			t.Fatalf("empty exclusive zone must yield no fish, got %q", f.Species)
		}
	}
}

// 增益区：名单内乘 boost，名单外乘 keep_others（都不剔除）。
func TestFishZoneBoostMultipliers(t *testing.T) {
	s, db := seedFishZoneTest(t)
	saveZoneForTest(t, db, store.FishZone{
		Loc: "rules-test", Name: "青蛙窝", Kind: "grid_rect",
		GX0: 0, GZ0: 0, GX1: 9, GZ1: 9, Mode: "boost",
		Boost: 4, KeepOthers: 0.25, Enabled: true,
		Fish: []store.FishZoneFish{{FishKey: "frog", Weight: 1, Enabled: true}},
	})
	reloadZonesForTest(t, s)
	zones := s.fishZonesFor(context.Background(), "rules-test")
	if len(zones) != 1 {
		t.Fatalf("zones=%d", len(zones))
	}
	x, z := 50.0, 50.0
	if got := fishZoneFactorForPoint(zones, x, z, "frog"); got != 4 {
		t.Fatalf("listed boost factor=%v want 4", got)
	}
	if got := fishZoneFactorForPoint(zones, x, z, "c.roach"); got != 0.25 {
		t.Fatalf("unlisted keep factor=%v want 0.25", got)
	}
	if got := fishZoneFactorForPoint(zones, 9999, 9999, "frog"); got != 1 {
		t.Fatalf("outside zone factor=%v want 1", got)
	}
}

// 查鱼区域评分接线：名册系数乘进每个区域的分布因子（独占区外目标鱼为 0）。
// 区外因子必须与无名册基线完全一致（散布哈希自身可归零，不能误判成名册效果）。
func TestFishZoneFactorsInRegionalQuery(t *testing.T) {
	at := time.Unix(1794096000, 0)
	zones := []store.FishZone{{
		ID: 1, Loc: "test", Kind: "grid_rect", Mode: "exclusive",
		GX0: 0, GZ0: 0, GX1: 0, GZ1: 0, Enabled: true,
		Fish: []store.FishZoneFish{{FishKey: "c.roach", Weight: 1, Enabled: true}},
	}}
	g, err := newFishQueryRegions("test", fishQueryRegionScene{Size: 80}, at, true, zones)
	if err != nil {
		t.Fatal(err)
	}
	base, err := newFishQueryRegions("test", fishQueryRegionScene{Size: 80}, at, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	pick := func(g *fishQueryRegions, key string) []float64 {
		f, err := g.distributionFactors(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	roachZ, roachB := pick(g, "c.roach"), pick(base, "c.roach")
	frogZ, frogB := pick(g, "frog"), pick(base, "frog")
	if len(roachZ) != 4 {
		t.Fatalf("cells=%d", len(roachZ))
	}
	// 第一格 (0,0) 在独占区内：拟鲤保留原值（weight=1）、青蛙被名册归零。
	if roachZ[0] != roachB[0] {
		t.Fatalf("listed fish factor changed: %v vs %v", roachZ[0], roachB[0])
	}
	if frogB[0] != 0 && frogZ[0] != 0 {
		t.Fatalf("unlisted fish not zeroed inside zone: %v", frogZ[0])
	}
	// 其余三格在区外：与基线逐项一致。
	for i := 1; i < 4; i++ {
		if roachZ[i] != roachB[i] || frogZ[i] != frogB[i] {
			t.Fatalf("outside zone factors differ: roach %v/%v frog %v/%v", roachZ[i], roachB[i], frogZ[i], frogB[i])
		}
	}
}

// 几何与优先级：矩形含端点；无定位不命中；独占区优先于增益区；海图区号按声呐证据。
func TestFishZoneContainmentAndPriority(t *testing.T) {
	exclusive := store.FishZone{ID: 7, Kind: "grid_rect", Mode: "exclusive", GX0: 2, GZ0: 2, GX1: 4, GZ1: 4, Enabled: true}
	boost := store.FishZone{ID: 3, Kind: "grid_rect", Mode: "boost", GX0: 0, GZ0: 0, GX1: 9, GZ1: 9, Enabled: true}
	edge := fishRollContext{x: 2 * 40, z: 4 * 40, hasPos: true}
	if !fishZoneContains(exclusive, edge) {
		t.Fatal("rect bounds must be inclusive")
	}
	beyond := fishRollContext{x: 4*40 + 40, z: 4 * 40, hasPos: true}
	if fishZoneContains(exclusive, beyond) {
		t.Fatal("gx beyond gx1 must not contain")
	}
	if fishZoneContains(exclusive, fishRollContext{x: 100, z: 100}) {
		t.Fatal("no position evidence must not contain")
	}
	// 叠加语义：独占与增益同时命中时两个都返回，聚合层负责取交+连乘。
	if hits := resolveFishZones([]store.FishZone{boost, exclusive}, edge); len(hits) != 2 {
		t.Fatalf("stacking semantics must return all hits, got %d", len(hits))
	}
	sea := store.FishZone{ID: 9, Kind: "sea_zone", Mode: "exclusive", SeaZone: 12, Enabled: true}
	onSea := fishRollContext{runtime: &fishRollRuntime{zoneKnown: true, zone: 12}}
	offSea := fishRollContext{runtime: &fishRollRuntime{zoneKnown: true, zone: 13}}
	if !fishZoneContains(sea, onSea) || fishZoneContains(sea, offSea) {
		t.Fatal("sea zone containment broken")
	}
	if fishZoneContains(sea, fishRollContext{runtime: &fishRollRuntime{}}) {
		t.Fatal("unknown sea zone must not contain")
	}
}

// 叠加语义：独占∩独占取交集（权重连乘）、增益连乘、独占先过滤后增益继续乘。
func TestFishZoneStackingSemantics(t *testing.T) {
	s, db := seedFishZoneTest(t)
	saveZoneForTest(t, db, store.FishZone{
		Loc: "rules-test", Name: "A独占", Kind: "grid_rect",
		GX0: 0, GZ0: 0, GX1: 9, GZ1: 9, Mode: "exclusive", Enabled: true,
		Fish: []store.FishZoneFish{
			{FishKey: "c.roach", Weight: 2, Enabled: true},
			{FishKey: "frog", Weight: 3, Enabled: true},
		},
	})
	saveZoneForTest(t, db, store.FishZone{
		Loc: "rules-test", Name: "B独占", Kind: "grid_rect",
		GX0: 0, GZ0: 0, GX1: 9, GZ1: 9, Mode: "exclusive", Enabled: true,
		Fish: []store.FishZoneFish{{FishKey: "frog", Weight: 1, Enabled: true}},
	})
	saveZoneForTest(t, db, store.FishZone{
		Loc: "rules-test", Name: "C增益", Kind: "grid_rect",
		GX0: 0, GZ0: 0, GX1: 9, GZ1: 9, Mode: "boost",
		Boost: 2, KeepOthers: 0.5, Enabled: true,
		Fish: []store.FishZoneFish{
			{FishKey: "c.roach", Weight: 1, Enabled: true},
			{FishKey: "frog", Weight: 1, Enabled: true},
		},
	})
	reloadZonesForTest(t, s)
	zones := s.fishZonesFor(context.Background(), "rules-test")
	// 查鱼口径：独占交集外=0；frog = A.w3 × B.w1 × C.boost2×C.w1 = 6。
	if got := fishZoneFactorForPoint(zones, 50, 50, "c.roach"); got != 0 {
		t.Fatalf("不在独占交集的鱼种系数应为 0, got %v", got)
	}
	if got := fishZoneFactorForPoint(zones, 50, 50, "frog"); got != 6 {
		t.Fatalf("frog 叠加系数应为 3×1×2×1=6, got %v", got)
	}
	if got := fishZoneFactorForPoint(zones, 9999, 9999, "frog"); got != 1 {
		t.Fatalf("区外系数应为 1, got %v", got)
	}
	// 判鱼口径：区内只可能出 frog（c.roach 被 B 剔除）。
	c := fishRollContext{baits: []string{"rule_worm"}, now: time.Now(), x: 50, z: 50, hasPos: true}
	for range 32 {
		if f := s.rollFishWithRules("level_900_rules", "rule_worm", 0, fishingSkillBonus{BiteSpeed: 1}, c); f != nil && f.Species != "frog" {
			t.Fatalf("叠加后区内只应出 frog, got %q", f.Species)
		}
	}
}

// 刷选格子区域（grid_cells）：任意格子组合命中、集合外不命中、矩形区不受影响。
func TestFishZoneGridCells(t *testing.T) {
	s, db := seedFishZoneTest(t)
	saveZoneForTest(t, db, store.FishZone{
		Loc: "rules-test", Name: "涂的湾", Kind: "grid_cells", Mode: "exclusive", Enabled: true,
		Cells: [][2]int64{{2, 2}, {2, 3}, {3, 3}},
		Fish:  []store.FishZoneFish{{FishKey: "c.roach", Weight: 1, Enabled: true}},
	})
	reloadZonesForTest(t, s)
	in := fishRollContext{x: 2*40 + 20, z: 3*40 + 20, hasPos: true}
	out := fishRollContext{x: 3*40 + 20, z: 2*40 + 20, hasPos: true} // (3,2) 不在集合
	zones := s.fishZonesFor(context.Background(), "rules-test")
	if len(zones) != 1 || !fishZoneContains(zones[0], in) {
		t.Fatal("格子集合内的落点应命中")
	}
	if fishZoneContains(zones[0], out) {
		t.Fatal("格子集合外的落点不应命中")
	}
	if got := fishZoneFactorForPoint(zones, 100, 140, "c.roach"); got != 1 {
		t.Fatalf("命中格内 c.roach 系数应为名单权重 1, got %v", got)
	}
	if got := fishZoneFactorForPoint(zones, 140, 100, "c.roach"); got != 1 {
		t.Fatalf("集合外不受影响, got %v", got)
	}
	_ = fishZoneContains
}

// 圆形钓点区：米级精度命中（含边界）、与格子区叠加（独占取交）。
func TestFishZoneCircle(t *testing.T) {
	s, db := seedFishZoneTest(t)
	saveZoneForTest(t, db, store.FishZone{
		Loc: "rules-test", Name: "小坑", Kind: "circle", Cx: 300, Cz: 200, Radius: 15,
		Mode: "exclusive", Enabled: true,
		Fish: []store.FishZoneFish{{FishKey: "c.roach", Weight: 2, Enabled: true}},
	})
	reloadZonesForTest(t, s)
	zones := s.fishZonesFor(context.Background(), "rules-test")
	if len(zones) != 1 {
		t.Fatalf("zones=%d", len(zones))
	}
	in := fishRollContext{x: 305, z: 205, hasPos: true}        // 距中心 ~7.07m
	edge := fishRollContext{x: 300 + 15, z: 200, hasPos: true} // 恰在半径上
	out := fishRollContext{x: 330, z: 205, hasPos: true}       // 30m 外
	if !fishZoneContains(zones[0], in) || !fishZoneContains(zones[0], edge) {
		t.Fatal("圆内/边界应命中")
	}
	if fishZoneContains(zones[0], out) {
		t.Fatal("圆外不应命中")
	}
	if got := fishZoneFactorForPoint(zones, 305, 205, "c.roach"); got != 2 {
		t.Fatalf("圆内系数应为名单权重 2, got %v", got)
	}
	if got := fishZoneFactorForPoint(zones, 330, 205, "c.roach"); got != 1 {
		t.Fatalf("圆外不受影响, got %v", got)
	}
	// 判鱼：圆内只出 c.roach
	for range 32 {
		if f := s.rollFishWithRules("level_900_rules", "rule_worm", 0, fishingSkillBonus{BiteSpeed: 1}, fishRollContext{baits: []string{"rule_worm"}, now: time.Now(), x: 305, z: 205, hasPos: true}); f != nil && f.Species != "c.roach" {
			t.Fatalf("圆内只应出 c.roach, got %q", f.Species)
		}
	}
}

// 海图区号集合：框选范围存多区号，客户端权威区号命中任一即生效。
func TestFishZoneSeaSet(t *testing.T) {
	s, db := seedFishZoneTest(t)
	// rules-test 不是海图，runtime.zone 不会由 loc12_ 注入——直接构造 runtime 命中集合语义。
	z := store.FishZone{
		Loc: "rules-test", Name: "多区号区", Kind: "sea_zone", SeaZone: 10,
		SeaZones: []int64{10, 120, 198}, Mode: "exclusive", Enabled: true,
		Fish: []store.FishZoneFish{{FishKey: "c.roach", Weight: 1, Enabled: true}},
	}
	saveZoneForTest(t, db, z)
	reloadZonesForTest(t, s)
	zones := s.fishZonesFor(context.Background(), "rules-test")
	if len(zones) != 1 || len(zones[0].SeaZones) != 3 {
		t.Fatalf("集合未持久化: %+v", zones)
	}
	in := fishRollContext{runtime: &fishRollRuntime{zoneKnown: true, zone: 120}}
	out := fishRollContext{runtime: &fishRollRuntime{zoneKnown: true, zone: 121}}
	if !fishZoneContains(zones[0], in) || fishZoneContains(zones[0], out) {
		t.Fatal("集合命中语义错误")
	}
	// 旧单区号回退：SeaZones 为空时按 sea_zone 判。
	legacy := store.FishZone{Kind: "sea_zone", SeaZone: 7, Mode: "exclusive"}
	if !fishZoneContains(legacy, fishRollContext{runtime: &fishRollRuntime{zoneKnown: true, zone: 7}}) ||
		fishZoneContains(legacy, fishRollContext{runtime: &fishRollRuntime{zoneKnown: true, zone: 8}}) {
		t.Fatal("旧单区号回退语义错误")
	}
}
