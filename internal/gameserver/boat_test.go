package gameserver

import (
	"strings"
	"testing"
	"time"

	"rf4server/internal/proto"
)

// TestCheckTicket 验证 3/7 player/check_ticket 的响应形状：
//
// 客户端会把响应强转成 ckdficiaaji（wire 类 bplepbfgpmk / type_id 162）。
// 之前该命令落到兜底链、回的是空 args，客户端抛 InvalidCastException，
// BoatWatchDog 把玩家从船上传回岸边——即"买了船票也上不了船"。
func TestCheckTicket(t *testing.T) {
	srv, sess, ctx := newMiscProbe(t)

	it, err := srv.CreateItem(ctx, 1000001, "ticket_boat_komar_24", "backpack", 1)
	if err != nil || it == nil {
		t.Fatalf("创建船票失败: %v", err)
	}
	if it.TypeID != boatTicketTypeID {
		t.Fatalf("船票 type_id 应为 %d，实得 %d（wire %s）", boatTicketTypeID, it.TypeID, it.WireClass)
	}

	// 命中：返回船票对象本身。
	req := proto.Args524(proto.U32(1), proto.GuidArr([]string{it.GUID}))
	body, ok := hCheckTicket(miscCtx(sess, 3, 7, req))
	if !ok || len(body) == 0 {
		t.Fatal("3/7 未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatalf("3/7 响应不可解码: %v", err)
	}
	if dec.Kind != proto.KindObj || dec.TypeID != boatTicketTypeID {
		t.Fatalf("3/7 应回 type_id=%d 的 obj，实得 kind=%s type=%d class=%s",
			boatTicketTypeID, dec.Kind, dec.TypeID, dec.Class)
	}
	anchorIdx := fieldIdxByCS(dec.Class, "oieepfcgjgn")
	activeIdx := fieldIdxByCS(dec.Class, "knpoijanngk")
	if anchorIdx < 0 || activeIdx < 0 {
		t.Fatal("3/7 响应缺少船票锚点或激活字段")
	}
	if got := u64Of(dec.Fields[activeIdx].Num); got != 1 {
		t.Fatalf("3/7 激活字段=%d，期望 1", got)
	}
	wantAnchor := uint64(worldTotalGameMin(time.Now()))
	gotAnchor := u64Of(dec.Fields[anchorIdx].Num)
	if diff := int64(gotAnchor) - int64(wantAnchor); diff < -2 || diff > 2 {
		t.Fatalf("3/7 锚点=%d，期望世界时钟坐标约 %d", gotAnchor, wantAnchor)
	}

	// 3/7 重新上船必须覆盖更早的 3/10 下船事件。
	st := srv.stateFor(sess.Userid)
	st.mu.Lock()
	st.markBoatClientEventLocked(time.Now().Unix(), false)
	st.mu.Unlock()
	if body, ok := hCheckTicket(miscCtx(sess, 3, 7, req)); !ok || len(body) == 0 {
		t.Fatal("3/7 重新上船未处理")
	}
	st.mu.Lock()
	onBoat, boatKnown := st.onBoatLocked(time.Now().Unix())
	st.mu.Unlock()
	if !boatKnown || !onBoat {
		t.Fatalf("3/7 重新上船后应判在船上，得到 known=%v on=%v", boatKnown, onBoat)
	}

	// 未命中：必须回 null，让客户端走"无票"分支而不是异常分支。
	miss := proto.Args524(proto.U32(1), proto.GuidArr([]string{strings.Repeat("f", 32)}))
	body2, ok2 := hCheckTicket(miscCtx(sess, 3, 7, miss))
	if !ok2 {
		t.Fatal("3/7 未命中时应返回 handled")
	}
	if len(body2) != 1 || body2[0] != 0x00 {
		t.Fatalf("未命中应回 null(0x00)，实得 %x", body2)
	}

	// 客户端按 E 时会发 [站点, null guid]（marker 0x17）询问该码头可用的票。
	// 必须按站点回一张真票，否则选票框是空的。
	nullGUID := proto.Args524(proto.U32(1), proto.Guid(strings.Repeat("0", 32), 0x17))
	body3, ok3 := hCheckTicket(miscCtx(sess, 3, 7, nullGUID))
	if !ok3 || len(body3) == 0 {
		t.Fatal("3/7 null guid 未处理")
	}
	dec3, _, err3 := proto.DecodeValue(body3, 0, srv.Table())
	if err3 != nil {
		t.Fatalf("3/7 null guid 响应不可解码: %v", err3)
	}
	if dec3.Kind != proto.KindObj || dec3.TypeID != boatTicketTypeID {
		t.Fatalf("null guid 应按站点回 type_id=%d 的票，实得 kind=%s type=%d",
			boatTicketTypeID, dec3.Kind, dec3.TypeID)
	}
}

func TestOnBoatStateNeedsBoardingEvidence(t *testing.T) {
	st := &FishState{}
	if on, known := st.onBoatLocked(1000); on || known {
		t.Fatal("没见过上船流程时应是未知，不能封鱼")
	}
	st.markBoardLocked(2000)
	if on, known := st.onBoatLocked(1500); !on || !known {
		t.Fatal("船票有效期内应判定为在船上")
	}
	if on, known := st.onBoatLocked(2500); on || !known {
		t.Fatal("船票到期后应判定为已下船")
	}
}

// TestCheckTicketRejectsCrossLevelTicket 验证船票只能在其购买地图的码头使用。
//
// 票定义里 algmimlfbdj（站点枚举）在地图之间会重复：站点 1 在 11 张图上都有，
// 站点 2 在 7 张图上都有。只比站点会让"在阿赫图巴买的票到通古斯卡照样上船"，
// 所以必须同时校验 dhaenhabecc（购买地图）。
func TestCheckTicketRejectsCrossLevelTicket(t *testing.T) {
	srv, sess, ctx := newMiscProbe(t)

	// 阿赫图巴快票：站点 2，地图 level_009_ahtuba。
	it, err := srv.CreateItem(ctx, 1000001, "ticket_boat_ahtuba_fast_24", "backpack", 1)
	if err != nil || it == nil {
		t.Fatalf("创建船票失败: %v", err)
	}

	// 玩家在出生图（克马羚诺也湖）上，跨图票必须被拒。
	srv.setLevel(sess.Userid, spawnLevelKey)
	req := proto.Args524(proto.U32(2), proto.GuidArr([]string{it.GUID}))
	body, ok := hCheckTicket(miscCtx(sess, 3, 7, req))
	if !ok {
		t.Fatal("3/7 未处理")
	}
	if len(body) != 1 || body[0] != 0x00 {
		t.Fatalf("跨地图票应回 null(0x00) 让客户端弹选票框，实得 %x", body)
	}

	// 被拒的票不能留下租用状态，否则切图后会被当成"已在租用"。
	row, err := srv.DB.GetItemByGUID(ctx, it.GUID)
	if err != nil || row == nil {
		t.Fatalf("读取船票失败: %v", err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	activeIdx := fieldIdxByCS(row.WireClass, "knpoijanngk")
	if activeIdx < 0 || activeIdx >= len(fields) {
		t.Fatal("船票缺少激活字段")
	}
	if got := u64Of(fields[activeIdx].Num); got != 0 {
		t.Fatalf("跨地图票不应被激活，激活字段=%d", got)
	}

	// 走到该票所属地图后，同一张票必须放行。
	srv.setLevel(sess.Userid, "level_009_ahtuba")
	body2, ok2 := hCheckTicket(miscCtx(sess, 3, 7, req))
	if !ok2 || len(body2) == 0 {
		t.Fatal("3/7 同图票未处理")
	}
	dec, _, err := proto.DecodeValue(body2, 0, srv.Table())
	if err != nil {
		t.Fatalf("3/7 同图票响应不可解码: %v", err)
	}
	if dec.Kind != proto.KindObj || dec.TypeID != boatTicketTypeID {
		t.Fatalf("同图票应回 type_id=%d 的票，实得 kind=%s type=%d",
			boatTicketTypeID, dec.Kind, dec.TypeID)
	}
}

func TestActiveRentalTicketRepairsLegacyWorldAnchor(t *testing.T) {
	srv, sess, ctx := newMiscProbe(t)
	it, err := srv.CreateItem(ctx, 1000001, "ticket_boat_komar_24", "backpack", 1)
	if err != nil || it == nil {
		t.Fatalf("创建船票失败: %v", err)
	}

	activate := proto.Args524(proto.U32(1), proto.GuidArr([]string{it.GUID}))
	if body, ok := hCheckTicket(miscCtx(sess, 3, 7, activate)); !ok || len(body) == 0 {
		t.Fatal("3/7 激活船票失败")
	}

	row, err := srv.DB.GetItemByGUID(ctx, it.GUID)
	if err != nil || row == nil {
		t.Fatalf("读取船票失败: %v", err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	anchorIdx := fieldIdxByCS(row.WireClass, "oieepfcgjgn")
	if anchorIdx < 0 {
		t.Fatal("船票缺少世界时钟锚点")
	}
	fields[anchorIdx].Num = ptrU64(1)
	fj, err := FieldsToJSON(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.DB.UpdateItemFields(ctx, row.GUID, fj, row.Qty, row.Durability,
		row.Workmanship, row.RigGUID); err != nil {
		t.Fatal(err)
	}

	query := proto.Args524(proto.U32(1), proto.Guid(strings.Repeat("0", 32), 0x17))
	body, ok := hCheckTicket(miscCtx(sess, 3, 7, query))
	if !ok || len(body) == 0 {
		t.Fatal("3/7 按站点复用有效票失败")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	gotAnchor := u64Of(dec.Fields[anchorIdx].Num)
	wantAnchor := uint64(worldTotalGameMin(time.Now()))
	if diff := int64(gotAnchor) - int64(wantAnchor); diff < -5 || diff > 5 {
		t.Fatalf("复用票锚点=%d，期望世界时钟坐标约 %d", gotAnchor, wantAnchor)
	}
}
