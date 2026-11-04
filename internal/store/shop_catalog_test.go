package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
)

func openShopCatalogTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "shop-catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func findShopPrice(t *testing.T, rows []*ShopPrice, itemID string) *ShopPrice {
	t.Helper()
	for _, row := range rows {
		if row.ItemID == itemID {
			return row
		}
	}
	t.Fatalf("货架缺少 %s", itemID)
	return nil
}

func TestEmbeddedShopCatalogMatchesEvidenceAndCoversAllStores(t *testing.T) {
	sum := sha256.Sum256(embeddedShopKindDefs)
	const wantHash = "cbf7494f46f9f0768bd38a282e3780389b02b1b95a649d179f2d457c2b15f72d"
	if got := hex.EncodeToString(sum[:]); got != wantHash {
		t.Fatalf("商店目录 hash=%s want=%s", got, wantHash)
	}
	catalog, err := embeddedShopKindCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Stores) != 205 {
		t.Fatalf("商店目录 stores=%d want=205", len(catalog.Stores))
	}
	if len(catalog.Kinds) != 8 {
		t.Fatalf("商店目录 kinds=%d want=8", len(catalog.Kinds))
	}

	db := openShopCatalogTestDB(t)
	stores, err := db.ListAdminStores(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(stores) != 205 {
		t.Fatalf("管理后台商店数=%d want=205", len(stores))
	}
	var nonempty, missingEvidence int
	for _, view := range stores {
		if !view.CatalogKnown {
			t.Fatalf("目录内商店被标记为未知: %s", view.StoreID)
		}
		if view.ItemCount > 0 {
			nonempty++
			continue
		}
		missingEvidence++
		if view.Kind != 2 && view.Kind != 8 {
			t.Fatalf("有货架模板的 kind=%d 商店为空: %s", view.Kind, view.StoreID)
		}
		if view.ShelfSource != shopShelfSourceMissing {
			t.Fatalf("缺证据商店来源=%q want=%q", view.ShelfSource, shopShelfSourceMissing)
		}
	}
	if nonempty != 187 || missingEvidence != 18 {
		t.Fatalf("205 店覆盖异常: nonempty=%d missing=%d", nonempty, missingEvidence)
	}
}

func TestShopCatalogRecordedStoreAndBrandReusePrecedence(t *testing.T) {
	db := openShopCatalogTestDB(t)
	ctx := context.Background()
	if _, err := db.SQL.Exec(`INSERT INTO shop_item(store_id,item_id,silver,gold,stock) VALUES
		('fishingstore_006','exact_recorded_item',321,0,7),
		('monkeycola_001','brand_recorded_item',55,0,9)`); err != nil {
		t.Fatal(err)
	}

	exact, err := db.ShopItems(ctx, "fishingstore_006")
	if err != nil {
		t.Fatal(err)
	}
	if len(exact) != 1 || exact[0].ItemID != "exact_recorded_item" {
		t.Fatalf("逐店实录未优先于 kind 模板: %+v", exact)
	}

	reused, err := db.ShopItems(ctx, "monkeycola_002")
	if err != nil {
		t.Fatal(err)
	}
	if len(reused) != 1 || reused[0].ItemID != "brand_recorded_item" || reused[0].Stock != 9 {
		t.Fatalf("同 kind+同品牌实录未复用: %+v", reused)
	}
}

func TestShopCatalogPreservesCurrencyAndPerStoreOverrides(t *testing.T) {
	db := openShopCatalogTestDB(t)
	ctx := context.Background()
	if _, err := db.SQL.Exec(`INSERT INTO item_def(numeric_id,item_id,category,kind,brand,attrs_json)
		VALUES(990001,'wallet_50',24,'','','{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO item_catalog(item_id,name,name_zh,kind,numeric_id)
		VALUES('wallet_50','Wallet 50','ATM 钱包','',990001)`); err != nil {
		t.Fatal(err)
	}

	goldRows, err := db.ShopItems(ctx, "atm_006")
	if err != nil {
		t.Fatal(err)
	}
	if len(goldRows) != 5 {
		t.Fatalf("ATM 模板行数=%d want=5", len(goldRows))
	}
	for _, row := range goldRows {
		if row.Silver != 0 || row.Gold <= 0 {
			t.Fatalf("金币-only 模板被破坏: %+v", row)
		}
	}

	silverRows, err := db.ShopItems(ctx, "boatstation_006")
	if err != nil {
		t.Fatal(err)
	}
	if len(silverRows) != 4 {
		t.Fatalf("船站模板行数=%d want=4", len(silverRows))
	}
	for _, row := range silverRows {
		if row.Silver <= 0 || row.Gold != 0 {
			t.Fatalf("银币-only 模板被破坏: %+v", row)
		}
	}

	otherBase := findShopPrice(t, goldRows, "wallet_50")
	if err := db.SetAdminShopOverride(ctx, "atm_006", "wallet_50", 0, 60, 7, "test"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetAdminShopOverride(ctx, "atm_009", "wallet_50", 0, 70, 13, "test"); err != nil {
		t.Fatal(err)
	}
	first, err := db.ShopItemGet(ctx, "atm_006", "wallet_50")
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.ShopItemGet(ctx, "atm_009", "wallet_50")
	if err != nil {
		t.Fatal(err)
	}
	if first.Gold != 60 || first.Stock != 7 || second.Gold != 70 || second.Stock != 13 {
		t.Fatalf("覆盖值串店: first=%+v second=%+v", first, second)
	}
	if otherBase.Stock == first.Stock || otherBase.Stock == second.Stock {
		t.Fatalf("基础货架被原地修改: base=%+v", otherBase)
	}

	items, err := db.ListAdminStoreItems(ctx, "atm_006", "钱包", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !items[0].HasOverride || items[0].Name != "ATM 钱包" ||
		items[0].Silver != 0 || items[0].Gold != 60 || items[0].Stock != 7 {
		t.Fatalf("后台生效货架不符: %+v", items)
	}
	if err := db.ResetAdminShopOverride(ctx, "atm_006", "wallet_50"); err != nil {
		t.Fatal(err)
	}
	reset, _ := db.ShopItemGet(ctx, "atm_006", "wallet_50")
	if reset.Gold != otherBase.Gold || reset.Stock != otherBase.Stock {
		t.Fatalf("重置未恢复模板: got=%+v base=%+v", reset, otherBase)
	}
	stillOverridden, _ := db.ShopItemGet(ctx, "atm_009", "wallet_50")
	if stillOverridden.Gold != 70 || stillOverridden.Stock != 13 {
		t.Fatalf("重置一店影响了另一店: %+v", stillOverridden)
	}
}

func TestShopCatalogUnknownStoreDoesNotFabricateShelf(t *testing.T) {
	db := openShopCatalogTestDB(t)
	ctx := context.Background()
	if _, err := db.SQL.Exec(`INSERT INTO item_price(item_id,silver,gold,stock)
		VALUES('global_only',100,1,99)`); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ShopItems(ctx, "not_a_catalog_store")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("未知店不得用全局价目伪造货架: %+v", rows)
	}
	if _, err := db.ShopItemGet(ctx, "not_a_catalog_store", "global_only"); err == nil {
		t.Fatal("未知店不应命中全局价目")
	}
	if err := db.SetAdminShopOverride(ctx, "not_a_catalog_store", "global_only", 100, 1, 99, "test"); err == nil ||
		!strings.Contains(err.Error(), "生效货架") {
		t.Fatalf("未知店覆盖应被拒绝，得到 %v", err)
	}
}

func TestShopSupplementPricesRestockAndAdminStayAligned(t *testing.T) {
	db := openShopCatalogTestDB(t)
	ctx := context.Background()
	if _, err := db.SQL.Exec(`INSERT INTO item_def(numeric_id,item_id,category,kind,brand,attrs_json) VALUES
 (991001,'base_line',11,'line','brand_test','{}'),(991002,'gold_line',11,'line','brand_test','{}'),
 (991003,'silver_line',11,'line','brand_test','{}'),(991004,'unpriced_line',11,'line','brand_test','{}'),
 (991005,'foreign_food',26,'food','','{}');
 INSERT INTO shop_item(store_id,item_id,silver,gold,stock) VALUES('fishingstore_009','base_line',100,2,0);
 INSERT INTO item_price(item_id,silver,gold,stock) VALUES('gold_line',0,15,0),('silver_line',200,0,0),('foreign_food',20,0,0);`); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ShopItems(ctx, "fishingstore_009")
	if err != nil || len(rows) != 4 {
		t.Fatalf("incomplete or cross-category shelf: %+v %v", rows, err)
	}
	for _, row := range rows {
		if row.Stock <= 0 {
			t.Fatal("shelf not restocked")
		}
	}
	gold := findShopPrice(t, rows, "gold_line")
	if gold.Gold != 15 || gold.Silver != 0 {
		t.Fatal("currency changed during supplementation")
	}
	if findShopPrice(t, rows, "unpriced_line").Silver <= 0 {
		t.Fatal("upstream estimate missing")
	}
	if err := db.SetAdminShopOverride(ctx, "fishingstore_009", "gold_line", 0, 23, 0, "test"); err != nil {
		t.Fatal(err)
	}
	bought, err := db.ShopItemGet(ctx, "fishingstore_009", "gold_line")
	if err != nil || bought.Gold != 23 || bought.Stock != 1000 {
		t.Fatalf("override/restock inconsistent: %+v %v", bought, err)
	}
	admin, err := db.ListAdminStoreItems(ctx, "fishingstore_009", "gold_line", 10)
	if err != nil || len(admin) != 1 || admin[0].Gold != bought.Gold || admin[0].Stock != bought.Stock || !admin[0].HasOverride {
		t.Fatalf("admin differs: %+v %v", admin, err)
	}
	stores, err := db.ListAdminStores(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range stores {
		if v.StoreID == "fishingstore_009" {
			found = v.ItemCount == 4 && v.TotalStock == 4000 && v.OverrideCount == 1
		}
	}
	if !found {
		t.Fatal("admin summary omits augmented shelf")
	}
	if err := db.ResetAdminShopOverride(ctx, "fishingstore_009", "gold_line"); err != nil {
		t.Fatal(err)
	}
	restored, _ := db.ShopItemGet(ctx, "fishingstore_009", "gold_line")
	if restored.Gold != 15 || restored.Stock <= 0 {
		t.Fatal("reset broke supplement/restock")
	}
}
