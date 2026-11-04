package gameserver

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

func TestCraftedLeaderUsesSelectedMaterials(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "leader-craft.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	importStaticForSkillsCraft(t, db, "item_def", "recipe")

	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)
	uid := int64(2000040)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15247", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	srv.alignItemRowQty(ctx, line)
	connector, err := srv.CreateItem(ctx, uid, "swlock_40009", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	srv.alignItemRowQty(ctx, connector)

	sess := &Session{ID: 1, Userid: uid, Srv: srv, log: logging.Discard()}
	// 使用客户端临时 GUID，验证服务端会按真实需求类目回退到实际材料。
	tree := proto.Args524(craftObj8("craft_leader_mar_40", []craftSlot{
		{ItemID: "craft_mat_line", GUID: "00112233445566778899aabbccddeeff"},
		{ItemID: "craft_mat_swivels", GUID: "ffeeddccbbaa99887766554433221100"},
	}, 1))
	hctx := &Ctx{
		Sess: sess, Main: 9, Sub: 1, CallID: 1, Tree: tree, DecodeOK: true,
		Log: logging.Discard(),
	}
	body, ok := hCraft(hctx)
	if !ok {
		t.Fatal("9/1 未处理")
	}
	result := decodeTreeValue(t, srv, body)
	if got := u64Of(result.Fields[0].Num); got != 1 {
		t.Fatalf("制作应成功，803.f0=%d", got)
	}
	if f1 := result.Fields[1].Val; f1 == nil || f1.Kind != proto.KindArgs ||
		len(f1.Items) != 1 || f1.Items[0].TypeID != 137 {
		t.Fatalf("803.f1 应带 1 件引线实例，实得 %+v", f1)
	}
	if result.Fields[7].Val == nil || result.Fields[7].Val.Kind != proto.KindNull ||
		result.Fields[8].Val == nil || result.Fields[8].Val.Kind != proto.KindNull {
		t.Fatalf("803.f7/f8 不应发送，实得 %+v / %+v", result.Fields[7].Val, result.Fields[8].Val)
	}
	if f6 := result.Fields[6].Val; f6 == nil || f6.Kind != proto.KindArgs || len(f6.Items) != 3 {
		t.Fatalf("803.f6 应同步产出和两份剩余材料，实得 %+v", f6)
	}
	if len(hctx.afterResponse) != 1 {
		t.Fatalf("制作成功后应注册一次背包刷新，实得 %d", len(hctx.afterResponse))
	}

	rows, err := db.ListItemsByContainers(ctx, uid, []string{"backpack", "rig"})
	if err != nil {
		t.Fatal(err)
	}
	var crafted *store.Item
	for _, row := range rows {
		if row.ItemID == "leader_crafted_mar_40" {
			crafted = row
			break
		}
	}
	if crafted == nil {
		t.Fatal("未找到成品引线")
	}
	fields, err := FieldsFromJSON(crafted.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}

	lineObj := fields[fieldIdxByCS(crafted.WireClass, gearAttrLeaderIn)].Val
	if lineObj == nil || lineObj.Kind != proto.KindObj {
		t.Fatalf("成品 f1 线材对象未物化: %+v", lineObj)
	}
	wireClass := categoryWireClass[gearCatLeaderWire]
	if lineObj.Class != wireClass.Class || lineObj.TypeID != uint32(wireClass.TypeID) {
		t.Fatalf("成品 f1 协议类型=%s/%d，期望 %s/%d",
			lineObj.Class, lineObj.TypeID, wireClass.Class, wireClass.TypeID)
	}
	if got := u64Of(lineObj.Fields[fieldIdxByCS(lineObj.Class, csNumericID)].Num); got != 16064 {
		t.Fatalf("线材模型 numeric_id=%d，期望前导线 16064", got)
	}
	if got := wireFloatForTest(t, lineObj, csStrength); math.Abs(got-3.4) > 0.001 {
		t.Fatalf("线材强度=%f，期望 3.4kg", got)
	}
	if got := u64Of(lineObj.Fields[fieldIdxByCS(lineObj.Class, gearAttrLineLen)].Num); got != 40 {
		t.Fatalf("成品内线材长度=%d，期望 40cm", got)
	}

	connectorObj := fields[fieldIdxByCS(crafted.WireClass, "lbadjgllglo")].Val
	if connectorObj == nil || connectorObj.Kind != proto.KindObj {
		t.Fatalf("成品 f2 连接件对象未物化: %+v", connectorObj)
	}
	if got := u64Of(connectorObj.Fields[fieldIdxByCS(connectorObj.Class, csNumericID)].Num); got != 14984 {
		t.Fatalf("连接件模型 numeric_id=%d，期望 14984", got)
	}
	if got := wireFloatForTest(t, connectorObj, csStrength); math.Abs(got-6.75) > 0.001 {
		t.Fatalf("连接件强度=%f，期望 6.75kg", got)
	}
	if got := wireFloatForTest(t, &proto.Value{
		Kind:   proto.KindObj,
		Class:  crafted.WireClass,
		Fields: fields,
		TypeID: uint32(crafted.TypeID),
	}, csStrength); math.Abs(got-3.4) > 0.001 {
		t.Fatalf("引线总强度=%f，期望取最弱的 3.4kg", got)
	}

	tree, err = srv.ItemToTree(ctx, crafted)
	if err != nil {
		t.Fatal(err)
	}
	reloadedLine := tree.Fields[fieldIdxByCS(tree.Class, gearAttrLeaderIn)].Val
	if reloadedLine == nil ||
		u64Of(reloadedLine.Fields[fieldIdxByCS(reloadedLine.Class, csNumericID)].Num) != 16064 {
		t.Fatalf("重载后的成品没有保留真实线材模型: %+v", reloadedLine)
	}
	if got := wireFloatForTest(t, tree, csStrength); math.Abs(got-3.4) > 0.001 {
		t.Fatalf("重载后引线强度=%f，期望 3.4kg", got)
	}

	lineAfter, _ := db.GetItemByGUID(ctx, line.GUID)
	if lineAfter == nil || lineAfter.Qty != 99 {
		t.Fatalf("线材应只扣 1m（100→99），实得 %+v", lineAfter)
	}
	connectorAfter, _ := db.GetItemByGUID(ctx, connector.GUID)
	if connectorAfter == nil || connectorAfter.Qty != 4 {
		t.Fatalf("连接件应只扣 1 个（5→4），实得 %+v", connectorAfter)
	}
}

func TestNormalizeLeaderInnerWirePreservesMaterialIdentity(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "leader-wire-normalize.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	importStaticForSkillsCraft(t, db, "item_def")

	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)
	lineDef, err := db.GetItemDefByItemID(ctx, "mono_15065")
	if err != nil || lineDef == nil {
		t.Fatalf("读取线材定义失败: %v", err)
	}
	host := &store.Item{GUID: "00112233445566778899aabbccddeeff"}
	line := &store.Item{GUID: "102132435465768798a9bacbdcedfe0f", ItemID: lineDef.ItemID,
		NumericID: lineDef.NumericID, Workmanship: 1, Durability: 1}
	wireObj, err := srv.craftedMaterialObject(host, craftedMaterial{row: line, def: lineDef},
		gearAttrLeaderIn, 10)
	if err != nil {
		t.Fatal(err)
	}
	legacy := remapObjectClassForTest(t, wireObj, categoryWireClass[gearCatLine].Class)
	normalized, changed := normalizeLeaderInnerWire(legacy)
	if !changed {
		t.Fatal("旧版主线对象没有触发转换")
	}
	if normalized.Class != categoryWireClass[gearCatLeaderWire].Class {
		t.Fatalf("转换后类型=%s，期望 %s", normalized.Class, categoryWireClass[gearCatLeaderWire].Class)
	}
	if got := u64Of(normalized.Fields[fieldIdxByCS(normalized.Class, csNumericID)].Num); got != 9201 {
		t.Fatalf("转换后模型 numeric_id=%d，期望 9201", got)
	}
	if got := wireFloatForTest(t, normalized, csStrength); math.Abs(got-3.1) > 0.001 {
		t.Fatalf("转换后强度=%f，期望 3.1kg", got)
	}
	if got := u64Of(normalized.Fields[fieldIdxByCS(normalized.Class, gearAttrLineLen)].Num); got != 10 {
		t.Fatalf("转换后长度=%d，期望 10cm", got)
	}
}

func remapObjectClassForTest(t *testing.T, source *proto.Value, targetClass string) *proto.Value {
	t.Helper()
	obj, err := wiregen.MakeObj(targetClass, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, targetField := range wiregen.ClassMeta[targetClass].Fields {
		sourceIdx := fieldIdxByCS(source.Class, targetField.Name)
		if sourceIdx >= 0 && sourceIdx < len(source.Fields) {
			obj.Fields[targetField.Idx] = source.Fields[sourceIdx]
		}
	}
	return obj
}

func wireFloatForTest(t *testing.T, obj *proto.Value, field string) float64 {
	t.Helper()
	idx := fieldIdxByCS(obj.Class, field)
	if idx < 0 || idx >= len(obj.Fields) || obj.Fields[idx].Num == nil {
		t.Fatalf("%s 缺少字段 %s", obj.Class, field)
	}
	return float64(proto.F32FromBits(uint32(u64Of(obj.Fields[idx].Num))))
}
