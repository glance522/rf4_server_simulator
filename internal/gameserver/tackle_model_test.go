package gameserver

import (
	"context"
	"math"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func TestBodyCalibrationReferencesAndGiantBoundaries(t *testing.T) {
	if len(fishBodyReferences) != 57 {
		t.Fatal(len(fishBodyReferences))
	}
	for species, ref := range fishBodyReferences {
		got := fishBodyLength(species, ref.ReferenceG, 0.0355, 1.0/3)
		if math.Abs(got-ref.ReferenceM) > 0.02 {
			t.Fatalf("%s length %.3f != reference %.3f", species, got, ref.ReferenceM)
		}
	}
	if got := fishBodyLength("cetorhinus_m", 2_000_000, 0.0355, 1.0/3); got < 6.9 || got > 7 {
		t.Fatal(got)
	}
	if got := fishBodyLength("cetorhinus_m", 2_000_000, 0.02, 0.3); math.Abs(got-0.02*math.Pow(2_000_000, 0.3)) > 0.00001 {
		t.Fatal("species-specific length curve overwritten")
	}
}

func TestTackleLoadMaterialsDragAndLineEnd(t *testing.T) {
	spec := gearPhysicsSpec{RodPowerKg: 40, LineStrengthKg: 20, LineLengthM: 1200, LineDensityGPM: 0.2, ReelPresent: true, ReelDragKg: 30, ReelBaseDragKg: 10}
	g := verifiedForcePerGram * 1000
	in := tackleLoadContext{ForceN: 15 * g, PeakN: 30 * g, Locked: true, LineOutM: 100}
	stiff := evaluateTackleLoad(spec, in)
	spec.LineStretch = 0.9
	soft := evaluateTackleLoad(spec, in)
	if soft.TensionN != stiff.TensionN || soft.PeakN >= stiff.PeakN || math.Abs(soft.LoadRatio-0.75) > 1e-9 {
		t.Fatalf("buffer changed steady load or did not reduce impulse: %+v %+v", stiff, soft)
	}
	if soft.RunningLineMassG != 20 || soft.ReserveM != 1100 {
		t.Fatal(soft)
	}
	in.Locked = false
	in.DragFraction = 0.5
	slip := evaluateTackleLoad(spec, in)
	if !slip.Slipping || math.Abs(slip.LoadRatio-0.25) > 1e-9 {
		t.Fatalf("loosening drag did not reduce transmitted load: %+v", slip)
	}
	in.LineOutM = 1200
	end := evaluateTackleLoad(spec, in)
	if !end.LineExhausted || end.Slipping || end.LoadRatio <= slip.LoadRatio {
		t.Fatal("exhausted line still provides unlimited drag protection")
	}
}

func TestCraftedLeaderUsesSelectedMaterialsAndSurvivesRead(t *testing.T) {
	s, db := newBalanceTestServer(t)
	ctx := context.Background()
	uid := int64(testUID)
	line, err := s.CreateItem(ctx, uid, "mono_15095", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := s.CreateItem(ctx, uid, "swlock_40009", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetItemDurability(ctx, line.GUID, 0.6); err != nil {
		t.Fatal(err)
	}
	row, _ := db.GetItemByGUID(ctx, line.GUID)
	before := s.itemLength(ctx, row)
	req := craftObj8("craft_leader_10", []craftSlot{{ItemID: "craft_mat_line", GUID: line.GUID}, {ItemID: "craft_mat_swivels", GUID: lock.GUID}}, 2)
	body, ok := hCraft(&Ctx{Sess: newProbeSess(s), Tree: proto.Args524(req), DecodeOK: true, ExpectResponse: true, Log: logging.Discard()})
	if !ok {
		t.Fatal("craft handler did not handle leader")
	}
	v, _, err := proto.DecodeValue(body, 0, s.Table())
	if err != nil || v.Fields[0].Num == nil || u64Of(v.Fields[0].Num) == 0 {
		t.Fatalf("craft failed: %v", err)
	}
	rows, err := db.ListItemsByContainers(ctx, uid, []string{"backpack"})
	if err != nil {
		t.Fatal(err)
	}
	var leaders []*store.Item
	for _, row := range rows {
		if row.ItemID == "leader_crafted_10" {
			leaders = append(leaders, row)
		}
	}
	if len(leaders) != 2 {
		t.Fatalf("batch produced %d leaders", len(leaders))
	}
	seen := map[string]bool{}
	for _, leader := range leaders {
		for range 2 {
			tree, err := s.ItemToTree(ctx, leader)
			if err != nil {
				t.Fatal(err)
			}
			strength := leaderFieldsStrength(tree.Fields, leader.Durability)
			def, _ := db.GetItemDefByItemID(ctx, "mono_15095")
			if math.Abs(strength-lineStrengthKg(def)*0.6) > 0.0001 {
				t.Fatalf("material wear lost: %g", strength)
			}
			if s.itemBrokenForMount(ctx, leader) {
				t.Fatal("valid crafted leader rejected")
			}
		}
		fields, _ := FieldsFromJSON(leader.FieldsJSON)
		child := protoFieldAt(fields, leader.WireClass, "baadhdmaonp")
		idx := fieldIdxByCS(child.Class, csGUID)
		guid := child.Fields[idx].Hex
		if seen[guid] {
			t.Fatal("batch shares an embedded material GUID")
		}
		seen[guid] = true
	}
	row, _ = db.GetItemByGUID(ctx, line.GUID)
	if got := s.itemLength(ctx, row); got != before-2 {
		t.Fatalf("batch line consumption %d -> %d", before, got)
	}
}

func TestEmptyHistoricalCraftedLeaderCannotInventStrength(t *testing.T) {
	s, _ := newBalanceTestServer(t)
	ctx := context.Background()
	row, err := s.CreateItem(ctx, testUID, "leader_crafted_10", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !s.itemBrokenForMount(ctx, row) {
		t.Fatal("empty historical crafted leader treated as usable")
	}
	fields, _ := FieldsFromJSON(row.FieldsJSON)
	if leaderFieldsStrength(fields, row.Durability) != 0 {
		t.Fatal("invented material strength")
	}
}

func TestCraftedWireLeaderPreservesWearAndLimitsMountedRig(t *testing.T) {
	s, db := newBalanceTestServer(t)
	ctx := context.Background()
	wire, err := s.CreateItem(ctx, testUID, "br_leader_40077", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := s.CreateItem(ctx, testUID, "swlock_40009", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetItemDurability(ctx, wire.GUID, 0.3); err != nil {
		t.Fatal(err)
	}
	if !craftLeaderForTest(t, s, "craft_leader_10", []craftSlot{{ItemID: "craft_mat_line", GUID: wire.GUID}, {ItemID: "craft_mat_swivels", GUID: lock.GUID}}, 1) {
		t.Fatal("wire craft failed")
	}
	rows, _ := db.ListItemsByContainers(ctx, testUID, []string{"backpack"})
	var leader *store.Item
	for _, row := range rows {
		if isCraftedLeader(row) {
			leader = row
			break
		}
	}
	if leader == nil {
		t.Fatal("no crafted wire leader")
	}
	def, _ := db.GetItemDefByItemID(ctx, wire.ItemID)
	want := lineStrengthKg(def) * 0.3
	for range 2 {
		tree, err := s.ItemToTree(ctx, leader)
		if err != nil || math.Abs(leaderFieldsStrength(tree.Fields, leader.Durability)-want) > 0.0001 {
			t.Fatal("wire wear reset during read", err)
		}
	}
	rod, err := s.CreateItem(ctx, testUID, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	mountGearForTest(t, s, newProbeSess(s), rod.GUID, leader.GUID)
	spec := s.loadGearSpecForRig(ctx, s.rigGearIDs(ctx, testUID, rod.GUID), rod.GUID)
	if math.Abs(spec.LeaderStrengthKg-want) > 0.0001 {
		t.Fatalf("mount model ignored crafted wire: %+v", spec)
	}
	fields, _ := FieldsFromJSON(leader.FieldsJSON)
	child := protoFieldAt(fields, leader.WireClass, gearAttrLeaderIn)
	setWireFloat(child.Fields, child.Class, fieldIdxByCS(child.Class, csDurability), 0)
	leader.FieldsJSON, _ = FieldsToJSON(fields)
	tree, err := s.ItemToTree(ctx, leader)
	if err != nil || leaderFieldsStrength(tree.Fields, leader.Durability) != 0 || !s.itemBrokenForMount(ctx, leader) {
		t.Fatal("exhausted embedded material was revived", err)
	}
}

func TestBrokenPartCannotDisappearFromMatching(t *testing.T) {
	s, _ := newBalanceTestServer(t)
	ctx := context.Background()
	for _, part := range []string{"rod", "line", "reel", "leader", "leader_lock"} {
		ids := rigGearIDs{Rod: "tele_10180_4", Line: "mono_15095", Reel: "spin_5140", Leader: "fc_leader_40048", Connector: "swlock_40009", RodDurability: 1, LineDurability: 1, ReelDurability: 1, LeaderDurability: 1, ConnectorDurability: 1}
		switch part {
		case "rod":
			ids.RodDurability = 0
		case "line":
			ids.LineDurability = 0
		case "reel":
			ids.ReelDurability = 0
		case "leader":
			ids.LeaderDurability = 0
		case "leader_lock":
			ids.ConnectorDurability = 0
		}
		spec := s.loadGearSpec(ctx, ids)
		if spec.BrokenPart != part || gearBreakLimitKg(spec) != 0 || gearWeakestPart(spec) != part {
			t.Fatalf("broken %s was ignored: %+v", part, spec)
		}
		if s.sampleMatchedWeight("c.roach", func() float64 { return 100 }, 0, spec) != 0 {
			t.Fatal("broken tackle received fish", part)
		}
		if can, _ := canLandGear(spec, 0.1, 0.35); can {
			t.Fatal("broken tackle can land fish", part)
		}
	}
}

func TestExhaustedLineCannotCastOrMatchFish(t *testing.T) {
	s, db := newBalanceTestServer(t)
	ctx := context.Background()
	rod, err := s.CreateItem(ctx, testUID, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(s)
	var line *store.Item
	for _, id := range []string{"spin_5820", "mono_40853"} {
		row, err := s.CreateItem(ctx, testUID, id, "backpack", 1)
		if err != nil {
			t.Fatal(err)
		}
		mountGearForTest(t, s, sess, rod.GUID, row.GUID)
		if id == "mono_40853" {
			line = row
		}
	}
	line, _ = db.GetItemByGUID(ctx, s.rigGearIDs(ctx, testUID, rod.GUID).LineGUID)
	fields, _ := FieldsFromJSON(line.FieldsJSON)
	setItemLengthField(line, fields, 0)
	raw, _ := FieldsToJSON(fields)
	if err := db.UpdateItemFields(ctx, line.GUID, raw, 0, line.Durability, line.Workmanship, line.RigGUID); err != nil {
		t.Fatal(err)
	}
	if got := s.castDistanceForRig(ctx, testUID, rod.GUID, 12); got.DistanceM != 0 {
		t.Fatalf("empty spool casts: %+v", got)
	}
	spec := s.loadGearSpecForRig(ctx, s.rigGearIDs(ctx, testUID, rod.GUID), rod.GUID)
	if got := s.sampleMatchedWeight("c.roach", func() float64 { return 100 }, 0, spec); got != 0 {
		t.Fatal("empty line received fish")
	}
	if load := evaluateTackleLoad(spec, tackleLoadContext{ForceN: 20, PeakN: 30, DragFraction: 0.2}); !load.LineExhausted || load.Slipping {
		t.Fatal("zero line retains drag protection", load)
	}
}

func TestFightFullSyncIncludesCurrentWeakComponents(t *testing.T) {
	s, db := newBalanceTestServer(t)
	ctx := context.Background()
	rod, err := s.CreateItem(ctx, testUID, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(s)
	for _, id := range []string{"mono_15095", "fc_leader_40048", "clas_20163"} {
		row, err := s.CreateItem(ctx, testUID, id, "backpack", 1)
		if err != nil {
			t.Fatal(err)
		}
		mountGearForTest(t, s, sess, rod.GUID, row.GUID)
	}
	st := s.stateFor(testUID)
	st.mu.Lock()
	st.GearGuid = rod.GUID
	st.mu.Unlock()
	slots := rigSlotGUIDs(t, s, rod.GUID)
	if err := db.SetItemDurability(ctx, slots[catHook], 0.2); err != nil {
		t.Fatal(err)
	}
	items, err := s.buildFightItems(ctx, testUID, true)
	if err != nil {
		t.Fatal(err)
	}
	classes := map[string]bool{}
	for _, item := range items {
		classes[item.Class] = true
	}
	for _, class := range []string{"cnolibojkao", "ohciikgjghe", "oldfpclaabo", "agedjipcnpe"} {
		if !classes[class] {
			t.Fatalf("load sync missing %s", class)
		}
	}
	for _, item := range items {
		if item.Class == "agedjipcnpe" {
			idx := fieldIdxByCS(item.Class, csStrength)
			def, _ := s.mountedHookDef(ctx, rod.GUID)
			want := itemDefF32(def, gearAttrMaxLoad) * 0.2
			if math.Abs(float64(proto.F32FromBits(uint32(u64Of(item.Fields[idx].Num))))-want) > 0.0001 {
				t.Fatal("hook wear not synchronized with model")
			}
		}
	}
	if err := db.SetItemDurability(ctx, slots[catHook], 0); err != nil {
		t.Fatal(err)
	}
	if spec := s.loadGearSpecForRig(ctx, s.rigGearIDs(ctx, testUID, rod.GUID), rod.GUID); spec.BrokenPart != "hook" {
		t.Fatal("broken hook disappeared from weakest chain", spec)
	}
}
