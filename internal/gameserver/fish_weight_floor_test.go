package gameserver

import (
	"context"
	"math"
	"testing"
)

// 「未达标」档的下界必须用该鱼种的自然最小体重（species_weight.min_g），
// 而不是旧实现写死的 1 g：1 g 鲤鱼、5 g 狗鱼这类个体就是那样抽出来的。
func TestGradeRatioBelowBandHonorsSpeciesMinWeight(t *testing.T) {
	srv, db := newDetachTestServer(t)
	enableGradeRatio(t, srv, db)

	const val, tro, sup = int64(1000), int64(5000), int64(8000)
	const minG = int64(120)
	lines := cachedGradeLines{
		val: ptrI64(val), trophy: ptrI64(tro), blue: ptrI64(sup), minG: ptrI64(minG),
	}

	below := 0
	for i := 0; i < 4000; i++ {
		w, ok := srv.sampleWeightByGradeRatio(lines, 1, 1, 0)
		if !ok {
			t.Fatalf("第 %d 次抽样没有结果", i)
		}
		if gradeRatioGradeOf(w, val, tro, sup) != 1 {
			continue
		}
		below++
		if w < minG {
			t.Fatalf("未达标个体 %d g 低于该鱼种最小体重 %d g", w, minG)
		}
	}
	if below == 0 {
		t.Fatal("样本里没有未达标个体，用例无效")
	}
}

// 缺 min_g 数据时保持旧行为（下界 1 g），避免未登记鱼种抽不出鱼。
func TestGradeRatioBelowBandFallsBackToOneGram(t *testing.T) {
	srv, db := newDetachTestServer(t)
	enableGradeRatio(t, srv, db)

	const val, tro, sup = int64(1000), int64(5000), int64(8000)
	lines := cachedGradeLines{val: ptrI64(val), trophy: ptrI64(tro), blue: ptrI64(sup)}

	minSeen := int64(math.MaxInt64)
	for i := 0; i < 4000; i++ {
		w, ok := srv.sampleWeightByGradeRatio(lines, 1, 1, 0)
		if !ok {
			t.Fatalf("第 %d 次抽样没有结果", i)
		}
		if g := gradeRatioGradeOf(w, val, tro, sup); g == 1 && w < minSeen {
			minSeen = w
		}
	}
	if minSeen != math.MaxInt64 && minSeen >= 120 {
		t.Fatalf("缺少 min_g 时应仍从 1 g 起采样，实测最小未达标个体 %d g", minSeen)
	}
}

// 数据异常（min_g ≥ 达标线）时退回达标线 5%，未达标档不能因此消失。
func TestGradeRatioBelowBandGuardsMinAboveLine(t *testing.T) {
	srv, db := newDetachTestServer(t)
	enableGradeRatio(t, srv, db)

	const val = int64(1000)
	lines := cachedGradeLines{
		val: ptrI64(val), trophy: ptrI64(5000), blue: ptrI64(8000), minG: ptrI64(1200),
	}
	model, handled := srv.gradeRatioModel(lines, 1, 1, 0)
	if !handled {
		t.Fatal("档位模型不可用")
	}
	found := false
	for _, b := range model.bands {
		if b.grade != 1 {
			continue
		}
		found = true
		if math.Abs(b.lo-50) > 1e-9 {
			t.Fatalf("未达标档下界应为达标线的 5%%（50 g），实得 %.2f", b.lo)
		}
	}
	if !found {
		t.Fatal("未达标档不应因 min_g 越界而消失")
	}
}

// min_g 缺失的鱼种（静态库 207 个）由 sampleWeightDB 层兜底：下界 = 达标线×6%，
// 不再退回 1 g——否则 1 g 小鱼会从这批鱼种继续漏出来。
func TestSampleWeightDBFloorsBelowBandWhenMinGMissing(t *testing.T) {
	srv, db := newDetachTestServer(t)
	enableGradeRatio(t, srv, db)
	// 直接给 cachedSpeciesMinG 喂"无数据"：不给 species_weight 插行。
	// settle[0]（达标线）来自 settleStatics，对未登记鱼种按奖杯线估算；
	// 为了测试可控，直接注入 roll cache 的 settleStatics。
	c := rollContext(nil) // rollContext 会补上 roll cache
	cache := c.cache
	const valLine = int64(1000)
	cache.mu.Lock()
	cache.settleStatics["floor_probe"] = [6]int64{valLine, 5000, 8000, 10000, 8800, 9600}
	cache.mu.Unlock()

	below := 0
	minSeen := int64(math.MaxInt64)
	for i := 0; i < 4000; i++ {
		w := srv.sampleWeightDB(context.Background(), "floor_probe", 1, 1, 0, c)
		if w <= 0 {
			t.Fatalf("第 %d 次抽样没有结果", i)
		}
		if w >= valLine {
			continue
		}
		below++
		if w < minSeen {
			minSeen = w
		}
	}
	if below == 0 {
		t.Fatal("样本里没有未达标个体，用例无效")
	}
	floor := belowGradeFloorFromVal(valLine) // 60
	if minSeen < floor {
		t.Fatalf("min_g 缺失时未达标个体最小应 ≥ 兜底下界 %d g，实测 %d g", floor, minSeen)
	}
}

func TestBelowGradeFloorFromVal(t *testing.T) {
	cases := []struct {
		val, want int64
	}{
		{0, 0}, {-5, 0}, // 无达标线：不兜底
		{1000, 30}, // 6% = 60，但钳到上限 30 g
		{100, 6},
		{10, 1},    // 四舍五入后钳 1
		{5000, 30}, // 上限 30 g
		{100000, 30},
	}
	for _, c := range cases {
		if got := belowGradeFloorFromVal(c.val); got != c.want {
			t.Fatalf("belowGradeFloorFromVal(%d) = %d, want %d", c.val, got, c.want)
		}
	}
}
