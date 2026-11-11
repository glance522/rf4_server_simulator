package gameserver

// 渔轮磨损、断件与修理的生产链路回归：全部走真实入口（抛竿 14/7、断件上报 17/9、
// 工坊换件与进店修理 21/3）。手工构造磨损账的测试挡不住这类接线错误。

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

const reviewWearUID = int64(1000001)

// reviewPartDurabilities 读回轮内寿命件的当前耐久，供逐件比对。
func reviewPartDurabilities(t *testing.T, srv *Server, ctx context.Context, row *store.Item) map[int64]float64 {
	t.Helper()
	out := map[int64]float64{}
	for _, category := range reelWearPartCategories {
		_, _, durability := srv.reelPartStateFromRow(ctx, row, category)
		out[category] = durability
	}
	return out
}

// castRigWithReel 装配一套真实的竿加轮钓组并抛竿，返回竿状态与两件 GUID。
func castRigWithReel(t *testing.T, srv *Server, sid uint64) (*FishState, string, string) {
	t.Helper()
	ctx := t.Context()
	setWeatherTestTunable(t, srv, "bite_delay_fixed", "60")
	rod, err := srv.CreateItem(ctx, reviewWearUID, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	reel, err := srv.CreateItem(ctx, reviewWearUID, "spin_5820", "backpack", 1)
	if err != nil {
		t.Fatalf("造轮失败: %v", err)
	}
	sess := registerBiteTestSession(t, srv, sid, nil)
	mountGearForTest(t, srv, sess, rod.GUID, reel.GUID)
	st := srv.selectFishingRod(reviewWearUID, rod.GUID)
	if st.GearGuid != rod.GUID {
		t.Fatalf("选竿后状态未记住竿: %q", st.GearGuid)
	}
	if !srv.scheduleBite(sess, st) {
		t.Fatal("抛竿失败")
	}
	return st, rod.GUID, reel.GUID
}

// TestInvalidBreakReportLeavesReelUntouched 无效断件上报必须整个失败：既不能磨损
// 轮内零件，也不能把本竿的磨损账标记成已落库，否则真正的断件上报再来时断件冲击就记不上了。
func TestInvalidBreakReportLeavesReelUntouched(t *testing.T) {
	srv, db := newRigTestServer(t)
	t.Cleanup(func() { stopFishingRecoveryTimers(srv) })
	ctx := t.Context()
	st, rodGUID, reelGUID := castRigWithReel(t, srv, 731)

	// 本竿已经搏过一段：账上有打滑磨损，还没落库。
	st.mu.Lock()
	st.reelWear.SlipWear = reelSlipWear
	st.reelWear.SlipTicks = 1
	st.mu.Unlock()

	row := wearReelRow(t, db, ctx, reelGUID)
	bodyBefore := row.Durability
	partsBefore := reviewPartDurabilities(t, srv, ctx, row)

	// 背包里的饵既不是钓组件也不在允许的断件品类里，17/9 必须判无效。
	invalid, err := srv.CreateItem(ctx, reviewWearUID, "crankbaits_2017", "backpack", 1)
	if err != nil {
		t.Fatalf("造无效上报件失败: %v", err)
	}
	if _, err := srv.breakLineOrRod(ctx, reviewWearUID, rodGUID, invalid.GUID); err == nil {
		t.Fatal("无效上报件应被拒绝")
	}

	row = wearReelRow(t, db, ctx, reelGUID)
	if row.Durability != bodyBefore {
		t.Fatalf("无效上报不该磨本体: %.6f -> %.6f", bodyBefore, row.Durability)
	}
	for category, before := range partsBefore {
		if _, _, after := srv.reelPartStateFromRow(ctx, row, category); after != before {
			t.Fatalf("无效上报不该磨品类 %d: %.6f -> %.6f", category, before, after)
		}
	}
	st.mu.Lock()
	settled := st.reelWear.settled
	st.mu.Unlock()
	if settled {
		t.Fatal("无效上报不应把本竿磨损账标记成已落库")
	}
}

// TestEmptyRodRealCastSettlesNothing 钉住官服口径（底钓2 66min 空竿段全零件零磨损）：
// 整竿没收到过 14/8 的空竿没有任何磨损账，结算不落库、不推送、不掉耐久。
func TestEmptyRodRealCastSettlesNothing(t *testing.T) {
	srv, db := newRigTestServer(t)
	t.Cleanup(func() { stopFishingRecoveryTimers(srv) })
	ctx := t.Context()
	_, rodGUID, reelGUID := castRigWithReel(t, srv, 732)

	// 注册一个带真实隧道的在线会话：空竿结算后必须连 4/22 都不发（无事可推）。
	tunnelSess, peer := newTunnelProbe(t, srv)
	t.Cleanup(func() {
		srv.mu.Lock()
		delete(srv.sessions, tunnelSess.ID)
		srv.mu.Unlock()
	})
	srv.mu.Lock()
	srv.sessions[tunnelSess.ID] = tunnelSess
	srv.mu.Unlock()
	pushed := receiveTestPush(peer, 4, 22)

	// 一尾未上，直接收竿结算。
	srv.settleFightReelWear(ctx, reviewWearUID, rodGUID, "14/4")

	select {
	case env := <-pushed:
		if env == nil {
			t.Fatal("收到空推送帧")
		}
		t.Fatal("空竿结算不应推送任何 4/22（官服空竿零磨损、无账可推）")
	case <-time.After(500 * time.Millisecond):
		// 预期路径：没有推送。
	}

	row := wearReelRow(t, db, ctx, reelGUID)
	if row.Durability != 1 {
		t.Fatalf("空竿后本体耐久应保持 1，实得 %v", row.Durability)
	}
	for _, category := range reelWearPartCategories {
		if _, _, durability := srv.reelPartStateFromRow(ctx, row, category); durability != 1 {
			t.Fatalf("空竿后品类 %d 耐久应保持 1，实得 %v", category, durability)
		}
	}
}

// TestWorkshopPartSwapWritesPerformanceAfterBodyRecalc 工坊换件必须先按新零件把
// 本体耐久抬回来，再写渔轮物理参数表；先写表就会留下旧本体的承力，要等下次使用才追上。
func TestWorkshopPartSwapWritesPerformanceAfterBodyRecalc(t *testing.T) {
	srv, db := newRigTestServer(t)
	t.Cleanup(func() { _ = db.Close() })
	ctx := t.Context()
	if err := srv.EnsurePlayer(ctx, reviewWearUID); err != nil {
		t.Fatal(err)
	}
	profile, err := db.EnsureProfile(ctx, reviewWearUID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, reviewWearUID, 1000000, profile.Gold, profile.Bank); err != nil {
		t.Fatal(err)
	}
	reel, err := srv.CreateItem(ctx, reviewWearUID, "spin_5354", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	// 烧穿后本体那一格归零，轮内零件还很新：聚合值远高于本体。
	if err := db.SetItemDurability(ctx, reel.GUID, 0); err != nil {
		t.Fatal(err)
	}
	broken, serr := srv.syncReelPerformance(ctx, wearReelRow(t, db, ctx, reel.GUID))
	if serr != nil || broken == nil {
		t.Fatalf("烧穿后应能同步物理参数: %v", serr)
	}

	// 换一只同型号新齿轮：零件回满、本体抬回来，物理参数必须同一次跟上。
	encoded, err := proto.EncodeValue(snapshotWorkshopRequest(reel.GUID, "rgm_ef_spark10021"), srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := proto.DecodeValue(encoded, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	c := &Ctx{Sess: newProbeSess(srv), Main: 21, Sub: 3, Tree: decoded, DecodeOK: true, ExpectResponse: true, Log: logging.Discard()}
	if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
		t.Fatal("工坊换件失败")
	}

	stored, err := db.GetReelPerformance(ctx, reel.GUID)
	if err != nil || stored == nil {
		t.Fatalf("读物理参数表失败: %v", err)
	}
	afterRow := mustItem(t, db, reel.GUID)
	_, afterAgg := reelBodyFromStore(t, srv, db, ctx, reel.GUID)
	// 换上的必须是满耐久的新件：零件聚合与本体都应随之抬回来。
	if afterAgg <= 0 {
		t.Fatalf("换上新零件后零件聚合不应为 0，实得 %.4f", afterAgg)
	}
	if afterRow.Durability <= 0 {
		t.Fatalf("换上新零件后本体应抬回，实得 %.4f", afterRow.Durability)
	}
	fresh, err := srv.syncReelPerformance(ctx, wearReelRow(t, db, ctx, reel.GUID))
	if err != nil || fresh == nil {
		t.Fatalf("重算物理参数失败: %v", err)
	}
	if math.Abs(stored.Durability-fresh.Durability) > 1e-9 ||
		math.Abs(stored.LockedDragKg-fresh.LockedDragKg) > 1e-9 ||
		math.Abs(stored.CastFriction-fresh.CastFriction) > 1e-9 {
		t.Fatalf("换件后表里仍是旧本体：本体 %.4f 锁轮承力 %.4f 摩擦 %.4f，按新本体应为本体 %.4f 锁轮承力 %.4f 摩擦 %.4f",
			stored.Durability, stored.LockedDragKg, stored.CastFriction,
			fresh.Durability, fresh.LockedDragKg, fresh.CastFriction)
	}
}

// TestRepairAbortsWhenReelInternalsFail 进店修理渔轮时，轮内零件恢复写库失败就不能扣费
// 也不能把本体写回满值，否则会出现修好外表但摩擦片仍是 0 的状态。
func TestRepairAbortsWhenReelInternalsFail(t *testing.T) {
	srv, db := newRigTestServer(t)
	t.Cleanup(func() { _ = db.Close() })
	ctx := t.Context()
	if err := srv.EnsurePlayer(ctx, reviewWearUID); err != nil {
		t.Fatal(err)
	}
	profile, err := db.EnsureProfile(ctx, reviewWearUID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, reviewWearUID, 1000000, profile.Gold, profile.Bank); err != nil {
		t.Fatal(err)
	}
	reel, err := srv.CreateItem(ctx, reviewWearUID, "spin_5354", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	// 先烧掉摩擦片：不烧的话轮内没有要恢复的东西，恢复会直接判定无事可做。
	row := wearReelRow(t, db, ctx, reel.GUID)
	fields := snapshotFields(t, row.FieldsJSON)
	washer, _, _, ok := srv.selectedReelPart(ctx, fields, reelPartCatWasher)
	if !ok {
		t.Fatal("轮内应有摩擦片")
	}
	setWireFloat(washer.Fields, washer.Class, fieldIdxByCS(washer.Class, csDragForce), 0)
	if err := db.UpdateItemFields(ctx, reel.GUID, snapshotJSON(t, fields), row.Qty, row.Durability, row.Workmanship, row.RigGUID); err != nil {
		t.Fatal(err)
	}
	if err := db.SetItemDurability(ctx, reel.GUID, 0.6); err != nil {
		t.Fatal(err)
	}
	// 注入：轮内零件写库失败。
	if _, err := db.SQL.ExecContext(ctx, fmt.Sprintf(
		`CREATE TRIGGER fail_reel_fields BEFORE UPDATE OF fields_json ON item FOR EACH ROW WHEN OLD.guid='%s' BEGIN SELECT RAISE(ABORT,'注入轮内零件写库失败'); END`, reel.GUID)); err != nil {
		t.Fatalf("注入失败: %v", err)
	}
	t.Cleanup(func() {
		if _, derr := db.SQL.ExecContext(context.Background(), `DROP TRIGGER fail_reel_fields`); derr != nil {
			t.Logf("清理注入失败: %v", derr)
		}
	})

	// 自检：确认注入真的会拦住这一次零件写库。
	if _, uerr := db.SQL.ExecContext(ctx, `UPDATE item SET fields_json = fields_json WHERE guid=?`, reel.GUID); uerr == nil {
		t.Fatal("注入未生效：零件字段写库没有失败")
	}

	before, err := db.GetProfile(ctx, reviewWearUID)
	if err != nil {
		t.Fatal(err)
	}
	repair := proto.Args524(proto.StrVal("workshop_01"), wiregen.MustMakeObj("nfmkdcbnkhp", map[int]*proto.Value{
		0: proto.Guid(reel.GUID), 1: proto.Args524(proto.U32(52)), 2: proto.U32(0), 3: proto.U64(0)}))
	c := &Ctx{Sess: newProbeSess(srv), Main: 21, Sub: 3, Tree: repair, DecodeOK: true, Log: logging.Discard()}
	if _, ok := hWorkshopRepair(false)(c); ok {
		t.Fatal("轮内零件恢复失败时修理不应成功")
	}
	after, err := db.GetProfile(ctx, reviewWearUID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Silver != before.Silver {
		t.Fatalf("修理失败不应扣费: %v -> %v", before.Silver, after.Silver)
	}
	if row := wearReelRow(t, db, ctx, reel.GUID); math.Abs(row.Durability-0.6) > 1e-9 {
		t.Fatalf("修理失败后本体耐久应保持 0.6，实得 %v", row.Durability)
	}
}
