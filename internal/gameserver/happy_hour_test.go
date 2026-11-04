package gameserver

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"
)

func registerHappyHourTestSession(t *testing.T, s *Server, sid uint64) *Session {
	t.Helper()
	addFishingRecoverySession(s, 1000001, sid)
	sess := s.byID(sid)
	gsSessionsActive.Add(1)
	t.Cleanup(func() { sess.Close("test_finished") })
	return sess
}

func TestHappyHourXPAddsToBaseAndReplaysOnce(t *testing.T) {
	s, db := newProgressSrv(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	st := s.stateFor(uid)
	f := &RolledFish{FishGUID: newFishGUID(), Species: "perch", Grade: 2, WeightG: 642, HappyHour: true, HappyHourKnown: true}
	r, err := s.settleCatchOnce(ctx, uid, f, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := r.baseXP + int64(math.Round(float64(r.baseXP)*.5))
	if r.totalXP != want || len(r.bonuses) != 1 || r.bonuses[0].Kind != 56 {
		t.Fatalf("breakdown %+v", r)
	}
	entry := s.buildSettle813(ctx, uid, f, r).Field(8).Val.Field(6).Val.ArgItem(0)
	if u32Of(entry.Field(4).Num) != 56 {
		t.Fatalf("native happy-hour entry %+v", entry)
	}
	replayed, err := s.settleCatchOnce(ctx, uid, f, 1)
	if err != nil || !replayed.replayed || replayed.totalXP != want {
		t.Fatalf("replay %+v %v", replayed, err)
	}
	p, err := db.GetProfile(ctx, uid)
	if err != nil || p.XP != want {
		t.Fatalf("duplicate XP %+v %v", p, err)
	}
	// 吃口资格随原鱼保存，结算失败后重启仍沿用同一资格。
	second := &RolledFish{FishGUID: newFishGUID(), Species: "perch", Grade: 2, WeightG: 642, HappyHour: true, HappyHourKnown: true}
	st.mu.Lock()
	st.Pending = second
	st.mu.Unlock()
	if _, err := db.SQL.Exec(`CREATE TRIGGER fail_happy_settlement BEFORE UPDATE ON profile
		BEGIN SELECT RAISE(ABORT,'test settlement failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.settleCatchOnce(ctx, uid, second, 1); err == nil {
		t.Fatal("failed settlement did not roll back")
	}
	if _, err := db.SQL.Exec(`DROP TRIGGER fail_happy_settlement`); err != nil {
		t.Fatal(err)
	}
	fresh := newFishingRecoveryServer(t, db)
	saved := fresh.fishingState(uid).Pending
	if saved == nil || !saved.HappyHourKnown || !saved.HappyHour {
		t.Fatalf("happy qualification lost on restart: %+v", saved)
	}
	retry, err := fresh.settleCatchOnce(ctx, uid, saved, 1)
	if err != nil || retry.totalXP != want || len(retry.bonuses) != 1 {
		t.Fatalf("retry lost original bonus: %+v %v", retry, err)
	}
}

func TestHappyHourLowBaitInterestAtDefaultWaterProbability(t *testing.T) {
	s, db := seedBaitRuleTest(t)
	setBaitRuleSwitch(t, s, "1")
	ctx, now := context.Background(), time.Now()
	if _, err := db.SQL.Exec(`INSERT INTO fish_weight_override(loc,fish_key,weight,enabled,updated_by,updated_ts)
 VALUES('rules-test','c.roach',1,1,'test',0),('rules-test','frog',0,0,'test',0)`); err != nil {
		t.Fatal(err)
	}
	if got := s.biteProbabilityForLoc("level_900_rules"); got != 1 {
		t.Fatalf("default water probability=%v", got)
	}
	keys, weights := s.filterFishBaitSet("rules-test", []string{"rule_other"}, []string{"c.roach"}, []float64{1}, now)
	if len(keys) != 1 {
		t.Fatal("low-interest bait unexpectedly excluded")
	}
	interest := weights[0] / maxBaitPreference
	if interest <= 0 || interest >= .1 {
		t.Fatalf("expected low interest, got %v", interest)
	}
	c := fishRollContext{baits: []string{"rule_other"}, now: now, biteRoll: func() float64 { return interest * 1.05 }}
	if fish := s.rollFishForRig(ctx, 1000001, "", "level_900_rules", "rule_other", neutralFishingSkillBonus(), c); fish != nil {
		t.Fatal("inactive hour accepted the injected low-interest roll")
	}
	c.happyBite = .1
	if fish := s.rollFishForRig(ctx, 1000001, "", "level_900_rules", "rule_other", neutralFishingSkillBonus(), c); fish == nil || fish.Species != "c.roach" {
		t.Fatalf("active hour did not reach final bait gate: %+v", fish)
	}
	c.baits = []string{"rule_lure"}
	if fish := s.rollFishForRig(ctx, 1000001, "", "level_900_rules", "rule_lure", neutralFishingSkillBonus(), c); fish != nil {
		t.Fatal("happy hour bypassed explicit forbidden bait")
	}
	if happyHourBiteChance(.1, .1) != .1 || happyHourBiteChance(0, .1) != 0 {
		t.Fatal("happy hour exceeded low-interest limit or enabled forbidden bait")
	}
}

func TestHappyHourProficiencyChanceAndPermanentCap(t *testing.T) {
	for _, tc := range []struct {
		current, roll float64
		want          bool
	}{{0, .94, true}, {0, .96, false}, {50, .35, true}, {50, .38, false}, {100, 0, false}} {
		gain := fishingProficiencyGainForRoll(tc.current, happyHourProficiencyRoll(tc.current, tc.roll, true), 0)
		if (gain > 0) != tc.want {
			t.Fatalf("current=%v roll=%v gain=%v", tc.current, tc.roll, gain)
		}
		if got := happyHourProficiencyRoll(tc.current, tc.roll, false); got != tc.roll {
			t.Fatal("inactive hour changed proficiency roll")
		}
	}
}

func TestHappyHourValidFishingPendingAndCloseFence(t *testing.T) {
	s, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rod := baitTestRod(t, s)
	sess := registerHappyHourTestSession(t, s, 828)
	st := s.fishingState(uid)
	st.mu.Lock()
	st.GearGuid = rod
	st.CastActive, st.InWater, st.Resolved = true, true, false
	st.mu.Unlock()
	now := time.Now()
	if r := sess.updateHappyHour(now, false); r.Active {
		t.Fatal("empty rod started happy hour")
	}
	st.mu.Lock()
	st.Pending = &RolledFish{FishGUID: newFishGUID()}
	st.Hooked = true
	st.mu.Unlock()
	if r := sess.updateHappyHour(now, false); !r.Active {
		t.Fatal("pending fight after bait consumption lost qualification")
	}
	if r := sess.updateHappyHour(now.Add(10*time.Second), false); r.RemainingSeconds != 3590 {
		t.Fatalf("fight time not counted: %+v", r)
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); sess.updateHappyHour(now.Add(11*time.Second), false) }()
	}
	sess.Close("happy-hour-test")
	wg.Wait()
	var active bool
	if err := db.SQL.QueryRowContext(ctx, `SELECT active FROM happy_hour_state WHERE uid=?`, uid).Scan(&active); err != nil || active {
		t.Fatalf("closed session resumed happy time: %v %v", active, err)
	}
}

func TestHappyHourBiteQualificationPersistsBeforeFailedPush(t *testing.T) {
	s, db := newRigTestServer(t)
	const uid = int64(1000001)
	rod := baitTestRod(t, s)
	baitTestMount(t, s, rod, "worm_01", 2, 350)
	sess := registerHappyHourTestSession(t, s, 829)
	fish := &RolledFish{FishGUID: newFishGUID(), Species: "perch", Grade: 2, WeightG: 642}
	st := s.fishingState(uid)
	st.mu.Lock()
	st.GearGuid = rod
	st.CastActive, st.InWater = true, true
	st.Resolved = false
	st.CastTs = time.Now().Add(-time.Minute)
	st.Armed = fish
	st.mu.Unlock()
	s.onBiteTimer(sess.Userid)
	st.mu.Lock()
	if st.Pending != fish || !fish.HappyHour || !fish.HappyHourKnown {
		t.Errorf("bite qualification missing: %+v", st.Pending)
	}
	st.mu.Unlock()
	saved, err := db.LoadFishingState(context.Background(), uid)
	if err != nil || saved == nil || saved.Pending == nil || !saved.Pending.HappyHourKnown || !saved.Pending.HappyHour {
		t.Fatalf("qualification not persisted: %+v %v", saved, err)
	}
	restored := rolledFishFromStore(saved.Pending)
	if restored.FishGUID != fish.FishGUID || !restored.HappyHour || !restored.HappyHourKnown {
		t.Fatal("recovery changed original qualification")
	}
}
