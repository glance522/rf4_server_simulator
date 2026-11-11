package gameserver

import (
	"context"
	"math"
	"testing"

	"rf4server/internal/store"
)

func electricReelRow(t *testing.T, srv *Server, db *store.DB, ctx context.Context, itemID string) *store.Item {
	t.Helper()
	reel, err := srv.CreateItem(ctx, wearTestUID, itemID, "backpack", 1)
	if err != nil || reel == nil {
		t.Fatalf("造电轮失败: %v", err)
	}
	return wearReelRow(t, db, ctx, reel.GUID)
}

func electricMechanicalLockForTest(t *testing.T, srv *Server, ctx context.Context, row *store.Item) float64 {
	t.Helper()
	// 统一口径：规格表收录的轮子机械承力 = 出厂规格 × 轴承子件耐久；未收录回退旧公式。
	if spec, ok := reelRootSpec(row.ItemID); ok && row.Durability > 0 {
		_, _, bearingDurability := srv.reelPartStateFromRow(ctx, row, reelPartCatBearing)
		return spec * clampUnit(bearingDurability)
	}
	_, bearingQuality, bearingDurability := srv.reelPartStateFromRow(ctx, row, reelPartCatBearing)
	_, gearQuality, gearDurability := srv.reelPartStateFromRow(ctx, row, reelPartCatGear)
	return reelLockedDragKg(
		srv.reelBaseDragFromRow(ctx, row),
		row.Durability,
		reelBearingFactor(bearingQuality, bearingDurability),
		reelGearFactor(gearQuality, gearDurability),
	)
}

func setElectricWasherDragForTest(t *testing.T, srv *Server, db *store.DB, ctx context.Context,
	reelGUID string, dragKg float64) {
	t.Helper()
	row := wearReelRow(t, db, ctx, reelGUID)
	fields := snapshotFields(t, row.FieldsJSON)
	washer, _, _, ok := srv.selectedReelPart(ctx, fields, reelPartCatWasher)
	if !ok {
		t.Fatal("电轮缺少摩擦片")
	}
	idx := fieldIdxByCS(washer.Class, csDragForce)
	if idx < 0 || !setWireFloat(washer.Fields, washer.Class, idx, dragKg) {
		t.Fatal("写摩擦片制动力失败")
	}
	if err := db.UpdateItemFields(ctx, row.GUID, snapshotJSON(t, fields), row.Qty,
		row.Durability, row.Workmanship, row.RigGUID); err != nil {
		t.Fatalf("保存摩擦片制动力失败: %v", err)
	}
}

func replaceElectricMotorForTest(t *testing.T, srv *Server, db *store.DB, ctx context.Context,
	reelGUID, motorItemID string) {
	t.Helper()
	row := wearReelRow(t, db, ctx, reelGUID)
	fields := snapshotFields(t, row.FieldsJSON)
	idx := fieldIdxByCS(row.WireClass, "dfjpnambjcj")
	if idx < 0 || idx >= len(fields) {
		t.Fatal("电轮缺少 f14 电机槽")
	}
	motor, err := srv.buildPartInstance(ctx, row, motorItemID, "dfjpnambjcj")
	if err != nil || motor == nil {
		t.Fatalf("构造电机失败: %v", err)
	}
	setReelChildDurability(motor, 1)
	fields[idx].Val = motor
	if err := db.UpdateItemFields(ctx, row.GUID, snapshotJSON(t, fields), row.Qty,
		row.Durability, row.Workmanship, row.RigGUID); err != nil {
		t.Fatalf("保存电机失败: %v", err)
	}
}

func TestElectricReelLockedDragUsesLiveBrakeAndMotor(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	row := electricReelRow(t, srv, db, ctx, "elcon_6003")

	perf, err := srv.syncReelPerformance(ctx, row)
	if err != nil || perf == nil {
		t.Fatalf("同步电轮性能失败: %v", err)
	}
	fields := snapshotFields(t, row.FieldsJSON)
	mechanical := electricMechanicalLockForTest(t, srv, ctx, row)
	_, _, bearingDurability := srv.reelPartStateFromRow(ctx, row, reelPartCatBearing)
	_, bearingQuality, _ := srv.reelPartStateFromRow(ctx, row, reelPartCatBearing)
	_, gearQuality, gearDurability := srv.reelPartStateFromRow(ctx, row, reelPartCatGear)
	motor, _, found := srv.reelMotorCapacityInFields(ctx, row, fields,
		reelBearingFactor(bearingQuality, bearingDurability),
		reelGearFactor(gearQuality, gearDurability))
	if !found || motor <= 0 {
		t.Fatal("电轮应有有效电机承力")
	}
	want := srv.electricLockedMultiplier() * math.Min(mechanical, motor)
	if math.Abs(perf.LockedDragKg-want) > 1e-6 {
		t.Fatalf("电轮锁轮值 %.6f，期望 3×min(机械 %.6f, 电机 %.6f)=%.6f",
			perf.LockedDragKg, mechanical, motor, want)
	}
}

func TestElectricReelLockedDragTracksPartChangesWithoutCompounding(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	row := electricReelRow(t, srv, db, ctx, "elcon_5968")
	reelGUID := row.GUID

	// 把摩擦片数值抬高，让电机先成为限制项。
	setElectricWasherDragForTest(t, srv, db, ctx, reelGUID, 1000)
	first, err := srv.syncReelPerformance(ctx, wearReelRow(t, db, ctx, reelGUID))
	if err != nil || first == nil {
		t.Fatalf("首次同步失败: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, serr := srv.syncReelPerformance(ctx, wearReelRow(t, db, ctx, reelGUID))
		if serr != nil || again == nil {
			t.Fatalf("第 %d 次重复同步失败: %v", i+2, serr)
		}
		if math.Abs(again.LockedDragKg-first.LockedDragKg) > 1e-9 {
			t.Fatalf("重复同步发生复利：%.6f -> %.6f", first.LockedDragKg, again.LockedDragKg)
		}
	}

	replaceElectricMotorForTest(t, srv, db, ctx, reelGUID, "rengine_2")
	upgraded, err := srv.syncReelPerformance(ctx, wearReelRow(t, db, ctx, reelGUID))
	if err != nil || upgraded == nil {
		t.Fatalf("换电机后同步失败: %v", err)
	}
	if upgraded.LockedDragKg <= first.LockedDragKg+1e-6 {
		t.Fatalf("换更强电机后锁轮值应上升：%.6f -> %.6f",
			first.LockedDragKg, upgraded.LockedDragKg)
	}

	// 统一口径：摩擦片数值只作用于 1~29 档泄力，锁轮承力 = 规格×轴承耐久，
	// 修改摩擦片不得再影响锁轮值。
	setElectricWasherDragForTest(t, srv, db, ctx, reelGUID, 10)
	lowBrake, err := srv.syncReelPerformance(ctx, wearReelRow(t, db, ctx, reelGUID))
	if err != nil || lowBrake == nil {
		t.Fatalf("低制动同步失败: %v", err)
	}
	setElectricWasherDragForTest(t, srv, db, ctx, reelGUID, 20)
	highBrake, err := srv.syncReelPerformance(ctx, wearReelRow(t, db, ctx, reelGUID))
	if err != nil || highBrake == nil {
		t.Fatalf("高制动同步失败: %v", err)
	}
	if math.Abs(highBrake.LockedDragKg-lowBrake.LockedDragKg) > 1e-9 {
		t.Fatalf("摩擦片数值不应影响锁轮承力：%.6f -> %.6f",
			lowBrake.LockedDragKg, highBrake.LockedDragKg)
	}
}

func TestElectricReelMotorExcludedFromBodyAggregation(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	row := electricReelRow(t, srv, db, ctx, "elcon_6003")

	fields := snapshotFields(t, row.FieldsJSON)
	before, ok := srv.reelBodyDurabilityInFields(ctx, row, fields)
	if !ok || before <= 0 {
		t.Fatal("电轮应能聚合本体耐久")
	}
	motor, _, _, found := srv.selectedReelPart(ctx, fields, reelPartCatMotor)
	if !found {
		t.Fatal("电轮缺少电机")
	}
	// 电机磨损通道停扣（官服无电轮数据，2026-11-11 裁定），聚合不再纳入电机寿命：
	// 电机耐久怎么变都不应影响本体那一格，也不能把整轮判死。
	setReelChildDurability(motor, 0.2)
	after, ok := srv.reelBodyDurabilityInFields(ctx, row, fields)
	if !ok || math.Abs(after-before) > 1e-9 {
		t.Fatalf("电机停扣后不应影响本体聚合：%.6f -> %.6f", before, after)
	}

	setReelChildDurability(motor, 0)
	zero, ok := srv.reelBodyDurabilityInFields(ctx, row, fields)
	if !ok || zero <= 0 {
		t.Fatalf("电机归零不能直接把整轮判死，实得 %.6f", zero)
	}
	_, bearingQuality, bearingDurability := srv.reelPartStateFromRow(ctx, row, reelPartCatBearing)
	_, gearQuality, gearDurability := srv.reelPartStateFromRow(ctx, row, reelPartCatGear)
	mechanical := electricMechanicalLockForTest(t, srv, ctx, row)
	locked := srv.reelEffectiveLockedDragInFields(ctx, row, fields, srv.reelBaseDragFromRow(ctx, row),
		row.Durability,
		reelBearingFactor(bearingQuality, bearingDurability),
		reelGearFactor(gearQuality, gearDurability))
	if math.Abs(locked-mechanical) > 1e-9 {
		t.Fatalf("电机归零后应退回机械锁轮：%.6f vs %.6f", locked, mechanical)
	}
}

func TestElectricReelMotorNotWornByStress(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	row := electricReelRow(t, srv, db, ctx, "elcon_6003")
	fields := snapshotFields(t, row.FieldsJSON)

	motor, _, _, ok := srv.selectedReelPart(ctx, fields, reelPartCatMotor)
	if !ok {
		t.Fatal("电轮缺少电机")
	}
	motorBefore := reelChildDurability(motor)
	// 受力磨损只进矩阵内的轮件（油/齿轮/轴承/线杯）；电机不在矩阵，任何受力都不磨。
	worn := srv.wearReelPartsInFields(ctx, fields, reelPartCounts{StressSecs: 600})
	if _, hasMotor := worn["rengine_2"]; hasMotor {
		t.Fatal("电机磨损已停扣，不应出现在磨损表")
	}
	if motorAfter := reelChildDurability(motor); motorAfter != motorBefore {
		t.Fatalf("电机耐久不应变化：%.6f -> %.6f", motorBefore, motorAfter)
	}
	if len(worn) == 0 {
		t.Fatal("受力磨损应仍作用于矩阵内轮件")
	}
}

func TestNormalReelLockedDragDoesNotGetElectricMultiplier(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	reel, err := srv.CreateItem(ctx, wearTestUID, "spin_5820", "backpack", 1)
	if err != nil || reel == nil {
		t.Fatalf("造普通轮失败: %v", err)
	}
	row := wearReelRow(t, db, ctx, reel.GUID)
	mechanical := electricMechanicalLockForTest(t, srv, ctx, row)
	locked := srv.reelLockedDragFromRow(ctx, row)
	if math.Abs(locked-mechanical) > 1e-9 {
		t.Fatalf("普通轮不应吃电轮倍率：锁轮 %.6f 机械 %.6f", locked, mechanical)
	}
}
