package gameserver

import (
	"context"
	"testing"
)

func TestCombinedSkillBaitWeatherAndHappyHourUseOneAcceptanceGate(t *testing.T) {
	s, db := seedBaitRuleTest(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO fish_weight_override(loc,fish_key,weight,enabled,updated_by,updated_ts)
		VALUES('rules-test','c.roach',1,1,'test',0),('rules-test','frog',0,0,'test',0)`); err != nil {
		t.Fatal(err)
	}
	if err := db.AddSkillPoints(ctx, uid, 11, 60); err != nil {
		t.Fatal(err)
	}
	rod, err := s.CreateItem(ctx, uid, "boatr_10222_70_100", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	bonus := s.fishingBonus(ctx, uid, 55, rod.GUID)
	if bonus.BiteSpeed >= s.fishingBonus(ctx, uid, 55, "").BiteSpeed {
		t.Fatal("current owned rod did not contribute mastery")
	}
	setBaitRuleSwitch(t, s, "1")
	setWeatherTestTunable(t, s, "fish_weather_enabled", "1")
	c := fishRollContext{baits: []string{"rule_other"}, now: weatherTimeAt(12),
		weather: weatherActivitySample(fishingWeatherThunderstorm, 22), happyBite: .1}
	keys, weights := s.filterFishBaitSet("rules-test", c.baits, []string{"c.roach"}, []float64{1}, c.now, c)
	if len(keys) != 1 {
		t.Fatal("legal alternative bait was excluded")
	}
	interest := fishingBaitInterest(weights[0]/maxBaitPreference, bonus)
	activity := s.weatherFishActivity("c.roach", c)
	if interest <= .05 || interest >= .1 || activity >= 1 {
		t.Fatalf("fixture did not combine skill, low interest and weather: %v %v", interest, activity)
	}
	chance := happyHourBiteChance(interest, c.happyBite) * activity
	roll := func(draw float64) *RolledFish {
		c.biteRoll = func() float64 { return draw }
		return s.rollFishForRig(ctx, uid, "", "level_900_rules", "rule_other", bonus, c)
	}
	// 天气降低吃口后，这个随机值需要欢乐时光加成才能命中。
	draw := (interest*activity + chance) / 2
	if fish := roll(draw); fish == nil || fish.Species != "c.roach" {
		t.Fatalf("combined bonus failed at the final gate: %+v", fish)
	}
	c.happyBite = 0
	if fish := roll(draw); fish != nil {
		t.Fatal("inactive happy hour retained its bonus")
	}
	c.happyBite = .1
	// 重复叠加欢乐时光或漏掉天气修正都会错误放行。
	doubleChance := happyHourBiteChance(happyHourBiteChance(interest, .1), .1) * activity
	if fish := roll((chance + doubleChance) / 2); fish != nil {
		t.Fatal("happy-hour bonus was applied twice or weather was skipped")
	}
	skillTwice := happyHourBiteChance(fishingBaitInterest(interest, bonus), c.happyBite) * activity
	if fish := roll((chance + skillTwice) / 2); fish != nil {
		t.Fatal("skill bonus was applied twice")
	}
	c.baits = []string{"rule_lure"}
	if fish := roll(0); fish != nil {
		t.Fatal("combined modifiers bypassed forbidden bait")
	}
}
