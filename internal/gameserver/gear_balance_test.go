package gameserver

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func newBalanceTestServer(t *testing.T) (*Server, *store.DB) {
	t.Helper()
	db, err := store.OpenWithStatic(filepath.Join(t.TempDir(), "balance.db"), filepath.Join("..", "..", "server_data", "static.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	return NewServerForGearTest(t, db), db
}

func TestReelModelLoadsUseExactDefinitions(t *testing.T) {
	s, db := newBalanceTestServer(t)
	ctx := context.Background()
	if len(reelLockedLoads) != 544 {
		t.Fatal(len(reelLockedLoads))
	}
	for id, load := range reelLockedLoads {
		def, err := db.GetItemDefByItemID(ctx, id)
		if err != nil || def == nil || def.Category != gearCatReel || !isFinitePositive(load) {
			t.Fatalf("invalid reel load: %s %g %v", id, load, err)
		}
	}
	for id, want := range map[string]float64{"spin_5132": 10, "spin_5140": 99, "spin_5152": 62, "spin_5820": 3.4} {
		row, err := s.CreateItem(ctx, testUID, id, "backpack", 1)
		if err != nil {
			t.Fatal(err)
		}
		perf, err := s.syncReelPerformance(ctx, row)
		if err != nil || math.Abs(perf.LockedDragKg-want) > 1e-6 {
			t.Fatalf("%s: %+v %v", id, perf, err)
		}
	}
}

func TestLeaderIncludesEmbeddedConnectorAndSharedWeakestLink(t *testing.T) {
	def := &store.ItemDef{AttrsJSON: attrsJSON(t, map[string]any{
		gearAttrLeaderIn:   map[string]any{gearAttrMaxLoad: f32Attr(40)},
		gearAttrLeaderLock: map[string]any{gearAttrMaxLoad: f32Attr(15)},
	})}
	if got := leaderStrengthKg(def); got != 15 {
		t.Fatal(got)
	}
	for _, tc := range []struct {
		spec gearPhysicsSpec
		part string
		kg   float64
	}{
		{gearPhysicsSpec{RodPowerKg: 20, LineStrengthKg: 10}, "line", 10},
		{gearPhysicsSpec{RodPowerKg: 20, LineStrengthKg: 10, ConnectorStrengthKg: 5}, "leader_lock", 5},
		{gearPhysicsSpec{RodPowerKg: 20, LineStrengthKg: 10, HookPresent: true, HookStrengthKg: 3}, "hook", 3},
		{gearPhysicsSpec{RodPowerKg: 20, LineStrengthKg: 10, ReelPresent: true, ReelDragKg: 4}, "reel", 4},
	} {
		if gearWeakestPart(tc.spec) != tc.part || gearBreakLimitKg(tc.spec) != tc.kg {
			t.Fatalf("%+v", tc)
		}
	}
}

func TestWeakHookBreakKeepsRodReelLineLeaderAndFloat(t *testing.T) {
	s, db := newBalanceTestServer(t)
	ctx := context.Background()
	rod, _ := s.CreateItem(ctx, testUID, "tele_10180_4", "backpack", 1)
	sess := newProbeSess(s)
	var hookGUID string
	for _, id := range []string{"mono_15095", "fc_leader_40048", "clas_20163", "worm_01", "bob_25001"} {
		row, err := s.CreateItem(ctx, testUID, id, "backpack", 1)
		if err != nil {
			t.Fatal(err)
		}
		res := mountGearForTest(t, s, sess, rod.GUID, row.GUID)
		if id == "clas_20163" {
			hookGUID = res.MountedGUID
		}
	}
	if err := db.SetItemDurability(ctx, hookGUID, 0.01); err != nil {
		t.Fatal(err)
	}
	before := rigSlotGUIDs(t, s, rod.GUID)
	length := lineLengthOfTestItem(t, s, before[catLine])
	dec, err := s.resolveGearBreak(ctx, testUID, rod.GUID, rod.GUID)
	if err != nil || dec.Part != "hook" || dec.GUID != hookGUID {
		t.Fatalf("%+v %v", dec, err)
	}
	out, err := s.breakLineOrRod(ctx, testUID, rod.GUID, rod.GUID)
	if err != nil || out.BreakPart != "hook" || !out.LineBroken || out.LineLossM != 0 {
		t.Fatalf("%+v %v", out, err)
	}
	for _, cat := range []int64{catLine, catLead, catFloat} {
		if row, err := db.GetItemByGUID(ctx, before[cat]); err != nil || row == nil {
			t.Fatalf("hook break lost slot %d: %v", cat, err)
		}
	}
	if row, _ := db.GetItemByGUID(ctx, hookGUID); row != nil {
		t.Fatal("broken hook retained")
	}
	if lineLengthOfTestItem(t, s, before[catLine]) != length {
		t.Fatal("hook break shortened main line")
	}
	if slots := rigSlotGUIDs(t, s, rod.GUID); slots[catHook] != "" || slots[catBait] != "" {
		t.Fatal("broken hook/bait still mounted")
	}
	obj, err := buildGearBreakResult(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proto.EncodeValue(obj, s.Table()); err != nil {
		t.Fatal(err)
	}
}

func TestLureHookBreakRetainsLureAndRemainingHooks(t *testing.T) {
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
	hook := s.mountedHookGuid(ctx, rod.GUID)
	if hook == "" {
		t.Fatal("no factory hook")
	}
	before, _ := db.GetItemByGUID(ctx, lure.GUID)
	fields, _ := FieldsFromJSON(before.FieldsJSON)
	count := len(fields[0].Val.Items)
	out, err := s.breakLineTx(ctx, testUID, rod.GUID, hook, catHook)
	if err != nil {
		t.Fatal(err)
	}
	if out.BreakPart != "hook" || out.LineLossM != 0 {
		t.Fatalf("%+v", out)
	}
	after, err := db.GetItemByGUID(ctx, lure.GUID)
	if err != nil || after == nil {
		t.Fatalf("lost lure: %v", err)
	}
	fields, _ = FieldsFromJSON(after.FieldsJSON)
	if len(fields[0].Val.Items) != count-1 {
		t.Fatal("factory hook removal failed")
	}
	if s.mountedHookGuid(ctx, rod.GUID) == hook {
		t.Fatal("broken hook still active")
	}
	// 读取物品不会重新生成已断的出厂钩。
	if _, err := s.ItemToTree(ctx, after); err != nil {
		t.Fatal(err)
	}
	if s.mountedHookGuid(ctx, rod.GUID) == hook {
		t.Fatal("read resurrected broken hook")
	}
}
