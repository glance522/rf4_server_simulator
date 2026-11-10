package gameserver

import (
	"context"
	"fmt"
	"testing"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func markLegacyStackPurchase(t *testing.T, srv *Server, uid int64, row *store.Item) {
	t.Helper()
	tx, err := beginEconomyTx(context.Background(), srv.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := srv.saveStorePurchaseTx(context.Background(), tx, uid, "legacy_store", row.ItemID, 10, false, []*store.Item{row}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func setStackOrder(t *testing.T, db *store.DB, ordered ...*store.Item) {
	t.Helper()
	for slot, row := range ordered {
		if _, err := db.SQL.Exec(`UPDATE item SET slot=? WHERE guid=?`, slot, row.GUID); err != nil {
			t.Fatal(err)
		}
		row.Slot = int64(slot)
	}
}

func TestConsolidateNonReturnableItemStacksPreservesBoundariesAndReferences(t *testing.T) {
	srv, db := newEconomyTxTestServer(t)
	const (
		uid    = int64(4620)
		foodID = "legacy_food"
		baitID = "legacy_bait"
		hookID = "legacy_hook"
	)
	seedEconomyItemDef(t, db, 994001, foodID, 26)
	seedEconomyItemDef(t, db, 994002, baitID, 27)
	seedEconomyItemDef(t, db, 994003, hookID, 14)

	foodKeep := economyTestItem(t, srv, uid, foodID, "backpack", 2)
	foodDrop := economyTestItem(t, srv, uid, foodID, "backpack", 3)
	baitKeep := economyTestItem(t, srv, uid, baitID, "backpack", 4)
	baitDrop := economyTestItem(t, srv, uid, baitID, "backpack", 5)
	houseKeep := economyTestItem(t, srv, uid, foodID, "house", 7)
	houseDrop := economyTestItem(t, srv, uid, foodID, "house", 11)
	hookA := economyTestItem(t, srv, uid, hookID, "backpack", 2)
	hookB := economyTestItem(t, srv, uid, hookID, "backpack", 3)
	mountedA := economyTestItem(t, srv, uid, baitID, "backpack", 1)
	mountedB := economyTestItem(t, srv, uid, baitID, "backpack", 1)
	setStackOrder(t, db, foodKeep, foodDrop)
	setStackOrder(t, db, baitKeep, baitDrop)
	setStackOrder(t, db, houseKeep, houseDrop)
	if _, err := db.SQL.Exec(`UPDATE item SET container='rig',rig_guid=? WHERE guid=?`, "rod-a", mountedA.GUID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE item SET container='rig',rig_guid=? WHERE guid=?`, "rod-b", mountedB.GUID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO container_slot(uid,container,slot,guid) VALUES(?,0,7,?)`, uid, foodDrop.GUID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO drink_bonus_effect(uid,category,peak,remaining_millis,duration_millis,source_guid)
		VALUES(?,'food',0.2,1000,1000,?)`, uid, foodDrop.GUID); err != nil {
		t.Fatal(err)
	}
	markLegacyStackPurchase(t, srv, uid, foodDrop)

	removed, err := srv.ConsolidateNonReturnableItemStacks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if removed != 3 {
		t.Fatalf("删除重复堆=%d，期望 3", removed)
	}
	for _, tc := range []struct {
		item, container, guid string
		rows, qty             int64
	}{
		{foodID, "backpack", foodKeep.GUID, 1, 5},
		{baitID, "backpack", baitKeep.GUID, 1, 9},
		{foodID, "house", houseKeep.GUID, 1, 18},
		{hookID, "backpack", "", 2, hookA.Qty + hookB.Qty},
		{baitID, "rig", "", 2, mountedA.Qty + mountedB.Qty},
	} {
		rows, qty, guid := stackingTestStats(t, db, uid, tc.item, tc.container)
		if rows != tc.rows || qty != tc.qty || tc.guid != "" && guid != tc.guid {
			t.Errorf("%s/%s: rows=%d qty=%d guid=%s，期望 %d/%d/%s", tc.item, tc.container, rows, qty, guid, tc.rows, tc.qty, tc.guid)
		}
	}
	var shortcut, source string
	if err := db.SQL.QueryRow(`SELECT guid FROM container_slot WHERE uid=? AND container=0 AND slot=7`, uid).Scan(&shortcut); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.QueryRow(`SELECT source_guid FROM drink_bonus_effect WHERE uid=? AND category='food'`, uid).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if shortcut != foodKeep.GUID || source != foodKeep.GUID {
		t.Fatalf("存量引用未重定向: shortcut=%s source=%s want=%s", shortcut, source, foodKeep.GUID)
	}
	kept, err := db.GetItemByGUID(context.Background(), foodKeep.GUID)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := FieldsFromJSON(kept.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	origin := fieldIdxByCS(kept.WireClass, csOrigin)
	if kept.BuyPrice != 0 || origin >= 0 && origin < len(fields) && fields[origin].Val != nil && fields[origin].Val.Kind != proto.KindNull {
		t.Fatalf("合并堆仍携带失效退货元数据: buy_price=%d", kept.BuyPrice)
	}
	var receipts int64
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM kv WHERE k LIKE ?`, fmt.Sprintf("store-purchase:%d:%%", uid)).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if receipts != 0 {
		t.Fatalf("旧退货凭据未清理: %d", receipts)
	}
	if _, err := db.SQL.Exec(`CREATE TRIGGER reject_clean_stack_rewrite BEFORE DELETE ON container_slot
		BEGIN SELECT RAISE(ABORT,'clean stack rewritten'); END`); err != nil {
		t.Fatal(err)
	}
	if again, err := srv.ConsolidateNonReturnableItemStacks(context.Background()); err != nil || again != 0 {
		t.Fatalf("重复整理不幂等: removed=%d err=%v", again, err)
	}
}

func TestConsolidateNonReturnableItemStacksRollsBackOnDeleteFailure(t *testing.T) {
	srv, db := newEconomyTxTestServer(t)
	const (
		uid    = int64(4621)
		foodID = "rollback_food"
	)
	seedEconomyItemDef(t, db, 994011, foodID, 26)
	keep := economyTestItem(t, srv, uid, foodID, "backpack", 2)
	drop := economyTestItem(t, srv, uid, foodID, "backpack", 3)
	setStackOrder(t, db, keep, drop)
	if _, err := db.SQL.Exec(`INSERT INTO container_slot(uid,container,slot,guid) VALUES(?,0,8,?)`, uid, drop.GUID); err != nil {
		t.Fatal(err)
	}
	markLegacyStackPurchase(t, srv, uid, drop)
	if _, err := db.SQL.Exec(fmt.Sprintf(`CREATE TRIGGER fail_stack_delete BEFORE DELETE ON item
		WHEN OLD.guid='%s' BEGIN SELECT RAISE(ABORT,'test delete failure'); END`, drop.GUID)); err != nil {
		t.Fatal(err)
	}

	if removed, err := srv.ConsolidateNonReturnableItemStacks(context.Background()); err == nil || removed != 0 {
		t.Fatalf("注入失败未整体回滚: removed=%d err=%v", removed, err)
	}
	rows, qty, _ := stackingTestStats(t, db, uid, foodID, "backpack")
	if rows != 2 || qty != 5 {
		t.Fatalf("失败整理改变库存: rows=%d qty=%d", rows, qty)
	}
	var shortcut string
	if err := db.SQL.QueryRow(`SELECT guid FROM container_slot WHERE uid=? AND container=0 AND slot=8`, uid).Scan(&shortcut); err != nil || shortcut != drop.GUID {
		t.Fatalf("失败整理改变快捷栏: guid=%s err=%v", shortcut, err)
	}
	var receipt string
	if err := db.SQL.QueryRow(`SELECT v FROM kv WHERE k=?`, storePurchaseKey(uid, drop.GUID)).Scan(&receipt); err != nil || receipt == "" {
		t.Fatalf("失败整理清除了退货凭据: err=%v", err)
	}
	current, err := db.GetItemByGUID(context.Background(), drop.GUID)
	if err != nil || current.BuyPrice != 10 {
		t.Fatalf("失败整理改变重复堆元数据: row=%+v err=%v", current, err)
	}
}
