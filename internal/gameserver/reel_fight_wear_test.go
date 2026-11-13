package gameserver

// 搏鱼受力磨损回归：磨损只按受力时间扣（官服抓包口径），拉力明显超过泄力（>1.05×
// 门槛）才打滑磨摩擦片；速率对齐官服实测（0.01%/41.7s ≈ 0.00024%/s）。
// 摩擦片剩余寿命同步压低泄力，结算每竿一次、重发不重复扣。
// 受力秒数按服务端收到 14/8 的真实间隔累加：客户端上报密度会变，固定节拍折算会把
// 账记错。入账的鱼拉力必须是公斤力（27.f8 是牛，进账前换算）。
// 口径见 specs/changes/wear-fight-time/spec.md 与 specs/changes/reel-wear-pull-unit/spec.md。

import (
	"context"
	"math"
	"testing"
	"time"

	"rf4server/internal/logging"
)

// newFightWearForTest 造一条搏鱼受力账：ratedKg 是摩擦片额定，drag 是此刻泄力
// （29 档即锁轮承力），pull 是鱼拉力（公斤力），oilWear 是轮油打滑系数。
func newFightWearForTest(rated, drag, pull, oilWear float64) fightReelWear {
	return fightReelWear{
		valid: true, fishGUID: "fish0000000000000000000000000001",
		rodGUID: "rod0000000000000000000000000000001", reelGUID: "reel000000000000000000000000000001",
		setting: 25, dragKg: drag, ratedKg: rated,
		oilWear: oilWear, pullKg: pull,
	}
}

// TestFightReelStressBilledWithoutSlip 钉住官服口径：磨损按受力时间扣，与是否打滑
// 无关——n.pike 全程无打滑（拉力不超泄力）但轮件照样按秒磨损；只有超泄力才记打滑。
func TestFightReelStressBilledWithoutSlip(t *testing.T) {
	srv := &Server{}
	tick := defaultReelLoadTickSeconds
	// 拉力低于打滑门槛（泄力×1.05）：无打滑，但受力秒照记。
	w := newFightWearForTest(7.7, 5, 5, 1)
	ticks, slip, stressSecs, slipSecs := srv.fightReelTickWear(w, tick)
	if ticks != 0 || slip != 0 || slipSecs != 0 {
		t.Fatalf("拉力不超泄力不应打滑，实得 ticks=%d slip=%v", ticks, slip)
	}
	if stressSecs != tick {
		t.Fatalf("无打滑也要记受力秒 %.2f，实得 %.2f", tick, stressSecs)
	}
	// 拉力等于门槛以下边界（1.04×，官服零磨损样本）：不打滑。
	w = newFightWearForTest(7.7, 5, 5*1.04, 1)
	if ticks, slip, _, _ := srv.fightReelTickWear(w, tick); ticks != 0 || slip != 0 {
		t.Fatalf("1.04× 泄力不应记打滑（官服实测零磨损），实得 ticks=%d slip=%v", ticks, slip)
	}
	// 超过门槛（1.5×）：记一次打滑。
	w = newFightWearForTest(7.7, 5, 7.5, 1)
	ticks, slip, _, slipSecs = srv.fightReelTickWear(w, tick)
	if ticks != 1 || slip <= 0 || slipSecs != tick {
		t.Fatalf("超泄力应记一次打滑，实得 ticks=%d slip=%v secs=%v", ticks, slip, slipSecs)
	}
	// 零时长上报什么都不记。
	if ticks, slip, stress, secs := srv.fightReelTickWear(w, 0); ticks != 0 || slip != 0 || stress != 0 || secs != 0 {
		t.Fatalf("零时长不该产生任何账：ticks=%d slip=%v stress=%v secs=%v", ticks, slip, stress, secs)
	}
}

func TestLongHugeFishSlipWearNoOverloadScaling(t *testing.T) {
	srv := &Server{}
	// 2026-11-09 实机：28.373kg 鲨鱼换算 46.53kgf，泄力档实际 19.04kg，
	// 一条长搏斗累计 38 条 14/8。旧实现把超出比例再乘一次，摩擦片直接掉 34.2%。
	// 拍平后（2026-11-12）速率不再读档位，断言钉住"速率不随超载复乘"这一点。
	w := newFightWearForTest(20.9, 19.04, 46.53, 0.9)
	w.setting = 21
	var total float64
	for i := 0; i < 38; i++ {
		ticks, slip, _, _ := srv.fightReelTickWear(w, reelSlipCalibrationSeconds)
		if ticks != 1 {
			t.Fatalf("第 %d 次应只记打滑，实得 ticks=%d", i+1, ticks)
		}
		total += slip
	}
	want := 38 * reelSlipWear * 0.9
	if math.Abs(total-want) > 1e-12 {
		t.Fatalf("38 次打滑应累计 %.4f，实得 %.4f", want, total)
	}
	if total > 0.005 {
		t.Fatalf("38 次未锁轮打滑不应超过 0.5%%，实得 %.4f", total)
	}
}

func TestFightReelSlipWearFlatAcrossSettings(t *testing.T) {
	srv := &Server{}
	// 2026-11-12 拍平：逐档阶梯摘除，所有档位同一基准率（官服仅 W5 一个速率点）。
	for _, setting := range []int64{0, 21, 24, 25, 26, 27, 28, 29} {
		w := newFightWearForTest(7.7, 5, 7.5, 1)
		w.setting = setting
		_, slip, _, _ := srv.fightReelTickWear(w, reelSlipCalibrationSeconds)
		if math.Abs(slip-reelSlipWear) > 1e-12 {
			t.Fatalf("%d 档打滑磨损=%v，期望统一基准 %v", setting, slip, reelSlipWear)
		}
	}
}

// TestFightReelSlipWearMatchesOfficial 钉住官服校准点：连续出线的磨损率
// ≈0.00024%/s——W5 实测 112s 重度打滑磨 0.027%。期望值取抓包字面量，不用实现复算。
// 拍平后（2026-11-12）速率不分档位，这里仍以 29 档（锁轮）走过同一通道作代表。
func TestFightReelSlipWearMatchesOfficial(t *testing.T) {
	srv := &Server{}
	w := newFightWearForTest(7.7, 5, 50, 1)
	w.setting = 29
	var total float64
	for i := 0; i < 112; i++ {
		_, slip, _, _ := srv.fightReelTickWear(w, 1)
		total += slip
	}
	// 0.027% = 0.00027 寿命比例；官服单样本，给 ±25% 容差。
	const want = 0.00027
	if math.Abs(total-want) > want*0.25 {
		t.Fatalf("112s 重度打滑应磨 %.5f（官服 W5 实测 0.027%%），实得 %.5f", want, total)
	}
	// 换算口径：连续打滑烧完一套摩擦片约 115 小时，不再是旧的 5 小时。
	hours := 1 / (total / 112) / 3600
	if hours < 60 || hours > 180 {
		t.Fatalf("摩擦片按打滑小时数寿命应落在 60~180h（官服口径），实得 %.0fh", hours)
	}
}

func TestFightReelOilLowersSlipWear(t *testing.T) {
	srv := &Server{}
	tick := reelSlipCalibrationSeconds
	// 打滑：好油省、差油费，且都被收进 0.8~1.2 区间。
	w := newFightWearForTest(7.7, 5, 7.5, 1)
	_, normal, _, _ := srv.fightReelTickWear(w, tick)
	w.oilWear = 0.2
	_, best, _, _ := srv.fightReelTickWear(w, tick)
	w.oilWear = 3
	_, worst, _, _ := srv.fightReelTickWear(w, tick)
	if !(best < normal && normal < worst) {
		t.Fatalf("轮油应改变打滑磨损：最佳 %.6f 常规 %.6f 最差 %.6f", best, normal, worst)
	}
	baseWear := reelSlipWear
	if math.Abs(best-baseWear*0.8) > 1e-12 || math.Abs(worst-baseWear*1.2) > 1e-12 {
		t.Fatalf("轮油系数应被收进 0.8~1.2，实得 best=%.6f worst=%.6f", best, worst)
	}
}

// TestFightReelLockedUsesSameSlipChannel 锁轮不再是独立烧穿曲线（官服无锁轮样本，
// 2026-11-11 裁定删除）：29 档 dragKg 已是锁轮承力，出线打滑走同一条口径；
// 2026-11-12 拍平后锁轮与未锁轮连速率也相同（逐档阶梯摘除）。
func TestFightReelLockedUsesSameSlipChannel(t *testing.T) {
	srv := &Server{}
	// 锁轮（dragKg = 锁轮承力 7.7）且拉力 11.55（1.5×）：与未锁轮同速率打滑。
	locked := newFightWearForTest(7.7, 7.7, 11.55, 1)
	locked.setting = 29
	_, lockedSlip, lockedStress, _ := srv.fightReelTickWear(locked, reelSlipCalibrationSeconds)
	unlocked := newFightWearForTest(7.7, 5, 7.5, 1)
	_, unlockedSlip, unlockedStress, _ := srv.fightReelTickWear(unlocked, reelSlipCalibrationSeconds)
	if math.Abs(lockedSlip-reelSlipWear) > 1e-12 {
		t.Fatalf("锁轮打滑应按统一基准磨损 %.6f，实得 %.6f", reelSlipWear, lockedSlip)
	}
	if lockedStress != unlockedStress || lockedStress != reelSlipCalibrationSeconds {
		t.Fatalf("锁轮受力秒照记，实得 %v", lockedStress)
	}
	if math.Abs(lockedSlip-unlockedSlip) > 1e-12 {
		t.Fatalf("拍平后锁轮/未锁轮应同速率：locked=%.6f unlocked=%.6f", lockedSlip, unlockedSlip)
	}
}

func TestFightReelSlipWearScalesByElapsedSeconds(t *testing.T) {
	srv := &Server{}
	w := newFightWearForTest(7.7, 5, 7.5, 1)
	w.setting = 29

	_, short, _, _ := srv.fightReelTickWear(w, reelSlipCalibrationSeconds/2)
	_, long, _, _ := srv.fightReelTickWear(w, reelSlipCalibrationSeconds)
	if math.Abs(long-2*short) > 1e-12 {
		t.Fatalf("打滑磨损应按真实秒数线性累计：半节拍 %.9f，整节拍 %.9f", short, long)
	}
}

func TestFightReelDedicatedOilReducesWearRate(t *testing.T) {
	srv := &Server{}
	w := newFightWearForTest(7.7, 5, 7.5, 1)
	w.setting = 29
	w.oilMatch = reelOilMatchMismatch
	_, mismatch, _, _ := srv.fightReelTickWear(w, reelSlipCalibrationSeconds)
	w.oilMatch = reelOilMatchDedicated
	_, dedicated, _, _ := srv.fightReelTickWear(w, reelSlipCalibrationSeconds)
	if math.Abs(dedicated-mismatch*0.5) > 1e-12 {
		t.Fatalf("品牌专用油应把打滑磨损降到一半：不匹配 %.9f 专用 %.9f", mismatch, dedicated)
	}
}

func TestFightReelSettleWearsWasherAndLowersDrag(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	reel, err := srv.CreateItem(ctx, uid, "bclp_5004", "backpack", 1)
	if err != nil {
		t.Fatalf("造轮失败: %v", err)
	}
	mountReelOnRod(t, srv, rod.GUID, reel.GUID)

	row, err := db.GetItemByGUID(ctx, reel.GUID)
	if err != nil || row == nil {
		t.Fatalf("读取轮子失败: %v", err)
	}
	rated, ok := srv.reelWasherRatedFromRow(ctx, row)
	if !ok || rated <= 0 {
		t.Fatal("装配好的轮子应能读到摩擦片额定制动力")
	}
	before, perr := srv.syncReelPerformance(ctx, row)
	if perr != nil || before.BaseDragKg <= 0 || before.LockedDragKg <= 0 {
		t.Fatalf("轮子应有泄力与锁轮承力: %+v err=%v", before, perr)
	}

	st := srv.fishingStateForRod(uid, rod.GUID)
	st.mu.Lock()
	st.reelWear = newFightWearForTest(rated, before.BaseDragKg, before.BaseDragKg*3, 1)
	st.reelWear.rodGUID, st.reelWear.reelGUID = rod.GUID, reel.GUID
	st.reelWear.SlipWear = 0.50 // 本竿打滑累计烧掉一半寿命
	st.mu.Unlock()

	srv.settleFightReelWearState(ctx, st, uid, "14/4")

	afterRow, err := db.GetItemByGUID(ctx, reel.GUID)
	if err != nil || afterRow == nil {
		t.Fatalf("读取轮子失败: %v", err)
	}
	after, perr := srv.syncReelPerformance(ctx, afterRow)
	if perr != nil {
		t.Fatalf("重建轮子物理参数失败: %v", perr)
	}
	if math.Abs(after.BaseDragKg-before.BaseDragKg*0.5) > before.BaseDragKg*0.02 {
		t.Fatalf("烧掉一半寿命后泄力应约减半：before=%.3f after=%.3f", before.BaseDragKg, after.BaseDragKg)
	}
	// 统一口径：锁轮承力=规格×轴承耐久，不随摩擦片寿命衰减（规格表收录的轮子）；
	// 摩擦片基准制动（1~29 档泄力）已在上面的断言里验证减半。
	if math.Abs(after.LockedDragKg-before.LockedDragKg) > 1e-9 {
		t.Fatalf("锁轮承力不应随摩擦片磨损变化：before=%.3f after=%.3f", before.LockedDragKg, after.LockedDragKg)
	}

	// 每竿一次：重复结算不得再扣一次。
	srv.settleFightReelWearState(ctx, st, uid, "14/4")
	againRow, err := db.GetItemByGUID(ctx, reel.GUID)
	if err != nil || againRow == nil {
		t.Fatalf("读取轮子失败: %v", err)
	}
	again, perr := srv.syncReelPerformance(ctx, againRow)
	if perr != nil {
		t.Fatalf("重建轮子物理参数失败: %v", perr)
	}
	if math.Abs(again.BaseDragKg-after.BaseDragKg) > 1e-9 {
		t.Fatalf("重复结算不应重复扣耐久：%.6f -> %.6f", after.BaseDragKg, again.BaseDragKg)
	}

	// 烧穿：制动力归零，泄力与锁轮承力都失效，只能剪线后修。
	st.mu.Lock()
	st.reelWear = newFightWearForTest(rated, after.BaseDragKg, after.BaseDragKg*3, 1)
	st.reelWear.rodGUID, st.reelWear.reelGUID = rod.GUID, reel.GUID
	st.reelWear.SlipWear = 1
	st.mu.Unlock()
	srv.settleFightReelWearState(ctx, st, uid, "14/4")
	deadRow, err := db.GetItemByGUID(ctx, reel.GUID)
	if err != nil || deadRow == nil {
		t.Fatalf("读取轮子失败: %v", err)
	}
	dead, perr := srv.syncReelPerformance(ctx, deadRow)
	if perr != nil {
		t.Fatalf("重建轮子物理参数失败: %v", perr)
	}
	if dead.BaseDragKg > 1e-9 || dead.LockedDragKg > 1e-9 {
		t.Fatalf("摩擦片烧穿后泄力应失效，实得 base=%.6f locked=%.6f", dead.BaseDragKg, dead.LockedDragKg)
	}
}

func TestFightReelWearWithoutReelIsSafe(t *testing.T) {
	srv, _ := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	handRod, err := srv.CreateItem(ctx, uid, "tele_10012_tl16", "backpack", 1)
	if err != nil {
		t.Fatalf("造手竿失败: %v", err)
	}
	if got := srv.fightReelCache(ctx, uid, handRod.GUID, &RolledFish{FishGUID: "fishx"}); got.valid {
		t.Fatal("手竿没有渔轮，不应生成磨损账")
	}
	// 空账与空 GUID 都不该写库、不该 panic。
	srv.settleFightReelWear(ctx, uid, handRod.GUID, "14/4")
	srv.settleFightReelWear(ctx, uid, "", "14/4")
	// 没在搏鱼时收到 14/8 也必须安全返回。
	sess := newProbeSess(srv)
	srv.recordFightReelLoad(&Ctx{Sess: sess, Main: 14, Sub: 8, Tree: nil, DecodeOK: true, Log: logging.Discard()})
}

// TestFightReelLoadTickUsesRealGaps 断受力时长的折算口径：本竿第一条上报按兜底
// 节拍起算，之后按两条上报之间的真实秒数，间隔过大只记上限，时钟回拨不倒扣。
func TestFightReelLoadTickUsesRealGaps(t *testing.T) {
	base := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	w := fightReelWear{}
	if got := w.loadTickSecondsLocked(base, defaultReelLoadTickSeconds, defaultReelLoadMaxTickSeconds); got != defaultReelLoadTickSeconds {
		t.Fatalf("首条上报应按兜底节拍 %.2fs 起算，实得 %.2fs", defaultReelLoadTickSeconds, got)
	}
	w.lastLoadAt = base
	cases := []struct {
		name   string
		offset time.Duration
		want   float64
	}{
		{"上报比节拍快只记真实间隔", 600 * time.Millisecond, 0.6},
		{"上报比节拍慢记到 4 秒", 4 * time.Second, 4},
		{"迟到上报只记上限", 40 * time.Second, defaultReelLoadMaxTickSeconds},
		{"时钟回拨不倒扣", -2 * time.Second, 0},
	}
	for _, tc := range cases {
		got := w.loadTickSecondsLocked(base.Add(tc.offset), defaultReelLoadTickSeconds, defaultReelLoadMaxTickSeconds)
		if math.Abs(got-tc.want) > 1e-9 {
			t.Fatalf("%s: 应折算 %.2fs，实得 %.2fs", tc.name, tc.want, got)
		}
	}
}

func TestFightReelDragChangeKeepsLastLoadBoundary(t *testing.T) {
	srv, _ := newRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	reel, err := srv.CreateItem(ctx, uid, "spin_5820", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	mountReelOnRod(t, srv, rod.GUID, reel.GUID)
	fish := &RolledFish{FishGUID: "drag-change-time", Species: "carp", WeightG: 50_000, Grade: 3}
	cached := srv.fightReelCache(ctx, uid, rod.GUID, fish)
	if !cached.valid || cached.dragKg <= 0 || cached.pullKg <= cached.dragKg {
		t.Fatalf("测试账本前置条件不成立: %+v", cached)
	}
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.Pending, st.GearGuid = fish, rod.GUID
	st.CastActive, st.InWater, st.Hooked = true, true, true
	st.reelWear = cached
	st.reelWear.lastLoadAt = time.Now().Add(-100 * time.Millisecond)
	st.reelWear.invalidate()
	st.mu.Unlock()

	srv.recordFightReelLoad(&Ctx{Sess: newProbeSess(srv), Main: 14, Sub: 8, DecodeOK: true,
		Log: logging.Discard()})

	st.mu.Lock()
	got := st.reelWear.SlipSecs
	st.mu.Unlock()
	if got >= 1.0 {
		t.Fatalf("改摩擦档后真实间隔被按兜底计时: SlipSecs=%.2fs", got)
	}
}

// TestFightReelStressSecondsFollowRealGaps 断受力秒数不再假设"一条上报 = 1.9 秒"：
// 客户端上报翻倍密集时，累计受力秒只跟着真实时间走，磨损不会被虚增时长多扣。
func TestFightReelStressSecondsFollowRealGaps(t *testing.T) {
	srv := &Server{}
	base := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	ledger := newFightWearForTest(7.7, 5, 7.5, 1)
	var secs, slip float64
	for i := 0; i < 8; i++ {
		at := base.Add(time.Duration(i) * 500 * time.Millisecond)
		tick := ledger.loadTickSecondsLocked(at, defaultReelLoadTickSeconds, defaultReelLoadMaxTickSeconds)
		_, slipWear, stress, _ := srv.fightReelTickWear(ledger, tick)
		slip += slipWear
		secs += stress
		ledger.lastLoadAt = at
	}
	// 首条 1.9 秒兜底，其余 7 条真实间隔 0.5 秒。
	if want := defaultReelLoadTickSeconds + 7*0.5; math.Abs(secs-want) > 1e-9 {
		t.Fatalf("受力秒应按真实时长 %.2fs 累加，实得 %.2fs", want, secs)
	}
	// 同样的 8 条上报，按旧口径一律折 1.9 秒会多记近三倍时长，磨损必须更狠。
	fixed := newFightWearForTest(7.7, 5, 7.5, 1)
	var fixedSlip, fixedStress float64
	for i := 0; i < 8; i++ {
		_, slipWear, stress, _ := srv.fightReelTickWear(fixed, defaultReelLoadTickSeconds)
		fixedSlip += slipWear
		fixedStress += stress
	}
	if !(secs < fixedStress && slip < fixedSlip) {
		t.Fatalf("密集上报按真实时长结算应更省：受力 %.2f vs %.2f，打滑 %.6f vs %.6f",
			secs, fixedStress, slip, fixedSlip)
	}
	// 同一瞬间的重复上报不带时长，也就一分账都不该记。
	dup := newFightWearForTest(7.7, 5, 7.5, 1)
	if ticks, slipWear, stress, slipSecs := srv.fightReelTickWear(dup, 0); ticks != 0 || slipWear != 0 || stress != 0 || slipSecs != 0 {
		t.Fatalf("零时长上报不该产生任何账：ticks=%d slip=%v stress=%v secs=%v",
			ticks, slipWear, stress, slipSecs)
	}
}

// TestFishFightPullKgfMatchesKTable 钉住换算恒等式：27.f8 是牛，磨损账比较的
// 泄力是公斤，入账前除以 newtonPerKgf。期望值由逐鱼种 K 表公式手算
// （perch K0=17.6，f8 = 0.461kg × (17.6 + 0.35·ln(0.461)) ≈ 7.9887N ≈ 0.8146kgf），
// 不用实现代码复算。
func TestFishFightPullKgfMatchesKTable(t *testing.T) {
	srv := &Server{}
	f := &RolledFish{FishGUID: "fish0000000000000000000000000001", Species: "perch", WeightG: 461, Grade: 2}
	// fishFightPullFor 末尾四舍五入到万分位：牛数最大偏差 5e-5，折公斤力约 5.1e-6。
	const tol = 1e-4
	wantN := 0.461 * (17.6 + fishPullLnSlope*math.Log(0.461))
	if got, want := srv.fishFightPullFor(f), wantN; math.Abs(got-want) > tol {
		t.Fatalf("27.f8 应为 K 表牛数 %.6f，实得 %.6f", want, got)
	}
	if got, want := srv.fishFightPullKgfFor(f), wantN/newtonPerKgf; math.Abs(got-want) > tol {
		t.Fatalf("公斤力拉力应为牛数/newtonPerKgf %.6f，实得 %.6f", want, got)
	}
}

// TestFightReelWearBoundaryUsesKgfNotNewton 用实机事故数字做账本边界正反对照
// （2026-11-09 01:37：461g 鱼、1.87kg 泄力、3.2kg 片、9 条 14/8 全部封顶打滑）：
// 把 f8 牛数当公斤入账的旧口径必打滑，公斤力口径零打滑（受力秒照记）。回滚后此测试变红。
func TestFightReelWearBoundaryUsesKgfNotNewton(t *testing.T) {
	srv := &Server{}
	f := &RolledFish{FishGUID: "fish0000000000000000000000000002", Species: "perch", WeightG: 461, Grade: 2}
	oldWay := newFightWearForTest(3.2, 1.87, srv.fishFightPullFor(f), 1.175)
	if ticks, slip, _, _ := srv.fightReelTickWear(oldWay, defaultReelLoadTickSeconds); ticks != 1 || slip <= 0 {
		t.Fatalf("对照失效：牛当公斤的旧口径应判打滑，实得 ticks=%d slip=%v", ticks, slip)
	}
	newWay := newFightWearForTest(3.2, 1.87, srv.fishFightPullKgfFor(f), 1.175)
	ticks, slip, stress, slipSecs := srv.fightReelTickWear(newWay, defaultReelLoadTickSeconds)
	if ticks != 0 || slip != 0 || slipSecs != 0 {
		t.Fatalf("461g 鱼对 1.87kg 泄力不应打滑: ticks=%d slip=%v secs=%v", ticks, slip, slipSecs)
	}
	if stress != defaultReelLoadTickSeconds {
		t.Fatalf("不打滑也要记受力秒 %.2f，实得 %.2f", defaultReelLoadTickSeconds, stress)
	}
}

// TestFightReelCachePullKgfNoSlipForSmallFish 过真实装配接线复现同一场事故：
// fightReelCache 是牛→公斤的换算点，461g 鱼入账后必须低于该轮泄力且整场零打滑。
// 正控断言 f8 牛数确实高于泄力——保证这条测试在旧代码下真的会红。
func TestFightReelCachePullKgfNoSlipForSmallFish(t *testing.T) {
	srv, _ := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	reel, err := srv.CreateItem(ctx, uid, "bclp_5004", "backpack", 1)
	if err != nil {
		t.Fatalf("造轮失败: %v", err)
	}
	mountReelOnRod(t, srv, rod.GUID, reel.GUID)
	f := &RolledFish{FishGUID: "fish0000000000000000000000000003", Species: "perch", WeightG: 461, Grade: 2}
	cached := srv.fightReelCache(ctx, uid, rod.GUID, f)
	if !cached.valid || cached.ratedKg <= 0 || cached.dragKg <= 0 {
		t.Fatalf("应生成有效磨损账: %+v", cached)
	}
	if cached.pullKg >= cached.dragKg {
		t.Fatalf("461g 鱼的公斤力拉力 %.4f 不应达到泄力 %.2f", cached.pullKg, cached.dragKg)
	}
	ticks, slip, stress, slipSecs := srv.fightReelTickWear(cached, defaultReelLoadTickSeconds)
	if ticks != 0 || slip != 0 || slipSecs != 0 {
		t.Fatalf("拉力不超泄力应零打滑: ticks=%d slip=%v secs=%v", ticks, slip, slipSecs)
	}
	if stress != defaultReelLoadTickSeconds {
		t.Fatalf("受力秒应照记 %.2f，实得 %.2f", defaultReelLoadTickSeconds, stress)
	}
	if pullN := srv.fishFightPullFor(f); pullN <= cached.dragKg {
		t.Fatalf("正控失效：f8 牛数 %.4f 应高于泄力 %.2f，否则测不出旧 bug", pullN, cached.dragKg)
	}
}

// TestFightWearRolloverCarriesAccumulatedWear 钉住账段滚动：有磨损的账段取走落库，
// 新段从零开始；没有磨损的账段滚动不产生落库账（空竿/重排不再写任何东西）。
func TestFightWearRolloverCarriesAccumulatedWear(t *testing.T) {
	const rodGUID = "rod0000000000000000000000000000001"
	st := &FishState{}
	// 空账滚动：不产生落库账（官服口径：抛竿/空竿零磨损）。
	if _, has := rollFightWearLedgerLocked(st, rodGUID); has {
		t.Fatal("空账不应产生落库账")
	}
	// 有磨损的账段：取走后新段清零。
	st.reelWear.SlipWear = 0.1
	st.reelWear.StressSecs = 17
	pending, has := rollFightWearLedgerLocked(st, rodGUID)
	if !has || pending.SlipWear != 0.1 || pending.StressSecs != 17 {
		t.Fatalf("受力账应随段落库: has=%v slip=%v stress=%v", has, pending.SlipWear, pending.StressSecs)
	}
	if st.reelWear.SlipWear != 0 || st.reelWear.StressSecs != 0 {
		t.Fatalf("新段应从零开始: slip=%v stress=%v", st.reelWear.SlipWear, st.reelWear.StressSecs)
	}
	// 断件只进诊断：不构成落库账。
	st.reelWear.markRodBreakLocked()
	if _, has := rollFightWearLedgerLocked(st, rodGUID); has {
		t.Fatal("只有断件次数的账不应产生落库账")
	}
}
