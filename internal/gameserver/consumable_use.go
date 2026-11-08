package gameserver

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// foodConsumableCategory 是食物/饮料的物品分类（item_def.category）。
const foodConsumableCategory = 26

// satietyMax 是饱食度上限（player_condition.primary 的取值范围 0~1）。
const satietyMax = 1.0

// 使用一份消耗品增加的饱食度：固体食物补得多，饮料补得少，酒水最少。
const (
	foodSatietySolid   = 0.10
	foodSatietyDrink   = 0.06
	foodSatietyDefault = 0.10
	drinkSatietyGain   = 0.05
	// fightEnergyPerSecond 是搏鱼/遛鱼期间每秒消耗的体力（player_condition.primary）。
	fightEnergyPerSecond = 0.01
	// energyRegenPerSecond 是食物条满时每秒回复的体力（按食物条比例缩放）。
	energyRegenPerSecond = 1.0 / 30.0
	// satietyDecayPerMinute 是饱食度自然回落速度（每现实分钟）。
	satietyDecayPerMinute = 0.08
)

// foodSatietyGain 按 item_id 粗略区分饮料与固体食物，返回本次进食的饱食度增量。
func foodSatietyGain(def *store.ItemDef) float64 {
	if def == nil {
		return foodSatietyDefault
	}
	id := strings.ToLower(def.ItemID)
	for _, kw := range []string{"juice", "water", "tea", "coffee", "kvas", "milk", "drink", "soda", "_can", "bottle"} {
		if strings.Contains(id, kw) {
			return foodSatietyDrink
		}
	}
	return foodSatietySolid
}

// satietyForTx 读取玩家当前饱食度（player_condition.secondary，对应客户端“食物”条，0~1），
// 并按上次更新时间折算自然回落（每分钟 -satietyDecayPerMinute），结果写回同一事务。
func satietyForTx(ctx *Ctx, tx *sql.Tx, uid int64) (float64, error) {
	cctx := ctx.Sess.pushCtx()
	var s, ts float64
	err := tx.QueryRowContext(cctx,
		`SELECT COALESCE(secondary_value,0), COALESCE(updated_ts,0) FROM player_condition WHERE uid=?`,
		uid).Scan(&s, &ts)
	if errors.Is(err, sql.ErrNoRows) {
		return store.DefaultPlayerConditionSecondary, nil
	}
	if err != nil {
		return 0, err
	}
	now := float64(time.Now().Unix())
	if ts > 0 && now > ts && s > 0 {
		decayed := s - satietyDecayPerMinute*(now-ts)/60.0
		if decayed < 0 {
			decayed = 0
		}
		if decayed != s {
			if _, uerr := tx.ExecContext(cctx,
				`UPDATE player_condition SET secondary_value=?, updated_ts=? WHERE uid=?`,
				decayed, now, uid); uerr != nil {
				return 0, uerr
			}
			s = decayed
		}
	}
	return s, nil
}

// addSatietyTx 在当前事务内增加饱食度（食物条），并按 2/3 比例顺带回复少量体力。
func addSatietyTx(ctx *Ctx, tx *sql.Tx, uid int64, delta float64) error {
	if delta <= 0 {
		return nil
	}
	return ctx.Sess.Srv.DB.AddConditionTx(ctx.Sess.pushCtx(), tx, uid, delta*2/9, delta)
}

// useFoodConsumable 处理 4/8 中 category 26 的可食用消耗品：
// 饱食度未满时把数量 -1、饱食度 += 增量，回执落 kv 保证重放幂等，
// 并返回更新后的 800 快照。饱食度已满时直接拒绝，提示“人物已经吃饱”。
//
// 酒类（drinkRuleForItem 命中）与技能书（category 80）由各自处理器先行接管。
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
	satiety, err := satietyForTx(ctx, tx, uid)
	if err != nil {
		return failed(err)
	}
	if satiety >= satietyMax-0.005 {
		return fail("人物已经吃饱")
	}
	if _, err := consumeItemQtyTx(cctx, tx, row, 1); err != nil {
		return failed(err)
	}
	if err := addSatietyTx(ctx, tx, uid, foodSatietyGain(def)); err != nil {
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

type foodUseReceipt struct {
	BodySHA256     string
	ExpectResponse bool
	Body           []byte
	CreatedAt      int64 `json:"created_at"`
}
