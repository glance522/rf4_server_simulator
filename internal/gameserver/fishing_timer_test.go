package gameserver

import (
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

func timerTestServer(t *testing.T) (*Server, *Session, *FishState) {
	t.Helper()
	db := connectTestDB(t)
	srv := newFishingRecoveryServer(t, db)
	sess := registerBiteTestSession(t, srv, 501, nil)
	srv.tunCache = map[string]float64{"bite_delay_fixed": 20}
	srv.tunAt = time.Now()
	st := srv.stateFor(sess.Userid)
	srv.scheduleBite(sess, st)
	return srv, sess, st
}

// 旧回调已排队时实际重抛，旧回调不能把新杆20秒改为落水保护的6秒。
func TestQueuedOldBiteTimerCannotReplaceNewCastTimer(t *testing.T) {
	srv, sess, st := timerTestServer(t)
	st.mu.Lock()
	old := fishingTimerTicket{st.castGeneration, st.biteTicket}
	st.mu.Unlock()
	srv.mu.Lock()
	started, done := make(chan struct{}), make(chan struct{})
	go func() { close(started); srv.onBiteTimerTicket(sess.Userid, old); close(done) }()
	<-started
	srv.scheduleBite(sess, st)
	st.mu.Lock()
	timer, due, generation := st.biteTimer, st.BiteAt, st.castGeneration
	st.mu.Unlock()
	srv.mu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("旧回调未退出")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.castGeneration == old.generation || generation != st.castGeneration || st.biteTimer != timer || !st.BiteAt.Equal(due) || time.Until(st.BiteAt) < 19*time.Second {
		t.Fatalf("旧回调覆盖新杆timer: generation=%d/%d timerSame=%v due=%v want=%v", st.castGeneration, old.generation, st.biteTimer == timer, st.BiteAt, due)
	}
}

// 同一杆内重新排期也必须拒绝已经启动的旧ticket。
func TestOldBiteTicketCannotReplaceNewDeadline(t *testing.T) {
	srv, sess, st := timerTestServer(t)
	st.mu.Lock()
	old := fishingTimerTicket{st.castGeneration, st.biteTicket}
	due := time.Now().Add(30 * time.Second)
	srv.armBiteTimerLocked(sess.Userid, st, due)
	timer := st.biteTimer
	st.mu.Unlock()
	srv.onBiteTimerTicket(sess.Userid, old)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.biteTimer != timer || !st.BiteAt.Equal(due) {
		t.Fatal("同杆旧ticket清掉了新timer")
	}
}

// 14/12重推必须可取消，且已排队的旧鱼回调不能推送新一杆的鱼。
func TestQueuedOldPendingTimerCannotPushNewCastFish(t *testing.T) {
	srv, sess, st := timerTestServer(t)
	oldFish := &RolledFish{FishGUID: "11111111111111111111111111111111", Species: "perch", WeightG: 100}
	st.mu.Lock()
	st.Pending = oldFish
	st.PushSent = true
	st.mu.Unlock()
	args := proto.Args524(wiregen.MustMakeObj("lhicjpmglbo", map[int]*proto.Value{0: proto.U32(3)}))
	if _, ok := hFishingLeft(&Ctx{Sess: sess, Tree: args, Log: logging.Discard()}); !ok {
		t.Fatal("14/12未处理")
	}
	st.mu.Lock()
	old := fishingTimerTicket{st.castGeneration, st.pendingTicket}
	oldTimer := st.pendingTimer
	st.mu.Unlock()
	if oldTimer == nil {
		t.Fatal("重推timer未保存，无法取消")
	}
	srv.mu.Lock()
	started, done := make(chan struct{}), make(chan struct{})
	go func() { close(started); srv.repushPendingTicket(sess.Userid, old, oldFish); close(done) }()
	<-started
	srv.scheduleBite(sess, st)
	newFish := &RolledFish{FishGUID: "22222222222222222222222222222222", Species: "perch", WeightG: 200}
	st.mu.Lock()
	st.Pending = newFish
	st.PushSent = true
	timer := st.biteTimer
	st.mu.Unlock()
	stoppedByRecast := !oldTimer.Stop()
	srv.mu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("旧重推回调未退出")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if !stoppedByRecast || st.Pending != newFish || !st.PushSent || st.biteTimer != timer || st.pendingTimer != nil {
		t.Fatalf("旧重推干扰新杆: oldStopped=%v sameFish=%v sent=%v timerSame=%v retry=%v", stoppedByRecast, st.Pending == newFish, st.PushSent, st.biteTimer == timer, st.pendingTimer != nil)
	}
	srv.callMu.Lock()
	defer srv.callMu.Unlock()
	if srv.callSeq[sess.Token] != 0 {
		t.Fatal("旧回调又推了新一杆的鱼")
	}
}
