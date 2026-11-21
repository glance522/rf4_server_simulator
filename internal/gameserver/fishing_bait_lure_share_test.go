package gameserver

import (
	"math"
	"testing"
	"time"
)

// setLureShare 直接写入拟饵占比快照，避免测试依赖 rf4records_catch 的真实数据。
func setLureShare(s *Server, m map[string]float64) {
	s.rf4ShareMu.Lock()
	s.rf4LureShare = m
	s.rf4ShareLoaded = true
	s.rf4ShareAt = time.Now().Add(time.Hour)
	s.rf4ShareMu.Unlock()
}

// 拟饵门按鱼种在官方纪录里的拟饵占比分档：<1% 出池、1~5% 满档但压到四分之一、
// ≥5% 不干预；双壳类与螯虾不吃移动拟饵，无论占比都不参与判鱼。
func TestLureGateByFishShare(t *testing.T) {
	s, db := seedBaitRuleTest(t)
	for _, q := range []string{
		`INSERT INTO loc_wiki_fish(loc,fish_key,fish_zh) VALUES('rules-test','n.pike','常见狗鱼'),('rules-test','freshwater_mussel','河蚌')`,
	} {
		if _, err := db.SQL.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	bite := func(fish string) ([]string, []float64) {
		return s.filterFishBaitSet("rules-test", []string{"rule_lure"}, []string{fish}, []float64{1}, now)
	}
	natural := func(fish string) ([]string, []float64) {
		return s.filterFishBaitSet("rules-test", []string{"rule_worm"}, []string{fish}, []float64{1}, now)
	}

	// ≥5%：正常参与判鱼。
	setLureShare(s, map[string]float64{"n.pike": 50, "freshwater_mussel": 50})
	highKeys, highW := bite("n.pike")
	if len(highKeys) != 1 || highW[0] <= 0 {
		t.Fatalf("high share: keys=%v weights=%v", highKeys, highW)
	}

	// 1~5%：还在池里，但满档权重压到四分之一。
	setLureShare(s, map[string]float64{"n.pike": 3})
	midKeys, midW := bite("n.pike")
	if len(midKeys) != 1 {
		t.Fatalf("mid share excluded: %v", midKeys)
	}
	if ratio := midW[0] / highW[0]; math.Abs(ratio-0.25) > 1e-9 {
		t.Fatalf("mid share ratio=%g want 0.25", ratio)
	}

	// <1%：直接出池。
	setLureShare(s, map[string]float64{"n.pike": 0.5})
	if keys, _ := bite("n.pike"); len(keys) != 0 {
		t.Fatalf("low share still bit: %v", keys)
	}

	// 无官方拟饵纪录（-1）时按未知处理，不能误伤。
	setLureShare(s, map[string]float64{})
	if keys, _ := bite("n.pike"); len(keys) != 1 {
		t.Fatalf("unknown share excluded: %v", keys)
	}

	// 双壳类：占比再高也不吃移动拟饵。
	setLureShare(s, map[string]float64{"freshwater_mussel": 90})
	if keys, _ := bite("freshwater_mussel"); len(keys) != 0 {
		t.Fatalf("bivalve accepted lure: %v", keys)
	}

	// 天然饵不受拟饵门影响。
	setLureShare(s, map[string]float64{"n.pike": 0.5})
	if keys, _ := natural("n.pike"); len(keys) != 1 {
		t.Fatalf("natural bait gated by lure share: %v", keys)
	}
}
