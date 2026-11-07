package gameserver

import (
	"testing"
	"time"
)

func enableManualDebugPreset(t *testing.T, s *Server) {
	t.Helper()
	for key, value := range map[string]string{"debug_mode": "1", "bite_delay_fixed": "0", "bite_delay_scale": "1", "bite_delay_max": "5", "fish_weight_scale": "4"} {
		setWeatherTestTunable(t, s, key, value)
	}
}

func TestManualDebugPresetSchedulesThreeToFiveSeconds(t *testing.T) {
	s, db := seedBaitRuleTest(t)
	enableManualDebugPreset(t, s)
	if _, err := db.SQL.Exec(`INSERT INTO fish_bite_setting(loc,bite_delay_fixed,bite_delay_scale,bite_delay_max,bite_delay_lure_max)
        VALUES('rules-test',90,3,120,120)`); err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(s)
	st := s.stateFor(sess.Userid)
	for _, scenario := range []struct {
		rig int64
		loc string
	}{{1, ""}, {12, ""}, {1, "level_900_rules"}, {12, "level_900_rules"}} {
		st.RigType, st.Loc = scenario.rig, scenario.loc
		for range 8 {
			s.scheduleBite(sess, st)
			st.mu.Lock()
			delay := st.BiteAt.Sub(st.CastTs).Seconds()
			st.biteTimer.Stop()
			st.biteTimer = nil
			inWater := st.InWater
			st.mu.Unlock()
			if delay < 3 || delay > 5 {
				t.Fatalf("schedule %+v delay=%f", scenario, delay)
			}
			if inWater {
				t.Fatal("preset skipped the new-cast water confirmation")
			}
		}
	}
}

func TestManualDebugPresetPreservesCustomAndDisabledScheduling(t *testing.T) {
	s, db := seedBaitRuleTest(t)
	if _, err := db.SQL.Exec(`INSERT INTO fish_bite_setting(loc,bite_delay_fixed) VALUES('rules-test',90)`); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct{ key, value string }{
		{"debug_mode", "0"}, {"bite_delay_fixed", "0.2"}, {"bite_delay_scale", "0.5"},
		{"bite_delay_max", "7"}, {"fish_weight_scale", "2"},
	} {
		enableManualDebugPreset(t, s)
		setWeatherTestTunable(t, s, scenario.key, scenario.value)
		for _, rig := range []int64{1, 12} {
			got := s.biteDelayInWeather(&FishState{RigType: rig, Loc: "level_900_rules"}, fishingWeather{}, time.Now())
			if got != 90 {
				t.Fatalf("custom %s=%s rig=%d got=%f want=90", scenario.key, scenario.value, rig, got)
			}
		}
	}
}

func TestManualDebugPresetDoesNotAllowBannedBait(t *testing.T) {
	s, _ := seedBaitRuleTest(t)
	enableManualDebugPreset(t, s)
	setBaitRuleSwitch(t, s, "1")
	if fish := s.rollFishForRig(s.dbCtx(), 0, "", "level_900_rules", "rule_lure", neutralFishingSkillBonus(), fishRollContext{now: time.Now()}); fish != nil {
		t.Fatal("debug preset bypassed banned bait")
	}
}

func TestManualDebugPresetRequiresEveryConfiguredField(t *testing.T) {
	s, db := seedBaitRuleTest(t)
	if _, err := db.SQL.Exec(`INSERT INTO fish_bite_setting(loc,bite_delay_fixed) VALUES('rules-test',90)`); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"bite_delay_fixed", "bite_delay_scale", "bite_delay_max", "fish_weight_scale"} {
		enableManualDebugPreset(t, s)
		if _, err := db.SQL.Exec(`DELETE FROM tunables WHERE key=?`, key); err != nil {
			t.Fatal(err)
		}
		s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
		if got := s.biteDelay(&FishState{RigType: 1, Loc: "level_900_rules"}); got != 90 {
			t.Fatalf("missing %s activated debug preset: %f", key, got)
		}
	}
	enableManualDebugPreset(t, s)
	setWeatherTestTunable(t, s, "bite_delay_scale", "1.0")
	if got := s.biteDelay(&FishState{RigType: 1, Loc: "level_900_rules"}); got < 3 || got > 5 {
		t.Fatalf("equivalent configured value failed preset: %f", got)
	}
}
