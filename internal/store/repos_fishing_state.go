package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// FishingFish 是 RolledFish 的可持久化快照。恢复 pending 时必须复用原 GUID、
// 重量与曲线输入，禁止重启后重新 roll。
type FishingFish struct {
	GearGUID       string  `json:"gear_guid,omitempty"`
	HookGUID       string  `json:"hook_guid,omitempty"`
	HappyHour      bool    `json:"happy_hour,omitempty"`
	HappyHourKnown bool    `json:"happy_hour_known,omitempty"`
	BaitGUIDs      string  `json:"bait_guids,omitempty"`
	BaitsKnown     bool    `json:"baits_known,omitempty"`
	BiteQuality    string  `json:"bite_quality,omitempty"`
	HookHoldP      float64 `json:"hook_hold_p,omitempty"`
	LuckProtected  bool    `json:"luck_protected,omitempty"`
	HookConfirmed  bool    `json:"hook_confirmed,omitempty"`
	HuntStage      int     `json:"hunt_stage,omitempty"`
	HuntStageKnown bool    `json:"hunt_stage_known,omitempty"`
	FishGUID       string  `json:"fish_guid"`
	Species        string  `json:"species"`
	Grade          int64   `json:"grade"`
	WeightG        int64   `json:"weight_g"`
	LengthM        float64 `json:"length_m"`
	XP             int64   `json:"xp"`
	Silver         int64   `json:"silver"`
	LocLevel       string  `json:"loc_level"`
	NamedID        string  `json:"named_id"`
	Name           string  `json:"name"`
	// 咬钩快照：天然饵在 14/11 被消耗删除，重启后也不能再按 GUID 反查。
	QualBaitID       string  `json:"qual_bait_id,omitempty"`
	QualBaitCat      int64   `json:"qual_bait_cat,omitempty"`
	QualBaitClass    int64   `json:"qual_bait_class,omitempty"`
	QualBaitFlavor   int64   `json:"qual_bait_flavor,omitempty"`
	QualBaitLureType int64   `json:"qual_bait_lure_type,omitempty"`
	QualBaitLenCM    float64 `json:"qual_bait_len_cm,omitempty"`
	QualBaitSizeMM   float64 `json:"qual_bait_size_mm,omitempty"`
	CastM            float64 `json:"cast_m,omitempty"`
	IsNight          bool    `json:"is_night,omitempty"`
	DepthCM          int64   `json:"depth_cm,omitempty"`
	OnBoat           bool    `json:"on_boat,omitempty"`
	Drift            bool    `json:"drift,omitempty"`
}

// FishingState 是 FishState 的可持久化子集。Keepnet 是 DB 权威缓存，
// mutex/timer 是进程内对象，均不在这里序列化。
type FishingState struct {
	UID            int64
	RodGUID        string // 稳定的竿键，GearGUID清空后仍覆盖原竿快照
	GearGUID       string
	RigType        int64
	Loc            string
	Spot           uint64
	DepthCM        int64
	CastM          float64
	CastActive     bool
	CastTS         time.Time
	FallTS         time.Time
	FallSeen       bool
	RigSettled     bool
	TickSeen       bool
	TickSeq        uint64
	LastAnim       uint32
	PushSent       bool
	Hooked         bool
	Resolved       bool
	Pending        *FishingFish
	Bait           string
	LastSkill      int64
	Armed          *FishingFish
	BiteSpeed      float64
	TrophyLuck     float64
	FightTolerance float64
	BiteAt         time.Time
}

const fishingStateCols = `
		uid, COALESCE(gear_guid,''), COALESCE(rig_type,0), COALESCE(loc,''),
		COALESCE(spot,0), COALESCE(depth_cm,0), COALESCE(cast_m,0), COALESCE(cast_active,0),
		COALESCE(cast_ts_ns,0), COALESCE(fall_ts_ns,0), COALESCE(fall_seen,0),
		COALESCE(rig_settled,0), COALESCE(tick_seen,0), COALESCE(tick_seq,0),
		COALESCE(last_anim,0),
		COALESCE(push_sent,0), COALESCE(hooked,0), COALESCE(resolved,0),
		pending_json, COALESCE(bait,''), COALESCE(last_skill,0), armed_json,
		COALESCE(bite_speed,1.0), COALESCE(trophy_luck,0.0),
		COALESCE(fight_tolerance,0.0), COALESCE(bite_at_ns,0), rod_guid`

// LoadFishingState 读取玩家钓鱼快照；不存在时返回 (nil, nil)。
func (d *DB) LoadFishingState(ctx context.Context, uid int64) (*FishingState, error) {
	row := d.SQL.QueryRowContext(ctx,
		`SELECT `+fishingStateCols+` FROM fishing_state WHERE uid=? ORDER BY updated_ts DESC LIMIT 1`, uid)
	return scanFishingState(row)
}

// LoadFishingStates returns all rods, most recently updated first.
func (d *DB) LoadFishingStates(ctx context.Context, uid int64) ([]*FishingState, error) {
	rows, err := d.SQL.QueryContext(ctx, `SELECT `+fishingStateCols+` FROM fishing_state WHERE uid=? ORDER BY updated_ts DESC`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var states []*FishingState
	for rows.Next() {
		st, err := scanFishingState(rows)
		if err != nil {
			return nil, err
		}
		states = append(states, st)
	}
	return states, rows.Err()
}

func scanFishingState(row interface{ Scan(...any) error }) (*FishingState, error) {
	var (
		st       FishingState
		spot     int64
		castNS   int64
		fallNS   int64
		biteNS   int64
		pending  sql.NullString
		armed    sql.NullString
		cast     int64
		fallSeen int64
		settled  int64
		tickSeen int64
		tickSeq  int64
		pushSent int64
		hooked   int64
		resolved int64
	)
	err := row.Scan(
		&st.UID, &st.GearGUID, &st.RigType, &st.Loc, &spot, &st.DepthCM, &st.CastM, &cast,
		&castNS, &fallNS, &fallSeen, &settled, &tickSeen, &tickSeq, &st.LastAnim,
		&pushSent, &hooked, &resolved, &pending, &st.Bait, &st.LastSkill, &armed,
		&st.BiteSpeed, &st.TrophyLuck, &st.FightTolerance, &biteNS, &st.RodGUID,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	st.Spot = uint64(spot)
	st.CastActive = cast != 0
	st.CastTS = fishingTimeFromNS(castNS)
	st.FallTS = fishingTimeFromNS(fallNS)
	st.FallSeen = fallSeen != 0
	st.RigSettled = settled != 0
	st.TickSeen = tickSeen != 0
	st.TickSeq = uint64(tickSeq)
	st.PushSent = pushSent != 0
	st.Hooked = hooked != 0
	st.Resolved = resolved != 0
	st.BiteAt = fishingTimeFromNS(biteNS)
	if pending.Valid && pending.String != "" {
		if st.Pending, err = decodeFishingFish(pending.String); err != nil {
			return nil, fmt.Errorf("解析 fishing_state.pending_json: %w", err)
		}
	}
	if armed.Valid && armed.String != "" {
		if st.Armed, err = decodeFishingFish(armed.String); err != nil {
			return nil, fmt.Errorf("解析 fishing_state.armed_json: %w", err)
		}
	}
	return &st, nil
}

// SaveFishingState 原子覆盖该玩家该竿的快照。
func (d *DB) SaveFishingState(ctx context.Context, st *FishingState) error {
	if st == nil || st.UID == 0 {
		return fmt.Errorf("fishing_state: uid 不能为空")
	}
	pending, err := encodeFishingFish(st.Pending)
	if err != nil {
		return fmt.Errorf("编码 pending_json: %w", err)
	}
	armed, err := encodeFishingFish(st.Armed)
	if err != nil {
		return fmt.Errorf("编码 armed_json: %w", err)
	}
	rodGUID := st.RodGUID
	if rodGUID == "" {
		rodGUID = st.GearGUID
	}
	_, err = d.SQL.ExecContext(ctx, `
		INSERT INTO fishing_state(
			uid, gear_guid, rig_type, loc, spot, depth_cm, cast_m, cast_active,
			cast_ts_ns, fall_ts_ns, fall_seen, rig_settled, tick_seen,
			tick_seq, last_anim, push_sent, hooked, resolved, pending_json, bait,
			last_skill, armed_json, bite_speed, trophy_luck, fight_tolerance,
			bite_at_ns, updated_ts, rod_guid
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(uid,rod_guid) DO UPDATE SET
			gear_guid=excluded.gear_guid,
			rig_type=excluded.rig_type,
			loc=excluded.loc,
			spot=excluded.spot,
			depth_cm=excluded.depth_cm,
			cast_m=excluded.cast_m,
			cast_active=excluded.cast_active,
			cast_ts_ns=excluded.cast_ts_ns,
			fall_ts_ns=excluded.fall_ts_ns,
			fall_seen=excluded.fall_seen,
			rig_settled=excluded.rig_settled,
			tick_seen=excluded.tick_seen,
			tick_seq=excluded.tick_seq,
			last_anim=excluded.last_anim,
			push_sent=excluded.push_sent,
			hooked=excluded.hooked,
			resolved=excluded.resolved,
			pending_json=excluded.pending_json,
			bait=excluded.bait,
			last_skill=excluded.last_skill,
			armed_json=excluded.armed_json,
			bite_speed=excluded.bite_speed,
			trophy_luck=excluded.trophy_luck,
			fight_tolerance=excluded.fight_tolerance,
			bite_at_ns=excluded.bite_at_ns,
			updated_ts=excluded.updated_ts`,
		st.UID, st.GearGUID, st.RigType, st.Loc, int64(st.Spot), st.DepthCM, st.CastM,
		fishingBoolInt(st.CastActive), fishingTimeNS(st.CastTS),
		fishingTimeNS(st.FallTS), fishingBoolInt(st.FallSeen),
		fishingBoolInt(st.RigSettled), fishingBoolInt(st.TickSeen), int64(st.TickSeq), st.LastAnim,
		fishingBoolInt(st.PushSent), fishingBoolInt(st.Hooked),
		fishingBoolInt(st.Resolved), pending, st.Bait, st.LastSkill, armed,
		st.BiteSpeed, st.TrophyLuck, st.FightTolerance, fishingTimeNS(st.BiteAt),
		float64(time.Now().UnixNano())/1e9,
		rodGUID,
	)
	return err
}

func encodeFishingFish(f *FishingFish) (any, error) {
	if f == nil {
		return nil, nil
	}
	b, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func decodeFishingFish(raw string) (*FishingFish, error) {
	var f FishingFish
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		return nil, err
	}
	return &f, nil
}

func fishingBoolInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

func fishingTimeNS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func fishingTimeFromNS(ns int64) time.Time {
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}
