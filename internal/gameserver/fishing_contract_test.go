package gameserver

import (
	"math"
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func f32At(v *proto.Value, idx int) float64 {
	f := v.Field(idx)
	return float64(proto.F32FromBits(f.U32()))
}

// TestFish27UsesVerifiedFightFields 验证 obj27 的体力、初始体力和拉力恢复到
// 旧版真实服务器样本校准出的字段语义，而不是把积分/质量塞进搏鱼字段。
func TestFish27UsesVerifiedFightFields(t *testing.T) {
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "contract"}}, logging.Discard(), nil)
	f := &RolledFish{FishGUID: "11111111111111111111111111111111", Species: "c.roach", Grade: 1, WeightG: 186, LengthM: 0.1981}
	profile := srv.fishFightProfileFor(f)
	tree := srv.buildFish27(f, "22222222222222222222222222222222", "33333333333333333333333333333333")
	if defaultFishStaminaScale != 1 {
		t.Fatalf("默认体力倍率=%f，期望使用真实样本校准值 1", defaultFishStaminaScale)
	}
	if defaultFishInitialStaminaRatio != verifiedInitialStaminaRatio {
		t.Fatalf("默认初始体力=%f，期望真实样本校准值 %f",
			defaultFishInitialStaminaRatio, verifiedInitialStaminaRatio)
	}
	wantStamina := verifiedStaminaPerGram * 186 * profile.StaminaScale
	if got := f32At(tree, 5); math.Abs(got-wantStamina) > 0.001 {
		t.Fatalf("c.roach f5 体力池=%f，期望%f", got, wantStamina)
	}
	if got := f32At(tree, 6); math.Abs(got-verifiedInitialStaminaRatio) > 1e-6 {
		t.Fatalf("c.roach f6 初始体力=%f，期望%f", got, verifiedInitialStaminaRatio)
	}
	wantPull := verifiedForcePerGram * 186 * profile.ForceScale
	if got := f32At(tree, 8); math.Abs(got-wantPull) > 0.0001 {
		t.Fatalf("c.roach f8 拉力=%f，期望%f", got, wantPull)
	}

	// 两条实际样本必须严格复现。
	for _, tc := range []struct {
		weightG float64
		force   float64
	}{
		{53, 0.5197524514},
		{63, 0.6178189516},
	} {
		f = &RolledFish{FishGUID: "44444444444444444444444444444444", Species: "ruffe_n", Grade: 2, WeightG: int64(tc.weightG), LengthM: 0.18}
		profile = srv.fishFightProfileFor(f)
		tree = srv.buildFish27(f, "", "")
		want := tc.force * profile.ForceScale
		if got := f32At(tree, 8); math.Abs(got-want) > 1e-3 {
			t.Fatalf("%.0fg f8 拉力=%f，期望%f", tc.weightG, got, want)
		}
	}
}

// TestFish27UnknownSpeciesFallback 验证未知鱼种使用回退搏鱼参数。
func TestFish27UnknownSpeciesFallback(t *testing.T) {
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "contract"}}, logging.Discard(), nil)
	f := &RolledFish{FishGUID: "55555555555555555555555555555555", Species: "not_in_calib", Grade: 1, WeightG: 100, LengthM: 0.2}
	profile := srv.fishFightProfileFor(f)
	tree := srv.buildFish27(f, "", "")
	wantStamina := verifiedStaminaPerGram * 100 * profile.StaminaScale
	if got := f32At(tree, 5); math.Abs(got-wantStamina) > 0.001 {
		t.Fatalf("未知鱼 f5 体力池=%f，期望%f", got, wantStamina)
	}
	if got := f32At(tree, 6); math.Abs(got-verifiedInitialStaminaRatio) > 1e-6 {
		t.Fatalf("未知鱼 f6 初始体力=%f，期望%f", got, verifiedInitialStaminaRatio)
	}
	if got := f32At(tree, 9); got < 1.35 || got > 1.37 {
		t.Fatalf("未知鱼 f9=%f，期望按94/189g校准约1.3607", got)
	}
	if got := f32At(tree, 13); math.Abs(got-0.35) > 1e-6 {
		t.Fatalf("未知鱼 f13=%f，期望0.35", got)
	}
	if got := f32At(tree, 16); math.Abs(got-0.833907) > 1e-5 {
		t.Fatalf("未知鱼 f16=%f，期望0.833907", got)
	}
	if got := f32At(tree, 20); math.Abs(got-0.997804) > 1e-5 {
		t.Fatalf("未知鱼 f20=%f，期望0.997804", got)
	}
}

// TestFish27PreservesOfficialF19ProbabilityCalibration 验证 27.f19 保持官方
// 14/14 样本对应的概率量级，不能把行为 105 的概率门控放大成恒定成功。
func TestFish27PreservesOfficialF19ProbabilityCalibration(t *testing.T) {
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "contract"}}, logging.Discard(), nil)
	for _, tc := range []struct {
		species string
		weightG int64
		want    float64
		tol     float64
	}{
		{species: "c.roach", weightG: 635, want: 0.00309, tol: 0.00005},
		{species: "perch", weightG: 78, want: 0.00067, tol: 0.00005},
		{species: "e.chub", weightG: 723, want: 0.00313, tol: 0.00005},
		{species: "not_in_calib", weightG: 94, want: 0.00060, tol: 0.00001},
	} {
		f := &RolledFish{
			FishGUID: "66666666666666666666666666666666",
			Species:  tc.species,
			Grade:    1,
			WeightG:  tc.weightG,
			LengthM:  0.1981,
		}
		if got := f32At(srv.buildFish27(f, "", ""), 19); math.Abs(got-tc.want) > tc.tol {
			t.Fatalf("%s %dg 的 27.f19=%f，期望官方校准概率约 %f", tc.species, tc.weightG, got, tc.want)
		}
	}
}

// TestFish27IncludesBehavior105Candidate 把自动上钩调度的服务端前置条件单独锁定：
// 27.f21 必须包含正权重、正时长的行为 105。客户端是否抽中仍由 f19 概率决定。
func TestFish27IncludesBehavior105Candidate(t *testing.T) {
	srv, _ := newRigTestServer(t)
	for _, species := range []string{"c.roach", "perch", "e.chub", "not_in_calib"} {
		f := &RolledFish{
			FishGUID: "77777777777777777777777777777777",
			Species:  species,
			Grade:    1,
			WeightG:  635,
			LengthM:  0.1981,
		}
		curves := srv.buildFish27(f, "", "").Field(21).Val
		if curves == nil {
			t.Fatalf("%s 的 27.f21 为空", species)
		}
		found := false
		for _, row := range curves.Items {
			stage := row.Field(0)
			if stage.U32() != 105 {
				continue
			}
			weight, minDuration, maxDuration := f32At(row, 1), f32At(row, 2), f32At(row, 3)
			if weight > 0 && minDuration > 0 && maxDuration >= minDuration {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s 的 27.f21 缺少可调度的行为 105 候选", species)
		}
	}
}
