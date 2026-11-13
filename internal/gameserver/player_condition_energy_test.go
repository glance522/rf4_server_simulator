package gameserver

import (
	"errors"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// seedConditionPrimary 写入一条"刚刚对齐时钟"的状态行，避免推进函数引入额外增量。
func seedConditionPrimary(t *testing.T, srv *Server, uid int64, primary float64) {
	t.Helper()
	c := store.PlayerCondition{
		UID: uid, Primary: primary, Secondary: 0.9946, Comfort: 1.0, LuckPenalty: 0,
		UpdatedTS: float64(time.Now().Unix()),
	}
	if err := srv.DB.UpsertPlayerCondition(t.Context(), c); err != nil {
		t.Fatal(err)
	}
}

func readConditionPrimary(t *testing.T, srv *Server, uid int64) float64 {
	t.Helper()
	c, err := srv.DB.EnsurePlayerCondition(t.Context(), uid)
	if err != nil || c == nil {
		t.Fatalf("读取状态行失败: %+v err=%v", c, err)
	}
	return c.Primary
}

func countBackpackRows(t *testing.T, srv *Server, uid int64) int64 {
	t.Helper()
	var n int64
	if err := srv.DB.SQL.QueryRow(
		`SELECT COUNT(*) FROM item WHERE uid=? AND container='backpack'`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// digOnce 发一次 9/3 挖饵请求，返回 803 的 f0（1=成功，0=失败）与字节数。
func digOnce(t *testing.T, srv *Server, toolGUID string) (uint64, int) {
	t.Helper()
	hctx := &Ctx{Sess: newProbeSess(srv), Main: 9, Sub: 3,
		Tree: proto.Args524(proto.Guid(toolGUID)), DecodeOK: true, Log: logging.Discard()}
	body, handled := hCraftMine2(hctx)
	if !handled || len(body) == 0 {
		t.Fatalf("9/3 挖饵未处理: handled=%v body=%d", handled, len(body))
	}
	result := decodeTreeValue(t, srv, body)
	if result.TypeID != 803 {
		t.Fatalf("9/3 应回 803，实得 %d", result.TypeID)
	}
	return u64Of(result.Field(0).Num), len(body)
}

// 挖饵成功必须扣体力，且扣在与饵入包同一个事务里。
func TestBaitDigSpendsEnergy(t *testing.T) {
	srv, _ := newDetachTestServer(t)
	const uid = int64(1000001)
	tool, err := srv.CreateItem(t.Context(), uid, baitShovelItemID, "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	srv.tunCache = map[string]float64{tunableBaitDigEnergyCost: 0.15}
	srv.tunAt = time.Now()
	seedConditionPrimary(t, srv, uid, 0.80)

	if status, _ := digOnce(t, srv, tool.GUID); status != 1 {
		t.Fatalf("体力充足时挖饵应成功，实得 f0=%d", status)
	}
	if got := readConditionPrimary(t, srv, uid); got < 0.64 || got > 0.66 {
		t.Fatalf("挖饵后体力=%.4f，期望约 0.65（0.80-0.15）", got)
	}
}

// 体力不足一次挖饵的消耗量时拒绝，且不产生任何产出。
func TestBaitDigRejectedWhenEnergyTooLow(t *testing.T) {
	srv, _ := newDetachTestServer(t)
	const uid = int64(1000001)
	tool, err := srv.CreateItem(t.Context(), uid, baitShovelItemID, "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	srv.tunCache = map[string]float64{tunableBaitDigEnergyCost: 0.15}
	srv.tunAt = time.Now()
	seedConditionPrimary(t, srv, uid, 0.10)
	before := countBackpackRows(t, srv, uid)

	if status, _ := digOnce(t, srv, tool.GUID); status != 0 {
		t.Fatalf("体力不足时挖饵应失败，实得 f0=%d", status)
	}
	if after := countBackpackRows(t, srv, uid); after != before {
		t.Fatalf("被拒的挖饵不应产出物品: before=%d after=%d", before, after)
	}
	if got := readConditionPrimary(t, srv, uid); got < 0.09 || got > 0.11 {
		t.Fatalf("被拒的挖饵不应扣体力: 体力=%.4f", got)
	}
}

// 消耗量配置为 0 时退化为旧行为（不扣、不拒），便于运维一键关掉。
func TestBaitDigEnergyCostZeroIsNoop(t *testing.T) {
	srv, _ := newDetachTestServer(t)
	const uid = int64(1000001)
	tool, err := srv.CreateItem(t.Context(), uid, baitShovelItemID, "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	srv.tunCache = map[string]float64{tunableBaitDigEnergyCost: 0}
	srv.tunAt = time.Now()
	seedConditionPrimary(t, srv, uid, 0.01)

	if status, _ := digOnce(t, srv, tool.GUID); status != 1 {
		t.Fatalf("消耗为 0 时挖饵应照常成功，实得 f0=%d", status)
	}
	if got := readConditionPrimary(t, srv, uid); got < 0.0 || got > 0.02 {
		t.Fatalf("消耗为 0 时不应扣体力: 体力=%.4f", got)
	}
}

// 总开关关闭时 spendEnergyTx 必须是纯 no-op（不写库）。
func TestSpendEnergyTxDisabledIsNoop(t *testing.T) {
	srv, _ := newDetachTestServer(t)
	const uid = int64(1000001)
	seedConditionPrimary(t, srv, uid, 0.50)
	tx, err := beginEconomyTx(t.Context(), srv.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	spent, err := spendEnergyTx(t.Context(), tx, uid, conditionParams{Enabled: false}, conditionEnv{}, 0.15)
	if err != nil || spent != 0 {
		t.Fatalf("关闭时应 no-op: spent=%v err=%v", spent, err)
	}
}

// 事务回滚后体力必须复原：证明扣减真的在调用方事务内，而不是独立写。
func TestSpendEnergyTxRollsBackWithCaller(t *testing.T) {
	srv, _ := newDetachTestServer(t)
	const uid = int64(1000001)
	seedConditionPrimary(t, srv, uid, 0.80)
	params := conditionParams{
		Enabled: true, EnergyMax: 0.85, EnergyRegenPerHour: 0,
		HungerRegenFloor: 0.25, MaxBackfillHours: 12,
	}
	tx, err := beginEconomyTx(t.Context(), srv.DB)
	if err != nil {
		t.Fatal(err)
	}
	spent, err := spendEnergyTx(t.Context(), tx, uid, params, conditionEnv{}, 0.15)
	if err != nil || spent != 0.15 {
		t.Fatalf("事务内扣减失败: spent=%v err=%v", spent, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := readConditionPrimary(t, srv, uid); got < 0.79 || got > 0.81 {
		t.Fatalf("回滚后体力应复原为 0.80，实得 %.4f", got)
	}
}

// 体力刚好等于消耗量时应放行（落到 0），差一点点则拒绝。
func TestSpendEnergyTxBoundary(t *testing.T) {
	srv, _ := newDetachTestServer(t)
	const uid = int64(1000001)
	params := conditionParams{
		Enabled: true, EnergyMax: 0.85, EnergyRegenPerHour: 0,
		HungerRegenFloor: 0.25, MaxBackfillHours: 12,
	}

	seedConditionPrimary(t, srv, uid, 0.15)
	tx, err := beginEconomyTx(t.Context(), srv.DB)
	if err != nil {
		t.Fatal(err)
	}
	spent, err := spendEnergyTx(t.Context(), tx, uid, params, conditionEnv{}, 0.15)
	if err != nil || spent != 0.15 {
		t.Fatalf("刚好够时应放行: spent=%v err=%v", spent, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := readConditionPrimary(t, srv, uid); got > 0.001 {
		t.Fatalf("刚好够时体力应落到 0，实得 %.4f", got)
	}

	seedConditionPrimary(t, srv, uid, 0.1499)
	tx2, err := beginEconomyTx(t.Context(), srv.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx2.Rollback() }()
	if _, err := spendEnergyTx(t.Context(), tx2, uid, params, conditionEnv{}, 0.15); !errors.Is(err, errEnergyNotEnough) {
		t.Fatalf("差一点点应返回 errEnergyNotEnough，实得 %v", err)
	}
}

// 9/3 的两个数字完全相同的请求必须复用统一 dispatchRequest 回执：
// 第二次只重放第一次的响应，不得再次扣精力或再次发饵。
func TestBaitDigReplayThroughReceiptsDoesNotSpendTwice(t *testing.T) {
	srv, _ := newRigTestServer(t)
	const uid = int64(1000001)
	tool, err := srv.CreateItem(t.Context(), uid, baitShovelItemID, "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	srv.tunCache = map[string]float64{tunableBaitDigEnergyCost: 0.15}
	srv.tunAt = time.Now()
	seedConditionPrimary(t, srv, uid, 0.80)
	beforeRows := countBackpackRows(t, srv, uid)

	h := newReceiptTestSession(t, srv, 1, "1|2|1000001|baitdig-replay")
	body, err := proto.EncodeValue(proto.Args524(proto.Guid(tool.GUID)), srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	h.send(9, 3, 501, body, 0x00)
	first := waitReceiptResponse(t, h)
	firstRows := countBackpackRows(t, srv, uid)
	firstEnergy := readConditionPrimary(t, srv, uid)

	h.send(9, 3, 501, body, 0x00)
	second := waitReceiptResponse(t, h)
	if string(second.Body) != string(first.Body) {
		t.Fatalf("重放必须返回第一次的响应: first=%x second=%x", first.Body, second.Body)
	}
	if rows := countBackpackRows(t, srv, uid); rows != firstRows {
		t.Fatalf("重放不得再发饵: first_rows=%d replay_rows=%d before=%d", firstRows, rows, beforeRows)
	}
	if energy := readConditionPrimary(t, srv, uid); energy < firstEnergy-1e-6 || energy > firstEnergy+1e-6 {
		t.Fatalf("重放不得二次扣精力: first_energy=%v replay_energy=%v", firstEnergy, energy)
	}
}
