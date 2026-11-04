package gameserver

import (
	"context"
	"math/rand"
	"testing"
	"time"
)

func setWeatherTestTunable(t *testing.T, s *Server, key, value string) {
	t.Helper()
	if err := s.DB.TunableSet(context.Background(), key, value, "test"); err != nil {
		t.Fatal(err)
	}
	s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
}

func TestWeatherAndBaitAffectActualBitesWithIndependentSwitches(t *testing.T) {
	t.Setenv("GODEBUG", "randseednop=0")
	s, db := seedBaitRuleTest(t)
	if _, err := db.SQL.Exec(`INSERT INTO fish_weight_override(loc,fish_key,weight,enabled,updated_by,updated_ts)
		VALUES('rules-test','frog',0,0,'test',0),('rules-test','c.roach',1,1,'test',0)`); err != nil {
		t.Fatal(err)
	}
	setWeatherTestTunable(t, s, "bait_pref_scale", "0")
	weather := weatherActivitySample(fishingWeatherRain, 22)
	count := func(bait string, w fishingWeather) int {
		t.Helper()
		rand.Seed(20261104)
		hits := 0
		for range 256 {
			if s.rollFishForRig(context.Background(), 0, "", "level_900_rules", bait, neutralFishingSkillBonus(),
				fishRollContext{now: weatherTimeAt(12), weather: w}) != nil {
				hits++
			}
		}
		return hits
	}
	if s.tunableF("fish_weather_enabled", 0) != 0 || count("rule_worm", weather) != 256 {
		t.Fatal("default weather switch changed legacy bites")
	}
	setWeatherTestTunable(t, s, "fish_weather_enabled", "1")
	storm := weather
	storm.kind = fishingWeatherThunderstorm
	weatherOnly := count("rule_worm", storm)
	if weatherOnly < 50 || weatherOnly > 180 {
		t.Fatalf("weather without bait/distribution switches had no absolute effect: %d", weatherOnly)
	}
	setBaitRuleSwitch(t, s, "1")
	preferred, other, badWeather := count("rule_worm", weather), count("rule_other", weather), count("rule_worm", storm)
	if preferred < 120 || other < 1 || other > 45 || preferred <= other || badWeather >= preferred {
		t.Fatalf("preferred=%d other=%d storm=%d", preferred, other, badWeather)
	}
	if count("rule_lure", weather) != 0 {
		t.Fatal("weather or skills bypassed a banned bait")
	}
	setBaitRuleSwitch(t, s, "0")
	setWeatherTestTunable(t, s, "fish_weather_enabled", "0")
	if count("rule_worm", storm) != 256 {
		t.Fatal("disabling weather failed to restore legacy bites")
	}
	setWeatherTestTunable(t, s, "fish_weather_enabled", "1")
	if _, err := db.SQL.Exec(`UPDATE fish_weight_override SET enabled=0,weight=0 WHERE loc='rules-test'`); err != nil {
		t.Fatal(err)
	}
	if s.rollFishForRig(context.Background(), 0, "", "level_900_rules", "rule_worm", neutralFishingSkillBonus(), fishRollContext{weather: weather}) != nil {
		t.Fatal("weather-only mode restored administratively disabled fish")
	}
}

func TestWeatherRetryPreservesCastAndStopsWhenDisabledOrHooked(t *testing.T) {
	s, _ := seedBaitRuleTest(t)
	setWeatherTestTunable(t, s, "fish_weather_enabled", "1")
	setWeatherTestTunable(t, s, "bite_probability", "0")
	setWeatherTestTunable(t, s, "bite_delay_fixed", "60")
	addFishingRecoverySession(s, 1000001, 901)
	sess := s.liveSessionByUID(1000001)
	st := s.stateFor(sess.Userid)
	for _, scenario := range []struct {
		rig     int64
		hooked  bool
		enabled string
		retry   bool
	}{{1, false, "1", true}, {12, false, "1", true}, {5, false, "1", false}, {1, true, "1", false}, {1, false, "0", false}} {
		setWeatherTestTunable(t, s, "fish_weather_enabled", scenario.enabled)
		st.mu.Lock()
		st.CastActive, st.InWater, st.Resolved, st.PushSent, st.Hooked = true, true, false, false, scenario.hooked
		st.CastTs, st.RigType = time.Now().Add(-time.Minute), scenario.rig
		castTs, generation := st.CastTs, st.castGeneration
		ticket := fishingTimerTicket{st.castGeneration, st.biteTicket}
		timer := time.NewTimer(time.Hour)
		st.biteTimer = timer
		st.mu.Unlock()
		s.onBiteTimerTicket(sess.Userid, ticket)
		timer.Stop()
		st.mu.Lock()
		retry := st.biteTimer != nil
		if retry != scenario.retry || st.Pending != nil || st.PushSent || st.castGeneration != generation || !st.CastTs.Equal(castTs) {
			t.Errorf("retry state for %+v: retry=%v pending=%v pushed=%v", scenario, retry, st.Pending, st.PushSent)
		}
		if st.biteTimer != nil {
			st.biteTimer.Stop()
			st.biteTimer = nil
		}
		st.mu.Unlock()
	}
}
