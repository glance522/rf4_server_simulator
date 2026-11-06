package gameserver

// 同名品类重复挂载回归：海钓竿换钓组类型后，旧类型的前导没有拆下来，
// f5 里留下两个 cat=13、extra=175 的组件。客户端的钓组槽位只有 175 一个，
// 两个组件叠在同一索引上，抛竿后整组被判无效并立即收回（表现为饵一入水
// 就回到杆上）。去重后同索引只保留一条，多余的退回背包。

import (
	"context"
	"testing"
)

// TestDuplicateRigIndexIsReturnedByProjection 验证历史重复索引在投影重建时收敛。
func TestDuplicateRigIndexIsReturnedByProjection(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "pilkr_10212_92-80190", "backpack", 1)
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	if !srv.rigSetType(ctx, uid, rod.GUID, 50) { // rig_MarinePilker：前导只有 175 一个槽
		t.Fatal("rigSetType 失败")
	}
	kept, err := srv.CreateItem(ctx, uid, "sh_leader_40815", "backpack", 1)
	if err != nil {
		t.Fatalf("造前导失败: %v", err)
	}
	extra, err := srv.CreateItem(ctx, uid, "sh_leader_40815", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	srv.rigSetComponent(ctx, uid, rod.GUID, 0, kept.GUID, 175,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true})

	// 构造旧版本/换类型留下的第二个同索引前导。
	row, err := db.GetItemByGUID(ctx, rod.GUID)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	idx := fieldIdxByCS(row.WireClass, csRigObject)
	name, rigType, ok := rigIdentity(fields[idx].Val)
	if !ok {
		t.Fatal("缺少钓组身份")
	}
	comps := rigCompsOf(fields[idx].Val)
	comps[rigCompKey(13, 1)] = rigComp{Category: 13, Ordinal: 1, GUID: extra.GUID, Extra: 175}
	fields[idx].Val = buildRig153Like(fields[idx].Val, name, rigType, comps, 100, 0)
	srv.saveItemFields(ctx, row, fields)
	if err := db.SetItemRigLink(ctx, extra.GUID, rod.GUID, 13); err != nil {
		t.Fatal(err)
	}

	if err := srv.reconcileRigProjection(ctx, rod.GUID); err != nil {
		t.Fatalf("投影重建失败: %v", err)
	}
	fresh, err := db.GetItemByGUID(ctx, rod.GUID)
	if err != nil {
		t.Fatal(err)
	}
	got := rigTestCategoryComps(t, rigStoredF5(t, fresh), 13)
	if len(got) != 1 || got[0].GUID != kept.GUID {
		t.Fatalf("重复前导未清理: %+v", got)
	}
	returned, err := db.GetItemByGUID(ctx, extra.GUID)
	if err != nil || returned == nil {
		t.Fatal(err)
	}
	if returned.Container != "backpack" || returned.RigGUID != "" {
		t.Fatalf("多余前导未退回背包: container=%q rig=%q", returned.Container, returned.RigGUID)
	}
}

// TestRigTypeSwitchDedupesSameIndex 验证换钓组类型时同索引组件被收敛。
func TestRigTypeSwitchDedupesSameIndex(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "pilkr_10212_92-80190", "backpack", 1)
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	first, err := srv.CreateItem(ctx, uid, "sh_leader_40815", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := srv.CreateItem(ctx, uid, "sh_leader_40815", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	srv.rigSetComponent(ctx, uid, rod.GUID, 0, first.GUID, 175,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true})

	// 手工制造两条同索引前导，模拟两个钓组类型各装过一次。
	row, err := db.GetItemByGUID(ctx, rod.GUID)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	idx := fieldIdxByCS(row.WireClass, csRigObject)
	name, rigType, _ := rigIdentity(fields[idx].Val)
	comps := rigCompsOf(fields[idx].Val)
	comps[rigCompKey(13, 1)] = rigComp{Category: 13, Ordinal: 1, GUID: second.GUID, Extra: 175}
	fields[idx].Val = buildRig153Like(fields[idx].Val, name, rigType, comps, 100, 0)
	srv.saveItemFields(ctx, row, fields)

	if !srv.rigSetType(ctx, uid, rod.GUID, 53) {
		t.Fatal("切换钓组类型失败")
	}
	fresh, err := db.GetItemByGUID(ctx, rod.GUID)
	if err != nil {
		t.Fatal(err)
	}
	got := rigTestCategoryComps(t, rigStoredF5(t, fresh), 13)
	if len(got) != 1 {
		t.Fatalf("换类型后同索引前导仍有 %d 条: %+v", len(got), got)
	}
	if got[0].GUID != first.GUID {
		t.Fatalf("两条都 target-valid 时应保留先装配的 first，实得 %+v", got)
	}
}

// TestRigDedupeAcrossMutuallyExclusiveCategories 复现审查 P2：rig57 模板把钩(12)
// 和拟饵(17)声明为同一槽位 252 的互斥替代项，两者同时挂载应被去重，而不是共存。
func TestRigDedupeAcrossMutuallyExclusiveCategories(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "pilkr_10212_92-80190", "backpack", 1)
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	if !srv.rigSetType(ctx, uid, rod.GUID, 57) {
		t.Fatal("切 rig57 失败")
	}
	hook, err := srv.CreateItem(ctx, uid, "clas_20163", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	lure, err := srv.CreateItem(ctx, uid, "pilkerlures_2630", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	// 手工注入同一槽位 252 的钩(12)和拟饵(17)。
	row, err := db.GetItemByGUID(ctx, rod.GUID)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	idx := fieldIdxByCS(row.WireClass, csRigObject)
	name, rigType, _ := rigIdentity(fields[idx].Val)
	comps := rigCompsOf(fields[idx].Val)
	comps[rigCompKey(12, 0)] = rigComp{Category: 12, Ordinal: 0, GUID: hook.GUID, Extra: 252}
	comps[rigCompKey(17, 0)] = rigComp{Category: 17, Ordinal: 0, GUID: lure.GUID, Extra: 252}
	fields[idx].Val = buildRig153Like(fields[idx].Val, name, rigType, comps, 100, 0)
	srv.saveItemFields(ctx, row, fields)
	if err := db.SetItemRigLink(ctx, hook.GUID, rod.GUID, 12); err != nil {
		t.Fatal(err)
	}
	if err := db.SetItemRigLink(ctx, lure.GUID, rod.GUID, 17); err != nil {
		t.Fatal(err)
	}

	if err := srv.reconcileRigProjection(ctx, rod.GUID); err != nil {
		t.Fatalf("投影重建失败: %v", err)
	}
	fresh, err := db.GetItemByGUID(ctx, rod.GUID)
	if err != nil {
		t.Fatal(err)
	}
	stored := rigCompsOf(rigStoredF5(t, fresh))
	count := 0
	for _, c := range stored {
		if (c.Category == 12 || c.Category == 17) && c.Extra == 252 {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("钩/拟饵同槽 252 应去重为 1 条，实得 %d 条: %+v", count, stored)
	}
}

// TestDedupeRigComponentsKeepsTargetValid 直接验证去重选择逻辑：
// 同品类两个组件规范化到同一索引时，保留 Extra 已是模板合法索引的那个，
// 并通过收集器上报退回去向（审查补充建议：退回物品必须能进响应快照）。
func TestDedupeRigComponentsKeepsTargetValid(t *testing.T) {
	srv, _ := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	old, err := srv.CreateItem(ctx, uid, "sh_leader_40815", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := srv.CreateItem(ctx, uid, "sh_leader_40815", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	// 先把旧前导挂到竿容器下，模拟"已挂载后触发去重"的真实场景。
	if err := srv.DB.SetItemRigLink(ctx, old.GUID, "10000000000000000000000000000000", 13); err != nil {
		t.Fatal(err)
	}
	comps := map[int64]rigComp{
		rigCompKey(13, 0): {Category: 13, Ordinal: 0, GUID: old.GUID, Extra: 150},
		rigCompKey(13, 1): {Category: 13, Ordinal: 1, GUID: cur.GUID, Extra: 175},
	}
	var released []*rigReleased
	srv.dedupeRigComponents(ctx, uid, "10000000000000000000000000000000", 50, comps, &released)
	if _, ok := comps[rigCompKey(13, 0)]; ok {
		t.Fatalf("旧组件(extra 150)应被清理: %+v", comps)
	}
	if c, ok := comps[rigCompKey(13, 1)]; !ok || c.GUID != cur.GUID {
		t.Fatalf("应保留 target-valid(175) 组件: %+v", comps)
	}
	// 收集器必须上报旧组件的退回去向，且退回树可进快照 f1。
	if len(released) != 1 || released[0].GUID != old.GUID {
		t.Fatalf("应收集到 1 条退回记录(旧组件): %+v", released)
	}
	if !released[0].Returned || released[0].ReturnTree == nil {
		t.Fatalf("普通物品应标记 Returned 且带退回树: %+v", released[0])
	}
	if trees := releasedReturnTrees(released); len(trees) != 1 || trees[0] == nil {
		t.Fatalf("releasedReturnTrees 应产出退回树: %+v", trees)
	}
}

// TestRigMountReplacesCrossCategorySharedSlot 验证正常装配时最后一次装配生效：
// rig57 的钩(12)与拟饵(17)共用槽位252，先装钩再装拟饵后，应保留拟饵并把钩退回背包。
func TestRigMountReplacesCrossCategorySharedSlot(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "pilkr_10212_92-80190", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !srv.rigSetType(ctx, uid, rod.GUID, 57) {
		t.Fatal("切 rig57 失败")
	}
	hook, err := srv.CreateItem(ctx, uid, "clas_20163", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	lure, err := srv.CreateItem(ctx, uid, "pilkerlures_2630", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}

	if res := srv.rigSetComponent(ctx, uid, rod.GUID, 12, hook.GUID, 252,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true, SplitStack: true}); res.Rejected {
		t.Fatalf("装钩失败: %+v", res)
	}
	res := srv.rigSetComponent(ctx, uid, rod.GUID, 17, lure.GUID, 252,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true, SplitStack: true})
	if res.Rejected {
		t.Fatalf("装拟饵失败: %+v", res)
	}

	row, err := db.GetItemByGUID(ctx, rod.GUID)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	comps := rigCompsOf(rigStoredF5(t, row))
	var occupant *rigComp
	for _, c := range comps {
		if c.Extra == 252 {
			c := c
			occupant = &c
		}
	}
	if occupant == nil || occupant.GUID != lure.GUID || occupant.Category != 17 {
		t.Fatalf("最后一次装配的拟饵应占槽252，实得 %+v comps=%+v", occupant, comps)
	}
	hookRow, err := db.GetItemByGUID(ctx, hook.GUID)
	if err != nil || hookRow == nil {
		t.Fatal(err)
	}
	if hookRow.Container != "backpack" || hookRow.RigGUID != "" {
		t.Fatalf("被替换的钩应退回背包，实得 container=%q rig=%q", hookRow.Container, hookRow.RigGUID)
	}
	foundReleased := false
	for _, rel := range res.Released {
		if rel != nil && rel.GUID == hook.GUID {
			foundReleased = true
		}
	}
	if !foundReleased {
		t.Fatalf("装配结果应上报被替换钩的退回去向: %+v", res.Released)
	}
	foundReturnTree := false
	for _, tree := range res.UpdatedTrees {
		for _, rel := range res.Released {
			if rel != nil && rel.Returned && tree == rel.ReturnTree {
				foundReturnTree = true
			}
		}
	}
	if !foundReturnTree {
		t.Fatalf("被替换钩的退回树必须进入最终 800.f1 更新段: %+v", res.UpdatedTrees)
	}
}
