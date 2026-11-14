package gameserver

// 海钓链斗式（57 = rig_MarineBottomPaternoster）装配回归。
//
// 实机证据（logs/gameserver.log 2026-11-14 17:13–18:08，杆 914e9495）：
//
//	17:13:15 竿钓组类型更新 rig_type=57
//	17:13:17 装配 slot=29 extra=200   ← 震子线
//	17:13:20 装配 slot=13 extra=150   ← 第一组海引线
//	17:13:25 装配 slot=12 extra=250   ← 第一组海钩
//	18:02:48 装配 slot=17 extra=300   ← 第一组拟饵（客户端索引 300）
//	18:02:59 装配 slot=17 extra=327   ← 吸引成分 Fire stick（客户端索引 327）
//
// 旧模板把拟饵族(17)只声明成 325/326/327（那是吸引成分的槽位），
// canonicalIndexForOrdinal 于是把拟饵的 300/301/302 改写成 325~327：
// 装拟饵先把吸引成分挤回背包，三个拟饵又在同一组槽位里互相顶掉，
// 而客户端仍按自己的 300~302 渲染，看到的是「拟饵装不上/不显示」。
// 13 与 12 的旧问题同理（13/150 被改写成 175、12/250 被改写成 252）。

import (
	"context"
	"testing"
)

// TestPaternosterTemplateHasIndependentBranchSlots 锁定模板声明：三组支线
// 各自的引线/钩索引互不重复，拟饵(300~302)与吸引成分(325~327)分组独立，
// 钩与拟饵不得再共用 252。
func TestPaternosterTemplateHasIndependentBranchSlots(t *testing.T) {
	want := map[int64][]int64{
		catLead: {175, 150, 151, 152},
		29:      {200},
		catLure: {300, 301, 302, 325, 326, 327},
		catHook: {250, 251, 252},
	}
	for cat, expected := range want {
		got := slotIndicesFor(57, cat)
		if len(got) != len(expected) {
			t.Fatalf("rig57 品类 %d 槽位 = %v，期望 %v", cat, got, expected)
		}
		for i := range expected {
			if got[i] != expected[i] {
				t.Fatalf("rig57 品类 %d 槽位 = %v，期望 %v", cat, got, expected)
			}
		}
	}
	hookIdx := map[int64]bool{}
	for _, idx := range slotIndicesFor(57, catHook) {
		hookIdx[idx] = true
	}
	for _, idx := range slotIndicesFor(57, catLure) {
		if hookIdx[idx] {
			t.Fatalf("rig57 钩位 %d 仍被拟饵共用：两者会互相顶掉", idx)
		}
	}
}

// paternosterTestRod 建一根船竿并切到链斗式。
func paternosterTestRod(t *testing.T, s *Server) string {
	t.Helper()
	rod, err := s.CreateItem(context.Background(), 1000001, "boatr_10222_SBH80_100", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !s.rigSetType(context.Background(), 1000001, rod.GUID, 57) {
		t.Fatal("rigSetType(57) 失败")
	}
	return rod.GUID
}

// paternosterMount 按客户端上报的槽位索引装一件组件，返回装配后的 f5 组件表。
func paternosterMount(t *testing.T, s *Server, rod string, slot int64, itemID string, extra int64) {
	t.Helper()
	ctx := context.Background()
	item, err := s.CreateItem(ctx, 1000001, itemID, "backpack", 1)
	if err != nil {
		t.Fatalf("造 %s 失败: %v", itemID, err)
	}
	res := s.rigSetComponent(ctx, 1000001, rod, slot, item.GUID, extra,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true})
	if res.Rejected || res.MountedGUID == "" {
		t.Fatalf("装配 slot=%d extra=%d 被拒: %+v", slot, extra, res)
	}
}

// paternosterComps 读取当前 f5 组件表。
func paternosterComps(t *testing.T, s *Server, rod string) map[int64]rigComp {
	t.Helper()
	return s.storedRigComponents(context.Background(), rod)
}

func TestPaternosterMountKeepsClientReportedIndices(t *testing.T) {
	s, _ := newRigTestServer(t)
	rod := paternosterTestRod(t, s)

	// 与实机日志同序：震子线、三组引线/钩、三个吸引成分，最后三个拟饵。
	paternosterMount(t, s, rod, 29, "pyrsinker_30026", 200)
	paternosterMount(t, s, rod, 13, "sh_leader_40815", 150)
	paternosterMount(t, s, rod, 12, "clas_20163", 250)
	paternosterMount(t, s, rod, 17, "fire_stick_003", 325)
	paternosterMount(t, s, rod, 13, "sh_leader_40815", 151)
	paternosterMount(t, s, rod, 12, "clas_20163", 251)
	paternosterMount(t, s, rod, 17, "fire_tube_006", 326)
	paternosterMount(t, s, rod, 13, "sh_leader_40815", 152)
	paternosterMount(t, s, rod, 12, "clas_20163", 252)
	paternosterMount(t, s, rod, 17, "color_bead_006", 327)
	paternosterMount(t, s, rod, 17, "shr_21107_1", 300)
	paternosterMount(t, s, rod, 17, "shr_21108_1", 301)
	paternosterMount(t, s, rod, 17, "shr_21109_1", 302)

	comps := paternosterComps(t, s, rod)
	byIndex := map[int64]map[int64]int{}
	for _, c := range comps {
		if byIndex[c.Category] == nil {
			byIndex[c.Category] = map[int64]int{}
		}
		byIndex[c.Category][c.Extra]++
	}
	want := map[int64][]int64{
		29:      {200},
		catLead: {150, 151, 152},
		catHook: {250, 251, 252},
		catLure: {300, 301, 302, 325, 326, 327},
	}
	for cat, idxs := range want {
		for _, idx := range idxs {
			if n := byIndex[cat][idx]; n != 1 {
				t.Fatalf("品类 %d 槽位 %d 应有且仅有 1 条组件，实得 %d 条；全部 = %v",
					cat, idx, n, byIndex[cat])
			}
		}
	}
	// 装三组海引线后震子线必须仍在（旧模板下会被自动切类型退回背包）。
	if n := byIndex[29][200]; n != 1 {
		t.Fatalf("装完海引线后震子线丢了：29 槽位 = %v", byIndex[29])
	}
}

// TestPaternosterLureDoesNotEjectAttractant 复现实机现象「装第一个拟饵，
// 吸引成分被取下、拟饵还装不上」：拟饵(300)与吸引成分(325)是两组独立槽位，
// 装拟饵不得改写索引、也不得把吸引成分退回背包。
func TestPaternosterLureDoesNotEjectAttractant(t *testing.T) {
	s, db := newRigTestServer(t)
	ctx := context.Background()
	rod := paternosterTestRod(t, s)

	paternosterMount(t, s, rod, 17, "fire_stick_003", 325)
	paternosterMount(t, s, rod, 17, "shr_21107_1", 300)

	comps := paternosterComps(t, s, rod)
	itemAt := func(extra int64) string {
		for _, c := range comps {
			if c.Category == catLure && c.Extra == extra {
				row, err := db.GetItemByGUID(ctx, c.GUID)
				if err != nil || row == nil {
					t.Fatalf("槽位 %d 的组件 %s 不在库中", extra, shortGUID(c.GUID))
				}
				if row.Container != "rig" || row.RigGUID != rod {
					t.Fatalf("槽位 %d 的组件 %s 没挂在杆上：container=%s rig=%s",
						extra, shortGUID(c.GUID), row.Container, shortGUID(row.RigGUID))
				}
				return row.ItemID
			}
		}
		return ""
	}
	if got := itemAt(325); got != "fire_stick_003" {
		t.Fatalf("吸引成分槽位 325 = %q，期望 fire_stick_003（被拟饵挤掉了）", got)
	}
	if got := itemAt(300); got != "shr_21107_1" {
		t.Fatalf("拟饵槽位 300 = %q，期望 shr_21107_1（拟饵没装上）", got)
	}

	// 第二、第三个拟饵各就各位，不能互相顶掉。
	paternosterMount(t, s, rod, 17, "shr_21108_1", 301)
	paternosterMount(t, s, rod, 17, "shr_21109_1", 302)
	comps = paternosterComps(t, s, rod)
	for extra, want := range map[int64]string{300: "shr_21107_1", 301: "shr_21108_1", 302: "shr_21109_1", 325: "fire_stick_003"} {
		if got := itemAt(extra); got != want {
			t.Fatalf("装完三个拟饵后槽位 %d = %q，期望 %q", extra, got, want)
		}
	}
}

// TestSwitchToPaternosterDropsStaleArmSlots 复现「第三组海钩未装配却出现」：
// 倒钓臂展开后的 1000+ 子槽属于旧钓组的运行时状态，切类型时必须退回背包。
func TestSwitchToPaternosterDropsStaleArmSlots(t *testing.T) {
	s, db := newRigTestServer(t)
	ctx := context.Background()
	rod, err := s.CreateItem(ctx, 1000001, "boatr_10222_SBH80_100", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !s.rigSetType(ctx, 1000001, rod.GUID, 50) {
		t.Fatal("rigSetType(50) 失败")
	}

	// rig50 上展开倒钓臂：主钩 250 + 三个 1000+ 钩位与拟饵子槽。
	paternosterMount(t, s, rod.GUID, 12, "clas_20163", 250)
	for _, extra := range []int64{1001, 1005, 1009} {
		paternosterMount(t, s, rod.GUID, 12, "clas_20163", extra)
	}
	for _, extra := range []int64{1002, 1006, 1010} {
		paternosterMount(t, s, rod.GUID, 17, "pilkerlures_2630", extra)
	}
	before := paternosterComps(t, s, rod.GUID)
	if n := len(rigCompKeysOfCategory(before, catHook)); n != 4 {
		t.Fatalf("切类型前钩组件 = %d，期望 4（主钩 + 三个倒钓臂钩位）", n)
	}

	// 切到链斗式，残留必须清干净，正常的 250 主钩保留。
	if !s.rigSetType(ctx, 1000001, rod.GUID, 57) {
		t.Fatal("rigSetType(57) 失败")
	}
	after := paternosterComps(t, s, rod.GUID)
	for _, c := range after {
		if hookSlotIndexNamespace(c.Category, c.Extra) {
			t.Fatalf("切到 rig57 后仍有倒钓臂残留子槽：cat=%d extra=%d guid=%s",
				c.Category, c.Extra, shortGUID(c.GUID))
		}
	}
	hooks := rigCompKeysOfCategory(after, catHook)
	if len(hooks) != 1 || after[hooks[0]].Extra != 250 {
		t.Fatalf("切到 rig57 后应只剩 250 主钩，实得 %d 条: %v", len(hooks), after)
	}
	// rig_component 派生表也不能留残留：它与 f5 一起下发给客户端。
	rows, err := db.SQL.Query(`SELECT slot, extra FROM rig_component WHERE rig_guid=? AND extra>=1000`, rod.GUID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var slot, extra int64
		if err := rows.Scan(&slot, &extra); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("rig_component 仍有倒钓臂残留：slot=%d extra=%d", slot, extra)
	}
}
