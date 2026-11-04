package gameserver

import (
	"context"
	"math"
	"testing"
	"time"

	"rf4server/internal/store"
)

func seedDrinkEffect(t *testing.T, s *Server, category string, remaining time.Duration, guid string) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DB.SQL.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := store.AdvanceDrinkBonusesTx(ctx, tx, 1000001, s.drinkRunKey(), time.Now(), false, s.drinkMaxGap()); err != nil {
		t.Fatal(err)
	}
	if err := store.RefreshDrinkBonusTx(ctx, tx, 1000001, category, remaining, guid); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestDrinkSettlementAddsToBaseAndFreezesReceipt(t *testing.T) {
	s, db := newProgressSrv(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	source := newFishGUID()
	seedDrinkEffect(t, s, "wine", 10*time.Minute, source)
	fish := &RolledFish{FishGUID: newFishGUID(), Species: "perch", Grade: 2, WeightG: 642}
	r, err := s.settleCatchOnce(ctx, uid, fish, 1)
	if err != nil {
		t.Fatal(err)
	}
	wantDrink := int64(math.Round(float64(r.baseXP) * .1))
	want := r.baseXP + wantDrink
	if r.totalXP != want || len(r.bonuses) != 1 {
		t.Fatalf("additive XP: %+v want %d", r, want)
	}
	bonus := r.bonuses[0]
	if bonus.Kind != 55 || bonus.GUID != source || bonus.Multiplier != .1 || bonus.XP != wantDrink {
		t.Fatalf("drink breakdown: %+v", bonus)
	}
	entry := s.buildSettle813(ctx, uid, fish, r).Field(8).Val.Field(6).Val.ArgItem(0)
	kind, amount, guid := entry.Field(4), entry.Field(2), entry.Field(1)
	if kind.U32() != 55 || amount.U64() != uint64(wantDrink) || guid.GuidHex() != source {
		t.Fatalf("native Alco bonus: %+v", entry)
	}
	// 酒兴过期或更换服务器后，原鱼仍重放首次入账的分解。
	if _, err := db.SQL.Exec(`UPDATE drink_bonus_effect SET remaining_millis=0 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	fresh := NewServer(s.cfg, s.log, nil)
	fresh.SetDB(db)
	replay, err := fresh.settleCatchOnce(ctx, uid, fish, 1)
	if err != nil || !replay.replayed || replay.totalXP != want || len(replay.bonuses) != 1 || replay.bonuses[0] != bonus {
		t.Fatalf("changed replay: %+v %v", replay, err)
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil || profile.XP != want {
		t.Fatalf("credited twice: %+v %v", profile, err)
	}
}

func TestDrinkSettlementUsesHighestDecayedRateAndOnlyActualUpgrade(t *testing.T) {
	s, db := newProgressSrv(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	// 蜜酒已衰减到5%，红酒10%胜出，不相加。
	seedDrinkEffect(t, s, "mead", 75*time.Second, newFishGUID())
	source := newFishGUID()
	seedDrinkEffect(t, s, "wine", 10*time.Minute, source)
	if _, err := db.SQL.Exec(`UPDATE profile SET level=1,xp=294,membership=0 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	fish := &RolledFish{FishGUID: newFishGUID(), Species: "perch", Grade: 1, WeightG: 32}
	r, err := s.settleCatchOnce(ctx, uid, fish, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.bonuses) != 1 || r.bonuses[0].Multiplier != .1 || r.bonuses[0].GUID != source || r.bonuses[0].XP != 1 {
		t.Fatalf("highest current rate: %+v", r)
	}
	if r.newLevel != 2 || r.levelsGained != 1 || r.totalXp != 300 {
		t.Fatalf("real progression: %+v", r)
	}
	block := s.buildSettle813(ctx, uid, fish, r).Field(8).Val
	if u32Of(block.Field(1).Num) != 1 {
		t.Fatal("real upgrade missing")
	}
	next := &RolledFish{FishGUID: newFishGUID(), Species: "perch", Grade: 1, WeightG: 33}
	r, err = s.settleCatchOnce(ctx, uid, next, 1)
	if err != nil || !r.isRecord || r.levelsGained != 0 || u32Of(s.buildSettle813(ctx, uid, next, r).Field(8).Val.Field(1).Num) != 0 {
		t.Fatalf("record emitted false upgrade: %+v %v", r, err)
	}
}

func TestDrinkSettlementOmitsRoundedZero(t *testing.T) {
	s, db := newProgressSrv(t)
	if _, err := db.EnsureProfile(t.Context(), 1000001); err != nil {
		t.Fatal(err)
	}
	seedDrinkEffect(t, s, "wine", time.Millisecond, newFishGUID())
	r, err := s.settleCatchOnce(t.Context(), 1000001,
		&RolledFish{FishGUID: newFishGUID(), Species: "perch", Grade: 1, WeightG: 1}, 1)
	if err != nil || len(r.bonuses) != 0 || r.totalXP != r.baseXP {
		t.Fatalf("zero-XP bonus must be omitted: %+v %v", r, err)
	}
}

func TestDrinkClockClosesOnlyAfterLastSession(t *testing.T) {
	s, db := newProgressSrv(t)
	register := func(id uint64) *Session {
		sess := newSession(s, id, newFishGUID(), 1000001, nil)
		s.mu.Lock()
		s.sessions[id], s.byToken[sess.Token] = sess, sess
		s.mu.Unlock()
		gsSessionsActive.Add(1)
		t.Cleanup(func() { sess.Close("test_finished") })
		return sess
	}
	first := register(7101)
	second := register(7102)
	seedDrinkEffect(t, s, "wine", 10*time.Minute, newFishGUID())
	first.updateDrinkBonuses(time.Now())
	assertOnline := func(want bool) {
		t.Helper()
		var online bool
		if err := db.SQL.QueryRow(`SELECT online FROM drink_bonus_clock WHERE uid=1000001`).Scan(&online); err != nil || online != want {
			t.Fatalf("online=%v want=%v err=%v", online, want, err)
		}
	}
	assertOnline(true)
	first.Close("test_disconnect")
	assertOnline(true)
	second.Close("test_disconnect")
	assertOnline(false)
	// 关闭后到达的周期更新也不能恢复在线计时。
	first.updateDrinkBonuses(time.Now().Add(time.Hour))
	assertOnline(false)
	register(7103).updateDrinkBonuses(time.Now().Add(time.Hour))
	assertOnline(true)
	s.CloseAll()
	assertOnline(false)
}
