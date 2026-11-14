package gameserver

import (
	"context"
	"testing"
	"time"
)

func setBiteGateSwitch(t *testing.T, s *Server, value string) {
	t.Helper()
	if err := s.DB.TunableSet(context.Background(), "bite_gate_enabled", value, "test"); err != nil {
		t.Fatal(err)
	}
	s.tunMu.Lock()
	s.tunAt = time.Time{}
	s.tunMu.Unlock()
}

// G1：钩上是拟饵时，食性 lure 权重低于门槛的鱼直接出候选——
// 青蛙（0.20）不再攻亮片，常见拟鲤（0.48）照常；开关关闭回到遗留手感。
func TestBiteGatesExcludeLureNonPredators(t *testing.T) {
	s, _ := seedBaitRuleTest(t)
	setBiteGateSwitch(t, s, "0")
	seen := map[string]bool{}
	for range 64 {
		if f := s.rollFish("level_900_rules", "rule_lure"); f != nil {
			seen[f.Species] = true
		}
	}
	if !seen["frog"] || !seen["c.roach"] {
		t.Fatalf("gate off must keep legacy behavior, saw %v", seen)
	}
	setBiteGateSwitch(t, s, "1")
	for range 64 {
		f := s.rollFish("level_900_rules", "rule_lure")
		if f == nil {
			continue
		}
		if f.Species == "frog" {
			t.Fatal("frog must never take a lure with gates on")
		}
		if f.Species != "c.roach" {
			t.Fatalf("unexpected species %q", f.Species)
		}
	}
}

// G2：水层超界乘地板不剔除；栖息带内的正常权重不受影响。
func TestBiteGateLayerFloor(t *testing.T) {
	s, db := seedBaitRuleTest(t)
	// 模板库只带 item_def，名册表为空；补中文名供双票解析攻略规则。
	if _, err := db.SQL.Exec(`INSERT INTO loc_fish(loc,fish_key,name_zh,catches) VALUES('gates-test','vjun','泥鳅',1),('gates-test','crucian','银鲫',1),('gates-test','burbot','江鳕',1)`); err != nil {
		t.Fatal(err)
	}
	setBiteGateSwitch(t, s, "1")
	cfg := s.biteGateConfig()
	if !cfg.enabled {
		t.Fatal("test expects gate enabled by tunable below")
	}
	run := func(c fishRollContext, key string) float64 {
		keys, wts := s.applyBiteGates(s.dbCtx(), []string{key, "perch"}, []float64{1, 1}, c, cfg, "")
		for i, k := range keys {
			if k == key {
				return wts[i]
			}
		}
		return 0
	}
	// 江鳕（layer 0.435~0.992）攻水面拟饵（0.06）：只剩 1% 杂口。
	if w := run(fishRollContext{rigType: 5, lureLayer: 0.06, baits: []string{"rule_lure"}, now: time.Now()}, "burbot"); w != cfg.layerFloor {
		t.Fatalf("burbot at surface must hit floor, got %v", w)
	}
	// 河鲈（layer 0.061~0.965）攻水面拟饵：正常权重。
	if w := run(fishRollContext{rigType: 5, lureLayer: 0.06, baits: []string{"rule_lure"}, now: time.Now()}, "perch"); w != 1 {
		t.Fatalf("perch at surface must keep full weight, got %v", w)
	}
	// 泥鳅（layer 0.435~0.988）浮钓近底（400cm→0.8）：带内不受影响。
	if w := run(fishRollContext{rigType: 1, depthCM: 400, baits: []string{"rule_worm"}, now: time.Now()}, "vjun"); w != 1 {
		t.Fatalf("loach near bottom must keep full weight, got %v", w)
	}
	// 底钓（0.93）对鲤科/底栖带鱼零误伤：没有任何鱼种的 layer_max < 0.78。
	if w := run(fishRollContext{rigType: 12, baits: []string{"rule_worm"}, now: time.Now()}, "crucian"); w != 1 {
		t.Fatalf("crucian on bottom rig must keep full weight, got %v", w)
	}
}

// 门把本图名册剔空时必须空竿重排，不能从内置鱼池捞出不属于本图的鱼。
func TestBiteGateEmptyRosterBlocksPoolFallback(t *testing.T) {
	s, db := seedBaitRuleTest(t)
	for _, q := range []string{
		`INSERT INTO loc_alias(alias,loc) VALUES('level_901_gates','gates-test')`,
		`INSERT INTO loc_wiki_fish(loc,fish_key,fish_zh) VALUES('gates-test','crucian','银鲫')`,
	} {
		if _, err := db.SQL.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	setBiteGateSwitch(t, s, "0")
	if f := s.rollFish("level_901_gates", "rule_lure"); f == nil || f.Species != "crucian" {
		t.Fatalf("gate off legacy roll: %+v", f)
	}
	setBiteGateSwitch(t, s, "1")
	for range 8 {
		if f := s.rollFish("level_901_gates", "rule_lure"); f != nil {
			t.Fatalf("all-lure-incapable roster must yield no fish, got %q from pool fallback", f.Species)
		}
	}
	// 同一名册挂蚯蚓正常出鱼：门只拦「饵钓不到」，不封图。
	if f := s.rollFish("level_901_gates", "rule_worm"); f == nil || f.Species != "crucian" {
		t.Fatalf("worm must still roll crucian: %+v", f)
	}
}

func TestBiteGateConfigDefaultsAndClamps(t *testing.T) {
	s, _ := seedBaitRuleTest(t)
	if cfg := s.biteGateConfig(); cfg.enabled {
		t.Fatal("gate must default off")
	}
	setBiteGateSwitch(t, s, "1")
	if cfg := s.biteGateConfig(); !cfg.enabled || cfg.lureDietMin != 0.30 || cfg.layerFloor != 0.01 || cfg.dietMin != 0.25 || cfg.dietFloor != 0.05 {
		t.Fatalf("defaults: %+v", cfg)
	}
	if err := s.DB.TunableSet(context.Background(), "bite_gate_layer_floor", "-1", "test"); err != nil {
		t.Fatal(err)
	}
	s.tunMu.Lock()
	s.tunAt = time.Time{}
	s.tunMu.Unlock()
	if cfg := s.biteGateConfig(); cfg.layerFloor != 0 {
		t.Fatalf("negative floor must clamp to 0: %+v", cfg)
	}
}

// G-diet 天然饵双票：攻略证据与生态食性合成——双票原权重、单票小口、
// 零票（玉米钓泥鳅）与明确禁饵（饵鱼钓银鲫）剔除。
func TestBiteGateDietDualTicket(t *testing.T) {
	s, db := seedBaitRuleTest(t)
	for _, q := range []string{
		`INSERT INTO loc_alias(alias,loc) VALUES('level_902_diet','gates-diet')`,
		`INSERT INTO loc_wiki_fish(loc,fish_key,fish_zh) VALUES('gates-diet','vjun','泥鳅鱼'),('gates-diet','crucian','银鲫')`,
		`INSERT INTO loc_fish(loc,fish_key,name_zh,catches) VALUES('gates-diet','vjun','泥鳅',1),('gates-diet','crucian','银鲫',1)`,
		`INSERT INTO item_def(numeric_id,item_id,category,attrs_json) VALUES(999804,'rule_corn',27,'{"fopbhlmkcpe":13}'),(999805,'rule_dead',27,'{"fopbhlmkcpe":15}')`,
		`INSERT INTO item_catalog(item_id,name_zh) VALUES('rule_corn','玉米粥'),('rule_dead','死鲭鱼')`,
	} {
		if _, err := db.SQL.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	setBiteGateSwitch(t, s, "1")
	// 蚯蚓：泥鳅（allow 蚯蚓 + worm 1.87）与银鲫（allow 蚯蚓 + worm 1.73）都是双票。
	seen := map[string]bool{}
	for range 64 {
		if f := s.rollFish("level_902_diet", "rule_worm"); f != nil {
			seen[f.Species] = true
		}
	}
	if !seen["vjun"] || !seen["crucian"] {
		t.Fatalf("worm must roll both loach and crucian, saw %v", seen)
	}
	// 玉米粥：银鲫（allow 玉米粥 + plant 2.02）双票；泥鳅（攻略不认 + plant 0.2
	// 在噪声地板以下）零票剔除。
	for range 64 {
		if f := s.rollFish("level_902_diet", "rule_corn"); f != nil && f.Species == "vjun" {
			t.Fatal("loach must never take corn porridge")
		}
	}
	// 死饵鱼：银鲫明确禁饵类（不吃拟饵/饵鱼类）——deny 直接剔除。
	for range 16 {
		if f := s.rollFish("level_902_diet", "rule_dead"); f != nil && f.Species == "crucian" {
			t.Fatal("crucian deny-lists baitfish and must be excluded")
		}
	}
}
