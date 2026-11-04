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
	if !s.applyBaitedHookSpec(t.Context(), groups[0], &spec) || !spec.HookPresent || spec.HookSize == 999 || spec.HookStrengthKg <= 0 {
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
