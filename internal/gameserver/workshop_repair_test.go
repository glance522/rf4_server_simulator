package gameserver

import (
	"fmt"
	"math"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

func snapshotWorkshopRequestPartPayment(guid string, category uint32, systemID string, clientCost uint64, useGold bool) *proto.Value {
	goldFlag := uint32(0)
	if useGold {
		goldFlag = 1
	}
	return proto.Args524(proto.StrVal("workshop_01"), wiregen.MustMakeObj("nfmkdcbnkhp", map[int]*proto.Value{
		0: proto.Guid(guid),
		1: proto.Args(proto.S("503"), wiregen.MustMakeObj("gcllbdkgkna", map[int]*proto.Value{
			0: proto.U32(category),
			1: proto.StrVal(systemID),
		})),
		2: proto.U32(goldFlag),
		3: proto.U64(clientCost),
	}))
}

func snapshotWorkshopRequestPart(guid string, category uint32, systemID string, clientCost uint64) *proto.Value {
	return snapshotWorkshopRequestPartPayment(guid, category, systemID, clientCost, false)
}

func seedWornWorkshopRod(t *testing.T, srv *Server, db *store.DB, partLife, bodyLife float64) (*store.Item, string) {
	t.Helper()
	ctx := t.Context()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	fields := snapshotFields(t, rod.FieldsJSON)
	part, partDef := snapshotPart(t, srv, fields, 4)
	if part == nil || partDef == nil {
		t.Fatal("竿柄部件缺失")
	}
	setWireFloat(part.Fields, part.Class, fieldIdxByCS(part.Class, csDurability), partLife)
	raw := snapshotJSON(t, fields)
	rod.FieldsJSON = raw
	if err := db.UpdateItemFields(ctx, rod.GUID, raw, rod.Qty, bodyLife, rod.Workmanship, rod.RigGUID); err != nil {
		t.Fatalf("预置竿状态失败: %v", err)
	}
	rod.Durability = bodyLife
	return rod, partDef.ItemID
}

func seedWornWorkshopReel(t *testing.T, srv *Server, db *store.DB, gearLife, washerLife, bearingLife, bodyLife float64) (*store.Item, string) {
	t.Helper()
	ctx := t.Context()
	const uid = int64(1000001)

	reel, err := srv.CreateItem(ctx, uid, "spin_5354", "backpack", 1)
	if err != nil {
		t.Fatalf("造轮失败: %v", err)
	}
	fields := snapshotFields(t, reel.FieldsJSON)
	gear, gearDef := snapshotPart(t, srv, fields, reelPartCatGear)
	washer, washerDef := snapshotPart(t, srv, fields, reelPartCatWasher)
	bearing, _ := snapshotPart(t, srv, fields, reelPartCatBearing)
	setWireFloat(gear.Fields, gear.Class, fieldIdxByCS(gear.Class, csDurability), gearLife)
	setWireFloat(bearing.Fields, bearing.Class, fieldIdxByCS(bearing.Class, csDurability), bearingLife)
	setWireFloat(washer.Fields, washer.Class, fieldIdxByCS(washer.Class, csDurability), washerLife)
	if _, present, idx := frictionWasherDrag(washer); present {
		setWireFloat(washer.Fields, washer.Class, idx,
			itemDefF32(washerDef, gearAttrDragMax)*reel.Workmanship*washerLife)
	}
	raw := snapshotJSON(t, fields)
	reel.FieldsJSON = raw
	if err := db.UpdateItemFields(ctx, reel.GUID, raw, reel.Qty, bodyLife, reel.Workmanship, reel.RigGUID); err != nil {
		t.Fatalf("预置轮状态失败: %v", err)
	}
	reel.Durability = bodyLife
	return reel, gearDef.ItemID
}

func TestWorkshopSameModelRepairRestoresItemDurability(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	rod, partID := seedWornWorkshopRod(t, srv, db, .4, .5)

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(rod.GUID, 4, partID, 0),
		Log:  logging.Discard(),
	}
	body, ok := hWorkshopOrder(c)
	if !ok || len(body) == 0 {
		t.Fatal("同型号竿柄修理失败")
	}
	fresh := mustItem(t, db, rod.GUID)
	if fresh.Durability != 1 {
		t.Fatalf("同型号修理后物品本体耐久应回 1，实得 %v", fresh.Durability)
	}
	freshFields := snapshotFields(t, fresh.FieldsJSON)
	repairedPart, _ := snapshotPart(t, srv, freshFields, 4)
	if got := snapshotFloat(repairedPart.Fields, repairedPart.Class, csDurability); got != 1 {
		t.Fatalf("同型号修理后竿柄耐久应回 1，实得 %v", got)
	}
}

func TestWorkshopOrderRejectsLowClientQuote(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	rod, partID := seedWornWorkshopRod(t, srv, db, .8, .5)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 1000, 0, 0); err != nil {
		t.Fatal(err)
	}

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(rod.GUID, 4, partID, 1),
		Log:  logging.Discard(),
	}
	if _, ok := hWorkshopOrder(c); ok {
		t.Fatal("1 银币伪造报价应被拒绝")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Silver != 1000 {
		t.Fatalf("报价被拒绝后余额应保持 1000，实得 %d", profile.Silver)
	}
	fresh := mustItem(t, db, rod.GUID)
	if fresh.Durability != .5 {
		t.Fatalf("报价被拒绝后本体耐久应保持 0.5，实得 %v", fresh.Durability)
	}
	fields := snapshotFields(t, fresh.FieldsJSON)
	part, _ := snapshotPart(t, srv, fields, 4)
	if got := snapshotFloat(part.Fields, part.Class, csDurability); math.Abs(got-.8) > 1e-6 {
		t.Fatalf("报价被拒绝后竿柄耐久应保持 0.8，实得 %v", got)
	}
}

func TestWorkshopOrderRollsBackWalletWhenItemWriteFails(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	rod, partID := seedWornWorkshopRod(t, srv, db, .8, .5)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 1000, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.ExecContext(ctx, fmt.Sprintf(
		`CREATE TRIGGER fail_workshop_item BEFORE UPDATE OF fields_json ON item
		 FOR EACH ROW WHEN OLD.guid='%s'
		 BEGIN SELECT RAISE(ABORT,'注入工坊物品写库失败'); END`, rod.GUID)); err != nil {
		t.Fatalf("注入失败: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.SQL.Exec(`DROP TRIGGER IF EXISTS fail_workshop_item`)
	})
	if _, err := db.SQL.ExecContext(ctx,
		`UPDATE item SET fields_json=fields_json WHERE guid=?`, rod.GUID); err == nil {
		t.Fatal("注入未生效：物品字段写库没有失败")
	}

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(rod.GUID, 4, partID, 0),
		Log:  logging.Discard(),
	}
	if _, ok := hWorkshopOrder(c); ok {
		t.Fatal("物品写库失败时工坊订单不应成功")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Silver != 1000 {
		t.Fatalf("物品写库失败后余额必须回滚为 1000，实得 %d", profile.Silver)
	}
	var txnCount int
	if err := db.SQL.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM txn WHERE uid=? AND kind='workshop_part'`, uid).Scan(&txnCount); err != nil {
		t.Fatal(err)
	}
	if txnCount != 0 {
		t.Fatalf("物品写库失败后流水必须回滚，实得 %d 条", txnCount)
	}
	fresh := mustItem(t, db, rod.GUID)
	if fresh.Durability != .5 {
		t.Fatalf("物品写库失败后本体耐久应保持 0.5，实得 %v", fresh.Durability)
	}
}

// 金币（即时）通道对同型号修理合法：工坊界面本身提供「2 小时(银) / 即时(金)」
// 两档，客户端点「即时」即 useGold=true。旧实现硬拒，客户端收到空包后强转
// onobdmohahp（物品实例）失败，弹「发生内部错误」。
func TestWorkshopReelRepairAcceptsGoldPayment(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	reel, gearID := seedWornWorkshopReel(t, srv, db, 1, .2, .5, .5)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 1000, 1000, 0); err != nil {
		t.Fatal(err)
	}

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPartPayment(reel.GUID, uint32(reelPartCatGear), gearID, 0, true),
		Log:  logging.Discard(),
	}
	body, ok := hWorkshopOrder(c)
	if !ok || len(body) == 0 {
		t.Fatal("同型号修理应接受金币（即时）支付并回物品树")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Silver != 1000 {
		t.Fatalf("金币通道不得扣银币，实得银币 %d", profile.Silver)
	}
	if profile.Gold >= 1000 {
		t.Fatalf("金币通道必须扣金币，实得金币 %d", profile.Gold)
	}
	fresh := mustItem(t, db, reel.GUID)
	if fresh.Durability <= .5 {
		t.Fatalf("金币修理后本体耐久应回升，实得 %v", fresh.Durability)
	}
}

func TestWorkshopReelRepairFullPartDoesNotFreeWholeService(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	reel, gearID := seedWornWorkshopReel(t, srv, db, 1, .2, .5, .5)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(reel.GUID, uint32(reelPartCatGear), gearID, 0),
		Log:  logging.Discard(),
	}
	if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
		t.Fatal("满耐久齿轮不应阻止其他磨损件正常计费修理")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Silver >= 10000 {
		t.Fatalf("整机保养必须按实际磨损收费，余额仍为 %d", profile.Silver)
	}
	fresh := mustItem(t, db, reel.GUID)
	if fresh.Durability != 1 {
		t.Fatalf("整机保养后本体耐久应为 1，实得 %v", fresh.Durability)
	}
	fields := snapshotFields(t, fresh.FieldsJSON)
	washer, _ := snapshotPart(t, srv, fields, reelPartCatWasher)
	if got := snapshotFloat(washer.Fields, washer.Class, csDurability); math.Abs(got-1) > 1e-6 {
		t.Fatalf("整机保养后摩擦片耐久应为 1，实得 %v", got)
	}
}

// 客户端对同一次操作会连发两次 21/3，第二次到达时已无磨损。旧实现把零价当
// 非法交易回空包 → 客户端强转失败报错。新口径：幂等成功，回当前物品树。
func TestWorkshopReelRepairZeroCostIsIdempotent(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	reel, gearID := seedWornWorkshopReel(t, srv, db, 1, 1, 1, 1)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 1000, 0, 0); err != nil {
		t.Fatal(err)
	}

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(reel.GUID, uint32(reelPartCatGear), gearID, 0),
		Log:  logging.Discard(),
	}
	body, ok := hWorkshopOrder(c)
	if !ok || len(body) == 0 {
		t.Fatal("无磨损的重复修理应幂等回物品树（空包会让客户端报内部错误）")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Silver != 1000 {
		t.Fatalf("零金额幂等分支不得扣费，实得银币 %d", profile.Silver)
	}
	var txnCount int
	if err := db.SQL.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM txn WHERE uid=? AND kind='workshop_part'`, uid).Scan(&txnCount); err != nil {
		t.Fatal(err)
	}
	if txnCount != 0 {
		t.Fatalf("零金额修理不得写流水，实得 %d 条", txnCount)
	}
}

func TestWorkshopReelRepairChargesAllRestoredWear(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	reel, gearID := seedWornWorkshopReel(t, srv, db, .8, .2, .5, .5)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(reel.GUID, uint32(reelPartCatGear), gearID, 0),
		Log:  logging.Discard(),
	}
	if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
		t.Fatal("整机保养订单失败")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	// 齿轮 0.2 + 摩擦片 0.8 + 轴承 0.5 = 1.5 个满磨损单位。
	wantCost := int64(math.Round(1.5 * 500))
	if got := int64(10000) - profile.Silver; got != wantCost {
		t.Fatalf("整机保养费用=%d，期望覆盖全部磨损=%d", got, wantCost)
	}
}

func TestWorkshopDiagReelRepairQuoteMatchesWholeService(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	defer db.Close()
	reel, gearID := seedWornWorkshopReel(t, srv, db, .8, .2, .5, .5)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}

	diagBody, ok := hWorkshopDiag(&Ctx{
		Sess: newProbeSess(srv),
		Tree: proto.Args524(proto.Guid(reel.GUID)),
		Log:  logging.Discard(),
	})
	if !ok || len(diagBody) == 0 {
		t.Fatal("21/2 未返回诊断")
	}
	diag, _, err := proto.DecodeValue(diagBody, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	comps := diag.Field(1).Val
	if comps == nil || comps.ArgCount() == 0 {
		t.Fatal("21/2 缺少内件报价")
	}
	price := comps.ArgItem(0).Field(3).Val
	if price == nil {
		t.Fatal("21/2 缺少价格对象")
	}
	quoteField := price.Field(0)
	quote := int64(quoteField.U64())
	// 齿轮 0.2 + 摩擦片 0.8 + 轴承 0.5 = 1.5 个满磨损单位。
	want := int64(math.Round(1.5 * 500))
	if quote != want {
		t.Fatalf("21/2 整机保养报价=%d，期望 %d", quote, want)
	}

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(reel.GUID, uint32(reelPartCatGear), gearID, uint64(quote)),
		Log:  logging.Discard(),
	}
	if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
		t.Fatal("客户端按 21/2 整机报价提交同型号修理应成功")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if got := int64(10000) - profile.Silver; got != want {
		t.Fatalf("整机保养扣费=%d，期望 %d", got, want)
	}
}

func TestWorkshopReelRepairAcceptsAggregatedClientQuote(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	defer db.Close()
	reel, gearID := seedWornWorkshopReel(t, srv, db, .8, .2, .5, .5)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}

	quote := int64(math.Round(1.5 * 500))
	orders := proto.Args(proto.S("503"),
		wiregen.MustMakeObj("gcllbdkgkna", map[int]*proto.Value{
			0: proto.U32(uint32(reelPartCatGear)),
			1: proto.StrVal(gearID),
		}),
		wiregen.MustMakeObj("gcllbdkgkna", map[int]*proto.Value{
			0: proto.U32(uint32(reelPartCatGear)),
			1: proto.StrVal(gearID),
		}),
	)
	req := proto.Args524(proto.StrVal("workshop_01"), wiregen.MustMakeObj("nfmkdcbnkhp", map[int]*proto.Value{
		0: proto.Guid(reel.GUID),
		1: orders,
		2: proto.U32(0),
		3: proto.U64(uint64(quote * 2)),
	}))
	c := &Ctx{Sess: newProbeSess(srv), Main: 21, Sub: 3, Tree: req, Log: logging.Discard()}
	if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
		t.Fatal("客户端多选累加后的整机报价应被接受")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if got := int64(10000) - profile.Silver; got != quote {
		t.Fatalf("整机保养应只扣一次 %d，实扣 %d", quote, got)
	}
}

func TestWorkshopReelRepairRejectsLowQuote(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	defer db.Close()
	reel, gearID := seedWornWorkshopReel(t, srv, db, .8, .2, .5, .5)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(reel.GUID, uint32(reelPartCatGear), gearID, 1),
		Log:  logging.Discard(),
	}
	if _, ok := hWorkshopOrder(c); ok {
		t.Fatal("低于整机保养价的客户端报价应被拒绝")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Silver != 10000 {
		t.Fatalf("低报价被拒绝后余额应保持 10000，实得 %d", profile.Silver)
	}
}
