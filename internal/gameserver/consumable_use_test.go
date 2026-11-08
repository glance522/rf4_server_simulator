package gameserver

import (
	"testing"
	"time"
)

// 吃东西：数量 -1，饱食度（player_condition.primary）上涨。
func TestFoodConsumableDeductsAndRaisesSatiety(t *testing.T) {
	s := newDrinkUseTestServer(t)
	food := drinkUseTestItem(t, s, "cake_rf4", 4, 0)
	ctx := drinkUseTestCtx(t, s, 50, food.GUID, 0)
	body, handled := hItemUse(ctx)
	if !handled || len(body) == 0 {
		t.Fatal("food use must be handled")
	}
	drinkUseTestQty(t, s, food.GUID, 3, 0)

	var sat float64
	if err := s.DB.SQL.QueryRow(`SELECT COALESCE(primary_value,0) FROM player_condition WHERE uid=1000001`).Scan(&sat); err != nil {
		t.Fatal(err)
	}
	if sat <= 0 || sat > 1 {
		t.Fatalf("satiety out of range: %v", sat)
	}
}

// 吃饱后再吃被拒，物品数量不变。
func TestFoodConsumableRejectsWhenFull(t *testing.T) {
	s := newDrinkUseTestServer(t)
	food := drinkUseTestItem(t, s, "cake_rf4", 2, 0)
	if _, err := s.DB.SQL.Exec(`INSERT INTO player_condition(uid, primary_value, secondary_value, updated_ts)
		VALUES(1000001, 1.0, 1.0, ?)
		ON CONFLICT(uid) DO UPDATE SET primary_value=1.0, updated_ts=excluded.updated_ts`,
		float64(time.Now().Unix())); err != nil {
		t.Fatal(err)
	}
	ctx := drinkUseTestCtx(t, s, 51, food.GUID, 0)
	if body, handled := hItemUse(ctx); !handled || body != nil {
		t.Fatal("full satiety must reject the use")
	}
	drinkUseTestQty(t, s, food.GUID, 2, 0)
}
