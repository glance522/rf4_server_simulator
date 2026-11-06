package gameserver

import (
	"context"
	"math"
	"testing"
)

// 派鱼重量上限策略：
//
//	鱼咬不咬钩取决于饵、水域、深度和鱼种分布，**不取决于钓具强度，也不取决于钩号**。
//	本体里钩号只决定钩的强度与外观，没有"鱼嘴太大咬不住小钩"的判定，所以
//	fish_weight_cap 与 hook_entry_cap 默认都是 0：派鱼不做任何重量截断。
//
//	钓具只决定"拉不拉得上来"：线细、钩小，遇到大鱼会在搏鱼阶段断线断钩。
//
// 两个开关保留，供管理员对照调试旧模型（上游默认是 1/1）。

// TestDispatchCapDefaultIsOff 未配置时派鱼不截断：任何钓具组合都是一样的口径。
func TestDispatchCapDefaultIsOff(t *testing.T) {
	s, _ := newDetachTestServer(t)
	if got := s.tunableF("fish_weight_cap", 0); got != 0 {
		t.Fatalf("未配置时 fish_weight_cap = %v, want 0（承力不参与派鱼）", got)
	}
	if got := s.tunableF("hook_entry_cap", 0); got != 0 {
		t.Fatalf("未配置时 hook_entry_cap = %v, want 0（钩号不参与派鱼）", got)
	}
	for _, spec := range []gearPhysicsSpec{
		{},
		{RodPowerKg: 3, LineStrengthKg: 1, HookPresent: true, HookLengthMM: 2, HookSize: 2},
		{RodPowerKg: 300, LineStrengthKg: 200, HookPresent: true, HookLengthMM: 76, HookSize: 33},
	} {
		if got := s.fishWeightCapKg(spec); got != 0 {
			t.Fatalf("默认不应截断，spec=%+v 得到 %.3f kg", spec, got)
		}
	}
}

// TestFishWeightCapInvalidValueFallsBack 非法配置值回退到默认（0）。
func TestFishWeightCapInvalidValueFallsBack(t *testing.T) {
	s, db := newDetachTestServer(t)
	for _, key := range []string{"fish_weight_cap", "hook_entry_cap"} {
		if err := db.TunableSet(context.Background(), key, "abc", "test"); err != nil {
			t.Fatal(err)
		}
		s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
		if got := s.tunableF(key, 0); got != 0 {
			t.Fatalf("非法值 %s = %v, want 0（回退默认）", key, got)
		}
	}
}

// TestDispatchIgnoresGearAndHook 是策略本身的行为锁：同一条鱼，换线、换钩都不改变派鱼口径。
func TestDispatchIgnoresGearAndHook(t *testing.T) {
	s, db := newDetachTestServer(t)
	weak := gearPhysicsSpec{RodPowerKg: 3, LineStrengthKg: 1, HookPresent: true, HookLengthMM: 2, HookSize: 2}
	strong := gearPhysicsSpec{RodPowerKg: 80, LineStrengthKg: 100, HookPresent: true, HookLengthMM: 76, HookSize: 33}

	if got := s.fishWeightCapKg(weak); got != 0 {
		t.Fatalf("弱组默认口径应为 0（不截断），实得 %.3f", got)
	}
	if got := s.fishWeightCapKg(strong); got != 0 {
		t.Fatalf("强组默认口径应为 0（不截断），实得 %.3f", got)
	}

	// 开关保留：打开后回到各自的旧口径，用于对照。
	if err := db.TunableSet(context.Background(), "fish_weight_cap", "1", "test"); err != nil {
		t.Fatal(err)
	}
	s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
	if got, want := s.fishWeightCapKg(weak), gearStrengthCapKg(weak); math.Abs(got-want) > 1e-9 {
		t.Fatalf("开启承力截断后应回到强度上限: got=%.4f want=%.4f", got, want)
	}

	if err := db.TunableSet(context.Background(), "fish_weight_cap", "0", "test"); err != nil {
		t.Fatal(err)
	}
	if err := db.TunableSet(context.Background(), "hook_entry_cap", "1", "test"); err != nil {
		t.Fatal(err)
	}
	s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
	if got, want := s.fishWeightCapKg(weak), hookEntryCapKg(weak); math.Abs(got-want) > 1e-9 {
		t.Fatalf("开启钩入口截断后应回到钩口径: got=%.4f want=%.4f", got, want)
	}

	// 两个都打开时取较小值。
	if err := db.TunableSet(context.Background(), "fish_weight_cap", "1", "test"); err != nil {
		t.Fatal(err)
	}
	s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
	if got, want := s.fishWeightCapKg(weak), gearWeightCapKg(weak); math.Abs(got-want) > 1e-9 {
		t.Fatalf("两个开关都开时应取较小值: got=%.4f want=%.4f", got, want)
	}
}
