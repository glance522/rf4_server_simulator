package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// TestCouponChucheloUsesVoucherClass 锁定梦鱼优惠券（category=74）的实例类。
//
// 券从邮件领取后，标本制作商店的材料列表里找不到它、无法制作标本。根因是
// categoryWireClass 缺 74，prepareNewItem 回落成钓线兜底类 ohciikgjghe/140，
// 客户端按钓线 DTO 解码。定义类 fpcfjngdcho/13015 的域字段
// fopbhlmkcpe/ppiooalfgii/eiofdcjlaai 与 coupon_chuchelo.attrs_json 完全吻合，
// 配对实例类 ahgeldclmdj/13018（同族锚点：cat76→fdnngkgnmon/13041）。
func TestCouponChucheloUsesVoucherClass(t *testing.T) {
	t.Parallel()
	entry, ok := categoryWireClass[74]
	if !ok {
		t.Fatal("categoryWireClass 缺少 74（梦鱼优惠券）")
	}
	if entry.Class != "ahgeldclmdj" || entry.TypeID != 13018 {
		t.Fatalf("梦鱼优惠券映射 = %s/%d，期望 ahgeldclmdj/13018", entry.Class, entry.TypeID)
	}
	if got := wiregen.ClassTypeID["ahgeldclmdj"]; got != 13018 {
		t.Fatalf("ahgeldclmdj type_id = %d，期望 13018", got)
	}
	if !reclassifyCategories[74] {
		t.Fatal("reclassifyCategories 缺少 74：存量券不会在启动修复时改写")
	}
	// 合同守卫 itemTypeIDs 从 categoryWireClass 派生：新 tid 必须能被 4/22
	// 物品增量下发，否则背包同步仍会把券拒掉。
	if !isItemTypeID(13018) {
		t.Fatal("itemTypeIDs 缺少 13018：4/22 增量会拒发梦鱼优惠券")
	}
}

// TestCreateCouponUsesVoucherClass 端到端验证新发的梦鱼优惠券落类正确：
// 邮件附件领取走 CreateItem，补映射前会落成钓线兜底类。
func TestCreateCouponUsesVoucherClass(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()

	it, err := srv.CreateItem(ctx, 1000004, "coupon_chuchelo", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if it.WireClass != "ahgeldclmdj" || it.TypeID != 13018 {
		t.Fatalf("新发梦鱼优惠券 = %s/%d，期望 ahgeldclmdj/13018", it.WireClass, it.TypeID)
	}
	fresh, err := db.GetItemByGUID(ctx, it.GUID)
	if err != nil || fresh == nil {
		t.Fatal("读取新发券失败: ", err)
	}
	if fresh.WireClass != "ahgeldclmdj" || fresh.TypeID != 13018 {
		t.Fatalf("入库后 = %s/%d，期望 ahgeldclmdj/13018", fresh.WireClass, fresh.TypeID)
	}
	fields, err := FieldsFromJSON(fresh.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != len(wiregen.ClassOps["ahgeldclmdj"]) {
		t.Fatalf("字段数 = %d，期望 %d", len(fields), len(wiregen.ClassOps["ahgeldclmdj"]))
	}
}

// TestReclassifyCouponFromFallback 验证存量梦鱼优惠券会在启动修复时从钓线类
// 改写成 ahgeldclmdj/13018，guid / numeric_id / 数量 / 容器保持不变。
// 回滚本修复后该用例必红。
func TestReclassifyCouponFromFallback(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()

	def, err := db.GetItemDefByItemID(ctx, "coupon_chuchelo")
	if err != nil || def == nil {
		t.Skip("静态库缺 coupon_chuchelo 定义: ", err)
	}
	if def.Category != 74 {
		t.Fatalf("coupon_chuchelo 品类 = %d，期望 74", def.Category)
	}

	guid := "fedcba9876543210fedcba9876543210"
	obj, err := wiregen.MakeObj(fallbackWireClass, nil)
	if err != nil {
		t.Fatal(err)
	}
	fields := obj.Fields
	fields[fieldIdxByCS(fallbackWireClass, csGUID)].Hex = guid
	fields[fieldIdxByCS(fallbackWireClass, csNumericID)].Num = ptrU64(uint64(def.NumericID))
	fj, err := FieldsToJSON(fields)
	if err != nil {
		t.Fatal(err)
	}
	legacy := &store.Item{
		GUID: guid, UID: 1000004, ItemID: "coupon_chuchelo", NumericID: def.NumericID,
		WireClass: fallbackWireClass, TypeID: fallbackTypeID,
		Container: "backpack", Qty: 1, FieldsJSON: fj, Durability: 1, Workmanship: 1,
	}
	if err := db.InsertItem(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetItemByGUID(ctx, guid)
	if err != nil || row == nil {
		t.Fatal("读取探测物品失败: ", err)
	}

	changed, err := srv.reclassifyStoredItem(ctx, row, def)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("梦鱼优惠券未被改写")
	}
	fresh, err := db.GetItemByGUID(ctx, guid)
	if err != nil || fresh == nil {
		t.Fatal("改写后读取失败: ", err)
	}
	if fresh.WireClass != "ahgeldclmdj" || fresh.TypeID != 13018 {
		t.Fatalf("改写后 = %s/%d，期望 ahgeldclmdj/13018", fresh.WireClass, fresh.TypeID)
	}
	if fresh.Container != "backpack" || fresh.Qty != 1 || fresh.GUID != guid {
		t.Fatalf("容器/数量/guid 被改动: %+v", fresh)
	}
	freshFields, err := FieldsFromJSON(fresh.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(freshFields) != len(wiregen.ClassOps["ahgeldclmdj"]) {
		t.Fatalf("字段数 = %d，期望 %d", len(freshFields), len(wiregen.ClassOps["ahgeldclmdj"]))
	}
	if idx := fieldIdxByCS("ahgeldclmdj", csGUID); freshFields[idx].Hex != guid {
		t.Errorf("guid 未保留: %s", freshFields[idx].Hex)
	}
	if idx := fieldIdxByCS("ahgeldclmdj", "iofbjlaadfg"); freshFields[idx].Num == nil || int64(*freshFields[idx].Num) != def.NumericID {
		t.Error("numeric id 未保留")
	}
	// 券已退出 countableCategories；reclassify 必须显式把权威数量槽 dnmfciolgep
	// 写回 1，否则改写后该槽保持 0，客户端读不出这一张。
	if n, ok := itemQtyValue("ahgeldclmdj", freshFields); !ok || n != 1 {
		t.Errorf("改写后券数量槽应为 1，实得 %d ok=%v", n, ok)
	}
}
