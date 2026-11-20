package gameserver

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"rf4server/internal/store"
)

func testDef(attrs string) *store.ItemDef {
	return &store.ItemDef{AttrsJSON: attrs}
}

// 浮漂只登记 jpaemlfkohn（按号数的克重），没有 dhlkahnodoo/
// ijbjficbhog。旧实现把整支浮漂算成 0 g。
func TestFloatWeightComesFromRatedGram(t *testing.T) {
	t.Parallel()
	float := testDef(fmt.Sprintf(`{"fopbhlmkcpe":1,"jpaemlfkohn":%d,"hkgndoipdlc":%d}`,
		f32Attr(4.0), f32Attr(11.0)))
	if got := itemDefCastWeightG(float); math.Abs(got-4.0) > 1e-6 {
		t.Fatalf("4G 浮漂应计 4g，实得 %f", got)
	}
	heavy := testDef(fmt.Sprintf(`{"jpaemlfkohn":%d}`, f32Attr(50.0)))
	if got := itemDefCastWeightG(heavy); math.Abs(got-50.0) > 1e-6 {
		t.Fatalf("50G 浮漂应计 50g，实得 %f", got)
	}
}

// 成品前导没有重量字段，只能按“线径 × 长度 × 材质密度”折算。
func TestLeaderWeightFromGeometry(t *testing.T) {
	t.Parallel()
	// 200cm 尼龙前导，内嵌线径 0.35mm。
	leader := &store.ItemDef{AttrsJSON: fmt.Sprintf(
		`{"hkgndoipdlc":%d,"mbnambppbom":{"ciichedlnjh":%d,"ocmbfkebgpp":1}}`,
		f32Attr(200.0), f32Attr(0.35))}
	want := 1.14 * math.Pi / 4 * 0.35 * 0.35 * 2.0
	if got := leaderMassG(leader, 0); math.Abs(got-want) > 1e-6 {
		t.Fatalf("2m 0.35mm 尼龙前导应约 %.4fg，实得 %f", want, got)
	}
	// 散装钢丝前导线：长度在 ljnnpokapig（米）。
	wire := &store.ItemDef{AttrsJSON: fmt.Sprintf(
		`{"ocmbfkebgpp":6,"kngaaefkpka":%d,"ciichedlnjh":%d,"ljnnpokapig":%d}`,
		f32Attr(13.0), f32Attr(0.50), f32Attr(15.0))}
	gotWire := leaderMassG(wire, 0)
	if gotWire <= 0 {
		t.Fatalf("钢丝前导线应折算为正重量，实得 %f", gotWire)
	}
	if gotWire < 10 {
		t.Fatalf("15m 0.5mm 钢丝前导应明显重于普通前导，实得 %f g", gotWire)
	}
	// 重量必须随长度线性增长。
	short := &store.ItemDef{AttrsJSON: fmt.Sprintf(
		`{"hkgndoipdlc":%d,"mbnambppbom":{"ciichedlnjh":%d,"ocmbfkebgpp":1}}`,
		f32Attr(100.0), f32Attr(0.35))}
	if math.Abs(leaderMassG(short, 0)*2-leaderMassG(leader, 0)) > 1e-6 {
		t.Fatalf("前导重量应对长度线性: 1m=%f 2m=%f", leaderMassG(short, 0), leaderMassG(leader, 0))
	}
}

// 拟饵的子配置数组里挂着三本钩，钩重必须与饵体重量相加。
func TestLureWeightIncludesNestedHooks(t *testing.T) {
	t.Parallel()
	lure := &store.ItemDef{AttrsJSON: fmt.Sprintf(
		`{"dhlkahnodoo":%d,"gpoaffnjbdo":[{"ijbjficbhog":%d},{"ijbjficbhog":%d},{"ijbjficbhog":%d}]}`,
		f32Attr(20.0), f32Attr(1.6), f32Attr(1.6), f32Attr(1.6))}
	if got := lureChildWeightG(lure); math.Abs(got-4.8) > 1e-6 {
		t.Fatalf("三枚 1.6g 子钩应合计 4.8g，实得 %f", got)
	}
	if got := rigComponentCastWeight(catLure, lure, 0); math.Abs(got-24.8) > 1e-6 {
		t.Fatalf("20g 饵体 + 4.8g 子钩 应计 24.8g，实得 %f", got)
	}
}

// 各类部件都要走到正确的重量字段，不能有整类归零。
func TestRigComponentWeightsByCategory(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		category int64
		attrs    string
		want     float64
	}{
		{"钩(水中重量)", 12, fmt.Sprintf(`{"ijbjficbhog":%d}`, f32Attr(0.25)), 0.25},
		{"饵(重量)", 27, fmt.Sprintf(`{"ijbjficbhog":%d}`, f32Attr(1.0)), 1.0},
		{"沉子(自重)", 29, fmt.Sprintf(`{"dhlkahnodoo":%d}`, f32Attr(30.0)), 30.0},
		{"浮漂(号数克重)", 28, fmt.Sprintf(`{"jpaemlfkohn":%d}`, f32Attr(3.5)), 3.5},
	}
	for _, tc := range cases {
		if got := rigComponentCastWeight(tc.category, testDef(tc.attrs), 0); math.Abs(got-tc.want) > 1e-6 {
			t.Fatalf("%s: 期望 %.4fg，实得 %f", tc.name, tc.want, got)
		}
	}
}

// 线重必须随线径变化；旧的 lgjeahdobch 在同一材质下是常数。
func TestLineMassScalesWithDiameter(t *testing.T) {
	t.Parallel()
	thin := testDef(fmt.Sprintf(`{"fopbhlmkcpe":1,"ciichedlnjh":%d,"lgjeahdobch":%d}`,
		f32Attr(0.20), f32Attr(0.02)))
	thick := testDef(fmt.Sprintf(`{"fopbhlmkcpe":1,"ciichedlnjh":%d,"lgjeahdobch":%d}`,
		f32Attr(0.40), f32Attr(0.02)))
	thinMass, thickMass := lineMassGPM(thin), lineMassGPM(thick)
	if !(thickMass > thinMass*3.5) {
		t.Fatalf("线径翻倍时线重应约 4 倍增长: %.5f → %.5f", thinMass, thickMass)
	}
	// 0.30mm 尼龙约 0.08g/m，用来确认量级没有跑偏。
	mono := testDef(fmt.Sprintf(`{"fopbhlmkcpe":1,"ciichedlnjh":%d}`, f32Attr(0.30)))
	if got := lineMassGPM(mono); got < 0.06 || got > 0.10 {
		t.Fatalf("0.30mm 尼龙线重应在 0.06~0.10 g/m，实得 %f", got)
	}
	// 缺线径时回退静态字段，不能返回 0。
	fallback := testDef(fmt.Sprintf(`{"fopbhlmkcpe":1,"lgjeahdobch":%d}`, f32Attr(0.02)))
	if got := lineMassGPM(fallback); math.Abs(got-0.02) > 1e-9 {
		t.Fatalf("无线径时应回退 lgjeahdobch，实得 %f", got)
	}
}

// 分项摘要要能在日志里定位到最重的几件东西。
func TestFormatCastWeightParts(t *testing.T) {
	t.Parallel()
	parts := []castWeightPart{
		{Label: "float:bob_1", Weight: 4},
		{Label: "lure:pilker", Weight: 173.7},
		{Label: "sinker:psinker", Weight: 30},
	}
	got := formatCastWeightParts(parts, 0.27)
	if got == "" {
		t.Fatal("分项摘要不应为空")
	}
	for _, want := range []string{"lure:pilker=173.70g", "sinker:psinker=30.00g", "running_line=0.27g"} {
		if !strings.Contains(got, want) {
			t.Fatalf("摘要缺少 %q: %s", want, got)
		}
	}
}

// f5 装配表的 slot 就是物品品类号。旧实现按 1..8 的槽位号映射，
// 结果主线、鱼钩、浮漂全部退化成 default 磨损率。
// main 的磨损走 progress_ops.go 的 persistedSlotWearKey（前导按线磨损）。
func TestRigWearKeyFollowsAssemblyCategory(t *testing.T) {
	t.Parallel()
	wearKey := func(slot int64) string {
		if k, ok := persistedSlotWearKey[slot]; ok {
			return k
		}
		return "default"
	}
	cases := map[int64]string{
		catLine:  "line",
		catLead:  "line",
		catHook:  "hook",
		catLure:  "lure",
		catBait:  "bait",
		catFloat: "float",
		29:       "default",
		catReel:  "default",
	}
	for slot, want := range cases {
		if got := wearKey(slot); got != want {
			t.Fatalf("slot %d 应映射到 %q，实得 %q", slot, want, got)
		}
	}
	// 官服只按搏鱼受力时间磨损：仅拟饵保留按尾磨损（产品口径），其余按尾通道停扣。
	if wearPerCatch[wearKey(catLine)] != 0 || wearPerCatch[wearKey(catHook)] != 0 ||
		wearPerCatch[wearKey(catFloat)] != 0 || wearPerCatch[wearKey(catBait)] != 0 {
		t.Fatal("线/钩/浮漂/饵不应再有按尾扣耐久")
	}
	if wearPerCatch["rod"] != 0 || wearPerCatch["default"] != 0 {
		t.Fatal("竿本体与未映射件（轮体/前导线等）不应再有按尾扣耐久")
	}
	if wearPerCatch[wearKey(catLure)] <= 0 {
		t.Fatal("拟饵按尾磨损应保留")
	}
}
