package gameserver

// 断件后果的回归用例：
//   - 清杯断主线：主线整条丢失，主线下方的引线、钩、饵全没，竿轮不动；
//   - 断轮：轮子耐久归零留在竿上，钓组原样保留、鱼仍咬钩，只是收不动线；
//   - 断竿：竿子断梢——竿本体（0 耐久）与竿上的轮子保留，主线只按上报放线
//     米数扣减（扣剩的线留在断竿上），挂线配件全部丢失；
//   - 强度随磨损线性降低（1% 损耗 = 1% 强度损失），见 gear_physics_test 半耐久用例。

import (
	"context"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

// TestMainLineBreakLosesWholeRig 清杯：主线断=主线整条 + 引线/钩/饵全丢，竿轮不动。
func TestMainLineBreakLosesWholeRig(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15095", "backpack", 1) // 5kg，最弱环
	if err != nil {
		t.Fatal(err)
	}
	hook, err := srv.CreateItem(ctx, uid, "c_hook_20748", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	bait, err := srv.CreateItem(ctx, uid, "worm_01", "backpack", 3)
	if err != nil {
		t.Fatal(err)
	}
	srv.setStackCount(ctx, bait, 3)
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, hook.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, bait.GUID)
	mountedBait := rigSlotGUIDs(t, srv, rod.GUID)[catBait]
	if mountedBait == "" {
		t.Fatal("饵未装配")
	}

	out, err := srv.breakLineOrRod(ctx, uid, rod.GUID, line.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if !out.LineBroken || out.BreakPart != "line" {
		t.Fatalf("应断主线: %+v", out)
	}
	for _, guid := range []string{line.GUID, hook.GUID, mountedBait} {
		if row, _ := db.GetItemByGUID(ctx, guid); row != nil {
			t.Fatalf("清杯后 %s 应丢失销毁，实得 %+v", guid, row)
		}
		if !containsGUID(out.RemovedGUIDs, guid) {
			t.Fatalf("移除列表缺少 %s: %+v", guid, out.RemovedGUIDs)
		}
	}
	// 竿与竿上引用不受影响，只是 f3 主线引用清空。
	freshRod := mustItem(t, db, rod.GUID)
	if freshRod.Durability <= 0 {
		t.Fatalf("断主线不应伤到竿: %v", freshRod.Durability)
	}
	fields, _ := FieldsFromJSON(freshRod.FieldsJSON)
	if i := fieldIdxByCS(freshRod.WireClass, csLine); i >= 0 && i < len(fields) && fields[i].GuidHex() != "" {
		t.Fatal("断主线后竿 f3 应清空主线引用")
	}
	if got := rigSlotGUIDs(t, srv, rod.GUID); got[catLine] != "" || got[catHook] != "" {
		t.Fatalf("断主线后钓组应清空: %+v", got)
	}
}

// TestReportedReelBreakKeepsFishHooked 官服断轮：轮坏、钓组在、鱼还咬着钩。
func TestReportedReelBreakKeepsFishHooked(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "spin_10001_S60L", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	reel, err := srv.CreateItem(ctx, uid, "spin_5820", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15249", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, reel.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)

	st := srv.stateFor(uid)
	st.mu.Lock()
	st.GearGuid = rod.GUID
	st.Pending = &RolledFish{FishGUID: "33333333333333333333333333333333", Species: "perch", WeightG: 500}
	st.Hooked = true
	// 搏鱼中的状态：CastActive 为真才不是"已落网待入护"的待处理渔获。
	st.CastActive = true
	st.Resolved = false
	st.mu.Unlock()

	tree := rigRequestTree(t, proto.Guid(rod.GUID), proto.Guid(reel.GUID), proto.U32(2), proto.Null())
	body, ok := hDynamicRodBreak(&Ctx{Sess: sess, Main: 17, Sub: 9, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatal("17/9 断轮未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if u64Of(dec.Fields[0].Num) != 0 {
		t.Fatal("断轮响应 f0（LineBroken）应为 false")
	}
	st.mu.Lock()
	hooked, resolved, pending, gear := st.Hooked, st.Resolved, st.Pending, st.GearGuid
	st.mu.Unlock()
	if !hooked || resolved || pending == nil {
		t.Fatalf("官服断轮后鱼应仍咬钩: hooked=%v resolved=%v pending=%v", hooked, resolved, pending != nil)
	}
	if gear != rod.GUID {
		t.Fatalf("断轮后竿应仍在手: %s", gear)
	}
	// 钓组原样保留。
	if got := rigSlotGUIDs(t, srv, rod.GUID); got[catLine] != line.GUID || got[catReel] != reel.GUID {
		t.Fatalf("断轮后钓组应原样保留: %+v", got)
	}
	if row, _ := db.GetItemByGUID(ctx, line.GUID); row == nil || row.RigGUID != rod.GUID {
		t.Fatalf("断轮后主线应仍在竿上: %+v", row)
	}
	// 轮坏后不能再次抛投。
	if got := srv.rigBrokenReelGUID(ctx, uid, rod.GUID); got != reel.GUID {
		t.Fatalf("断轮后应拦截抛投，rigBrokenReelGUID=%q", got)
	}
}
