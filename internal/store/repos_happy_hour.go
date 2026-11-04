package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const HappyHourMillis = int64(60 * 60 * 1000)

var happyHourZone = time.FixedZone("Asia/Shanghai", 8*60*60)

type HappyHourStatus struct {
	Enabled          bool    `json:"enabled"`
	Started          bool    `json:"started"`
	Active           bool    `json:"active"`
	RemainingSeconds int64   `json:"remaining_seconds"`
	XPBonus          float64 `json:"xp_bonus"`
	BiteBonus        float64 `json:"bite_bonus"`
	ProficiencyBonus float64 `json:"proficiency_bonus"`
}

func happyHourDay(now time.Time) string { return now.In(happyHourZone).Format("2006-01-02") }

func happyHourStatus(enabled, started, active bool, used int64) HappyHourStatus {
	remaining := max(int64(0), HappyHourMillis-used)
	r := HappyHourStatus{Enabled: enabled, Started: started, Active: enabled && active && remaining > 0,
		RemainingSeconds: (remaining + 999) / 1000}
	if r.Active {
		r.XPBonus, r.BiteBonus, r.ProficiencyBonus = .5, .1, .25
	}
	return r
}

// AdvanceHappyHour 只累计同一在线会话的有效钓鱼时间，离线和进程重启不补扣。
func (d *DB) AdvanceHappyHour(ctx context.Context, uid int64, owner string, now time.Time, fishing, takeover bool, maxGap time.Duration) (HappyHourStatus, error) {
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return HappyHourStatus{}, err
	}
	defer tx.Rollback()
	// 先取得写锁，多个连接不能同时消费同一账户的时长。
	if _, err := tx.ExecContext(ctx, `UPDATE happy_hour_state SET active=active WHERE uid=?`, uid); err != nil {
		return HappyHourStatus{}, err
	}
	enabled := true
	var flag string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM tunables WHERE key='happy_hour_enabled'`).Scan(&flag); err == nil {
		enabled = flag == "1"
	} else if !errors.Is(err, sql.ErrNoRows) {
		return HappyHourStatus{}, err
	}
	var day, previousOwner string
	var used, last int64
	var previousActive bool
	err = tx.QueryRowContext(ctx, `SELECT local_day,used_millis,last_tick,session_key,active FROM happy_hour_state WHERE uid=?`, uid).
		Scan(&day, &used, &last, &previousOwner, &previousActive)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return HappyHourStatus{}, err
	}
	if exists && previousOwner != owner && (!takeover || previousActive && now.UnixMilli()-last < max(30000, maxGap.Milliseconds())) {
		return happyHourStatus(enabled, true, false, used), nil
	}
	millis, currentDay := now.UnixMilli(), happyHourDay(now)
	if !exists || day != currentDay {
		used = 0
		if exists && previousOwner == owner && previousActive {
			midnight := time.Date(now.In(happyHourZone).Year(), now.In(happyHourZone).Month(), now.In(happyHourZone).Day(), 0, 0, 0, 0, happyHourZone).UnixMilli()
			last = max(last, midnight)
		} else {
			last = millis
		}
	}
	if exists && previousOwner == owner && previousActive {
		elapsed := max(int64(0), millis-last)
		if maxGap > 0 {
			elapsed = min(elapsed, maxGap.Milliseconds())
		}
		used = min(HappyHourMillis, used+elapsed)
	}
	active := fishing && enabled && used < HappyHourMillis
	if !exists && !active {
		return happyHourStatus(enabled, false, false, 0), nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO happy_hour_state(uid,local_day,used_millis,last_tick,session_key,active)
		VALUES(?,?,?,?,?,?) ON CONFLICT(uid) DO UPDATE SET local_day=excluded.local_day,
		used_millis=excluded.used_millis,last_tick=excluded.last_tick,session_key=excluded.session_key,active=excluded.active`,
		uid, currentDay, used, millis, owner, active); err != nil {
		return HappyHourStatus{}, err
	}
	if err := tx.Commit(); err != nil {
		return HappyHourStatus{}, err
	}
	return happyHourStatus(enabled, true, active, used), nil
}

func (d *DB) GetHappyHourStatus(ctx context.Context, uid int64, now time.Time) (HappyHourStatus, error) {
	enabled := true
	if flag, found, err := d.TunableGet(ctx, "happy_hour_enabled"); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return HappyHourStatus{}, err
	} else if found {
		enabled = flag == "1"
	}
	var day string
	var used int64
	var last int64
	var active bool
	err := d.SQL.QueryRowContext(ctx, `SELECT local_day,used_millis,active,last_tick FROM happy_hour_state WHERE uid=?`, uid).Scan(&day, &used, &active, &last)
	if errors.Is(err, sql.ErrNoRows) || err == nil && day != happyHourDay(now) {
		return happyHourStatus(enabled, false, false, 0), nil
	}
	if err != nil {
		return HappyHourStatus{}, err
	}
	return happyHourStatus(enabled, true, active && now.UnixMilli()-last <= 30000, used), nil
}
