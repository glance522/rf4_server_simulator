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

func completedRecoveryFish(t *testing.T, srv *Server, sess *Session, guids ...string) (*store.FishingState, int64) {
	t.Helper()
	if _, err := srv.DB.EnsureProfile(context.Background(), recoveryUID); err != nil {
		t.Fatal(err)
	}
	st := srv.stateFor(recoveryUID)
	st.mu.Lock()
	guid := "66666666666666666666666666666666"
	if len(guids) > 0 {
		guid = guids[0]
	}
	st.Pending = &RolledFish{FishGUID: guid, Species: "perch", Grade: 2,
		WeightG: 321, LengthM: .24, LocLevel: "level_002_kirzah_river", Name: "SavedFish"}
	st.CastActive, st.Hooked, st.Resolved = true, true, false
	st.CastTs = time.Now().Add(-20 * time.Second)
	st.mu.Unlock()
	body, handled := srv.fishingEndSettle(&Ctx{Sess: sess, Main: 14, Sub: 4, Log: logging.Discard()})
	if !handled || len(body) == 0 || decodeKeepnetBody(t, body).TypeID != 801 {
		t.Fatal("14/4 did not finish successfully")
	}
	saved, err := srv.DB.LoadFishingState(context.Background(), recoveryUID)
	if err != nil || saved.Pending == nil || saved.CastActive || !saved.Hooked || saved.Pending.XP == 0 {
		t.Fatalf("settled pending is not recoverable: %+v %v", saved, err)
	}
	profile, err := srv.DB.GetProfile(context.Background(), recoveryUID)
	if err != nil {
		t.Fatal(err)
	}
	return saved, profile.XP
}

func TestCompletedCatchRecoveryFlagDoesNotAffectNextOnlineFish(t *testing.T) {
	srv, db := newDetachTestServer(t)
	first := recoveryLogin(t, srv, 871)
	if err := db.KVSet(context.Background(), "native-import:1000001:inventory", `{"countsByKind":{"5":{"a":0,"b":0}}}`); err != nil {
		t.Fatal(err)
	}
	saved, _ := completedRecoveryFish(t, srv, first)
	first.Close("test_full_recovery_release")
	sess := recoveryLogin(t, srv, 872)
	if body, handled := srv.fishingRelease(&Ctx{Sess: sess, Tree: proto.Args524(proto.Guid(saved.Pending.FishGUID)), Log: logging.Discard()}); !handled || len(body) == 0 {
		t.Fatal("could not release the waiting catch")
	}
	if err := db.KVSet(context.Background(), "native-import:1000001:inventory", `{"countsByKind":{"5":{"a":1,"b":2}}}`); err != nil {
		t.Fatal(err)
	}
	st := srv.fishingState(recoveryUID)
	if !srv.scheduleBite(sess, st) {
		t.Fatal("released catch kept the next cast blocked")
	}
	fresh, _ := completedRecoveryFish(t, srv, sess, "88888888888888888888888888888888")
	body, _ := hItemCount(&Ctx{Sess: sess, Tree: proto.Args524(proto.U32(5))})
	pending, _ := db.LoadFishingState(context.Background(), recoveryUID)
	if decodeKeepnetBody(t, body).Fields[0].U32() != 0 || pending.Pending == nil || pending.Pending.FishGUID != fresh.Pending.FishGUID {
		t.Fatal("old recovery flag chose keep for a later online catch")
	}
}

func assertRecoveredCatch(t *testing.T, srv *Server, fish *store.FishingFish, xp int64, count int) {
	t.Helper()
	list, err := srv.DB.KeepnetList(context.Background(), recoveryUID)
	if err != nil || len(list) != count {
		t.Fatalf("keepnet count=%d want=%d err=%v", len(list), count, err)
	}
	if count == 1 && (list[0].EchoGUID != fish.FishGUID || list[0].WeightG != fish.WeightG || list[0].Species != fish.Species || list[0].NameZh != fish.Name) {
		t.Fatalf("recovery changed the fish: %+v", list[0])
	}
	profile, err := srv.DB.GetProfile(context.Background(), recoveryUID)
	if err != nil || profile.XP != xp {
		t.Fatalf("recovery changed awarded XP: got=%+v want=%d err=%v", profile, xp, err)
	}
	saved, err := srv.DB.LoadFishingState(context.Background(), recoveryUID)
	if err != nil || saved.Pending != nil || !saved.Resolved {
		t.Fatalf("recovery did not consume pending atomically: %+v %v", saved, err)
	}
}

func TestCompletedCatchNormalChoiceAndReconnectAutoKeep(t *testing.T) {
	srv, db := newDetachTestServer(t)
	first := recoveryLogin(t, srv, 801)
	saved, xp := completedRecoveryFish(t, srv, first)
	// Normal online inventory reads must not choose "keep" on the player's behalf.
	for range 2 {
		body, _ := hItemCount(&Ctx{Sess: first, Tree: proto.Args524(proto.U32(5))})
		if decodeKeepnetBody(t, body).Fields[0].U32() != 0 {
			t.Fatal("online catch was kept before the player chose")
		}
	}
	first.Close("test_disconnect_after_14_4")
	recoveryLogin(t, srv, 802)
	assertRecoveredCatch(t, srv, saved.Pending, xp, 1)
	recoveryLogin(t, srv, 803)
	assertRecoveredCatch(t, srv, saved.Pending, xp, 1)
	var logs, rewards int
	_ = db.SQL.QueryRow(`SELECT COUNT(*) FROM catch_log WHERE uid=?`, recoveryUID).Scan(&logs)
	_ = db.SQL.QueryRow(`SELECT COUNT(*) FROM txn WHERE uid=? AND kind='xp'`, recoveryUID).Scan(&rewards)
	if logs != 1 || rewards != 1 {
		t.Fatalf("reconnect duplicated settlement: logs=%d rewards=%d", logs, rewards)
	}
}

func TestCompletedCatchRestartAutoKeepsOriginalTimestamp(t *testing.T) {
	srv, db := newDetachTestServer(t)
	sess := recoveryLogin(t, srv, 811)
	saved, xp := completedRecoveryFish(t, srv, sess)
	var receipt catchReceipt
	raw, _, _ := db.KVGet(context.Background(), fmt.Sprintf("catch-settlement:%d:%s", recoveryUID, saved.Pending.FishGUID))
	if err := json.Unmarshal([]byte(raw), &receipt); err != nil || receipt.CaughtAt <= 0 {
		t.Fatal("14/4 did not persist the catch time")
	}
	srv.CloseAll()
	stopFishingRecoveryTimers(srv)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(db.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if err := reopened.Migrate(); err != nil {
		t.Fatal(err)
	}
	restarted := newFishingRecoveryServer(t, reopened)
	recoveryLogin(t, restarted, 812)
	assertRecoveredCatch(t, restarted, saved.Pending, xp, 1)
	entry, err := reopened.KeepnetGet(context.Background(), recoveryUID, saved.Pending.FishGUID)
	if err != nil || entry.TS != receipt.CaughtAt {
		t.Fatal("recovery replaced the original catch time with login time")
	}
}

func TestCompletedCatchFullKeepnetBlocksCastUntilSpaceFreed(t *testing.T) {
	srv, db := newDetachTestServer(t)
	first := recoveryLogin(t, srv, 821)
	if err := db.KVSet(context.Background(), "native-import:1000001:inventory", `{"countsByKind":{"5":{"a":1,"b":2}}}`); err != nil {
		t.Fatal(err)
	}
	occupied, _ := seedImportedKeepnetFish(t, srv, recoveryUID, 41, true)
	saved, xp := completedRecoveryFish(t, srv, first)
	first.Close("test_full_keepnet_disconnect")
	srv = newFishingRecoveryServer(t, db)
	sess := recoveryLogin(t, srv, 822)
	st := srv.fishingState(recoveryUID)
	for _, handler := range []func(*Ctx) ([]byte, bool){hRodTake, hCastPrepare, hCast} {
		capture := &ctxReplyCapture{}
		body, handled := handler(&Ctx{Sess: sess, Tree: proto.Args524(proto.Guid("11111111111111111111111111111111")),
			Main: 14, Sub: 2, ExpectResponse: true, replyCapture: capture, Log: logging.Discard()})
		if !handled || body != nil || !capture.failure {
			t.Fatal("full keepnet allowed a new cast to overwrite pending")
		}
	}
	if srv.scheduleBite(sess, st) {
		t.Fatal("direct scheduler overwrote the pending catch")
	}
	persisted, _ := db.LoadFishingState(context.Background(), recoveryUID)
	if persisted.Pending == nil || *persisted.Pending != *saved.Pending || persisted.CastActive {
		t.Fatal("full keepnet changed the waiting fish")
	}
	if _, handled := hItemRemove(&Ctx{Sess: sess, Tree: proto.Args524(proto.Guid(occupied.EchoGUID)), Log: logging.Discard()}); !handled {
		t.Fatal("could not free a keepnet slot")
	}
	assertRecoveredCatch(t, srv, saved.Pending, xp, 1)
	if !srv.pendingCatchAllowsCast(&Ctx{Sess: sess}, st) {
		t.Fatal("cast stayed blocked after the waiting fish was kept")
	}
}

func TestCompletedCatchRecoveryFailurePreservesPendingAndRetries(t *testing.T) {
	srv, db := newDetachTestServer(t)
	sess := recoveryLogin(t, srv, 831)
	saved, xp := completedRecoveryFish(t, srv, sess)
	if _, err := db.SQL.Exec(`CREATE TRIGGER fail_recovered_catch BEFORE INSERT ON catch_log
		BEGIN SELECT RAISE(ABORT,'catch log unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	sess.Close("test_failed_recovery")
	next := recoveryLogin(t, srv, 832)
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

func TestCompletedCatchRecoveryNeverRevivesKeptSoldOrReleasedFish(t *testing.T) {
	for _, decision := range []string{"kept", "sold", "released"} {
		t.Run(decision, func(t *testing.T) {
			srv, db := newDetachTestServer(t)
			sess := recoveryLogin(t, srv, 841)
			saved, _ := completedRecoveryFish(t, srv, sess)
			ctx := &Ctx{Sess: sess, Tree: proto.Args524(proto.Guid(saved.Pending.FishGUID)), Log: logging.Discard()}
			if decision == "released" {
				srv.fishingRelease(ctx)
			} else {
				srv.fishingKeep(ctx)
				if decision == "sold" {
					hMarketSell(ctx)
				}
			}
			profile, _ := db.GetProfile(context.Background(), recoveryUID)
			// Simulate an old snapshot surviving after the decision committed.
			if err := db.SaveFishingState(context.Background(), saved); err != nil {
				t.Fatal(err)
			}
			sess.Close("test_stale_snapshot")
			restarted := newFishingRecoveryServer(t, db)
			recoveryLogin(t, restarted, 842)
			count := 0
			if decision == "kept" {
				count = 1
			}
			assertRecoveredCatch(t, restarted, saved.Pending, profile.XP, count)
		})
	}
}

func TestUnsettledPendingIsNeverAutoRewardedOrKept(t *testing.T) {
	srv, db := newDetachTestServer(t)
	sess := recoveryLogin(t, srv, 851)
	st := srv.fishingState(recoveryUID)
	st.mu.Lock()
	st.Pending = &RolledFish{FishGUID: "77777777777777777777777777777777", Species: "perch", WeightG: 300}
	st.CastActive, st.Hooked, st.Resolved = false, true, false
	st.mu.Unlock()
	sess.Close("test_without_settlement")
	recoveryLogin(t, srv, 852)
	list, _ := db.KeepnetList(context.Background(), recoveryUID)
	profile, _ := db.GetProfile(context.Background(), recoveryUID)
	if len(list) != 0 || profile.XP != 0 {
		t.Fatal("unsettled pending received a reward or was automatically kept")
	}
	saved, _ := db.LoadFishingState(context.Background(), recoveryUID)
	if saved.Pending == nil {
		t.Fatal("unsettled pending was discarded")
	}
}

func TestKeepnetCapacityUsesCurrentMembershipForManualAndAutomaticKeep(t *testing.T) {
	for _, premium := range []string{"active", "expired", "permanent"} {
		t.Run(premium, func(t *testing.T) {
			srv, db := newDetachTestServer(t)
			sess := recoveryLogin(t, srv, 861)
			if err := db.KVSet(context.Background(), "native-import:1000001:inventory", `{"countsByKind":{"5":{"a":1,"b":2}}}`); err != nil {
				t.Fatal(err)
			}
			kind, expires := 1, time.Now().Unix()+3600
			if premium == "expired" {
				expires = time.Now().Unix() - 1
			} else if premium == "permanent" {
				kind, expires = 2, 0
			}
			if _, err := db.SQL.Exec(`UPDATE profile SET membership=?,membership_exp=? WHERE uid=?`, kind, expires, recoveryUID); err != nil {
				t.Fatal(err)
			}
			seedImportedKeepnetFish(t, srv, recoveryUID, 51, true)
			saved, _ := completedRecoveryFish(t, srv, sess)
			capture := &ctxReplyCapture{}
			srv.fishingKeep(&Ctx{Sess: sess, Tree: proto.Args524(proto.Guid(saved.Pending.FishGUID)), ExpectResponse: true,
				replyCapture: capture, Log: logging.Discard()})
			list, _ := db.KeepnetList(context.Background(), recoveryUID)
			if premium == "expired" {
				if !capture.failure || len(list) != 1 {
					t.Fatal("expired membership bypassed the normal keepnet limit")
				}
				sess.Close("test_expired_membership")
				recoveryLogin(t, srv, 862)
				list, _ = db.KeepnetList(context.Background(), recoveryUID)
				pending, _ := db.LoadFishingState(context.Background(), recoveryUID)
				if len(list) != 1 || pending.Pending == nil {
					t.Fatal("automatic keep bypassed the expired membership limit")
				}
			} else if capture.failure || len(list) != 2 {
				t.Fatal("active membership lost the imported premium capacity")
			}
		})
	}
}
