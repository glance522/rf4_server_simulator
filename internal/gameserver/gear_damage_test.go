package gameserver

import (
	"context"
	"database/sql"
	"slices"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func mountGearForTest(t *testing.T, srv *Server, sess *Session, rodGUID string, itemGUID string) *rigSetResult {
	t.Helper()
	ctx := context.Background()
	row, err := srv.DB.GetItemByGUID(ctx, itemGUID)
	if err != nil || row == nil {
		t.Fatalf("读取待装配物品失败: %v", err)
	}
	cat, _ := srv.itemCategoryOf(ctx, row)
	res := srv.rigSetComponent(ctx, row.UID, rodGUID, cat, itemGUID, 0,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true, SplitStack: true})
	if res == nil || res.Rejected || res.MountedGUID == "" {
		t.Fatalf("装配 %s 失败: %+v", row.ItemID, res)
	}
	return res
}

func rigSlotGUIDs(t *testing.T, srv *Server, rodGUID string) map[int64]string {
	t.Helper()
	rows, err := srv.DB.ListRigComponents(context.Background(), rodGUID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int64]string{}
	for _, row := range rows {
		out[row.Slot] = row.GUID
	}
	return out
}

func lineLengthOfTestItem(t *testing.T, srv *Server, guid string) int64 {
	t.Helper()
	row, err := srv.DB.GetItemByGUID(context.Background(), guid)
	if err != nil || row == nil {
		t.Fatalf("读取鱼线失败: %v", err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	return itemLengthField(row, fields)
}

func TestWeakestLeaderBreaksAndKeepsMainLine(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15095", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	leader, err := srv.CreateItem(ctx, uid, "fc_leader_40048", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := srv.CreateItem(ctx, uid, "clas_20163", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	bait, err := srv.CreateItem(ctx, uid, "worm_01", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	bobber, err := srv.CreateItem(ctx, uid, "bob_25001", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	for _, item := range []string{line.GUID, leader.GUID, hook.GUID, bait.GUID, bobber.GUID} {
		mountGearForTest(t, srv, sess, rod.GUID, item)
	}
	comps := rigSlotGUIDs(t, srv, rod.GUID)
	lineGUID := comps[catLine]
	if lineGUID != line.GUID {
		t.Fatalf("主线 GUID=%s，期望 %s", lineGUID, line.GUID)
	}
	beforeLength := lineLengthOfTestItem(t, srv, lineGUID)
	if beforeLength <= 1 {
		t.Fatalf("测试鱼线长度异常: %d", beforeLength)
	}
	beforeLine, _ := db.GetItemByGUID(ctx, lineGUID)

	tree := rigRequestTree(t, proto.Guid(rod.GUID), proto.Guid(leader.GUID), proto.U32(2), proto.Null())
	body, ok := hDynamicRodBreak(&Ctx{Sess: sess, Main: 17, Sub: 9, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatal("17/9 断线未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if u64Of(dec.Fields[0].Num) == 0 {
		t.Fatal("断线响应 f0 应为 true")
	}
	if got := dec.Fields[1].GuidHex(); got != leader.GUID {
		t.Fatalf("断子线响应 f1=%s，期望子线 %s", got, leader.GUID)
	}

	afterLine, err := db.GetItemByGUID(ctx, lineGUID)
	if err != nil || afterLine == nil {
		t.Fatalf("断线后主线应保留: %v", err)
	}
	if afterLine.Durability != beforeLine.Durability {
		t.Fatalf("断线不应扣耐久: %v -> %v", beforeLine.Durability, afterLine.Durability)
	}
	afterLength := lineLengthOfTestItem(t, srv, lineGUID)
	if afterLength != beforeLength {
		t.Fatalf("断子线不应扣主线长度: 原=%d 现在=%d", beforeLength, afterLength)
	}
	if row, _ := db.GetItemByGUID(ctx, leader.GUID); row != nil {
		t.Fatal("断子线后子线实例应被删除")
	}
	if row, _ := db.GetItemByGUID(ctx, bobber.GUID); row == nil {
		t.Fatal("断子线后上方浮漂应保留")
	}

	for _, slot := range []int64{catHook, catLead, catWireLeader, catBait} {
		if guid := comps[slot]; guid != "" {
			if row, _ := db.GetItemByGUID(ctx, guid); row != nil {
				t.Fatalf("断线后槽位 %d 的物品 %s 应被删除，实际仍存在", slot, guid)
			}
		}
	}
	if got := rigSlotGUIDs(t, srv, rod.GUID); got[catLine] != lineGUID {
		t.Fatal("断线后竿 f3/rig_component 应仍指向主线")
	}
	if _, ok := rigCompsOf(rigStoredF5(t, mustItem(t, db, rod.GUID)))[catHook]; ok {
		t.Fatal("断线后竿 f5 不应残留鱼钩")
	}

	logs, err := db.ListItemDamageLogs(ctx, uid, 100)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, entry := range logs {
		counts[entry.Event]++
	}
	if counts[damageEventLineBreak] != 1 {
		t.Fatalf("line_break 记录=%d，期望 1；全部=%v", counts[damageEventLineBreak], counts)
	}
	if counts[damageEventLostOnLineBreak] < 3 {
		t.Fatalf("lost_on_line_break 记录=%d，期望至少 3；全部=%v", counts[damageEventLostOnLineBreak], counts)
	}
	var lineBreaks int64
	if err := db.SQL.QueryRow(`SELECT value FROM player_counters WHERE uid=? AND key='line_breaks'`, uid).Scan(&lineBreaks); err != nil || lineBreaks != 1 {
		t.Fatalf("line_breaks 计数=%d err=%v，期望 1", lineBreaks, err)
	}
}

func TestMainLineBreakLosesExactlyFiveMeters(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15095", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	lineRow, _ := db.GetItemByGUID(ctx, line.GUID)
	fields, _ := FieldsFromJSON(lineRow.FieldsJSON)
	if !setItemLengthField(lineRow, fields, 50) {
		t.Fatal("设置鱼线长度失败")
	}
	fj, _ := FieldsToJSON(fields)
	if err := db.UpdateItemFields(ctx, line.GUID, fj, lineRow.Qty, lineRow.Durability, lineRow.Workmanship, lineRow.RigGUID); err != nil {
		t.Fatal(err)
	}

	// 客户端上报主线断：官服清杯口径，主线整条丢失（含挂线配件）。
	tree := rigRequestTree(t, proto.Guid(rod.GUID), proto.Guid(line.GUID), proto.U32(1), proto.Null())
	body, ok := hDynamicRodBreak(&Ctx{Sess: sess, Main: 17, Sub: 9, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatal("17/9 主线断裂未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if u64Of(dec.Fields[0].Num) == 0 {
		t.Fatal("主线断裂响应 f0 应为 true")
	}
	if got := dec.Fields[1].GuidHex(); got != line.GUID {
		t.Fatalf("应断主线 %s，实得 %s", line.GUID, got)
	}
	if row, _ := db.GetItemByGUID(ctx, line.GUID); row != nil {
		t.Fatalf("断主线后主线应整条销毁，实得 %+v", row)
	}
}

func TestBrokenRodPersistsZeroDurabilityAndCannotMount(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	tree := rigRequestTree(t, proto.Guid(rod.GUID), proto.Guid(rod.GUID), proto.U32(1), proto.Null())
	body, ok := hDynamicRodBreak(&Ctx{Sess: sess, Main: 17, Sub: 9, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatal("17/9 断竿未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if u64Of(dec.Fields[0].Num) != 0 {
		t.Fatal("断竿响应 f0 应为 false")
	}
	row, err := db.GetItemByGUID(ctx, rod.GUID)
	if err != nil || row == nil || row.Durability > 0 {
		t.Fatalf("断竿耐久应落库为 0: row=%+v err=%v", row, err)
	}

	hook, err := srv.CreateItem(ctx, uid, "clas_20163", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetItemDurability(ctx, hook.GUID, 0); err != nil {
		t.Fatal(err)
	}
	res := srv.rigSetComponent(ctx, uid, rod.GUID, catHook, hook.GUID, 0,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true})
	if res == nil || !res.Rejected {
		t.Fatalf("耐久 0 的鱼钩应禁止装配: %+v", res)
	}

	line, err := srv.CreateItem(ctx, uid, "mono_40853", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	lineFields, err := FieldsFromJSON(line.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !setItemLengthField(line, lineFields, 0) {
		t.Fatal("设置鱼线长度失败")
	}
	lineJSON, _ := FieldsToJSON(lineFields)
	if err := db.UpdateItemFields(ctx, line.GUID, lineJSON, line.Qty, line.Durability, line.Workmanship, line.RigGUID); err != nil {
		t.Fatal(err)
	}
	res = srv.rigSetComponent(ctx, uid, rod.GUID, catLine, line.GUID, 0,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true})
	if res == nil || !res.Rejected {
		t.Fatalf("剩余长度为 0 的鱼线应禁止装配: %+v", res)
	}

	takeTree := rigRequestTree(t, proto.Guid(rod.GUID))
	takeBody, ok := hRodTake(&Ctx{Sess: sess, Main: 17, Sub: 7, Tree: takeTree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()})
	if !ok || len(takeBody) == 0 {
		t.Fatal("17/7 取断竿未处理")
	}
	takeDec, _, err := proto.DecodeValue(takeBody, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if got := takeDec.Fields[0].GuidHex(); got != zeroGUID {
		t.Fatalf("断竿不应被拿到手上，158.f0=%s", got)
	}
	logs, err := db.ListItemDamageLogs(ctx, uid, 20)
	if err != nil || len(logs) == 0 || logs[0].Event != damageEventRodBreak {
		t.Fatalf("缺少 rod_break 记录: logs=%+v err=%v", logs, err)
	}
	var rodBreaks int64
	if err := db.SQL.QueryRow(`SELECT value FROM player_counters WHERE uid=? AND key='rod_breaks'`, uid).Scan(&rodBreaks); err != nil || rodBreaks != 1 {
		t.Fatalf("rod_breaks 计数=%d err=%v，期望 1", rodBreaks, err)
	}
}

func TestWearAddsHistoryAndWearsRodBody(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_40853", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	srv.wearRigForCatchPersisted(ctx, rod.GUID)

	freshRod, _ := db.GetItemByGUID(ctx, rod.GUID)
	freshLine, _ := db.GetItemByGUID(ctx, line.GUID)
	if freshRod.Durability >= 1 {
		t.Fatalf("竿本体应产生磨损，耐久=%v", freshRod.Durability)
	}
	if freshLine.Durability >= 1 {
		t.Fatalf("鱼线应产生磨损，耐久=%v", freshLine.Durability)
	}
	logs, err := db.ListItemDamageLogs(ctx, uid, 20)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, entry := range logs {
		if entry.Event == damageEventWear {
			got[entry.ItemGUID] = true
		}
	}
	if !got[rod.GUID] || !got[line.GUID] {
		t.Fatalf("磨损历史缺少竿或线: %+v", got)
	}
}

func TestLineBreakExhaustsAndDeletesLine(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rod, err := srv.CreateItem(ctx, uid, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15095", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	lineRow, _ := db.GetItemByGUID(ctx, line.GUID)
	fields, _ := FieldsFromJSON(lineRow.FieldsJSON)
	if !setItemLengthField(lineRow, fields, 1) {
		t.Fatal("设置鱼线长度失败")
	}
	fj, _ := FieldsToJSON(fields)
	if err := db.UpdateItemFields(ctx, line.GUID, fj, lineRow.Qty, lineRow.Durability, lineRow.Workmanship, lineRow.RigGUID); err != nil {
		t.Fatal(err)
	}

	tree := rigRequestTree(t, proto.Guid(rod.GUID), proto.Guid(line.GUID), proto.U32(2), proto.Null())
	if _, ok := hDynamicRodBreak(&Ctx{Sess: sess, Main: 17, Sub: 9, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()}); !ok {
		t.Fatal("17/9 断线未处理")
	}
	if row, _ := db.GetItemByGUID(ctx, line.GUID); row != nil {
		t.Fatal("鱼线长度耗尽后应删除物品")
	}
	freshRod, _ := db.GetItemByGUID(ctx, rod.GUID)
	rodFields, _ := FieldsFromJSON(freshRod.FieldsJSON)
	if idx := fieldIdxByCS(freshRod.WireClass, csLine); idx >= 0 && rodFields[idx].GuidHex() != "" {
		t.Fatal("鱼线删除后竿 f3 应清空")
	}
	logs, _ := db.ListItemDamageLogs(ctx, uid, 20)
	seen := map[string]bool{}
	for _, entry := range logs {
		seen[entry.Event] = true
	}
	if !seen[damageEventLineBreak] || !seen[damageEventLineExhausted] {
		t.Fatalf("长度耗尽应记录 line_break 和 line_exhausted: %+v", seen)
	}
}

func TestBrokenHandRodDeletesFloat(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15065", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	bobber, err := srv.CreateItem(ctx, uid, "bob_25001", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, bobber.GUID)
	comps := rigSlotGUIDs(t, srv, rod.GUID)
	if comps[catFloat] != bobber.GUID {
		t.Fatalf("手竿浮漂未装配: slot28=%s，期望 %s", comps[catFloat], bobber.GUID)
	}

	tree := rigRequestTree(t, proto.Guid(rod.GUID), proto.Guid(line.GUID), proto.U32(2), proto.Null())
	if _, ok := hDynamicRodBreak(&Ctx{Sess: sess, Main: 17, Sub: 9, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()}); !ok {
		t.Fatal("17/9 手竿断线未处理")
	}
	if row, _ := db.GetItemByGUID(ctx, bobber.GUID); row != nil {
		t.Fatal("手竿断线后浮漂应被删除")
	}
	if got := rigSlotGUIDs(t, srv, rod.GUID); got[catFloat] != "" {
		t.Fatalf("手竿断线后 rig_component 仍残留浮漂: %s", got[catFloat])
	}
	freshRod := mustItem(t, db, rod.GUID)
	if _, ok := rigCompsOf(rigStoredF5(t, freshRod))[catFloat]; ok {
		t.Fatal("手竿断线后竿 f5 仍残留浮漂")
	}
}

// 钩已进承力链：客户端上报钩断后由服务端按最弱环裁决，只有钩确实最弱时
// 才摘钩。这里用 3.15kg 弱钩 + 4.09kg 竿 + 13.6kg 强线构造该场景，验证
// 裁决为断钩时组件耐久归零并离竿，竿本体与承力线不受影响。
// （回归：旧的"无条件采信钩断"实现会跳过这条物理裁决。）
func TestHookBreakDetachesComponentKeepsRod(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "tele_10012_tl16", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_40853", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := srv.CreateItem(ctx, uid, "orig_20008_18", "backpack", 1) // 3.15kg，最弱环
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	res := srv.rigSetComponent(ctx, uid, rod.GUID, catHook, hook.GUID, 0,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true})
	if res == nil || res.Rejected {
		t.Fatalf("鱼钩装配被拒: %+v", res)
	}
	if got := rigSlotGUIDs(t, srv, rod.GUID); got[catHook] != hook.GUID {
		t.Fatalf("鱼钩未装配: slot12=%s 期望 %s", got[catHook], hook.GUID)
	}

	tree := rigRequestTree(t, proto.Guid(rod.GUID), proto.Guid(hook.GUID), proto.U32(2), proto.Null())
	body, ok := hDynamicRodBreak(&Ctx{Sess: sess, Main: 17, Sub: 9, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatal("17/9 钩断未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if u64Of(dec.Fields[0].Num) != 0 {
		t.Fatal("钩断响应 f0 应为 false（非断线）")
	}
	if got := dec.Fields[1].GuidHex(); got != hook.GUID {
		t.Fatalf("钩断响应 f1=%s，期望 %s", got, hook.GUID)
	}
	if got := rigSlotGUIDs(t, srv, rod.GUID); got[catHook] != "" {
		t.Fatalf("钩断后 rig_component 仍残留鱼钩: slot12=%s", got[catHook])
	}
	freshRod := mustItem(t, db, rod.GUID)
	if _, ok := rigCompsOf(rigStoredF5(t, freshRod))[catHook]; ok {
		t.Fatal("钩断后竿 f5 仍残留鱼钩")
	}
	if freshRod.Durability <= 0 {
		t.Fatalf("钩断不应归零竿耐久: %v", freshRod.Durability)
	}
	// 官服口径：断钩即丢失销毁，不再退回背包。
	if row, _ := db.GetItemByGUID(ctx, hook.GUID); row != nil {
		t.Fatalf("断钩后鱼钩应销毁，实得 %+v", row)
	}
	if mustItem(t, db, line.GUID) == nil {
		t.Fatal("钩断不应影响鱼线")
	}
	logs, err := db.ListItemDamageLogs(ctx, uid, 20)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, entry := range logs {
		seen[entry.Event] = true
	}
	if !seen[damageEventComponentBreak] {
		t.Fatalf("应记录 component_break: %+v", seen)
	}
	if seen[damageEventRodBreak] || seen[damageEventLineBreak] {
		t.Fatalf("钩断不应记录 rod_break/line_break: %+v", seen)
	}
}

// 钩已移出无条件采信的终端件集合，这里改用浮漂（仍是终端件）验证"仅有
// rig_component 投影、f5 未同步"时断裂也会清掉投影，避免拿一个不再属于该
// 路径的品类来断言旧行为。
func TestTerminalComponentProjectionOnlyBreakCleansProjection(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "tele_10012_tl16", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	bobber, err := srv.CreateItem(ctx, uid, "bob_25001", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟历史/部分同步状态：rig_component 有投影，但竿 f5 尚未包含该组件。
	if err := db.SetItemRigLink(ctx, bobber.GUID, rod.GUID, catFloat); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertRigComponentAt(ctx, rod.GUID, catFloat, 0, bobber.GUID, 0); err != nil {
		t.Fatal(err)
	}

	outcome, err := srv.breakLineOrRod(ctx, uid, rod.GUID, bobber.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome == nil || !outcome.ComponentBroken {
		t.Fatalf("应裁决为终端件断裂: %+v", outcome)
	}
	if got := rigSlotGUIDs(t, srv, rod.GUID)[catFloat]; got != "" {
		t.Fatalf("仅投影存在的组件断裂后仍残留 rig_component: %s", got)
	}
	// 官服口径：终端件断裂即丢失销毁。
	if row, _ := db.GetItemByGUID(ctx, bobber.GUID); row != nil {
		t.Fatalf("终端件断裂后应销毁，实得 %+v", row)
	}
}

func TestInvalidBreakReportPreservesCatchAndEquipment(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	sess := newProbeSess(srv)
	uid := sess.Userid
	create := func(owner int64, itemID string) string {
		t.Helper()
		row, err := srv.CreateItem(ctx, owner, itemID, "backpack", 1)
		if err != nil {
			t.Fatal(err)
		}
		return row.GUID
	}
	rod := create(uid, "feeder_10125_FD360")
	line := create(uid, "mono_40853")
	mountGearForTest(t, srv, sess, rod, line)
	bell := mountGearForTest(t, srv, sess, rod, create(uid, "feeder_bell")).MountedGUID
	otherRod := create(uid, "feeder_10125_FD360")
	foreignRod := create(uid+1, "feeder_10125_FD360")
	unmounted := create(uid, "mono_40853")
	foreignHook := create(uid+1, "clas_20163")
	otherLine := create(uid, "mono_40853")
	mountGearForTest(t, srv, sess, otherRod, otherLine)
	wrongSlot := create(uid, "clas_20163")
	if err := db.UpsertRigComponentAt(ctx, rod, catFloat, 0, wrongSlot, 0); err != nil {
		t.Fatal(err)
	}

	st := srv.stateFor(uid)
	pending := &RolledFish{Species: "perch"}
	st.mu.Lock()
	st.GearGuid, st.Pending, st.CastActive, st.Hooked = rod, pending, true, true
	st.mu.Unlock()
	length := lineLengthOfTestItem(t, srv, line)
	before := mustItem(t, db, rod).Durability

	for _, tc := range []struct {
		name, target, part string
	}{
		{"unknown", rod, "ffffffffffffffffffffffffffffffff"},
		{"foreign component", rod, foreignHook},
		{"foreign rod", foreignRod, foreignRod},
		{"unmounted line", rod, unmounted},
		{"another rig's line", rod, otherLine},
		{"unsupported mounted item", rod, bell},
		{"wrong projection slot", rod, wrongSlot},
		{"nonrod target", line, line},
		{"undecoded request", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := &Ctx{Sess: sess, Main: 17, Sub: 9, ExpectResponse: true, Log: logging.Discard()}
			if tc.target != "" {
				request.Tree = rigRequestTree(t, proto.Guid(tc.target), proto.Guid(tc.part), proto.U32(1), proto.Null())
				request.DecodeOK = true
			}
			result := srv.dispatch.executeRequest(request, true)
			if !result.hasResponse || !result.nativeFailure {
				t.Fatalf("无效断件没有返回协议错误: %+v", result)
			}
			st.mu.Lock()
			unchanged := st.Pending == pending && st.CastActive && st.Hooked && !st.Resolved && st.GearGuid == rod
			st.mu.Unlock()
			if !unchanged || mustItem(t, db, rod).Durability != before || lineLengthOfTestItem(t, srv, line) != length {
				t.Fatal("无效断件改变了钓获或装备")
			}
			logs, err := db.ListItemDamageLogs(ctx, uid, 20)
			if err != nil || len(logs) != 0 {
				t.Fatalf("无效断件写入了损坏记录: %+v %v", logs, err)
			}
		})
	}
}

func TestBreakReportAcceptsMountedLoadComponents(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	sess := newProbeSess(srv)
	uid := sess.Userid
	create := func(id string) string {
		t.Helper()
		row, err := srv.CreateItem(ctx, uid, id, "backpack", 1)
		if err != nil {
			t.Fatal(err)
		}
		return row.GUID
	}
	rod := create("feeder_10125_FD360")
	for _, id := range []string{"mono_40853", "spin_5820", "fc_leader_40048"} {
		item := mountGearForTest(t, srv, sess, rod, create(id)).MountedGUID
		if report, err := srv.validateBreakReport(ctx, uid, rod, item); err != nil || report.terminalCategory != 0 || report.embedded {
			t.Fatalf("已装配的承力件 %s 没有交由承力规则: %+v %v", id, report, err)
		}
	}

	factoryRod := create("spin_10158_M88HH")
	factoryGUID := func(guid string) string {
		t.Helper()
		tree, err := srv.ItemToTree(ctx, mustItem(t, db, guid))
		if err != nil {
			t.Fatal(err)
		}
		child := tree.Field(0).Val
		if child == nil {
			t.Fatal("缺少出厂竿件")
		}
		return child.Field(fieldIdxByCS(child.Class, csGUID)).Hex
	}
	if _, err := srv.breakLineOrRod(ctx, uid, factoryRod, factoryGUID(create("spin_10158_M88HH"))); err == nil {
		t.Fatal("错误接收了另一把竿的出厂部件")
	}
	part := factoryGUID(factoryRod)
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.GearGuid = factoryRod
	st.mu.Unlock()
	request := &Ctx{Sess: sess, Main: 17, Sub: 9, DecodeOK: true, ExpectResponse: true, Log: logging.Discard(),
		Tree: rigRequestTree(t, proto.Guid(zeroGUID), proto.Guid(part), proto.U32(1), proto.Null())}
	result := srv.dispatch.executeRequest(request, true)
	if !result.hasResponse || result.nativeFailure || result.failed {
		t.Fatalf("该竿的出厂部件未走原有断竿响应: %+v", result)
	}
	decoded, _, err := proto.DecodeValue(result.body, 0, srv.Table())
	if err != nil || decoded.TypeID != 11028 || decoded.Field(1).Hex != part {
		t.Fatalf("断竿响应应指明经校验的出厂部件: %v %v", decoded, err)
	}
	if mustItem(t, db, factoryRod).Durability != 0 {
		t.Fatal("已裁决断裂的母竿没有落库")
	}
	logs, err := db.ListItemDamageLogs(ctx, uid, 10)
	if err != nil || len(logs) != 1 || logs[0].ItemGUID != factoryRod || logs[0].BrokenGUID != part {
		t.Fatalf("出厂部件与母竿的损坏记录不一致: %+v %v", logs, err)
	}
}

func TestTerminalComponentBreakRechecksAttachment(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	sess := newProbeSess(srv)
	rod, err := srv.CreateItem(ctx, sess.Userid, "tele_10012_tl16", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := srv.CreateItem(ctx, sess.Userid, "clas_20163", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.breakComponentTx(ctx, sess.Userid, rod.GUID, hook.GUID, catHook); err == nil {
		t.Fatal("事务接受了已卸下的组件")
	}
	if mustItem(t, db, hook.GUID).Durability != hook.Durability {
		t.Fatal("事务损坏了未装配的组件")
	}
}

// 断竿后果：竿子断梢——竿本体（0 耐久）和竿上的渔轮保留；主线只按上报放线
// 米数扣（扣剩的线留在断竿上），挂线配件全部丢失销毁，销毁件进 11028 移除
// 列表让客户端清掉线组 UI。
func TestRodBreakDestroysRigGearKeepsBrokenRod(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "tele_10012_tl16", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_40853", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := srv.CreateItem(ctx, uid, "clas_20163", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	bobber, err := srv.CreateItem(ctx, uid, "bob_25001", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, hook.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, bobber.GUID)
	setTestLineLengthM(t, db, line.GUID, 50)

	tree := rigRequestTree(t, proto.Guid(rod.GUID), proto.Guid(rod.GUID), proto.U32(1), proto.Null())
	hctx := &Ctx{Sess: sess, Main: 17, Sub: 9, Tree: tree,
		Body:     lineBreakBodyWithLineOut(12.5),
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()}
	body, ok := hDynamicRodBreak(hctx)
	if !ok || len(body) == 0 {
		t.Fatal("17/9 断竿未处理")
	}
	// 配件回背包的刷新必须排在 11028 之后，否则客户端删掉本地模型后要重登才看得见。
	if len(hctx.afterResponse) != 1 {
		t.Fatalf("断竿后应注册一次回包后的物品刷新，实得 %d", len(hctx.afterResponse))
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	removed := map[string]bool{}
	if f4 := dec.Fields[4].Val; f4 != nil {
		for _, it := range f4.Items {
			removed[it.Hex] = true
		}
	}
	for _, guid := range []string{hook.GUID, bobber.GUID} {
		if row, _ := db.GetItemByGUID(ctx, guid); row != nil {
			t.Fatalf("断竿后 %s 应丢失销毁（官服口径），实得 %+v", guid, row)
		}
		if !removed[guid] {
			t.Fatalf("断竿移除列表缺少 %s", guid)
		}
	}
	// 主线只扣放出去的 13 米（12.5+0.5 取整）：物品保留、不进移除列表。
	if removed[line.GUID] {
		t.Fatalf("断竿不应把扣剩的主线放进移除列表: %+v", removed)
	}
	freshLine := mustItem(t, db, line.GUID)
	lineFields, err := FieldsFromJSON(freshLine.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if got := itemLengthField(freshLine, lineFields); got != 37 {
		t.Fatalf("断竿后主线应剩 37 米，实得 %d", got)
	}
	// 更新段要带线树，客户端才能看到扣剩的长度。
	updatedHasLine := false
	if f3 := dec.Fields[3].Val; f3 != nil {
		for _, it := range f3.Items {
			if containsGUID(deepGUIDs(it), line.GUID) {
				updatedHasLine = true
			}
		}
	}
	if !updatedHasLine {
		t.Fatal("断竿更新段应包含扣剩主线的物品树")
	}
	var projRows int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM rig_component WHERE rig_guid=?`, rod.GUID).Scan(&projRows); err != nil {
		t.Fatal(err)
	}
	if projRows != 1 {
		t.Fatalf("断竿后 rig_component 应只剩主线投影 1 行，实得 %d 行", projRows)
	}
	if got := rigSlotGUIDs(t, srv, rod.GUID)[catLine]; got != line.GUID {
		t.Fatalf("断竿后 catLine 投影应保留主线 %s，实得 %q", line.GUID, got)
	}
	freshRod := mustItem(t, db, rod.GUID)
	if freshRod.Durability != 0 {
		t.Fatalf("断竿耐久应归零: %v", freshRod.Durability)
	}
	rodFields, _ := FieldsFromJSON(freshRod.FieldsJSON)
	if i := fieldIdxByCS(freshRod.WireClass, csLine); i >= 0 && i < len(rodFields) && rodFields[i].GuidHex() != line.GUID {
		t.Fatal("断竿后竿 f3 应保留扣剩主线引用")
	}
	if _, hasHook := rigCompsOf(rigStoredF5(t, freshRod))[catHook]; hasHook {
		t.Fatal("断竿后竿 f5 不应残留组件")
	}
	logs, _ := db.ListItemDamageLogs(ctx, uid, 50)
	seen := map[string]int{}
	for _, entry := range logs {
		seen[entry.Event]++
	}
	if seen[damageEventRodBreak] != 1 {
		t.Fatalf("rod_break 记录=%d，期望 1", seen[damageEventRodBreak])
	}
	if seen[damageEventLostOnRodBreak] != 2 {
		t.Fatalf("lost_on_rod_break 记录=%d，期望 2（钩+漂）", seen[damageEventLostOnRodBreak])
	}
	if seen[damageEventLineBreak] != 1 {
		t.Fatalf("line_break 记录=%d，期望 1（扣 13 米）", seen[damageEventLineBreak])
	}
}

// 断竿不可抛投（回归：客户端不理会 17/7 拒取仍持竿直发 14/2，
// 线上日志确认断竿 7 秒后仍抛竿成功）。
func TestBrokenRodCannotCast(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "tele_10012_tl16", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)

	// 健康竿：14/1 正常返回 158 带竿 GUID。
	prep := rigRequestTree(t, proto.Guid(rod.GUID))
	prepBody, ok := hCastPrepare(&Ctx{Sess: sess, Main: 14, Sub: 1, Tree: prep,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()})
	if !ok || len(prepBody) == 0 {
		t.Fatal("健康竿 14/1 未处理")
	}
	dec, _, err := proto.DecodeValue(prepBody, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	healthyF0 := dec.Field(0)
	if got := healthyF0.GuidHex(); got != rod.GUID {
		t.Fatalf("健康竿 158.f0=%s，期望 %s", got, rod.GUID)
	}

	// 断竿后：14/1 返回空 GUID。
	if err := db.SetItemDurability(ctx, rod.GUID, 0); err != nil {
		t.Fatal(err)
	}
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.GearGuid = rod.GUID
	st.mu.Unlock()
	prepBody2, ok := hCastPrepare(&Ctx{Sess: sess, Main: 14, Sub: 1, Tree: prep,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()})
	if !ok || len(prepBody2) == 0 {
		t.Fatal("断竿 14/1 未处理")
	}
	dec2, _, err := proto.DecodeValue(prepBody2, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	brokenF0 := dec2.Field(0)
	if got := brokenF0.GuidHex(); got != zeroGUID {
		t.Fatalf("断竿 14/1 应返回空 GUID，实得 %s", got)
	}
	st.mu.Lock()
	gearNow := st.GearGuid
	st.mu.Unlock()
	if gearNow != "" {
		t.Fatalf("断竿 14/1 后 GearGuid 应清空，实得 %s", shortGUID(gearNow))
	}

	// 断竿直发 14/2：回失败，不进入抛竿状态。
	st.mu.Lock()
	st.GearGuid = rod.GUID
	st.mu.Unlock()
	castTree := proto.Args524(proto.Guid(rod.GUID))
	capture := &ctxReplyCapture{}
	body3, ok := hCast(&Ctx{Sess: sess, Main: 14, Sub: 2, Tree: castTree,
		DecodeOK: true, ExpectResponse: true, replyCapture: capture, Log: logging.Discard()})
	if !ok || body3 != nil {
		t.Fatalf("断竿 14/2 应回失败且无成功体: ok=%v body=%v", ok, body3 != nil)
	}
	st.mu.Lock()
	castActive := st.CastActive
	st.mu.Unlock()
	if castActive {
		t.Fatal("断竿 14/2 不得进入抛竿状态")
	}
}

// 断竿不可组装配件（回归：此前只拦"耐久 0 配件上竿"，没拦"往断竿上装配件"）。
func TestBrokenRodRejectsComponentMount(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "tele_10012_tl16", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_40853", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetItemDurability(ctx, rod.GUID, 0); err != nil {
		t.Fatal(err)
	}
	res := srv.rigSetComponent(ctx, uid, rod.GUID, catLine, line.GUID, 0,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true})
	if res == nil || !res.Rejected {
		t.Fatalf("断竿应禁止装配线组: %+v", res)
	}
	if rows, _ := db.SQL.Query(`SELECT COUNT(*) FROM rig_component WHERE rig_guid=?`, rod.GUID); rows != nil {
		var n int
		_ = rows.Scan(&n)
		_ = rows.Close()
		if n != 0 {
			t.Fatalf("断竿装配被拒后不应有 rig_component 残留: %d", n)
		}
	}
	// 修理后恢复可装。
	if err := db.SetItemDurability(ctx, rod.GUID, 1); err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	if res := mountGearForTest(t, srv, sess, rod.GUID, line.GUID); res == nil {
		t.Fatal("修理后应恢复装配")
	}
}

func TestRodBreakKeepsReelMounted(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rod, err := srv.CreateItem(ctx, uid, "spin_10001_S60L", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	reel, err := srv.CreateItem(ctx, uid, "spin_5820", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15249", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, reel.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)

	outcome, err := srv.breakRodTx(ctx, uid, rod.GUID, rod.GUID, 0)
	if err != nil {
		t.Fatal(err)
	}
	// 官服口径：断竿时轮子没坏，仍挂在竿上；未上报放线米数时主线一米不扣，
	// 整卷保留在竿上（可 17/3 拆下换竿）。
	if slices.Contains(outcome.RemovedGUIDs, reel.GUID) {
		t.Fatalf("断竿不应把渔轮移出钓组: %+v", outcome.RemovedGUIDs)
	}
	if slices.Contains(outcome.RemovedGUIDs, line.GUID) {
		t.Fatalf("断竿不应把主线移出钓组: %+v", outcome.RemovedGUIDs)
	}
	fresh, err := db.GetItemByGUID(ctx, rod.GUID)
	if err != nil || fresh == nil {
		t.Fatal(err)
	}
	fields, err := FieldsFromJSON(fresh.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if idx := fieldIdxByCS(fresh.WireClass, csReel); idx < 0 || idx >= len(fields) || fields[idx].GuidHex() != reel.GUID {
		t.Fatalf("断竿后竿实例应仍引用渔轮 %s", reel.GUID)
	}
	if idx := fieldIdxByCS(fresh.WireClass, csLine); idx < 0 || idx >= len(fields) || fields[idx].GuidHex() != line.GUID {
		t.Fatalf("断竿后竿实例应仍引用主线 %s", line.GUID)
	}
	mountedLine, err := db.GetItemByGUID(ctx, line.GUID)
	if err != nil || mountedLine == nil {
		t.Fatalf("断竿后主线应保留: %v", err)
	}
	if mountedLine.Container != "rig" || mountedLine.RigGUID != rod.GUID {
		t.Fatalf("主线应仍挂在断竿上: %+v", mountedLine)
	}
	mounted, err := db.GetItemByGUID(ctx, reel.GUID)
	if err != nil || mounted == nil {
		t.Fatal(err)
	}
	if mounted.Container != "rig" || mounted.RigGUID != rod.GUID {
		t.Fatalf("渔轮应仍挂在断竿上: %+v", mounted)
	}
	if mounted.Durability != 1 {
		t.Fatalf("断竿不应伤到渔轮耐久: %v", mounted.Durability)
	}
}

// setTestLineLengthM 把测试用鱼线实例的剩余长度改写成指定米数。
func setTestLineLengthM(t *testing.T, db *store.DB, guid string, meters int64) {
	t.Helper()
	ctx := context.Background()
	row, err := db.GetItemByGUID(ctx, guid)
	if err != nil || row == nil {
		t.Fatalf("读取鱼线失败: %v", err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !setItemLengthField(row, fields, meters) {
		t.Fatal("设置鱼线长度失败")
	}
	fj, err := FieldsToJSON(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateItemFields(ctx, guid, fj, row.Qty, row.Durability, row.Workmanship, row.RigGUID); err != nil {
		t.Fatal(err)
	}
}

// TestCutLineLosesReportedMetersAndTerminalTackle 回归：17/10 rod/cutline 过去
// 只回一个容器快照，既不扣主线长度也不删钩组，实机表现为"按 X 线没断、饵还在"。
func TestCutLineLosesReportedMetersAndTerminalTackle(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15095", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	leader, err := srv.CreateItem(ctx, uid, "fc_leader_40048", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := srv.CreateItem(ctx, uid, "clas_20163", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	bait, err := srv.CreateItem(ctx, uid, "worm_01", "backpack", 3)
	if err != nil {
		t.Fatal(err)
	}
	bobber, err := srv.CreateItem(ctx, uid, "bob_25001", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	baitQtyBeforeMount := srv.stackCountOf(ctx, bait)
	for _, item := range []string{line.GUID, leader.GUID, hook.GUID, bait.GUID, bobber.GUID} {
		mountGearForTest(t, srv, sess, rod.GUID, item)
	}
	compsBefore := rigSlotGUIDs(t, srv, rod.GUID)
	if compsBefore[catLine] != line.GUID {
		t.Fatalf("竿上主线应为 %s，实得 %s", line.GUID, compsBefore[catLine])
	}
	// 浮钓会把整包饵装进钩饵子实例，原背包堆随之删除。
	baitMounted := compsBefore[catBait]
	if baitMounted == "" {
		t.Fatal("浮钓装配后应存在钩饵子实例")
	}
	setTestLineLengthM(t, db, line.GUID, 50)

	// 客户端上报本次放出 12 米。
	tree := rigRequestTree(t, proto.Guid(rod.GUID), proto.U32(12))
	hctx := &Ctx{Sess: sess, Main: 17, Sub: 10, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()}
	body, ok := hRodCutLine(hctx)
	if !ok || len(body) == 0 {
		t.Fatal("17/10 剪线未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if dec.Class != "dlbahppbdad" || dec.TypeID != 800 {
		t.Fatalf("17/10 响应=%s/%d，期望 dlbahppbdad/800", dec.Class, dec.TypeID)
	}
	if reason := srv.dispatch.checkRegisteredResponse(17, 10, body, srv.Table()); reason != "" {
		t.Fatalf("17/10 合同守卫误拒: %s", reason)
	}
	// 删除与刷新推送必须排在 800 回包之后，否则客户端本地物品模型不更新。
	if len(hctx.afterResponse) != 1 {
		t.Fatalf("剪线后应注册一次回包后的物品刷新，实得 %d", len(hctx.afterResponse))
	}
	if got := lineLengthOfTestItem(t, srv, line.GUID); got != 38 {
		t.Fatalf("剪线 12m 后主线长度=%d，期望 38", got)
	}
	for _, guid := range []string{leader.GUID, hook.GUID, bobber.GUID} {
		if row, _ := db.GetItemByGUID(ctx, guid); row != nil {
			t.Fatalf("剪线后 %s 应随线丢失", guid)
		}
	}
	// 挂上钩组的饵是从来源堆拆出的一份；剪线删除钩上子实例，
	// 来源堆仍留在背包，数量保持装配后的值。
	f1 := dec.Fields[1].Val
	if f1 == nil || !containsGUID(deepGUIDs(f1), bait.GUID) {
		t.Fatalf("剪线回包 f1 缺少来源饵堆 %s", bait.GUID)
	}
	var baitTree *proto.Value
	for _, item := range f1.Items {
		idx := fieldIdxByCS(item.Class, csGUID)
		if idx >= 0 && fieldGuidHex(item, idx) == bait.GUID {
			baitTree = item
			break
		}
	}
	if baitTree == nil {
		t.Fatalf("剪线回包 f1 没有来源饵堆的顶层树 %s", bait.GUID)
	}
	if got := baitDigTreeQuantity(t, baitTree); got != baitQtyBeforeMount-1 {
		t.Fatalf("剪线回包饵堆数量=%d，期望 %d", got, baitQtyBeforeMount-1)
	}
	// 挂上钩组的饵是从整堆里拆出来的一份，剪线只损失装上去的那一份。
	for slot, guid := range compsBefore {
		if guid == "" || slot == catBait || !lineBreakTerminalCategories[slot] {
			continue
		}
		if row, _ := db.GetItemByGUID(ctx, guid); row != nil {
			t.Fatalf("剪线后槽位 %d 的 %s 应随线丢失", slot, guid)
		}
	}
	if row, _ := db.GetItemByGUID(ctx, baitMounted); row != nil {
		t.Fatalf("剪线后钩上饵子实例应删除: %+v", row)
	}
	source, err := db.GetItemByGUID(ctx, bait.GUID)
	if err != nil || source == nil || source.Container != "backpack" {
		t.Fatalf("剪线后来源饵堆应留在背包: %+v %v", source, err)
	} else if got := srv.stackCountOf(ctx, source); got != baitQtyBeforeMount-1 {
		t.Fatalf("剪线后来源饵数量=%d，期望 %d", got, baitQtyBeforeMount-1)
	}
	freshRod := mustItem(t, db, rod.GUID)
	rodFields, _ := FieldsFromJSON(freshRod.FieldsJSON)
	if i := fieldIdxByCS(freshRod.WireClass, csLine); i >= 0 && i < len(rodFields) && rodFields[i].GuidHex() != line.GUID {
		t.Fatal("剪线保留主线，竿 f3 应仍指向主线")
	}
	if comps := rigSlotGUIDs(t, srv, rod.GUID); comps[catLine] != line.GUID {
		t.Fatal("剪线保留主线，rig_component 应仍挂主线")
	}
	if _, hasHook := rigCompsOf(rigStoredF5(t, freshRod))[catHook]; hasHook {
		t.Fatal("剪线后竿 f5 不应残留鱼钩")
	}

	logs, err := db.ListItemDamageLogs(ctx, uid, 100)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, entry := range logs {
		seen[entry.Event]++
	}
	if seen[damageEventLineCut] != 1 {
		t.Fatalf("line_cut 记录=%d，期望 1；全部=%v", seen[damageEventLineCut], seen)
	}
	if seen[damageEventLostOnLineCut] < 4 {
		t.Fatalf("lost_on_line_cut 记录=%d，期望至少 4（前导+钩+饵+漂）；全部=%v", seen[damageEventLostOnLineCut], seen)
	}
	if seen[damageEventLineBreak] != 0 {
		t.Fatalf("剪线不能写 line_break；全部=%v", seen)
	}
	var lineBreaks int64
	if err := db.SQL.QueryRow(`SELECT value FROM player_counters WHERE uid=? AND key='line_breaks'`, uid).Scan(&lineBreaks); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	if lineBreaks != 0 {
		t.Fatalf("主动剪线不应计入断线次数: %d", lineBreaks)
	}
}

// TestCutLineExhaustsLineAndDeletesIt 剪掉的米数超过主线剩余长度时，
// 主线整条消失并断开竿上的引用，同时留 line_exhausted 记录。
func TestCutLineExhaustsLineAndDeletesIt(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15095", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := srv.CreateItem(ctx, uid, "clas_20163", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, hook.GUID)
	setTestLineLengthM(t, db, line.GUID, 5)

	tree := rigRequestTree(t, proto.Guid(rod.GUID), proto.U32(11))
	hctx := &Ctx{Sess: sess, Main: 17, Sub: 10, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()}
	if body, ok := hRodCutLine(hctx); !ok || len(body) == 0 {
		t.Fatal("17/10 剪线未处理")
	}
	if row, _ := db.GetItemByGUID(ctx, line.GUID); row != nil {
		t.Fatal("主线用完应整条删除")
	}
	if row, _ := db.GetItemByGUID(ctx, hook.GUID); row != nil {
		t.Fatal("剪线后鱼钩应随线丢失")
	}
	if comps := rigSlotGUIDs(t, srv, rod.GUID); comps[catLine] != "" {
		t.Fatalf("剪线删线后 rig_component 仍挂 %s", comps[catLine])
	}
	freshRod := mustItem(t, db, rod.GUID)
	rodFields, _ := FieldsFromJSON(freshRod.FieldsJSON)
	if i := fieldIdxByCS(freshRod.WireClass, csLine); i >= 0 && i < len(rodFields) && rodFields[i].GuidHex() != "" {
		t.Fatal("主线删除后竿 f3 应清空引用")
	}
	logs, _ := db.ListItemDamageLogs(ctx, uid, 100)
	seen := map[string]int{}
	for _, entry := range logs {
		seen[entry.Event]++
	}
	if seen[damageEventLineCut] != 1 {
		t.Fatalf("line_cut 记录=%d，期望 1；全部=%v", seen[damageEventLineCut], seen)
	}
	if seen[damageEventLineExhausted] != 1 {
		t.Fatalf("line_exhausted 记录=%d，期望 1；全部=%v", seen[damageEventLineExhausted], seen)
	}
}

// TestCutLineWithoutCastKeepsTackle 没有放线时（放线米数为 0 且不在钓组入水状态）
// 不接受剪线，避免误触把整套钩组清空。
func TestCutLineWithoutCastKeepsTackle(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15095", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := srv.CreateItem(ctx, uid, "clas_20163", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, hook.GUID)
	setTestLineLengthM(t, db, line.GUID, 50)

	tree := rigRequestTree(t, proto.Guid(rod.GUID), proto.U32(0))
	hctx := &Ctx{Sess: sess, Main: 17, Sub: 10, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()}
	body, ok := hRodCutLine(hctx)
	if !ok || len(body) == 0 {
		t.Fatal("17/10 空放线剪线未回快照")
	}
	if got := lineLengthOfTestItem(t, srv, line.GUID); got != 50 {
		t.Fatalf("未放线时主线长度被改动: %d", got)
	}
	if row, _ := db.GetItemByGUID(ctx, hook.GUID); row == nil {
		t.Fatal("未放线时鱼钩不应消失")
	}
	logs, _ := db.ListItemDamageLogs(ctx, uid, 100)
	for _, entry := range logs {
		if entry.Event == damageEventLineCut || entry.Event == damageEventLostOnLineCut {
			t.Fatalf("未放线时不应写剪线记录: %+v", entry)
		}
	}
}

// TestCutLineWithZeroLengthRejectedInWater 防止异常零米报文在入水状态清空钩组。
func TestCutLineWithZeroLengthRejectedInWater(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15095", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := srv.CreateItem(ctx, uid, "clas_20163", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, hook.GUID)
	setTestLineLengthM(t, db, line.GUID, 50)
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.CastActive = true
	st.InWater = true
	st.mu.Unlock()

	tree := rigRequestTree(t, proto.Guid(rod.GUID), proto.U32(0))
	hctx := &Ctx{Sess: sess, Main: 17, Sub: 10, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()}
	body, ok := hRodCutLine(hctx)
	if !ok || len(body) == 0 {
		t.Fatal("17/10 零米剪线未回快照")
	}
	if got := lineLengthOfTestItem(t, srv, line.GUID); got != 50 {
		t.Fatalf("零米剪线不应改变主线长度，实得 %d", got)
	}
	if row, _ := db.GetItemByGUID(ctx, hook.GUID); row == nil {
		t.Fatal("零米剪线不应删除鱼钩")
	}
}

// TestMainLineLossCategoriesCoverRigComponents 主线断/剪的丢失集合必须来自槽位表，
// 铅坠、食笼、底钓组主体、铅芯前导这些挂在线上的东西不能漏。
func TestMainLineLossCategoriesCoverRigComponents(t *testing.T) {
	t.Parallel()
	for _, cat := range []int64{catHook, catLead, catLeaderLock, catWireLeader, catLure,
		catBait, catFloat, 29, 31, 33, 35, 44, 47, 57, 18, 19, 20, 30, 38, 46} {
		if !mainLineLossCategories[cat] {
			t.Errorf("品类 %d 挂在线上，主线断了应随线丢失", cat)
		}
	}
	for _, cat := range []int64{catReel, catLine, 36, 56} {
		if mainLineLossCategories[cat] {
			t.Errorf("品类 %d 属于竿体/渔轮/主线本身，不应随线丢失", cat)
		}
	}
}

// TestCutLineBottomRigDeletesSinkerAndReportsRemovals 水底（底钓）钓组剪线：
// 沉子和钩饵都要随线消失，回包按 800 的三段语义把移除 GUID 放进 f2，
// 而不是把整个背包塞进 f0（那会让配件重新入库甚至落进背包）。
func TestCutLineBottomRigDeletesSinkerAndReportsRemovals(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "feeder_10053_FD100", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15095", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sinker, err := srv.CreateItem(ctx, uid, "pyrsinker_30026", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := srv.CreateItem(ctx, uid, "clas_20163", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, sinker.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, hook.GUID)
	compsBefore := rigSlotGUIDs(t, srv, rod.GUID)
	if compsBefore[catLead] != sinker.GUID && compsBefore[29] != sinker.GUID {
		t.Fatalf("沉子未挂上钓组: %+v", compsBefore)
	}
	setTestLineLengthM(t, db, line.GUID, 30)

	tree := rigRequestTree(t, proto.Guid(rod.GUID), proto.U32(9))
	hctx := &Ctx{Sess: sess, Main: 17, Sub: 10, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()}
	body, ok := hRodCutLine(hctx)
	if !ok || len(body) == 0 {
		t.Fatal("17/10 剪线未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if dec.Class != "dlbahppbdad" || dec.TypeID != 800 {
		t.Fatalf("17/10 响应=%s/%d", dec.Class, dec.TypeID)
	}
	// f0 新增段必须为空：剪线不产生新物品。
	if f0 := dec.Fields[0].Val; f0 != nil && len(f0.Items) > 0 {
		t.Fatalf("剪线回包 f0 应为空，实得 %d 个新增物品", len(f0.Items))
	}
	// f2 是移除清单，沉子和钩都要在里面。
	removed := map[string]bool{}
	if f2 := dec.Fields[2].Val; f2 != nil {
		for _, it := range f2.Items {
			removed[it.Hex] = true
		}
	}
	for _, guid := range []string{sinker.GUID, hook.GUID} {
		if !removed[guid] {
			t.Fatalf("剪线移除清单缺少 %s（沉子/钩应随线删除）", guid)
		}
		if row, _ := db.GetItemByGUID(ctx, guid); row != nil {
			t.Fatalf("剪线后 %s 应被删除，实得 container=%s rig=%s", guid, row.Container, row.RigGUID)
		}
	}
	// 主线和渔轮留在竿上，主线扣 9 米。
	if got := lineLengthOfTestItem(t, srv, line.GUID); got != 21 {
		t.Fatalf("剪线 9m 后主线长度=%d，期望 21", got)
	}
	if row, _ := db.GetItemByGUID(ctx, line.GUID); row == nil || row.Container != "rig" {
		t.Fatalf("主线应仍挂在竿上: %+v", row)
	}
	// 竿和主线的更新走 f1。
	if f1 := dec.Fields[1].Val; f1 == nil || len(f1.Items) < 2 {
		t.Fatalf("剪线回包 f1 应有竿+主线更新，实得 %+v", f1)
	}
	if reason := srv.dispatch.checkRegisteredResponse(17, 10, body, srv.Table()); reason != "" {
		t.Fatalf("17/10 合同守卫误拒: %s", reason)
	}
}

// TestCutLineSplitChildRefreshesSourceStack 覆盖逐份挂饵的旧存档：
// 钩上只有 1 份饵，剩余饵仍在原背包堆；剪线后客户端必须收到来源堆的新数量。
func TestCutLineSplitChildRefreshesSourceStack(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	rigType := int64(5)
	if !srv.rigItemMutate(ctx, rod.GUID, nil, nil, &rigType) {
		t.Fatal("设置钓组类型失败")
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15095", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	bait, err := srv.CreateItem(ctx, uid, "worm_01", "backpack", 3)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, bait.GUID)
	setTestLineLengthM(t, db, line.GUID, 20)

	comps := rigSlotGUIDs(t, srv, rod.GUID)
	mountedBait := comps[catBait]
	if mountedBait == "" || mountedBait == bait.GUID {
		t.Fatalf("逐份挂饵应拆出独立子实例，实得 %s", mountedBait)
	}
	source, err := db.GetItemByGUID(ctx, bait.GUID)
	if err != nil || source == nil {
		t.Fatalf("来源饵堆应保留: %v", err)
	}
	wantQty := srv.stackCountOf(ctx, source)

	tree := rigRequestTree(t, proto.Guid(rod.GUID), proto.U32(2))
	hctx := &Ctx{Sess: sess, Main: 17, Sub: 10, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()}
	body, ok := hRodCutLine(hctx)
	if !ok || len(body) == 0 {
		t.Fatal("17/10 剪线未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if row, _ := db.GetItemByGUID(ctx, mountedBait); row != nil {
		t.Fatalf("剪线后挂载饵子实例应删除: %+v", row)
	}
	if row, _ := db.GetItemByGUID(ctx, bait.GUID); row == nil || row.Container != "backpack" {
		t.Fatalf("来源饵堆应留在背包: %+v", row)
	} else if got := srv.stackCountOf(ctx, row); got != wantQty {
		t.Fatalf("来源饵堆数量=%d，期望保持 %d", got, wantQty)
	}
	if f1 := dec.Fields[1].Val; f1 == nil || !containsGUID(deepGUIDs(f1), bait.GUID) {
		t.Fatalf("剪线回包 f1 缺少来源饵堆 %s", bait.GUID)
	}
}
