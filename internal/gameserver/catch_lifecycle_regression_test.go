package gameserver

import (
	"strings"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func TestReleaseRejectsStaleFishGUID(t *testing.T) {
	for _, withRod := range []bool{false, true} {
		t.Run(map[bool]string{false: "fish_only", true: "rod_and_fish"}[withRod], func(t *testing.T) {
			s, db := newRigTestServer(t)
			sess := newProbeSess(s)
			if _, err := db.EnsureProfile(t.Context(), sess.Userid); err != nil {
				t.Fatal(err)
			}
			rod := "11111111111111111111111111111111"
			st := s.stateFor(sess.Userid)
			fish := &RolledFish{FishGUID: strings.Repeat("5", 32), Species: "perch", XP: 100}
			st.mu.Lock()
			st.Pending, st.Hooked = fish, true
			st.mu.Unlock()
			args := []*proto.Value{proto.Guid(strings.Repeat("6", 32))}
			if withRod {
				args = append([]*proto.Value{proto.Guid(rod)}, args...)
			}
			capture := &ctxReplyCapture{}
			body, handled := s.fishingRelease(&Ctx{Sess: sess, Tree: proto.Args524(args...), ExpectResponse: true,
				replyCapture: capture, Log: logging.Discard()})
			if !handled || body != nil || !capture.failure || st.Pending != fish {
				t.Fatal("stale release changed the current catch or reported success")
			}
			profile, err := db.GetProfile(t.Context(), sess.Userid)
			if err != nil || profile.XP != 0 {
				t.Fatalf("stale release awarded experience: %+v %v", profile, err)
			}
		})
	}
}

func TestCatchAndReleaseLevelUpWithoutGoldOrGiftMail(t *testing.T) {
	s, db := newRigTestServer(t)
	sess := newProbeSess(s)
	uid := sess.Userid
	if _, err := db.EnsureProfile(t.Context(), uid); err != nil {
		t.Fatal(err)
	}
	// 旧存档即使保留开启值，也不能再触发已移除的奖励。
	if err := db.TunableSet(t.Context(), "level_rewards_enabled", "1", "test"); err != nil {
		t.Fatal(err)
	}
	for _, release := range []bool{false, true} {
		if _, err := db.SQL.Exec(`UPDATE profile SET level=9,xp=?,gold=1234 WHERE uid=?`, baseXp(10)-1, uid); err != nil {
			t.Fatal(err)
		}
		fish := &RolledFish{FishGUID: strings.Repeat("7", 32), Species: "perch", Grade: 2, WeightG: 642}
		if release {
			result, replayed, err := s.releaseCatchOnce(t.Context(), uid, fish.FishGUID, 10)
			if err != nil || replayed || result.Level < 10 {
				t.Fatalf("release level-up failed: %+v %v", result, err)
			}
		} else {
			result, err := s.settleCatchOnce(t.Context(), uid, fish, 1)
			if err != nil || result.newLevel < 10 {
				t.Fatalf("catch level-up failed: %+v %v", result, err)
			}
		}
		profile, err := db.GetProfile(t.Context(), uid)
		if err != nil || profile.Gold != 1234 {
			t.Fatalf("level-up changed gold: %+v %v", profile, err)
		}
		var count int
		if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM mail WHERE uid=?`, uid).Scan(&count); err != nil || count != 0 {
			t.Fatalf("level-up sent gift mail: %d %v", count, err)
		}
	}
}
