package gameserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type releaseReceipt struct {
	Level, XP, LevelsGained, Bonus int64
}

// releaseCatchOnce 放生奖励与消费 pending 同事务，重启后的重试不重复入账。
func (s *Server) releaseCatchOnce(ctx context.Context, uid int64, fishGUID string, bonus int64) (releaseReceipt, bool, error) {
	var result releaseReceipt
	if fishGUID == "" {
		return result, false, fmt.Errorf("缺少放生鱼标识")
	}
	if _, err := s.DB.EnsureProfile(ctx, uid); err != nil {
		return result, false, err
	}
	key := fmt.Sprintf("catch-release:%d:%s", uid, strings.ToLower(fishGUID))
	tx, err := s.DB.SQL.BeginTx(ctx, nil)
	if err != nil {
		return result, false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE kv SET v=v WHERE k=?`, key); err != nil {
		return result, false, err
	}
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT v FROM kv WHERE k=?`, key).Scan(&raw); err == nil {
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			return result, true, err
		}
		return result, true, nil
	} else if err != sql.ErrNoRows {
		return result, false, err
	}
	var oldLevel, oldXP int64
	if err := tx.QueryRowContext(ctx, `SELECT level,xp FROM profile WHERE uid=?`, uid).Scan(&oldLevel, &oldXP); err != nil {
		return result, false, err
	}
	if bonus < 0 {
		bonus = 0
	}
	result.XP = oldXP + bonus
	result.Level = levelForXp(result.XP)
	if result.Level < oldLevel {
		result.Level = oldLevel
	}
	if result.Level > maxLevel {
		result.Level = maxLevel
	}
	result.LevelsGained = max64(0, result.Level-oldLevel)
	result.Bonus = bonus
	if bonus > 0 {
		now := float64(time.Now().UnixNano()) / 1e9
		if _, err := tx.ExecContext(ctx, `UPDATE profile SET xp=?,level=?,updated_ts=? WHERE uid=?`, result.XP, result.Level, now, uid); err != nil {
			return result, false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO txn(uid,ts,kind,silver_delta,gold_delta,note) VALUES(?,?,'xp',0,0,'fishing_release')`, uid, now); err != nil {
			return result, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE fishing_state SET pending_json=NULL,cast_active=0,
		hooked=0,resolved=1 WHERE uid=? AND json_extract(pending_json,'$.fish_guid')=?`, uid, fishGUID); err != nil {
		return result, false, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return result, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO kv(k,v) VALUES(?,?)`, key, string(encoded)); err != nil {
		return result, false, err
	}
	if err := tx.Commit(); err != nil {
		return result, false, err
	}
	return result, false, nil
}
