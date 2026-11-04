package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func TestFishingReleaseRejectsOtherFishAndRewardsOnce(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.Pending = &RolledFish{FishGUID: "30303030303030303030303030303030", Species: "perch", Grade: 2, WeightG: 321, XP: 100}
	st.mu.Unlock()

	wrong := &ctxReplyCapture{}
	if body, ok := srv.fishingRelease(&Ctx{Sess: sess, Main: 14, Sub: 6, ExpectResponse: true,
		Tree: proto.Args524(proto.Guid("40404040404040404040404040404040")), replyCapture: wrong, Log: logging.Discard()}); !ok || body != nil || !wrong.failure {
		t.Fatal("mismatched release GUID was not rejected")
	}
	st.mu.Lock()
	if st.Pending == nil {
		st.mu.Unlock()
		t.Fatal("mismatched release consumed pending fish")
	}
	st.mu.Unlock()

	body, ok := srv.fishingRelease(&Ctx{Sess: sess, Main: 14, Sub: 6, ExpectResponse: true,
		Tree: proto.Args524(proto.Guid("30303030303030303030303030303030")), Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatal("valid release failed")
	}
	profile, _ := db.GetProfile(ctx, uid)
	if profile.XP != 50 {
		t.Fatalf("release XP=%d want 50", profile.XP)
	}
	if _, ok := srv.fishingRelease(&Ctx{Sess: sess, Main: 14, Sub: 6, ExpectResponse: true,
		Tree: proto.Args524(proto.Guid("30303030303030303030303030303030")), Log: logging.Discard()}); !ok {
		t.Fatal("release replay was not handled")
	}
	profile, _ = db.GetProfile(ctx, uid)
	if profile.XP != 50 {
		t.Fatalf("release replay awarded XP twice: %d", profile.XP)
	}
}

func TestFishingReleaseFailureDoesNotNameOrReward(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	const fishGUID = "50505050505050505050505050505050"
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.Pending = &RolledFish{FishGUID: fishGUID, Species: "perch", Grade: superTrophyGrade, WeightG: 321, XP: 100}
	st.mu.Unlock()
	if _, err := db.SQL.Exec(`CREATE TRIGGER fail_release_receipt BEFORE INSERT ON kv
		WHEN NEW.k LIKE 'catch-release:%' BEGIN SELECT RAISE(ABORT,'release failure'); END`); err != nil {
		t.Fatal(err)
	}
	capture := &ctxReplyCapture{}
	body, handled := srv.fishingRelease(&Ctx{Sess: sess, Main: 14, Sub: 6, ExpectResponse: true,
		Tree:         proto.Args524(proto.Guid("60606060606060606060606060606060"), proto.Guid(fishGUID), proto.StrVal("BlueCrown")),
		replyCapture: capture, Log: logging.Discard()})
	if !handled || body != nil || !capture.failure {
		t.Fatal("release failure reported success")
	}
	profile, _ := db.GetProfile(ctx, uid)
	st.mu.Lock()
	pending := st.Pending
	st.mu.Unlock()
	if profile.XP != 0 || pending == nil || pending.FishGUID != fishGUID || db.NamedFishIsNamed(ctx, fishGUID) {
		t.Fatal("failed release partially changed profile, state, or naming")
	}
	if _, err := db.SQL.Exec(`DROP TRIGGER fail_release_receipt`); err != nil {
		t.Fatal(err)
	}
	body, handled = srv.fishingRelease(&Ctx{Sess: sess, Main: 14, Sub: 6, ExpectResponse: true,
		Tree: proto.Args524(proto.Guid("60606060606060606060606060606060"), proto.Guid(fishGUID), proto.StrVal("BlueCrown")), Log: logging.Discard()})
	if !handled || len(body) == 0 {
		t.Fatal("release retry failed")
	}
	profile, _ = db.GetProfile(ctx, uid)
	st.mu.Lock()
	pending = st.Pending
	st.mu.Unlock()
	if profile.XP != 50 || pending != nil || !db.NamedFishIsNamed(ctx, fishGUID) {
		t.Fatalf("successful release state mismatch: xp=%d pending=%+v named=%v", profile.XP, pending, db.NamedFishIsNamed(ctx, fishGUID))
	}
}
