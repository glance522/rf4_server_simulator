package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

func TestAchievementPushProbeBodiesPassGuard(t *testing.T) {
	obj := achievementStatePushObject(101)
	if obj.Class != "efnkohdcnld" {
		t.Fatalf("probe object class=%s want efnkohdcnld", obj.Class)
	}
	if obj.Fields[3].Num == nil || *obj.Fields[3].Num != 1 {
		t.Fatalf("probe object must be claimed=1: %+v", obj.Fields[3])
	}
	for _, tc := range []struct{ main, sub uint8 }{{3, 3}, {2, 8}} {
		body := proto.Args(proto.S("114"), obj)
		if reason := checkPushArgsStrict(tc.main, tc.sub, body); reason != "" {
			t.Fatalf("%d/%d probe body rejected: %s", tc.main, tc.sub, reason)
		}
		raw, err := proto.EncodeValue(body, wiregen.Table())
		if err != nil {
			t.Fatalf("%d/%d encode: %v", tc.main, tc.sub, err)
		}
		if len(raw) == 0 || raw[0] != 0x03 {
			t.Fatalf("%d/%d probe body is not top-level args", tc.main, tc.sub)
		}
	}
}

func TestAchievementPushProbeVariantsAreKnown(t *testing.T) {
	want := map[string]bool{"3_3": false, "2_8": false, "2_9": false, "chat": false}
	for _, v := range achievementProbeVariants {
		if _, ok := want[v]; !ok {
			t.Fatalf("unexpected variant %q", v)
		}
		want[v] = true
	}
	for v, seen := range want {
		if !seen {
			t.Errorf("missing variant %q", v)
		}
	}
}

// 打开成就面板是纯查询，既不写状态也不推送。
func TestAchievementQueryDoesNotPush(t *testing.T) {
	const uid = int64(1000001)
	env := newDLineTestEnv(t, uid)
	registerAchievementHandlers(env.srv.dispatch)
	ctx := context.Background()

	if _, err := env.db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	sess, _ := newTunnelProbe(t, env.srv)
	h := env.srv.dispatch.lookup(3, 3)
	if h == nil {
		t.Fatal("3/3 handler not registered")
	}
	c := &Ctx{Sess: sess, Main: 3, Sub: 3, ExpectResponse: true, Log: logging.Discard()}
	body, handled := h(c)
	if !handled || len(body) == 0 {
		t.Fatalf("3/3 failed: handled=%v len=%d", handled, len(body))
	}
	if len(c.afterResponse) != 0 {
		t.Fatalf("opening achievement panel registered %d pushes, want 0", len(c.afterResponse))
	}
	var rows int
	if err := env.db.SQL.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM player_achievements WHERE uid=?`, uid).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("3/3 query wrote %d achievement rows, want 0", rows)
	}
}

// 上鱼：未解锁则解锁并推送 2/8，已解锁则无事发生。
func TestAchievementUnlockOnCatch(t *testing.T) {
	const uid = int64(1000001)
	env := newDLineTestEnv(t, uid)
	ctx := context.Background()

	if _, err := env.db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	sess, gc := newTunnelProbe(t, env.srv)
	c := &Ctx{Sess: sess, Main: 14, Sub: 4, ExpectResponse: true, Log: logging.Discard()}
	env.srv.evaluateAchievements(c, achievementEvent{Kind: evFishLanded, WeightG: 100})
	if len(c.afterResponse) != 1 {
		t.Fatalf("want 1 after-response callback, got %d", len(c.afterResponse))
	}
	c.afterResponse[0]()
	push := awaitPushEnvelope(t, gc)
	if push.Main != 2 || push.Sub != 8 {
		t.Fatalf("achievement unlock push = %d/%d, want 2/8", push.Main, push.Sub)
	}
	dec, _, err := proto.DecodeValue(push.Body, 0, wiregen.Table())
	if err != nil {
		t.Fatalf("decode push body: %v", err)
	}
	if dec.Kind != proto.KindArgs || dec.Code == nil || *dec.Code != "114" || len(dec.Items) != 1 {
		t.Fatalf("unexpected push shape: kind=%s code=%v len=%d", dec.Kind, dec.Code, len(dec.Items))
	}
	obj := dec.Items[0]
	if obj.Class != "efnkohdcnld" {
		t.Fatalf("push object class=%s want efnkohdcnld", obj.Class)
	}
	if got := obj.Fields[0].Num; got == nil || *got != 900 {
		t.Fatalf("push achievement id=%v want 900", got)
	}
	if got := obj.Fields[3].Num; got == nil || *got != 1 {
		t.Fatalf("push claimed=%v want 1 (client lights up on this bit)", got)
	}

	c2 := &Ctx{Sess: sess, Main: 14, Sub: 4, ExpectResponse: true, Log: logging.Discard()}
	env.srv.evaluateAchievements(c2, achievementEvent{Kind: evFishLanded, WeightG: 200})
	if len(c2.afterResponse) != 0 {
		t.Fatalf("already-unlocked catch registered %d pushes, want 0", len(c2.afterResponse))
	}
}

// 排队函数：空列表不注册回调，非空列表在应答后推 2/8。
func TestQueueAchievementUnlockPushes(t *testing.T) {
	env := newDLineTestEnv(t, 1000001)
	sess, gc := newTunnelProbe(t, env.srv)
	newCtx := func() *Ctx {
		return &Ctx{Sess: sess, Main: 14, Sub: 4, ExpectResponse: true, Log: logging.Discard()}
	}

	empty := newCtx()
	env.srv.queueAchievementUnlockPushes(empty, nil)
	if len(empty.afterResponse) != 0 {
		t.Fatalf("empty ids registered %d callbacks", len(empty.afterResponse))
	}

	c := newCtx()
	env.srv.queueAchievementUnlockPushes(c, []int64{103})
	if len(c.afterResponse) != 1 {
		t.Fatalf("want 1 callback, got %d", len(c.afterResponse))
	}
	c.afterResponse[0]()
	push := awaitPushEnvelope(t, gc)
	if push.Main != 2 || push.Sub != 8 {
		t.Fatalf("push = %d/%d, want 2/8", push.Main, push.Sub)
	}
	dec, _, err := proto.DecodeValue(push.Body, 0, wiregen.Table())
	if err != nil {
		t.Fatalf("decode push: %v", err)
	}
	if dec.Kind != proto.KindArgs || dec.Code == nil || *dec.Code != "114" || len(dec.Items) != 1 {
		t.Fatalf("unexpected push shape: kind=%s code=%v len=%d", dec.Kind, dec.Code, len(dec.Items))
	}
	if got := dec.Items[0].Fields[0].Num; got == nil || *got != 103 {
		t.Fatalf("push achievement id=%v want 103", got)
	}
}
