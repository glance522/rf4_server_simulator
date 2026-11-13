package gameserver

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// huntWeightsTestServer 构造只带 tunables 缓存的 Server，用于纯函数级权重断言。
func huntWeightsTestServer(tunables map[string]float64) *Server {
	m := map[string]float64{"grade_ratio_enabled": 0, "fish_hunt_enabled": 1, "two_stage_hunt_enabled": 0}
	for k, v := range tunables {
		m[k] = v
	}
	return &Server{tunCache: m, tunAt: time.Now()}
}

func TestFishHuntStageWeights(t *testing.T) {
	tests := []struct {
		stage          int
		w1, w2, w3, w4 float64
	}{
		{fishHuntDaily, 74.7, 24, 1, .3},
		{fishHuntGather, 54.5, 43, 2, .5},
		{fishHuntActive, 30, 61.2, 6.8, 2},
		{fishHuntGiant, 10, 64, 20, 6},
	}
	s := huntWeightsTestServer(nil)
	for _, mode := range []int{fishHuntModeFreshwater, fishHuntModeMarine, fishHuntModeLure} {
		for _, tc := range tests {
			w1, w2, w3, w4, force := s.fishHuntWeights(mode, tc.stage, &fishHuntState{}, 0, false)
			if force || math.Abs(w1-tc.w1) > 1e-9 || math.Abs(w2-tc.w2) > 1e-9 ||
				math.Abs(w3-tc.w3) > 1e-9 || math.Abs(w4-tc.w4) > 1e-9 {
				t.Fatalf("mode=%d stage=%d weights=(%.1f,%.1f,%.1f,%.1f) force=%v",
					mode, tc.stage, w1, w2, w3, w4, force)
			}
		}
	}
}

func TestTwoStageFreshwaterWeights(t *testing.T) {
	s := huntWeightsTestServer(map[string]float64{"two_stage_hunt_enabled": 1})
	tests := []struct {
		name           string
		stage          int
		w1, w2, w3, w4 float64
	}{
		{"daily", fishHuntDaily, 10, 86.5, 3, .5},
		{"baited-gather", fishHuntGather, 1, 92, 6, 1},
		{"baited-active", fishHuntActive, 1, 92, 6, 1},
		{"baited-giant", fishHuntGiant, 1, 92, 6, 1},
	}
	for _, tc := range tests {
		w1, w2, w3, w4, force := s.fishHuntWeights(fishHuntModeFreshwater, tc.stage, &fishHuntState{}, 0, false)
		if force || math.Abs(w1-tc.w1) > 1e-9 || math.Abs(w2-tc.w2) > 1e-9 ||
			math.Abs(w3-tc.w3) > 1e-9 || math.Abs(w4-tc.w4) > 1e-9 {
			t.Fatalf("%s weights=(%.2f,%.2f,%.2f,%.2f) force=%v", tc.name, w1, w2, w3, w4, force)
		}
	}
	// 两档制下海钓/路亚必须仍是原四阶段表。
	s4 := [4][4]float64{{74.7, 24, 1, .3}, {54.5, 43, 2, .5}, {30, 61.2, 6.8, 2}, {10, 64, 20, 6}}
	for i, stage := range []int{fishHuntDaily, fishHuntGather, fishHuntActive, fishHuntGiant} {
		w1, w2, w3, w4, _ := s.fishHuntWeights(fishHuntModeMarine, stage, &fishHuntState{}, 0, false)
		want := s4[i]
		if math.Abs(w1-want[0]) > 1e-9 || math.Abs(w2-want[1]) > 1e-9 ||
			math.Abs(w3-want[2]) > 1e-9 || math.Abs(w4-want[3]) > 1e-9 {
			t.Fatalf("两档制不得改写海钓四阶段表 stage=%d: (%.1f,%.1f,%.1f,%.1f)", stage, w1, w2, w3, w4)
		}
	}
}

func TestTwoStageCustomWeightsNormalize(t *testing.T) {
	s := huntWeightsTestServer(map[string]float64{
		"two_stage_hunt_enabled":    1,
		"two_stage_daily_below":     20,
		"two_stage_daily_qualified": 140,
		"two_stage_daily_trophy":    30,
		"two_stage_daily_super":     10,
	})
	w1, w2, w3, w4, _ := s.fishHuntWeights(fishHuntModeFreshwater, fishHuntDaily, &fishHuntState{}, 0, false)
	want := [4]float64{10, 70, 15, 5}
	got := [4]float64{w1, w2, w3, w4}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Fatalf("自定义权重应归一到总和 100%%: got=%.2f want=%.2f", got, want)
		}
	}
}

func TestHuntStateEnabledCombinesFlags(t *testing.T) {
	s, _ := newRigTestServer(t)
	cases := []struct {
		fishHunt, twoStage float64
		want               bool
	}{
		{0, 0, false},
		{1, 0, true},
		{0, 1, true},
		{1, 1, true},
	}
	for _, tc := range cases {
		s.tunCache = map[string]float64{
			"fish_hunt_enabled":      tc.fishHunt,
			"two_stage_hunt_enabled": tc.twoStage,
		}
		s.tunAt = time.Now()
		if got := s.huntStateEnabled(); got != tc.want {
			t.Fatalf("fish_hunt=%.0f two_stage=%.0f: huntStateEnabled=%v want=%v", tc.fishHunt, tc.twoStage, got, tc.want)
		}
	}
}

func TestFishHuntStageNameFollowsMode(t *testing.T) {
	ts := huntWeightsTestServer(map[string]float64{"two_stage_hunt_enabled": 1})
	if got := ts.fishHuntStageName(fishHuntModeFreshwater, fishHuntDaily); got != "日常" {
		t.Fatalf("两档制日常名称错: %s", got)
	}
	for _, stage := range []int{fishHuntGather, fishHuntActive, fishHuntGiant} {
		if got := ts.fishHuntStageName(fishHuntModeFreshwater, stage); got != "打窝" {
			t.Fatalf("两档制打窝名称错 stage=%d: %s", stage, got)
		}
	}
	if got := ts.fishHuntStageName(fishHuntModeMarine, fishHuntGiant); got != "巨物发窝" {
		t.Fatalf("两档制不得改写海钓阶段名: %s", got)
	}
	fh := huntWeightsTestServer(nil)
	if got := fh.fishHuntStageName(fishHuntModeFreshwater, fishHuntGather); got != "聚鱼" {
		t.Fatalf("鱼猎模式淡水阶段名错: %s", got)
	}
}

func TestFishHuntPityForcesTrophy(t *testing.T) {
	s := huntWeightsTestServer(nil)
	_, _, _, _, force := s.fishHuntWeights(fishHuntModeMarine, fishHuntActive, &fishHuntState{Pity: 60}, 0, false)
	if !force {
		t.Fatal("60 尾未出奖杯时必须触发奖杯硬保底")
	}
	_, _, w3, _, force := s.fishHuntWeights(fishHuntModeMarine, fishHuntActive, &fishHuntState{Pity: 30}, 0, false)
	if force || w3 <= 6.8 {
		t.Fatalf("30 尾软保底应提高奖杯权重，w3=%.2f force=%v", w3, force)
	}
	// 两档制打窝表同样吃保底：基础 w3=6，Pity30 → ×1.5 = 9。
	ts := huntWeightsTestServer(map[string]float64{"two_stage_hunt_enabled": 1})
	_, _, w3b, _, force := ts.fishHuntWeights(fishHuntModeFreshwater, fishHuntGather, &fishHuntState{Pity: 30}, 0, false)
	if force || math.Abs(w3b-9) > 1e-9 {
		t.Fatalf("两档制打窝 Pity30 应 w3=9，实得 %.2f force=%v", w3b, force)
	}
	_, _, _, _, force = ts.fishHuntWeights(fishHuntModeFreshwater, fishHuntGather, &fishHuntState{Pity: 60}, 0, false)
	if !force {
		t.Fatal("两档制打窝 60 尾仍必须触发硬保底")
	}
}

func TestHuntBaitMultiplierStaysInsideSelectedBand(t *testing.T) {
	lo, hi, _, ok := huntBandBounds(100, 200, 400, 3)
	if !ok {
		t.Fatal("奖杯档位边界缺失")
	}
	low, high := lo*2, hi*2
	for _, weight := range []float64{low, (low + high) / 2, high} {
		got := clampHuntBandWeight(weight, low, high, 2, 0)
		if got < low || got > high {
			t.Fatalf("饵效倍率越过奖杯档: weight=%.2f got=%.2f range=[%.2f,%.2f]",
				weight, got, low, high)
		}
	}
	if got := clampHuntBandWeight(high, low, high, 2, 0); got >= 400*2 {
		t.Fatalf("奖杯档被饵效倍率推入蓝冠档: %.2f", got)
	}
}

func TestHuntBaitMultiplierKeepsSelectedGrade(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	c := fishRollContext{cache: newFishingRollCache()}
	lines := s.cachedSettleStatics(ctx, c, "perch")
	if lines[0] <= 0 || lines[1] <= lines[0] || lines[2] <= lines[1] {
		t.Fatalf("perch 档位线不可用: %v", lines)
	}
	sampled := 0
	for i := 0; i < 1000; i++ {
		weight := s.sampleHuntBand(ctx, "perch", 3, 2, 1, 0, c)
		if weight <= 0 {
			continue
		}
		sampled++
		if grade := s.rollGradeForRoll(ctx, "perch", weight, c); grade != 3 {
			t.Fatalf("饵效倍率把奖杯档推到 grade=%d: weight=%d lines=%v", grade, weight, lines)
		}
	}
	if sampled == 0 {
		t.Fatal("没有采到奖杯档样本")
	}
}

func TestHookSpawnCompatibilityPrefersMatchingFish(t *testing.T) {
	small := gearPhysicsSpec{HookPresent: true, HookSize: 6, HookLengthMM: 3.3}
	large := gearPhysicsSpec{HookPresent: true, HookSize: 14, HookLengthMM: 6}
	huge := gearPhysicsSpec{HookPresent: true, HookSize: 28, HookLengthMM: 24}
	if got := hookSpawnCompatibility(300, small); got != 1 {
		t.Fatalf("6 号钩遇 300g 应完全匹配，实得 %.3f", got)
	}
	if got := hookSpawnCompatibility(1737, small); got >= .40 {
		t.Fatalf("6 号钩遇 1737g 应显著降权，实得 %.3f", got)
	}
	if got := hookSpawnCompatibility(1737, large); got < .95 {
		t.Fatalf("14 号钩遇 1737g 应基本匹配，实得 %.3f", got)
	}
	if got := hookSpawnCompatibility(50, huge); got >= .60 {
		t.Fatalf("特大钩遇 50g 应降权，实得 %.3f", got)
	}
}

func TestHookSpawnMatchBiasesGradesAndPreservesBlue(t *testing.T) {
	blueProbability := func(w1, w2, w3, w4 float64) float64 {
		total := w1 + w2 + w3 + w4
		if total <= 0 {
			return 0
		}
		return w4 / total
	}
	tests := []struct {
		name string
		h    *fishHuntState
		comp bool
	}{
		{name: "active", h: &fishHuntState{}},
		{name: "giant", h: &fishHuntState{Pity: 55}},
		{name: "blueComp", h: &fishHuntState{CompFish: 4, CompGrade: 4}, comp: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := huntWeightsTestServer(nil)
			w1, w2, w3, w4, _ := s.fishHuntWeights(fishHuntModeMarine, fishHuntActive, tc.h, 0, tc.comp)
			before := blueProbability(w1, w2, w3, w4)
			small1, small2, small3, small4 := w1, w2, w3, w4
			applyHookSpawnMatch(&small1, &small2, &small3, &small4, [4]float64{1, .5, .1, 1}, 1)
			large1, large2, large3, large4 := w1, w2, w3, w4
			applyHookSpawnMatch(&large1, &large2, &large3, &large4, [4]float64{.2, .5, 1, 1}, 1)
			if small3/small1 >= large3/large1 {
				t.Fatalf("小钩应降低奖杯相对权重: small=%.3f large=%.3f", small3/small1, large3/large1)
			}
			for name, w := range map[string][4]float64{
				"small": {small1, small2, small3, small4},
				"large": {large1, large2, large3, large4},
			} {
				if got := blueProbability(w[0], w[1], w[2], w[3]); math.Abs(got-before) > 1e-12 {
					t.Fatalf("%s 软匹配改变了蓝冠概率: before=%.12f after=%.12f", name, before, got)
				}
			}
		})
	}
}

func TestHookControlsDefaultOff(t *testing.T) {
	s := &Server{
		tunCache: map[string]float64{"grade_ratio_enabled": 0},
		tunAt:    time.Now(),
	}
	if s.hookNibbleEnabled() {
		t.Fatal("嘬饵机制默认应关闭")
	}
	if got := s.effectiveHookSpawnMatchStrength(); got != 0 {
		t.Fatalf("钩型发鱼软匹配默认应关闭，实得强度 %g", got)
	}
}

func TestHookNibbleSwitchKeepsProfileHoldMultiplier(t *testing.T) {
	s := &Server{
		tunCache: map[string]float64{tunableHookNibbleEnabled: 0},
		tunAt:    time.Now(),
		hookProfiles: map[string]store.HookBiteProfile{
			"small": {ItemID: "small", SolidScale: 1, HoldScale: .5},
		},
	}
	spec := gearPhysicsSpec{HookPresent: true, HookItemID: "small", HookSize: 6, HookLengthMM: 3.3}
	f := &RolledFish{Grade: 2, WeightG: 1737}
	s.applyHookBiteQuality(f, spec)
	if f.BiteQuality != "solid" {
		t.Fatalf("关闭嘬饵后不得再产生 nibble: %+v", f)
	}
	if math.Abs(f.HookHoldP-.25) > 1e-9 {
		t.Fatalf("后台保持率倍率仍应生效，HookHoldP=%.4f", f.HookHoldP)
	}
}

func TestLuckProtectedBlueBypassesHookQuality(t *testing.T) {
	tiny := gearPhysicsSpec{HookPresent: true, HookSize: 0, HookLengthMM: 1.8}
	f := &RolledFish{Grade: 4, WeightG: 500_000}
	(&Server{}).applyHookBiteQuality(f, tiny)
	if !f.LuckProtected || f.BiteQuality != "solid" || f.HookHoldP != 1 {
		t.Fatalf("蓝冠天命鱼必须绕过钩型判定: %+v", f)
	}
}

func TestHookBiteStatePersistsWithPendingFish(t *testing.T) {
	want := &RolledFish{
		FishGUID: "fish-nibble", BiteQuality: "nibble", HookHoldP: .42, LuckProtected: false,
		HookConfirmed: true, HuntStage: fishHuntActive, HuntStageKnown: true,
	}
	got := rolledFishFromStore(rolledFishToStore(want))
	if got == nil || got.BiteQuality != want.BiteQuality || got.HookHoldP != want.HookHoldP ||
		got.LuckProtected != want.LuckProtected || got.HookConfirmed != want.HookConfirmed ||
		got.HuntStage != want.HuntStage || got.HuntStageKnown != want.HuntStageKnown {
		t.Fatalf("钩型咬口状态未持久化: %+v", got)
	}
}

// TestLuckProtectedBlueEscapesLikeNormalFish：蓝冠天命鱼中钩后的钩型脱钩帧
// 按真脱钩处理（与普通鱼同路径），不再救援重推；脱钩补偿照常记账。
func TestLuckProtectedBlueEscapesLikeNormalFish(t *testing.T) {
	s, _ := newRigTestServer(t)
	sess, _ := newTunnelProbe(t, s)
	setWeatherTestTunable(t, s, "fish_hunt_enabled", "1")
	st := s.stateFor(sess.Userid)
	fish := &RolledFish{FishGUID: "luck-escape", Species: "crucian", Grade: 4, WeightG: 3200,
		BiteQuality: "solid", HookHoldP: 1, LuckProtected: true}
	st.mu.Lock()
	st.CastActive, st.InWater, st.Hooked, st.Pending = true, true, true, fish
	st.Loc, st.Spot = "luck-escape-14-12", 3
	st.mu.Unlock()

	tree := proto.Args524(proto.Obj("lhicjpmglbo", 0,
		proto.Field{Op: "U32", Num: ptrU64(2)}))
	if _, ok := hFishingLeft(miscCtx(sess, 14, 12, tree)); !ok {
		t.Fatal("14/12 未处理")
	}
	st.mu.Lock()
	escapes := st.Pending == nil && !st.Hooked && !st.Resolved && st.biteTimer != nil
	st.cancelBiteTimerLocked()
	st.mu.Unlock()
	if !escapes {
		t.Fatalf("天命蓝冠脱钩应按真脱钩处理并重排下一口: pending=%v hooked=%v resolved=%v timer=%v",
			st.Pending != nil, st.Hooked, st.Resolved, st.biteTimer != nil)
	}
	h := s.loadFishHuntState(t.Context(), sess.Userid, "luck-escape-14-12", 3)
	if h.BigMiss != 1 || h.CompFish != 4 || h.CompGrade != 4 {
		t.Fatalf("蓝冠脱钩应获得回访补偿: %+v", h)
	}
}

func TestLuckProtectedBlueTerminalFrameStillFinal(t *testing.T) {
	s, _ := newRigTestServer(t)
	sess, _ := newTunnelProbe(t, s)
	st := s.stateFor(sess.Userid)
	fish := &RolledFish{Species: "crucian", Grade: 4, WeightG: 3200,
		BiteQuality: "solid", HookHoldP: 1, LuckProtected: true}
	st.mu.Lock()
	st.CastActive, st.InWater, st.Hooked, st.Pending = true, true, true, fish
	st.mu.Unlock()

	tree := proto.Args524(proto.Obj("lhicjpmglbo", 0,
		proto.Field{Op: "U32", Num: ptrU64(7)}))
	if _, ok := hFishingLeft(miscCtx(sess, 14, 12, tree)); !ok {
		t.Fatal("14/12 f0=7 未处理")
	}
	st.mu.Lock()
	terminal := st.Pending == nil && !st.Hooked && st.Resolved && st.pendingTimer == nil
	st.cancelBiteTimerLocked()
	st.mu.Unlock()
	if !terminal {
		t.Fatalf("f0=7 是终态，蓝冠鱼同样不得复活: pending=%v hooked=%v resolved=%v timer=%v",
			st.Pending != nil, st.Hooked, st.Resolved, st.pendingTimer != nil)
	}
}

func TestUnhookedTrophyUnknownFrameDoesNotGrantEscapeCompensation(t *testing.T) {
	s, _ := newRigTestServer(t)
	sess, _ := newTunnelProbe(t, s)
	setWeatherTestTunable(t, s, "fish_hunt_enabled", "1")
	st := s.stateFor(sess.Userid)
	fish := &RolledFish{FishGUID: "unhooked-trophy", Species: "carp", Grade: 3, WeightG: 12_000}
	st.mu.Lock()
	st.Pending, st.CastActive, st.InWater, st.Hooked = fish, true, true, false
	st.Loc, st.Spot = "unhooked-14-12", 7
	st.mu.Unlock()

	tree := proto.Args524(
		proto.Guid(st.GearGuid),
		proto.Guid(fish.FishGUID),
		proto.Obj("lhicjpmglbo", 0, proto.Field{Op: "U32", Num: ptrU64(4)}),
	)
	if _, ok := hFishingLeft(miscCtx(sess, 14, 12, tree)); !ok {
		t.Fatal("14/12 f0=4 未处理")
	}
	h := s.loadFishHuntState(t.Context(), sess.Userid, "unhooked-14-12", 7)
	if h.BigMiss != 0 || h.CompFish != 0 || h.CompUntil != 0 {
		t.Fatalf("未中钩鱼不得获得脱钩补偿: %+v", h)
	}
}

func TestNibbleDoesNotEnterHookedState(t *testing.T) {
	s, _ := newRigTestServer(t)
	setWeatherTestTunable(t, s, tunableHookNibbleEnabled, "1")
	sess, _ := newTunnelProbe(t, s)
	st := s.stateFor(sess.Userid)
	fish := &RolledFish{Species: "crucian", Grade: 1, WeightG: 120,
		BiteQuality: "nibble", HookHoldP: .4}
	st.mu.Lock()
	st.CastActive, st.InWater, st.Resolved, st.Pending = true, true, false, fish
	st.mu.Unlock()

	if _, ok := hFishingHooked(miscCtx(sess, 14, 11, nil)); !ok {
		t.Fatal("14/11 嘬饵处理失败")
	}
	st.mu.Lock()
	cleared := st.Pending == nil && !st.Hooked && st.biteTimer != nil
	st.cancelBiteTimerLocked()
	st.mu.Unlock()
	if !cleared {
		t.Fatalf("嘬饵不应进入 Hooked 状态: pending=%v hooked=%v timer=%v",
			st.Pending != nil, st.Hooked, st.biteTimer != nil)
	}
}

func TestDisabledNibbleStillHooksPendingNibble(t *testing.T) {
	s, _ := newRigTestServer(t)
	sess, _ := newTunnelProbe(t, s)
	setWeatherTestTunable(t, s, tunableHookNibbleEnabled, "0")
	st := s.stateFor(sess.Userid)
	fish := &RolledFish{Species: "crucian", Grade: 1, WeightG: 120,
		BiteQuality: "nibble", HookHoldP: 1}
	st.mu.Lock()
	st.CastActive, st.InWater, st.Resolved, st.Pending = true, true, false, fish
	st.mu.Unlock()

	if _, ok := hFishingHooked(miscCtx(sess, 14, 11, nil)); !ok {
		t.Fatal("关闭嘬饵后的 14/11 处理失败")
	}
	st.mu.Lock()
	hooked := st.Hooked && st.Pending == fish && fish.HookConfirmed
	st.cancelBiteTimerLocked()
	st.cancelPendingTimerLocked()
	st.mu.Unlock()
	if !hooked {
		t.Fatalf("关闭嘬饵后，在途 nibble 也必须正常中钩: pending=%v hooked=%v confirmed=%v",
			st.Pending != nil, st.Hooked, fish.HookConfirmed)
	}
}

func TestDuplicateHookedConfirmationIsIdempotent(t *testing.T) {
	s, _ := newRigTestServer(t)
	sess, _ := newTunnelProbe(t, s)
	st := s.stateFor(sess.Userid)
	fish := &RolledFish{Species: "carp", Grade: 2, WeightG: 3200,
		BiteQuality: "solid", HookHoldP: 1}
	st.mu.Lock()
	st.CastActive, st.InWater, st.Pending = true, true, fish
	st.mu.Unlock()

	if _, ok := hFishingHooked(miscCtx(sess, 14, 11, nil)); !ok {
		t.Fatal("首次 14/11 未处理")
	}
	st.mu.Lock()
	if !st.Hooked || st.Pending != fish {
		st.mu.Unlock()
		t.Fatal("首次 14/11 没有稳定中钩")
	}
	// 重复确认不得再次抽样，也不得因为当前状态被误改而丢鱼。
	fish.BiteQuality = "nibble"
	st.mu.Unlock()
	if _, ok := hFishingHooked(miscCtx(sess, 14, 11, nil)); !ok {
		t.Fatal("重复 14/11 未处理")
	}
	st.mu.Lock()
	kept := st.Hooked && st.Pending == fish
	st.cancelBiteTimerLocked()
	st.cancelPendingTimerLocked()
	st.mu.Unlock()
	if !kept {
		t.Fatal("重复 14/11 不得重抽保持率或清掉已中钩鱼")
	}
}

func TestMarineNibbleDoesNotEnterHookedState(t *testing.T) {
	s, _ := newRigTestServer(t)
	setWeatherTestTunable(t, s, tunableHookNibbleEnabled, "1")
	sess, _ := newTunnelProbe(t, s)
	st := s.stateFor(sess.Userid)
	fish := &RolledFish{Species: "atl_cod", Grade: 2, WeightG: 4200,
		BiteQuality: "nibble", HookHoldP: .4}
	st.mu.Lock()
	st.RigType, st.CastActive, st.InWater, st.Pending = 50, true, true, fish
	st.mu.Unlock()

	if _, ok := hFishingPull(miscCtx(sess, 14, 9, nil)); !ok {
		t.Fatal("14/9 海钓嘬饵处理失败")
	}
	st.mu.Lock()
	cleared := st.Pending == nil && !st.Hooked && st.biteTimer != nil
	st.cancelBiteTimerLocked()
	st.mu.Unlock()
	if !cleared {
		t.Fatalf("海钓嘬饵不应进入 Hooked 状态: pending=%v hooked=%v timer=%v",
			st.Pending != nil, st.Hooked, st.biteTimer != nil)
	}
}

// 嘬饵否决必须让客户端知情：否决时推送 14/15[竿,鱼]（客户端把 14/15
// 当"鱼离场"，鱼走自然脱钩状态机游走）。否则客户端会蒙在
// 鼓里继续搏鱼，拖到岸边收竿 14/4 落空，鱼凭空消失（2026-11-08 23:24/23:26 实证）。
func TestNibbleVetoPushesFishLeave(t *testing.T) {
	s, _ := newRigTestServer(t)
	setWeatherTestTunable(t, s, tunableHookNibbleEnabled, "1")
	sess, gc := newTunnelProbe(t, s)
	st := s.stateFor(sess.Userid)
	fish := &RolledFish{FishGUID: "1411feed1411feed1411feed1411feed", Species: "crucian", Grade: 1, WeightG: 120,
		BiteQuality: "nibble", HookHoldP: .4}
	st.mu.Lock()
	st.GearGuid = "aaa00000aaa00000aaa00000aaa00000"
	st.CastActive, st.InWater, st.Resolved, st.Pending = true, true, false, fish
	st.mu.Unlock()

	if _, ok := hFishingHooked(miscCtx(sess, 14, 11, nil)); !ok {
		t.Fatal("14/11 嘬饵处理失败")
	}
	env := awaitPushEnvelope(t, gc)
	if env.Main != 14 || env.Sub != 15 {
		t.Fatalf("嘬饵否决应推送 14/15 鱼离场，实得 %d/%d", env.Main, env.Sub)
	}
	dec, _, err := proto.DecodeValue(env.Body, 0, s.Table())
	if err != nil {
		t.Fatalf("14/15 body 不可解码: %v", err)
	}
	if len(dec.Items) < 2 {
		t.Fatalf("14/15 args 应为 [竿GUID, 鱼GUID]，实得 %d 项", len(dec.Items))
	}
	if got := dec.Items[1].GuidHex(); !strings.EqualFold(got, fish.FishGUID) {
		t.Fatalf("14/15 args[1] 应为否决鱼 GUID %s，实得 %q", fish.FishGUID, got)
	}
	// 推送不得改变否决本身的状态转移。
	st.mu.Lock()
	cleared := st.Pending == nil && !st.Hooked && st.biteTimer != nil
	st.cancelBiteTimerLocked()
	st.mu.Unlock()
	if !cleared {
		t.Fatalf("否决状态转移不得改变: pending=%v hooked=%v timer=%v",
			st.Pending != nil, st.Hooked, st.biteTimer != nil)
	}
}

func TestMarineNibbleVetoPushesFishLeave(t *testing.T) {
	s, _ := newRigTestServer(t)
	setWeatherTestTunable(t, s, tunableHookNibbleEnabled, "1")
	sess, gc := newTunnelProbe(t, s)
	st := s.stateFor(sess.Userid)
	fish := &RolledFish{FishGUID: "149feed0149feed0149feed0149feed0", Species: "atl_cod", Grade: 2, WeightG: 4200,
		BiteQuality: "nibble", HookHoldP: .4}
	st.mu.Lock()
	st.GearGuid = "bbb00000bbb00000bbb00000bbb00000"
	st.RigType, st.CastActive, st.InWater, st.Pending = 50, true, true, fish
	st.mu.Unlock()

	if _, ok := hFishingPull(miscCtx(sess, 14, 9, nil)); !ok {
		t.Fatal("14/9 海钓嘬饵处理失败")
	}
	env := awaitPushEnvelope(t, gc)
	if env.Main != 14 || env.Sub != 15 {
		t.Fatalf("海钓嘬饵否决应推送 14/15 鱼离场，实得 %d/%d", env.Main, env.Sub)
	}
	dec, _, err := proto.DecodeValue(env.Body, 0, s.Table())
	if err != nil {
		t.Fatalf("14/15 body 不可解码: %v", err)
	}
	if got := dec.Items[1].GuidHex(); !strings.EqualFold(got, fish.FishGUID) {
		t.Fatalf("14/15 args[1] 应为否决鱼 GUID %s，实得 %q", fish.FishGUID, got)
	}
}

// 否决后客户端的离场回执（14/12 f0=5，此时 Pending 已空）必须按良性回执忽略：
// 不清刚重排的咬钩 timer，不把本竿标记为终局，也不产生任何推送。
func TestNibbleVetoLeaveAckBenign(t *testing.T) {
	s, _ := newRigTestServer(t)
	setWeatherTestTunable(t, s, tunableHookNibbleEnabled, "1")
	sess, gc := newTunnelProbe(t, s)
	st := s.stateFor(sess.Userid)
	fish := &RolledFish{FishGUID: "abcd0000abcd0000abcd0000abcd0000", Species: "crucian", Grade: 1, WeightG: 120,
		BiteQuality: "nibble", HookHoldP: .4}
	st.mu.Lock()
	st.GearGuid = "ccc00000ccc00000ccc00000ccc00000"
	st.CastActive, st.InWater, st.Resolved, st.Pending = true, true, false, fish
	st.mu.Unlock()
	if _, ok := hFishingHooked(miscCtx(sess, 14, 11, nil)); !ok {
		t.Fatal("14/11 嘬饵处理失败")
	}
	awaitPushEnvelope(t, gc) // 14/15 离场推送

	tree := proto.Args524(
		proto.Guid(st.GearGuid),
		proto.Guid(fish.FishGUID),
		proto.Obj("lhicjpmglbo", 0, proto.Field{Op: "U32", Num: ptrU64(5)}),
	)
	if _, ok := hFishingLeft(miscCtx(sess, 14, 12, tree)); !ok {
		t.Fatal("14/12 f0=5 离场回执未处理")
	}
	st.mu.Lock()
	timer := st.biteTimer
	resolved, pending := st.Resolved, st.Pending != nil
	st.mu.Unlock()
	if resolved || timer == nil || pending {
		t.Fatalf("离场回执不得终止本竿: resolved=%v timer=%v pending=%v",
			resolved, timer != nil, pending)
	}
	ch := make(chan error, 1)
	go func() {
		_, _, _, err := gc.ReadRecord()
		ch <- err
	}()
	select {
	case err := <-ch:
		t.Fatalf("离场回执不应产生推送，实得 %v", err)
	case <-time.After(400 * time.Millisecond):
	}
	st.mu.Lock()
	st.cancelBiteTimerLocked()
	st.mu.Unlock()
}

func TestHookQualityPrefersDifferentFishSizes(t *testing.T) {
	tiny := gearPhysicsSpec{HookPresent: true, HookSize: 4, HookLengthMM: 2.5}
	large := gearPhysicsSpec{HookPresent: true, HookSize: 28, HookLengthMM: 24}
	if hookScore(tiny) >= hookScore(large) {
		t.Fatalf("大钩评分应高于小钩: tiny=%.2f large=%.2f", hookScore(tiny), hookScore(large))
	}
	if solid, _ := hookSolidHold(100, large); solid >= .9 {
		t.Fatalf("大钩遇 100g 小鱼不应有稳定入口，solid=%.2f", solid)
	}
	if solid, _ := hookSolidHold(20_000, tiny); solid >= .9 {
		t.Fatalf("小钩遇 20kg 大鱼不应有稳定入口，solid=%.2f", solid)
	}
}

func TestHookHoldOnlyFailsAboveHookCapacity(t *testing.T) {
	large := gearPhysicsSpec{HookPresent: true, HookSize: 28, HookLengthMM: 24}
	if _, hold := hookSolidHold(100_000, large); hold != 1 {
		t.Fatalf("钩容量内中钩后应固定保持率 1，实得 %.4f", hold)
	}
	if _, hold := hookSolidHold(200_000, large); hold >= 1 {
		t.Fatalf("超过钩容量后保持率应下降，实得 %.4f", hold)
	}
}

func TestFishHuntCircularGroundbaitRange(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	const uid = int64(1000001)
	s.groundbaitDeposit(ctx, uid, "range-test", 100, 100, true, 7,
		"feed_karp", 10, "catapult", "range:1", "")

	unitsAt := func(x, z float64) float64 {
		return s.groundbaitSpotAt(ctx, uid, "range-test", x, z, true, 7).units
	}
	if got := unitsAt(100, 100); got < 9.99 || got > 10.01 {
		t.Fatalf("核心圈应为全额 10，实得 %.3f", got)
	}
	if got := unitsAt(105, 100); got < 8.6 || got > 8.75 {
		t.Fatalf("5 米距离应约 86.7%%，实得 %.3f", got)
	}
	if got := unitsAt(109, 100); got < 1.9 || got > 2.1 {
		t.Fatalf("9 米距离应约 20%%，实得 %.3f", got)
	}
	if got := unitsAt(111, 100); got != 0 {
		t.Fatalf("10 米外应为 0，实得 %.3f", got)
	}
}

func TestFishHuntReadinessRequiresFishMouthMatch(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	setWeatherTestTunable(t, s, "fish_ecology_enabled", "1")
	setWeatherTestTunable(t, s, "fish_weather_enabled", "1")
	const (
		uid  = int64(1000003)
		loc  = "hunt-stage-test"
		spot = uint64(7)
	)
	s.groundbaitDeposit(ctx, uid, loc, 300, 300, true, spot,
		"feed_dry", 8, "feeder", "stage:1", "")
	h := s.loadFishHuntState(ctx, uid, loc, spot)
	h.ActiveSeconds, h.ValidBites, h.AnchorKnown = 1800, 30, true
	h.AnchorX, h.AnchorZ = 300, 300
	c := fishRollContext{
		uid: uid, loc: loc, x: 300, z: 300, hasPos: true, spot: spot,
		baits: []string{"muckworm"}, rigType: 1, depthCM: 400,
		now: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC), cache: newFishingRollCache(),
		weather: fishingWeather{known: true, kind: fishingWeatherClear, airTemp: 20, previousAirTemp: 20, wind: 2},
		skill: fishingSkillBonus{BiteSpeed: 1, EcologyInterest: .22, RareFish: .18,
			LayerAccuracy: .18, WeatherAdaptation: .2, LureTolerance: .2},
	}
	r := s.fishHuntReadiness(ctx, "crucian", c, h)
	if r.A < .80 || r.G < .50 || r.P < .80 {
		t.Fatalf("窝料与持续度应已充分积累，实得 A=%.2f E=%.2f G=%.2f P=%.2f Q=%.2f",
			r.A, r.E, r.G, r.P, r.Q)
	}
	if r.E < .45 && r.Stage != fishHuntDaily {
		t.Fatalf("鱼口匹配不足时必须守住日常阶段，实得 E=%.2f stage=%s", r.E, s.fishHuntStageName(fishHuntModeFreshwater, r.Stage))
	}
}

func TestLureHuntBuildsBigFishProgressWithoutGroundbait(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	const (
		uid  = int64(1000009)
		loc  = "lure-hunt-test"
		spot = uint64(7)
	)
	c := fishRollContext{
		uid: uid, loc: loc, x: 300, z: 300, hasPos: true, spot: spot,
		rigType: 5, lureAction: "steady", lureLayer: .45,
		now: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC), cache: newFishingRollCache(),
		weather: fishingWeather{known: true, kind: fishingWeatherClear, airTemp: 20, previousAirTemp: 20, wind: 2},
	}
	h := &fishHuntState{
		UID: uid, Loc: loc, Spot: spot, Mode: fishHuntModeLure,
		ActiveSeconds: 1800, ValidBites: 30,
	}
	r := s.fishHuntReadiness(ctx, "perch", c, h)
	if r.Stage != fishHuntGiant {
		t.Fatalf("路亚持续搜索不应依赖窝料，stage=%s A=%.2f E=%.2f G=%.2f Q=%.2f",
			s.fishHuntStageName(fishHuntModeLure, r.Stage), r.A, r.E, r.G, r.Q)
	}
	if h.Mode != fishHuntModeLure {
		t.Fatalf("路亚应记录独立狩猎模式: %+v", h)
	}
}

func TestLureHuntDoesNotStartAtGiantWithoutProgress(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	const (
		uid  = int64(1000010)
		loc  = "lure-hunt-fresh"
		spot = uint64(7)
	)
	c := fishRollContext{
		uid: uid, loc: loc, x: 300, z: 300, hasPos: true, spot: spot,
		rigType: 5, lureAction: "steady", lureLayer: .45,
		now: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC), cache: newFishingRollCache(),
		weather: fishingWeather{known: true, kind: fishingWeatherClear, airTemp: 20, previousAirTemp: 20, wind: 2},
	}
	h := &fishHuntState{UID: uid, Loc: loc, Spot: spot, Mode: fishHuntModeLure}
	r := s.fishHuntReadiness(ctx, "perch", c, h)
	if r.Stage == fishHuntGiant {
		t.Fatalf("刚换路亚不应直接进入巨物阶段: A=%.2f G=%.2f Q=%.2f", r.A, r.G, r.Q)
	}
}

func TestSwitchingFromGroundbaitToLureResetsSharedProgress(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	const (
		uid  = int64(1000011)
		loc  = "lure-mode-switch"
		spot = uint64(7)
	)
	c := fishRollContext{
		uid: uid, loc: loc, x: 300, z: 300, hasPos: true, spot: spot,
		rigType: 5, lureAction: "steady", lureLayer: .45,
		now: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC), cache: newFishingRollCache(),
	}
	h := &fishHuntState{
		UID: uid, Loc: loc, Spot: spot, Mode: fishHuntModeFreshwater,
		ActiveSeconds: 1800, ValidBites: 30,
	}
	s.fishHuntReadiness(ctx, "perch", c, h)
	if h.Mode != fishHuntModeLure || h.ActiveSeconds != 0 || h.ValidBites != 0 {
		t.Fatalf("切到路亚时不能继承窝料发窝进度: %+v", h)
	}
}

func TestMarineHuntUsesSonarZoneWithoutGroundbait(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	setWeatherTestTunable(t, s, "fish_ecology_enabled", "1")
	setWeatherTestTunable(t, s, "fish_weather_enabled", "1")
	const (
		uid  = int64(1000005)
		loc  = "loc12_215"
		spot = uint64(7)
	)
	h := &fishHuntState{UID: uid, Loc: loc, Spot: spot, Mode: fishHuntModeMarine,
		Zone: 215, ZoneKnown: true, ActiveSeconds: 1800, ValidBites: 30}
	c := fishRollContext{
		uid: uid, loc: loc, x: 2260, z: -460, hasPos: true, spot: spot,
		rigType: 50, depthCM: 450, lureLayer: .90, lureAction: "marine_jig",
		lureSpeed: 8, boatKnown: true, onBoat: true,
		now:     time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
		cache:   newFishingRollCache(),
		weather: fishingWeather{known: true, kind: fishingWeatherClear, airTemp: 18, previousAirTemp: 17, wind: 3},
		runtime: &fishRollRuntime{chartKnown: true, zone: 215, zoneKnown: true, sonarSystemID: "loc12_215"},
		skill: fishingSkillBonus{BiteSpeed: 1, EcologyInterest: .22, RareFish: .18,
			LayerAccuracy: .18, WeatherAdaptation: .2, LureTolerance: .2},
	}
	r := s.fishHuntReadiness(ctx, "atl_cod", c, h)
	if r.A < .50 || r.G < .25 || r.Stage == fishHuntDaily {
		t.Fatalf("海图声呐区应能建立狩猎状态，实得 stage=%s A=%.2f E=%.2f G=%.2f P=%.2f Q=%.2f",
			s.fishHuntStageName(fishHuntModeMarine, r.Stage), r.A, r.E, r.G, r.P, r.Q)
	}
	if h.Mode != fishHuntModeMarine || !h.ZoneKnown || h.Zone != 215 {
		t.Fatalf("海图狩猎状态未写入声呐区: %+v", h)
	}
}

func TestMarineHuntUnknownZoneKeepsLastKnownProgress(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	setWeatherTestTunable(t, s, "fish_ecology_enabled", "1")
	setWeatherTestTunable(t, s, "fish_weather_enabled", "1")
	const (
		uid  = int64(1000008)
		loc  = "loc12_215"
		spot = uint64(7)
	)
	h := &fishHuntState{UID: uid, Loc: loc, Spot: spot, Mode: fishHuntModeMarine,
		Zone: 215, ZoneKnown: true, ActiveSeconds: 1800, ValidBites: 30, Pity: 25,
		CompFish: 2, CompGrade: 3, CompUntil: float64(time.Now().Add(time.Hour).Unix())}
	c := fishRollContext{
		uid: uid, loc: loc, x: 2260, z: -460, hasPos: true, spot: spot,
		rigType: 50, depthCM: 450, lureLayer: .90, lureAction: "marine_jig",
		lureSpeed: 8, boatKnown: true, onBoat: true,
		now:     time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
		cache:   newFishingRollCache(),
		weather: fishingWeather{known: true, kind: fishingWeatherClear, airTemp: 18, previousAirTemp: 17, wind: 3},
		runtime: &fishRollRuntime{chartKnown: true, zone: 215, zoneKnown: true, sonarSystemID: "loc12_215"},
		skill: fishingSkillBonus{BiteSpeed: 1, EcologyInterest: .22, RareFish: .18,
			LayerAccuracy: .18, WeatherAdaptation: .2, LureTolerance: .2},
	}
	s.fishMarineHuntReadiness(ctx, "atl_cod", c, h)
	if !h.ZoneKnown || h.Zone != 215 || h.Pity != 25 || h.CompFish != 2 {
		t.Fatalf("同一已知区号不应重置进度: %+v", h)
	}

	c.runtime.zoneKnown = false
	c.runtime.zone = 0
	s.fishMarineHuntReadiness(ctx, "atl_cod", c, h)
	if !h.ZoneKnown || h.Zone != 215 || h.Pity != 25 || h.CompFish != 2 {
		t.Fatalf("未知位置应保留上一次已知区号与进度: %+v", h)
	}

	c.runtime.zoneKnown = true
	c.runtime.zone = 216
	c.runtime.sonarSystemID = "loc12_216"
	s.fishMarineHuntReadiness(ctx, "atl_cod", c, h)
	if !h.ZoneKnown || h.Zone != 216 || h.ActiveSeconds != 0 || h.ValidBites != 0 ||
		h.Pity != 0 || h.CompFish != 0 {
		t.Fatalf("切换到不同已知区号应重置进度: %+v", h)
	}
}

func TestFishHuntEscapeCompensation(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	const (
		uid  = int64(1000002)
		loc  = "hunt-test"
		spot = uint64(7)
	)
	f := &RolledFish{FishGUID: "fish-trophy", Grade: 3, HuntStageKnown: true, HuntStage: fishHuntActive}
	s.recordHuntEscape(ctx, uid, loc, spot, f)
	h := s.loadFishHuntState(ctx, uid, loc, spot)
	if h.BigMiss != 1 || h.CompFish != 3 || h.CompUntil <= 0 {
		t.Fatalf("奖杯脱钩应留下 3 尾回访补偿，实得 %+v", h)
	}

	for i := 1; i <= 3; i++ {
		f = &RolledFish{FishGUID: fmt.Sprintf("fish-comp-%d", i), Grade: 1,
			HuntStageKnown: true, HuntStage: fishHuntGather}
		s.recordHuntCatch(ctx, uid, loc, spot, f)
		h = s.loadFishHuntState(ctx, uid, loc, spot)
		if want := int64(3 - i); h.CompFish != want {
			t.Fatalf("第 %d 尾有效鱼后补偿剩余=%d，期望 %d: %+v", i, h.CompFish, want, h)
		}
		s.recordHuntCatch(ctx, uid, loc, spot, f)
		if replay := s.loadFishHuntState(ctx, uid, loc, spot); replay.CompFish != h.CompFish {
			t.Fatalf("同一尾 FishGUID 重放不得再次消耗补偿: before=%d after=%d", h.CompFish, replay.CompFish)
		}
		if i == 3 && (h.CompGrade != 0 || h.CompUntil != 0) {
			t.Fatalf("补偿耗尽后应清空档位和截止时间: %+v", h)
		}
	}
	if h.BigMiss != 1 || h.Pity != 3 {
		t.Fatalf("三尾普通鱼应逐尾消耗补偿并只累计聚鱼阶段保底: %+v", h)
	}
}

func TestFishHuntDailyCatchDoesNotAccumulatePity(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	const (
		uid  = int64(1000006)
		loc  = "hunt-daily-test"
		spot = uint64(7)
	)
	daily := &RolledFish{FishGUID: "fish-daily", Grade: 1,
		HuntStageKnown: true, HuntStage: fishHuntDaily}
	s.recordHuntCatch(ctx, uid, loc, spot, daily)
	h := s.loadFishHuntState(ctx, uid, loc, spot)
	if h.Pity != 0 {
		t.Fatalf("日常鱼不得累计奖杯保底: %+v", h)
	}
	unknown := &RolledFish{FishGUID: "fish-unknown-stage", Grade: 1}
	s.recordHuntCatch(ctx, uid, loc, spot, unknown)
	h = s.loadFishHuntState(ctx, uid, loc, spot)
	if h.Pity != 0 {
		t.Fatalf("旧快照阶段未知时不得猜测并累计保底: %+v", h)
	}
	gather := &RolledFish{FishGUID: "fish-gather", Grade: 1,
		HuntStageKnown: true, HuntStage: fishHuntGather}
	s.recordHuntCatch(ctx, uid, loc, spot, gather)
	h = s.loadFishHuntState(ctx, uid, loc, spot)
	if h.Pity != 1 {
		t.Fatalf("聚鱼阶段普通鱼应累计奖杯保底: %+v", h)
	}
}

func TestFishHuntCompensationExpires(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	const (
		uid  = int64(1000007)
		loc  = "hunt-expire-test"
		spot = uint64(7)
	)
	h := s.loadFishHuntState(ctx, uid, loc, spot)
	h.CompFish, h.CompGrade, h.CompUntil = 1, 4, float64(time.Now().Add(-time.Second).Unix())
	s.saveFishHuntState(ctx, h)
	s.recordHuntCatch(ctx, uid, loc, spot, &RolledFish{
		FishGUID: "fish-after-expire", Grade: 1, HuntStageKnown: true, HuntStage: fishHuntGather,
	})
	h = s.loadFishHuntState(ctx, uid, loc, spot)
	if h.CompFish != 0 || h.CompGrade != 0 || h.CompUntil != 0 {
		t.Fatalf("超时补偿应直接失效: %+v", h)
	}
}

func TestFishHuntNewSpotResetsAfterTwentyMeters(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	const (
		uid  = int64(1000004)
		loc  = "hunt-anchor-test"
		spot = uint64(7)
	)
	s.groundbaitDeposit(ctx, uid, loc, 100, 100, true, spot,
		"feed_dry", 1, "catapult", "anchor:1", "")
	h := s.loadFishHuntState(ctx, uid, loc, spot)
	h.ValidBites = 7
	s.saveFishHuntState(ctx, h)

	s.groundbaitDeposit(ctx, uid, loc, 115, 100, true, spot,
		"feed_dry", 1, "catapult", "anchor:2", "")
	if got := s.loadFishHuntState(ctx, uid, loc, spot).ValidBites; got != 7 {
		t.Fatalf("20 米内的补窝不应重置发窝进度，实得 %d", got)
	}

	s.groundbaitDeposit(ctx, uid, loc, 125, 100, true, spot,
		"feed_dry", 1, "catapult", "anchor:3", "")
	if got := s.loadFishHuntState(ctx, uid, loc, spot).ValidBites; got != 0 {
		t.Fatalf("超过 20 米的新窝应重置发窝进度，实得 %d", got)
	}
}
