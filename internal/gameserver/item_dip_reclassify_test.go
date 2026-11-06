package gameserver

// 提取物（dip，类别 58）与竿架（类别 79）实例类回归（2026-11-05）：
// 两族同属客户端 UI 家族 dfignppokoh，但变体不同——竿架=pphmpibfgea
// （abmokghdbhf/11500），提取物=nledogicnfe（bpmmimcmpof/721，多 f0
// hmfmpbemjkg 父实例 guid）。客户端背包排序比较器把鱼饵类条目强转成
// nledogicnfe（hook 栈实证 'pphmpibfgea'->'nledogicnfe'）；早期没有映射时
// 落成钓线类 ohciikgjghe 会让按 I 打开背包永远转圈（Player.log 456 次
// cast to pphmpibfgea 实证）。

import (
	"context"
	"testing"

	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// legacyFallbackItem 造一条早期误落成钓线类的存量物品。
func legacyFallbackItem(t *testing.T, guid string, def *store.ItemDef, qty int64) *store.Item {
	t.Helper()
	obj, err := wiregen.MakeObj(fallbackWireClass, nil)
	if err != nil {
		t.Fatal(err)
	}
	fields := obj.Fields
	fields[fieldIdxByCS(fallbackWireClass, csGUID)].Hex = guid
	fields[fieldIdxByCS(fallbackWireClass, csNumericID)].Num = ptrU64(uint64(def.NumericID))
	fields[fieldIdxByCS(fallbackWireClass, csQty)].Num = ptrU64(uint64(qty))
	fj, err := FieldsToJSON(fields)
	if err != nil {
		t.Fatal(err)
	}
	return &store.Item{
		GUID: guid, UID: 1000004, ItemID: def.ItemID, NumericID: def.NumericID,
		WireClass: fallbackWireClass, TypeID: fallbackTypeID,
		Container: "backpack", Qty: qty, FieldsJSON: fj, Durability: 1, Workmanship: 1,
	}
}

// TestReclassifyDipFromFallback 存量提取物启动修复：换到 bpmmimcmpof/721，
// 数量写进该族的两个数量槽（f13 dnmfciolgep + f1 ljnnpokapig），状态位保留，
// 父实例 guid（f0 hmfmpbemjkg）保持为空。
func TestReclassifyDipFromFallback(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()

	def, err := db.GetItemDef(ctx, 5136) // dip_hvz_se 大草莓提取物
	if err != nil || def == nil {
		t.Skip("静态库缺 dip_hvz_se 定义: ", err)
	}
	if def.Category != 58 {
		t.Fatalf("dip_hvz_se 品类 = %d，期望 58", def.Category)
	}

	guid := "00112233445566778899aabbccddee01"
	legacy := legacyFallbackItem(t, guid, def, 100)
	if err := db.InsertItem(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetItemByGUID(ctx, guid)
	if err != nil || row == nil {
		t.Fatal("读取存量物品失败: ", err)
	}

	changed, err := srv.reclassifyStoredItem(ctx, row, def)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("提取物未被改写")
	}
	fresh, err := db.GetItemByGUID(ctx, guid)
	if err != nil || fresh == nil {
		t.Fatal("改写后读取失败: ", err)
	}
	if fresh.WireClass != "bpmmimcmpof" || fresh.TypeID != 721 {
		t.Fatalf("改写后 = %s/%d，期望 bpmmimcmpof/721", fresh.WireClass, fresh.TypeID)
	}
	if fresh.Qty != 100 {
		t.Fatalf("行数量 = %d，期望 100", fresh.Qty)
	}
	fields, err := FieldsFromJSON(fresh.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != len(wiregen.ClassOps["bpmmimcmpof"]) {
		t.Fatalf("字段数 = %d，期望 %d", len(fields), len(wiregen.ClassOps["bpmmimcmpof"]))
	}
	if got := u64Of(fields[fieldIdxByCS("bpmmimcmpof", "dnmfciolgep")].Num); got != 100 {
		t.Errorf("f13 dnmfciolgep = %d，期望 100", got)
	}
	if got := u64Of(fields[fieldIdxByCS("bpmmimcmpof", csQty)].Num); got != 100 {
		t.Errorf("f1 ljnnpokapig（UI 计数）= %d，期望 100", got)
	}
	if f := fields[fieldIdxByCS("bpmmimcmpof", "hmfmpbemjkg")]; f.Hex != "" || f.Num != nil {
		t.Errorf("f0 hmfmpbemjkg（父实例 guid）应为空: %+v", f)
	}
	if got := u64Of(fields[fieldIdxByCS("bpmmimcmpof", csNumericID)].Num); got != 5136 {
		t.Errorf("numeric id = %d，期望 5136", got)
	}
	if fields[fieldIdxByCS("bpmmimcmpof", csGUID)].Hex != guid {
		t.Errorf("guid 未保留: %s", fields[fieldIdxByCS("bpmmimcmpof", csGUID)].Hex)
	}
	if bits, ok := defAttrU64(def, csStateEnum); ok {
		if got := u64Of(fields[fieldIdxByCS("bpmmimcmpof", csStateEnum)].Num); got != bits {
			t.Errorf("状态位 = %d，期望定义值 %d", got, bits)
		}
	}
}

// TestReclassifyRodPodFromFallback 存量竿架（79 有映射但迁移集此前没包含它）。
func TestReclassifyRodPodFromFallback(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()

	def, err := db.GetItemDefByItemID(ctx, "rod_pod_03r")
	if err != nil || def == nil {
		t.Skip("静态库缺 rod_pod_03r 定义: ", err)
	}
	if def.Category != 79 {
		t.Fatalf("rod_pod_03r 品类 = %d，期望 79", def.Category)
	}

	guid := "00112233445566778899aabbccddee02"
	legacy := legacyFallbackItem(t, guid, def, 1)
	if err := db.InsertItem(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetItemByGUID(ctx, guid)
	if err != nil || row == nil {
		t.Fatal("读取存量物品失败: ", err)
	}
	changed, err := srv.reclassifyStoredItem(ctx, row, def)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("竿架未被改写")
	}
	fresh, err := db.GetItemByGUID(ctx, guid)
	if err != nil || fresh == nil {
		t.Fatal("改写后读取失败: ", err)
	}
	if fresh.WireClass != "abmokghdbhf" || fresh.TypeID != 11500 {
		t.Fatalf("改写后 = %s/%d，期望 abmokghdbhf/11500", fresh.WireClass, fresh.TypeID)
	}
	// 竿架不可堆叠：数量槽保持零值，不得误写。
	fields, err := FieldsFromJSON(fresh.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if got := u64Of(fields[fieldIdxByCS("abmokghdbhf", "dnmfciolgep")].Num); got != 0 {
		t.Errorf("不可堆叠竿架 f13 应为 0，实得 %d", got)
	}
}

// TestPrepareNewDipUsesFamilyClass 新购提取物直接落在正确的实例类上，
// 数量 = 定义份数（100）× 购买单位。
func TestPrepareNewDipUsesFamilyClass(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()

	if def, err := db.GetItemDefByItemID(ctx, "dip_hvz_se"); err != nil || def == nil {
		t.Skip("静态库缺 dip_hvz_se 定义: ", err)
	}
	it, err := srv.prepareNewItem(ctx, 1000004, "dip_hvz_se", "backpack", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if it.WireClass != "bpmmimcmpof" || it.TypeID != 721 {
		t.Fatalf("新购实例 = %s/%d，期望 bpmmimcmpof/721", it.WireClass, it.TypeID)
	}
	if it.Qty != 100 {
		t.Fatalf("新购数量 = %d，期望定义份数 100", it.Qty)
	}
	fields, err := FieldsFromJSON(it.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if got := u64Of(fields[fieldIdxByCS("bpmmimcmpof", "dnmfciolgep")].Num); got != 100 {
		t.Errorf("f13 dnmfciolgep = %d，期望 100", got)
	}
	if got := u64Of(fields[fieldIdxByCS("bpmmimcmpof", csQty)].Num); got != 100 {
		t.Errorf("f1 ljnnpokapig = %d，期望 100", got)
	}
	if f := fields[fieldIdxByCS("bpmmimcmpof", "hmfmpbemjkg")]; f.Hex != "" || f.Num != nil {
		t.Errorf("f0 hmfmpbemjkg（父实例 guid）应为空: %+v", f)
	}
}

// TestReclassifyDipFromRodPodVariant 58 映射修正后的存量迁移：此前 58 被映射成
// 家族另一变体 abmokghdbhf/11500（竿架的 DTO），客户端排序比较器强转
// nledogicnfe 失败（InvalidCastException 'pphmpibfgea'->'nledogicnfe'，
// 鱼饵筛选/两份小药必现）。启动迁移必须把这类行改写到 bpmmimcmpof/721。
func TestReclassifyDipFromRodPodVariant(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()

	def, err := db.GetItemDefByItemID(ctx, "dip_hvz_tf")
	if err != nil || def == nil {
		t.Skip("静态库缺 dip_hvz_tf 定义: ", err)
	}
	if def.Category != 58 {
		t.Fatalf("dip_hvz_tf 品类 = %d，期望 58", def.Category)
	}

	guid := "00112233445566778899aabbccddee03"
	obj, err := wiregen.MakeObj("abmokghdbhf", nil)
	if err != nil {
		t.Fatal(err)
	}
	fields := obj.Fields
	fields[fieldIdxByCS("abmokghdbhf", csGUID)].Hex = guid
	fields[fieldIdxByCS("abmokghdbhf", csNumericID)].Num = ptrU64(uint64(def.NumericID))
	fields[fieldIdxByCS("abmokghdbhf", "dnmfciolgep")].Num = ptrU64(100)
	fields[fieldIdxByCS("abmokghdbhf", "miecmmbpolk")].Num = ptrU64(100)
	fj, err := FieldsToJSON(fields)
	if err != nil {
		t.Fatal(err)
	}
	legacy := &store.Item{
		GUID: guid, UID: 1000004, ItemID: def.ItemID, NumericID: def.NumericID,
		WireClass: "abmokghdbhf", TypeID: 11500,
		Container: "backpack", Qty: 100, FieldsJSON: fj, Durability: 1, Workmanship: 1,
	}
	if err := db.InsertItem(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetItemByGUID(ctx, guid)
	if err != nil || row == nil {
		t.Fatal("读取存量物品失败: ", err)
	}

	changed, err := srv.reclassifyStoredItem(ctx, row, def)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("家族变体行未被迁移")
	}
	fresh, err := db.GetItemByGUID(ctx, guid)
	if err != nil || fresh == nil {
		t.Fatal("迁移后读取失败: ", err)
	}
	if fresh.WireClass != "bpmmimcmpof" || fresh.TypeID != 721 {
		t.Fatalf("迁移后 = %s/%d，期望 bpmmimcmpof/721", fresh.WireClass, fresh.TypeID)
	}
	if fresh.Qty != 100 {
		t.Fatalf("迁移后行数量 = %d，期望 100", fresh.Qty)
	}
	mf, err := FieldsFromJSON(fresh.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if got := u64Of(mf[fieldIdxByCS("bpmmimcmpof", "dnmfciolgep")].Num); got != 100 {
		t.Errorf("f13 dnmfciolgep = %d，期望 100", got)
	}
	if got := u64Of(mf[fieldIdxByCS("bpmmimcmpof", csQty)].Num); got != 100 {
		t.Errorf("f1 ljnnpokapig = %d，期望 100", got)
	}
}

// TestReclassifyFlavorFromFallback 存量香精（cat 19，剂量>0）启动修复：
// 早期无映射落成钓线兜底类会让背包静默卡死。应迁到 deckldefiap/125
// （DTO ifgjaajoofo : aklijhgdgbh<bmalnngjeho>）。香精的香味强度/剂量在
// def 模板上，wire 只带身份+数量+状态位。
func TestReclassifyFlavorFromFallback(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()

	def, err := db.GetItemDefByItemID(ctx, "fplt_hvz_bcp8")
	if err != nil || def == nil {
		t.Skip("静态库缺 fplt_hvz_bcp8 定义: ", err)
	}
	if def.Category != 19 {
		t.Fatalf("fplt_hvz_bcp8 品类 = %d，期望 19", def.Category)
	}

	guid := "00112233445566778899aabbccddee04"
	legacy := legacyFallbackItem(t, guid, def, 50)
	if err := db.InsertItem(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetItemByGUID(ctx, guid)
	if err != nil || row == nil {
		t.Fatal("读取存量物品失败: ", err)
	}

	changed, err := srv.reclassifyStoredItem(ctx, row, def)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("香精未被迁移")
	}
	fresh, err := db.GetItemByGUID(ctx, guid)
	if err != nil || fresh == nil {
		t.Fatal("迁移后读取失败: ", err)
	}
	if fresh.WireClass != "deckldefiap" || fresh.TypeID != 125 {
		t.Fatalf("迁移后 = %s/%d，期望 deckldefiap/125", fresh.WireClass, fresh.TypeID)
	}
	if fresh.Qty != 50 {
		t.Fatalf("迁移后行数量 = %d，期望 50", fresh.Qty)
	}
	fields, err := FieldsFromJSON(fresh.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != len(wiregen.ClassOps["deckldefiap"]) {
		t.Fatalf("字段数 = %d，期望 %d", len(fields), len(wiregen.ClassOps["deckldefiap"]))
	}
	if got := u64Of(fields[fieldIdxByCS("deckldefiap", "dnmfciolgep")].Num); got != 50 {
		t.Errorf("f14 dnmfciolgep（权威数量）= %d，期望 50", got)
	}
	if got := u64Of(fields[fieldIdxByCS("deckldefiap", csQty)].Num); got != 50 {
		t.Errorf("f2 ljnnpokapig（总数镜像）= %d，期望 50", got)
	}
	if got := u64Of(fields[fieldIdxByCS("deckldefiap", csNumericID)].Num); got != uint64(def.NumericID) {
		t.Errorf("numeric id = %d，期望 %d", got, def.NumericID)
	}
	if fields[fieldIdxByCS("deckldefiap", csGUID)].Hex != guid {
		t.Errorf("guid 未保留: %s", fields[fieldIdxByCS("deckldefiap", csGUID)].Hex)
	}
	if bits, ok := defAttrU64(def, csStateEnum); ok {
		if got := u64Of(fields[fieldIdxByCS("deckldefiap", csStateEnum)].Num); got != bits {
			t.Errorf("状态位 = %d，期望定义值 %d", got, bits)
		}
	}
	if idx := fieldIdxByCS("deckldefiap", "ninlajclahd"); idx >= 0 {
		t.Errorf("deckldefiap 不应携带 ninlajclahd（该属性在 def 模板上）")
	}
}

// poisonGroundbaitFlavor 造一条香精误落窝料类的存量行——2026-11-05 线上
// 毒行形态（北黑莓 12/黑加仑 8，按 I 必挂、无异常无网络）。
func poisonGroundbaitFlavor(t *testing.T, guid string, def *store.ItemDef, qty int64) *store.Item {
	t.Helper()
	obj, err := wiregen.MakeObj("cilmgoaoofg", nil)
	if err != nil {
		t.Fatal(err)
	}
	fields := obj.Fields
	fields[fieldIdxByCS("cilmgoaoofg", csGUID)].Hex = guid
	fields[fieldIdxByCS("cilmgoaoofg", csNumericID)].Num = ptrU64(uint64(def.NumericID))
	fields[fieldIdxByCS("cilmgoaoofg", csQty)].Num = ptrU64(uint64(qty))
	if v, ok := defAttrU64(def, "ninlajclahd"); ok {
		fields[fieldIdxByCS("cilmgoaoofg", "ninlajclahd")].Num = ptrU64(v)
	}
	if v, ok := defAttrU64(def, csStateEnum); ok {
		fields[fieldIdxByCS("cilmgoaoofg", csStateEnum)].Num = ptrU64(v)
	}
	fj, err := FieldsToJSON(fields)
	if err != nil {
		t.Fatal(err)
	}
	return &store.Item{
		GUID: guid, UID: 1000004, ItemID: def.ItemID, NumericID: def.NumericID,
		WireClass: "cilmgoaoofg", TypeID: 120,
		Container: "backpack", Qty: qty, FieldsJSON: fj, Durability: 1, Workmanship: 1,
	}
}

// TestReclassifyFlavorFromGroundbaitClass 存量迁移：落在窝料类 cilmgoaoofg
// 上的 19 类液体小料（fplt_* 与 feedadds_* 都一样）必须迁到 deckldefiap/125。
// 2026-11-05 隔离实验：feedadds 落 cilmgoaoofg 单独毒死鱼饵页，与 fplt 同罪。
func TestReclassifyFlavorFromGroundbaitClass(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()

	def, err := db.GetItemDefByItemID(ctx, "fplt_op_nep12")
	if err != nil || def == nil {
		t.Skip("静态库缺 fplt_op_nep12 定义: ", err)
	}

	guid := "00112233445566778899aabbccddee05"
	poison := poisonGroundbaitFlavor(t, guid, def, 1)
	if err := db.InsertItem(ctx, poison); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetItemByGUID(ctx, guid)
	if err != nil || row == nil {
		t.Fatal("读取存量物品失败: ", err)
	}
	changed, err := srv.reclassifyStoredItem(ctx, row, def)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("窝料类上的香精未被迁移")
	}
	fresh, err := db.GetItemByGUID(ctx, guid)
	if err != nil || fresh == nil {
		t.Fatal("迁移后读取失败: ", err)
	}
	if fresh.WireClass != "deckldefiap" || fresh.TypeID != 125 {
		t.Fatalf("迁移后 = %s/%d，期望 deckldefiap/125", fresh.WireClass, fresh.TypeID)
	}
	if fresh.Qty != 1 {
		t.Fatalf("迁移后行数量 = %d，期望 1", fresh.Qty)
	}
	fields, err := FieldsFromJSON(fresh.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if got := u64Of(fields[fieldIdxByCS("deckldefiap", "dnmfciolgep")].Num); got != 1 {
		t.Errorf("f14 dnmfciolgep = %d，期望 1", got)
	}

	// 对照：同为 cat 19 的 feedadds_* 条目也必须迁离窝料类。
	addDef, err := db.GetItemDefByItemID(ctx, "feedadds_biscuit")
	if err != nil || addDef == nil {
		t.Skip("静态库缺 feedadds_biscuit 定义: ", err)
	}
	aguid := "00112233445566778899aabbccddee06"
	add := poisonGroundbaitFlavor(t, aguid, addDef, 10)
	if err := db.InsertItem(ctx, add); err != nil {
		t.Fatal(err)
	}
	arow, err := db.GetItemByGUID(ctx, aguid)
	if err != nil || arow == nil {
		t.Fatal("读取存量物品失败: ", err)
	}
	changed, err = srv.reclassifyStoredItem(ctx, arow, addDef)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("窝料类上的添加剂未被迁移")
	}
	afresh, err := db.GetItemByGUID(ctx, aguid)
	if err != nil || afresh == nil {
		t.Fatal("迁移后读取失败: ", err)
	}
	if afresh.WireClass != "deckldefiap" || afresh.TypeID != 125 {
		t.Fatalf("添加剂迁移后 = %s/%d，期望 deckldefiap/125", afresh.WireClass, afresh.TypeID)
	}
}

// TestPrepareNewFlavorUsesFlavorClass 新购 cat 19 香精直接落在 ifgjaajoofo 实例类上；
// cat 20 添加剂由独立测试覆盖，应落 kgjoemjjadb。
func TestPrepareNewFlavorUsesFlavorClass(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()

	if _, err := db.GetItemDefByItemID(ctx, "fplt_hvz_bcp5"); err != nil {
		t.Skip("静态库缺 fplt_hvz_bcp5 定义: ", err)
	}
	// 散买 1 个：def ljnnpokapig=1，数量 = 1。
	it, err := srv.prepareNewItem(ctx, 1000004, "fplt_hvz_bcp5", "backpack", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if it.WireClass != "deckldefiap" || it.TypeID != 125 {
		t.Fatalf("新购实例 = %s/%d，期望 deckldefiap/125", it.WireClass, it.TypeID)
	}
	if it.Qty != 1 {
		t.Fatalf("散买数量 = %d，期望 1", it.Qty)
	}
	// 礼盒拆包路径：units=50（box_*_50），数量 = 1×50。
	box, err := srv.prepareNewItem(ctx, 1000004, "fplt_hvz_bcp5", "backpack", 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if box.Qty != 50 {
		t.Fatalf("礼盒拆包数量 = %d，期望 50", box.Qty)
	}
	fields, err := FieldsFromJSON(box.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if got := u64Of(fields[fieldIdxByCS("deckldefiap", "dnmfciolgep")].Num); got != 50 {
		t.Errorf("f14 dnmfciolgep = %d，期望 50", got)
	}
	if got := u64Of(fields[fieldIdxByCS("deckldefiap", csQty)].Num); got != 50 {
		t.Errorf("f2 ljnnpokapig = %d，期望 50", got)
	}
	if got := u64Of(fields[fieldIdxByCS("deckldefiap", csStateEnum)].Num); got != 21248 {
		t.Errorf("f11 hgaijieklek = %d，期望定义值 21248", got)
	}
	if fields[fieldIdxByCS("deckldefiap", csGUID)].Hex != box.GUID {
		t.Errorf("guid 未写入实例 guid 槽: %s", fields[fieldIdxByCS("deckldefiap", csGUID)].Hex)
	}
	// 对照：同为 cat 19 的 feedadds_* 条目也不能落窝料类。
	if _, err := db.GetItemDefByItemID(ctx, "feedadds_biscuit"); err != nil {
		t.Skip("静态库缺 feedadds_biscuit 定义: ", err)
	}
	add, err := srv.prepareNewItem(ctx, 1000004, "feedadds_biscuit", "backpack", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if add.WireClass != "deckldefiap" || add.TypeID != 125 {
		t.Fatalf("添加剂实例 = %s/%d，期望 deckldefiap/125", add.WireClass, add.TypeID)
	}
}

// wrongClassMetaQtyRow 造一条落在 metaQtyFamily 指定类上的错类行——
// 复现 2026-11-05 分发表修正前的存量形态：cat 20 添加剂误落
// deckldefiap/125、cat 45 PVA 误落 bpmmimcmpof/721。
func wrongClassMetaQtyRow(t *testing.T, guid string, def *store.ItemDef, qty int64,
	class string, typeID int64) *store.Item {
	t.Helper()
	obj, err := wiregen.MakeObj(class, nil)
	if err != nil {
		t.Fatal(err)
	}
	fields := obj.Fields
	fields[fieldIdxByCS(class, csGUID)].Hex = guid
	fields[fieldIdxByCS(class, csNumericID)].Num = ptrU64(uint64(def.NumericID))
	fields[fieldIdxByCS(class, csQty)].Num = ptrU64(uint64(qty))
	if v, ok := defAttrU64(def, csStateEnum); ok {
		fields[fieldIdxByCS(class, csStateEnum)].Num = ptrU64(v)
	}
	fj, err := FieldsToJSON(fields)
	if err != nil {
		t.Fatal(err)
	}
	return &store.Item{
		GUID: guid, UID: 1000004, ItemID: def.ItemID, NumericID: def.NumericID,
		WireClass: class, TypeID: typeID,
		Container: "backpack", Qty: qty, FieldsJSON: fj, Durability: 1, Workmanship: 1,
	}
}

// TestReclassifyAdditiveFromFlavorClass 存量迁移：cat 20 添加剂（aroma_*）
// 误落 deckldefiap/125 必须迁到 dpghngjmnap/126。客户端分发表 case 18 对
// cat 20 整袋 Cast<kgjoemjjadb>，125 的 ifgjaajoofo 一进袋就炸——2026-11-05
// 扫货 aroma/cb_attr/op_attr 后鱼饵页复现转圈的根因。数量写 14 字段布局的
// f12 dnmfciolgep + f0 ljnnpokapig。
func TestReclassifyAdditiveFromFlavorClass(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()

	def, err := db.GetItemDefByItemID(ctx, "aroma_mint")
	if err != nil || def == nil {
		t.Skip("静态库缺 aroma_mint 定义: ", err)
	}
	if def.Category != 20 {
		t.Fatalf("aroma_mint 品类 = %d，期望 20", def.Category)
	}

	guid := "00112233445566778899aabbccddee07"
	poison := wrongClassMetaQtyRow(t, guid, def, 5, "deckldefiap", 125)
	if err := db.InsertItem(ctx, poison); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetItemByGUID(ctx, guid)
	if err != nil || row == nil {
		t.Fatal("读取存量物品失败: ", err)
	}
	changed, err := srv.reclassifyStoredItem(ctx, row, def)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("香精类上的添加剂未被迁移")
	}
	fresh, err := db.GetItemByGUID(ctx, guid)
	if err != nil || fresh == nil {
		t.Fatal("迁移后读取失败: ", err)
	}
	if fresh.WireClass != "dpghngjmnap" || fresh.TypeID != 126 {
		t.Fatalf("迁移后 = %s/%d，期望 dpghngjmnap/126", fresh.WireClass, fresh.TypeID)
	}
	fields, err := FieldsFromJSON(fresh.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != len(wiregen.ClassOps["dpghngjmnap"]) {
		t.Fatalf("字段数 = %d，期望 %d", len(fields), len(wiregen.ClassOps["dpghngjmnap"]))
	}
	if got := u64Of(fields[fieldIdxByCS("dpghngjmnap", "dnmfciolgep")].Num); got != 5 {
		t.Errorf("f12 dnmfciolgep = %d，期望 5", got)
	}
	if got := u64Of(fields[fieldIdxByCS("dpghngjmnap", csQty)].Num); got != 5 {
		t.Errorf("f0 ljnnpokapig = %d，期望 5", got)
	}
	if fields[fieldIdxByCS("dpghngjmnap", csGUID)].Hex != guid {
		t.Errorf("guid 未保留: %s", fields[fieldIdxByCS("dpghngjmnap", csGUID)].Hex)
	}
}

// TestPrepareNewAdditiveUsesAdditiveClass 新购添加剂直接落 dpghngjmnap/126。
func TestPrepareNewAdditiveUsesAdditiveClass(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()

	if _, err := db.GetItemDefByItemID(ctx, "aroma_mint"); err != nil {
		t.Skip("静态库缺 aroma_mint 定义: ", err)
	}
	it, err := srv.prepareNewItem(ctx, 1000004, "aroma_mint", "backpack", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if it.WireClass != "dpghngjmnap" || it.TypeID != 126 {
		t.Fatalf("新购实例 = %s/%d，期望 dpghngjmnap/126", it.WireClass, it.TypeID)
	}
	// def ljnnpokapig=5：单件即含 5 份（线上 aroma_mint 行 qty=5 的来源）。
	if it.Qty != 5 {
		t.Fatalf("数量 = %d，期望 5", it.Qty)
	}
	fields, err := FieldsFromJSON(it.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if got := u64Of(fields[fieldIdxByCS("dpghngjmnap", "dnmfciolgep")].Num); got != 5 {
		t.Errorf("f12 dnmfciolgep = %d，期望 5", got)
	}
	if got := u64Of(fields[fieldIdxByCS("dpghngjmnap", csQty)].Num); got != 5 {
		t.Errorf("f0 ljnnpokapig = %d，期望 5", got)
	}
}

// TestReclassifyPVAFromDipClass 存量迁移：cat 45 PVA 误落 bpmmimcmpof/721
// （nledogicnfe 实为 cat 58 提取物的 DTO）必须迁到 pcngolccead/144——
// 分发表 case 43 期望 bebejlknhka，商店渲染 box_hvz_pva_* 礼盒必炸。
func TestReclassifyPVAFromDipClass(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()

	def, err := db.GetItemDefByItemID(ctx, "hvz_pva_material")
	if err != nil || def == nil {
		t.Skip("静态库缺 hvz_pva_material 定义: ", err)
	}
	if def.Category != 45 {
		t.Fatalf("hvz_pva_material 品类 = %d，期望 45", def.Category)
	}

	guid := "00112233445566778899aabbccddee08"
	poison := wrongClassMetaQtyRow(t, guid, def, 1, "bpmmimcmpof", 721)
	if err := db.InsertItem(ctx, poison); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetItemByGUID(ctx, guid)
	if err != nil || row == nil {
		t.Fatal("读取存量物品失败: ", err)
	}
	changed, err := srv.reclassifyStoredItem(ctx, row, def)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("提取物类上的 PVA 未被迁移")
	}
	fresh, err := db.GetItemByGUID(ctx, guid)
	if err != nil || fresh == nil {
		t.Fatal("迁移后读取失败: ", err)
	}
	if fresh.WireClass != "pcngolccead" || fresh.TypeID != 144 {
		t.Fatalf("迁移后 = %s/%d，期望 pcngolccead/144", fresh.WireClass, fresh.TypeID)
	}
	fields, err := FieldsFromJSON(fresh.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if got := u64Of(fields[fieldIdxByCS("pcngolccead", "dnmfciolgep")].Num); got != 1 {
		t.Errorf("f12 dnmfciolgep = %d，期望 1", got)
	}
	// 对照：cat 58 提取物留在 bpmmimcmpof/721 不动（case 56 就是 nledogicnfe）。
	dipDef, err := db.GetItemDefByItemID(ctx, "dip_hvz_se")
	if err != nil || dipDef == nil {
		t.Skip("静态库缺 dip_hvz_se 定义: ", err)
	}
	dguid := "00112233445566778899aabbccddee09"
	dip := wrongClassMetaQtyRow(t, dguid, dipDef, 3, "bpmmimcmpof", 721)
	if err := db.InsertItem(ctx, dip); err != nil {
		t.Fatal(err)
	}
	drow, err := db.GetItemByGUID(ctx, dguid)
	if err != nil || drow == nil {
		t.Fatal("读取存量提取物失败: ", err)
	}
	dchanged, err := srv.reclassifyStoredItem(ctx, drow, dipDef)
	if err != nil {
		t.Fatal(err)
	}
	if dchanged {
		t.Errorf("cat 58 提取物不应迁移（721 即正确类）")
	}
}

func assertDipQtyFields(t *testing.T, it *store.Item, want int64) {
	t.Helper()
	if it == nil {
		t.Fatal("物品为空")
	}
	fields, err := FieldsFromJSON(it.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	qtyIdx := itemQtyFieldIdx(it.WireClass)
	mirrorIdx := qtyMirrorIdx(it.WireClass)
	if qtyIdx < 0 || qtyIdx >= len(fields) || mirrorIdx < 0 || mirrorIdx >= len(fields) {
		t.Fatalf("%s 缺少数量字段: qty=%d mirror=%d fields=%d", it.WireClass, qtyIdx, mirrorIdx, len(fields))
	}
	qty := u64Of(fields[qtyIdx].Num)
	mirror := u64Of(fields[mirrorIdx].Num)
	if it.Qty != want || qty != uint64(want) || mirror != uint64(want) {
		t.Fatalf("qty=%d field[%d]=%d mirror[%d]=%d, want %d", it.Qty, qtyIdx, qty, mirrorIdx, mirror, want)
	}
}

func TestDipFactoryAndStackKeepQuantityFields(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	it, err := srv.CreateItem(ctx, 1000004, "dip_hvz_se", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	assertDipQtyFields(t, it, 100)

	stacked, merged, err := srv.AddToStack(ctx, 1000004, "dip_hvz_se", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !merged || stacked.GUID != it.GUID {
		t.Fatalf("应并入原堆，merged=%v got=%s want=%s", merged, stacked.GUID, it.GUID)
	}
	fresh, err := db.GetItemByGUID(ctx, it.GUID)
	if err != nil {
		t.Fatal(err)
	}
	assertDipQtyFields(t, fresh, 200)
}

func TestDipExactCountKeepsQuantityFields(t *testing.T) {
	srv, _ := newRigTestServer(t)
	it, err := srv.prepareNewItemWithCount(context.Background(), 1000004, "dip_hvz_se", "backpack", 7, "")
	if err != nil {
		t.Fatal(err)
	}
	assertDipQtyFields(t, it, 7)
}
