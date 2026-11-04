package gameserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/testutil"
	"rf4server/internal/wiregen"
)

// 竿组与钓鱼引擎的集成测试：
//   1. 商店竿的尖梢字段必须补齐，否则客户端拒绝抛竿。
//   2. 路亚拟饵挂载后，14/14 f23 必须使用已落库的出厂钩 GUID。

const (
	testShopRodGUID  = "10000000000000000000000000000001" // 测试用合成 GUID
	testShopLureGUID = "20000000000000000000000000000002" // 测试用合成 GUID
	testStoredHook   = "30000000000000000000000000000003" // 测试用合成 GUID
)

// seedShopRodDef 写入 feeder 竿定义、尖梢名单和竿型。
func seedShopRodDef(t *testing.T, db *store.DB) []string {
	t.Helper()
	tips := []string{"kam_quiv_0.5", "kam_quiv_1.0", "kam_quiv_1.5", "kam_quiv_2.0"}
	attrs, _ := json.Marshal(map[string]any{
		"fopbhlmkcpe": 10,
		"onlobkhillh": tips,
	})
	_, err := db.SQL.Exec(`INSERT OR REPLACE INTO item_def(numeric_id, item_id, category, kind, brand, attrs_json)
		VALUES(5559,'feeder_10126_FD390',3,'rod','',?)`, string(attrs))
	if err != nil {
		t.Fatalf("播种竿定义: %v", err)
	}
	return tips
}

// insertBareRod 插入一支新购竿，f5 和 f8 均为空。
func insertBareRod(t *testing.T, db *store.DB, uid int64, guid, itemID string, numericID int64) {
	t.Helper()
	ops := wiregen.ClassOps["cnolibojkao"]
	fields := make([]proto.Field, len(ops))
	for i, op := range ops {
		zf, err := proto.ZeroField(op)
		if err != nil {
			t.Fatalf("ZeroField(%s): %v", op, err)
		}
		fields[i] = zf
	}
	fields[10].Hex = guid // csGUID
	fj, err := FieldsToJSON(fields)
	if err != nil {
		t.Fatalf("FieldsToJSON: %v", err)
	}
	err = db.InsertItem(context.Background(), &store.Item{
		GUID: guid, UID: uid, ItemID: itemID, NumericID: numericID,
		WireClass: "cnolibojkao", TypeID: 157, Container: "backpack",
		FieldsJSON: fj, Durability: 1, Workmanship: 1,
	})
	if err != nil {
		t.Fatalf("插入商店竿: %v", err)
	}
}

// rodTipOf 从 fields_json 读尖梢（f8）现值。
func rodTipOf(t *testing.T, db *store.DB, guid string) (string, bool) {
	t.Helper()
	row, err := db.GetItemByGUID(context.Background(), guid)
	if err != nil || row == nil {
		t.Fatalf("取竿 %s: %v", guid[:8], err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatalf("fields_json: %v", err)
	}
	idx := fieldIdxByCS(row.WireClass, csRigTip)
	if idx < 0 || idx >= len(fields) {
		return "", false
	}
	if fields[idx].Str == nil {
		return "", false
	}
	return *fields[idx].Str, true
}

// TestRodTipAutoInstall 验证 14/1 会补齐商店竿尖梢并落库，
// 以便客户端在抛竿前通过本地校验。
func TestRodTipAutoInstall(t *testing.T) {
	db := connectTestDB(t)
	gsAddr, srv := startGameserverWithDB(t, db)
	gw := testutil.StartGateway(t, gsAddr, testutil.TestPSK)
	token, uid := gw.Login(t)
	fc := testutil.NewFakeClient(t, gw.RealAddr)
	fc.Dial(token, false)
	if boot := fc.WaitPush(2 * time.Second); boot.Main != 0 || boot.Sub != 1 {
		t.Fatalf("boot 应为 0/1: %d/%d", boot.Main, boot.Sub)
	}

	tips := seedShopRodDef(t, db)
	insertBareRod(t, db, uid, testShopRodGUID, "feeder_10126_FD390", 5559)

	// 前置：商店竿的 f8 为空。
	if tip, ok := rodTipOf(t, db, testShopRodGUID); ok && tip != "" {
		t.Fatalf("前置条件破坏：商店竿尖梢应空，实得 %q", tip)
	}

	// 14/1 钓组查询（抛竿准备的第一条消息）
	fc.SendRequest(300, 14, 1, bodyWithGUIDs(testShopRodGUID)...)
	if resp := fc.WaitResponse(3 * time.Second); len(resp.Body) == 0 || resp.Body[0] != 0x01 {
		t.Fatalf("14/1 应回 158: %x", resp.Body)
	}

	// 尖梢落库 = 名单第一支
	tip, ok := rodTipOf(t, db, testShopRodGUID)
	if !ok || tip == "" {
		t.Fatal("14/1 后尖梢仍未落库")
	}
	if tip != tips[0] {
		t.Fatalf("尖梢应默认名单第一支 %q，实得 %q", tips[0], tip)
	}
	// 校验条件：f8 非空且属于竿定义的尖梢名单。
	inList := false
	for _, k := range tips {
		if k == tip {
			inList = true
		}
	}
	if !inList {
		t.Fatalf("尖梢 %q 不在定义名单 %v —— 客户端将判未安装", tip, tips)
	}

	// 服务端需要推送补好的竿树，客户端同步后才能通过校验。
	p22 := waitPushCmd(t, fc, 4, 22, 3*time.Second)
	if p22 == nil {
		t.Fatal("14/1 尖梢收敛后未收到 4/22 全量同步")
	}
	dec, _, err := proto.DecodeValue(p22.Body, 0, wiregen.Table())
	if err != nil || dec.ArgCodeStr() != "61" || dec.ArgCount() < 1 {
		t.Fatalf("4/22 应为 args(61)[竿…]: %v", err)
	}
	rodTree := dec.ArgItem(0)
	if rodTree == nil || rodTree.Class != "cnolibojkao" {
		t.Fatalf("4/22 args[0] 应为竿: %+v", rodTree)
	}
	if got := rodTree.Field(8).Str; got == nil || *got != tip {
		t.Fatalf("4/22 竿树 f8 尖梢应=%q，实得 %v", tip, got)
	}

	// 14/2 返回的竿树也必须包含尖梢。
	fc.SendRequest(301, 14, 2, bodyWithGUIDs(testShopRodGUID)...)
	resp := fc.WaitResponse(3 * time.Second)
	if len(resp.Body) == 0 || resp.Body[0] != 0x01 {
		t.Fatalf("14/2 应回 800: %x", resp.Body)
	}
	dec, _, err = proto.DecodeValue(resp.Body, 0, wiregen.Table())
	if err != nil {
		t.Fatalf("14/2 解码: %v", err)
	}
	var castRod *proto.Value
	var walk func(v *proto.Value)
	walk = func(v *proto.Value) {
		if v == nil || castRod != nil {
			return
		}
		if v.Class == "cnolibojkao" {
			castRod = v
			return
		}
		for _, it := range v.Items {
			walk(it)
		}
		for i := range v.Fields {
			walk(v.Fields[i].Val)
		}
	}
	walk(dec)
	if castRod == nil {
		t.Fatal("14/2 响应里没有竿树")
	}
	if got := castRod.Field(8).Str; got == nil || *got != tip {
		t.Fatalf("14/2 竿树 f8 尖梢应=%q，实得 %v", tip, got)
	}

	// 幂等：再跑一次 ensureRig 不再改写（返回 false）
	if fixed := srv.ensureRig(context.Background(), uid, testShopRodGUID); fixed {
		t.Fatal("尖梢已收敛，ensureRig 不应再写")
	}

	// 补齐时优先使用玩家已经保存的尖梢选择。
	if err := db.UpsertRodSetting(context.Background(), testShopRodGUID, "quivertip", tips[2]); err != nil {
		t.Fatalf("写 rod_setting: %v", err)
	}
	{
		row, _ := db.GetItemByGUID(context.Background(), testShopRodGUID)
		fields, _ := FieldsFromJSON(row.FieldsJSON)
		idx := fieldIdxByCS(row.WireClass, csRigTip)
		fields[idx].Str = nil // 模拟存量竿 f8 丢失
		fj, _ := FieldsToJSON(fields)
		_ = db.UpdateItemFields(context.Background(), row.GUID, fj, row.Qty, row.Durability, row.Workmanship, row.RigGUID)
	}
	if fixed := srv.ensureRig(context.Background(), uid, testShopRodGUID); !fixed {
		t.Fatal("尖梢丢失后 ensureRig 应补写")
	}
	if tip, _ := rodTipOf(t, db, testShopRodGUID); tip != tips[2] {
		t.Fatalf("应优先玩家 rod_setting 所选 %q，实得 %q", tips[2], tip)
	}

	// 定义中没有尖梢名单的竿不写入。
	spinGUID := strings.Repeat("a", 32)
	_, err = db.SQL.Exec(`INSERT OR REPLACE INTO item_def(numeric_id, item_id, category, kind, brand, attrs_json)
		VALUES(13355,'spin_10032_S60L',3,'rod','', '{"fopbhlmkcpe":1}')`)
	if err != nil {
		t.Fatalf("播种 spin 定义: %v", err)
	}
	insertBareRod(t, db, uid, spinGUID, "spin_10032_S60L", 13355)
	if fixed := srv.ensureRig(context.Background(), uid, spinGUID); fixed {
		t.Fatal("无尖梢名单的竿不应写尖梢")
	}
	if tip, ok := rodTipOf(t, db, spinGUID); ok && tip != "" {
		t.Fatalf("spin 竿尖梢应保持空（客户端按竿型免除该校验），实得 %q", tip)
	}
}

// TestLureBiteGating 验证挂载拟饵后的咬钩条件：
// 挂载时 f23 使用已落库出厂钩 GUID，未挂载时回退到鱼自身 GUID。
func TestLureBiteGating(t *testing.T) {
	db := connectTestDB(t)
	gsAddr, srv := startGameserverWithDB(t, db)
	gw := testutil.StartGateway(t, gsAddr, testutil.TestPSK)
	token, uid := gw.Login(t)
	fc := testutil.NewFakeClient(t, gw.RealAddr)
	fc.Dial(token, false)
	if boot := fc.WaitPush(2 * time.Second); boot.Main != 0 || boot.Sub != 1 {
		t.Fatalf("boot 应为 0/1: %d/%d", boot.Main, boot.Sub)
	}
	ctx := context.Background()

	// starter 播种：路亚竿（rig_Lure type5）+ 拟饵（f0 出厂钩）
	rodGUID, lureGUID := "", ""
	guids, _ := db.ItemGUIDsByContainers(ctx, uid, []string{"backpack", "rig"})
	for _, g := range guids {
		row, err := db.GetItemByGUID(ctx, g)
		if err != nil {
			continue
		}
		switch {
		case row.WireClass == "cnolibojkao" && row.NumericID == 13328:
			rodGUID = g
		case row.WireClass == "ioombppcnlh" && row.NumericID == 14026:
			lureGUID = g
		}
	}
	if rodGUID == "" || lureGUID == "" {
		t.Fatalf("starter 缺路亚竿/拟饵: rod=%q lure=%q", rodGUID, lureGUID)
	}
	if lureGUID != testShopLureGUID {
		t.Logf("拟饵 guid=%s（使用落库出厂钩）", lureGUID[:8])
	}
	lureRow, _ := db.GetItemByGUID(ctx, lureGUID)
	lureFields, _ := FieldsFromJSON(lureRow.FieldsJSON)
	storedHooks := storedLureHookGUIDs(&lureFields[0])
	if len(storedHooks) == 0 {
		t.Fatal("starter 拟饵 f0 缺落库出厂钩")
	}
	wantStoredHook := storedHooks[0]
	// 拟饵定义（生产库常备；currentBait 的 numeric_id 反查依赖它）
	if _, err := db.SQL.Exec(`INSERT OR IGNORE INTO item_def(numeric_id, item_id, category, kind, brand, attrs_json)
		VALUES(14026,'spinnerbaits_1244',17,'lure','', '{}')`); err != nil {
		t.Fatalf("播种拟饵定义: %v", err)
	}

	// 挂载拟饵：更新实例组件表并同步钓组索引。
	mountLure(t, db, srv, rodGUID, lureGUID)
	if _, err := srv.DB.GetItemGUIDByRigSlot(ctx, rodGUID, 17); err != nil {
		t.Fatalf("rig_component 槽 17 未登记: %v", err)
	}

	// 钓组类型 5 属于路亚族，使用独立的咬钩节奏。
	row, _ := db.GetItemByGUID(ctx, rodGUID)
	if rt := srv.rigTypeOfItem(ctx, row); rt != 5 {
		t.Fatalf("rig_type 应=5 (rig_Lure)，实得 %d", rt)
	}
	if !rigTypeLureFamily[5] {
		t.Fatal("类型 5 应属路亚族")
	}

	// f23 必须解析到已落库的出厂钩 GUID。
	if hook := srv.mountedHookGuid(ctx, rodGUID); hook != wantStoredHook {
		t.Fatalf("f23 应=落库出厂钩 %s，实得 %s（公式派生则对不上客户端部件→秒弃咬）",
			wantStoredHook, hook)
	}
	// 拟饵也作为饵参与鱼种选择：品类 27 优先，17 兜底。
	if bait := srv.currentBait(ctx, rodGUID); bait != "spinnerbaits_1244" {
		t.Fatalf("currentBait 应=拟饵 item_id，实得 %q", bait)
	}
	// 路亚咬钩窗为 1.5 到 6 秒。
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.RigType = srv.rigTypeOfItem(ctx, row)
	st.mu.Unlock()
	if d := srv.biteDelay(st); d < 0.8 || d > 6.0 {
		t.Fatalf("路亚咬钩延迟应在 [0.8, 6.0]s，实得 %.2f", d)
	}

	// 执行 14/1 到 14/2 的抛竿流程。
	fc.SendRequest(400, 14, 1, bodyWithGUIDs(rodGUID)...)
	fc.WaitResponse(3 * time.Second)
	fc.SendRequest(401, 14, 2, bodyWithGUIDs(rodGUID)...)
	if resp := fc.WaitResponse(3 * time.Second); len(resp.Body) == 0 || resp.Body[0] != 0x01 {
		t.Fatalf("14/2 应回 800: %x", resp.Body)
	}

	waterBody, err := proto.EncodeValue(proto.Args524(wiregen.MustMakeObj("lobemkloehp", map[int]*proto.Value{5: proto.U32(0x600)})), wiregen.Table())
	if err != nil {
		t.Fatal(err)
	}
	fc.SendRequest(402, 14, 7, waterBody...)
	// 路亚应在 6 秒窗口内咬钩，额外等待落水时间。
	fish14 := waitPushCmd(t, fc, 14, 14, 15*time.Second)
	if fish14 == nil {
		t.Fatal("路亚挂饵后 15s 内未咬钩（14/14 未达）")
	}
	dec, _, err := proto.DecodeValue(fish14.Body, 0, wiregen.Table())
	if err != nil || dec.ArgCount() != 2 {
		t.Fatalf("14/14 应为 args[2]: %v", err)
	}
	fishObj := dec.ArgItem(1)
	if fishObj == nil || fishObj.Class != "fgogccpglhb" {
		t.Fatalf("14/14 args[1] 应为 27 判鱼对象: %+v", fishObj)
	}
	if got := fishObj.Field(23); got.Hex != wantStoredHook {
		t.Fatalf("14/14 f23 应=落库出厂钩 %s，实得 %s", wantStoredHook, got.Hex)
	}

	fc.SendRequest(403, 14, 6)
	if response := fc.WaitResponse(3 * time.Second); response == nil {
		t.Fatal("放生未完成")
	}

	// GM 指定鱼仍可在空钩时验证 f23 的鱼 GUID 回退。
	if _, err := db.SQL.Exec(`DELETE FROM rig_component WHERE rig_guid=?`, rodGUID); err != nil {
		t.Fatalf("清 rig_component: %v", err)
	}
	unmountLure(t, db, rodGUID)
	if hook := srv.mountedHookGuid(ctx, rodGUID); hook != "" {
		t.Fatalf("未挂载拟饵时 f23 应回空（线末端不可解析），实得 %s", hook)
	}
	st.mu.Lock()
	st.Armed = &RolledFish{FishGUID: newFishGUID(), Species: "perch", WeightG: 100, LengthM: 0.12}
	st.mu.Unlock()
	fc.SendRequest(404, 14, 2, bodyWithGUIDs(rodGUID)...)
	if resp := fc.WaitResponse(3 * time.Second); len(resp.Body) == 0 || resp.Body[0] != 0x01 {
		t.Fatalf("14/2 应回 800: %x", resp.Body)
	}
	fc.SendRequest(405, 14, 7, waterBody...)
	fish14b := waitPushCmd(t, fc, 14, 14, 15*time.Second)
	if fish14b == nil {
		t.Fatal("GM 指定鱼未送达，无法校验 f23 回退")
	}
	dec, _, _ = proto.DecodeValue(fish14b.Body, 0, wiregen.Table())
	fishObj = dec.ArgItem(1)
	got := fishObj.Field(23).Hex
	if got == wantStoredHook {
		t.Fatal("未挂载拟饵不应再解析出出厂钩 guid")
	}
	wantFishGUID := fishObj.Field(0).Hex
	if got != wantFishGUID {
		t.Fatalf("未挂载时 f23 应回退鱼 GUID %s，实得 %s", wantFishGUID, got)
	}
	if got == rodGUID {
		t.Fatalf("未挂载时 f23 不应回退竿 GUID %s", rodGUID)
	}
}

// mountLure 将拟饵写入竿实例组件表并同步钓组索引。
func mountLure(t *testing.T, db *store.DB, srv *Server, rodGUID, lureGUID string) {
	t.Helper()
	ctx := context.Background()
	row, err := db.GetItemByGUID(ctx, rodGUID)
	if err != nil || row == nil {
		t.Fatalf("取竿: %v", err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil || len(fields) <= 5 || fields[5].Val == nil {
		t.Fatalf("竿 f5 缺失: %v", err)
	}
	comp := wiregen.MustMakeObj("gcfcdnfmnbh", map[int]*proto.Value{
		0: proto.U32(17),
		1: proto.Guid(lureGUID),
		2: proto.U32(300),
	})
	f2 := fields[5].Val.Field(2)
	f2.Val = proto.Args(proto.S("81"), comp)
	fields[5].Val.Fields[2] = f2
	fj, err := FieldsToJSON(fields)
	if err != nil {
		t.Fatalf("FieldsToJSON: %v", err)
	}
	if err := db.UpdateItemFields(ctx, row.GUID, fj, row.Qty, row.Durability, row.Workmanship, row.RigGUID); err != nil {
		t.Fatalf("写回竿: %v", err)
	}
	srv.ensureRig(ctx, 1000001, rodGUID)
}

// unmountLure 清空竿实例组件表并移除钓组索引。
func unmountLure(t *testing.T, db *store.DB, rodGUID string) {
	t.Helper()
	ctx := context.Background()
	row, err := db.GetItemByGUID(ctx, rodGUID)
	if err != nil || row == nil {
		t.Fatalf("取竿: %v", err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil || len(fields) <= 5 || fields[5].Val == nil {
		t.Fatalf("竿 f5 缺失: %v", err)
	}
	f2 := fields[5].Val.Field(2)
	f2.Val = proto.Args(proto.S("81"))
	fields[5].Val.Fields[2] = f2
	fj, err := FieldsToJSON(fields)
	if err != nil {
		t.Fatalf("FieldsToJSON: %v", err)
	}
	if err := db.UpdateItemFields(ctx, row.GUID, fj, row.Qty, row.Durability, row.Workmanship, row.RigGUID); err != nil {
		t.Fatalf("写回竿: %v", err)
	}
}
