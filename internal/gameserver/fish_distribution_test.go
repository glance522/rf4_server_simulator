package gameserver

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/store"
)

func TestFishDistributionRegionsTimeAndActivity(t *testing.T) {
	now := time.Unix(worldDayEpochUnix+12*realSecPerGameHour, 0)
	c := fishRollContext{now: now, hasPos: true, x: 50, z: 70}
	a := fishArea("map-a", c)
	if a != fishArea("map-a", c) || a == fishArea("map-b", c) {
		t.Fatal("area must be stable and map-specific")
	}
	c.x += 80
	if a == fishArea("map-a", c) {
		t.Fatal("moving across regions must change the area")
	}
	keys := []string{"perch", "ruffe", "c.roach", "n.pike", "eel", "crucian"}
	weights := func(area string, at time.Time) []float64 {
		var out []float64
		for _, key := range keys {
			out = append(out, distributionMultiplier(area, key, at))
		}
		return out
	}
	w := weights(a, now)
	if !reflect.DeepEqual(w, weights(a, now.Add(time.Second))) {
		t.Fatal("school must stay stable within its refresh period")
	}
	if reflect.DeepEqual(w, weights(fishArea("map-a", c), now)) || reflect.DeepEqual(w, weights(a, now.Add(600*time.Second))) {
		t.Fatal("regions and refresh periods must change schools")
	}
	if activityMultiplier("ruffe", 23) <= activityMultiplier("ruffe", 12) || activityMultiplier("perch", 23) >= activityMultiplier("perch", 12) {
		t.Fatal("day and night habits reversed")
	}
	// 所有鱼种都参与随机分布，资料不足的鱼不编造昼夜偏好。
	if activityMultiplier("unknown", 23) != 1 {
		t.Fatal("unknown activity must be neutral")
	}
	zero, positive := false, false
	for i := 0; i < 40; i++ {
		v := distributionMultiplier(a, "perch", now.Add(time.Duration(i)*600*time.Second))
		zero = zero || v == 0
		positive = positive || v > 0
	}
	if !zero || !positive {
		t.Fatal("fish must move in and out of regions")
	}
}

func TestFishBaitPreferenceAlternateCombinationAndChange(t *testing.T) {
	s, db := seedBaitRuleTest(t)
	if _, err := db.SQL.Exec(`INSERT INTO item_def(numeric_id,item_id,category,attrs_json) VALUES(999804,'rule_fly',27,'{"fopbhlmkcpe":7}'); INSERT INTO item_catalog(item_id,name_zh) VALUES('rule_fly','苍蝇')`); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(worldDayEpochUnix+1500, 0)
	weight := func(baits []string, at time.Time) float64 {
		t.Helper()
		keys, w := s.filterFishBaitSet("rules-test", baits, []string{"c.roach"}, []float64{1}, at)
		if len(keys) == 0 {
			return 0
		}
		return w[0]
	}
	favorite := weight([]string{"rule_worm"}, now)
	other := weight([]string{"rule_other"}, now)
	if other <= 0 || other >= favorite || other > 0.12 || favorite < 1 {
		t.Fatalf("favorite=%f other=%f", favorite, other)
	}
	if weight([]string{"rule_worm", "rule_fly"}, now) <= favorite {
		t.Fatal("two preferred baits must participate together")
	}
	if weight([]string{"rule_worm", "rule_worm"}, now) != favorite || weight([]string{"rule_fly", "rule_worm"}, now) != weight([]string{"rule_worm", "rule_fly"}, now) {
		t.Fatal("duplicates or slot ordering changed bait preference")
	}
	if weight([]string{"rule_worm", "rule_lure"}, now) != 0 || weight([]string{"missing"}, now) != 0 || weight(nil, now) != 0 {
		t.Fatal("combination bypassed banned, invalid or absent bait")
	}
	if favorite == weight([]string{"rule_worm"}, now.Add(600*time.Second)) {
		t.Fatal("bait preference did not vary over game time")
	}
	setBaitRuleSwitch(t, s, "1")
	// 强制只选拟鲤，验证差异也传到了实际派鱼概率，而非仅改变鱼种比例。
	if _, err := db.SQL.Exec(`INSERT INTO fish_weight_override(loc,fish_key,weight,enabled,updated_by,updated_ts) VALUES('rules-test','frog',0,0,'test',0),('rules-test','c.roach',1,1,'test',0)`); err != nil {
		t.Fatal(err)
	}
	preferredHits, otherHits := 0, 0
	for i := 0; i < 160; i++ {
		if s.rollFishForRig(context.Background(), 0, "", "level_900_rules", "rule_worm", neutralFishingSkillBonus()) != nil {
			preferredHits++
		}
		if s.rollFishForRig(context.Background(), 0, "", "level_900_rules", "rule_other", neutralFishingSkillBonus()) != nil {
			otherHits++
		}
	}
	if preferredHits < 65 || otherHits > 35 || preferredHits <= otherHits {
		t.Fatalf("preferred hits=%d alternate hits=%d", preferredHits, otherHits)
	}
}

func TestFishSwitchCombinationsAndNoDisabledFallback(t *testing.T) {
	s, db := seedBaitRuleTest(t)
	for _, distribution := range []string{"0", "1"} {
		for _, bait := range []string{"0", "1"} {
			if err := db.TunableSet(context.Background(), "fish_distribution_enabled", distribution, "test"); err != nil {
				t.Fatal(err)
			}
			setBaitRuleSwitch(t, s, bait)
			f := s.rollFish("unknown-map", "rule_worm")
			if (f == nil) != (distribution == "1") {
				t.Fatalf("distribution=%s bait=%s fallback=%+v", distribution, bait, f)
			}
			if bait == "0" && distribution == "0" && s.rollFish("unknown-map", "missing") == nil {
				t.Fatal("closed switches changed legacy behavior")
			}
		}
	}
	if _, err := db.SQL.Exec(`INSERT INTO fish_weight_override(loc,fish_key,weight,enabled,updated_by,updated_ts) VALUES('rules-test','frog',0,0,'test',0),('rules-test','c.roach',0,0,'test',0)`); err != nil {
		t.Fatal(err)
	}
	for _, bait := range []string{"0", "1"} {
		setBaitRuleSwitch(t, s, bait)
		if s.rollFish("level_900_rules", "rule_worm") != nil {
			t.Fatal("distribution restored administratively disabled fish")
		}
	}
}

func TestFishDistributionAllStaticMapsAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")
	for attempt := 0; attempt < 2; attempt++ {
		db, err := store.OpenWithStatic(path, filepath.Join("..", "..", "server_data", "static.sqlite3"))
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Migrate(); err != nil {
			t.Fatal(err)
		}
		s := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "schools"}}, logging.Discard(), nil)
		s.SetDB(db)
		if attempt == 0 {
			if err := db.TunableSet(context.Background(), "fish_distribution_enabled", "1", "test"); err != nil {
				t.Fatal(err)
			}
			setBaitRuleSwitch(t, s, "1")
		}
		if s.tunableF("fish_distribution_enabled", 0) != 1 || s.tunableF("fish_bait_rules_enabled", 0) != 1 {
			t.Fatal("restart lost independent switches")
		}
		rows, err := db.SQL.Query(`SELECT alias,loc FROM loc_alias`)
		if err != nil {
			t.Fatal(err)
		}
		maps := map[string]string{}
		for rows.Next() {
			var alias, loc string
			if err := rows.Scan(&alias, &loc); err != nil {
				t.Fatal(err)
			}
			maps[alias] = loc
		}
		rows.Close()
		if len(maps) < 18 {
			t.Fatalf("maps=%d", len(maps))
		}
		for alias, loc := range maps {
			roster, err := db.LocRoster(context.Background(), loc)
			if err != nil || len(roster) == 0 {
				t.Fatalf("missing roster %s: %v", alias, err)
			}
			valid := map[string]bool{}
			for _, r := range roster {
				valid[r.FishKey] = true
			}
			for _, b := range []string{"0", "1"} {
				setBaitRuleSwitch(t, s, b)
				produced := 0
				for _, hour := range []int64{3, 12, 21} {
					f := s.rollFishWithRules(alias, "worm_01", 0, neutralFishingSkillBonus(), fishRollContext{x: 100, z: 200, hasPos: true, now: time.Unix(worldDayEpochUnix+hour*150, 0)})
					if f != nil {
						produced++
					}
					if f != nil && !valid[f.Species] {
						t.Fatalf("map %s produced foreign fish %s", alias, f.Species)
					}
				}
				if b == "0" && produced == 0 {
					t.Fatalf("map %s never produced a school", alias)
				}
			}
		}
		setBaitRuleSwitch(t, s, "1")
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
