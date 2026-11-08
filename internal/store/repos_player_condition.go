package store

import (
	"context"
	"database/sql"
	"time"
)

const (
	DefaultPlayerConditionPrimary   = 0.85
	DefaultPlayerConditionSecondary = 0.9946
)

type PlayerCondition struct {
	UID       int64
	Primary   float64
	Secondary float64
	UpdatedTS float64
}

func (d *DB) EnsurePlayerCondition(ctx context.Context, uid int64) (*PlayerCondition, error) {
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO player_condition(uid, primary_value, secondary_value, updated_ts)
		VALUES(?,?,?,?) ON CONFLICT(uid) DO NOTHING`,
		uid, DefaultPlayerConditionPrimary, DefaultPlayerConditionSecondary, float64(time.Now().Unix()))
	if err != nil {
		return nil, err
	}
	var v PlayerCondition
	err = d.SQL.QueryRowContext(ctx, `
		SELECT uid, primary_value, secondary_value, updated_ts
		FROM player_condition WHERE uid=?`, uid).Scan(&v.UID, &v.Primary, &v.Secondary, &v.UpdatedTS)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func (d *DB) UpsertPlayerCondition(ctx context.Context, v PlayerCondition) error {
	if v.Primary < 0 {
		v.Primary = 0
	}
	if v.Secondary < 0 {
		v.Secondary = 0
	}
	if v.UpdatedTS <= 0 {
		v.UpdatedTS = float64(time.Now().Unix())
	}
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO player_condition(uid, primary_value, secondary_value, updated_ts)
		VALUES(?,?,?,?)
		ON CONFLICT(uid) DO UPDATE SET
			primary_value=excluded.primary_value,
			secondary_value=excluded.secondary_value,
			updated_ts=excluded.updated_ts`, v.UID, v.Primary, v.Secondary, v.UpdatedTS)
	return err
}

// GetConditionTx 读取玩家状态条（primary=饱食度，secondary=体力）。
func (d *DB) GetConditionTx(ctx context.Context, tx *sql.Tx, uid int64) (float64, float64, error) {
	var p, s float64
	err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(primary_value,0), COALESCE(secondary_value,0) FROM player_condition WHERE uid=?`,
		uid).Scan(&p, &s)
	if err == sql.ErrNoRows {
		return DefaultPlayerConditionPrimary, DefaultPlayerConditionSecondary, nil
	}
	if err != nil {
		return 0, 0, err
	}
	return p, s, nil
}

// AddConditionTx 在当前事务内调整状态条并截断到 [0,1]。
func (d *DB) AddConditionTx(ctx context.Context, tx *sql.Tx, uid int64, dPrimary, dSecondary float64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO player_condition(uid, primary_value, secondary_value, updated_ts)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(uid) DO UPDATE SET
			primary_value   = MIN(1.0, MAX(0.0, COALESCE(primary_value,0) + ?)),
			secondary_value = MIN(1.0, MAX(0.0, COALESCE(secondary_value,0) + ?)),
			updated_ts      = excluded.updated_ts`,
		uid, clamp01(DefaultPlayerConditionPrimary+dPrimary), clamp01(DefaultPlayerConditionSecondary+dSecondary), float64(time.Now().Unix()),
		dPrimary, dSecondary)
	return err
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// AddCondition 与 AddConditionTx 相同，但在独立语句中执行（供高频调用点节流使用）。
func (d *DB) AddCondition(ctx context.Context, uid int64, dPrimary, dSecondary float64) error {
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO player_condition(uid, primary_value, secondary_value, updated_ts)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(uid) DO UPDATE SET
			primary_value   = MIN(1.0, MAX(0.0, COALESCE(primary_value,0) + ?)),
			secondary_value = MIN(1.0, MAX(0.0, COALESCE(secondary_value,0) + ?)),
			updated_ts      = excluded.updated_ts`,
		uid, clamp01(DefaultPlayerConditionPrimary+dPrimary), clamp01(DefaultPlayerConditionSecondary+dSecondary), float64(time.Now().Unix()),
		dPrimary, dSecondary)
	return err
}

// energyRegenRate 按食物条给出每秒体力恢复量（分段线性，命中实测目标）：
//
//	食物条 1.00 → 0.1000/秒（10 秒回满）
//	食物条 0.50 → 0.0167/秒（60 秒回满）
//	食物条 0.25 → 0.0111/秒（90 秒回满）
func energyRegenRate(food float64) float64 {
	switch {
	case food >= 1:
		return 0.1
	case food >= 0.5:
		return 0.0167 + (0.1-0.0167)*(food-0.5)/0.5
	case food >= 0.25:
		return 0.0111 + (0.0167-0.0111)*(food-0.25)/0.25
	default:
		return 0.0111 * food / 0.25
	}
}

// RegenEnergyFromFood 按食物条（secondary）回复体力（primary），同时按恢复量消耗食物条。
// 逐秒积分：每秒用“当时的食物条”算恢复速率，所以食物条掉下去后恢复也会随之变慢，
// 而不是一直按满食物条的速率补。
// 按“距上次更新经过的秒数”累计，调用方在任何请求前调用即可；单次最多补 maxRegenSeconds 秒。
func (d *DB) RegenEnergyFromFood(ctx context.Context, uid int64, fullRate float64) error {
	const maxRegenSeconds = 300.0
	// energyRegenFoodCost 是恢复 1 点体力消耗的食物条数量。
	const energyRegenFoodCost = 0.5
	var p, s, ts float64
	err := d.SQL.QueryRowContext(ctx,
		`SELECT COALESCE(primary_value,0), COALESCE(secondary_value,0), COALESCE(updated_ts,0)
		 FROM player_condition WHERE uid=?`,
		uid).Scan(&p, &s, &ts)
	if err != nil {
		return err
	}
	if s <= 0 || p >= 1 {
		return nil
	}
	now := float64(time.Now().Unix())
	elapsed := now - ts
	if elapsed < 1 {
		return nil
	}
	if elapsed > maxRegenSeconds {
		elapsed = maxRegenSeconds
	}
	scale := 1.0
	if fullRate > 0 {
		scale = fullRate / 0.1
	}
	for i := 0; i < int(elapsed); i++ {
		if s <= 0 || p >= 1 {
			break
		}
		rate := energyRegenRate(s) * scale
		if rate <= 0 {
			break
		}
		gain := rate
		if p+gain > 1 {
			gain = 1 - p
		}
		p += gain
		s -= gain * energyRegenFoodCost
		if s < 0 {
			s = 0
		}
	}
	_, err = d.SQL.ExecContext(ctx,
		`UPDATE player_condition SET primary_value=?, secondary_value=?, updated_ts=? WHERE uid=?`,
		p, s, now, uid)
	return err
}
