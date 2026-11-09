package gameserver

// 工坊"同型号修理"回归：摩擦片的权威状态是 csDragForce，csDurability 只是
// 它的显示镜像（见 degradeFrictionWasherFields）。历史上修理分支只把镜像写回
// 1.0，导致磨穿的摩擦片修完仍报 100% 完好、制动力仍是 0、本体聚合按 0 判损坏，
// 工坊反复点修理扣 0 元且毫无变化。
// 工坊诊断（21/2）必须按制动力报告磨损，否则界面显示"零件没有磨损。无需更换"。

import (
	"math"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

func TestWorkshopRepairRestoresWasherDragForce(t *testing.T) {
	t.Parallel()
	srv, db := newStaticRigTestServer(t)
	defer db.Close()
	ctx := t.Context()
	const uid = int64(1000001)
	if err := srv.EnsurePlayer(ctx, uid); err != nil {
		t.Fatal(err)
	}
	profile, err := db.EnsureProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 1_000_000, profile.Gold, profile.Bank); err != nil {
		t.Fatal(err)
	}
	reel, err := srv.CreateItem(ctx, uid, "spin_5354", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	fields := snapshotFields(t, reel.FieldsJSON)
	washer, washerDef, _, ok := srv.selectedReelPart(ctx, fields, reelPartCatWasher)
	if !ok || washerDef == nil {
		t.Fatal("轮内应有摩擦片")
	}
	// 磨穿：制动力归零，但镜像耐久故意留在 1.0——这正是坏档的真实形态。
	setWireFloat(washer.Fields, washer.Class, fieldIdxByCS(washer.Class, csDragForce), 0)
	setReelChildDurability(washer, 1)
	raw := snapshotJSON(t, fields)
	if err := db.UpdateItemFields(ctx, reel.GUID, raw, reel.Qty, reel.Durability, reel.Workmanship, reel.RigGUID); err != nil {
		t.Fatal(err)
	}
	// 本体按聚合口径落成损坏态，与客户端红框一致。
	if err := db.SetItemDurability(ctx, reel.GUID, 0); err != nil {
		t.Fatal(err)
	}

	req := proto.Args524(proto.StrVal("workshop_01"),
		wiregen.MustMakeObj("nfmkdcbnkhp", map[int]*proto.Value{
			0: proto.Guid(reel.GUID),
			1: proto.Args(proto.S("503"), wiregen.MustMakeObj("gcllbdkgkna", map[int]*proto.Value{
				0: proto.U32(uint32(reelPartCatWasher)), 1: proto.StrVal(washerDef.ItemID),
			})),
			2: proto.U32(0), 3: proto.U64(0),
		}))
	c := &Ctx{Sess: newProbeSess(srv), Main: 21, Sub: 3, Tree: req, DecodeOK: true,
		ExpectResponse: true, Log: logging.Discard()}
	if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
		t.Fatal("同型号修理应返回更新后的物品树")
	}

	fresh := mustItem(t, db, reel.GUID)
	updated := snapshotFields(t, fresh.FieldsJSON)
	fixedWasher, _, _, ok := srv.selectedReelPart(ctx, updated, reelPartCatWasher)
	if !ok {
		t.Fatal("修理后摩擦片丢失")
	}
	rated := itemDefF32(washerDef, gearAttrDragMax) * fresh.Workmanship
	gotDrag, present, _ := frictionWasherDrag(fixedWasher)
	if !present || math.Abs(gotDrag-rated) > 1e-4 {
		t.Fatalf("修理后制动力=%g present=%v，期望额定 %g", gotDrag, present, rated)
	}
	// 本体耐久必须按聚合口径抬回来，否则客户端仍是损坏态、装不上竿。
	if fresh.Durability <= 0 {
		t.Fatalf("修理后本体耐久仍为 %g", fresh.Durability)
	}
	body, ok := srv.reelBodyDurabilityInFields(ctx, fresh, updated)
	if !ok || math.Abs(body-fresh.Durability) > 1e-6 {
		t.Fatalf("本体耐久 %g 与聚合 %g 不一致", fresh.Durability, body)
	}
	if srv.itemBrokenForMount(ctx, fresh) {
		t.Fatal("修理后仍被判为不可装配")
	}
}

func TestWorkshopDiagReportsWasherDragWear(t *testing.T) {
	t.Parallel()
	srv, db := newStaticRigTestServer(t)
	defer db.Close()
	ctx := t.Context()
	const uid = int64(1000001)
	if err := srv.EnsurePlayer(ctx, uid); err != nil {
		t.Fatal(err)
	}
	reel, err := srv.CreateItem(ctx, uid, "spin_5354", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	fields := snapshotFields(t, reel.FieldsJSON)
	washer, washerDef, _, ok := srv.selectedReelPart(ctx, fields, reelPartCatWasher)
	if !ok || washerDef == nil {
		t.Fatal("轮内应有摩擦片")
	}
	rated := itemDefF32(washerDef, gearAttrDragMax) * reel.Workmanship
	setWireFloat(washer.Fields, washer.Class, fieldIdxByCS(washer.Class, csDragForce), rated*0.25)
	setReelChildDurability(washer, 1) // 镜像仍是 1.0：不能据此报"无磨损"
	raw := snapshotJSON(t, fields)
	if err := db.UpdateItemFields(ctx, reel.GUID, raw, reel.Qty, reel.Durability, reel.Workmanship, reel.RigGUID); err != nil {
		t.Fatal(err)
	}

	req := proto.Args524(proto.StrVal("workshop_01"),
		wiregen.MustMakeObj("nfmkdcbnkhp", map[int]*proto.Value{
			0: proto.Guid(reel.GUID), 1: proto.Args(proto.S("503")),
			2: proto.U32(0), 3: proto.U64(0),
		}))
	c := &Ctx{Sess: newProbeSess(srv), Main: 21, Sub: 2, Tree: req, DecodeOK: true,
		ExpectResponse: true, Log: logging.Discard()}
	body, ok := hWorkshopDiag(c)
	if !ok || len(body) == 0 {
		t.Fatal("21/2 诊断未返回")
	}
	decoded, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	comps := decoded.Field(1)
	if comps.Val == nil || len(comps.Val.Items) == 0 {
		t.Fatal("诊断缺少零件数组")
	}
	for _, it := range comps.Val.Items {
		f0 := it.Field(0)
		if f0.GuidHex() != washerGUIDOf(t, srv, reel.GUID) {
			continue
		}
		condField := it.Field(1)
		cond := proto.F32FromBits(condField.U32())
		if math.Abs(float64(cond)-0.25) > 1e-3 {
			t.Fatalf("诊断完好度=%g，期望按制动力报 0.25", cond)
		}
		return
	}
	t.Fatal("诊断未包含摩擦片")
}

// washerGUIDOf 取轮内摩擦片子实例的 guid。
func washerGUIDOf(t *testing.T, srv *Server, reelGUID string) string {
	t.Helper()
	row := mustItem(t, srv.DB, reelGUID)
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	child, _, _, ok := srv.selectedReelPart(t.Context(), fields, reelPartCatWasher)
	if !ok {
		t.Fatal("缺少摩擦片")
	}
	idx := fieldIdxByCS(child.Class, csGUID)
	if idx < 0 || idx >= child.FieldCount() {
		t.Fatal("摩擦片缺少 guid 字段")
	}
	return child.Fields[idx].Hex
}
