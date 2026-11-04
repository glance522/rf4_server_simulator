package gameserver

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/tunnel"
)

const recoveryUID = int64(1000001)

func recoveryLogin(t *testing.T, srv *Server, sid uint64) *Session {
	t.Helper()
	payload, err := json.Marshal(tunnel.OpenPayload{Token: "local|recovery|1000001|test"})
	if err != nil {
		t.Fatal(err)
	}
	srv.openSession(nil, sid, payload)
	t.Cleanup(srv.CloseAll)
	sess := srv.byID(sid)
	if sess == nil {
		t.Fatal("login did not create a session")
	}
	return sess
}

func completedRecoveryFish(t *testing.T, srv *Server, sess *Session) (*store.FishingState, int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := srv.DB.EnsureProfile(ctx, recoveryUID); err != nil {
		t.Fatal(err)
	}
	st := srv.stateFor(recoveryUID)
	st.mu.Lock()
	st.Pending = &RolledFish{
		FishGUID: "66666666666666666666666666666666",
		Species:  "perch",
		Grade:    2,
		WeightG:  321,
		LengthM:  .24,
		LocLevel: "level_002_kirzah_river",
		Name:     "SavedFish",
	}
	st.CastActive, st.Hooked, st.Resolved = true, true, false
	st.CastTs = time.Now().Add(-20 * time.Second)
	st.mu.Unlock()
	body, handled := srv.fishingEndSettle(&Ctx{Sess: sess, Main: 14, Sub: 4, Log: logging.Discard()})
	if !handled || len(body) == 0 {
		t.Fatal("14/4 did not finish successfully")
	}
	saved, err := srv.DB.LoadFishingState(ctx, recoveryUID)
	if err != nil || saved.Pending == nil || saved.CastActive || !saved.Hooked || saved.Pending.XP == 0 {
		t.Fatalf("settled pending is not recoverable: %+v %v", saved, err)
	}
	profile, err := srv.DB.GetProfile(ctx, recoveryUID)
	if err != nil {
		t.Fatal(err)
	}
	return saved, profile.XP
}

func assertRecoveredCatch(t *testing.T, srv *Server, fish *store.FishingFish, xp int64, count int) {
	t.Helper()
	ctx := context.Background()
	list, err := srv.DB.KeepnetList(ctx, recoveryUID)
	if err != nil || len(list) != count {
		t.Fatalf("keepnet count=%d want=%d err=%v", len(list), count, err)
	}
	if count == 1 && (list[0].EchoGUID != fish.FishGUID || list[0].WeightG != fish.WeightG || list[0].Species != fish.Species) {
		t.Fatalf("recovery changed fish: %+v", list[0])
	}
	profile, err := srv.DB.GetProfile(ctx, recoveryUID)
	if err != nil || profile.XP != xp {
		t.Fatalf("recovery changed XP: got=%+v want=%d err=%v", profile, xp, err)
	}
	saved, err := srv.DB.LoadFishingState(ctx, recoveryUID)
	if err != nil || saved.Pending != nil || !saved.Resolved {
		t.Fatalf("recovery did not consume pending: %+v %v", saved, err)
	}
}

func TestCompletedCatchReconnectAutoKeepsOnce(t *testing.T) {
	srv, db := newDetachTestServer(t)
	first := recoveryLogin(t, srv, 801)
	saved, xp := completedRecoveryFish(t, srv, first)
	hItemCount(&Ctx{Sess: first, Tree: proto.Args524(proto.U32(5))})
	if list, _ := db.KeepnetList(context.Background(), recoveryUID); len(list) != 0 {
		t.Fatal("online inventory read chose keep before the player")
	}
	first.Close("test_disconnect_after_14_4")
	recoveryLogin(t, srv, 802)
	assertRecoveredCatch(t, srv, saved.Pending, xp, 1)
	recoveryLogin(t, srv, 803)
	assertRecoveredCatch(t, srv, saved.Pending, xp, 1)
}

func TestCompletedCatchRecoveryKeepsOriginalTimestamp(t *testing.T) {
	srv, db := newDetachTestServer(t)
	sess := recoveryLogin(t, srv, 811)
	saved, _ := completedRecoveryFish(t, srv, sess)
	var receipt catchReceipt
	raw, _, _ := db.KVGet(context.Background(), fmt.Sprintf("catch-settlement:%d:%s", recoveryUID, saved.Pending.FishGUID))
	if err := json.Unmarshal([]byte(raw), &receipt); err != nil || receipt.CaughtAt <= 0 {
		t.Fatal("14/4 did not persist catch time")
	}
	sess.Close("test_reconnect_timestamp")
	recoveryLogin(t, srv, 812)
	entry, err := db.KeepnetGet(context.Background(), recoveryUID, saved.Pending.FishGUID)
	if err != nil || entry == nil || entry.TS != receipt.CaughtAt {
		t.Fatalf("recovery replaced catch time: entry=%+v err=%v", entry, err)
	}
}

func TestCompletedCatchRecoveryFailureRetries(t *testing.T) {
	srv, db := newDetachTestServer(t)
	sess := recoveryLogin(t, srv, 821)
	saved, xp := completedRecoveryFish(t, srv, sess)
	if _, err := db.SQL.Exec(`CREATE TRIGGER fail_recovered_catch BEFORE INSERT ON catch_log
		BEGIN SELECT RAISE(ABORT,'catch log unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	sess.Close("test_failed_recovery")
	next := recoveryLogin(t, srv, 822)
	persisted, _ := db.LoadFishingState(context.Background(), recoveryUID)
	list, _ := db.KeepnetList(context.Background(), recoveryUID)
	if persisted.Pending == nil || *persisted.Pending != *saved.Pending || len(list) != 0 {
		t.Fatal("failed recovery lost the original fish or partially kept it")
	}
	if _, err := db.SQL.Exec(`DROP TRIGGER fail_recovered_catch`); err != nil {
		t.Fatal(err)
	}
	hItemCount(&Ctx{Sess: next, Tree: proto.Args524(proto.U32(5))})
	assertRecoveredCatch(t, srv, saved.Pending, xp, 1)
}

func TestUnsettledPendingIsNotRewarded(t *testing.T) {
	srv, db := newDetachTestServer(t)
	sess := recoveryLogin(t, srv, 831)
	st := srv.stateFor(recoveryUID)
	st.mu.Lock()
	st.Pending = &RolledFish{FishGUID: "77777777777777777777777777777777", Species: "perch", WeightG: 300}
	st.CastActive, st.Hooked, st.Resolved = false, true, false
	st.mu.Unlock()
	sess.Close("test_without_settlement")
	recoveryLogin(t, srv, 832)
	list, _ := db.KeepnetList(context.Background(), recoveryUID)
	profile, _ := db.GetProfile(context.Background(), recoveryUID)
	if len(list) != 0 || profile.XP != 0 {
		t.Fatal("unsettled pending received a reward or was kept")
	}
	saved, _ := db.LoadFishingState(context.Background(), recoveryUID)
	if saved.Pending == nil {
		t.Fatal("unsettled pending was discarded")
	}
}

func TestFailedBitePushKeepsPendingForRetry(t *testing.T) {
	srv, _ := newDetachTestServer(t)
	sess := newProbeSess(srv)
	fish := &RolledFish{FishGUID: "88888888888888888888888888888888", Species: "perch", WeightG: 300}
	st := srv.stateFor(sess.Userid)
	st.mu.Lock()
	st.Pending = fish
	st.CastActive = true
	st.Resolved = false
	gen := st.BiteGen
	st.mu.Unlock()
	srv.pushPendingBite(sess, st, fish, "", gen)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.PushSent || st.Pending != fish || !st.persist.recoverPending || st.pendingTimer == nil {
		t.Fatalf("failed push did not preserve retry state: sent=%v pending=%v recover=%v timer=%v", st.PushSent, st.Pending == fish, st.persist.recoverPending, st.pendingTimer != nil)
	}
	st.cancelPendingTimerLocked()
}
