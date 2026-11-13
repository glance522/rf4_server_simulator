package gameserver

import (
	"strings"
	"testing"
	"time"

	"rf4server/internal/proto"
)

// escapeFrame 构造 14/12「鱼跑了」帧：f0 放在 lhicjpmglbo 的第 0 字段。
func escapeFrameForTest(f0 uint64) *Ctx {
	return miscCtx(nil, 14, 12, proto.Args524(proto.Obj("lhicjpmglbo", 24,
		proto.Field{Op: "U32", Num: ptrU64(f0)})))
}

// 一级菜单要列出四个功能，且 3/4 的开关各自独立落库。
func TestGearAssistantHelpListsFishingToggles(t *testing.T) {
	s, db := newRigTestServer(t)
	sess := newProbeSess(s)
	uid := sess.Userid

	sendGearAssistantForTest(t, s, sess, "帮助")
	mails, err := db.MailList(t.Context(), uid, false)
	if err != nil || len(mails) == 0 {
		t.Fatalf("no assistant reply: %v", err)
	}
	help := mails[0].Text
	for _, want := range []string{"1  【满红不爆】", "2  【耐力播报】", "3  【永不脱钩】", "4  【简易手杆】"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help menu missing %q: %q", want, help)
		}
	}

	// 3 进二级、y 开启：只写 noescape 的键，不碰另外三个。
	sendGearAssistantForTest(t, s, sess, "3")
	sendGearAssistantForTest(t, s, sess, "y")
	assertKVFlag(t, s, uid, "noescape", true)
	assertKVFlag(t, s, uid, "instantbite", false)
	assertKVFlag(t, s, uid, "overload", false)
	assertKVFlag(t, s, uid, "stamina", false)

	// b 回一级再开 4，两个开关互不影响。
	sendGearAssistantForTest(t, s, sess, "b")
	sendGearAssistantForTest(t, s, sess, "4 开")
	assertKVFlag(t, s, uid, "noescape", true)
	assertKVFlag(t, s, uid, "instantbite", true)

	// 关掉 3 后 4 仍然开着。
	sendGearAssistantForTest(t, s, sess, "b")
	sendGearAssistantForTest(t, s, sess, "3")
	sendGearAssistantForTest(t, s, sess, "n")
	assertKVFlag(t, s, uid, "noescape", false)
	assertKVFlag(t, s, uid, "instantbite", true)
}

func assertKVFlag(t *testing.T, s *Server, uid int64, feature string, want bool) {
	t.Helper()
	got, err := s.gearReadFlag(t.Context(), uid, feature)
	if err != nil {
		t.Fatalf("read %s: %v", feature, err)
	}
	if got != want {
		t.Fatalf("%s flag = %v, want %v", feature, got, want)
	}
}

// 【永不脱钩】关闭时 14/12 的跑鱼帧照旧清鱼；开启后同一尾鱼被原地重推。
func TestGearNoEscapeRepushesSameFish(t *testing.T) {
	s, _ := newRigTestServer(t)
	sess := newProbeSess(s)
	uid := sess.Userid
	rod, err := s.CreateItem(t.Context(), uid, "spin_10001_S60L", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	st := s.selectFishingRod(uid, rod.GUID)
	fish := func() *RolledFish {
		return &RolledFish{FishGUID: newFishGUID(), Species: "perch", WeightG: 4000}
	}
	arm := func(f *RolledFish) {
		st.mu.Lock()
		st.GearGuid, st.Pending, st.CastActive, st.Hooked = rod.GUID, f, true, true
		// InWater=false：本用例只看 14/12 的跑鱼分支，不让它顺带重排咬钩。
		st.InWater = false
		st.mu.Unlock()
	}
	left := escapeFrameForTest(1)
	left.Sess = sess

	// 开关关闭：原行为，鱼被清掉且不重推。
	arm(fish())
	hFishingLeft(left)
	st.mu.Lock()
	escaped := st.Pending == nil && !st.Hooked && st.pendingTimer == nil
	st.mu.Unlock()
	if !escaped {
		t.Fatal("no-escape off must keep the default escape path")
	}

	sendGearAssistantForTest(t, s, sess, "3 开")
	pendingFish := fish()
	arm(pendingFish)
	hFishingLeft(left)
	st.mu.Lock()
	replayed := st.Pending
	hooked := st.Hooked
	timer := st.pendingTimer
	retries := st.noEscapeRepush
	st.mu.Unlock()
	if replayed == nil || replayed.FishGUID != pendingFish.FishGUID {
		t.Fatalf("same fish must be kept for replay: %+v", replayed)
	}
	if hooked {
		t.Fatal("replayed fish must leave the hooked state until the client confirms again")
	}
	if timer == nil || retries != 1 {
		t.Fatalf("expected a pending replay timer: timer=%v retries=%d", timer, retries)
	}

	// 连跑三次都要原地重推，且不消耗鱼、不记跑鱼。
	for i := 2; i <= 3; i++ {
		st.mu.Lock()
		st.Hooked = true
		st.CastActive = true
		st.mu.Unlock()
		hFishingLeft(left)
		st.mu.Lock()
		still := st.Pending
		n := st.noEscapeRepush
		st.mu.Unlock()
		if still == nil || still.FishGUID != pendingFish.FishGUID || n != i {
			t.Fatalf("replay #%d kept the fish: pending=%+v retries=%d", i, still, n)
		}
	}

	// 重推次数到上限后落回正常跑鱼，避免客户端不兼容时无限重推。
	st.mu.Lock()
	st.noEscapeRepush = maxNoEscapeRepush
	st.Hooked = true
	st.CastActive = true
	st.mu.Unlock()
	hFishingLeft(left)
	st.mu.Lock()
	gone := st.Pending == nil
	st.mu.Unlock()
	if !gone {
		t.Fatal("must fall back to the normal escape path after the replay cap")
	}
}

// 【永不脱钩】开启后嘬饵与钩型保持失败都不再否决中钩。
func TestGearNoEscapeForcesHookConfirmation(t *testing.T) {
	s, _ := newRigTestServer(t)
	sess := newProbeSess(s)
	uid := sess.Userid
	rod, err := s.CreateItem(t.Context(), uid, "spin_10001_S60L", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	s.tunCache = map[string]float64{tunableHookNibbleEnabled: 1}
	s.tunAt = time.Now()
	st := s.selectFishingRod(uid, rod.GUID)
	fish := &RolledFish{FishGUID: newFishGUID(), Species: "perch", WeightG: 4000,
		BiteQuality: "nibble", HookHoldP: 0} // 保持率 0：不开开关必判嘬饵
	armFish := func() {
		st.mu.Lock()
		st.GearGuid, st.Pending, st.CastActive = rod.GUID, fish, true
		st.Hooked = false
		st.mu.Unlock()
	}
	pull := func() {
		ctx := miscCtx(sess, 14, 11, nil)
		ctx.ExpectResponse = true
		if _, ok := hFishingHooked(ctx); !ok {
			t.Fatal("14/11 not handled")
		}
	}
	hookedNow := func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.Hooked && st.Pending != nil && st.Pending.FishGUID == fish.FishGUID
	}

	// 关闭：嘬饵鱼不中钩，鱼被放走。
	armFish()
	pull()
	if hookedNow() {
		t.Fatal("nibble fish must not be hooked while the toggle is off")
	}

	// 开启：同一尾鱼直接进入搏鱼态。
	sendGearAssistantForTest(t, s, sess, "3 开")
	armFish()
	pull()
	if !hookedNow() {
		t.Fatal("no-escape must confirm the hook even for a nibble bite")
	}
}

// 【简易手杆】只把 f19 固定为 1，不再缩短咬钩延迟；提杆 100% 确认中钩。
func TestGearSimpleRodKeepsBiteDelay(t *testing.T) {
	srv, sess, st := timerTestServer(t) // bite_delay_fixed=20

	sendGearAssistantForTest(t, srv, sess, "4 开")
	if !srv.scheduleBite(sess, st) {
		t.Fatal("schedule bite failed")
	}
	st.mu.Lock()
	due := st.BiteAt
	st.mu.Unlock()
	if wait := time.Until(due); wait < 15*time.Second {
		t.Fatalf("simple rod must not shorten the bite delay: %v", wait)
	}
}

// 【简易手杆】开启后嘬饵与钩型保持失败都不再否决中钩（中鱼指示器提示后提杆 100% 上鱼）。
func TestGearSimpleRodConfirmsHook(t *testing.T) {
	s, _ := newRigTestServer(t)
	sess := newProbeSess(s)
	uid := sess.Userid
	rod, err := s.CreateItem(t.Context(), uid, "spin_10001_S60L", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	s.tunCache = map[string]float64{tunableHookNibbleEnabled: 1}
	s.tunAt = time.Now()
	st := s.selectFishingRod(uid, rod.GUID)
	fish := &RolledFish{FishGUID: newFishGUID(), Species: "perch", WeightG: 4000,
		BiteQuality: "nibble", HookHoldP: 0} // 保持率 0：不开开关必判嘬饵
	armFish := func() {
		st.mu.Lock()
		st.GearGuid, st.Pending, st.CastActive = rod.GUID, fish, true
		st.Hooked = false
		st.mu.Unlock()
	}
	pull := func() {
		ctx := miscCtx(sess, 14, 11, nil)
		ctx.ExpectResponse = true
		if _, ok := hFishingHooked(ctx); !ok {
			t.Fatal("14/11 not handled")
		}
	}
	hookedNow := func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.Hooked && st.Pending != nil && st.Pending.FishGUID == fish.FishGUID
	}

	// 关闭：嘬饵鱼不中钩，鱼被放走。
	armFish()
	pull()
	if hookedNow() {
		t.Fatal("nibble fish must not be hooked while the toggle is off")
	}

	// 开启：同一尾鱼直接进入搏鱼态。
	sendGearAssistantForTest(t, s, sess, "4 开")
	armFish()
	pull()
	if !hookedNow() {
		t.Fatal("simple rod must confirm the hook even for a nibble bite")
	}
}
