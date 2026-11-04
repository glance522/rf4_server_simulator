package gameserver

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

type drinkUseReceipt struct {
	BodySHA256     string
	ExpectResponse bool
	Body           []byte
	CreatedAt      int64 `json:"created_at"`
}

// useAlcoholDrink 将扣物、酒兴和业务回执一起提交；其它物品沿用原处理器。
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
	if !isDrink {
		return nil, false
	}
	if ctx.Sess.Closed() {
		return fail("会话已关闭，请重新连接")
	}
	if !hasStorage || storage != 0 || row.UID != uid || row.Container != "backpack" || row.RigGUID != "" {
		return fail("只能饮用自己背包中的饮品")
	}
	if def.Category != 26 || row.WireClass != "aafdnnmleob" || row.TypeID != 128 {
		return fail("饮品状态无效，请刷新背包")
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return failed(err)
	}
	qtyIdx := fieldIdxByCS(row.WireClass, csQty)
	if len(fields) != len(s.table.OpsFor(row.WireClass)) {
		return fail("饮品数量无效，请刷新背包")
	}
	if qtyIdx < 0 || qtyIdx >= len(fields) || fields[qtyIdx].Num == nil || row.Qty <= 0 ||
		u64Of(fields[qtyIdx].Num) != uint64(row.Qty) || uint64(row.Qty) > 0xffffffff {
		return fail("饮品数量无效，请刷新背包")
	}

	if _, err := store.AdvanceDrinkBonusesTx(cctx, tx, uid, s.drinkRunKey(), time.Now(), s.drinkOnline(uid), s.drinkMaxGap()); err != nil {
		return failed(err)
	}
	if _, err := consumeItemQtyTx(cctx, tx, row, 1); err != nil {
		return failed(err)
	}
	if err := store.RefreshDrinkBonusTx(cctx, tx, uid, rule.Category, rule.Duration, row.GUID); err != nil {
		return failed(err)
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
	return body, true
}
