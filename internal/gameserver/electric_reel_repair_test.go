package gameserver

import (
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func TestWorkshopRepairsReelBodyWhenAllPartsLookFull(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 1000, 0, 0); err != nil {
		t.Fatal(err)
	}
	reel, err := srv.CreateItem(ctx, uid, "elcon_5999", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetItemDurability(ctx, reel.GUID, 0); err != nil {
		t.Fatal(err)
	}
	row := mustItem(t, db, reel.GUID)
	parts := srv.repairPartsOf(ctx, row)
	foundBody := false
	for _, part := range parts {
		if part.Cat == uint32(gearCatReel) && part.Cond == 0 {
			foundBody = true
			break
		}
	}
	if !foundBody {
		t.Fatal("本体耐久为 0 时工坊必须提供本体修理行")
	}

	diagBody, ok := hWorkshopDiag(&Ctx{
		Sess: newProbeSess(srv),
		Tree: proto.Args524(proto.Guid(reel.GUID)),
		Log:  logging.Discard(),
	})
	if !ok || len(diagBody) == 0 {
		t.Fatal("21/2 未返回电轮本体诊断")
	}
	diag, _, err := proto.DecodeValue(diagBody, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	comps := diag.Field(1).Val
	if comps == nil {
		t.Fatal("21/2 缺少组件列表")
	}
	bodyQuote := int64(-1)
	for _, comp := range comps.Items {
		if comp == nil || comp.Kind != proto.KindObj || comp.FieldCount() < 4 {
			continue
		}
		guidField := comp.Field(0)
		if guidField.GuidHex() != reel.GUID {
			continue
		}
		price := comp.Field(3).Val
		if price != nil && price.Kind == proto.KindObj && price.FieldCount() > 0 {
			quoteField := price.Field(0)
			bodyQuote = int64(quoteField.U64())
		}
		break
	}
	// 官服口径：21/2 的 hlopebmchhp.f3 是**旧件残值**，不是修理费。
	// 本体满磨损时这一格就是"本体标价 × 残值率"，客户端拿它去减新件价
	// 得到下单报价。
	bodyPrice, _ := srv.partPrice(t.Context(), reel.ItemID, gearCatReel)
	wantResidual, _ := srv.workshopPartResidual(gearCatReel, bodyPrice, 0)
	if bodyQuote != wantResidual {
		t.Fatalf("21/2 本体报价应为旧件残值 %d（本体标价 %d），实得 %d",
			wantResidual, bodyPrice, bodyQuote)
	}

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(reel.GUID, uint32(gearCatReel), reel.ItemID, 0),
		Log:  logging.Discard(),
	}
	if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
		t.Fatal("本体修理订单应成功")
	}
	// 银币修理要等：到期结算后本体才回到满耐久。
	expireWorkshopJobs(t, db, uid)
	if settled := settleWorkshopForTest(t, srv, uid); len(settled) != 1 {
		t.Fatalf("到期应结算 1 件，实际 %v", settled)
	}
	fresh := mustItem(t, db, reel.GUID)
	if fresh.Durability != 1 {
		t.Fatalf("本体修理后耐久应为 1，实得 %v", fresh.Durability)
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Silver != 500 {
		t.Fatalf("满磨损本体修理应扣 500 银，余额=%d", profile.Silver)
	}
}
