package gameserver

// 钓组 ↔ 挂饵方式联动回归（2026-11-05）：
// 反编译证明客户端对联动零过滤——安装方式弹窗（bbhjnacijjf）每次都用
// 153 的 (f1 钓组类型, f9 可选掩码, f8 选中值) 重建列表，钓组类型只用于
// 预览图命名。联动完全由服务端下发：f9 按钓组类型收窄（锚点 = 官方抓包
// 126），换钓组后旧选择不再属于新可选集时判为未设置（f8 渲染与挂饵份数
// 都走 savedBaitMount 同一读口）。

import (
	"testing"

	"rf4server/internal/proto"
)

func TestBaitMountAvailForRigTable(t *testing.T) {
	base := int64(126) // 官方抓包锚点：沉底六种基础挂法
	for _, rt := range []int64{12, 13, 14, 15, 16, 17, 18, 23} {
		if got := baitMountAvailForRig(rt, 0); got != base {
			t.Fatalf("钓组 %d 可选集 = %d，期望锚点 %d", rt, got, base)
		}
	}
	// 编排表（用户按官方游戏知识给定，2026-11-05）：
	hair := base | 128 | 256 | 512 | 1024 | 2048
	if got := baitMountAvailForRig(24, 0); got != hair {
		t.Fatalf("发丝钓组可选集 = %d，期望 %d", got, hair)
	}
	snowman := int64(64 | 128 | 256 | 512 | 4096 | 8192 | 16384 | 32768)
	if got := baitMountAvailForRig(25, 0); got != snowman {
		t.Fatalf("雪人钓组可选集 = %d，期望 %d", got, snowman)
	}
	popup := int64(baitMountPopupFamily)
	for _, rt := range []int64{26, 27, 28, 32} {
		if got := baitMountAvailForRig(rt, 0); got != popup {
			t.Fatalf("钓组 %d 可选集 = %d，期望 pop-up 族 %d", rt, got, popup)
		}
	}
	maggot := int64(1024|2048) | baitMountMaggotFamily
	if got := baitMountAvailForRig(29, 0); got != maggot {
		t.Fatalf("底蛆钓组可选集 = %d，期望 %d", got, maggot)
	}
	zig := int64(16384|32768) | baitMountFoamFamily
	if got := baitMountAvailForRig(34, 0); got != zig {
		t.Fatalf("Zig 钓组可选集 = %d，期望 %d", got, zig)
	}
	// 30/31 暂未编排，维持过渡值。
	for _, rt := range []int64{30, 31} {
		if got := baitMountAvailForRig(rt, 0); got != base|baitMountPopupFamily {
			t.Fatalf("钓组 %d 可选集 = %d，期望 %d", rt, got, base|baitMountPopupFamily)
		}
	}
	// 未知/非沉底钓组维持旧行为：沿用实例旧值，缺失退回全开。
	if got := baitMountAvailForRig(1, 0); got != defaultBaitMountAvail {
		t.Fatalf("浮钓钓组可选集 = %d，期望全开", got)
	}
	if got := baitMountAvailForRig(0, 0x1f80); got != 0x1f80 {
		t.Fatalf("未知钓组应沿用旧值，实得 %d", got)
	}
}

func TestBuildRig153LikeClampsToRigMask(t *testing.T) {
	// 模板带旧全开掩码 + pop-up 选中（换钓组前的残留）。
	tpl := buildRig153Full("rig_BottomHingedStiff", 27, nil, 100, 0, 16384, defaultBaitMountAvail)
	rebuilt := buildRig153Like(tpl, "rig_BottomSimple", 12, nil, 100, 0)
	if sel, avail := rigBaitMountsOf(rebuilt); sel != 0 || avail != 126 {
		t.Fatalf("切到普通沉底后 f8/f9 = %d/%d，期望 0/126", sel, avail)
	}
	// 同族切换：选择仍在可选集内则保留（Chod = 纯 pop-up 族）。
	kept := buildRig153Like(tpl, "rig_BottomChod", 28, nil, 100, 0)
	if sel, avail := rigBaitMountsOf(kept); sel != 16384 || avail != baitMountPopupFamily {
		t.Fatalf("切到 Chod 后应保留选择 f8/f9 = %d/%d，期望 16384/%d", sel, avail, baitMountPopupFamily)
	}
}

func TestRigSetTypeRebuildsBaitMountFields(t *testing.T) {
	s, db := newRigTestServer(t)
	ctx := t.Context()
	rod, err := s.CreateItem(ctx, 1000001, "feeder_10126_FD390", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	// 残留场景：旧版本按全开掩码落库的 pop-up 选择。
	if err := db.UpsertRodSetting(ctx, rod.GUID, baitMountSettingKey, "16384"); err != nil {
		t.Fatal(err)
	}
	if err := s.applyRigBaitMount(ctx, rod.GUID, 16384); err != nil {
		t.Fatal(err)
	}
	if !s.rigSetType(ctx, 1000001, rod.GUID, 12) {
		t.Fatal("rigSetType 失败")
	}
	// pop-up 选择不属于普通沉底的可选集：读取口判为未设置。
	if v, ok := s.savedBaitMount(ctx, rod.GUID); ok {
		t.Fatalf("换普通沉底后残留的 pop-up 选择应判为未设置，实得 %d/%v", v, ok)
	}
	row, err := db.GetItemByGUID(ctx, rod.GUID)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	fields, ferr := FieldsFromJSON(row.FieldsJSON)
	if ferr != nil {
		t.Fatal(ferr)
	}
	idx := fieldIdxByCS(row.WireClass, csRigObject)
	if idx < 0 || idx >= len(fields) {
		t.Fatal("竿实例缺钓组字段")
	}
	if sel, avail := rigBaitMountsOf(fields[idx].Val); sel != 0 || avail != 126 {
		t.Fatalf("实例 f8/f9 = %d/%d，期望 0/126", sel, avail)
	}
	if got := s.baitMountFor(ctx, rod.GUID); got != 0 {
		t.Fatalf("渲染 f8 应为 0，实得 %d", got)
	}
	// 换回 pop-up 钓组（26）：族内选择仍有效。
	if err := db.UpsertRodSetting(ctx, rod.GUID, baitMountSettingKey, "16384"); err != nil {
		t.Fatal(err)
	}
	if !s.rigSetType(ctx, 1000001, rod.GUID, 26) {
		t.Fatal("rigSetType 失败")
	}
	if v, ok := s.savedBaitMount(ctx, rod.GUID); !ok || v != 16384 {
		t.Fatalf("传统 Pop-up 属于 pop-up 族，换钓组不应失效，实得 %d/%v", v, ok)
	}
}

// TestItemToTreeSanitizesLegacyBaitMountFields 锁定渲染级联动：
// 存量竿的快照还存着联动上线前的全开掩码 f9 + 失效 f8 时，登录背包树
// （4/5，ItemToTree→rebuildRod）必须按当前钓组类型重算后下发，且不落库。
func TestItemToTreeSanitizesLegacyBaitMountFields(t *testing.T) {
	s, db := newRigTestServer(t)
	ctx := t.Context()
	rod, err := s.CreateItem(ctx, 1000001, "feeder_10126_FD390", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !s.rigSetType(ctx, 1000001, rod.GUID, 12) {
		t.Fatal("rigSetType 失败")
	}
	// 模拟旧版落库快照：全开掩码 + pop-up 残留选择。
	row, err := db.GetItemByGUID(ctx, rod.GUID)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	idx := fieldIdxByCS(row.WireClass, csRigObject)
	if idx < 0 || idx >= len(fields) {
		t.Fatal("竿实例缺钓组字段")
	}
	setRigBaitMountSel(fields[idx].Val, 16384)
	setRigBaitMountAvail(fields[idx].Val, defaultBaitMountAvail)
	s.saveItemFields(ctx, row, fields)

	fresh, err := db.GetItemByGUID(ctx, rod.GUID)
	if err != nil || fresh == nil {
		t.Fatal(err)
	}
	tree, err := s.ItemToTree(ctx, fresh)
	if err != nil || tree == nil || tree.Kind != proto.KindObj {
		t.Fatal("ItemToTree 失败: ", err)
	}
	rig := tree.Fields[5].Val
	if sel, avail := rigBaitMountsOf(rig); sel != 0 || avail != 126 {
		t.Fatalf("渲染树 f8/f9 = %d/%d，期望 0/126（全开掩码不得外泄）", sel, avail)
	}
	// 渲染只改 wire：库里的快照保持原样。
	after, err := db.GetItemByGUID(ctx, rod.GUID)
	if err != nil || after == nil {
		t.Fatal(err)
	}
	afterFields, err := FieldsFromJSON(after.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if sel, avail := rigBaitMountsOf(afterFields[idx].Val); sel != 16384 || avail != defaultBaitMountAvail {
		t.Fatalf("渲染不应落库，库内 f8/f9 = %d/%d，期望 16384/%d", sel, avail, defaultBaitMountAvail)
	}
}
