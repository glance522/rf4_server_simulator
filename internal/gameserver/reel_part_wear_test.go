package gameserver

// 渔轮/鱼竿零件受力磨损与本体耐久聚合：specs/changes/wear-fight-time/spec.md。
// 零件按搏鱼受力秒数缓慢消耗，速率缺省值对齐官服抓包实测（lure2 n.pike 17s 窗口）；
// 旋钮（34）/手柄（4）/摩擦片不入矩阵；本体那一格等于零件的加权综合。

import (
	"context"
	"math"
	"testing"

	"rf4server/internal/store"
)

// TestReelPartWearMatchesOfficialRates 用官服实测字面量钉住速率：lure2 n.pike
// 搏鱼窗口 17s 各件 Δ%（docs/实测博鱼过程对装备磨损的影响分析.md §3）。期望值
// 一律取抓包实测速率，不允许用实现代码复算；矩阵缺省档位取存档方案档位，
// 与官服字面量的偏差在 ±20% 内。
func TestReelPartWearMatchesOfficialRates(t *testing.T) {
	const fightSecs = 17.0
	official := []struct {
		category int64
		rate     float64 // 官服实测 %/s
	}{
		{reelPartCatOil, 0.00038},
		{reelPartCatGear, 0.00039},
		{reelPartCatGuides, 0.00041},
		{reelPartCatBearing, 0.00014},
		{reelPartCatSpool, 0.00012},
		{reelPartCatRodBlank, 0.00003},
	}
	counts := reelPartCounts{StressSecs: fightSecs}
	for _, tc := range official {
		want := tc.rate * fightSecs / 100 // 官服实测的寿命比例
		got := reelPartWearFor(tc.category, 100, counts, reelPartWearMatrix[tc.category].perStressSec)
		if got <= 0 {
			t.Fatalf("品类 %d 受力 %vs 应耗耐久", tc.category, fightSecs)
		}
		if math.Abs(got-want) > want*0.2 {
			t.Fatalf("品类 %d 官服 17s 应磨 %.6f（%.5f%%/s），实得 %.6f",
				tc.category, want, tc.rate, got)
		}
	}
	// 摩擦片不参与受力矩阵：它的账由打滑单独结，再叠一层会变成第二个摩擦片。
	if got := reelPartWearFor(reelPartCatWasher, 100, counts, 1); got != 0 {
		t.Fatalf("摩擦片不应参与受力矩阵，实得 %v", got)
	}
	// 旋钮（34）与手柄（4）官服实测恒 1.0：不入矩阵。
	for _, category := range []int64{reelPartCatHandle, reelPartCatRodHandle} {
		if got := reelPartWearFor(category, 100, counts, 1); got != 0 {
			t.Fatalf("品类 %d 官服实测不磨损，实得 %v", category, got)
		}
	}
	// 断件次数不扣寿命（无官服数据，仅诊断）。
	if got := reelPartWearFor(reelPartCatSpool, 100, reelPartCounts{BreakHits: 1}, 1); got != 0 {
		t.Fatalf("断件冲击不应扣寿命，实得 %v", got)
	}
	// 品质寿命系数：低档货明显更不耐用；品质 100 恰为官服速率（标定约定）。
	for quality, want := range map[float64]float64{0: 0.35, 15: 0.3625, 40: 0.55, 100: 1.0} {
		if got := reelPartLifeFactor(quality); math.Abs(got-want) > 1e-9 {
			t.Fatalf("品质 %.0f 寿命系数=%v，期望 %v", quality, got, want)
		}
	}
	if math.Abs(reelPartWearFor(reelPartCatOil, 15, counts, 0.0004)/reelPartWearFor(reelPartCatOil, 100, counts, 0.0004)-
		1/reelPartLifeFactor(15)) > 1e-9 {
		t.Fatal("消耗量必须除以品质寿命系数")
	}
	// 生产口径：速率走 tunable，非法值回落矩阵缺省。
	srv := &Server{}
	if got := srv.reelStressRateFor(reelPartCatOil); math.Abs(got-0.0004) > 1e-12 {
		t.Fatalf("轮油受力速率缺省应为 0.0004，实得 %v", got)
	}
	if got := srv.reelStressRateFor(reelPartCatWasher); got != 0 {
		t.Fatalf("矩阵外品类速率应为 0，实得 %v", got)
	}
}

const wearTestUID = int64(1000001)

// wearReelFixture 造一只六件俱全的轮子（Spark 2 2000S）并同步出出厂物理参数。
func wearReelFixture(t *testing.T) (*Server, *store.DB, context.Context, string, *store.ReelPerformance) {
	t.Helper()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	reel, err := srv.CreateItem(ctx, wearTestUID, "spin_5820", "backpack", 1)
	if err != nil {
		t.Fatalf("造轮失败: %v", err)
	}
	row, err := db.GetItemByGUID(ctx, reel.GUID)
	if err != nil || row == nil {
		t.Fatalf("读取轮子失败: %v", err)
	}
	perf, err := srv.syncReelPerformance(ctx, row)
	if err != nil || perf == nil || perf.BaseDragKg <= 0 {
		t.Fatalf("出厂轮子应有泄力: %v", err)
	}
	return srv, db, ctx, reel.GUID, perf
}

// wearReelRow 重新读库，避免拿旧指针判新磨损。
func wearReelRow(t *testing.T, db *store.DB, ctx context.Context, guid string) *store.Item {
	t.Helper()
	row, err := db.GetItemByGUID(ctx, guid)
	if err != nil || row == nil {
		t.Fatalf("读取轮子失败: %v", err)
	}
	return row
}

// reelBodyFromStore 读库里本体当前耐久与按零件聚合出来的耐久。
func reelBodyFromStore(t *testing.T, srv *Server, db *store.DB, ctx context.Context, guid string) (float64, float64) {
	t.Helper()
	row := wearReelRow(t, db, ctx, guid)
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatalf("解析轮内零件失败: %v", err)
	}
	body, ok := srv.reelBodyDurabilityInFields(ctx, row, fields)
	if !ok {
		t.Fatal("轮子应能按零件聚合出本体耐久")
	}
	return row.Durability, body
}

// newFightAccount 造一笔本竿搏鱼受力账，并指向测试轮子。
func newFightAccount(rated float64, perf *store.ReelPerformance, reelGUID string) fightReelWear {
	w := newFightWearForTest(rated, perf.BaseDragKg, perf.BaseDragKg*3, 1)
	w.rodGUID, w.reelGUID = "rod0000000000000000000000000000001", reelGUID
	return w
}

// TestFightSettlementWearsPartsAndBody 确认一次结算同时写到三处：摩擦片制动力、
// 轮内零件耐久、本体耐久。只写其中一处就会出现“片磨了但配件与外面那格不动”。
func TestFightSettlementWearsPartsAndBody(t *testing.T) {
	srv, db, ctx, reelGUID, before := wearReelFixture(t)
	rated, ok := srv.reelWasherRatedFromRow(ctx, wearReelRow(t, db, ctx, reelGUID))
	if !ok || rated <= 0 {
		t.Fatal("读不到摩擦片额制制动力")
	}
	w := newFightAccount(rated, before, reelGUID)
	w.SlipWear = 0.5  // 本竿烧掉一半摩擦片寿命
	w.SlipTicks = 10  // 诊断口径
	w.StressSecs = 17 // 一场 n.pike 量级的搏鱼受力
	if !srv.applyFightReelWear(ctx, wearTestUID, w, "14/4", true) {
		t.Fatal("本竿磨损应落库")
	}

	afterRow := wearReelRow(t, db, ctx, reelGUID)
	after, err := srv.syncReelPerformance(ctx, afterRow)
	if err != nil {
		t.Fatalf("重建物理参数失败: %v", err)
	}
	if math.Abs(after.BaseDragKg-before.BaseDragKg*0.5) > before.BaseDragKg*0.02 {
		t.Fatalf("烧掉一半寿命后泄力应约半：%.3f -> %.3f", before.BaseDragKg, after.BaseDragKg)
	}
	// 摩擦片耐久字段是制动力剩余比例的镜像：背包与工坊靠它才能看到损耗%。
	if _, _, washerDur := srv.reelPartStateFromRow(ctx, afterRow, reelPartCatWasher); math.Abs(washerDur-0.5) > 1e-6 {
		t.Fatalf("摩擦片耐久应镜像制动力剩余比例，实得 %v", washerDur)
	}

	counts := w.partCounts()
	for _, category := range reelWearPartCategories {
		_, quality, durability := srv.reelPartStateFromRow(ctx, afterRow, category)
		want := 1 - reelPartWearFor(category, quality, counts, srv.reelStressRateFor(category))
		if durability >= 1 || math.Abs(durability-want) > 1e-6 {
			t.Fatalf("品类 %d 本竿应耗到 %.6f，实得 %.6f", category, want, durability)
		}
	}

	stored, aggregated := reelBodyFromStore(t, srv, db, ctx, reelGUID)
	if math.Abs(stored-aggregated) > 1e-6 {
		t.Fatalf("本体耐久必须等于零件聚合：库里 %.6f 聚合 %.6f", stored, aggregated)
	}
	if stored >= 1 {
		t.Fatalf("烧掉一半摩擦片后本体耐久应下降，实得 %v", stored)
	}
	washerLife := washerLifeAfterTest(srv, ctx, afterRow)
	if stored > washerLife+reelBodyWeakSpotTolerance+1e-6 {
		t.Fatalf("本体不能比最弱熔断件好太多：本体 %.3f 摩擦片 %.3f", stored, washerLife)
	}
}

// TestBurnedWasherBreaksReel 确认摩擦片烧穿后的后果：制动力与承力归零（收不了线），
// 本体耐久归零（客户端红框、只能剪线后修）。
func TestBurnedWasherBreaksReel(t *testing.T) {
	srv, db, ctx, reelGUID, perf := wearReelFixture(t)
	rated, _ := srv.reelWasherRatedFromRow(ctx, wearReelRow(t, db, ctx, reelGUID))
	w := newFightAccount(rated, perf, reelGUID)
	w.SlipWear = 1
	if !srv.applyFightReelWear(ctx, wearTestUID, w, "14/4", true) {
		t.Fatal("本竿磨损应落库")
	}
	row := wearReelRow(t, db, ctx, reelGUID)
	fresh, err := srv.syncReelPerformance(ctx, row)
	if err != nil {
		t.Fatalf("重建物理参数失败: %v", err)
	}
	if fresh.BaseDragKg > 1e-9 || fresh.LockedDragKg > 1e-9 {
		t.Fatalf("烧穿后泄力与承力都应失效：base=%.6f locked=%.6f", fresh.BaseDragKg, fresh.LockedDragKg)
	}
	if row.Durability != 0 {
		t.Fatalf("摩擦片烧穿后本体耐久应熔断到 0，实得 %v", row.Durability)
	}
	// 收不了线必须能修回来：换件与进修理都允许把本体抬回去。
	if changed, rerr := srv.restoreReelInternals(ctx, row); !changed || rerr != nil {
		t.Fatal("修理应恢复轮内零件")
	}
	row = wearReelRow(t, db, ctx, reelGUID)
	if back, _ := srv.syncReelPerformance(ctx, row); back.BaseDragKg <= 0 {
		t.Fatal("恢复零件后制动力应回来")
	}
	if !srv.recalcReelBodyDurability(ctx, row) {
		t.Fatal("换件后本体耐久应该能抬回来")
	}
	if row.Durability <= 0 {
		t.Fatalf("本体耐久应从 0 恢复，实得 %v", row.Durability)
	}
}

// TestEmptyFightSettlesNothing 钉住官服口径（底钓2 66min 空竿段全零件零磨损）：
// 没有受力秒的账（空竿、抛竿、等待）不产生任何落库，摩擦片与轮件一格都不扣。
func TestEmptyFightSettlesNothing(t *testing.T) {
	srv, db, ctx, reelGUID, perf := wearReelFixture(t)
	rated, _ := srv.reelWasherRatedFromRow(ctx, wearReelRow(t, db, ctx, reelGUID))
	w := newFightAccount(rated, perf, reelGUID)
	if srv.applyFightReelWear(ctx, wearTestUID, w, "14/4", true) {
		t.Fatal("空账不应落库")
	}
	row := wearReelRow(t, db, ctx, reelGUID)
	fresh, err := srv.syncReelPerformance(ctx, row)
	if err != nil {
		t.Fatalf("重建物理参数失败: %v", err)
	}
	if math.Abs(fresh.BaseDragKg-perf.BaseDragKg) > 1e-9 {
		t.Fatalf("空竿不该磨摩擦片：%.6f -> %.6f", perf.BaseDragKg, fresh.BaseDragKg)
	}
	for _, category := range reelWearPartCategories {
		_, _, durability := srv.reelPartStateFromRow(ctx, row, category)
		if durability != 1 {
			t.Fatalf("空竿品类 %d 耐久应保持 1，实得 %.6f", category, durability)
		}
	}
	stored, _ := reelBodyFromStore(t, srv, db, ctx, reelGUID)
	if math.Abs(stored-1) > 1e-9 {
		t.Fatalf("空竿后本体耐久应保持 1，实得 %.6f", stored)
	}
}

// TestBodyDurabilityNeverRaisedByWear 确认单调保护：存量轮子（零件全 1.0 但本体已磨旧）
// 不能被聚合公式刷回满值。
func TestBodyDurabilityNeverRaisedByWear(t *testing.T) {
	srv, db, ctx, reelGUID, perf := wearReelFixture(t)
	if err := db.SetItemDurability(ctx, reelGUID, 0.795); err != nil {
		t.Fatalf("写本体耐久失败: %v", err)
	}
	rated, _ := srv.reelWasherRatedFromRow(ctx, wearReelRow(t, db, ctx, reelGUID))
	w := newFightAccount(rated, perf, reelGUID)
	w.StressSecs = 17
	if !srv.applyFightReelWear(ctx, wearTestUID, w, "14/4", true) {
		t.Fatal("本竿磨损应落库")
	}
	stored, aggregated := reelBodyFromStore(t, srv, db, ctx, reelGUID)
	if stored > 0.795+1e-9 {
		t.Fatalf("磨损不得把本体刷回去：0.795 -> %v", stored)
	}
	if aggregated <= stored {
		t.Fatalf("零件还很新时聚合值应高于本体，说明单调保护没在挡：聚合 %.3f 本体 %.3f", aggregated, stored)
	}
}

// TestReelOilDryIsNotABreakdown 确认轮油只进加权不熔断：油干了整机变差，
// 但不能直接把轮子判成损坏。
func TestReelOilDryIsNotABreakdown(t *testing.T) {
	srv, db, ctx, reelGUID, _ := wearReelFixture(t)
	setReelPartDurabilityForTest(t, srv, db, ctx, reelGUID, reelPartCatOil, 0)
	_, aggregated := reelBodyFromStore(t, srv, db, ctx, reelGUID)
	if math.Abs(aggregated-(1-reelBodyWeightOil)) > 1e-6 {
		t.Fatalf("轮油干涸只应扣掉它自己的权重：聚合 %.6f，期望 %.6f", aggregated, 1-reelBodyWeightOil)
	}
}

// TestFusePartFailureZeroesBody 确认熔断件归零就是损坏：齿轮磨光 → 本体 0。
func TestFusePartFailureZeroesBody(t *testing.T) {
	srv, db, ctx, reelGUID, _ := wearReelFixture(t)
	setReelPartDurabilityForTest(t, srv, db, ctx, reelGUID, reelPartCatGear, 0)
	if _, aggregated := reelBodyFromStore(t, srv, db, ctx, reelGUID); aggregated != 0 {
		t.Fatalf("熔断件磨光后本体应为 0，实得 %v", aggregated)
	}
	// 未磨光但已很弱：本体被最弱短板拉到 短板 + 容差。
	setReelPartDurabilityForTest(t, srv, db, ctx, reelGUID, reelPartCatGear, 0.3)
	_, aggregated := reelBodyFromStore(t, srv, db, ctx, reelGUID)
	if math.Abs(aggregated-(0.3+reelBodyWeakSpotTolerance)) > 1e-6 {
		t.Fatalf("本体应被最弱件拉到 %.3f，实得 %.3f", 0.3+reelBodyWeakSpotTolerance, aggregated)
	}
}

// washerLifeAfterTest 用制动力反推摩擦片剩余寿命（裁决口径，不看镜像字段）。
func washerLifeAfterTest(srv *Server, ctx context.Context, row *store.Item) float64 {
	rated, ok := srv.reelWasherRatedFromRow(ctx, row)
	if !ok || rated <= 0 {
		return 1
	}
	return reelWasherLife(srv.reelBaseDragFromRow(ctx, row), rated)
}
