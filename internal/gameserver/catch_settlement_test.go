package gameserver

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"sync"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func TestCatchSettlementOnceAcrossCallsAndRestart(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.GearGuid = rod.GUID
	st.Pending = &RolledFish{FishGUID: "22222222222222222222222222222222", Species: "perch", Grade: 2,
		WeightG: 642, LengthM: .28, LocLevel: "level_001_lake"}
	st.Hooked = true
	st.mu.Unlock()
	sess := newProbeSess(srv)
	run := func(srv *Server, sess *Session, call uint32) {
		t.Helper()
		body, handled := srv.fishingEndSettle(&Ctx{Sess: sess, CallID: call,
			Main: 14, Sub: 4, ExpectResponse: true, Log: logging.Discard()})
		if !handled || len(body) == 0 {
			t.Fatal("结算未正常返回")
		}
	}
	run(srv, sess, 1)
	p1, _ := db.GetProfile(ctx, uid)
	r1, _ := db.GetItemByGUID(ctx, rod.GUID)
	var count1 int
	_ = db.SQL.QueryRow(`SELECT COUNT(*) FROM item_damage_log WHERE uid=?`, uid).Scan(&count1)
	run(srv, sess, 2)
	restarted := newFishingRecoveryServer(t, db)
	run(restarted, newProbeSess(restarted), 3)
	p2, _ := db.GetProfile(ctx, uid)
	r2, _ := db.GetItemByGUID(ctx, rod.GUID)
	var count2 int
	_ = db.SQL.QueryRow(`SELECT COUNT(*) FROM item_damage_log WHERE uid=?`, uid).Scan(&count2)
	if p1.XP <= 0 || p2.XP != p1.XP || r2.Durability != r1.Durability || count2 != count1 || count1 == 0 {
		t.Fatalf("重复结算产生副作用: xp=%d/%d wear=%g/%g logs=%d/%d", p1.XP, p2.XP, r1.Durability, r2.Durability, count1, count2)
	}
	recovered := restarted.stateFor(uid)
	recovered.mu.Lock()
	ready := recovered.Pending != nil && recovered.Pending.XP > 0
	recovered.mu.Unlock()
	if !ready {
		t.Fatal("重启后不能继续入护或放生")
	}
}

func TestCatchSettlementRollsBackOnReceiptFailure(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	before, _ := db.GetProfile(ctx, uid)
	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.GearGuid = rod.GUID
	st.mu.Unlock()
	if _, err := db.SQL.Exec(`CREATE TRIGGER fail_settlement_receipt BEFORE INSERT ON kv
		WHEN NEW.k LIKE 'catch-settlement:%' BEGIN SELECT RAISE(ABORT,'receipt failure'); END`); err != nil {
		t.Fatal(err)
	}
	fish := &RolledFish{FishGUID: "33333333333333333333333333333333", Species: "perch", Grade: 2, WeightG: 642}
	if result := srv.settleCatchXP(ctx, uid, fish, 1); result != nil {
		t.Fatal("回执失败不能视为结算成功")
	}
	after, _ := db.GetProfile(ctx, uid)
	var skills, transactions, damage int
	_ = db.SQL.QueryRow(`SELECT COUNT(*) FROM skill WHERE uid=?`, uid).Scan(&skills)
	_ = db.SQL.QueryRow(`SELECT COUNT(*) FROM txn WHERE uid=?`, uid).Scan(&transactions)
	_ = db.SQL.QueryRow(`SELECT COUNT(*) FROM item_damage_log WHERE uid=?`, uid).Scan(&damage)
	rodAfter, _ := db.GetItemByGUID(ctx, rod.GUID)
	if after.XP != before.XP || skills != 0 || transactions != 0 || fish.XP != 0 || damage != 0 || rodAfter.Durability != rod.Durability {
		t.Fatalf("结算失败未整体回滚: xp=%d skills=%d txns=%d fishXP=%d", after.XP, skills, transactions, fish.XP)
	}
}

func TestCatchSkillUsesRigAndReportsRemainingGain(t *testing.T) {
	for _, tc := range []struct {
		name             string
		rig, skill       int64
		bait             string
		before, wantGain float64
	}{
		{"float", 1, 1, "spinner_wrong_hint", 20, .2},
		{"lure", 5, 2, "worm", 30, .2},
		{"bottom", 12, 3, "spinner_wrong_hint", 40, .2},
		{"marine_pilker", 50, 11, "worm", 50, .2},
		{"marine_bottom", 55, 11, "worm", 99.9, .1},
		{"capped", 12, 3, "worm", 100, 0},
		{"legacy_above_cap", 12, 3, "worm", 105, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, db := newProgressSrv(t)
			ctx := context.Background()
			const uid = int64(1000001)
			if _, err := db.EnsureProfile(ctx, uid); err != nil {
				t.Fatal(err)
			}
			if _, err := db.SQL.Exec(`INSERT INTO skill(uid,skill_id,points) VALUES(?,?,?)`, uid, tc.skill, tc.before); err != nil {
				t.Fatal(err)
			}
			st := srv.stateFor(uid)
			st.mu.Lock()
			st.RigType = tc.rig
			st.mu.Unlock()
			fish := &RolledFish{FishGUID: newFishGUID(), Species: "perch", Grade: 2, WeightG: 642}
			result := srv.settleCatchXP(ctx, uid, fish, tc.rig)
			if result == nil || result.skillID != tc.skill || result.skillGain < 0 || result.skillGain > tc.wantGain+1e-6 {
				t.Fatalf("wrong skill result: %+v", result)
			}
			var points float64
			if err := db.SQL.QueryRow(`SELECT points FROM skill WHERE uid=? AND skill_id=?`, uid, tc.skill).Scan(&points); err != nil || math.Abs(points-(tc.before+result.skillGain)) > 1e-6 {
				t.Fatalf("points=%v err=%v", points, err)
			}
			block := srv.buildSettle813(ctx, uid, fish, result).Field(8).Val
			field := block.Field(3)
			if math.Abs(float64(proto.F32FromBits(field.U32()))-result.skillGain) > 1e-6 {
				t.Fatal("displayed skill gain differs from saved gain")
			}
		})
	}
}

func TestCatchSettlementConcurrentRequestsCreditOnce(t *testing.T) {
	s, db := newProgressSrv(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	fish := &RolledFish{FishGUID: newFishGUID(), Species: "perch", Grade: 2, WeightG: 642}
	type outcome struct {
		r   *settleXpResult
		err error
	}
	results := make(chan outcome, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			r, err := s.settleCatchOnce(ctx, uid, fish, 1)
			results <- outcome{r, err}
		}()
	}
	workers.Wait()
	close(results)
	fresh, replayed := 0, 0
	var credited int64
	for result := range results {
		if result.err != nil || result.r == nil {
			t.Fatalf("settlement failed: %v", result.err)
		}
		if result.r.replayed {
			replayed++
		} else {
			fresh++
		}
		if credited != 0 && credited != result.r.totalXP {
			t.Fatal("same fish returned different XP")
		}
		credited = result.r.totalXP
	}
	p, err := db.GetProfile(ctx, uid)
	var transactions int
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM txn WHERE uid=? AND kind='xp'`, uid).Scan(&transactions); err != nil {
		t.Fatal(err)
	}
	if fresh != 1 || replayed != 1 || p.XP != credited || transactions != 1 {
		t.Fatalf("duplicate credit: fresh=%d replayed=%d xp=%d want=%d transactions=%d", fresh, replayed, p.XP, credited, transactions)
	}
}

func TestCatchSettlementReadsLegacyReceiptWithoutRecredit(t *testing.T) {
	s, db := newProgressSrv(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	guid := newFishGUID()
	saved := catchReceipt{BaseXP: 40, TotalXP: 90, PremiumXP: 40, Level: 2, AccumulatedXP: 500, SkillID: 1,
		Bonuses: []catchXPBonus{{Kind: 1, GUID: zeroGUID, Multiplier: .25, XP: 10}}}
	raw, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.KVSet(ctx, "catch-settlement:1000001:"+guid, string(raw)); err != nil {
		t.Fatal(err)
	}
	fish := &RolledFish{FishGUID: strings.ToUpper(guid), Species: "perch", Grade: 2, WeightG: 642}
	r, err := s.settleCatchOnce(ctx, uid, fish, 1)
	if err != nil || r == nil || !r.replayed || r.totalXP != 90 || r.premiumXP != 40 || len(r.bonuses) != 1 || r.bonuses[0] != saved.Bonuses[0] || r.skillGain != .2 {
		t.Fatalf("legacy receipt changed: %+v %v", r, err)
	}
	entries := s.buildSettle813(ctx, uid, fish, r).Field(8).Val.Field(6).Val
	if entries.ArgCount() != 2 {
		t.Fatalf("legacy bonus count = %d", entries.ArgCount())
	}
	for i, want := range []struct {
		kind uint32
		xp   uint64
		rate float32
	}{{1, 10, .25}, {4, 40, 1}} {
		entry := entries.ArgItem(i)
		kind, amount, category, rate := entry.Field(4), entry.Field(2), entry.Field(3), entry.Field(0)
		if kind.U32() != want.kind || amount.U64() != want.xp || category.U32() != 9 || proto.F32FromBits(rate.U32()) != want.rate {
			t.Fatalf("legacy native bonus %d changed: %+v", i, entry)
		}
	}
	p, err := db.GetProfile(ctx, uid)
	if err != nil || p.XP != 0 {
		t.Fatalf("legacy receipt credited XP: %+v %v", p, err)
	}
}

func TestCatchSettlementFailureRetainsPendingFish(t *testing.T) {
	s, db := newProgressSrv(t)
	const uid = int64(1000001)
	if _, err := db.SQL.Exec(`CREATE TRIGGER reject_catch BEFORE INSERT ON kv WHEN NEW.k LIKE 'catch-settlement:%' BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	fish := &RolledFish{FishGUID: newFishGUID(), Species: "perch", Grade: 2, WeightG: 642}
	st := s.stateFor(uid)
	st.mu.Lock()
	st.Pending = fish
	st.Hooked = true
	st.CastActive = true
	st.mu.Unlock()
	capture := &ctxReplyCapture{}
	body, handled := s.fishingEndSettle(&Ctx{Sess: newProbeSess(s), Main: 14, Sub: 4, ExpectResponse: true, Log: logging.Discard(), replyCapture: capture})
	_, present, _, failure := capture.snapshot()
	st.mu.Lock()
	same := st.Pending == fish && st.Hooked && fish.XP == 0
	st.mu.Unlock()
	if !handled || len(body) != 0 || !present || !failure || !same {
		t.Fatalf("failed catch lost pending fish: handled=%v present=%v failure=%v same=%v", handled, present, failure, same)
	}
}
