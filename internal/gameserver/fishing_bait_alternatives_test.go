package gameserver

import (
	"math"
	"testing"
	"time"
)

func TestBaitRuleConditionsAndLegalAlternateDiet(t *testing.T) {
	conditional := 0
	for _, rule := range fishBaitRules {
		if rule.condition != "" && rule.condition != "无" {
			conditional++
		}
	}
	if conditional != 82 {
		t.Fatalf("parsed prerequisites = %d", conditional)
	}
	pike := fishBaitRules[ruleName("常见狗鱼")]
	if len(pike.bottomOnly) != 2 || len(pike.deny) != 7 {
		t.Fatalf("pike restrictions = %+v", pike)
	}
	for _, tc := range []struct {
		bait ruleBait
		rule string
		want bool
	}{
		{ruleBait{category: 27, baitType: 6}, "青蛙", true},
		{ruleBait{category: 27, baitType: 10}, "青蛙", false},
		{ruleBait{category: 27, baitType: 0}, "常见拟鲤", false},
		{ruleBait{category: 27, baitType: 4}, "常见拟鲤", false},
		{ruleBait{category: 17, lureType: 4}, "青蛙", false},
		{ruleBait{category: 27, baitType: 6}, "马舌鲽", false},
		{ruleBait{name: "鱼子 Pop Ups 10", category: 27, baitType: 8}, "常见拟鲤", false},
		{ruleBait{name: "河螯虾 14", category: 27, baitType: 9}, "常见拟鲤", false},
		{ruleBait{category: 27, baitType: 10}, "常见拟鲤", true},
	} {
		if got := tc.bait.legalAlternative(fishBaitRules[ruleName(tc.rule)]); got != tc.want {
			t.Errorf("alternate %+v / %s = %v", tc.bait, tc.rule, got)
		}
	}
	for _, tc := range []struct {
		kind int64
		term string
		want bool
	}{
		{8, "煮饵类", true}, {8, "玉米类", false},
		{9, "颗粒类", true}, {9, "奶油类", false},
		{10, "玉米类", true}, {10, "面团类", false},
	} {
		if got := (ruleBait{category: 27, baitType: tc.kind}).legalAlternative(fishBaitRule{allow: []string{tc.term}}); got != tc.want {
			t.Errorf("processed bait type %d / %s = %v", tc.kind, tc.term, got)
		}
	}
	s, db := seedBaitRuleTest(t)
	for _, q := range []string{
		`INSERT INTO loc_wiki_fish(loc,fish_key,fish_zh) VALUES('rules-test','n.pike','常见狗鱼')`,
		`INSERT INTO item_def(numeric_id,item_id,category,attrs_json) VALUES(999805,'rule_unknown',27,'{}'),(999806,'rule_flesh',27,'{"fopbhlmkcpe":14}')`,
		`INSERT INTO item_catalog(item_id,name_zh) VALUES('rule_unknown','未知天然饵'),('rule_flesh','鱼肉块')`,
	} {
		if _, err := db.SQL.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	keys, weights := s.filterFishBaitSet("rules-test", []string{"rule_other"}, []string{"c.roach"}, []float64{1}, now)
	if len(keys) != 1 || math.Abs(weights[0]/maxBaitPreference-.05) > 1e-12 {
		t.Fatalf("legal alternate must be 5%%: %v %v", keys, weights)
	}
	if keys, _ := s.filterFishBaitSet("rules-test", []string{"rule_unknown"}, []string{"c.roach"}, []float64{1}, now); len(keys) != 0 {
		t.Fatal("unknown natural bait became universal food")
	}
	if keys, _ := s.filterFishBaitSet("rules-test", []string{"rule_worm"}, []string{"n.pike"}, []float64{1}, now, fishRollContext{rigType: 12}); len(keys) != 0 {
		t.Fatal("bottom pike accepted worms")
	}
	if keys, _ := s.filterFishBaitSet("rules-test", []string{"rule_flesh"}, []string{"n.pike"}, []float64{1}, now, fishRollContext{rigType: 12}); len(keys) != 1 {
		t.Fatal("bottom pike rejected fish flesh")
	}
}

func TestAlternativeBaitInterestUsesSkillWithoutBypassingExclusions(t *testing.T) {
	for _, tc := range []struct{ interest, speed, want float64 }{
		{0, .5, 0}, {.05, 1, .05}, {.05, .8, .0625}, {.05, .1, .1}, {.08, .5, .1}, {.8, .5, 1},
	} {
		if got := fishingBaitInterest(tc.interest, fishingSkillBonus{BiteSpeed: tc.speed}); math.Abs(got-tc.want) > 1e-12 {
			t.Fatalf("interest=%g speed=%g: got %g want %g", tc.interest, tc.speed, got, tc.want)
		}
	}
}

func TestAlternativeBaitActualPoolBitesRemainRareAndSkillHelps(t *testing.T) {
	s, _ := seedBaitRuleTest(t)
	setBaitRuleSwitch(t, s, "1")
	loc := "alternate-bait-test-pool"
	previous, existed := fishPools[loc]
	fishPools[loc] = []fishPoolEntry{{"c.roach", 80, 600, 318}}
	t.Cleanup(func() {
		if existed {
			fishPools[loc] = previous
		} else {
			delete(fishPools, loc)
		}
	})
	c := fishRollContext{baits: []string{"rule_other"}, forRig: true}
	count := func(speed float64) int {
		bites := 0
		for range 2000 {
			if f := s.rollFishPoolsWithRules(loc, 0, fishingSkillBonus{BiteSpeed: speed}, c); f != nil {
				if f.Species != "c.roach" {
					t.Fatalf("unexpected species: %s", f.Species)
				}
				bites++
			}
		}
		return bites
	}
	plain, skilled := count(1), count(.5)
	if plain < 35 || plain > 170 || skilled < 110 || skilled > 320 || skilled <= plain {
		t.Fatalf("2000 attempts: base=%d skilled=%d", plain, skilled)
	}
}
