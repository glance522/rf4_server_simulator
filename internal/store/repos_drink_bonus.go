package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type DrinkBonusStatus struct {
	Category        string
	RemainingMillis int64
	DurationMillis  int64
	SourceGUID      string
}

// AdvanceDrinkBonuses 结算本次在线间隔，离线和更换运行实例不补扣。
func (d *DB) AdvanceDrinkBonuses(ctx context.Context, uid int64, runKey string, now time.Time, online bool, maxGap time.Duration) ([]DrinkBonusStatus, error) {
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	statuses, err := AdvanceDrinkBonusesTx(ctx, tx, uid, runKey, now, online, maxGap)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return statuses, nil
}

// AdvanceDrinkBonusesTx 返回仍有效的分类，供饮用和钓获在同一事务内读取。
func AdvanceDrinkBonusesTx(ctx context.Context, tx *sql.Tx, uid int64, runKey string, now time.Time, online bool, maxGap time.Duration) ([]DrinkBonusStatus, error) {
	if tx == nil || uid <= 0 || strings.TrimSpace(runKey) == "" {
		return nil, errors.New("饮品计时参数无效")
	}
	// 即使该账户尚无时钟行，写语句也会先取得 SQLite 写锁。
	if _, err := tx.ExecContext(ctx, `UPDATE drink_bonus_clock SET online=online WHERE uid=?`, uid); err != nil {
		return nil, err
	}
	var previousOwner string
	var lastTick int64
	var previousOnline bool
	err := tx.QueryRowContext(ctx, `SELECT owner,last_tick,online FROM drink_bonus_clock WHERE uid=?`, uid).
		Scan(&previousOwner, &lastTick, &previousOnline)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	millis := now.UnixMilli()
	if exists && previousOwner == runKey {
		if previousOnline && millis > lastTick {
			elapsed := time.UnixMilli(millis).Sub(time.UnixMilli(lastTick)).Milliseconds()
			if maxGap > 0 {
				elapsed = min(elapsed, maxGap.Milliseconds())
			}
			if elapsed > 0 {
				if _, err := tx.ExecContext(ctx, `UPDATE drink_bonus_effect
					SET remaining_millis=MAX(0,remaining_millis-?) WHERE uid=? AND remaining_millis>0`, elapsed, uid); err != nil {
					return nil, err
				}
			}
		}
		// 时间回拨时保留高水位，恢复原时刻不会再扣一次。
		millis = max(millis, lastTick)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO drink_bonus_clock(uid,owner,last_tick,online)
		VALUES(?,?,?,?) ON CONFLICT(uid) DO UPDATE SET owner=excluded.owner,
		last_tick=excluded.last_tick,online=excluded.online`, uid, runKey, millis, online); err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx, `SELECT category,remaining_millis,duration_millis,source_guid
		FROM drink_bonus_effect WHERE uid=? AND remaining_millis>0 ORDER BY category`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	statuses := make([]DrinkBonusStatus, 0)
	for rows.Next() {
		var status DrinkBonusStatus
		if err := rows.Scan(&status.Category, &status.RemainingMillis, &status.DurationMillis, &status.SourceGUID); err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	return statuses, rows.Err()
}

// RefreshDrinkBonusTx 同类刷新为完整时长，其他分类保持原样。
func RefreshDrinkBonusTx(ctx context.Context, tx *sql.Tx, uid int64, category string, duration time.Duration, sourceGUID string) error {
	if tx == nil || uid <= 0 || strings.TrimSpace(category) == "" || duration.Milliseconds() <= 0 {
		return errors.New("饮品效果参数无效")
	}
	// 先写后读的调用方可与物品消耗共用这笔事务。
	_, err := tx.ExecContext(ctx, `INSERT INTO drink_bonus_effect(uid,category,remaining_millis,duration_millis,source_guid)
		VALUES(?,?,?,?,?) ON CONFLICT(uid,category) DO UPDATE SET
		remaining_millis=excluded.remaining_millis,duration_millis=excluded.duration_millis,source_guid=excluded.source_guid`,
		uid, category, duration.Milliseconds(), duration.Milliseconds(), sourceGUID)
	return err
}

// CleanupDrinkUseReceipts 删除过期的饮酒事务回执。无法解析的旧回执也会清理。
func (d *DB) CleanupDrinkUseReceipts(ctx context.Context, before time.Time) (int64, error) {
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		DELETE FROM kv
		WHERE k GLOB 'drink-use:*'
		  AND CASE WHEN json_valid(v)
		           THEN COALESCE(CAST(json_extract(v, '$.created_at') AS INTEGER), 0) < ?
		           ELSE 1 END`,
		before.UnixMilli())
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}
