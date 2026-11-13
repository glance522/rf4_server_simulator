package gameserver

import (
	"math"
	"testing"
)

// 官服 14/14 抓包 A 级锚点（2026-11-13 落表 8 种 12 样本，
// 见 specs/changes/fight-statics-official-tables/spec.md「Further Notes」样本清单）。
// 只落数值锚点，不含任何抓包原始数据（CONTRIBUTING 禁令不受影响）。
// g_carp 行容差 3%：f9 五样本 pow 拟合最大残差 2.7%（10307g 点）；其余 ±2%。
var officialFightStaticsAnchors = []struct {
	species            string
	weightG            int64
	grade              int64
	tolPct             float64
	f9, f10, f11       float64
	f13, f14, f15      float64
	f16, f17, f18, f20 float64
}{
	{"g_carp", 4880, 2, 3, 1.82933, 1.65477, 0.707334, 0.60, 1.20, 6, 1, 0.945041, 0.950344, 0.999763},
	{"g_carp", 5484, 2, 3, 1.84947, 1.65804, 0.712367, 0.60, 1.20, 6, 1, 0.945041, 0.950344, 0.999763},
	{"g_carp", 5716, 2, 3, 1.8572, 1.6593, 0.714301, 0.60, 1.20, 6, 1, 0.945041, 0.950344, 0.999763},
	{"g_carp", 10307, 2, 3, 2.01024, 1.68416, 0.75256, 0.60, 1.20, 6, 1, 0.945041, 0.950344, 0.999763},
	{"g_carp", 21742, 2, 3, 2.39142, 1.74611, 0.847856, 0.60, 1.20, 6, 1, 0.945041, 0.950344, 0.999763},
	{"b_carp", 7828, 2, 2, 1.92761, 1.67074, 0.731901, 0.65, 1.20, 6, 1, 0.945114, 0.950416, 0.999765},
	{"carp", 1063, 1, 2, 1.46609, 1.06575, 0.633218, 0.20, 1.00, 6, 1, 0.927123, 0.944333, 0.999774},
	{"n.pike", 4305, 2, 2, 4.46105, 1.41526, 1.09221, 0.50, 1.00, 20, 1, 0.973469, 0.993922, 0.999839},
	{"merlang", 252, 1, 2, 4.60922, 1.29599, 1.06092, 0.50, 25.0, 200, 0.251768, 0.947264, 0.941456, 0.99973},
	{"piksha", 2975, 2, 2, 5.15528, 1.86211, 1.13106, 0.50, 20.0, 250, 0.129192, 0.994663, 0.99352, 0.999811},
	{"taran", 611, 2, 2, 2.47531, 1.48324, 0.734414, 0.40, 0.80, 16, 1, 0.692334, 1, 0.999452},
	{"b_gill", 113, 1, 2, 2.98462, 1.06095, 0.737278, 0.45, 0.45, 10, 1, 0.94152, 0.929121, 0.999804},
}

// TestFishFightStaticsMatchesOfficialAnchors 防回归：落表种的 27 静态参数
// 必须落在官服 14/14 样本 ±tol% 内（f7/f12 官方恒 0 不在列；f19 见
// TestFishFightStaticsF19FloatRigFallback）。回滚任一表值即红。
func TestFishFightStaticsMatchesOfficialAnchors(t *testing.T) {
	for _, a := range officialFightStaticsAnchors {
		got := fight27StaticsFor(a.species, float64(a.weightG), a.grade)
		fields := []struct {
			name      string
			want, got float64
		}{
			{"f9", a.f9, got.f9}, {"f10", a.f10, got.f10}, {"f11", a.f11, got.f11},
			{"f13", a.f13, got.f13}, {"f14", a.f14, got.f14}, {"f15", a.f15, got.f15},
			{"f16", a.f16, got.f16}, {"f17", a.f17, got.f17}, {"f18", a.f18, got.f18},
			{"f20", a.f20, got.f20},
		}
		for _, fld := range fields {
			tol := fld.want * a.tolPct / 100
			if math.Abs(fld.got-fld.want) > tol {
				t.Errorf("%s %dg grade%d %s=%.6g，官服=%.6g（±%v%%）",
					a.species, a.weightG, a.grade, fld.name, fld.got, fld.want, a.tolPct)
			}
		}
	}
}

// TestFishFightStaticsF19FloatRigFallback f19 只有浮钓族生效（buildFish27 对
// 其余钓法强制 1）：taran 有浮钓真值（0.00204695@611g 常数），其余落表种样本
// 均为非浮钓 rig，浮钓沿用 fallbackFightStatics 体重插值（落表前行为不变）。
func TestFishFightStaticsF19FloatRigFallback(t *testing.T) {
	if got := fight27StaticsFor("taran", 611, 2).f19; math.Abs(got-0.00204695) > 1e-9 {
		t.Errorf("taran 611g 浮钓 f19=%v，官服真值 0.00204695", got)
	}
	for _, c := range []struct {
		species string
		weightG int64
		grade   int64
	}{
		{"g_carp", 21742, 2}, {"b_carp", 7828, 2}, {"carp", 1063, 1},
		{"n.pike", 4305, 2}, {"merlang", 252, 1}, {"piksha", 2975, 2}, {"b_gill", 113, 1},
	} {
		want := fallbackFightStatics(float64(c.weightG))[19]
		if got := fight27StaticsFor(c.species, float64(c.weightG), c.grade).f19; got != want {
			t.Errorf("%s %dg 浮钓 f19=%v，应沿用 fallback 插值 %v", c.species, c.weightG, got, want)
		}
	}
}

// TestFishFightStaticsGroupFallback 未实测种走习性组兜底：menek → 海水组
// （merlang+piksha 两点均值）、karasekarp → 鲤科组、salmonid 等其余组与
// 未命中 → fallbackFightStatics 第三层（保持现状）。
func TestFishFightStaticsGroupFallback(t *testing.T) {
	m := fight27StaticsFor("menek", 22800, 2)
	for _, c := range []struct {
		name      string
		want, got float64
	}{
		{"f9", 4.88225, m.f9}, {"f10", 1.57905, m.f10}, {"f11", 1.09599, m.f11},
		{"f13", 0.50, m.f13}, {"f14", 22.5, m.f14}, {"f15", 225, m.f15},
		{"f16", 0.19048, m.f16}, {"f17", 0.970964, m.f17}, {"f18", 0.967488, m.f18},
		{"f20", 0.9997705, m.f20},
	} {
		if math.Abs(c.got-c.want) > 1e-9 {
			t.Errorf("menek（海水组）%s=%v，组均值 %v", c.name, c.got, c.want)
		}
	}
	k := fight27StaticsFor("karasekarp", 5000, 2)
	if k.f13 != 0.35 || k.f14 != 1.0 || k.f15 != 6 || k.f16 != 1.0 || k.f17 != 0.93 {
		t.Errorf("karasekarp（鲤科组）静态参数不符：f13=%v f14=%v f15=%v f16=%v f17=%v",
			k.f13, k.f14, k.f15, k.f16, k.f17)
	}
	fb := fight27StaticsFor("riv_rb_trout", 1000, 2)
	if fb.f14 != 0.5 || fb.f16 != 0.833907 || fb.f20 != 0.997804 {
		t.Errorf("riv_rb_trout（未命中组）应走第三层 fallback：f14=%v f16=%v f20=%v", fb.f14, fb.f16, fb.f20)
	}
}

// TestFishStaminaRateGroupFor f5 每克组兜底：海水 8.445/g、鲤科 2.5/g
// （官方口径直接 rate×体重），其余组旧口径 2.388×Scale。
func TestFishStaminaRateGroupFor(t *testing.T) {
	srv := &Server{}
	profile := fishFightProfile{StaminaScale: 1.5}
	if got := srv.fishStaminaForProfile(&RolledFish{Species: "menek", WeightG: 22800}, profile); math.Abs(got-8.445*22800) > 1e-6 {
		t.Errorf("menek 体力=%v，应 8.445/g×22800（组均值不乘 StaminaScale）", got)
	}
	if got := srv.fishStaminaForProfile(&RolledFish{Species: "karasekarp", WeightG: 5000}, profile); math.Abs(got-2.5*5000) > 1e-6 {
		t.Errorf("karasekarp 体力=%v，应 2.5/g×5000", got)
	}
	if got := srv.fishStaminaForProfile(&RolledFish{Species: "riv_rb_trout", WeightG: 1000}, profile); math.Abs(got-verifiedStaminaPerGram*1000*1.5) > 1e-6 {
		t.Errorf("riv_rb_trout 体力=%v，应旧口径 2.388×1000×1.5", got)
	}
}

// TestFishF6MarineGroupFallback f6 组兜底：海水组未实测种 0.50
// （merlang/piksha 实测均 0.50），其余组旧常数。
func TestFishF6MarineGroupFallback(t *testing.T) {
	srv := &Server{}
	if got := srv.fishF6For(&RolledFish{Species: "menek", WeightG: 22800}); got != 0.50 {
		t.Errorf("menek f6=%v，海水组应 0.50", got)
	}
	if got := srv.fishF6For(&RolledFish{Species: "riv_rb_trout", WeightG: 1000}); got != srv.fishInitialStaminaRatio() {
		t.Errorf("riv_rb_trout f6=%v，未命中组应旧常数 %v", got, srv.fishInitialStaminaRatio())
	}
}

// TestFishFightArchetypeMarineKeywords 词表修正：piksha 移出 ambush（实测海水
// 鲽科）、menek/agar/warm/merlang 补进 marine（CurvePool 随 marine 归 perch 池，
// 不再落 crucian 池）；词表其余判定不回退。
func TestFishFightArchetypeMarineKeywords(t *testing.T) {
	for _, sp := range []string{"piksha", "menek", "agar", "warm", "merlang", "coalfish", "m_scorpius"} {
		if arch := fishFightArchetypeFor(sp); arch != fishFightArchetypes["marine"] {
			t.Errorf("%s 应归 marine，实得 %+v", sp, arch)
		}
	}
	for _, c := range []struct {
		species string
		group   string
	}{{"e.pikeperch", "ambush"}, {"crucian", "default"}, {"karasekarp", "cyprinid"},
		{"n.pike", "ambush"}, {"perch", "ambush"}} {
		if arch := fishFightArchetypeFor(c.species); arch != fishFightArchetypes[c.group] {
			t.Errorf("%s 应归 %s，实得 %+v", c.species, c.group, arch)
		}
	}
}
