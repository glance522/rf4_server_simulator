package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/logging"
)

// 捆绑包发放：清单里的物品都要进背包，且槽位互不重复（同槽客户端只显示一件）。
func TestGrantPremiumBundleGrantsItemsWithUniqueSlots(t *testing.T) {
	srv, db := newEconomyTxTestServer(t)
	const uid = int64(2101)
	if _, err := db.EnsureProfile(context.Background(), uid); err != nil {
		t.Fatal(err)
	}
	// 先占一个槽位，确认发放从空槽开始。
	first, err := srv.CreateItem(context.Background(), uid, "worm_01", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetItemSlot(context.Background(), first.GUID, 0); err != nil {
		t.Fatal(err)
	}

	c := &Ctx{Log: logging.Discard()}
	srv.grantPremiumBundle(context.Background(), c, uid, "level_012_north_sea", 17990)

	var total, distinct int
	if err := db.SQL.QueryRow(
		`SELECT COUNT(*), COUNT(DISTINCT slot) FROM item WHERE uid=? AND container='backpack'`,
		uid).Scan(&total, &distinct); err != nil {
		t.Fatal(err)
	}
	if total <= 1 {
		t.Fatalf("捆绑包未发放任何物品，背包物品数=%d", total)
	}
	if total != distinct {
		t.Fatalf("槽位有重复：物品 %d 件，不同槽位只有 %d 个", total, distinct)
	}
}

// 不是捆绑包价位（或地图不匹配）时不应该发放任何东西。
func TestGrantPremiumBundleIgnoresUnknownPriceOrPlace(t *testing.T) {
	srv, db := newEconomyTxTestServer(t)
	const uid = int64(2102)
	if _, err := db.EnsureProfile(context.Background(), uid); err != nil {
		t.Fatal(err)
	}
	c := &Ctx{Log: logging.Discard()}
	srv.grantPremiumBundle(context.Background(), c, uid, "level_012_north_sea", 1234)
	srv.grantPremiumBundle(context.Background(), c, uid, "level_018_moskitnoe", 17990)

	var n int
	if err := db.SQL.QueryRow(
		`SELECT COUNT(*) FROM item WHERE uid=?`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("非捆绑包价位或地图不匹配时不应发放物品，实得 %d 件", n)
	}
}
