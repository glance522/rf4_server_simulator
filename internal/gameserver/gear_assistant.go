package gameserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const gearAssistantUID int64 = 2147483646
const gearAssistantName = "钓具助手"

func overloadProtectionKey(uid int64) string {
	return fmt.Sprintf("gear-assistant:overload-protection:%d", uid)
}

type overloadProtectionReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readOverloadProtection(ctx context.Context, db overloadProtectionReader, uid int64) (bool, error) {
	var raw string
	err := db.QueryRowContext(ctx, `SELECT v FROM kv WHERE k=?`, overloadProtectionKey(uid)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	switch raw {
	case "1":
		return true, nil
	case "0":
		return false, nil
	default:
		return false, fmt.Errorf("满红不爆设置无效")
	}
}

func gearAssistantReply(enabled bool, text string) (next bool, reply string) {
	next = enabled
	switch strings.TrimSpace(text) {
	case "开", "开启":
		next = true
		if enabled {
			return next, "满红不爆已经开启，无需重复设置。"
		}
		return next, "满红不爆已开启，对你的所有鱼竿生效。"
	case "关", "关闭":
		next = false
		if !enabled {
			return next, "满红不爆已经关闭，无需重复设置。"
		}
		return next, "满红不爆已关闭，恢复正常装备损坏规则。"
	}
	status := "关闭"
	if enabled {
		status = "开启"
	}
	reply = "满红不爆：" + status + "。"
	if strings.TrimSpace(text) != "状态" {
		reply += "\n发送「开」启用，「关」关闭，「状态」查询。\n仅防断件和锁轮过载烧毁；普通磨损、自然脱钩及主动剪线不变，不修复已有损伤。"
	}
	return next, reply
}

func (s *Server) receiveGearAssistant(ctx *Ctx, text string) ([]byte, bool) {
	if !ctx.ExpectResponse {
		return nil, true
	}
	uid := ctx.Sess.Userid
	if uid <= 0 {
		ctx.ReplyFailure("System.InvalidOperationException", "请先登录")
		return nil, true
	}
	// Serialize a change with damage/load decisions, including all rods and
	// concurrent sessions. Do not publish success before the setting is durable.
	s.overloadProtectionMu.Lock()
	defer s.overloadProtectionMu.Unlock()
	c := ctx.Sess.pushCtx()
	tx, err := beginEconomyTx(c, s.DB)
	if err != nil {
		ctx.ReplyFailure("System.IO.IOException", "钓具设置保存失败")
		return nil, true
	}
	defer func() { _ = tx.Rollback() }()
	enabled, err := readOverloadProtection(c, tx, uid)
	if err != nil {
		ctx.ReplyFailure("System.IO.IOException", "钓具设置读取失败，设置未改变")
		return nil, true
	}
	next, reply := gearAssistantReply(enabled, text)
	if next != enabled {
		value := "0"
		if next {
			value = "1"
		}
		_, err = tx.ExecContext(c, `INSERT INTO kv(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, overloadProtectionKey(uid), value)
	}
	var mailID int64
	if err == nil {
		mailID, err = insertContactReply(c, tx, uid, gearAssistantUID, reply)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		ctx.ReplyFailure("System.IO.IOException", "钓具设置保存失败，设置未改变")
		return nil, true
	}
	if next != enabled {
		now := time.Now()
		for _, st := range s.fishingStatesFor(uid) {
			st.mu.Lock()
			// Keep incurred wear; the timestamp only bounds later tick billing.
			st.reelWear.lastLoadAt = now
			st.reelWear.protectionChangedAt = now
			st.mu.Unlock()
		}
	}
	ctx.AfterResponse(func() { s.notifyMail(ctx.Sess.pushCtx(), uid, mailID) })
	return []byte{0}, true
}
