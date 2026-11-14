package gameserver

import (
	"context"
	"math"
	"testing"
	"time"

	"rf4server/internal/store"
)

func TestCastBaseDistanceProfiles(t *testing.T) {
	cases := []struct {
		name    string
		rigType int64
		want    float32
	}{
		{name: "float", rigType: 1, want: 7.5461488},
		{name: "bottom", rigType: 12, want: 7.5461488},
		{name: "lure", rigType: 5, want: 7.1598907},
	}
	for _, tc := range cases {
		got := math.Float32frombits(castBaseDistanceBits(tc.rigType))
		if math.Abs(float64(got-tc.want)) > 1e-6 {
			t.Fatalf("%s: got %.7f want %.7f", tc.name, got, tc.want)
		}
	}
}

func TestReelCastFrictionImprovesWithBearingOilAndDurability(t *testing.T) {
	wornBasic := reelCastFriction(10, 1, 15, 1, 0.4)
	newBasic := reelCastFriction(10, 1, 15, 1, 1.0)
	premium := reelCastFriction(100, 1, 100, 1, 1.0)
	if !(wornBasic > newBasic && newBasic > premium) {
		t.Fatalf("摩擦系数应随轴承/油品/耐久改善而下降: worn=%f basic=%f premium=%f",
			wornBasic, newBasic, premium)
	}
	if math.Abs(premium-0.05) > 1e-9 {
		t.Fatalf("满品质轮子摩擦=%f，期望0.05", premium)
	}
	// 零件自身磨旧也要体现在抛投手感上：油干了、轴承旧了都更甩不出去。
	dryOil := reelCastFriction(100, 1, 100, 0, 1.0)
	wornBearing := reelCastFriction(100, 0, 100, 1, 1.0)
	if !(dryOil > premium && wornBearing > premium) {
		t.Fatalf("轮油干涸或轴承磨旧应更难抛: 干油=%f 旧轴承=%f 全新=%f", dryOil, wornBearing, premium)
	}
}

func TestCastWeightAndSkillFactors(t *testing.T) {
	// 竿 Test 区间 2-10g：区间内越重越远，10g 达到峰值。
	light := rodTestWeightFactor(4, 2, 10)
	mid := rodTestWeightFactor(7, 2, 10)
	peak := rodTestWeightFactor(10, 2, 10)
	if !(mid > light && peak > mid) {
		t.Fatalf("Test 区间内载荷越重应抛得越远: 4g=%f 7g=%f 10g=%f", light, mid, peak)
	}
	// 超过 Test 上限后竿梢带不动，距离必须回落。
	over := rodTestWeightFactor(20, 2, 10)
	if !(over < peak) {
		t.Fatalf("超出 Test 上限后应衰减: 10g=%f 20g=%f", peak, over)
	}
	if !(castSkillFactor(100, 0) > castSkillFactor(50, 0) &&
		castSkillFactor(50, 10) > castSkillFactor(50, 0)) {
		t.Fatal("熟练度和已学习技能节点应提高抛投效率")
	}
}

func TestCastOverTestPayloadShortensDistance(t *testing.T) {
	peak := rodTestWeightFactor(10, 2, 10)
	twenty := rodTestWeightFactor(20, 2, 10)
	forty := rodTestWeightFactor(40, 2, 10)
	hundred := rodTestWeightFactor(100, 2, 10)
	if !(peak > twenty && twenty > forty && forty > hundred) {
		t.Fatalf("超 Test 后应单调衰减: 10g=%.4f 20g=%.4f 40g=%.4f 100g=%.4f",
			peak, twenty, forty, hundred)
	}
	if hundred < 0.15 {
		t.Fatalf("超重 10 倍时系数过低，距离会被压到失去意义: %.4f", hundred)
	}
	// 数值锚点：衰减只与超重倍率 r=payload/testMax 有关，相对峰值恒为
	// 1/(1+0.70*(r-1))。这里故意用字面量 0.70 而不是 rodTestOverloadDecay，
	// 这样以后微调常数会立刻被断言拦住，而不是断言跟着一起漂。
	for _, tc := range []struct {
		payload float64
		want    float64
	}{
		{15, 1.0 / (1 + 0.70*0.5)}, // 1.5× → 峰值 74.1%
		{20, 1.0 / (1 + 0.70*1.0)}, // 2×   → 峰值 58.8%
		{30, 1.0 / (1 + 0.70*2.0)}, // 3×   → 峰值 41.7%
	} {
		if ratio := rodTestWeightFactor(tc.payload, 2, 10) / peak; math.Abs(ratio-tc.want) > 1e-9 {
			t.Fatalf("%.0fg 应为峰值的 %.4f，实得 %.4f", tc.payload, tc.want, ratio)
		}
	}
	// 10× 撞的是 clampFloat 的绝对下限 0.22，不是"剩峰值的 22%"。
	if math.Abs(hundred-0.22) > 1e-9 {
		t.Fatalf("超重 10 倍应落到绝对下限 0.22，实得 %.4f", hundred)
	}
	if ratio := rodOverTestRatio(25, 10); math.Abs(ratio-2.5) > 1e-9 {
		t.Fatalf("超重倍率应为 2.5，实得 %f", ratio)
	}
	if ratio := rodOverTestRatio(25, 0); ratio != 0 {
		t.Fatalf("竿未登记 Test 时应返回 0，实得 %f", ratio)
	}
	// 同一条竿（Test 5-30g）：合规载荷必须比超重载荷抛得远。
	underDistance := castCalibrationDistance(30, 5)
	overDistance := castCalibrationDistance(120, 5)
	if overDistance >= underDistance {
		t.Fatalf("120g 配 30g 上限的竿不应抛得比 30g 更远: %.2fm vs %.2fm",
			overDistance, underDistance)
	}
	for _, payload := range []float64{7, 18, 40, 400, 4000} {
		d, factors := calculateCastDistance(CastDistanceContext{
			RigType: 5, CastAction: 1, CastPower: 1,
			RodTestMinG: 2, RodTestMaxG: 10, PayloadWeightG: payload,
		})
		if math.IsNaN(d) || math.IsInf(d, 0) || d < castMinPoweredM {
			t.Fatalf("载荷 %.0fg 计算异常: distance=%f factors=%+v", payload, d, factors)
		}
	}
}

func castCalibrationDistance(payloadG float64, spoolSize int64) float64 {
	d, _ := calculateCastDistance(CastDistanceContext{
		RigType: 5, CastAction: 1, CastPower: 1,
		RodLengthM: 3.0, RodAction: 2, RodTestMinG: 5, RodTestMaxG: 30,
		PayloadWeightG: payloadG, ReelEfficiency: 0.95, ReelType: 1,
		SpoolSize: spoolSize, SpoolFill: 0.8, LineDiameterMM: 0.20,
		LineStretch: 0.001, LineMaterial: 3,
		PlayerPrimary: defaultPlayerConditionPrimary, PlayerSecondary: defaultPlayerConditionSecondary,
		SkillMastery: 50,
	})
	return d
}

func TestCastCalibrationBands(t *testing.T) {
	for _, payload := range []float64{8, 10} {
		largeSpool := castCalibrationDistance(payload, 5) // 6000
		smallSpool := castCalibrationDistance(payload, 2) // 2000
		if largeSpool < 28 || largeSpool > 31 {
			t.Fatalf("6000线杯 %.0fg 距离=%0.2f，期望28～30米区间", payload, largeSpool)
		}
		if smallSpool < 32 || smallSpool > 36.5 {
			t.Fatalf("2000线杯 %.0fg 距离=%0.2f，期望32～36米区间", payload, smallSpool)
		}
		if smallSpool <= largeSpool+2 {
			t.Fatalf("小线杯应比6000线杯远至少2米: 小杯=%0.2f 大杯=%0.2f", smallSpool, largeSpool)
		}
	}
}

// 状态修正器边界：只按精力比例，0 值是真实空状态，落到 0.70 下限；
// 不再重置为默认值；饱食度不参与。
func TestCastPlayerStateFactorZeroStateUsesFloor(t *testing.T) {
	if got := castPlayerStateFactor(0); math.Abs(got-0.70) > 1e-9 {
		t.Fatalf("空状态应落到 0.70 下限: %v", got)
	}
	if got := castPlayerStateFactor(defaultPlayerConditionPrimary); math.Abs(got-1.0) > 1e-9 {
		t.Fatalf("精力满即应满抛距（饱食度不参与）: %v", got)
	}
	if got := castPlayerStateFactor(1.0); math.Abs(got-1.03) > 1e-9 {
		t.Fatalf("超上限应钳到 1.03: %v", got)
	}
	// 精力 0.60/0.85 ≈ 0.7059，仍在下限之上。
	if got := castPlayerStateFactor(0.60); math.Abs(got-0.60/defaultPlayerConditionPrimary) > 1e-9 {
		t.Fatalf("精力 0.60 应按比例 0.7059: %v", got)
	}
}

func TestCastHighEndGearCanReachFortyFiveMeters(t *testing.T) {
	d, _ := calculateCastDistance(CastDistanceContext{
		RigType: 5, CastAction: 1, CastPower: 1,
		RodLengthM: 4.2, RodAction: 4, RodTestMinG: 5, RodTestMaxG: 30,
		PayloadWeightG: 8, ReelEfficiency: 0.95, ReelType: 1,
		SpoolSize: 1, SpoolFill: 1.0, LineDiameterMM: 0.10,
		LineStretch: 0.001, LineMaterial: 3,
		PlayerPrimary: defaultPlayerConditionPrimary, PlayerSecondary: defaultPlayerConditionSecondary,
		SkillMastery: 100, SkillCastPct: 10,
	})
	if d < 44.9 {
		t.Fatalf("高阶装备应能进入45米精准抛投区间，实得%0.2f", d)
	}
}

func TestCastDynamicModifiersAreMonotonic(t *testing.T) {
	if !(castPowerFactor(1) > castPowerFactor(0.5) && castPowerFactor(0.5) > castPowerFactor(0.1)) {
		t.Fatal("抛投力度应单调增加距离")
	}
	if !(castLineDiameterFactor(0.12) > castLineDiameterFactor(0.40)) {
		t.Fatal("更粗主线应缩短距离")
	}
	if !(castLineStretchFactor(0.001) > castLineStretchFactor(0.87)) {
		t.Fatal("高拉伸主线应降低能量传递效率")
	}
	if !(castLineMaterialFactor(3) > castLineMaterialFactor(2)) {
		t.Fatal("编织线低拉伸材质应优于高拉伸材质")
	}
	if !(castLineMassFactor(1) > castLineMassFactor(20)) {
		t.Fatal("更重的运行主线应缩短距离")
	}
	if !(castRodActionFactor(4) > castRodActionFactor(3) && castRodActionFactor(3) > castRodActionFactor(1)) {
		t.Fatal("快调竿梢应小幅提高能量传递")
	}
}

func TestPlayerConditionUsesDatabase(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	// UpdatedTS 放到未来：无在线跨度，懒计算不推进，纯验证读库路径。
	if err := db.UpsertPlayerCondition(ctx, store.PlayerCondition{
		UID: uid, Primary: 0.62, Secondary: 0.71, Comfort: 1.0, UpdatedTS: float64(time.Now().Unix()) + 3600,
	}); err != nil {
		t.Fatal(err)
	}
	primary, secondary := srv.castPlayerCondition(ctx, uid)
	if math.Abs(primary-0.62) > 1e-9 || math.Abs(secondary-0.71) > 1e-9 {
		t.Fatalf("玩家状态未从 SQLite 读取: primary=%f secondary=%f", primary, secondary)
	}
	got, err := db.EnsurePlayerCondition(ctx, uid)
	if err != nil || got == nil || math.Abs(got.Primary-0.62) > 1e-9 || math.Abs(got.Secondary-0.71) > 1e-9 {
		t.Fatalf("玩家状态未持久化: got=%+v err=%v", got, err)
	}
}

func TestCastDistanceUsesMountedReelAndClip(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	reel, err := srv.CreateItem(ctx, uid, "spin_5820", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_40853", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !srv.rigSetType(ctx, uid, rod.GUID, 16) {
		t.Fatal("设置底钓 Method 钓组失败")
	}
	feeder, err := srv.CreateItem(ctx, uid, "feeder_30037", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	bait, err := srv.CreateItem(ctx, uid, "worm_01", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, reel.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, feeder.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, bait.GUID)
	if err := db.AddSkillPoints(ctx, uid, 3, 50); err != nil {
		t.Fatal(err)
	}

	p := srv.castDistanceForRig(ctx, uid, rod.GUID, 16)
	if p.ReelGUID != reel.GUID || p.DistanceM <= 10 || p.Friction <= 0 || p.Efficiency >= 1 {
		t.Fatalf("真实轮子抛投参数未生效: %+v", p)
	}
	if p.BaitItemID != "worm_01" || p.BaitWeightG <= 0 {
		t.Fatalf("当前饵未与抛投载荷对齐: bait=%q weight=%f", p.BaitItemID, p.BaitWeightG)
	}
	if p.RodLengthM <= 0 || p.RodTestMaxG <= 0 || p.RigWeightG <= 0 || p.BaitWeightG <= 0 ||
		p.PayloadWeightG <= p.BaitWeightG || p.SkillCategory != 3 || math.Abs(p.SkillMastery-50) > 1e-9 ||
		p.LineDiameterMM <= 0 || p.LineLengthM <= 0 || p.LineLinearDensityGPM <= 0 || p.LineRunningWeightG <= 0 ||
		p.PlayerPrimary <= 0 || p.PlayerSecondary <= 0 {
		t.Fatalf("竿长/锥度/Test/主线/线组/饵/玩家状态/技能数据未完整进入模型: %+v", p)
	}
	if p.SpoolCapacityM <= 0 || p.SpoolFill <= 0 || p.SpoolFill > 1 {
		t.Fatalf("spool capacity/fill missing: capacity=%.3f fill=%.3f", p.SpoolCapacityM, p.SpoolFill)
	}
	if want := math.Min(p.LineLengthM, p.SpoolCapacityM); math.Abs(p.LineLimitM-want) > 1e-9 {
		t.Fatalf("line limit should be capped by spool capacity: line=%.3f capacity=%.3f limit=%.3f want=%.3f", p.LineLengthM, p.SpoolCapacityM, p.LineLimitM, want)
	}
	perf, err := db.GetReelPerformance(ctx, reel.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(perf.CastFriction-p.Friction) > 1e-9 || math.Abs(perf.CastEfficiency-p.Efficiency) > 1e-9 {
		t.Fatalf("抛投参数未落入 reel_performance: perf=%+v profile=%+v", perf, p)
	}
	if err := db.AddSkillPoints(ctx, uid, 3, 50); err != nil {
		t.Fatal(err)
	}
	mastered := srv.castDistanceForRig(ctx, uid, rod.GUID, 16)
	if mastered.DistanceM <= p.DistanceM || mastered.SkillMastery != 100 {
		t.Fatalf("熟练度未提高抛投距离: 50%%=%+v 100%%=%+v", p, mastered)
	}

	if err := db.UpsertRodSetting(ctx, rod.GUID, reelClipSettingKey, "18"); err != nil {
		t.Fatal(err)
	}
	clipped := srv.castDistanceForRig(ctx, uid, rod.GUID, 16)
	if math.Abs(clipped.DistanceM-18) > 1e-6 || clipped.ClipLimitM != 18 {
		t.Fatalf("卡线夹未限制抛投距离: %+v", clipped)
	}
}

// TestReelSpoolCapacityMatchesGameData 用游戏内“线轴容量”读数锁定容量拟合式。
// 2000 杯容量系数 100，4000 杯按 180。
func TestReelSpoolCapacityMatchesGameData(t *testing.T) {
	cases := []struct {
		name     string
		coef     float64
		diameter float64
		want     float64
	}{
		{"2000/0.14", 100, 0.14, 234},
		{"2000/0.18", 100, 0.18, 154},
		{"2000/0.22", 100, 0.22, 111},
		{"4000/0.20", 180, 0.20, 245},
		{"4000/0.24", 180, 0.24, 181},
		{"4000/0.26", 180, 0.26, 168},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reelSpoolCapacityM(tc.coef, tc.diameter)
			if math.Abs(got-tc.want)/tc.want > 0.10 {
				t.Fatalf("容量 %.1fm，期望 %.0fm（误差>10%%）", got, tc.want)
			}
		})
	}
}

// TestSpoolFillCapsAtCapacity 锁定：整盘线长超过杯容量时按容量计，
// 短于容量时按实际线长计。
func TestSpoolFillCapsAtCapacity(t *testing.T) {
	if got := spoolFillRatio(600, 17); got != 1 {
		t.Fatalf("600m 盘装 17m 杯，上杯率应为 1，实得 %.3f", got)
	}
	if got := spoolFillRatio(8.5, 17); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("8.5m/17m 上杯率应为 0.5，实得 %.3f", got)
	}
	if got := spoolFillRatio(0, 17); got != 0 {
		t.Fatalf("无对应为 0，实得 %.3f", got)
	}
}
