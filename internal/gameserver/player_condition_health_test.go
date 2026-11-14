package gameserver

// 健康（f4）动态测试：饥寒压力掉血、压力消失后自愈、上下限钳制、
// 药品即时恢复，以及推进结果真的写回库。
//
// 期望值全部按 spec 里的规则手算，不用同一份代码回算。

import (
	"context"
	"math"
	"testing"
	"time"

	"rf4server/internal/store"
)

// 单个压力源（饿）：饱食 1h 后衰减到 0（低于 0.10 门槛），健康按 0.10/h 掉。
func TestAdvancePlayerConditionHealthDecaysWhenHungry(t *testing.T) {
	p := testConditionParams()
	cur := store.PlayerCondition{Primary: 0.85, Secondary: 0.05, Comfort: 1.0, UpdatedTS: 1000}
	next := advancePlayerCondition(cur, 1000+3600, p, conditionEnv{})
	if next.Secondary != 0 {
		t.Fatalf("饱食度应先触底: %v", next.Secondary)
	}
	if math.Abs(next.LuckPenalty-0.10) > 1e-9 {
		t.Fatalf("单个压力源 1h 健康应下降 0.10: %v", next.LuckPenalty)
	}
}

// 饥寒双压力源：掉血速率翻倍。0.5h 舒适度从 0 漂到 0.25（仍低于 0.30 门槛）。
func TestAdvancePlayerConditionHealthDecaysFasterUnderTwoStressors(t *testing.T) {
	p := testConditionParams()
	cur := store.PlayerCondition{Primary: 0.85, Secondary: 0.05, Comfort: 0.0, UpdatedTS: 1000}
	next := advancePlayerCondition(cur, 1000+1800, p, conditionEnv{Rain: true})
	// 雨天均衡点 0.65，漂移 0.5*0.5=0.25 → 舒适度 0.25 < 0.30，仍算一个压力源。
	if math.Abs(next.Comfort-0.25) > 1e-9 {
		t.Fatalf("0.5h 舒适度应漂到 0.25: %v", next.Comfort)
	}
	if math.Abs(next.LuckPenalty-0.10) > 1e-9 {
		t.Fatalf("两个压力源 0.5h 健康应下降 0.10: %v", next.LuckPenalty)
	}
}

// 无压力时自愈：2h 回升 0.10。
func TestAdvancePlayerConditionHealthRegenerates(t *testing.T) {
	p := testConditionParams()
	cur := store.PlayerCondition{Primary: 0.85, Secondary: 1.0, Comfort: 1.0, LuckPenalty: 0.40, UpdatedTS: 1000}
	next := advancePlayerCondition(cur, 1000+2*3600, p, conditionEnv{})
	if math.Abs(next.LuckPenalty-0.30) > 1e-9 {
		t.Fatalf("2h 无压力健康应回升 0.10: %v", next.LuckPenalty)
	}
}

// 上下限钳制：满血不会倒扣成负数，见底不会超过 1。
func TestAdvancePlayerConditionHealthClamps(t *testing.T) {
	p := testConditionParams()

	// 满血时继续自愈：2h 回升 0.10 后钳到 0，不会变成负数。
	// （跨度取短是刻意的：跨度过长会让饱食度先触底，反而变成压力源。）
	floor := advancePlayerCondition(
		store.PlayerCondition{Primary: 0.85, Secondary: 1.0, Comfort: 1.0, LuckPenalty: 0.10, UpdatedTS: 1000},
		1000+2*3600, p, conditionEnv{})
	if floor.LuckPenalty != 0 {
		t.Fatalf("健康不应回升越界: %v", floor.LuckPenalty)
	}

	// 跨度封顶 12h，单压力源 0.10*12=1.2 → 钳到 1。
	ceiling := advancePlayerCondition(
		store.PlayerCondition{Primary: 0.85, Secondary: 0.0, Comfort: 1.0, LuckPenalty: 0.90, UpdatedTS: 1000},
		1000+100*3600, p, conditionEnv{})
	if ceiling.LuckPenalty != 1 {
		t.Fatalf("健康不应跌破 0 对应的 1: %v", ceiling.LuckPenalty)
	}
}

// 健康开关关闭（condition_health_enabled=0）：饥寒双压力源也不掉血，
// f4 恒 0 满血透传，回到 PR317 之前的口径。
func TestAdvancePlayerConditionHealthDisabledForcesFull(t *testing.T) {
	p := testConditionParams()
	p.HealthEnabled = false
	cur := store.PlayerCondition{Primary: 0.85, Secondary: 0.0, Comfort: 0.0, LuckPenalty: 0.60, UpdatedTS: 1000}
	next := advancePlayerCondition(cur, 1000+1800, p, conditionEnv{})
	if next.LuckPenalty != 0 {
		t.Fatalf("健康开关关闭应恒满血(f4=0): %v", next.LuckPenalty)
	}
	// 与开启时对照：同一状态 0.5h，舒适度漂到 0.25 仍是双压力源，应掉 0.10。
	p.HealthEnabled = true
	enabled := advancePlayerCondition(cur, 1000+1800, p, conditionEnv{})
	if math.Abs(enabled.LuckPenalty-0.70) > 1e-9 {
		t.Fatalf("对照组双压力源 0.5h 应掉 0.10: %v", enabled.LuckPenalty)
	}
}

// 时间戳没前进（同一秒内多次读）时不应推进，也不应改写健康。
func TestAdvancePlayerConditionHealthNotAdvancedOnSameSecond(t *testing.T) {
	p := testConditionParams()
	cur := store.PlayerCondition{Primary: 0.85, Secondary: 0.0, Comfort: 0.0, LuckPenalty: 0.20, UpdatedTS: 1000}
	next := advancePlayerCondition(cur, 1000, p, conditionEnv{})
	if next.LuckPenalty != 0.20 {
		t.Fatalf("同一秒内不应推进健康: %v", next.LuckPenalty)
	}
}

// 健康变化必须写回库（写回阈值判定要覆盖 luck_penalty）。
func TestCurrentPlayerConditionPersistsHealth(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	old := float64(time.Now().Add(-2 * time.Hour).Unix())
	if err := db.UpsertPlayerCondition(ctx, store.PlayerCondition{
		UID: uid, Primary: 0.85, Secondary: 1.0, Comfort: 1.0, LuckPenalty: 0.40, UpdatedTS: old,
	}); err != nil {
		t.Fatal(err)
	}
	got := srv.currentPlayerCondition(ctx, uid)
	if math.Abs(got.LuckPenalty-0.30) > 1e-6 {
		t.Fatalf("2h 健康回升未生效: %v", got.LuckPenalty)
	}
	row, err := db.EnsurePlayerCondition(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(row.LuckPenalty-0.30) > 1e-6 {
		t.Fatalf("健康回升未持久化: %+v", row)
	}
}

// 药品（Sorbimax / pill_pack）走 4/8 使用通道，即时回健康并与扣物同事务。
func TestMedicineUseRestoresHealth(t *testing.T) {
	s := newDrinkUseTestServer(t)
	ctx0 := t.Context()
	const uid = int64(1000001)
	if _, err := s.DB.SQL.Exec(`INSERT INTO item_def(numeric_id,item_id,category,attrs_json)
		VALUES(900100,'pill_pack',26,'{"fopbhlmkcpe":3,"aekjeoifnkk":1}')`); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.UpsertPlayerCondition(ctx0, store.PlayerCondition{
		UID: uid, Primary: 0.85, Secondary: 0.50, Comfort: 1.0, LuckPenalty: 0.60,
		UpdatedTS: float64(time.Now().Unix()) + 3600,
	}); err != nil {
		t.Fatal(err)
	}
	pill := drinkUseTestItem(t, s, "pill_pack", 2, 0)
	ctx := drinkUseTestCtx(t, s, 58, pill.GUID, 0)
	body, handled := s.useAlcoholDrink(ctx)
	if !handled || len(body) == 0 {
		t.Fatalf("药品 4/8 应被消耗通道处理: handled=%v body=%d", handled, len(body))
	}
	drinkUseTestQty(t, s, pill.GUID, 1, 0)
	row, err := s.DB.EnsurePlayerCondition(ctx0, uid)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(row.LuckPenalty-0.10) > 1e-9 {
		t.Fatalf("药品应回健康 0.60-0.50: %v", row.LuckPenalty)
	}
	if row.Primary != 0.85 || row.Secondary != 0.50 || row.Comfort != 1.0 {
		t.Fatalf("药品不应改动其它状态条: %+v", row)
	}
	if len(ctx.afterResponse) == 0 {
		t.Fatal("药品使用后应补推 2/2")
	}
}

// 药品满血时使用不应把健康推成负数（f4 地板 0）。
func TestMedicineUseHealthFloor(t *testing.T) {
	s := newDrinkUseTestServer(t)
	ctx0 := t.Context()
	const uid = int64(1000001)
	if _, err := s.DB.SQL.Exec(`INSERT INTO item_def(numeric_id,item_id,category,attrs_json)
		VALUES(900100,'pill_pack',26,'{"fopbhlmkcpe":3,"aekjeoifnkk":1}')`); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.UpsertPlayerCondition(ctx0, store.PlayerCondition{
		UID: uid, Primary: 0.85, Secondary: 0.50, Comfort: 1.0, LuckPenalty: 0.10,
		UpdatedTS: float64(time.Now().Unix()) + 3600,
	}); err != nil {
		t.Fatal(err)
	}
	pill := drinkUseTestItem(t, s, "pill_pack", 2, 0)
	if body, handled := s.useAlcoholDrink(drinkUseTestCtx(t, s, 59, pill.GUID, 0)); !handled || len(body) == 0 {
		t.Fatalf("药品使用未处理: handled=%v body=%d", handled, len(body))
	}
	row, err := s.DB.EnsurePlayerCondition(ctx0, uid)
	if err != nil {
		t.Fatal(err)
	}
	if row.LuckPenalty != 0 {
		t.Fatalf("健康应被地板钳到满血(f4=0): %v", row.LuckPenalty)
	}
}

// 健康开关关闭时药品回到非消耗品：4/8 不处理（落回通用 handler）、
// 不扣数量、f4 落库值不动——Sorbimax 不白扔。
func TestMedicineUseNoOpWhenHealthDisabled(t *testing.T) {
	s := newDrinkUseTestServer(t)
	ctx0 := t.Context()
	const uid = int64(1000001)
	if _, err := s.DB.SQL.Exec(`INSERT INTO item_def(numeric_id,item_id,category,attrs_json)
		VALUES(900100,'pill_pack',26,'{"fopbhlmkcpe":3,"aekjeoifnkk":1}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.SQL.Exec(`INSERT INTO tunables(key,value) VALUES('condition_health_enabled','0')`); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.UpsertPlayerCondition(ctx0, store.PlayerCondition{
		UID: uid, Primary: 0.85, Secondary: 0.50, Comfort: 1.0, LuckPenalty: 0.60,
		UpdatedTS: float64(time.Now().Unix()) + 3600,
	}); err != nil {
		t.Fatal(err)
	}
	pill := drinkUseTestItem(t, s, "pill_pack", 2, 0)
	if body, handled := s.useAlcoholDrink(drinkUseTestCtx(t, s, 61, pill.GUID, 0)); handled {
		t.Fatalf("健康开关关闭时药品应落回通用 handler: body=%d", len(body))
	}
	drinkUseTestQty(t, s, pill.GUID, 2, 0)
	row, err := s.DB.EnsurePlayerCondition(ctx0, uid)
	if err != nil {
		t.Fatal(err)
	}
	if row.LuckPenalty != 0.60 {
		t.Fatalf("健康开关关闭时 f4 不应被药品改动: %v", row.LuckPenalty)
	}
}

// 消耗品分类表：药品只收 pill_pack，饵料香料（如 aroma_garlic）不得混进来。
func TestConsumableRestoreMedicineClassification(t *testing.T) {
	if kind, ok := consumableRestoreForItem("pill_pack"); !ok || kind != "medicine" {
		t.Fatalf("pill_pack 应归药品类: kind=%q ok=%v", kind, ok)
	}
	for _, itemID := range []string{"aroma_garlic", "dough_garlic", "ff_garlic", "boils_hvz_dg12"} {
		if _, ok := consumableRestoreForItem(itemID); ok {
			t.Fatalf("%s 是饵料香料/面团，不应进消耗品恢复表", itemID)
		}
	}
}
