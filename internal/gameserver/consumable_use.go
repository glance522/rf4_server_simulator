package gameserver

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"rf4server/internal/proto"
)

// foodConsumableCategory 是食物/饮料的物品分类（item_def.category）。
const foodConsumableCategory = 26

type foodUseReceipt struct {
	BodySHA256     string
	ExpectResponse bool
	Body           []byte
	CreatedAt      int64 `json:"created_at"`
}

// useFoodConsumable 处理 4/8 中 category 26 的可食用消耗品：
// 在同一事务内把数量 -1，回执落 kv 保证重放幂等，并返回更新后的 800 快照。
//
// 酒类（drinkRuleForItem 命中）与技能书（category 80）由各自处理器先行接管，
// 不会走到这里；此函数只处理“吃掉/喝掉一份”的通用消耗语义。
func (s *Server) useFoodConsumable(ctx *Ctx) ([]byte, bool) {
	if !s.requireDB(ctx) {
		return nil, false
	}
	storage, hasStorage := ctx.ArgInt(0)
	if ctx.Tree == nil || ctx.Tree.Kind != proto.KindArgs || len(ctx.Tree.Items) != 2 {
		return nil, false
	}
	arg := ctx.Tree.ArgItem(1)
	if arg == nil || arg.Kind != proto.KindGuid || !validRigGUID(arg.Hex) {
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
		ctx.Log.Warn("使用消耗品未完成", "err", err)
		return fail("使用未完成，请重试")
	}
	cctx, uid := ctx.Sess.pushCtx(), ctx.Sess.Userid
	key, bodySHA := requestReceiptKey(ctx)
	receiptKey := fmt.Sprintf("food-use:%d:%s:%d:%d:%d", uid, key.TokenHash, key.Main, key.Sub, key.CallID)

	var err error
	tx, err = beginEconomyTx(cctx, s.DB)
	if err != nil {
		return failed(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(cctx, `UPDATE kv SET v=v WHERE k=?`, receiptKey); err != nil {
		return failed(err)
	}
	var saved string
	err = tx.QueryRowContext(cctx, `SELECT v FROM kv WHERE k=?`, receiptKey).Scan(&saved)
	if err == nil {
		var receipt foodUseReceipt
		if err := json.Unmarshal([]byte(saved), &receipt); err != nil {
			return failed(err)
		}
		if receipt.BodySHA256 != bodySHA || receipt.ExpectResponse != ctx.ExpectResponse {
			return fail("使用请求与已处理的请求不一致")
		}
		return receipt.Body, true
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return failed(err)
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
	if def.Category != foodConsumableCategory {
		return nil, false
	}
	if _, isDrink := drinkRuleForItem(def.ItemID); isDrink {
		return nil, false
	}
	if ctx.Sess.Closed() {
		return fail("会话已关闭，请重新连接")
	}
	if !hasStorage || storage != 0 || row.UID != uid || row.Container != "backpack" || row.RigGUID != "" {
		return fail("只能使用自己背包中的物品")
	}
	if row.Qty <= 0 {
		return fail("物品数量不足，请刷新背包")
	}
	if _, err := consumeItemQtyTx(cctx, tx, row, 1); err != nil {
		return failed(err)
	}

	var updated []*proto.Value
	var removed []string
	if row.Qty == 0 {
		removed = []string{row.GUID}
	} else {
		fields, ferr := FieldsFromJSON(row.FieldsJSON)
		if ferr != nil {
			return failed(ferr)
		}
		tree := proto.Obj(row.WireClass, uint32(row.TypeID), fields...)
		stateFlagsZeroFill(row.WireClass, tree, def)
		updated = []*proto.Value{tree}
	}
	body, berr := proto.EncodeValue(buildRigSnapshot800(nil, updated, removed), ctx.Sess.Table())
	if berr != nil {
		return failed(berr)
	}
	receipt, rerr := json.Marshal(foodUseReceipt{
		BodySHA256: bodySHA, ExpectResponse: ctx.ExpectResponse, Body: body,
		CreatedAt: time.Now().UnixMilli(),
	})
	if rerr != nil {
		return failed(rerr)
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
