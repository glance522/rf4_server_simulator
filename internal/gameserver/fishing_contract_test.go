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
	tree := srv.buildFish27(f, "22222222222222222222222222222222", "33333333333333333333333333333333", 24)
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
		tree = srv.buildFish27(f, "", "", 24)
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
	tree := srv.buildFish27(f, "", "", 24)
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

// TestFish27F19AutoHookByRigFamily 验证 27.f19 的钓法口径。官方真服抓包
// （2026-11-10/11，11 个 14/14 逐帧解码）：沉底/海钓/路亚 f19 恒为精确 1.0，
// 浮钓族为 0.001~0.0024 连续小值（随鱼种/重量上行）——f19 是客户端行为 105
// 自动上钩尝试的强度输入，非布尔启用位。tunable float_f19_force_one 非 0 时
// 浮钓族也强制 1（浮钓静置不上钩的应急回滚）。
func TestFish27F19AutoHookByRigFamily(t *testing.T) {
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "contract"}}, logging.Discard(), nil)
	mk := func(species string) *RolledFish {
		return &RolledFish{FishGUID: "11111111111111111111111111111111", Species: species, Grade: 1, WeightG: 186, LengthM: 0.1981}
	}
	// 沉底(24)/海钓(55)/路亚(6)：官方恒 1，已校准鱼种与回退鱼种同口径。
	for _, rig := range []int64{24, 55, 6} {
		for _, species := range []string{"c.roach", "not_in_calib"} {
			if got := f32At(srv.buildFish27(mk(species), "", "", rig), 19); got != 1 {
				t.Fatalf("rig=%d %s f19=%f，期望 1", rig, species, got)
			}
		}
	}
	// 浮钓族(1)：走鱼种校准曲线 / fallback 曲线，官方量级 (0, 0.01) 小值。
	for _, species := range []string{"c.roach", "not_in_calib"} {
		got := f32At(srv.buildFish27(mk(species), "", "", 1), 19)
		if got <= 0 || got >= 0.01 {
			t.Fatalf("浮钓 %s f19=%f，期望官方量级小值 (0, 0.01)", species, got)
		}
	}
}
