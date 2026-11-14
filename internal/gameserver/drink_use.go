package gameserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// foodSatietyFullThreshold：饱食度到这个值就视为“吃饱”，再吃只会浪费物品。
// 用略低于 1.0 的阈值，避免浮点累加尾差让玩家永远差一点点吃不饱。
const foodSatietyFullThreshold = 0.995

// operationResultPlayerFull 官服操作结果码 55：玩家已吃饱
// （kOperationResult_PlayerFull，客户端枚举 abadjfbfdjb.lpkkndpoicm）。
const operationResultPlayerFull = 55

type drinkUseReceipt struct {
	BodySHA256     string
	ExpectResponse bool
	Body           []byte
	CreatedAt      int64 `json:"created_at"`
}

// currentConditionForUseTx 读消耗品生效前的状态行（与经济事务同连接）。
// 行不存在时 ok=false，表示按协议默认值处理：交给 applyConsumableRestoreTx 建行。
func currentConditionForUseTx(ctx context.Context, tx *sql.Tx, uid int64) (store.PlayerCondition, bool, error) {
	cur, err := store.EnsurePlayerConditionTx(ctx, tx, uid)
	if err != nil {
		return store.PlayerCondition{}, false, err
	}
	if cur == nil {
		return store.PlayerCondition{}, false, nil
	}
	return *cur, true, nil
}

// useAlcoholDrink 将扣物、酒兴/消耗品恢复和业务回执一起提交；其它物品沿用原处理器。
// 酒类按经验加成规则（drink_rules.go），食品/热饮按状态恢复规则
// （consumableRestoreForItem：咖啡→精力、茶→舒适度、可可→饱食+精力、食品→饱食度）。
func (s *Server) useAlcoholDrink(ctx *Ctx) ([]byte, bool) {
	if !s.requireDB(ctx) {
		return nil, false
	}
	var tx *sql.Tx
	fail := func(message string) ([]byte, bool) {
		if tx != nil {
			_ = tx.Rollback()
		}
		ctx.ReplyFailure("System.InvalidOperationException", message)
		return nil, true
	}
	failed := func(err error) ([]byte, bool) {
		ctx.Log.Warn("饮用未完成", "err", err)
		return fail("饮用未完成，请重试")
	}
	cctx, uid := ctx.Sess.pushCtx(), ctx.Sess.Userid
	// tunableF 首次装载有 2 秒超时的表查询，不能发生在持锁事务内
	//（查询等待锁会超时并静默回退默认值）；统一在开事务前取好状态机参数，
	// 并叠加当前装备的精力上限/恢复加成。
	cond := s.conditionParamsForUID(cctx, uid)
	key, bodySHA := requestReceiptKey(ctx)
	receiptKey := fmt.Sprintf("drink-use:%d:%s:%d:%d:%d", uid, key.TokenHash, key.Main, key.Sub, key.CallID)
	var err error
	tx, err = beginEconomyTx(cctx, s.DB)
	if err != nil {
		return failed(err)
	}
	defer tx.Rollback()
	// 先取写锁，避免读取快照后再升级锁失败。
	if _, err := tx.ExecContext(cctx, `UPDATE kv SET v=v WHERE k=?`, receiptKey); err != nil {
		return failed(err)
	}

	var saved string
	err = tx.QueryRowContext(cctx, `SELECT v FROM kv WHERE k=?`, receiptKey).Scan(&saved)
	if err == nil {
		var receipt drinkUseReceipt
		if err := json.Unmarshal([]byte(saved), &receipt); err != nil {
			return failed(err)
		}
		if receipt.BodySHA256 != bodySHA || receipt.ExpectResponse != ctx.ExpectResponse {
			return fail("饮用请求与已处理的请求不一致")
		}
		return receipt.Body, true
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return failed(err)
	}

	storage, hasStorage := ctx.ArgInt(0)
	if ctx.Tree == nil || ctx.Tree.Kind != proto.KindArgs || len(ctx.Tree.Items) != 2 {
		return nil, false
	}
	arg := ctx.Tree.ArgItem(1)
	if arg == nil || arg.Kind != proto.KindGuid || !validRigGUID(arg.Hex) {
		return nil, false
	}
	row, err := getItemTx(cctx, tx, arg.Hex)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		return failed(err)
	}
	def, err := itemDefByNumericIDTx(cctx, tx, row.NumericID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		return failed(err)
	}
	rule, isDrink := drinkRuleForItem(def.ItemID)
	restoreKind, isConsumable := "", false
	if !isDrink {
		restoreKind, isConsumable = consumableRestoreForItem(def.ItemID)
		// 状态机总开关关闭时食品/热饮维持旧行为（落回通用 handler，不扣不恢复）。
		if isConsumable && !cond.Enabled {
			isConsumable = false
		}
	}
	if !isDrink && !isConsumable {
		return nil, false
	}
	if ctx.Sess.Closed() {
		return fail("会话已关闭，请重新连接")
	}
	if !hasStorage || storage != 0 || row.UID != uid || row.Container != "backpack" || row.RigGUID != "" {
		return fail("只能使用自己背包中的消耗品")
	}
	if def.Category != 26 || row.WireClass != "aafdnnmleob" || row.TypeID != 128 {
		return fail("消耗品状态无效，请刷新背包")
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return failed(err)
	}
	qtyIdx := itemQtyFieldIdx(row.WireClass)
	if len(fields) != len(s.table.OpsFor(row.WireClass)) {
		return fail("饮品数量无效，请刷新背包")
	}
	if qtyIdx < 0 || qtyIdx >= len(fields) || fields[qtyIdx].Num == nil || row.Qty <= 0 ||
		u64Of(fields[qtyIdx].Num) != uint64(row.Qty) || uint64(row.Qty) > 0xffffffff {
		return fail("饮品数量无效，请刷新背包")
	}

	if isConsumable {
		cur, ok, err := currentConditionForUseTx(cctx, tx, uid)
		if err != nil {
			return failed(err)
		}
		if ok && !consumableRestoreChanges(cur, restoreKind, cond) {
			// 该消耗品推不动它负责的状态条：扣物只会白白损失一份物品。
			// 主食类（食品、以及以饱食为主的可可）在饱食度已满时仍回官服结果码 55
			// 显示「已吃饱」（satiety-full-reject 的既有语义）；其余
			// （咖啡精力满/茶舒适满/药品满血）落回通用 4/8 handler，
			// 物品原样保留、客户端不显示扣物。
			_ = tx.Rollback()
			if cur.Secondary >= foodSatietyFullThreshold &&
				(restoreKind == "food" || restoreKind == "cocoa") {
				ctx.ReplyOperationResult(operationResultPlayerFull)
				return nil, true
			}
			return nil, false
		}
	}

	if isDrink {
		if _, err := store.AdvanceDrinkBonusesTx(cctx, tx, uid, s.drinkRunKey(), time.Now(), s.drinkOnline(uid), s.drinkMaxGap()); err != nil {
			return failed(err)
		}
	}
	if _, err := consumeItemQtyTx(cctx, tx, row, 1); err != nil {
		return failed(err)
	}
	if isDrink {
		if err := store.AddDrinkBonusTx(cctx, tx, uid, rule.Category, rule.Peak, rule.Duration, row.GUID); err != nil {
			return failed(err)
		}
	}
	if isConsumable {
		// 消耗品恢复：与扣物同事务，失败整体回滚。
		if err := applyConsumableRestoreTx(cctx, tx, uid, restoreKind, cond, time.Now()); err != nil {
			return failed(err)
		}
	}

	var updated []*proto.Value
	var removed []string
	if row.Qty == 0 {
		removed = []string{row.GUID}
	} else {
		fields, err = FieldsFromJSON(row.FieldsJSON)
		if err != nil {
			return failed(err)
		}
		tree := proto.Obj(row.WireClass, uint32(row.TypeID), fields...)
		stateFlagsZeroFill(row.WireClass, tree, def)
		updated = []*proto.Value{tree}
	}
	body, err := proto.EncodeValue(buildRigSnapshot800(nil, updated, removed), ctx.Sess.Table())
	if err != nil {
		return failed(err)
	}
	receipt, err := json.Marshal(drinkUseReceipt{
		BodySHA256: bodySHA, ExpectResponse: ctx.ExpectResponse, Body: body,
		CreatedAt: time.Now().UnixMilli(),
	})
	if err != nil {
		return failed(err)
	}
	if _, err := tx.ExecContext(cctx, `INSERT INTO kv(k,v) VALUES(?,?)`, receiptKey, string(receipt)); err != nil {
		return failed(err)
	}
	if err := tx.Commit(); err != nil {
		return failed(err)
	}
	change := baitChange{Removed: removed}
	if len(updated) > 0 {
		change.Updated = []string{row.GUID}
	}
	ctx.AfterResponse(func() { ctx.Sess.pushBaitChange(change) })
	if isConsumable {
		// 恢复生效后立即补推一次 2/2，让对应状态条平滑回升。
		ctx.AfterResponse(func() { pushKeepalive(ctx.Sess) })
	}
	return body, true
}
