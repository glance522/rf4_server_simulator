package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/proto"
)

func TestStoreListUsesCatalogTemplateAndUnknownStoreStaysEmpty(t *testing.T) {
	srv, db := newEconomyTxTestServer(t)
	ctx := economyTestCtx(srv, 2601, proto.Args524(proto.StrVal("atm_006")))
	body, ok := hStoreList(ctx)
	if !ok {
		t.Fatal("22/1 应返回响应")
	}
	decoded, _, err := proto.DecodeValue(body, 0, ctx.Sess.Table())
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Kind != proto.KindArgs || decoded.ArgCodeStr() != "140" || decoded.ArgCount() < 5 {
		t.Fatalf("22/1 模板货架不符: kind=%v code=%q count=%d", decoded.Kind, decoded.ArgCodeStr(), decoded.ArgCount())
	}
	first := decoded.ArgItem(0)
	if first == nil {
		t.Fatal("22/1 ATM 首项为空")
	}
	itemID := first.Field(0)
	silver := first.Field(1)
	gold := first.Field(2)
	if itemID.StrOr() != "wallet_50" || silver.U64() != 0 || gold.U64() != 49 {
		t.Fatalf("22/1 ATM 首项不符: %+v", first)
	}
	price, err := srv.storePrice(context.Background(), "atm_006", "wallet_50", 0, true)
	if err != nil || price != 49 {
		t.Fatalf("22/3 模板定价不符: price=%d err=%v", price, err)
	}

	if _, err := db.SQL.Exec(`INSERT INTO item_price(item_id,silver,gold,stock)
		VALUES('global_only',100,1,99)`); err != nil {
		t.Fatal(err)
	}
	unknownCtx := economyTestCtx(srv, 2601, proto.Args524(proto.StrVal("not_a_catalog_store")))
	body, ok = hStoreList(unknownCtx)
	if !ok {
		t.Fatal("未知店 22/1 仍应回合法空响应")
	}
	decoded, _, err = proto.DecodeValue(body, 0, unknownCtx.Sess.Table())
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ArgCodeStr() != "140" || decoded.ArgCount() != 0 {
		t.Fatalf("未知店不得回全局价目兜底: code=%q count=%d", decoded.ArgCodeStr(), decoded.ArgCount())
	}
}

func TestReportedGoldLinesPurchaseAndInsufficientBalance(t *testing.T) {
	srv, db := newEconomyTxTestServer(t)
	const uid = int64(2620)
	ctx := context.Background()
	if _, err := db.SQL.Exec(`ATTACH DATABASE '../../server_data/static.sqlite3' AS src;
      INSERT OR REPLACE INTO main.item_price SELECT * FROM src.item_price;
      INSERT OR REPLACE INTO main.item_catalog SELECT * FROM src.item_catalog;
      DETACH DATABASE src;`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET silver=1000000,gold=10000 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	registerShopHandlers(srv.dispatch)
	for _, itemID := range []string{"braid_40907", "braid_40904"} {
		p, err := db.ShopItemGet(ctx, "fishingstore_009", itemID)
		if err != nil || p.Gold <= 0 || p.Stock <= 0 {
			t.Fatalf("reported item still unavailable: %+v %v", p, err)
		}
		request := economyTestCtx(srv, uid, proto.Args524(proto.StrVal("fishingstore_009"), proto.StrVal(itemID), proto.U64(1)))
		request.Main, request.Sub, request.ExpectResponse = 22, 3, true
		result := srv.dispatch.executeRequest(request, true)
		if result.nativeFailure || result.failed || !result.hasResponse {
			t.Fatalf("gold purchase rejected: %+v", result)
		}
		tree, _, err := proto.DecodeValue(result.body, 0, srv.Table())
		if err != nil || tree.TypeID != 804 {
			t.Fatalf("wrong purchase response: %+v %v", tree, err)
		}
	}
	var count, gold int64
	db.SQL.QueryRow(`SELECT COUNT(*) FROM txn WHERE uid=? AND kind='buy'`, uid).Scan(&count)
	db.SQL.QueryRow(`SELECT gold FROM profile WHERE uid=?`, uid).Scan(&gold)
	if count != 2 || gold != 10000-1513-1343 {
		t.Fatalf("wrong debit: count=%d gold=%d", count, gold)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET gold=0 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	request := economyTestCtx(srv, uid, proto.Args524(proto.StrVal("fishingstore_009"), proto.StrVal("braid_40907"), proto.U64(1)))
	request.Main, request.Sub, request.ExpectResponse = 22, 3, true
	result := srv.dispatch.executeRequest(request, true)
	if !result.nativeFailure {
		t.Fatal("insufficient balance returned a boolean/synthetic success")
	}
	db.SQL.QueryRow(`SELECT COUNT(*) FROM txn WHERE uid=? AND kind='buy'`, uid).Scan(&count)
	if count != 2 {
		t.Fatal("failed purchase wrote another debit")
	}
}
