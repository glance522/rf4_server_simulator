package gameserver

// 摩擦片/受力磨损参数的 tunable 化验证。见 specs/changes/reel-wear-tunables 与
// specs/changes/wear-fight-time/spec.md。
//
// 这组测试分两半：
//  1. 无 DB 的零值 Server：验证「未配置时行为与硬编码逐字节一致」；
//  2. 带 DB 的 Server：验证 tunable 覆盖与非法值回落。
//
// 打滑的物理手算样例在 reel_fight_wear_test.go 里已有覆盖，这里只钉参数通路。

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestReelSlipWearDefaultMatchesConstants(t *testing.T) {
	t.Parallel()
	srv := &Server{} // 无 DB：tunableF 一律回落默认
	cases := []struct {
		setting int64
		want    float64
	}{
		{0, reelSlipWearLow},
		{24, reelSlipWearLow},
		{25, 0.000025},
		{26, 0.00005},
		{27, 0.000075},
		{28, 0.000095},
		{29, reelSlipWearMax},
	}
	for _, tc := range cases {
		if got := srv.reelSlipWearForSetting(tc.setting); math.Abs(got-tc.want) > 1e-12 {
			t.Errorf("档位 %d 默认磨损率=%.9f，期望 %.9f", tc.setting, got, tc.want)
		}
		if got := reelSlipWearForSettingDefault(tc.setting); math.Abs(got-tc.want) > 1e-12 {
			t.Errorf("档位 %d 包级默认口径=%.9f，期望 %.9f", tc.setting, got, tc.want)
		}
	}
}

func TestReelSlipCalibrationDefaults(t *testing.T) {
	t.Parallel()
	srv := &Server{}
	if got := srv.reelSlipCalibrationSeconds(); math.Abs(got-reelSlipCalibrationSeconds) > 1e-12 {
		t.Errorf("默认校准秒数=%.4f，期望 %.4f", got, reelSlipCalibrationSeconds)
	}
	// 缺省校准对齐官服 W5 实测：0.01%/41.7s ≈ 0.00024%/s。
	if math.Abs(reelSlipCalibrationSeconds-41.7) > 1e-9 {
		t.Errorf("校准秒数缺省应取官服档位 41.7，实得 %.4f", reelSlipCalibrationSeconds)
	}
	if got := srv.reelSlipDeadbandRatio(); math.Abs(got-reelSlipDeadbandRatioDefault) > 1e-12 {
		t.Errorf("默认打滑门槛=%.4f，期望 %.4f", got, reelSlipDeadbandRatioDefault)
	}
	if got := srv.leaderStressWearPerSec(); math.Abs(got-leaderStressWearPerSecDefault) > 1e-12 {
		t.Errorf("默认前导受力速率=%.6f，期望 %.6f", got, leaderStressWearPerSecDefault)
	}
	if math.Abs(fightWearStreamIntervalSeconds-2.0) > 1e-12 {
		t.Errorf("中途落库节流间隔应固定为官服帧节奏 2s，实得 %.4f", fightWearStreamIntervalSeconds)
	}
}

// 打滑磨损量 = 档位率 / 校准秒数 x tick x 油系数。校准秒数翻倍 → 磨损减半。
func TestReelSlipWearHalvesWithDoubledCalibration(t *testing.T) {
	t.Parallel()
	srv, db := newEconomyTxTestServer(t)
	ctx := context.Background()

	w := newFightWearForTest(7.7, 5, 7.5, 1)
	w.setting = 29
	const tick = 2.0

	_, base, _, _ := srv.fightReelTickWear(w, tick)
	if base <= 0 {
		t.Fatalf("基准磨损应大于 0，实得 %.9f", base)
	}
	// 手算：0.0001 / 41.7 * 2 * 1 * 1
	wantBase := defaultReelSlipWearMax / reelSlipCalibrationSeconds * tick
	if math.Abs(base-wantBase) > 1e-12 {
		t.Fatalf("29 档打滑磨损=%.12f，手算 %.12f", base, wantBase)
	}

	if err := db.TunableSet(ctx, "reel_slip_calibration_seconds", "83.4", "test"); err != nil {
		t.Fatal(err)
	}
	srv.tunAt = time.Time{}
	_, halved, _, _ := srv.fightReelTickWear(w, tick)

	if math.Abs(halved-base/2) > 1e-12 {
		t.Fatalf("校准秒数翻倍后磨损应减半：base=%.12f halved=%.12f", base, halved)
	}
}

// 打滑门槛倍率可调：拉力落在门槛与旧口径之间时，门槛提高就不再算打滑。
func TestReelSlipDeadbandTunable(t *testing.T) {
	t.Parallel()
	srv, db := newEconomyTxTestServer(t)
	ctx := context.Background()

	// 拉力 5.2 = 泄力 5 的 1.04×：缺省门槛 1.05 下不打滑（官服边界样本）。
	w := newFightWearForTest(7.7, 5, 5.2, 1)
	if ticks, _, _, _ := srv.fightReelTickWear(w, 2); ticks != 0 {
		t.Fatalf("1.04× 泄力在缺省门槛下不应打滑，实得 ticks=%d", ticks)
	}
	// 门槛调到 1.0（旧口径）：1.04× 也算打滑。
	if err := db.TunableSet(ctx, "reel_slip_deadband_ratio", "1", "test"); err != nil {
		t.Fatal(err)
	}
	srv.tunAt = time.Time{}
	ticks, slip, _, _ := srv.fightReelTickWear(w, 2)
	if ticks != 1 || slip <= 0 {
		t.Fatalf("门槛调到 1 后 1.04× 应记打滑，实得 ticks=%d slip=%v", ticks, slip)
	}
	// 非法值（<1）回落缺省 1.05，打滑消失。
	if err := db.TunableSet(ctx, "reel_slip_deadband_ratio", "0.5", "test"); err != nil {
		t.Fatal(err)
	}
	srv.tunAt = time.Time{}
	if ticks, _, _, _ := srv.fightReelTickWear(w, 2); ticks != 0 {
		t.Fatal("非法门槛应回落缺省，1.04× 不应打滑")
	}
}

// 档位率本身可调：把 29 档改小一半，磨损同步减半。
func TestReelSlipWearHonorsSettingTunable(t *testing.T) {
	t.Parallel()
	srv, db := newEconomyTxTestServer(t)
	ctx := context.Background()

	if err := db.TunableSet(ctx, "reel_slip_wear_max", "0.00005", "test"); err != nil {
		t.Fatal(err)
	}
	srv.tunAt = time.Time{}
	if got := srv.reelSlipWearForSetting(29); math.Abs(got-0.00005) > 1e-12 {
		t.Fatalf("29 档应读 tunable 0.00005，实得 %.9f", got)
	}
	// 未单独配置的档位仍用各自默认
	if got := srv.reelSlipWearForSetting(25); math.Abs(got-defaultReelSlipWear25) > 1e-12 {
		t.Fatalf("25 档应保持默认 %.9f，实得 %.9f", defaultReelSlipWear25, got)
	}
}

// 非法值（0 / 负 / 非数值）必须回落到代码默认，不能把模型算歪。
func TestReelSlipWearFallsBackOnInvalidTunable(t *testing.T) {
	t.Parallel()
	srv, db := newEconomyTxTestServer(t)
	ctx := context.Background()

	for _, bad := range []string{"0", "-1", "abc"} {
		if err := db.TunableSet(ctx, "reel_slip_wear_max", bad, "test"); err != nil {
			t.Fatalf("写入 %q 失败: %v", bad, err)
		}
		srv.tunAt = time.Time{}
		if got := srv.reelSlipWearForSetting(29); math.Abs(got-defaultReelSlipWearMax) > 1e-12 {
			t.Fatalf("%q 应回落到默认 %.9f，实得 %.9f", bad, defaultReelSlipWearMax, got)
		}
	}
}

// 受力磨损速率走 tunable：调到 0 关通道，翻倍磨损翻倍；非法值回落矩阵缺省。
func TestReelStressRateHonorsTunable(t *testing.T) {
	t.Parallel()
	srv, db := newEconomyTxTestServer(t)
	ctx := context.Background()

	counts := reelPartCounts{StressSecs: 10}
	def := reelPartWearMatrix[reelPartCatGear].perStressSec
	if got := srv.reelStressRateFor(reelPartCatGear); math.Abs(got-def) > 1e-12 {
		t.Fatalf("未配置时齿轮受力速率应取矩阵缺省 %.6f，实得 %.6f", def, got)
	}
	if err := db.TunableSet(ctx, "wear_stress_gear_per_s", "0", "test"); err != nil {
		t.Fatal(err)
	}
	srv.tunAt = time.Time{}
	if got := reelPartWearFor(reelPartCatGear, 100, counts, srv.reelStressRateFor(reelPartCatGear)); got != 0 {
		t.Fatalf("速率调 0 应关通道，实得 %v", got)
	}
	if err := db.TunableSet(ctx, "wear_stress_gear_per_s", "abc", "test"); err != nil {
		t.Fatal(err)
	}
	srv.tunAt = time.Time{}
	if got := srv.reelStressRateFor(reelPartCatGear); math.Abs(got-def) > 1e-12 {
		t.Fatalf("非法值应回落矩阵缺省 %.6f，实得 %.6f", def, got)
	}
}
