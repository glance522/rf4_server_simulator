package gameserver

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/store"
)

func baitTestMount(t *testing.T, s *Server, rod, id string, qty, index int64) (string, string) {
	t.Helper()
	item, err := s.CreateItem(t.Context(), 1000001, id, "backpack", qty)
	if err != nil {
		t.Fatal(err)
	}
	if s.stackableItem(t.Context(), item) {
		s.setStackCount(t.Context(), item, qty)
	}
	r := s.rigSetComponent(t.Context(), 1000001, rod, 0, item.GUID, index, rigSetOpts{ParentType: familyRod, EnforceCategory: true, SplitStack: true})
	if r.Rejected || r.MountedGUID == "" {
		t.Fatalf("mount %s: %+v", id, r)
	}
	return item.GUID, r.MountedGUID
}

func baitTestRod(t *testing.T, s *Server) string {
	t.Helper()
	rod, err := s.CreateItem(t.Context(), 1000001, "feeder_10126_FD390", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	// 钉在路亚族 SimpleJig：钩槽与沉底族一致（idx 250），
	// 保持本文件验证的逐份拆分配饵路径。
	if !s.rigSetType(t.Context(), 1000001, rod.GUID, 6) {
		t.Fatal("rigSetType 失败")
	}
	return rod.GUID
}

func TestBaitConsumptionReserveReplayEmptyAndRestart(t *testing.T) {
	s, db := newRigTestServer(t)
	rod := baitTestRod(t, s)
	// 补饵以钓组仍有钩为前提；无钩钓组的补饵请求按无效处理。
	mountHookForBaitTest(t, s, rod, 0)
	source, child := baitTestMount(t, s, rod, "worm_01", 5, 350)
	use := func(server *Server, event string) {
		t.Helper()
		if _, err := server.consumeBaitEvent(t.Context(), 1000001, rod, event, []string{child}, false); err != nil {
			t.Fatal(err)
		}
	}
	use(s, "bite-1")
	use(s, "bite-1")
	row, _ := db.GetItemByGUID(t.Context(), source)
	if row == nil || row.Qty != 4 || baitQuantity(row) != 4 {
		t.Fatalf("mount reserved 1, eating it must leave 4 in stock, source=%+v", row)
	}
	// 从数据库重开服务后同一咬口不能再次扣除，pending 的饵快照也须保留。
	f := &RolledFish{FishGUID: "pending", BaitsKnown: true, BaitGUIDs: child}
	st := s.stateFor(1000001)
	st.mu.Lock()
	st.GearGuid = rod
	st.Pending = f
	st.mu.Unlock()
	path := db.Path
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Migrate(); err != nil {
		t.Fatal(err)
	}
	server := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "bait-restart"}}, logging.Discard(), nil)
	server.SetDB(reopened)
	if restored := server.stateFor(1000001).Pending; restored == nil || !restored.BaitsKnown || restored.BaitGUIDs != child {
		t.Fatal("lost bait snapshot on restart")
	}
	use(server, "bite-1")
	for i := 2; i <= 5; i++ {
		if _, err := server.reloadBaitForCast(t.Context(), 1000001, rod); err != nil {
			t.Fatal(err)
		}
		snapshot, _ := server.biteBaitSnapshot(t.Context(), rod)
		ids := strings.Fields(snapshot)
		if len(ids) != 1 {
			t.Fatalf("reload did not mount bait: %v", ids)
		}
		child = ids[0]
		use(server, fmt.Sprintf("bite-%d", i))
	}
	if row, _ := reopened.GetItemByGUID(t.Context(), source); row != nil {
		t.Fatalf("source remained: %+v", row)
	}
	if row, _ := reopened.GetItemByGUID(t.Context(), child); row != nil {
		t.Fatalf("eaten bait remained: %+v", row)
	}
	if len(server.currentBaits(t.Context(), rod)) != 0 {
		t.Fatal("empty rig still has bait")
	}
	if server.canFishWithBait(t.Context(), 1000001, rod) {
		t.Fatal("empty hook produced fish with both switches off")
	}
}

func TestBaitConsumptionCombinationAndRollback(t *testing.T) {
	s, db := newRigTestServer(t)
	rod := baitTestRod(t, s)
	_, a := baitTestMount(t, s, rod, "worm_01", 1, 350)
	bItem, err := s.CreateItem(t.Context(), 1000001, "muckworm", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	s.setStackCount(t.Context(), bItem, 1)
	b := bItem.GUID
	if err := db.SetItemRigLink(t.Context(), b, rod, 27); err != nil {
		t.Fatal(err)
	}
	s.rigComponentWriteAt(t.Context(), rod, 27, 1, b, 351)
	if _, err := db.SQL.Exec(fmt.Sprintf(`CREATE TRIGGER fail_bait_delete BEFORE DELETE ON item WHEN old.guid='%s' BEGIN SELECT RAISE(ABORT,'test'); END`, b)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.consumeBaitEvent(t.Context(), 1000001, rod, "both", []string{a, b}, false); err == nil {
		t.Fatal("expected rollback")
	}
	if row, _ := db.GetItemByGUID(t.Context(), a); row == nil {
		t.Fatal("first bait was deducted despite rollback")
	}
	var receipts int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM txn WHERE kind='bait_bite' AND guid='both'`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("failed event recorded: %d %v", receipts, err)
	}
	if _, err := db.SQL.Exec(`DROP TRIGGER fail_bait_delete`); err != nil {
		t.Fatal(err)
	}
	change, err := s.consumeBaitEvent(t.Context(), 1000001, rod, "both", []string{a, b, a}, false)
	if err != nil || len(change.Removed) != 2 {
		t.Fatalf("combination consumption=%+v %v", change, err)
	}
	if len(s.currentBaits(t.Context(), rod)) != 0 {
		t.Fatal("consumed combo remained mounted")
	}
	if _, err := s.consumeBaitEvent(t.Context(), 1000001, rod, "both", []string{a, b}, false); err != nil {
		t.Fatal(err)
	}
}

func TestBaitConsumptionReusableAndAllStaticTypes(t *testing.T) {
	s, db := newRigTestServer(t)
	rows, err := db.SQL.Query(`SELECT numeric_id,item_id,category,attrs_json FROM item_def WHERE category IN (17,27)`)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for rows.Next() {
		def := store.ItemDef{}
		if err := rows.Scan(&def.NumericID, &def.ItemID, &def.Category, &def.AttrsJSON); err != nil {
			t.Fatal(err)
		}
		consumed, wear := baitUseRule(&def, false)
		if def.Category == 17 && consumed {
			t.Fatalf("reusable lure was treated as edible: %s", def.ItemID)
		}
		if def.Category == 27 && !consumed && wear == 0 {
			t.Fatalf("unhandled bait: %s", def.ItemID)
		}
		checked++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if checked < 3000 {
		t.Fatalf("checked only %d definitions", checked)
	}
	for _, tt := range []struct {
		id   string
		wear float64
	}{{"zbb_mm_bg_13", 0.005}, {"softplastic_2627", 0.02}, {"spinnerbaits_1244", 0}} {
		rod := baitTestRod(t, s)
		_, child := baitTestMount(t, s, rod, tt.id, 3, 350)
		before, _ := db.GetItemByGUID(t.Context(), child)
		if _, err := s.consumeBaitEvent(t.Context(), 1000001, rod, "use-"+tt.id, []string{child}, false); err != nil {
			t.Fatal(err)
		}
		after, _ := db.GetItemByGUID(t.Context(), child)
		if after == nil || after.Qty != before.Qty || math.Abs(after.Durability-(before.Durability-tt.wear)) > 1e-6 {
			t.Fatalf("%s before=%+v after=%+v", tt.id, before, after)
		}
	}
}

func TestBaitConsumptionCastSuppliesAndForeignRig(t *testing.T) {
	s, db := newRigTestServer(t)
	rod := baitTestRod(t, s)
	_, food := baitTestMount(t, s, rod, "worm_01", 1, 350)
	_, pva := baitTestMount(t, s, rod, "pva_stick", 1, 0)
	_, feed := baitTestMount(t, s, rod, "feed_lesh", 1, 0)
	other := baitTestRod(t, s)
	_, foreign := baitTestMount(t, s, other, "muckworm", 1, 350)
	if _, err := s.consumeBaitEvent(context.Background(), 1000001, rod, "cast-1", []string{food, pva, feed, foreign}, true); err != nil {
		t.Fatal(err)
	}
	_, replacement := baitTestMount(t, s, rod, "pva_stick", 1, 0)
	if _, err := s.consumeBaitEvent(t.Context(), 1000001, rod, "cast-1", []string{replacement}, true); err != nil {
		t.Fatal(err)
	}
	if row, _ := db.GetItemByGUID(t.Context(), replacement); row == nil {
		t.Fatal("repeated water frame consumed newly loaded supply")
	}
	for _, guid := range []string{pva, feed} {
		if row, _ := db.GetItemByGUID(t.Context(), guid); row != nil {
			t.Fatal("cast supply remained")
		}
	}
	for _, guid := range []string{food, foreign} {
		if row, _ := db.GetItemByGUID(t.Context(), guid); row == nil {
			t.Fatal("cast consumed hook bait or foreign rig")
		}
	}
	if ids, known := s.biteBaitSnapshot(t.Context(), rod); !known || !strings.Contains(ids, food) {
		t.Fatal("missing bite snapshot")
	}
}

func TestBaitConsumptionLastReservedUnitReturnsAndZeroIsEmpty(t *testing.T) {
	s, db := newRigTestServer(t)
	rod := baitTestRod(t, s)
	// 补饵以钓组仍有钩为前提；无钩钓组的补饵请求按无效处理。
	mountHookForBaitTest(t, s, rod, 0)
	source, child := baitTestMount(t, s, rod, "worm_01", 2, 350)
	if _, err := s.consumeBaitEvent(t.Context(), 1000001, rod, "one", []string{child}, false); err != nil {
		t.Fatal(err)
	}
	if row, _ := db.GetItemByGUID(t.Context(), source); row == nil || baitQuantity(row) != 1 {
		t.Fatal("unused spare was consumed during the fight")
	}
	if _, err := s.reloadBaitForCast(t.Context(), 1000001, rod); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := s.biteBaitSnapshot(t.Context(), rod)
	child = strings.Fields(snapshot)[0]
	if result := s.rigRemoveItemComponent(t.Context(), 1000001, rod, child); result == nil {
		t.Fatal("detach failed")
	}
	row, _ := db.GetItemByGUID(t.Context(), child)
	if row == nil || row.Container != "backpack" || baitQuantity(row) != 1 || splitSourceGUID(row) != "" {
		t.Fatal("uneaten last unit was lost or refunded twice")
	}
	_, empty := baitTestMount(t, s, rod, "muckworm", 1, 350)
	row, _ = db.GetItemByGUID(t.Context(), empty)
	s.setStackCount(t.Context(), row, 0)
	if len(s.currentBaits(t.Context(), rod)) != 0 {
		t.Fatal("zero quantity was interpreted as a full package")
	}
	if _, err := s.consumeBaitEvent(t.Context(), 1000001, rod, "empty", []string{empty}, false); err == nil {
		t.Fatal("empty bait consumption succeeded")
	}
}

func TestBaitConsumptionThroughBiteAndReplay(t *testing.T) {
	s, db := newRigTestServer(t)
	rod := baitTestRod(t, s)
	// 补饵以钓组仍有钩为前提；无钩钓组的补饵请求按无效处理。
	mountHookForBaitTest(t, s, rod, 0)
	source, _ := baitTestMount(t, s, rod, "worm_01", 5, 350)
	addFishingRecoverySession(s, 1000001, 939)
	st := s.stateFor(1000001)
	st.mu.Lock()
	st.GearGuid = rod
	st.Loc = "unknown-map"
	st.Bait = "worm_01"
	st.CastActive = true
	st.InWater = true
	st.CastTs = time.Now().Add(-time.Minute)
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	st.biteTimer = timer
	st.mu.Unlock()
	s.onBiteTimer(1000001)
	st.mu.Lock()
	pending := st.Pending
	st.mu.Unlock()
	if pending == nil || !pending.BaitsKnown {
		t.Fatalf("real bite did not save its bait snapshot: %+v", pending)
	}
	row, _ := db.GetItemByGUID(t.Context(), source)
	if row == nil || baitQuantity(row) != 4 || len(s.currentBaits(t.Context(), rod)) != 1 {
		t.Fatal("fish push consumed bait before the client confirmed a hook")
	}
	s.repushPending14_14(1000001)
	row, _ = db.GetItemByGUID(t.Context(), source)
	if row == nil || baitQuantity(row) != 4 || len(s.currentBaits(t.Context(), rod)) != 1 {
		t.Fatal("replaying fish consumed bait before hook confirmation")
	}
	sess := s.liveSessionByUID(1000001)
	if _, ok := hFishingHooked(&Ctx{Sess: sess, Main: 14, Sub: 11, Log: logging.Discard()}); !ok {
		t.Fatal("14/11 hook confirmation was not handled")
	}
	row, _ = db.GetItemByGUID(t.Context(), source)
	if row == nil || baitQuantity(row) != 4 {
		t.Fatal("hook confirmation did not consume exactly one unit")
	}
	if _, ok := hFishingHooked(&Ctx{Sess: sess, Main: 14, Sub: 11, Log: logging.Discard()}); !ok {
		t.Fatal("replayed 14/11 hook confirmation was not handled")
	}
	row, _ = db.GetItemByGUID(t.Context(), source)
	if row == nil || baitQuantity(row) != 4 {
		t.Fatal("replayed hook confirmation consumed another bait")
	}
	if len(s.currentBaits(t.Context(), rod)) != 0 {
		t.Fatal("next bait was mounted during the fight")
	}
	if err := sess.prepareBaitForCast(st, rod); err != nil {
		t.Fatal(err)
	}
	row, _ = db.GetItemByGUID(t.Context(), source)
	if row == nil || baitQuantity(row) != 4 {
		t.Fatal("preparation during fight reserved another bait")
	}
	st.mu.Lock()
	st.CastActive = false
	st.mu.Unlock()
	if err := sess.prepareBaitForCast(st, rod); err != nil {
		t.Fatal(err)
	}
	row, _ = db.GetItemByGUID(t.Context(), source)
	if row == nil || baitQuantity(row) != 3 || len(s.currentBaits(t.Context(), rod)) != 1 {
		t.Fatal("next cast did not reserve exactly one replacement")
	}
}

func TestMarineBaitConsumptionReloadsAllSlotsInOrder(t *testing.T) {
	s, db := newRigTestServer(t)
	ctx := t.Context()
	const uid int64 = 1000001
	rod, err := s.CreateItem(ctx, uid, "boatr_10222_SBH80_100", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !s.rigSetType(ctx, uid, rod.GUID, 59) {
		t.Fatal("marine rig rejected")
	}
	ids := []string{"worm_01", "muckworm", "fly"}
	for i := range ids {
		mountHookForBaitTest(t, s, rod.GUID, int64(i))
	}
	for i, id := range ids {
		baitTestMount(t, s, rod.GUID, id, 5, int64(350+i))
	}
	snapshot, known := s.biteBaitSnapshot(ctx, rod.GUID)
	if !known || len(strings.Fields(snapshot)) != len(ids) {
		t.Fatalf("snapshot=%q", snapshot)
	}
	change, err := s.consumeBaitEvent(ctx, uid, rod.GUID, "marine-bite", strings.Fields(snapshot), false)
	if err != nil || len(change.Removed) != len(ids) {
		t.Fatalf("deduction=%+v err=%v", change, err)
	}
	if _, err := s.reloadBaitForCast(ctx, uid, rod.GUID); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		guid := s.storedRigComponents(ctx, rod.GUID)[rigCompKey(catBait, int64(i))].GUID
		row, err := db.GetItemByGUID(ctx, guid)
		if err != nil || row == nil || row.ItemID != id {
			t.Fatalf("slot %d: row=%+v want=%s err=%v", i, row, id, err)
		}
	}
}

// TestMountBaitRefreshesSourceStackForClient 锁定：计件饵挂载拆出子实例后，
// 来源背包栈的新数量必须能刷新到客户端（回包后再发一次移除+重加）。
func TestMountBaitRefreshesSourceStackForClient(t *testing.T) {
	s, db := newRigTestServer(t)
	ctx := t.Context()
	rod := baitTestRod(t, s)
	item, err := s.CreateItem(ctx, 1000001, "worm_01", "backpack", 5)
	if err != nil {
		t.Fatal(err)
	}
	s.setStackCount(ctx, item, 5)
	res := s.rigSetComponent(ctx, 1000001, rod, catBait, item.GUID, 350,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true, SplitStack: true})
	if res.Rejected || res.MountedGUID == "" {
		t.Fatalf("挂载失败: %+v", res)
	}
	if res.SourceGUID != item.GUID || res.SourceTree == nil {
		t.Fatalf("挂载未记录来源堆刷新素材: %+v", res)
	}
	row, _ := db.GetItemByGUID(ctx, item.GUID)
	if row == nil || baitQuantity(row) != 4 {
		t.Fatalf("挂载后来源堆应为 4，实际 %+v", row)
	}
	hctx := &Ctx{Sess: newProbeSess(s), Main: 17, Sub: 2, Log: logging.Discard()}
	s.queueSplitSourceRefresh(hctx, res)
	if len(hctx.afterResponse) != 1 {
		t.Fatalf("应在回包后刷新来源堆，回调数 = %d", len(hctx.afterResponse))
	}
	hctx.afterResponse[0]()
}

// TestDetachBaitRefreshesSourceStackForClient 锁定：拆下拆分子实例把数量退回来源堆后，
// 必须在回包后先移除来源栈再重新加入，否则客户端背包数量不会 +1。
func TestDetachBaitRefreshesSourceStackForClient(t *testing.T) {
	s, db := newRigTestServer(t)
	ctx := t.Context()
	rod := baitTestRod(t, s)
	source, child := baitTestMount(t, s, rod, "worm_01", 5, 350)
	before, _ := db.GetItemByGUID(ctx, source)
	res := s.rigRemoveItemComponent(ctx, 1000001, rod, child)
	if res == nil || res.SourceGUID != source || res.SourceTree == nil {
		t.Fatalf("拆卸未记录来源堆刷新素材: %+v", res)
	}
	after, _ := db.GetItemByGUID(ctx, source)
	if after == nil || baitQuantity(after) != baitQuantity(before)+1 {
		t.Fatalf("退回后来源堆未 +1: before=%+v after=%+v", before, after)
	}
	if len(s.currentBaits(ctx, rod)) != 0 {
		t.Fatal("拆下后钩上不应再有饵")
	}
	hctx := &Ctx{Sess: newProbeSess(s), Main: 17, Sub: 3, Log: logging.Discard()}
	s.queueSourceStackRefresh(hctx, res)
	if len(hctx.afterResponse) != 1 {
		t.Fatalf("应在回包后刷新来源堆，回调数 = %d", len(hctx.afterResponse))
	}
	hctx.afterResponse[0]()
}

// TestBaitReloadRefreshesSourceStackForClient 锁定：自动补饵后必须把来源栈同时放进
// 移除段，客户端才会丢弃本地旧栈并使用新数量；只推更新段时背包计数不会刷新。
func TestBaitReloadRefreshesSourceStackForClient(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	rod := baitTestRod(t, s)
	// 补饵以钓组仍有钩为前提；无钩钓组的补饵请求按无效处理。
	mountHookForBaitTest(t, s, rod, 0)
	source, child := baitTestMount(t, s, rod, "worm_01", 5, 350)
	if _, err := s.consumeBaitEvent(ctx, 1000001, rod, "refresh-bite", []string{child}, false); err != nil {
		t.Fatal(err)
	}
	change, err := s.reloadBaitForCast(ctx, 1000001, rod)
	if err != nil {
		t.Fatal(err)
	}
	if !containsGUID(change.Removed, source) {
		t.Fatalf("来源栈 %s 未进入移除段：%+v", source, change)
	}
	if !containsGUID(change.Updated, source) {
		t.Fatalf("来源栈 %s 未进入更新段：%+v", source, change)
	}
	if len(change.Removed) != 1 {
		t.Fatalf("移除段只应包含来源栈，实际 %v", change.Removed)
	}
}

// TestBaitReloadSkipsWhenSelectedHookIsGone 锁定：钩随断线/断竿离组后，收竿补饵不能
// 再往没有钩的钓组上挂饵（线上事故：断线丢掉钩和浮漂，14/4 仍补挂一份苍蝇，
// 玩家看到钓组“损坏后鱼饵还在”）。
func TestBaitReloadSkipsWhenSelectedHookIsGone(t *testing.T) {
	s, db := newRigTestServer(t)
	ctx := t.Context()
	rod := baitTestRod(t, s)
	hook := mountHookForBaitTest(t, s, rod, 0)
	source, child := baitTestMount(t, s, rod, "worm_01", 5, 350)
	if groups := s.baitedHookSets(ctx, rod); len(groups) != 1 || groups[0].hookGUID != hook {
		t.Fatalf("初始钩饵关联异常: %+v", groups)
	}
	if _, err := s.consumeBaitEvent(ctx, 1000001, rod, "hook-gone-bite", []string{child}, false); err != nil {
		t.Fatal(err)
	}
	if res := s.rigRemoveItemComponent(ctx, 1000001, rod, hook); res == nil {
		t.Fatal("拆钩失败")
	}

	if _, err := s.reloadBaitForCast(ctx, 1000001, rod); err != nil {
		t.Fatal(err)
	}
	if baits := s.currentBaits(ctx, rod); len(baits) != 0 {
		t.Fatalf("钩已不在钓组上，收竿仍补挂了 %v", baits)
	}
	if row, err := db.GetItemByGUID(ctx, source); err != nil || row == nil || baitQuantity(row) != 4 {
		t.Fatalf("来源饵堆不应被再次拆走: %+v err=%v", row, err)
	}
	var pending int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM rod_setting WHERE guid=? AND key LIKE 'rebait:%'`, rod).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("已拆钩的补饵请求应清除，仍剩 %d 条", pending)
	}
}

// 旧格式 rebait 记录（直接存 item_id，没有 hook 字段）在钩离组后同样不能补饵。
func TestBaitReloadSkipsLegacyRebaitWhenHookIsGone(t *testing.T) {
	s, db := newRigTestServer(t)
	ctx := t.Context()
	rod := baitTestRod(t, s)
	hook := mountHookForBaitTest(t, s, rod, 0)
	source, err := s.CreateItem(ctx, 1000001, "worm_01", "backpack", 5)
	if err != nil {
		t.Fatal(err)
	}
	s.setStackCount(ctx, source, 5)
	if err := db.UpsertRodSetting(ctx, rod, "rebait:0", "worm_01"); err != nil {
		t.Fatal(err)
	}
	if res := s.rigRemoveItemComponent(ctx, 1000001, rod, hook); res == nil {
		t.Fatal("拆钩失败")
	}

	if _, err := s.reloadBaitForCast(ctx, 1000001, rod); err != nil {
		t.Fatal(err)
	}
	if baits := s.currentBaits(ctx, rod); len(baits) != 0 {
		t.Fatalf("旧格式补饵记录在无钩钓组上仍补挂了 %v", baits)
	}
	if row, err := db.GetItemByGUID(ctx, source.GUID); err != nil || row == nil || baitQuantity(row) != 5 {
		t.Fatalf("来源饵堆不应被拆走: %+v err=%v", row, err)
	}
	if got := rodSettingCountForBait(t, db, rod, "rebait:%"); got != 0 {
		t.Fatalf("无钩钓组的旧格式补饵请求应清除，仍剩 %d 条", got)
	}
}

// 单钩钓组的旧格式记录可以推断出唯一钩，补饵后要补写 bait_hook 映射。
func TestBaitReloadInfersLegacyRebaitHookOnSingleHookRig(t *testing.T) {
	s, db := newRigTestServer(t)
	ctx := t.Context()
	rod := baitTestRod(t, s)
	hook := mountHookForBaitTest(t, s, rod, 0)
	source, err := s.CreateItem(ctx, 1000001, "worm_01", "backpack", 5)
	if err != nil {
		t.Fatal(err)
	}
	s.setStackCount(ctx, source, 5)
	if err := db.UpsertRodSetting(ctx, rod, "rebait:0", "worm_01"); err != nil {
		t.Fatal(err)
	}

	if _, err := s.reloadBaitForCast(ctx, 1000001, rod); err != nil {
		t.Fatal(err)
	}
	groups := s.baitedHookSets(ctx, rod)
	if len(groups) != 1 || groups[0].hookGUID != hook || len(groups[0].baits) != 1 {
		t.Fatalf("单钩钓组旧格式补饵应挂到唯一钩上: %+v", groups)
	}
	if row, err := db.GetItemByGUID(ctx, source.GUID); err != nil || row == nil || baitQuantity(row) != 4 {
		t.Fatalf("来源饵堆应只被拆走一份: %+v err=%v", row, err)
	}
	var mapped string
	if err := db.SQL.QueryRow(`SELECT value FROM rod_setting WHERE guid=? AND key=?`,
		rod, "bait_hook:"+groups[0].baitGUIDs[0]).Scan(&mapped); err != nil || mapped != hook {
		t.Fatalf("旧格式补饵应补写钩饵映射: mapped=%q err=%v", mapped, err)
	}
	if got := rodSettingCountForBait(t, db, rod, "rebait:%"); got != 0 {
		t.Fatalf("补饵完成后应清除 rebait 记录，仍剩 %d 条", got)
	}
}

// 多钩钓组的旧格式记录无法确定挂哪只钩，按无法安全补饵处理。
func TestBaitReloadSkipsLegacyRebaitOnMultiHookRig(t *testing.T) {
	s, db := newRigTestServer(t)
	ctx := t.Context()
	rod, err := s.CreateItem(ctx, 1000001, "boatr_10222_SBH80_100", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !s.rigSetType(ctx, 1000001, rod.GUID, 59) {
		t.Fatal("设置多钩钓组类型失败")
	}
	for i := int64(0); i < 3; i++ {
		mountHookForBaitTest(t, s, rod.GUID, i)
	}
	source, err := s.CreateItem(ctx, 1000001, "worm_01", "backpack", 5)
	if err != nil {
		t.Fatal(err)
	}
	s.setStackCount(ctx, source, 5)
	if err := db.UpsertRodSetting(ctx, rod.GUID, "rebait:0", "worm_01"); err != nil {
		t.Fatal(err)
	}

	if _, err := s.reloadBaitForCast(ctx, 1000001, rod.GUID); err != nil {
		t.Fatal(err)
	}
	if baits := s.currentBaits(ctx, rod.GUID); len(baits) != 0 {
		t.Fatalf("多钩钓组旧格式记录不应补饵，实际补挂 %v", baits)
	}
	if row, err := db.GetItemByGUID(ctx, source.GUID); err != nil || row == nil || baitQuantity(row) != 5 {
		t.Fatalf("来源饵堆不应被拆走: %+v err=%v", row, err)
	}
	if got := rodSettingCountForBait(t, db, rod.GUID, "rebait:%"); got != 0 {
		t.Fatalf("多钩钓组的旧格式补饵请求应清除，仍剩 %d 条", got)
	}
}

func rodSettingCountForBait(t *testing.T, db *store.DB, rod, pattern string) int64 {
	t.Helper()
	var n int64
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM rod_setting WHERE guid=? AND key LIKE ?`, rod, pattern).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestBaitRefillsWhenFishIsLanded 锁定：14/11 吃掉钩上的最后一份饵后，14/4 收竿应当
// 立即补挂同种库存。若把补饵拖到下一次 14/1/14/2，客户端会停在空钩状态，玩家
// 必须先手动换饵才能抛竿（手杆上鱼一条后鱼饵消失）。
func TestBaitRefillsWhenFishIsLanded(t *testing.T) {
	s, db := newRigTestServer(t)
	ctx := t.Context()
	rod := baitTestRod(t, s)
	// 补饵的前提是钓组上仍有钩；无钩钓组的补饵请求按无效处理（见 P1 回归）。
	mountHookForBaitTest(t, s, rod, 0)
	source, child := baitTestMount(t, s, rod, "worm_01", 5, 350)
	addFishingRecoverySession(s, 1000001, 939)
	st := s.stateFor(1000001)
	fish := &RolledFish{
		FishGUID: "14a4ba17000000000000000000000000", Species: "c.roach", Grade: 2,
		WeightG: 300, LengthM: 0.24, BaitsKnown: true, BaitGUIDs: child,
	}
	st.mu.Lock()
	st.GearGuid = rod
	st.Loc = "unknown-map"
	st.Bait = "worm_01"
	st.CastActive, st.InWater = true, true
	st.CastTs = time.Now().Add(-time.Minute)
	st.Pending, st.Hooked = fish, false
	st.mu.Unlock()

	sess := s.liveSessionByUID(1000001)
	if sess == nil {
		t.Fatal("no live session")
	}
	if _, ok := hFishingHooked(&Ctx{Sess: sess, Main: 14, Sub: 11, Log: logging.Discard()}); !ok {
		t.Fatal("14/11 未处理")
	}
	if got := len(s.currentBaits(ctx, rod)); got != 0 {
		t.Fatalf("中鱼后钩上饵数 = %d，期望 0", got)
	}
	ctx14 := &Ctx{Sess: sess, Main: 14, Sub: 4, ExpectResponse: true, Log: logging.Discard()}
	body, ok := s.fishingEndSettle(ctx14)
	if !ok || len(body) == 0 {
		t.Fatalf("14/4 未处理: ok=%v bytes=%d", ok, len(body))
	}
	// 上鱼还会注册成就解锁推送（2/8），补饵回调不再固定是第一个；
	// 逐个执行应答后回调，两者互不干扰。
	if len(ctx14.afterResponse) < 1 {
		t.Fatalf("补饵推送应在 801 回包后执行，回调数 = %d", len(ctx14.afterResponse))
	}
	for _, fn := range ctx14.afterResponse {
		fn()
	}
	if got := len(s.currentBaits(ctx, rod)); got != 1 {
		t.Fatalf("收竿后钩上应补挂 1 份同种饵，实际 %d", got)
	}
	row, _ := db.GetItemByGUID(ctx, source)
	if row == nil || baitQuantity(row) != 3 {
		t.Fatalf("补挂应从背包预留 1 份（剩余 3），实际 %+v", row)
	}
	var pending int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM rod_setting WHERE guid=? AND key LIKE 'rebait:%'`, rod).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("补挂完成后仍残留补饵请求：%d %v", pending, err)
	}
}
