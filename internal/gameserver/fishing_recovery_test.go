package gameserver

import (
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func TestCompletedPendingCatchBlocksNewCastUntilHandled(t *testing.T) {
	s, _ := newProgressSrv(t)
	sess := newProbeSess(s)
	st := s.stateFor(sess.Userid)
	fish := &RolledFish{FishGUID: newFishGUID(), Species: "perch", WeightG: 200}
	st.mu.Lock()
	st.Pending, st.GearGuid = fish, "11111111111111111111111111111111"
	st.Hooked, st.CastActive, st.Resolved = true, false, false
	generation := st.castGeneration
	st.mu.Unlock()
	capture := &ctxReplyCapture{}
	body, handled := hCastPrepare(&Ctx{Sess: sess, Main: 14, Sub: 1, ExpectResponse: true,
		Tree: rigRequestTree(t, proto.Guid("22222222222222222222222222222222")), Log: logging.Discard(), replyCapture: capture})
	_, present, _, failure := capture.snapshot()
	if !handled || len(body) != 0 || !present || !failure {
		t.Fatal("new cast did not report the pending catch")
	}
	if s.scheduleBite(sess, st) {
		t.Fatal("new timer replaced an unhandled completed catch")
	}
	st.mu.Lock()
	preserved := st.Pending == fish && st.GearGuid == "11111111111111111111111111111111" && st.castGeneration == generation && st.biteTimer == nil
	st.Pending = nil
	st.Hooked = false
	st.Resolved = true
	st.mu.Unlock()
	if !preserved {
		t.Fatal("cast attempt changed the completed catch")
	}
	if !s.pendingCatchAllowsCast(&Ctx{Sess: sess}, st) {
		t.Fatal("handled catch kept blocking the next cast")
	}
}
