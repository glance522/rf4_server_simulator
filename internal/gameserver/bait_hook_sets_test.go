package gameserver

import "testing"

func mountHookForBaitTest(t *testing.T, s *Server, rod string, ordinal int64) string {
	t.Helper()
	hook, err := s.CreateItem(t.Context(), 1000001, "c_hook_20712", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	result := s.rigSetComponent(t.Context(), 1000001, rod, catHook, hook.GUID, 250+ordinal, rigSetOpts{ParentType: familyRod, EnforceCategory: true})
	if result.Rejected || result.MountedGUID == "" {
		t.Fatalf("mount hook: %+v", result)
	}
	return result.MountedGUID
}

func TestBaitedHookSingleCombinationAndSelectedSpec(t *testing.T) {
	t.Parallel()
	s, db := newRigTestServer(t)
	rod := baitTestRod(t, s)
	hook := mountHookForBaitTest(t, s, rod, 0)
	_, first := baitTestMount(t, s, rod, "worm_01", 3, 350)
	_, second := baitTestMount(t, s, rod, "muckworm", 3, 351)
	groups := s.baitedHookSets(t.Context(), rod)
	if len(groups) != 1 || groups[0].hookGUID != hook || len(groups[0].baits) != 2 || groups[0].baitGUIDs[0] != first || groups[0].baitGUIDs[1] != second {
		t.Fatalf("single-hook combination = %+v", groups)
	}
	spec := gearPhysicsSpec{HookSize: 999}
	if !s.applyBaitedHookSpec(t.Context(), groups[0], &spec) || !spec.HookPresent ||
		spec.HookItemID != "c_hook_20712" || spec.HookSize == 999 || spec.HookStrengthKg <= 0 {
		t.Fatalf("selected hook spec = %+v", spec)
	}
	if change, err := s.consumeBaitEvent(t.Context(), 1000001, rod, "single-combination", groups[0].baitGUIDs, false); err != nil || len(change.Removed) != 2 {
		t.Fatalf("single combination consumption = %+v / %v", change, err)
	}
	if _, err := s.reloadBaitForCast(t.Context(), 1000001, rod); err != nil {
		t.Fatal(err)
	}
	if reloaded := s.baitedHookSets(t.Context(), rod); len(reloaded) != 1 || len(reloaded[0].baits) != 2 || reloaded[0].baits[0] != "worm_01" || reloaded[0].baits[1] != "muckworm" {
		t.Fatalf("single-hook combination reload = %+v", reloaded)
	}
	if err := db.SetItemDurability(t.Context(), hook, 0); err != nil {
		t.Fatal(err)
	}
	if s.applyBaitedHookSpec(t.Context(), groups[0], &spec) {
		t.Fatal("broken selected hook accepted")
	}
}

func TestBaitedHookUsesEmbeddedLureHookDefinition(t *testing.T) {
	t.Parallel()
	s, _ := newRigTestServer(t)
	rod, err := s.CreateItem(t.Context(), 1000001, "spin_10001_S60MH", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	baitTestMount(t, s, rod.GUID, "spinnerbaits_1244", 1, 300)
	groups := s.baitedHookSets(t.Context(), rod.GUID)
	if len(groups) != 1 || groups[0].hookGUID == "" {
		t.Fatalf("lure hook group = %+v", groups)
	}
	var spec gearPhysicsSpec
	if !s.applyBaitedHookSpec(t.Context(), groups[0], &spec) || !spec.HookPresent || spec.HookLengthMM <= 0 || spec.HookStrengthKg <= 0 {
		t.Fatalf("embedded lure hook spec = %+v", spec)
	}
}

func TestBaitedHookConsumptionPreservesOtherHooksAfterCompactionAndRestart(t *testing.T) {
	t.Parallel()
	s, db := newRigTestServer(t)
	rod, err := s.CreateItem(t.Context(), 1000001, "boatr_10222_SBH80_100", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !s.rigSetType(t.Context(), 1000001, rod.GUID, 59) {
		t.Fatal("marine rig rejected")
	}
	ids := []string{"worm_01", "muckworm", "fly"}
	for i, id := range ids {
		mountHookForBaitTest(t, s, rod.GUID, int64(i))
		baitTestMount(t, s, rod.GUID, id, 3, int64(350+i))
	}
	groups := s.baitedHookSets(t.Context(), rod.GUID)
	if len(groups) != 3 {
		t.Fatalf("hook groups = %+v", groups)
	}
	if change, err := s.consumeBaitEvent(t.Context(), 1000001, rod.GUID, "selected-hook", groups[0].baitGUIDs, false); err != nil || len(change.Removed) != 1 {
		t.Fatalf("selected consumption = %+v / %v", change, err)
	}
	restarted := newFishingRecoveryServer(t, db)
	remaining := restarted.baitedHookSets(t.Context(), rod.GUID)
	if len(remaining) != 2 || remaining[0].hookGUID != groups[1].hookGUID || remaining[0].baitGUIDs[0] != groups[1].baitGUIDs[0] || remaining[1].hookGUID != groups[2].hookGUID {
		t.Fatalf("other hooks changed after compaction/restart: %+v", remaining)
	}
	if _, err := restarted.consumeBaitEvent(t.Context(), 1000001, rod.GUID, "selected-hook", groups[1].baitGUIDs, false); err != nil {
		t.Fatal(err)
	}
	if got := restarted.baitedHookSets(t.Context(), rod.GUID); len(got) != 2 {
		t.Fatal("replayed event consumed another hook")
	}
	if _, err := restarted.reloadBaitForCast(t.Context(), 1000001, rod.GUID); err != nil {
		t.Fatal(err)
	}
	reloaded := restarted.baitedHookSets(t.Context(), rod.GUID)
	if len(reloaded) != 3 {
		t.Fatalf("reloaded hooks = %+v", reloaded)
	}
	for i, group := range reloaded {
		if group.hookGUID != groups[i].hookGUID || len(group.baits) != 1 || group.baits[0] != ids[i] {
			t.Fatalf("reload changed hook %d: %+v", i, group)
		}
	}
	if _, err := db.SQL.Exec(`DELETE FROM item WHERE guid=?`, groups[1].baitGUIDs[0]); err != nil {
		t.Fatal(err)
	}
	if result := restarted.rigRemoveItemComponent(t.Context(), 1000001, rod.GUID, groups[1].hookGUID); result == nil {
		t.Fatal("detach hook failed")
	}
	_ = restarted.baitedHookSets(t.Context(), rod.GUID)
	var stale int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM rod_setting WHERE guid=? AND key=?`, rod.GUID, "bait_hook:"+groups[1].baitGUIDs[0]).Scan(&stale); err != nil || stale != 0 {
		t.Fatalf("invalid mapping retained: count=%d err=%v", stale, err)
	}
}

func TestOrderBaitsByHookSyncsRigComponentOrdinals(t *testing.T) {
	t.Parallel()
	s, db := newRigTestServer(t)
	rod, _, groups := selectedHookMarineRig(t, s, []string{"worm_01", "muckworm", "fly"})
	for i, group := range groups {
		hook := groups[len(groups)-1-i].hookGUID
		if err := db.UpsertRodSetting(t.Context(), rod, "bait_hook:"+group.baitGUIDs[0], hook); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.orderBaitsByHook(t.Context(), 1000001, rod); err != nil {
		t.Fatal(err)
	}
	comps := s.storedRigComponents(t.Context(), rod)
	rows, err := db.ListRigComponents(t.Context(), rod)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, row := range rows {
		if row.Slot != catBait {
			continue
		}
		count++
		comp, ok := comps[rigCompKey(catBait, row.Ordinal)]
		if !ok || comp.GUID != row.GUID {
			t.Fatalf("rig_component bait ordinal=%d guid=%s, f5=%+v", row.Ordinal, row.GUID, comp)
		}
	}
	if count != len(groups) {
		t.Fatalf("rig_component bait rows=%d, want %d", count, len(groups))
	}
}

// 串钩的钩位分配以“第 N 个饵位 → 第 N 个钩位”为准。
//
// 复现线上事故：客户端先挂饵、后挂钩，装配中途钩还没装齐时 hookPositionsIn
// 只看到模板声明的 1 个钩位，旧逻辑把每条饵都兜底绑到第一枚钩并持久化，
// 钩装到 4 个之后仍是“钩位=4 已挂饵钩位=1”
// （logs/gameserver.log 2026-11-13 07:17）。
func TestBaitHookAssignmentsPairByOrdinal(t *testing.T) {
	t.Parallel()
	s, _ := newRigTestServer(t)
	rod := baitTestRod(t, s)
	comps := map[int64]rigComp{}
	hooks := make([]string, 3)
	for i := 0; i < 3; i++ {
		hook, err := s.CreateItem(t.Context(), 1000001, "c_hook_20712", "backpack", 1)
		if err != nil {
			t.Fatal(err)
		}
		hooks[i] = hook.GUID
		comps[rigCompKey(catHook, int64(i))] = rigComp{Category: catHook, Ordinal: int64(i), GUID: hook.GUID, Extra: 250 + int64(i)}
	}
	baits := make([]string, 3)
	for i := 0; i < 3; i++ {
		bait, err := s.CreateItem(t.Context(), 1000001, "worm_01", "backpack", 3)
		if err != nil {
			t.Fatal(err)
		}
		baits[i] = bait.GUID
		comps[rigCompKey(catBait, int64(i))] = rigComp{Category: catBait, Ordinal: int64(i), GUID: bait.GUID, Extra: 350 + int64(i)}
	}

	assigned, err := s.baitHookAssignments(t.Context(), rod, comps, false)
	if err != nil {
		t.Fatal(err)
	}
	for i := range baits {
		if assigned[baits[i]] != hooks[i] {
			t.Fatalf("第 %d 个饵位应配第 %d 个钩位，得到 %q（钩位 %q）", i, i, assigned[baits[i]], hooks[i])
		}
	}
}

// 库里已经写成“多条饵共用第一枚钩”的中间态记录要被纠正并回写，
// 否则钩位变化后旧记录会一直生效。
func TestBaitHookAssignmentsRepairsStaleSameHookMappings(t *testing.T) {
	t.Parallel()
	s, db := newRigTestServer(t)
	rod := baitTestRod(t, s)
	comps := map[int64]rigComp{}
	hooks := make([]string, 2)
	for i := 0; i < 2; i++ {
		hook, err := s.CreateItem(t.Context(), 1000001, "c_hook_20712", "backpack", 1)
		if err != nil {
			t.Fatal(err)
		}
		hooks[i] = hook.GUID
		comps[rigCompKey(catHook, int64(i))] = rigComp{Category: catHook, Ordinal: int64(i), GUID: hook.GUID, Extra: 250 + int64(i)}
	}
	baits := make([]string, 2)
	for i := 0; i < 2; i++ {
		bait, err := s.CreateItem(t.Context(), 1000001, "worm_01", "backpack", 3)
		if err != nil {
			t.Fatal(err)
		}
		baits[i] = bait.GUID
		comps[rigCompKey(catBait, int64(i))] = rigComp{Category: catBait, Ordinal: int64(i), GUID: bait.GUID, Extra: 350 + int64(i)}
	}
	for _, bait := range baits {
		if _, err := db.SQL.Exec(`INSERT INTO rod_setting(guid,key,value) VALUES(?,?,?)
			ON CONFLICT(guid,key) DO UPDATE SET value=excluded.value`,
			rod, "bait_hook:"+bait, hooks[0]); err != nil {
			t.Fatal(err)
		}
	}

	assigned, err := s.baitHookAssignments(t.Context(), rod, comps, false)
	if err != nil {
		t.Fatal(err)
	}
	if assigned[baits[1]] != hooks[1] {
		t.Fatalf("第二条饵应改配钩 1，得到 %q", assigned[baits[1]])
	}
	var saved string
	if err := db.SQL.QueryRow(`SELECT value FROM rod_setting WHERE guid=? AND key=?`,
		rod, "bait_hook:"+baits[1]).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if saved != hooks[1] {
		t.Fatalf("纠正结果未回写，库里仍是 %q", saved)
	}
}

// 海钓多钩的钩位在客户端是 1001/1005/1009 这套独立子槽，钩位下的拟饵与珠子
// 落在同族子槽（1002/1006/1010…），主线上的拟饵与亮片则在 300/325/326。
// 组件 ordinal 只是数组下标，和这些位置并不对齐，按 ordinal 配对会把 4 个钩位
// 塌成 1 组（实机 2026-11-13 07:40 "钩位=4 已挂饵钩位=1"）。这里按 extra 归组。
func TestBaitHookAssignmentsFollowsMarineHookSubSlots(t *testing.T) {
	t.Parallel()
	s, db := newRigTestServer(t)
	rod := baitTestRod(t, s)
	comps := map[int64]rigComp{}
	mount := func(category, ordinal, extra int64, itemID string) string {
		t.Helper()
		item, err := s.CreateItem(t.Context(), 1000001, itemID, "backpack", 3)
		if err != nil {
			t.Fatal(err)
		}
		comps[rigCompKey(category, ordinal)] = rigComp{
			Category: category, Ordinal: ordinal, GUID: item.GUID, Extra: extra,
		}
		return item.GUID
	}

	// 主线钩位 250 + 三个倒钓臂钩位 1001/1005/1009。
	mainHook := mount(catHook, 0, 250, "c_hook_20712")
	arm1 := mount(catHook, 1, 1001, "c_hook_20712")
	arm2 := mount(catHook, 2, 1005, "c_hook_20712")
	arm3 := mount(catHook, 3, 1009, "c_hook_20712")

	// 饵位按客户端上报顺序：先主线，再逐条子线，子线上还夹着珠子。
	pilker := mount(catLure, 0, 300, "worm_01")
	fireStick := mount(catLure, 1, 325, "worm_01")
	fireTube := mount(catLure, 2, 326, "worm_01")
	arm1Bait := mount(catBait, 3, 1002, "worm_01")
	arm1Bead := mount(catBait, 4, 1003, "worm_01")
	arm2Bait := mount(catBait, 5, 1006, "worm_01")
	arm2Bead := mount(catBait, 6, 1007, "worm_01")
	arm3Bait := mount(catBait, 7, 1010, "worm_01")
	arm3Bead := mount(catBait, 8, 1011, "worm_01")

	// 主线饵按 bait_hook 记录走（客户端上饵时写下的位置），不参与子槽拓扑。
	if _, err := db.SQL.Exec(`INSERT INTO rod_setting(guid,key,value) VALUES(?,?,?)
		ON CONFLICT(guid,key) DO UPDATE SET value=excluded.value`,
		rod, "bait_hook:"+pilker, mainHook); err != nil {
		t.Fatal(err)
	}

	assigned, err := s.baitHookAssignments(t.Context(), rod, comps, false)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		pilker:   mainHook,
		arm1Bait: arm1, arm1Bead: arm1,
		arm2Bait: arm2, arm2Bead: arm2,
		arm3Bait: arm3, arm3Bead: arm3,
	}
	for bait, hook := range want {
		if assigned[bait] != hook {
			t.Fatalf("饵 %s 应归钩位 %s，得到 %q（全部结果 %v）", bait[:8], hook[:8], assigned[bait], assigned)
		}
	}
	// 主线上的装饰（亮片、发光管）没有子槽归属，不能凭空占一个钩位。
	for _, bait := range []string{fireStick, fireTube} {
		if assigned[bait] != "" {
			t.Fatalf("主线装饰 %s 不该被分配钩位，得到 %q", bait[:8], assigned[bait])
		}
	}
	if got := len(map[string]bool{
		assigned[pilker]: true, assigned[arm1Bait]: true,
		assigned[arm2Bait]: true, assigned[arm3Bait]: true,
	}); got != 4 {
		t.Fatalf("4 个钩位应各有一组饵，得到 %d 组", got)
	}
}

// 装配中途只装上一枚钩时写下的记录（多条饵全指向同一枚钩），在钩装齐之后
// 必须被子槽拓扑覆盖，否则旧记录会一直把多钩压成一组。
func TestBaitHookAssignmentsOverridesStaleRecordInSubSlotRig(t *testing.T) {
	t.Parallel()
	s, db := newRigTestServer(t)
	rod := baitTestRod(t, s)
	comps := map[int64]rigComp{}
	mount := func(category, ordinal, extra int64) string {
		t.Helper()
		item, err := s.CreateItem(t.Context(), 1000001, "c_hook_20712", "backpack", 3)
		if err != nil {
			t.Fatal(err)
		}
		comps[rigCompKey(category, ordinal)] = rigComp{
			Category: category, Ordinal: ordinal, GUID: item.GUID, Extra: extra,
		}
		return item.GUID
	}
	mainHook := mount(catHook, 0, 250)
	arm2 := mount(catHook, 1, 1005)
	mainBait := mount(catBait, 0, 300)
	arm2Bait := mount(catBait, 1, 1006)

	// 钩还没装齐时两枚饵都被写成主线钩。
	for _, bait := range []string{mainBait, arm2Bait} {
		if _, err := db.SQL.Exec(`INSERT INTO rod_setting(guid,key,value) VALUES(?,?,?)
			ON CONFLICT(guid,key) DO UPDATE SET value=excluded.value`,
			rod, "bait_hook:"+bait, mainHook); err != nil {
			t.Fatal(err)
		}
	}

	assigned, err := s.baitHookAssignments(t.Context(), rod, comps, false)
	if err != nil {
		t.Fatal(err)
	}
	if assigned[arm2Bait] != arm2 {
		t.Fatalf("子线饵应改归 %s，得到 %q", arm2[:8], assigned[arm2Bait])
	}
	var saved string
	if err := db.SQL.QueryRow(`SELECT value FROM rod_setting WHERE guid=? AND key=?`,
		rod, "bait_hook:"+arm2Bait).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if saved != arm2 {
		t.Fatalf("纠正结果未回写，库里仍是 %q", saved)
	}
}

// 实机 2026-11-13 08:01：主线拟饵的 bait_hook 记录指向它自带的三本钩
// （内嵌引用 04047060…，item 表早已查不到），主线钩 250 因此整枚空置，
// "钩位=4" 实际只有 3 个真钩在出鱼。记录一旦落不到本钓组的钩组件上，
// 主线槽位必须回落到主线钩，并把纠正结果回写。
func TestBaitHookAssignmentsFallsBackToMainHookForStaleEmbeddedHook(t *testing.T) {
	t.Parallel()
	s, db := newRigTestServer(t)
	rod := baitTestRod(t, s)
	comps := map[int64]rigComp{}
	mount := func(category, ordinal, extra int64) string {
		t.Helper()
		item, err := s.CreateItem(t.Context(), 1000001, "c_hook_20712", "backpack", 3)
		if err != nil {
			t.Fatal(err)
		}
		comps[rigCompKey(category, ordinal)] = rigComp{
			Category: category, Ordinal: ordinal, GUID: item.GUID, Extra: extra,
		}
		return item.GUID
	}

	mainHook := mount(catHook, 0, 250)
	arm1 := mount(catHook, 1, 1001)
	arm2 := mount(catHook, 2, 1005)
	arm3 := mount(catHook, 3, 1009)

	pilker := mount(catLure, 0, 300)
	spoon := mount(catLure, 1, 325)
	arm1Bait := mount(catLure, 2, 1002)
	arm2Bait := mount(catLure, 3, 1006)
	arm3Bait := mount(catLure, 4, 1010)

	// 真拟饵自带的三本钩：不在本钓组钩组件里（实机是内嵌引用 04047060…，
	// item 表早已查不到），但 lureActiveHookGUID 仍会把它当合法钩位。
	lure, err := s.CreateItem(t.Context(), 1000001, "spinnerbaits_1244", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	factory := lureTestHooks(t, s, lure.GUID)
	if len(factory) == 0 {
		t.Fatal("测试拟饵没有出厂钩")
	}
	embedded := factory[0]
	comps[rigCompKey(catLure, 5)] = rigComp{Category: catLure, Ordinal: 5, GUID: lure.GUID, Extra: 1010 + 4}
	for bait, hook := range map[string]string{pilker: embedded} {
		if _, err := db.SQL.Exec(`INSERT INTO rod_setting(guid,key,value) VALUES(?,?,?)
			ON CONFLICT(guid,key) DO UPDATE SET value=excluded.value`,
			rod, "bait_hook:"+bait, hook); err != nil {
			t.Fatal(err)
		}
	}

	assigned, err := s.baitHookAssignments(t.Context(), rod, comps, false)
	if err != nil {
		t.Fatal(err)
	}
	if assigned[pilker] != mainHook {
		t.Fatalf("主线拟饵应回落到主线钩 %s，得到 %q", mainHook[:8], assigned[pilker])
	}
	for bait, want := range map[string]string{arm1Bait: arm1, arm2Bait: arm2, arm3Bait: arm3} {
		if assigned[bait] != want {
			t.Fatalf("子槽饵应归 %s，得到 %q", want[:8], assigned[bait])
		}
	}
	if assigned[spoon] != "" {
		t.Fatalf("没有记录的主线装饰不该占钩位，得到 %q", assigned[spoon])
	}

	var saved string
	if err := db.SQL.QueryRow(`SELECT value FROM rod_setting WHERE guid=? AND key=?`,
		rod, "bait_hook:"+pilker).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if saved != mainHook {
		t.Fatalf("回落结果未回写，库里仍是 %q", saved)
	}
}
