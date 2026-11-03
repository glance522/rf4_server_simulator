package gameserver

import (
	"context"
	"math"
	"testing"
	"time"

	"rf4server/internal/proto"
)

func TestMarineHookBreakPreservesOtherHooksAndBaits(t *testing.T) {
	testMarineHookBreakPreservesChain(t, 3, 3)
}

func TestMarineHookBreakPreservesSparseBait(t *testing.T) {
	testMarineHookBreakPreservesChain(t, 3, 1)
}

func TestMarineSingleHookBreakDropsAllItsBait(t *testing.T) {
	testMarineHookBreakPreservesChain(t, 1, 2)
}

func testMarineHookBreakPreservesChain(t *testing.T, hookCount, baitCount int) {
	t.Helper()
	s, db := newBalanceTestServer(t)
	ctx := context.Background()
	rod, err := s.CreateItem(ctx, testUID, "boatr_10222_SBH80_100", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !s.rigSetType(ctx, testUID, rod.GUID, 59) {
		t.Fatal("marine three-hook rig rejected")
	}
	sess := newProbeSess(s)
	line, err := s.CreateItem(ctx, testUID, "mono_15095", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	mountGearForTest(t, s, sess, rod.GUID, line.GUID)
	var hooks, baits []string
	for ordinal := 0; ordinal < max(hookCount, baitCount); ordinal++ {
		if ordinal < hookCount {
			hook, err := s.CreateItem(ctx, testUID, "clas_20163", "backpack", 1)
			if err != nil {
				t.Fatal(err)
			}
			res := mountGearForTest(t, s, sess, rod.GUID, hook.GUID)
			hooks = append(hooks, res.MountedGUID)
		}
		if ordinal >= baitCount {
			continue
		}
		// 保存多钩物品树已有的同序号挂饵格式，不经单饵界面的替换路径。
		bait, err := s.prepareNewItemWithCount(ctx, testUID, "worm_01", "rig", 1, "")
		if err != nil {
			t.Fatal(err)
		}
		bait.RigGUID = rod.GUID
		fields, _ := FieldsFromJSON(bait.FieldsJSON)
		fields[fieldIdxByCS(bait.WireClass, csParent)].Hex = rod.GUID
		bait.FieldsJSON, _ = FieldsToJSON(fields)
		if err := db.InsertItem(ctx, bait); err != nil {
			t.Fatal(err)
		}
		s.rigComponentWriteAt(ctx, rod.GUID, catBait, int64(ordinal), bait.GUID, 350)
		baits = append(baits, bait.GUID)
	}
	st := s.stateFor(testUID)
	st.mu.Lock()
	st.GearGuid = rod.GUID
	st.mu.Unlock()
	for broken := 0; broken < min(2, hookCount); broken++ {
		if err := db.SetItemDurability(ctx, hooks[broken], 0.01); err != nil {
			t.Fatal(err)
		}
		out, err := s.breakLineOrRod(ctx, testUID, rod.GUID, rod.GUID)
		if err != nil || out.BreakPart != "hook" || out.BrokenGUID != hooks[broken] || out.LineLossM != 0 {
			t.Fatalf("hook break %d: %+v %v", broken, out, err)
		}
		for i, guid := range hooks {
			row, _ := db.GetItemByGUID(ctx, guid)
			if (row == nil) != (i <= broken) {
				t.Fatalf("break %d changed unrelated hook %d/%s", broken, i, guid)
			}
		}
		for i, guid := range baits {
			row, _ := db.GetItemByGUID(ctx, guid)
			if (row == nil) != (hookCount == 1 || (baitCount == hookCount && i <= broken)) {
				t.Fatalf("break %d changed unrelated bait %d/%s", broken, i, guid)
			}
		}
		row, _ := db.GetItemByGUID(ctx, rod.GUID)
		f5 := rigStoredF5(t, row)
		comps := rigCompsOf(f5)
		gotHooks := rigTestCategoryComps(t, f5, catHook)
		if len(gotHooks) != hookCount-broken-1 {
			t.Fatalf("hook count after break %d: %+v", broken, gotHooks)
		}
		for ordinal, hook := range gotHooks {
			original := broken + 1 + ordinal
			if hook.GUID != hooks[original] || hook.Extra != int64(250+original) {
				t.Fatalf("break %d moved surviving hook: %+v", broken, hook)
			}
		}
		wantBaits := baitCount
		if hookCount == 1 {
			wantBaits = 0
		} else if baitCount == hookCount {
			wantBaits -= broken + 1
		}
		if got := rigTestCategoryComps(t, f5, catBait); len(got) != wantBaits {
			t.Fatalf("bait count after break %d: %+v", broken, got)
		}
		projected, err := db.ListRigComponents(ctx, rod.GUID)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range projected {
			if p.Slot != catHook && p.Slot != catBait {
				continue
			}
			c, ok := comps[rigCompKey(p.Slot, p.Ordinal)]
			if !ok || p.GUID != c.GUID || p.Extra != c.Extra {
				t.Fatalf("projection does not match compacted tree: %+v %+v", p, c)
			}
		}
		wantHook := ""
		if broken+1 < hookCount {
			wantHook = hooks[broken+1]
		}
		if got := s.mountedHookGuid(ctx, rod.GUID); got != wantHook {
			t.Fatal("next surviving hook not active", got)
		}
		items, err := s.buildFightItems(ctx, testUID, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			if _, err := proto.EncodeValue(item, s.Table()); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := proto.EncodeValue(out.UpdatedTrees[0], s.Table()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFactoryHookWearMatchesTransmittedStrengthAndZeroRemainsBroken(t *testing.T) {
	s, db := newBalanceTestServer(t)
	ctx := context.Background()
	rod, err := s.CreateItem(ctx, testUID, "spin_10001_S60MH", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	lure, err := s.CreateItem(ctx, testUID, "spinnerbaits_1244", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	mountLure(t, db, s, rod.GUID, lure.GUID)
	for _, wear := range []float64{0.1, 0} {
		row, err := db.GetItemByGUID(ctx, lure.GUID)
		if err != nil {
			t.Fatal(err)
		}
		fields, _ := FieldsFromJSON(row.FieldsJSON)
		child := fields[0].Val.Items[0]
		setWireFloat(child.Fields, child.Class, fieldIdxByCS(child.Class, csDurability), wear)
		raw, _ := FieldsToJSON(fields)
		if err := db.UpdateItemFields(ctx, row.GUID, raw, row.Qty, row.Durability, row.Workmanship, row.RigGUID); err != nil {
			t.Fatal(err)
		}
		def, durability := s.mountedHookDef(ctx, rod.GUID)
		if def == nil || math.Abs(durability-wear) > 1e-6 {
			t.Fatal("factory hook wear ignored", durability, wear)
		}
		spec := s.loadGearSpecForRig(ctx, s.rigGearIDs(ctx, testUID, rod.GUID), rod.GUID)
		tree := s.buildMountedHookTree(ctx, rod.GUID)
		transmitted := float64(proto.F32FromBits(uint32(u64Of(tree.Fields[fieldIdxByCS(tree.Class, csStrength)].Num))))
		if math.Abs(transmitted-spec.HookStrengthKg) > 0.0001 {
			t.Fatalf("force model %g != client %g", spec.HookStrengthKg, transmitted)
		}
		if wear == 0 && spec.BrokenPart != "hook" {
			t.Fatal("zero-wear factory hook revived", spec)
		}
	}
}

func TestZeroLengthBreakDoesNotRefillLineOrDeadlock(t *testing.T) {
	s, db := newBalanceTestServer(t)
	ctx := context.Background()
	rod, err := s.CreateItem(ctx, testUID, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := s.CreateItem(ctx, testUID, "mono_15095", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	mountGearForTest(t, s, newProbeSess(s), rod.GUID, line.GUID)
	line, err = db.GetItemByGUID(ctx, s.rigGearIDs(ctx, testUID, rod.GUID).LineGUID)
	if err != nil {
		t.Fatal(err)
	}
	fields, _ := FieldsFromJSON(line.FieldsJSON)
	setItemLengthField(line, fields, 0)
	raw, _ := FieldsToJSON(fields)
	if err := db.UpdateItemFields(ctx, line.GUID, raw, 0, line.Durability, line.Workmanship, line.RigGUID); err != nil {
		t.Fatal(err)
	}
	db.SQL.SetMaxOpenConns(1)
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := s.breakLineTx(bounded, testUID, rod.GUID, line.GUID, catLine)
	if err != nil || out.LineLossM != 0 {
		t.Fatalf("zero length break refilled or blocked: %+v %v", out, err)
	}
	if remaining, _ := db.GetItemByGUID(ctx, line.GUID); remaining != nil {
		t.Fatal("empty line still present")
	}
}
