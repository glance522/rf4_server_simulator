package gameserver

// 可堆叠产物的背包语义：制作产出的窝料/PVA 只占一个堆且可反复使用；
// 挂载拆分再卸载不能让同种物品多出一格；引线等非计件物品保持独立实例。

import (
	"context"
	"fmt"
	"testing"
)

func TestCraftedStackSingleSlotReusable(t *testing.T) {
	s, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rod := baitTestRod(t, s)

	backpackStacks := func(itemID string) (rows int, qty int64) {
		t.Helper()
		if err := db.SQL.QueryRowContext(ctx,
			`SELECT COUNT(*), COALESCE(SUM(qty),0) FROM item
			 WHERE uid=? AND item_id=? AND container='backpack'`,
			uid, itemID).Scan(&rows, &qty); err != nil {
			t.Fatal(err)
		}
		return rows, qty
	}

	for _, itemID := range []string{"feedcompleted", "pva_stick_s"} {
		t.Run(itemID, func(t *testing.T) {
			def, err := db.GetItemDefByItemID(ctx, itemID)
			if err != nil || def == nil {
				t.Fatalf("物品定义缺失: %v", err)
			}
			stack, err := s.CreateItem(ctx, uid, itemID, "backpack", 60)
			if err != nil {
				t.Fatal(err)
			}
			mount := func() string {
				res := s.rigSetComponent(ctx, uid, rod, def.Category, stack.GUID, 0,
					rigSetOpts{ParentType: familyRod, EnforceCategory: true, SplitStack: true})
				if res.Rejected || res.MountedGUID == "" {
					t.Fatalf("挂载失败: %+v", res)
				}
				return res.MountedGUID
			}

			// 挂载再卸载：数量回到 60，且背包仍只有一堆。
			mounted := mount()
			if rows, qty := backpackStacks(itemID); rows != 1 || qty != 59 {
				t.Fatalf("挂载后背包 rows=%d qty=%d，期望 1/59", rows, qty)
			}
			s.rigRemoveItemComponent(ctx, uid, rod, mounted)
			if rows, qty := backpackStacks(itemID); rows != 1 || qty != 60 {
				t.Fatalf("卸载后背包 rows=%d qty=%d，期望 1/60", rows, qty)
			}
			if row, _ := db.GetItemByGUID(ctx, mounted); row != nil {
				t.Fatalf("卸载后挂载子实例仍存在: %+v", row)
			}

			// 连续使用：每次从同一堆拆 1 个消耗，背包始终只有一堆。
			for use := 0; use < 3; use++ {
				mounted = mount()
				if _, err := s.consumeBaitEvent(ctx, uid, rod,
					fmt.Sprintf("cast-%s-%d", itemID, use), []string{mounted}, true); err != nil {
					t.Fatal(err)
				}
				want := int64(59 - use)
				if rows, qty := backpackStacks(itemID); rows != 1 || qty != want {
					t.Fatalf("第 %d 次使用后背包 rows=%d qty=%d，期望 1/%d", use+1, rows, qty, want)
				}
			}
		})
	}
}

// 挖取入包必须并入已有堆：230 个蚯蚓挖到 4 个后只剩一个图鉴、数量 234。
func TestDugBaitStackMergesToSingleEntry(t *testing.T) {
	s, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	seed, err := s.prepareNewItem(ctx, uid, "worm_01", "backpack", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := setPreparedItemQuantity(seed, 230); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertItem(ctx, seed); err != nil {
		t.Fatal(err)
	}

	gain, err := s.prepareNewItem(ctx, uid, "worm_01", "backpack", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := setPreparedItemQuantity(gain, 4); err != nil {
		t.Fatal(err)
	}
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	row, merged, err := grantPreparedItemTx(ctx, tx, gain, true)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if !merged || row == nil || row.GUID != seed.GUID || row.Qty != 234 {
		t.Fatalf("挖取未并入原堆: merged=%t row=%+v", merged, row)
	}

	var rows int
	var total int64
	if err := db.SQL.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(qty),0) FROM item
		  WHERE uid=? AND item_id='worm_01' AND container='backpack'`, uid).
		Scan(&rows, &total); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || total != 234 {
		t.Fatalf("背包蚯蚓 rows=%d total=%d，期望 1/234", rows, total)
	}
}

// PVA 每组独立占一格后，挂载拆分必须退回原堆，不能串到同种的其他批次。
func TestPvaSplitRefundsToOriginStack(t *testing.T) {
	s, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rod := baitTestRod(t, s)

	first, err := s.CreateItem(ctx, uid, "pva_stick_s", "backpack", 60)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateItem(ctx, uid, "pva_stick_s", "backpack", 10)
	if err != nil {
		t.Fatal(err)
	}
	res := s.rigSetComponent(ctx, uid, rod, 30, second.GUID, 0,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true, SplitStack: true})
	if res.Rejected || res.MountedGUID == "" {
		t.Fatalf("挂载失败: %+v", res)
	}
	mounted, err := db.GetItemByGUID(ctx, res.MountedGUID)
	if err != nil || mounted == nil {
		t.Fatalf("挂载子实例缺失: %v", err)
	}
	if got := splitSourceGUID(mounted); got != second.GUID {
		t.Fatalf("拆分来源=%s，期望 %s", got, second.GUID)
	}
	if row, err := db.GetItemByGUID(ctx, second.GUID); err != nil || row == nil || row.Qty != 9 {
		t.Fatalf("来源堆应剩 9，实得 %+v err=%v", row, err)
	}

	s.rigRemoveItemComponent(ctx, uid, rod, res.MountedGUID)
	if row, err := db.GetItemByGUID(ctx, second.GUID); err != nil || row == nil || row.Qty != 10 {
		t.Fatalf("卸载后来源堆应回到 10，实得 %+v err=%v", row, err)
	}
	if row, err := db.GetItemByGUID(ctx, first.GUID); err != nil || row == nil || row.Qty != 60 {
		t.Fatalf("其他批次不应被并入，实得 %+v err=%v", row, err)
	}
	if row, _ := db.GetItemByGUID(ctx, res.MountedGUID); row != nil {
		t.Fatalf("卸载后挂载子实例仍存在: %+v", row)
	}
}
