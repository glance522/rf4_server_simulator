package gameserver

import (
	"context"
	"math"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

func reelPartByCategory(t *testing.T, srv *Server, rowGUID string, category int64) (*proto.Value, string) {
	t.Helper()
	row, err := srv.DB.GetItemByGUID(context.Background(), rowGUID)
	if err != nil || row == nil {
		t.Fatalf("读取轮子失败: %v", err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	for i := range fields {
		child := fields[i].Val
		def := srv.reelPartDef(context.Background(), child)
		if def != nil && def.Category == category {
			return child, def.ItemID
		}
	}
	t.Fatalf("轮子 %s 缺少品类 %d", row.ItemID, category)
	return nil, ""
}

func TestWorkshopExposesAllCompatibleReelParts(t *testing.T) {
	t.Parallel()
	srv, _ := newRigTestServer(t)
	if n, _, err := srv.LoadCompatTable(); err != nil || n < 1000 {
		t.Fatalf("兼容表未完整加载: alternatives=%d err=%v", n, err)
	}
	ctx := context.Background()
	reel, err := srv.CreateItem(ctx, 1000001, "spin_5820", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	parts := srv.repairPartsOf(ctx, reel)
	counts := map[uint32]int{}
	for _, part := range parts {
		counts[part.Cat] = len(srv.partAlternatives(ctx, reel.ItemID, part))
	}
	// Spark 2 2000S 的精确表含 6 个线杯、2 个摩擦片，再加当前零件自身；
	// 不得被旧的 3/4 个上限截断。
	if counts[8] != 7 {
		t.Fatalf("线杯工单=%d，期望当前件+6个兼容件", counts[8])
	}
	// 主动齿轮（52）2026-11-11 起只许同型号修理：官服工坊这一行没有"选择替代品"，
	// 玩家定案"主动齿轮是固定不能换的"。所以工单里只剩当前件本身。
	if counts[52] != 1 {
		t.Fatalf("齿轮工单=%d，期望只有当前件（主动齿轮不允许换型号）", counts[52])
	}
	if counts[9] != 3 {
		t.Fatalf("摩擦片工单=%d，期望当前件+2个兼容件", counts[9])
	}
	if counts[10] != 21 {
		t.Fatalf("润滑油工单=%d，期望静态库全部21种品质油", counts[10])
	}
	// 摇柄（34）2026-11-11 起是废弃字段：不磨损、不进聚合，也不出现在工坊零件
	// 列表里（官服工坊与背包都只有五行）。这里锁死"不能出现"，谁把它加回来都会红。
	if _, ok := counts[34]; ok {
		t.Fatal("摇柄已废弃，不应出现在轮子零件诊断/工坊列表中")
	}
}

func TestWorkshopOrderRefreshesDragVersionAndPerformance(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if err := srv.EnsurePlayer(ctx, uid); err != nil {
		t.Fatal(err)
	}
	p, _ := db.EnsureProfile(ctx, uid)
	if err := db.UpdateBalance(ctx, uid, 1_000_000, p.Gold, p.Bank); err != nil {
		t.Fatal(err)
	}
	reel, err := srv.CreateItem(ctx, uid, "spin_5820", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	beforeFields, _ := FieldsFromJSON(reel.FieldsJSON)
	createdIdx := fieldIdxByCS(reel.WireClass, csCreated)
	beforeVersion := u64Of(beforeFields[createdIdx].Num)

	order := wiregen.MustMakeObj("gcllbdkgkna", map[int]*proto.Value{
		0: proto.U32(9), 1: proto.StrVal("ef-fb-sp.orig-5805"),
	})
	req := proto.Args524(
		proto.StrVal("workshop_01"),
		wiregen.MustMakeObj("nfmkdcbnkhp", map[int]*proto.Value{
			0: proto.Guid(reel.GUID),
			1: proto.Args(proto.S("503"), order),
			2: proto.U32(0),
			3: proto.U64(0),
		}),
	)
	sess := newProbeSess(srv)
	hctx := &Ctx{Sess: sess, Main: 21, Sub: 3, Tree: req, DecodeOK: true,
		ExpectResponse: true, Log: logging.Discard()}
	body, ok := hWorkshopOrder(hctx)
	if !ok || len(body) == 0 {
		t.Fatal("21/3 更换摩擦片未返回更新物品")
	}
	if _, _, err := proto.DecodeValue(body, 0, srv.Table()); err != nil {
		t.Fatalf("21/3 响应不可解码: %v", err)
	}
	if len(hctx.afterResponse) != 1 {
		t.Fatalf("工作坊成功后应注册一次 4/22 刷新，实得 %d", len(hctx.afterResponse))
	}

	// 银币换件要等：入队当场不写 item 行，到期结算才落地。版本戳递增、
	// 曳力重建、物理参数刷新都发生在结算这一步。
	expireWorkshopJobs(t, db, uid)
	if settled := settleWorkshopForTest(t, srv, uid); len(settled) != 1 {
		t.Fatalf("到期应结算 1 件，实际 %v", settled)
	}

	fresh, _ := db.GetItemByGUID(ctx, reel.GUID)
	freshFields, _ := FieldsFromJSON(fresh.FieldsJSON)
	if got := u64Of(freshFields[createdIdx].Num); got <= beforeVersion {
		t.Fatalf("物品版本戳未递增: %d -> %d", beforeVersion, got)
	}
	drag, itemID := reelPartByCategory(t, srv, reel.GUID, 9)
	if itemID != "ef-fb-sp.orig-5805" {
		t.Fatalf("摩擦片仍为 %s", itemID)
	}
	dragIdx := fieldIdxByCS(drag.Class, csDragForce)
	dragField := drag.Field(dragIdx)
	gotDrag := float64(proto.F32FromBits(dragField.U32()))
	if math.Abs(gotDrag-3.5) > 0.001 {
		t.Fatalf("改装摩擦力=%f，期望 3.5kg", gotDrag)
	}
	perf, err := db.GetReelPerformance(ctx, reel.GUID)
	if err != nil {
		t.Fatal(err)
	}
	// 统一口径：base=换装摩擦片的 3.5（1~29 档泄力）；locked=机构强度（spin_5820 规格 3.4）
	// × 轴承子件耐久（满耐久=1），不再吃轴承品质/齿轮倍率。
	_, _, bearingDur := srv.reelPartStateFromRow(ctx, fresh, reelPartCatBearing)
	wantLocked := 3.4 * clampUnit(bearingDur)
	if math.Abs(perf.BaseDragKg-3.5) > 0.001 || math.Abs(perf.LockedDragKg-wantLocked) > 0.001 {
		t.Fatalf("轮子物理参数=%+v，期望 base=3.5 locked=%f", perf, wantLocked)
	}
	if perf.BearingItemID != "ef-sp_001_bb" || perf.BearingQuality != 10 ||
		perf.OilItemID != "ef_reel_oil" || perf.OilQuality != 15 {
		t.Fatalf("轴承/润滑油参数未写入物理表: %+v", perf)
	}
	if math.Abs(perf.CastFriction-reelCastFriction(10, 1, 15, 1, 1)) > 1e-9 ||
		math.Abs(perf.CastEfficiency-(1-perf.CastFriction)) > 1e-9 {
		t.Fatalf("轮子抛投摩擦参数未随工坊零件刷新: %+v", perf)
	}
}

func TestReelWorkshopStatsSurviveRelogTreeBuild(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	// 这条用例守的是"重登重建物品树后零件/力度仍对得上"，需要换件当场落库，
	// 所以显式走即时路径（银币排队的时序由 workshop_repair_queue_test.go 覆盖）。
	setWorkshopSilverRepairDelay(t, srv, db, false)
	ctx := context.Background()
	const uid = int64(1000001)
	if err := srv.EnsurePlayer(ctx, uid); err != nil {
		t.Fatal(err)
	}
	p, _ := db.EnsureProfile(ctx, uid)
	if err := db.UpdateBalance(ctx, uid, 1_000_000, p.Gold, p.Bank); err != nil {
		t.Fatal(err)
	}
	reel, err := srv.CreateItem(ctx, uid, "spin_5354", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}

	orders := proto.Args(proto.S("503"),
		wiregen.MustMakeObj("gcllbdkgkna", map[int]*proto.Value{
			0: proto.U32(9), 1: proto.StrVal("ef-fb-sp.orig-5805"),
		}),
		wiregen.MustMakeObj("gcllbdkgkna", map[int]*proto.Value{
			0: proto.U32(52), 1: proto.StrVal("rgm_zmn_jet_jsg-r"),
		}),
	)
	req := proto.Args524(
		proto.StrVal("workshop_01"),
		wiregen.MustMakeObj("nfmkdcbnkhp", map[int]*proto.Value{
			0: proto.Guid(reel.GUID),
			1: orders,
			2: proto.U32(0),
			3: proto.U64(0),
		}),
	)
	sess := newProbeSess(srv)
	hctx := &Ctx{Sess: sess, Main: 21, Sub: 3, Tree: req, DecodeOK: true,
		ExpectResponse: true, Log: logging.Discard()}
	if body, ok := hWorkshopOrder(hctx); !ok || len(body) == 0 {
		t.Fatal("21/3 更换摩擦片和齿轮失败")
	}

	fresh, err := db.GetItemByGUID(ctx, reel.GUID)
	if err != nil || fresh == nil {
		t.Fatalf("重新读取轮子失败: %v", err)
	}
	// 模拟下线再上线时重新构造 4/5 和 157 使用的物品树。
	tree, err := srv.ItemToTree(ctx, fresh)
	if err != nil {
		t.Fatal(err)
	}
	si := fieldIdxByCS(tree.Class, csStrength)
	if si < 0 || si >= tree.FieldCount() || tree.Fields[si].Num == nil {
		t.Fatal("轮子树缺少传动比字段")
	}
	gotRatio := float64(proto.F32FromBits(uint32(u64Of(tree.Fields[si].Num))))
	_, gearDef, _, ok := srv.selectedReelPart(ctx, tree.Fields, 52)
	if !ok || gearDef == nil || gearDef.ItemID != "rgm_zmn_jet_jsg-r" {
		t.Fatalf("重登后齿轮=%v ok=%v，期望保留JSG-R", gearDef, ok)
	}
	// 统一口径：重登后轮根=出厂规格×轴承耐久（spin_5354 规格 20.84），
	// 不能是传动比，也不再跟随摩擦片值。
	wantStrength, ok := reelRootSpec("spin_5354")
	if !ok {
		t.Fatal("规格表缺 spin_5354")
	}
	if math.Abs(gotRatio-wantStrength) > 0.001 {
		t.Fatalf("重登后轮子力度上限=%f，期望规格%f，不能是传动比%f", gotRatio, wantStrength, itemDefF32(gearDef, gearAttrRatio)*fresh.Workmanship)
	}

	drag, dragDef, _, ok := srv.selectedReelPart(ctx, tree.Fields, 9)
	if !ok || dragDef == nil || dragDef.ItemID != "ef-fb-sp.orig-5805" {
		t.Fatalf("重登后摩擦片=%v ok=%v，期望保留5805", dragDef, ok)
	}
	di := fieldIdxByCS(drag.Class, csDragForce)
	if di < 0 || drag.Fields[di].Num == nil {
		t.Fatal("摩擦片缺少制动力字段")
	}
	gotDrag := float64(proto.F32FromBits(uint32(u64Of(drag.Fields[di].Num))))
	if math.Abs(gotDrag-3.5) > 0.001 {
		t.Fatalf("重登后摩擦力=%f，期望保留3.5kg", gotDrag)
	}

}

func TestReelOverloadUsesDurabilityStages(t *testing.T) {
	t.Parallel()
	for durability, want := range map[float64]float64{
		1.0: 1.4, 0.8: 1.3, 0.6: 1.2, 0.4: 1.1, 0.2: 1.0,
	} {
		if got := reelLockMultiplier(durability); math.Abs(got-want) > 1e-9 {
			t.Fatalf("耐久 %.1f 倍率=%f，期望 %f", durability, got, want)
		}
	}
	for quality, want := range map[float64]float64{0: 0.90, 10: 0.91, 75: 0.975, 100: 1.0} {
		if got := reelBearingFactor(quality, 1); math.Abs(got-want) > 1e-9 {
			t.Fatalf("轴承品质 %.0f 传力系数=%f，期望 %f", quality, got, want)
		}
	}
	// 轴承自身磨旧：耐久从 1 掉到 0 最多再减 15%，换件能整段找回来。
	for durability, want := range map[float64]float64{1: 1.0, 0.6: 0.94, 0.2: 0.88, 0: 0.85} {
		if got := reelBearingFactor(100, durability); math.Abs(got-want) > 1e-9 {
			t.Fatalf("轴承耐久 %.1f 传力系数=%f，期望 %f", durability, got, want)
		}
	}
	// 齿轮效率：满品质满耐久不加成，磨完最多再减 10%。
	for durability, want := range map[float64]float64{1: 1.0, 0.5: 0.95, 0: 0.90} {
		if got := reelGearFactor(100, durability); math.Abs(got-want) > 1e-9 {
			t.Fatalf("齿轮耐久 %.1f 传动系数=%f，期望 %f", durability, got, want)
		}
	}
	if got := reelGearFactor(0, 1); math.Abs(got-0.95) > 1e-9 {
		t.Fatalf("0 品质齿轮传动系数=%f，期望 0.95", got)
	}
	for quality, want := range map[float64]float64{15: 1.175, 70: 0.90, 100: 0.75} {
		if got := reelOilWearFactor(quality, 1); math.Abs(got-want) > 1e-9 {
			t.Fatalf("润滑油品质 %.0f 磨损系数=%f，期望 %f", quality, got, want)
		}
	}
	if got := reelOilWearFactor(100, 0); math.Abs(got-1.0) > 1e-9 {
		t.Fatalf("轮油干涸后应失去品质保护，磨损系数=%f，期望 1.0", got)
	}
	if !(reelOilWearFactor(100, 1) < reelOilWearFactor(100, .5) &&
		reelOilWearFactor(100, .5) < reelOilWearFactor(100, 0)) {
		t.Fatal("轮油剩余耐久下降时摩擦片磨损倍率应逐步上升")
	}

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
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, reel.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)

	// —— 客户端已上报锁轮（线上 29 / UI 30）：锁轮承力参与最弱环比较 ——
	if err := db.UpsertRodSetting(ctx, rod.GUID, reelDragSettingKey, "29"); err != nil {
		t.Fatal(err)
	}
	decision, err := srv.resolveGearBreak(ctx, uid, rod.GUID, reel.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Part != "reel" || decision.ReelSlip {
		t.Fatalf("锁轮承力最弱时应判渔轮损坏(ReelSlip=false): %+v", decision)
	}
	lockOutcome, err := srv.breakLineOrRod(ctx, uid, rod.GUID, reel.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if !lockOutcome.ReelDamaged || !lockOutcome.ReelBroken || lockOutcome.FrictionWorn || lockOutcome.LineBroken {
		t.Fatalf("上报渔轮损坏结果错误: %+v", lockOutcome)
	}
	lockFresh, _ := db.GetItemByGUID(ctx, reel.GUID)
	if lockFresh.Durability != 0 {
		t.Fatalf("渔轮损坏后耐久应直接归 0，实得 %f", lockFresh.Durability)
	}
}

// TestReelSlipDegradesFrictionWasherByStep 验证单次泄力打滑让摩擦片制动力
// 按 reelSlipWear 比例衰减，且与轮油无关（摩擦片发热磨损，油润滑齿轮不润滑摩擦片）。
func TestReelSlipDegradesFrictionWasherByStep(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	reel, err := srv.CreateItem(ctx, uid, "spin_5354", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	before, perr := srv.syncReelPerformance(ctx, reel)
	if perr != nil || before.BaseDragKg <= 0 {
		t.Fatalf("轮子应有摩擦片制动力: %+v err=%v", before, perr)
	}
	if _, err := srv.damageReelSlip(ctx, uid, zeroGUID, reel.GUID); err != nil {
		t.Fatal(err)
	}
	fresh, _ := db.GetItemByGUID(ctx, reel.GUID)
	// 本体那一格是零件综合：磨掉单次基准的摩擦片，外面只随它的权重微降，
	// 不会把本体一次性打掉；但它确实会动，不再是“磨件不伤本体”。
	wantBody := 1 - reelBodyWeightWasher*reelSlipWear
	if math.Abs(fresh.Durability-wantBody) > 1e-6 {
		t.Fatalf("首次打滑后本体耐久=%f，应只随摩擦片权重微降", fresh.Durability)
	}
	after, perr := srv.syncReelPerformance(ctx, fresh)
	if perr != nil {
		t.Fatal(perr)
	}
	want := before.BaseDragKg * (1 - reelSlipWear)
	if math.Abs(after.BaseDragKg-want) > 0.01 {
		t.Fatalf("摩擦片制动力应按单次基准衰减：before=%v after=%v want=%v", before.BaseDragKg, after.BaseDragKg, want)
	}
}

func TestHandRodIgnoresBackpackReel(t *testing.T) {
	t.Parallel()
	srv, _ := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rod, _ := srv.CreateItem(ctx, uid, "tele_10180_4", "backpack", 1)
	line, _ := srv.CreateItem(ctx, uid, "mono_15095", "backpack", 1)
	_, _ = srv.CreateItem(ctx, uid, "spin_5820", "backpack", 1)
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	ids := srv.rigGearIDs(ctx, uid, rod.GUID)
	if ids.Reel != "" || ids.ReelGUID != "" {
		t.Fatalf("手竿错误引用了背包轮子: %+v", ids)
	}
	spec := srv.loadGearSpecForRig(ctx, ids, rod.GUID)
	if spec.ReelPresent {
		t.Fatal("手竿承力链不应包含轮子")
	}
}
