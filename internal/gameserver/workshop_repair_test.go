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

func TestWorkshopReelRepairRejectsGoldPayment(t *testing.T) {
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
	if _, ok := hWorkshopOrder(c); ok {
		t.Fatal("同型号修理不应接受金币支付")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Silver != 1000 || profile.Gold != 1000 {
		t.Fatalf("金币修理被拒绝后余额应保持 1000/1000，实得 %d/%d",
			profile.Silver, profile.Gold)
	}
	fresh := mustItem(t, db, reel.GUID)
	if fresh.Durability != .5 {
		t.Fatalf("金币修理被拒绝后本体耐久应保持 0.5，实得 %v", fresh.Durability)
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

	// 客户端连发的第二次请求到达时零件已被第一次修满，零价纯修理必须按
	// 幂等成功处理（回物品树、不扣费、不写流水），否则客户端弹错。
	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(reel.GUID, uint32(reelPartCatGear), gearID, 0),
		Log:  logging.Discard(),
	}
	if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
		t.Fatal("零价纯修理应幂等成功返回物品树")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Silver != 1000 {
		t.Fatalf("零价修理不得扣费，余额应保持 1000，实得 %d", profile.Silver)
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

// TestWorkshopSameModelRepairTwiceIsIdempotent 客户端对同一修理操作会连发
// 两次 21/3，第二次到达时零件已被第一次修满：重复请求必须幂等成功且不扣费。
func TestWorkshopSameModelRepairTwiceIsIdempotent(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	rod, partID := seedWornWorkshopRod(t, srv, db, .8, .5)
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
		Tree: snapshotWorkshopRequestPart(rod.GUID, 4, partID, 0),
		Log:  logging.Discard(),
	}
	if _, ok := hWorkshopOrder(c); !ok {
		t.Fatal("第一次修理应成功")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	afterFirst := profile.Silver

	// 第二次：同一请求原样重发。
	if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
		t.Fatal("重复修理请求应幂等成功，而不是报零金额错误")
	}
	profile, err = db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Silver != afterFirst {
		t.Fatalf("第二次修理不得扣费：第一次后 %d，第二次后 %d", afterFirst, profile.Silver)
	}
	var txnCount int
	if err := db.SQL.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM txn WHERE uid=? AND kind='workshop_part'`, uid).Scan(&txnCount); err != nil {
		t.Fatal(err)
	}
	if txnCount != 1 {
		t.Fatalf("重复修理只应有第一次的 1 条流水，实得 %d 条", txnCount)
	}
	fresh := mustItem(t, db, rod.GUID)
	if fresh.Durability != 1 {
		t.Fatalf("重复修理后本体耐久应保持 1，实得 %v", fresh.Durability)
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

// TestWorkshopZeroCostRepairRefillsBodyDurability 钉住"返回成功就必须修满"：
// 零件磨损四舍五入为 0 但本体还差千分之几时，零价修理必须把本体刷满落库，
// 而不是幂等返回一棵耐久没变的树。
func TestWorkshopZeroCostRepairRefillsBodyDurability(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	// 竿柄 0.9995 → Round(0.0005*500)=Round(0.25)=0，整机价必然为零。
	rod, partID := seedWornWorkshopRod(t, srv, db, 1, 0.9995)
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
		Tree: snapshotWorkshopRequestPart(rod.GUID, 4, partID, 0),
		Log:  logging.Discard(),
	}
	if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
		t.Fatal("零价修理应幂等成功")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Silver != 1000 {
		t.Fatalf("零价修理不得扣费，余额应保持 1000，实得 %d", profile.Silver)
	}
	fresh := mustItem(t, db, rod.GUID)
	if fresh.Durability != 1 {
		t.Fatalf("零价修理后本体耐久必须刷满 1，实得 %v", fresh.Durability)
	}
}

// TestWorkshopReelOilWearRestoredBySameModelRepair 钉住"原油可修"：轮油磨了
// 之后同型号修理必须把油耐久恢复到 1（整机保养口径含油），并且扣费覆盖油的磨损。
func TestWorkshopReelOilWearRestoredBySameModelRepair(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	reel, _ := seedWornWorkshopReel(t, srv, db, 1, 1, 1, 1)
	ctx := t.Context()
	const uid = int64(1000001)

	// 把油磨到 0.4，其余件保持满耐久。
	fields := snapshotFields(t, reel.FieldsJSON)
	oil, oilDef := snapshotPart(t, srv, fields, reelPartCatOil)
	if oil == nil || oilDef == nil {
		t.Fatal("轮油子件缺失")
	}
	setWireFloat(oil.Fields, oil.Class, fieldIdxByCS(oil.Class, csDurability), 0.4)
	raw := snapshotJSON(t, fields)
	reel.FieldsJSON = raw
	if err := db.UpdateItemFields(ctx, reel.GUID, raw, reel.Qty, 1, reel.Workmanship, reel.RigGUID); err != nil {
		t.Fatalf("预置油磨损失败: %v", err)
	}
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(reel.GUID, uint32(reelPartCatOil), oilDef.ItemID, 0),
		Log:  logging.Discard(),
	}
	if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
		t.Fatal("同型号修油失败")
	}
	fresh := mustItem(t, db, reel.GUID)
	freshFields := snapshotFields(t, fresh.FieldsJSON)
	oilAfter, _ := snapshotPart(t, srv, freshFields, reelPartCatOil)
	if oilAfter == nil {
		t.Fatal("修理后轮油子件缺失")
	}
	if got := snapshotFloat(oilAfter.Fields, oilAfter.Class, csDurability); math.Abs(got-1) > 1e-6 {
		t.Fatalf("修油后油耐久应回 1（原油必须可修），实得 %v", got)
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if got := int64(10000) - profile.Silver; got != int64(math.Round(0.6*500)) {
		t.Fatalf("修油扣费应覆盖油磨损 300，实扣 %d", got)
	}
}

// TestWorkshopSwapHandleSurvivesStartupRepairAndRender 钉住换装不被打回：
// 工坊把竿柄换成别的型号后，启动修复（RepairItemPhysics）和下发渲染
// （ItemToTree→rebuildRod）都必须保留玩家装配，不能按定义重建回默认件。
func TestWorkshopSwapHandleSurvivesStartupRepairAndRender(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	rod, err := srv.CreateItem(ctx, uid, "boatr_10222_70_100", "backpack", 1)
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 100000, 0, 0); err != nil {
		t.Fatal(err)
	}

	handleModel := func() string {
		row := mustItem(t, db, rod.GUID)
		fields := snapshotFields(t, row.FieldsJSON)
		h, def := snapshotPart(t, srv, fields, 4)
		if h == nil || def == nil {
			t.Fatal("手柄子件缺失")
		}
		return def.ItemID
	}

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(rod.GUID, 4, "handle_BLD_masterBolo", 0),
		Log:  logging.Discard(),
	}
	if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
		t.Fatal("换手柄请求失败")
	}
	if got := handleModel(); got != "handle_BLD_masterBolo" {
		t.Fatalf("换手柄后应为 BLD，实得 %s", got)
	}

	// 启动修复不得打回默认件。
	if err := srv.RepairItemPhysics(ctx); err != nil {
		t.Fatalf("启动修复失败: %v", err)
	}
	if got := handleModel(); got != "handle_BLD_masterBolo" {
		t.Fatalf("启动修复把手柄打回 %s —— 换装被回滚", got)
	}

	// 下发渲染（rebuildRod 路径）同样不得打回；树里 f5 零件行应跟随新柄。
	tree, terr := srv.ItemToTree(ctx, mustItem(t, db, rod.GUID))
	if terr != nil {
		t.Fatal(terr)
	}
	_ = tree
	if got := handleModel(); got != "handle_BLD_masterBolo" {
		t.Fatalf("ItemToTree 后手柄变为 %s —— 渲染路径回滚了换装", got)
	}
}

// TestWorkshopSwapNewModelGetsFreshGuid 钉住换装 guid 绑定型号：客户端按子件
// guid 缓存零件显示，同一槽位换不同型号必须产生不同 guid 才会让客户端重渲染。
func TestWorkshopSwapNewModelGetsFreshGuid(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	rod, err := srv.CreateItem(ctx, uid, "boatr_10222_70_100", "backpack", 1)
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 1000000, 0, 0); err != nil {
		t.Fatal(err)
	}
	handleGUID := func() string {
		row := mustItem(t, db, rod.GUID)
		fields := snapshotFields(t, row.FieldsJSON)
		h, _ := snapshotPart(t, srv, fields, 4)
		if h == nil {
			t.Fatal("手柄子件缺失")
		}
		gidx := fieldIdxByCS(h.Class, csGUID)
		if gidx < 0 {
			t.Fatal("手柄子件缺 guid 槽")
		}
		return h.Fields[gidx].Hex
	}
	swap := func(sysID string) {
		c := &Ctx{
			Sess: newProbeSess(srv), Main: 21, Sub: 3,
			Tree: snapshotWorkshopRequestPart(rod.GUID, 4, sysID, 0),
			Log:  logging.Discard(),
		}
		if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
			t.Fatalf("换装 %s 失败", sysID)
		}
	}

	factory := handleGUID()
	swap("handle_BLD_masterBolo")
	bolo := handleGUID()
	swap("handle_BLD_masterTele")
	tele := handleGUID()

	if bolo == factory {
		t.Fatal("第一次换装应产生新 guid")
	}
	if tele == bolo {
		t.Fatal("换不同型号必须产生不同 guid，否则客户端不会重渲染")
	}
	swap("handle_BLD_masterBolo")
	if again := handleGUID(); again != bolo {
		t.Fatalf("同型号重复构造 guid 应稳定：%s != %s", again, bolo)
	}
}

// TestWorkshopRodRepairRestoresAllFactoryParts 钉住整竿保养：客户端点一次
// 修理只上报一个零件条目，服务端必须恢复全部出厂件耐久并按磨损总量计价。
func TestWorkshopRodRepairRestoresAllFactoryParts(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	rod, err := srv.CreateItem(ctx, uid, "boatr_10222_70_100", "backpack", 1)
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	// 三件出厂件分别磨到 0.8 / 0.9 / 0.7，本体满耐久。
	fields := snapshotFields(t, rod.FieldsJSON)
	lives := []float64{0.8, 0.9, 0.7}
	for i, life := range lives {
		child, def := snapshotPartAt(t, srv, fields, i)
		if child == nil || def == nil {
			t.Fatalf("出厂件 [%d] 缺失", i)
		}
		setWireFloat(child.Fields, child.Class, fieldIdxByCS(child.Class, csDurability), life)
	}
	raw := snapshotJSON(t, fields)
	if err := db.UpdateItemFields(ctx, rod.GUID, raw, rod.Qty, 1, rod.Workmanship, rod.RigGUID); err != nil {
		t.Fatal(err)
	}
	rod.FieldsJSON = raw
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 100000, 0, 0); err != nil {
		t.Fatal(err)
	}

	blank, _ := snapshotPart(t, srv, fields, 36)
	if blank == nil {
		t.Fatal("竿坯定义缺失")
	}
	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(rod.GUID, 36, blankDefItemID(t, srv, rod), 0),
		Log:  logging.Discard(),
	}
	if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
		t.Fatal("整竿保养请求失败")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	// 三件磨损 0.2+0.1+0.3=0.6，本体满耐久 → 收 300。
	if got := int64(100000) - profile.Silver; got != 300 {
		t.Fatalf("整竿保养应按磨损总量收 300，实扣 %d", got)
	}
	fresh := mustItem(t, db, rod.GUID)
	freshFields := snapshotFields(t, fresh.FieldsJSON)
	for i, want := range []float64{1, 1, 1} {
		child, _ := snapshotPartAt(t, srv, freshFields, i)
		if child == nil {
			t.Fatalf("出厂件 [%d] 缺失", i)
		}
		if got := snapshotFloat(child.Fields, child.Class, csDurability); math.Abs(got-want) > 1e-6 {
			t.Fatalf("整竿保养后出厂件 [%d] 耐久应回 %v，实得 %v", i, want, got)
		}
	}
}

// snapshotPartAt 取竿 fields 第 idx 个槽的子件（f0/f1/f2 出厂件按序号定位）。
func snapshotPartAt(t *testing.T, srv *Server, fields []proto.Field, idx int) (*proto.Value, *store.ItemDef) {
	t.Helper()
	if idx >= len(fields) {
		return nil, nil
	}
	v := fields[idx].Val
	if v == nil || v.Kind != proto.KindObj {
		return nil, nil
	}
	nidx := fieldIdxByCS(v.Class, csNumericID)
	if nidx < 0 || nidx >= v.FieldCount() || v.Fields[nidx].Num == nil {
		return nil, nil
	}
	def, err := srv.DB.GetItemDef(t.Context(), int64(u64Of(v.Fields[nidx].Num)))
	if err != nil || def == nil {
		return nil, nil
	}
	return v, def
}

func blankDefItemID(t *testing.T, srv *Server, rod *store.Item) string {
	t.Helper()
	fields := snapshotFields(t, rod.FieldsJSON)
	_, def := snapshotPartAt(t, srv, fields, 0)
	if def == nil {
		t.Fatal("竿坯定义解析失败")
	}
	return def.ItemID
}

// TestWorkshopSameModelRepairRefreshesChildGuid 钉住修理换 guid：客户端按子件
// guid 缓存零件显示，同型号修理原地改耐久 guid 不变时 4/22 新树不触发零件行
// 重渲染。实际恢复了耐久的子件必须换新 guid。
func TestWorkshopSameModelRepairRefreshesChildGuid(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	rod, partID := seedWornWorkshopRod(t, srv, db, .4, 1)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 100000, 0, 0); err != nil {
		t.Fatal(err)
	}
	childGUID := func() string {
		row := mustItem(t, db, rod.GUID)
		fields := snapshotFields(t, row.FieldsJSON)
		h, _ := snapshotPart(t, srv, fields, 4)
		if h == nil {
			t.Fatal("手柄子件缺失")
		}
		gidx := fieldIdxByCS(h.Class, csGUID)
		return h.Fields[gidx].Hex
	}
	before := childGUID()
	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(rod.GUID, 4, partID, 0),
		Log:  logging.Discard(),
	}
	if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
		t.Fatal("同型号修理失败")
	}
	after := childGUID()
	if after == before {
		t.Fatal("实际修理过的子件 guid 必须换新，否则客户端不重渲染耐久显示")
	}
}

// TestWorkshopZeroCostRepairPersistsChildRepair 钉住零价修理也要真的修好：
// 零件磨损四舍五入为 0 时，同型号修理循环已把子件刷满，零价幂等分支必须把
// 这次子件修理落库并换 guid，不能回一棵数据库旧树把修理丢掉。重复请求（子件
// 已满）不得再换 guid，保持幂等。
func TestWorkshopZeroCostRepairPersistsChildRepair(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	// 竿柄 0.9995 → Round(0.0005*500)=0，整机价必然为零，但子件确实被修了。
	rod, partID := seedWornWorkshopRod(t, srv, db, .9995, 1)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 1000, 0, 0); err != nil {
		t.Fatal(err)
	}
	partState := func() (float64, string) {
		row := mustItem(t, db, rod.GUID)
		fields := snapshotFields(t, row.FieldsJSON)
		p, _ := snapshotPart(t, srv, fields, 4)
		if p == nil {
			t.Fatal("竿柄子件缺失")
		}
		gidx := fieldIdxByCS(p.Class, csGUID)
		if gidx < 0 {
			t.Fatal("竿柄子件缺 guid 槽")
		}
		return snapshotFloat(p.Fields, p.Class, csDurability), p.Fields[gidx].Hex
	}

	_, beforeGUID := partState()
	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(rod.GUID, 4, partID, 0),
		Log:  logging.Discard(),
	}
	if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
		t.Fatal("零价修理应幂等成功")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Silver != 1000 {
		t.Fatalf("零价修理不得扣费，余额应保持 1000，实得 %d", profile.Silver)
	}
	dur, afterGUID := partState()
	if dur != 1 {
		t.Fatalf("零价修理后竿柄耐久必须落库刷满 1，实得 %v", dur)
	}
	if afterGUID == beforeGUID {
		t.Fatal("零价修理实际恢复了子件耐久，guid 必须换新落库")
	}

	// 子件已满的重复请求：不再换 guid、不再落库，保持幂等。
	if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
		t.Fatal("重复的零价修理应幂等成功")
	}
	if _, againGUID := partState(); againGUID != afterGUID {
		t.Fatal("重复请求没有恢复任何耐久，guid 必须保持稳定")
	}
	var txnCount int
	if err := db.SQL.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM txn WHERE uid=? AND kind='workshop_part'`, uid).Scan(&txnCount); err != nil {
		t.Fatal(err)
	}
	if txnCount != 0 {
		t.Fatalf("零价修理不得写流水，实得 %d 条", txnCount)
	}
}

// TestWorkshopZeroCostBodyRepairWithLooseOrderRejected 钉住零价幂等只限纯修理：
// 本体修理条目搭带一条解析不到槽位的换件条目时，整机价算出 0，若按幂等成功
// 返回，换件条目就被静默丢弃——必须按定价缺失拒绝。
func TestWorkshopZeroCostBodyRepairWithLooseOrderRejected(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	reel, _ := seedWornWorkshopReel(t, srv, db, 1, 1, 1, 1)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 1000, 0, 0); err != nil {
		t.Fatal(err)
	}
	orders := proto.Args(proto.S("503"),
		wiregen.MustMakeObj("gcllbdkgkna", map[int]*proto.Value{
			0: proto.U32(uint32(gearCatReel)), // 本体修理条目
			1: proto.StrVal(reel.ItemID),
		}),
		wiregen.MustMakeObj("gcllbdkgkna", map[int]*proto.Value{
			0: proto.U32(63), // 轮上不存在的品类：解析不到槽位，不计价
			1: proto.StrVal("part_unknown_63"),
		}),
	)
	req := proto.Args524(proto.StrVal("workshop_01"), wiregen.MustMakeObj("nfmkdcbnkhp", map[int]*proto.Value{
		0: proto.Guid(reel.GUID),
		1: orders,
		2: proto.U32(0),
		3: proto.U64(0),
	}))
	c := &Ctx{Sess: newProbeSess(srv), Main: 21, Sub: 3, Tree: req, Log: logging.Discard()}
	if _, ok := hWorkshopOrder(c); ok {
		t.Fatal("本体修理搭带换件条目的零价请求应按定价缺失拒绝，而不是幂等成功丢弃换件")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Silver != 1000 {
		t.Fatalf("被拒绝的零价请求不得扣费，余额应保持 1000，实得 %d", profile.Silver)
	}
	fresh := mustItem(t, db, reel.GUID)
	if fresh.Durability != 1 {
		t.Fatalf("被拒绝的零价请求不得改动物品，本体耐久应保持 1，实得 %v", fresh.Durability)
	}
}

// TestWorkshopReelWholeServiceOnlyRefreshesWornGuids 钉住整机保养的 guid 口径：
// 只有本次实际恢复了耐久的子件才换 guid，满耐久子件保持原 guid，避免客户端
// 无谓重渲染。
func TestWorkshopReelWholeServiceOnlyRefreshesWornGuids(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	// 只有齿轮磨损（0.2），摩擦片/轴承/油全满。
	reel, gearID := seedWornWorkshopReel(t, srv, db, .8, 1, 1, 1)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}
	partGUID := func(cat int64) string {
		row := mustItem(t, db, reel.GUID)
		fields := snapshotFields(t, row.FieldsJSON)
		p, _ := snapshotPart(t, srv, fields, cat)
		if p == nil {
			t.Fatalf("子件 %d 缺失", cat)
		}
		gidx := fieldIdxByCS(p.Class, csGUID)
		if gidx < 0 {
			t.Fatalf("子件 %d 缺 guid 槽", cat)
		}
		return p.Fields[gidx].Hex
	}
	gearBefore, washerBefore, bearingBefore := partGUID(reelPartCatGear), partGUID(reelPartCatWasher), partGUID(reelPartCatBearing)

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(reel.GUID, uint32(reelPartCatGear), gearID, 0),
		Log:  logging.Discard(),
	}
	if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
		t.Fatal("整机保养订单失败")
	}
	if got := partGUID(reelPartCatGear); got == gearBefore {
		t.Fatal("恢复过耐久的齿轮 guid 必须换新")
	}
	if got := partGUID(reelPartCatWasher); got != washerBefore {
		t.Fatal("满耐久摩擦片不应换 guid（无谓重渲染）")
	}
	if got := partGUID(reelPartCatBearing); got != bearingBefore {
		t.Fatal("满耐久轴承不应换 guid（无谓重渲染）")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if got := int64(10000) - profile.Silver; got != 100 {
		t.Fatalf("整机保养应按齿轮磨损收 100，实扣 %d", got)
	}
}

// TestWorkshopRenderKeepsRepairedDefaultPartGuid 钉住默认件的重建口径：
// rebuildRod 对出厂默认件按定义重建（让静态定义更新能重新派生），但必须
// 保留原子件 guid——修理刚刷新的 guid 若被重建打回确定性派生值，客户端
// 按 guid 缓存就看不到耐久恢复；换过型号的子件仍然整体保留。
func TestWorkshopRenderKeepsRepairedDefaultPartGuid(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	rod, partID := seedWornWorkshopRod(t, srv, db, .4, 1)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 100000, 0, 0); err != nil {
		t.Fatal(err)
	}
	childGUID := func() string {
		row := mustItem(t, db, rod.GUID)
		fields := snapshotFields(t, row.FieldsJSON)
		h, _ := snapshotPart(t, srv, fields, 4)
		if h == nil {
			t.Fatal("手柄子件缺失")
		}
		gidx := fieldIdxByCS(h.Class, csGUID)
		return h.Fields[gidx].Hex
	}
	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(rod.GUID, 4, partID, 0),
		Log:  logging.Discard(),
	}
	if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
		t.Fatal("同型号修理失败")
	}
	repaired := childGUID()

	// 下发渲染走 rebuildRod：默认件重建后 guid 必须保持修理刷新值。
	tree, terr := srv.ItemToTree(ctx, mustItem(t, db, rod.GUID))
	if terr != nil {
		t.Fatal(terr)
	}
	treeGUID := ""
	for i := range tree.Fields {
		v := tree.Fields[i].Val
		if v == nil || v.Kind != proto.KindObj {
			continue
		}
		if srv.childCategory(ctx, v) == 4 {
			if gidx := fieldIdxByCS(v.Class, csGUID); gidx >= 0 && gidx < v.FieldCount() {
				treeGUID = v.Fields[gidx].Hex
			}
			break
		}
	}
	if treeGUID != repaired {
		t.Fatalf("下发渲染把修理刷新的手柄 guid 打回 %s（应为 %s）——同 guid 耐久更新不会重渲染",
			treeGUID, repaired)
	}

	// 启动修复同样不得打回。
	if err := srv.RepairItemPhysics(ctx); err != nil {
		t.Fatalf("启动修复失败: %v", err)
	}
	if got := childGUID(); got != repaired {
		t.Fatalf("启动修复把手柄 guid 打回 %s（应为 %s）", got, repaired)
	}
}
