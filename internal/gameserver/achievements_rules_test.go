package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

type achievementDTO struct {
	unlocked bool
	progress float32
	claimed  bool
	ts       uint64
}

func achievementsByID(t *testing.T, v *proto.Value) map[int64]achievementDTO {
	t.Helper()
	if v.Kind != proto.KindArgs || v.Code == nil || *v.Code != "114" || len(v.Items) != 130 {
		t.Fatalf("unexpected 3/3 shape: kind=%s code=%v len=%d", v.Kind, v.Code, len(v.Items))
	}
	out := make(map[int64]achievementDTO, len(v.Items))
	for _, item := range v.Items {
		if item.Fields[0].Num == nil {
			t.Fatalf("achievement id missing: %+v", item)
		}
		id := int64(*item.Fields[0].Num)
		d := achievementDTO{}
		if item.Fields[1].Num != nil {
			d.unlocked = *item.Fields[1].Num != 0
		}
		if item.Fields[2].Num != nil {
			d.progress = proto.F32FromBits(uint32(*item.Fields[2].Num))
		}
		if item.Fields[3].Num != nil {
			d.claimed = *item.Fields[3].Num != 0
		}
		if item.Fields[4].Num != nil {
			d.ts = *item.Fields[4].Num
		}
		out[id] = d
	}
	return out
}

// 900「一个好的开始」：上鱼事件一次判断即解锁点亮，第二次上鱼无事发生。
func TestAchievementRule900UnlocksOnCatch(t *testing.T) {
	const uid = int64(1000001)
	env := newDLineTestEnv(t, uid)
	registerAchievementHandlers(env.srv.dispatch)
	ctx := context.Background()

	if _, err := env.db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	before := achievementsByID(t, env.invoke(t, 3, 3, nil))
	if before[900].unlocked || before[900].claimed || before[900].progress != 0 || before[900].ts != 0 {
		t.Fatalf("fresh player achievement 900 = %+v, want all zero", before[900])
	}

	c := env.ctx(14, 4, nil)
	env.srv.evaluateAchievements(c, achievementEvent{Kind: evFishLanded, WeightG: 100})

	got := achievementsByID(t, env.invoke(t, 3, 3, nil))
	if !got[900].unlocked || got[900].progress != 1 || got[900].ts == 0 {
		t.Errorf("achievement 900 should be unlocked on catch: %+v", got[900])
	}
	if !got[900].claimed {
		t.Errorf("achievement 900 should be lit up (claimed=1): %+v", got[900])
	}
	// 小鱼不该解锁 905，抛投成就也不该由上鱼事件触发。
	for _, id := range []int64{101, 102, 103, 151, 200, 250, 300, 905, 906, 911, 1004} {
		if got[id].unlocked || got[id].claimed || got[id].progress != 0 {
			t.Errorf("not-yet-earned achievement %d must stay zero: %+v", id, got[id])
		}
	}

	c2 := env.ctx(14, 4, nil)
	env.srv.evaluateAchievements(c2, achievementEvent{Kind: evFishLanded, WeightG: 200})
	if len(c2.afterResponse) != 0 {
		t.Fatalf("already-unlocked catch registered %d pushes, want 0", len(c2.afterResponse))
	}
	again := achievementsByID(t, env.invoke(t, 3, 3, nil))
	if again[900].ts != got[900].ts {
		t.Error("completion time changed on second catch")
	}
	var rows int
	if err := env.db.SQL.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM player_achievements WHERE uid=?`, uid).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("player_achievements rows=%d want 1", rows)
	}
}

// 905「白鲸」：超过 50 公斤才解锁，恰好 50 公斤不算。
func TestAchievementRule905NeedsOver50kg(t *testing.T) {
	const uid = int64(1000002)
	env := newDLineTestEnv(t, uid)
	registerAchievementHandlers(env.srv.dispatch)
	ctx := context.Background()
	if _, err := env.db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	land := func(g int64) *Ctx {
		c := env.ctx(14, 4, nil)
		env.srv.evaluateAchievements(c, achievementEvent{Kind: evFishLanded, WeightG: g})
		return c
	}

	land(50_000)
	got := achievementsByID(t, env.invoke(t, 3, 3, nil))
	if !got[900].unlocked {
		t.Error("900 should unlock on any fish")
	}
	if got[905].unlocked || got[905].claimed || got[905].progress != 0 {
		t.Errorf("905 must not unlock at exactly 50kg: %+v", got[905])
	}

	c := land(50_001)
	got = achievementsByID(t, env.invoke(t, 3, 3, nil))
	if !got[905].unlocked || !got[905].claimed || got[905].progress != 1 {
		t.Errorf("905 should be unlocked and lit above 50kg: %+v", got[905])
	}
	if len(c.afterResponse) != 1 {
		t.Errorf("905 unlock registered %d pushes, want 1", len(c.afterResponse))
	}
}

// 1000-1004 抛投距离阶梯：一次抛投只解锁已达成的阈值，其余保持未解锁。
func TestAchievementCastDistanceTiers(t *testing.T) {
	const uid = int64(1000003)
	env := newDLineTestEnv(t, uid)
	registerAchievementHandlers(env.srv.dispatch)
	ctx := context.Background()
	if _, err := env.db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	cast := func(m float64) *Ctx {
		c := env.ctx(14, 2, nil)
		env.srv.evaluateAchievements(c, achievementEvent{Kind: evCast, DistanceM: m})
		return c
	}

	c25 := cast(25)
	if len(c25.afterResponse) != 0 {
		t.Error("25m cast should not unlock anything")
	}
	got := achievementsByID(t, env.invoke(t, 3, 3, nil))
	for _, id := range []int64{900, 905, 1000, 1001, 1002, 1003, 1004} {
		if got[id].unlocked || got[id].claimed {
			t.Errorf("achievement %d should stay locked after 25m cast: %+v", id, got[id])
		}
	}

	c50 := cast(50)
	got = achievementsByID(t, env.invoke(t, 3, 3, nil))
	for _, id := range []int64{1000, 1001} {
		if !got[id].unlocked || !got[id].claimed {
			t.Errorf("achievement %d should unlock at 50m: %+v", id, got[id])
		}
	}
	for _, id := range []int64{900, 905, 1002, 1003, 1004} {
		if got[id].unlocked {
			t.Errorf("achievement %d should stay locked at 50m: %+v", id, got[id])
		}
	}
	if len(c50.afterResponse) != 1 {
		t.Errorf("50m cast registered %d push callbacks, want 1", len(c50.afterResponse))
	}

	c120 := cast(120)
	got = achievementsByID(t, env.invoke(t, 3, 3, nil))
	for _, id := range []int64{1000, 1001, 1002, 1003, 1004} {
		if !got[id].unlocked || !got[id].claimed {
			t.Errorf("achievement %d should be unlocked after 120m cast: %+v", id, got[id])
		}
	}
	if len(c120.afterResponse) != 1 {
		t.Errorf("120m cast registered %d push callbacks, want 1", len(c120.afterResponse))
	}
	if c := cast(120); len(c.afterResponse) != 0 {
		t.Error("repeat cast should not push again")
	}
}

// 14/2 抛竿成功后确实发出抛投事件，成就按本次实际距离解锁。
func TestAchievementCastEventFromHandler(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	rod, err := srv.CreateItem(ctx, uid, "tele_10012_tl16", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	fields := make([]proto.Field, 8)
	for i := range fields {
		fields[i] = proto.Field{Op: "U32", Num: ptrU64(0)}
	}
	fields[0] = proto.Field{Op: "U32", Num: ptrU64(uint64(proto.F32Bits(120)))}
	tree := proto.Args524(proto.Guid(rod.GUID), proto.Obj("olieodeoaof", 0, fields...))
	c := &Ctx{Sess: sess, Main: 14, Sub: 2, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()}
	body, handled := hCast(c)
	if !handled || len(body) == 0 {
		t.Fatalf("14/2 failed: handled=%v len=%d", handled, len(body))
	}
	states, err := db.PlayerAchievementStates(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{1000, 1001, 1002, 1003, 1004} {
		if !states[id].Unlocked || !states[id].Claimed {
			t.Errorf("14/2 120m should unlock achievement %d: %+v", id, states[id])
		}
	}
	if len(c.afterResponse) != 1 {
		t.Errorf("14/2 registered %d push callbacks, want 1", len(c.afterResponse))
	}
}

func TestAchievementRulesLeaveNewPlayerLocked(t *testing.T) {
	env := newDLineTestEnv(t, 7)
	registerAchievementHandlers(env.srv.dispatch)

	got := achievementsByID(t, env.invoke(t, 3, 3, nil))
	for _, r := range achievementRules {
		if got[r.ID].unlocked || got[r.ID].progress != 0 || got[r.ID].claimed {
			t.Errorf("fresh player achievement %d should be zero: %+v", r.ID, got[r.ID])
		}
	}
}
