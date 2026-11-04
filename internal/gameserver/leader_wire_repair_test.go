package gameserver

import (
	"context"
	"path/filepath"
	"testing"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func TestLeaderWireDefForLineMapping(t *testing.T) {
	srv, db := newLeaderTestServer(t)
	ctx := context.Background()

	cases := map[string]string{
		"mono_15245": "wrl_sim_monorig_4LB",
		"mono_15247": "wrl_sim_monorig_7.5LB",
		"mono_15249": "wrl_sim_monorig_15LB",
	}
	for source, want := range cases {
		lineDef, err := db.GetItemDefByItemID(ctx, source)
		if err != nil {
			t.Fatal(err)
		}
		got, err := srv.leaderWireDefForLine(ctx, lineDef)
		if err != nil {
			t.Fatalf("%s 映射失败: %v", source, err)
		}
		if got.ItemID != want {
			t.Fatalf("%s 映射=%s，期望 %s", source, got.ItemID, want)
		}
	}

	unmapped, err := db.GetItemDefByItemID(ctx, "mono_15065")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.leaderWireDefForLine(ctx, unmapped); err == nil {
		t.Fatal("mono_15065 没有参数一致的前导线定义，必须拒绝映射")
	}
}

func TestRepairCraftedLeaderWireMapsLegacyLineAndQuarantinesUnknown(t *testing.T) {
	srv, db := newLeaderTestServer(t)
	ctx := context.Background()
	const uid = int64(2000042)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}

	repairable := makeLegacyCraftedLeader(t, srv, db, uid, "mono_15247")
	unrepairable := makeLegacyCraftedLeader(t, srv, db, uid, "mono_15065")

	if err := srv.RepairItemPhysics(ctx); err != nil {
		t.Fatal(err)
	}

	fixed, err := db.GetItemByGUID(ctx, repairable)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := FieldsFromJSON(fixed.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	line := fields[fieldIdxByCS(fixed.WireClass, gearAttrLeaderIn)].Val
	if line == nil || line.Kind != proto.KindObj {
		t.Fatalf("修复后缺少内嵌线材: %+v", line)
	}
	if got := u64Of(line.Fields[fieldIdxByCS(line.Class, csNumericID)].Num); got != 16064 {
		t.Fatalf("修复后内线 numeric_id=%d，期望 16064", got)
	}
	if got := wireFloatForTest(t, line, csStrength); got < 3.39 || got > 3.41 {
		t.Fatalf("修复后内线强度=%f，期望 3.4kg", got)
	}

	hidden, err := db.GetItemByGUID(ctx, unrepairable)
	if err != nil {
		t.Fatal(err)
	}
	if hidden.Container != craftedLeaderQuarantineContainer {
		t.Fatalf("无法映射的引线容器=%s，期望 %s", hidden.Container, craftedLeaderQuarantineContainer)
	}
}

func newLeaderTestServer(t *testing.T) (*Server, *store.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "leader-wire.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	importStaticForSkillsCraft(t, db, "item_def")
	return newSkillsCraftServer(t, db), db
}

func makeLegacyCraftedLeader(t *testing.T, srv *Server, db *store.DB, uid int64, lineItemID string) string {
	t.Helper()
	ctx := context.Background()
	row, err := srv.prepareNewItem(ctx, uid, "leader_crafted_carp_50", "backpack", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	lineDef, err := db.GetItemDefByItemID(ctx, lineItemID)
	if err != nil {
		t.Fatal(err)
	}
	material := craftedMaterial{
		row: &store.Item{GUID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Workmanship: 1, Durability: 1},
		def: lineDef,
	}
	line, err := srv.craftedMaterialObject(row, material, gearAttrLeaderIn, 50)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	fields[fieldIdxByCS(row.WireClass, gearAttrLeaderIn)].Val = line
	setWireFloat(fields, row.WireClass, fieldIdxByCS(row.WireClass, csStrength),
		itemDefF32(lineDef, gearAttrMaxLoad))
	row.FieldsJSON, err = FieldsToJSON(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertItem(ctx, row); err != nil {
		t.Fatal(err)
	}
	return row.GUID
}
