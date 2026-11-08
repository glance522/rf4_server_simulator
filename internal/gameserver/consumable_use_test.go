package gameserver

import (
	"database/sql"
	"testing"
)

// firstFoodItem 从测试静态库里挑一个真正算“食物”的物品：
// category 26 且不属于烹饪材料/窝料/鱼饵前缀，避免依赖某一条具体商品数据。
func firstFoodItem(t *testing.T, db interface {
	QueryRow(string, ...any) *sql.Row
}) string {
	t.Helper()
	var id string
	err := db.QueryRow(`
		SELECT item_id FROM item_def
		WHERE category=26
		  AND item_id NOT LIKE 'ff\_%'      ESCAPE '\'
		  AND item_id NOT LIKE 'craft\_%'   ESCAPE '\'
		  AND item_id NOT LIKE 'boils\_%'   ESCAPE '\'
		  AND item_id NOT LIKE 'fb\_%'      ESCAPE '\'
		  AND item_id NOT LIKE 'bait\_%'    ESCAPE '\'
		  AND item_id NOT LIKE 'dough\_%'   ESCAPE '\'
		  AND item_id NOT LIKE 'aroma\_%'   ESCAPE '\'
		  AND item_id NOT LIKE 'feedadds\_%' ESCAPE '\'
		  AND item_id NOT LIKE 'tool\_%'    ESCAPE '\'
		ORDER BY item_id LIMIT 1`).Scan(&id)
	if err != nil {
		t.Fatalf("找不到测试用食物定义: %v", err)
	}
	return id
}

// 吃东西：数量 -1。
func TestFoodConsumableDeductsQuantity(t *testing.T) {
	s := newDrinkUseTestServer(t)
	foodID := firstFoodItem(t, s.DB.SQL)
	food := drinkUseTestItem(t, s, foodID, 4, 0)
	ctx := drinkUseTestCtx(t, s, 50, food.GUID, 0)
	body, handled := hItemUse(ctx)
	if !handled || len(body) == 0 {
		t.Fatal("food use must be handled")
	}
	drinkUseTestQty(t, s, food.GUID, 3, 0)
}

// 同一 call 重放不再扣减。
func TestFoodConsumableReplaysWithoutDoubleConsume(t *testing.T) {
	s := newDrinkUseTestServer(t)
	foodID := firstFoodItem(t, s.DB.SQL)
	food := drinkUseTestItem(t, s, foodID, 4, 0)
	ctx := drinkUseTestCtx(t, s, 52, food.GUID, 0)
	body, handled := hItemUse(ctx)
	if !handled || len(body) == 0 {
		t.Fatal("food use must be handled")
	}
	drinkUseTestQty(t, s, food.GUID, 3, 0)
	replay := drinkUseTestCtx(t, s, 52, food.GUID, 0)
	if replayed, ok := hItemUse(replay); !ok || string(replayed) != string(body) {
		t.Fatal("same call must replay the committed body")
	}
	drinkUseTestQty(t, s, food.GUID, 3, 0)
}
