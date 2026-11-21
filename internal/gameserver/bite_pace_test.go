package gameserver

// 饵效控速（bite_pace_enabled）测试。语义与数值口径见
// specs/changes/bite-pace/spec.md；夹具沿用 bait_weight_test 的名册/tier 种子。

import (
	"math"
	"testing"
	"time"

	"rf4server/internal/store"
)

// seedPaceTest 在饵效权重夹具上补齐 pace 需要的数据：pace 开关 +
// 常见拟鲤的食性显式覆盖（吃虫与植物、几乎不攻拟饵——蚯蚓/玉米对口、亮片不对口）。
func seedPaceTest(t *testing.T) (*Server, *store.DB) {
	t.Helper()
	s, db := seedBaitWeightTest(t)
	// 测试库 fish_ecology 为空（读走内嵌兜底），必须显式插入该行：常见拟鲤
	// 吃虫与植物、几乎不攻拟饵——蚯蚓/玉米对口（0.9×0.8=0.72）、亮片不对口
	//（0.2/2=0.1）。先落数据再 reload：reload_tunables 会顺带全量加载
	// fishEcologyCache，后插的行不刷新就永远是内嵌兜底值。
	if _, err := db.SQL.Exec(`INSERT OR REPLACE INTO fish_ecology(fish_key, diet_json, enabled) VALUES('c.roach','{"worm":0.9,"plant":0.7,"lure":0.2}',1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.TunableSet(t.Context(), "bite_pace_enabled", "1", "test"); err != nil {
		t.Fatal(err)
	}
	s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
	return s, db
}

func TestPaceTierForMapping(t *testing.T) {
	s, _ := newDetachTestServer(t)
	cases := []struct {
		score                  float64
		factor, prob, lureProb float64
		tier                   int
	}{
		{0.90, 1.0, 1.0, 0.30, 2},
		{0.75, 1.0, 1.0, 0.30, 2},
		{0.50, 1.6, 0.5, 0.10, 1},
		{0.45, 1.6, 0.5, 0.10, 1},
		{0.30, 3.0, 0.12, 0.02, 0},
		{0.0, 3.0, 0.12, 0.02, 0},
	}
	for _, tc := range cases {
		factor, prob, lureProb, tier := s.paceTierFor(tc.score)
		if factor != tc.factor || prob != tc.prob || lureProb != tc.lureProb || tier != tc.tier {
			t.Fatalf("score %.2f: got (%g,%g,%g,%d) want (%g,%g,%g,%d)",
				tc.score, factor, prob, lureProb, tier, tc.factor, tc.prob, tc.lureProb, tc.tier)
		}
	}
}

func TestPaceStandardClampAndFloorCap(t *testing.T) {
	s, _ := newDetachTestServer(t)
	if got := paceStandardClamp(3, 6, 60, 1); got != 6 {
		t.Fatal(got)
	}
	if got := paceStandardClamp(100, 6, 60, 1); got != 60 {
		t.Fatal(got)
	}
	// scale 在 cap 之后，与 legacy standardBiteDelay 同序（可破下限）。
	if got := paceStandardClamp(10, 6, 60, 0.5); got != 5 {
		t.Fatal(got)
	}
	if floor, capS := s.paceFloorCapFor(2, false, 1); floor != 6 || capS != 60 {
		t.Fatalf("好饵底/浮区间应为 [6,60]，实得 [%g,%g]", floor, capS)
	}
	if floor, capS := s.paceFloorCapFor(1, false, 1); floor != 10 || capS != 60 {
		t.Fatalf("中饵区间应为 [10,60]，实得 [%g,%g]", floor, capS)
	}
	if floor, capS := s.paceFloorCapFor(0, false, 1); floor != 20 || capS != 60 {
		t.Fatalf("差饵区间应为 [20,60]，实得 [%g,%g]", floor, capS)
	}
	if floor, capS := s.paceFloorCapFor(2, true, 1); floor != 3 || capS != 30 {
		t.Fatalf("海底钓窗应为 [3,30]，实得 [%g,%g]", floor, capS)
	}
	if floor, capS := s.paceFloorCapFor(2, false, 0.38); floor != 3 || capS != 60 {
		t.Fatalf("打窝压缩后好饵区间应为 [3,60]，实得 [%g,%g]", floor, capS)
	}
	// 满窝好饵：样本上限 46.6×0.38=17.7，落在 3~18 秒目标带。
	if got := s.paceStandardBiteDelay(46.6, 1, 1, 2, 0.38, false, 1); got < 3 || got > 18 {
		t.Fatalf("满窝好饵样本应落在 3~18 秒，实得 %g", got)
	}
	// 样本下限 6.48×0.38=2.46 → 窝料地板 3s。
	if got := s.paceStandardBiteDelay(6.48, 1, 1, 2, 0.38, false, 1); got != 3 {
		t.Fatalf("窝料地板应为 3s，实得 %g", got)
	}
}

func TestPaceFloorShrink(t *testing.T) {
	s, _ := newDetachTestServer(t)
	// P2-1 之后落水保护受开关门控，本测试验证开启态的压缩数值。
	setWeatherTestTunable(t, s, "bite_pace_enabled", "1")
	st := &FishState{PaceKnown: true, PaceGround: 0.38}
	cfg := biteDelaySettings{}
	// 6s 基础保护 ×0.38=2.28 → 抬到窝料地板 3s（只对满窝压缩生效）。
	if got := s.paceFloorShrink(cfg, st, false, 6*time.Second); got != 3*time.Second {
		t.Fatal(got)
	}
	// 只缩短不拉长：极小倍率下 800ms 的基础保护不受 3s 地板反向抬高。
	small := biteDelaySettings{scale: 0.13} // 6×0.13≈0.8s
	if got := s.paceFloorShrink(small, st, false, 800*time.Millisecond); got != 800*time.Millisecond {
		t.Fatal(got)
	}
	// fixed 指定延迟原样保留；路亚固定保护不动；无窝不动。
	if got := s.paceFloorShrink(biteDelaySettings{fixed: 5}, st, false, 5*time.Second); got != 5*time.Second {
		t.Fatal(got)
	}
	if got := s.paceFloorShrink(cfg, st, true, 1200*time.Millisecond); got != 1200*time.Millisecond {
		t.Fatal(got)
	}
	st.PaceGround = 1
	if got := s.paceFloorShrink(cfg, st, false, 6*time.Second); got != 6*time.Second {
		t.Fatal(got)
	}
	// PaceKnown=false（重启恢复）按中性处理。
	st.PaceGround, st.PaceKnown = 0.38, false
	if got := s.paceFloorShrink(cfg, st, false, 6*time.Second); got != 6*time.Second {
		t.Fatal(got)
	}
}

func TestPaceLegacyEquivalenceWhenDisabled(t *testing.T) {
	s, _ := newDetachTestServer(t) // bite_pace_enabled 默认 0
	st := &FishState{RigType: 12, PaceKnown: true, PaceFactor: 3, PaceGround: 0.38}
	// 开关关闭：pace 字段即使有值也不参与，结果必须落在 legacy 通道 [6,20]
	//（bite_delay_max 默认 20；pace 模式同因子会给 [19.4,60]）。
	for i := 0; i < 50; i++ {
		got := s.biteDelayInWeather(st, fishingWeather{}, time.Now())
		if got < 6-1e-9 || got > 20+1e-9 {
			t.Fatalf("legacy 通道越界：got %g", got)
		}
	}
}

func TestPaceDelayBoundsAndLureUnchanged(t *testing.T) {
	s, _ := seedPaceTest(t)
	now := time.Now()
	// 底层杆差饵（×3，区间 [20,60]）：单轮 [20, 60]；legacy 同上限是 20，越界即证明 pace 生效。
	bad := &FishState{RigType: 12, PaceKnown: true, PaceTier: 0, PaceFactor: 3, PaceGround: 1, PaceProb: 0.12}
	over20 := 0
	for i := 0; i < 50; i++ {
		got := s.biteDelayInWeather(bad, fishingWeather{}, now)
		if got < 19.44-1e-9 || got > 60+1e-9 {
			t.Fatalf("差饵单轮越界：got %g", got)
		}
		if got > 20 {
			over20++
		}
	}
	if over20 == 0 {
		t.Fatal("50 次采样全部 ≤20s，pace 分支未生效")
	}
	// 打窝压缩：全部 ≤17.71 且下限踩到 3s 地板（legacy 最低 6s）。
	compressed := &FishState{RigType: 12, PaceKnown: true, PaceTier: 2, PaceFactor: 1, PaceGround: 0.38}
	minGot := 1e9
	for i := 0; i < 200; i++ {
		got := s.biteDelayInWeather(compressed, fishingWeather{}, now)
		if got > 17.71+1e-9 {
			t.Fatalf("满窝样本越界：got %g", got)
		}
		if got < minGot {
			minGot = got
		}
	}
	if minGot > 3+1e-9 {
		t.Fatalf("满窝下限应踩到 3s 地板，最小实得 %g", minGot)
	}
	// 海底钓（marineRigType 且非路亚族）：上限 30。
	marine := &FishState{RigType: 55, PaceKnown: true, PaceTier: 2, PaceFactor: 1, PaceGround: 1}
	for i := 0; i < 200; i++ {
		if got := s.biteDelayInWeather(marine, fishingWeather{}, now); got > 30+1e-9 {
			t.Fatalf("海底钓上限应为 30s，实得 %g", got)
		}
	}
	// 路亚族在 pace 模式下延迟链默认零改动：×3 因子也不放大，仍是 1.5~6s 窗。
	lure := &FishState{RigType: 5, PaceKnown: true, PaceTier: 0, PaceFactor: 3, PaceGround: 1, PaceLureProb: 0.02}
	for i := 0; i < 50; i++ {
		got := s.biteDelayInWeather(lure, fishingWeather{}, now)
		if got > 6+1e-9 {
			t.Fatalf("路亚窗口被 pace 放大：got %g", got)
		}
	}
}

func TestBaitPaceScoreEvidence(t *testing.T) {
	s, _ := seedPaceTest(t)
	ctx := t.Context()
	base := fishRollContext{now: time.Now()}
	cases := []struct {
		name  string
		loc   string
		baits []string
		want  float64
	}{
		// rule_worm：companion tier3 → 1.0（食性票 0.72 不覆盖证据票）。
		{"companion tier3", "level_900_rules", []string{"rule_worm"}, 1.0},
		// rule_other：tier1 → 0.6；食性票（名册内 frog worm 1.69×0.8 → 封顶 0.6）
		// 同为 0.6。家族对口封顶 mid，到不了 good。
		{"tier1 plus diet vote", "level_900_rules", []string{"rule_other"}, 0.6},
		// rule_neutral：无证据 → 0.30 基线，名册内 frog 吃虫把食性票抬到封顶 0.6。
		{"diet vote only", "level_900_rules", []string{"rule_neutral"}, 0.6},
		// rule_lure：拟饵无证据 → 0.30 基线（与天然饵同口径），食性票
		// 0.2/2=0.1 不覆盖基线。
		{"lure weak", "level_900_rules", []string{"rule_lure"}, 0.30},
		{"multi bait best", "level_900_rules", []string{"rule_lure", "rule_worm"}, 1.0},
		{"unknown loc", "unknown-map", []string{"rule_worm"}, 0.5},
		{"unknown bait", "level_900_rules", []string{"missing_bait"}, 0.5},
		{"no bait", "level_900_rules", nil, 0.5},
	}
	for _, tc := range cases {
		if got := s.baitPaceScore(ctx, base, tc.loc, tc.baits, 1); math.Abs(got-tc.want) > 1e-9 {
			t.Fatalf("%s: got %g want %g", tc.name, got, tc.want)
		}
	}
	// 拟饵的证据票：rf4 占比 10% 应把无证据拟饵抬到 0.9（审查 P2-3）。
	s.rf4ShareMu.Lock()
	s.rf4ShareCache = map[string]map[string]float64{"c.roach": {"rule_lure": 10}}
	s.rf4ShareLoaded = true
	s.rf4ShareMu.Unlock()
	if got := s.baitPaceScore(ctx, base, "level_900_rules", []string{"rule_lure"}, 1); math.Abs(got-0.9) > 1e-9 {
		t.Fatalf("lure rf4 share evidence ignored: got %g want 0.9", got)
	}
	// 食性票对拟饵的「对口」判定：名册内出现 lure 2.0 的鱼后，拟饵满分。
	if _, err := s.DB.SQL.Exec(`UPDATE fish_ecology SET diet_json='{"lure":2.0,"fish":1.0}' WHERE fish_key='c.roach'`); err != nil {
		t.Fatal(err)
	}
	s.invalidateFishEcologyCache()
	if got := s.baitPaceScore(ctx, base, "level_900_rules", []string{"rule_lure"}, 5); math.Abs(got-1.0) > 1e-9 {
		t.Fatalf("lure 食性 2.0 应满分，实得 %g", got)
	}
}

func TestGroundbaitPaceFactorCompressesAndExemptsLure(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	const uid = int64(1000001)
	if got := s.groundbaitPaceFactor(ctx, uid, "gb-test-loc", 300, 300, true, 7, 1); got != 1 {
		t.Fatalf("无窝应中性，实得 %g", got)
	}
	// 8 份满强度：S=1-e^(-2)≈0.865，gain 0.62 → ≈0.464。
	s.groundbaitDeposit(ctx, uid, "gb-test-loc", 300, 300, true, 7, "feed_karp", 8, "feeder", "gb:p1", "")
	full := s.groundbaitPaceFactor(ctx, uid, "gb-test-loc", 300, 300, true, 7, 1)
	if full <= 0.40 || full >= 0.55 {
		t.Fatalf("满窝压缩应落在 0.40~0.55，实得 %.3f", full)
	}
	// 路亚族豁免（与死窝闸门同口径）。
	if got := s.groundbaitPaceFactor(ctx, uid, "gb-test-loc", 300, 300, true, 7, 5); got != 1 {
		t.Fatalf("路亚族应豁免，实得 %g", got)
	}
	// 死窝不加速：8+20=28 份，overdoseP=0 → 回 1。
	s.groundbaitDeposit(ctx, uid, "gb-test-loc", 300, 300, true, 7, "feed_karp", 20, "feeder", "gb:p2", "")
	if got := s.groundbaitPaceFactor(ctx, uid, "gb-test-loc", 300, 300, true, 7, 1); got != 1 {
		t.Fatalf("死窝不应压缩，实得 %.3f", got)
	}
}

func TestPaceProbForRigFamily(t *testing.T) {
	st := &FishState{}
	if st.paceProbFor() != 1 {
		t.Fatal("PaceKnown=false 必须中性")
	}
	st.PaceKnown, st.PaceProb, st.PaceLureProb = true, 0.5, 0.3
	st.RigType = 1
	if st.paceProbFor() != 0.5 {
		t.Fatal("底层杆应取 PaceProb")
	}
	st.RigType = 5
	if st.paceProbFor() != 0.3 {
		t.Fatal("路亚轮应取 PaceLureProb")
	}
}

func TestScheduleBiteStoresPace(t *testing.T) {
	s, _ := seedPaceTest(t)
	const uid = int64(1000001)
	addFishingRecoverySession(s, uid, 911)
	sess := s.liveSessionByUID(uid)
	st := s.stateFor(uid)
	st.mu.Lock()
	st.GearGuid, st.Loc, st.Bait, st.RigType = "", "level_900_rules", "", 12
	st.mu.Unlock()
	if !s.scheduleBite(sess, st) {
		t.Fatal("scheduleBite 被拒")
	}
	st.mu.Lock()
	known, factor, prob := st.PaceKnown, st.PaceFactor, st.PaceProb
	timer := st.biteTimer
	st.mu.Unlock()
	stopWeatherDamageTimer(st)
	if !known {
		t.Fatal("pace 因子未落库")
	}
	// 无饵（currentBaits 为空）→ 对口分中性 0.5 → mid 档。
	if factor != 1.6 || prob != 0.5 {
		t.Fatalf("中性 0.5 应落 mid 档（1.6/0.5），实得 (%g,%g)", factor, prob)
	}
	_ = timer
}

func TestEmptyRollReusesPaceFactors(t *testing.T) {
	s, _ := newRigTestServer(t)
	setWeatherTestTunable(t, s, "bite_pace_enabled", "1")
	const uid = int64(1000001)
	addFishingRecoverySession(s, uid, 911)
	sess := s.liveSessionByUID(uid)
	st := s.stateFor(uid)
	st.mu.Lock()
	st.CastActive, st.InWater, st.Hooked = true, true, false
	st.CastTs = time.Now().Add(-time.Minute)
	st.RigType = 12
	st.storePaceState(fishPaceState{Known: true, Score: 0.1, Factor: 3, Prob: 0.12, LureProb: 0.02, Ground: 1, Tier: 0})
	s.emptyWeatherBiteLocked(sess, st, time.Now())
	due, timer := st.BiteAt, st.biteTimer
	st.mu.Unlock()
	stopWeatherDamageTimer(st)
	if timer == nil {
		t.Fatal("空轮未重排")
	}
	// 空轮重排复用差饵因子：延迟样本 ×3 → [19.44, 60]。
	wait := time.Until(due).Seconds()
	if wait < 19.4-1e-9 || wait > 60+1e-9 {
		t.Fatalf("差饵空轮重排应落在 [19.44,60]s，实得 %.2fs", wait)
	}
}

func TestMeanPaceStandardDelayMatchesClamp(t *testing.T) {
	s, _ := newDetachTestServer(t)
	cfg := biteDelaySettings{scale: 1}
	var sum float64
	for _, v := range officialPushDelayEligible {
		sum += v
	}
	want := sum / float64(len(officialPushDelayEligible))
	// 好饵无窝：所有样本都在 [6,60] 内 → 均值 = 样本均值。
	if got := s.meanPaceStandardDelay(cfg, 1, 1, 2, 1, false); math.Abs(got-want) > 1e-9 {
		t.Fatalf("好饵均值应等于样本均值 %g，实得 %g", want, got)
	}
	// 满窝好饵：均值落在 3~20 秒带。
	compressed := s.meanPaceStandardDelay(cfg, 1, 1, 2, 0.38, false)
	if compressed < 3 || compressed > 20 {
		t.Fatalf("满窝均值应落在 3~20 秒带，实得 %g", compressed)
	}
	if compressed >= want*0.5 {
		t.Fatalf("满窝均值应明显压缩（<%.2f），实得 %g", want*0.5, compressed)
	}
	// 海底钓：上限 30。
	if got := s.meanPaceStandardDelay(cfg, 1, 1, 2, 1, true); got > 30 {
		t.Fatalf("海底钓均值应 ≤30，实得 %g", got)
	}
}

func TestFishQueryPaceState(t *testing.T) {
	s, _ := seedPaceTest(t)
	base := fishRollContext{now: time.Now()}
	st := &FishState{Loc: "level_900_rules", Bait: "rule_worm", RigType: 1}
	p := s.fishQueryPaceState(t.Context(), base, st)
	if !p.Known || p.Score != 1.0 || p.Factor != 1.0 || p.Prob != 1.0 || p.Ground != 1 {
		t.Fatalf("对口饵 pace 状态不符：%+v", p)
	}
	// pace 开关关闭：中性。
	if err := s.DB.TunableSet(t.Context(), "bite_pace_enabled", "0", "test"); err != nil {
		t.Fatal(err)
	}
	s.tunMu.Lock()
	s.tunAt = time.Time{}
	s.tunMu.Unlock()
	p = s.fishQueryPaceState(t.Context(), base, st)
	if p.Known || p.Factor != 1 || p.Prob != 1 {
		t.Fatalf("开关关闭应中性：%+v", p)
	}
}

// ---- PR #531 审查修复的回归（5 个 P2）----

// P2-1：运行中热关闭 bite_pace_enabled 后，落水保护与判定概率立即回 legacy，
// 残留的 st.PaceKnown 不得继续生效。
func TestPaceHotDisableStopsFloorAndProb(t *testing.T) {
	s, _ := newDetachTestServer(t) // bite_pace_enabled=0
	st := &FishState{RigType: 12, PaceKnown: true, PaceGround: 0.38, PaceProb: 0.12, PaceLureProb: 0.02}
	cfg := biteDelaySettings{}
	if got := s.paceFloorShrink(cfg, st, false, 6*time.Second); got != 6*time.Second {
		t.Fatalf("disabled pace still shrank floor: got %v want 6s", got)
	}
	if got := s.rodPaceProb(st); got != 1 {
		t.Fatalf("disabled pace still scaled prob: got %g want 1", got)
	}
	// 对照：开关开启时同一状态照常生效。
	setWeatherTestTunable(t, s, "bite_pace_enabled", "1")
	if got := s.paceFloorShrink(cfg, st, false, 6*time.Second); got != 3*time.Second {
		t.Fatalf("enabled pace lost floor shrink: got %v want 3s", got)
	}
	if got := s.rodPaceProb(st); got != 0.12 {
		t.Fatalf("enabled pace lost prob: got %g want 0.12", got)
	}
}

// P2-2：补排/嘬饵否决/脱钩重排（resetCast=false）复用快照，不得把
// PaceKnown 抹成 false，也不得按 legacy 延迟重排。
func TestPaceReschedulePreservesKnown(t *testing.T) {
	s, _ := seedPaceTest(t)
	const uid = int64(1000001)
	addFishingRecoverySession(s, uid, 911)
	sess := s.liveSessionByUID(uid)
	st := s.stateFor(uid)
	st.mu.Lock()
	st.GearGuid, st.Loc, st.RigType = "", "level_900_rules", 12
	st.storePaceState(fishPaceState{Known: true, Score: 0.1, Factor: 3, Prob: 0.12, LureProb: 0.02, Ground: 1, Tier: 0})
	st.mu.Unlock()

	pace := s.computePaceState(sess, st, false)
	if !pace.Known {
		t.Fatal("snapshot dropped Known")
	}
	s.scheduleBiteForHooked(sess, st)
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.PaceKnown {
		t.Fatal("re-schedule lost pace: PaceKnown=false")
	}
	wait := time.Until(st.BiteAt).Seconds()
	// time.Until 会消耗调度和测试开销，留出 10ms 级边界余量。
	if wait < 19.3 || wait > 60+1e-9 {
		t.Fatalf("re-schedule fell back to legacy delay: got %.2fs want [19.3,60]", wait)
	}
	stopWeatherDamageTimer(st)
}

// P2-4：名册食性缓存按 statLoc 独立过期——一张新图写缓存不得复活
// 其他地图的已过期条目；fish_ecology 重载时整体清空。
func TestPaceDietCachePerLocTTLAndReloadClear(t *testing.T) {
	s, _ := seedPaceTest(t)
	ctx := t.Context()
	base := fishRollContext{now: time.Now()}
	roster, err := s.cachedLocRoster(ctx, base, "rules-test")
	if err != nil || len(roster) == 0 {
		t.Fatal(err)
	}
	fresh := &paceRosterDiet{dietMax: map[string]float64{}}
	s.paceDietMu.Lock()
	s.paceDietByLoc = map[string]paceDietCacheEntry{
		"rules-test": {diet: fresh, at: time.Now()},
		"other-loc":  {diet: fresh, at: time.Now().Add(-2 * paceDietTTL)},
	}
	s.paceDietMu.Unlock()
	// 新鲜条目直接复用。
	if got := s.paceRosterDietFor(ctx, base, "rules-test", roster); got != fresh {
		t.Fatal("fresh entry was rebuilt")
	}
	// 过期条目重建，即使另一张图刚刷新过。
	got := s.paceRosterDietFor(ctx, base, "other-loc", roster)
	if got == fresh {
		t.Fatal("expired per-location diet entry was revived by another location's refresh")
	}
	// fish_ecology 重载后整体清空。
	if err := s.reloadFishEcologyCache(ctx); err != nil {
		t.Fatal(err)
	}
	s.paceDietMu.Lock()
	cleared := s.paceDietByLoc == nil
	s.paceDietMu.Unlock()
	if !cleared {
		t.Fatal("ecology reload did not clear pace diet cache")
	}
}

// P2-5：多钩钓组每钩独立评分取最好一钩（与 rollFishForRig「任意已挂饵
// 钩位都可能出鱼」同语义）；空组中性。
func TestPaceScoreForGroups(t *testing.T) {
	s, _ := seedPaceTest(t)
	ctx := t.Context()
	base := fishRollContext{now: time.Now()}
	cases := []struct {
		name   string
		groups [][]string
		want   float64
	}{
		{"no groups", nil, 0.5},
		{"empty group", [][]string{{}}, 0.5},
		{"single mid hook", [][]string{{"rule_neutral"}}, 0.6},
		{"best hook wins", [][]string{{"rule_neutral"}, {"rule_worm"}}, 1.0},
		{"best hook wins reversed", [][]string{{"rule_worm"}, {"rule_neutral"}}, 1.0},
		{"both weak", [][]string{{"rule_neutral"}, {"rule_lure"}}, 0.6},
	}
	for _, tc := range cases {
		if got := s.paceScoreForGroups(ctx, base, "level_900_rules", tc.groups, 1); math.Abs(got-tc.want) > 1e-9 {
			t.Fatalf("%s: got %g want %g", tc.name, got, tc.want)
		}
	}
}
