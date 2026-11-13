package gameserver

// 动作型体力消耗的事务内扣减（当前接入挖饵 9/3 的两条路径）。
//
// 为什么不复用 spendEnergy：spendEnergy 走独立的读→推进→写回，不带事务，
// 与"给了饵/没扣体力"这类账务必须同生共死；挖饵要落在调用方已有的经济事务里。
//
// 调用约定：conditionParams 必须在开经济事务之前取好并传入。tunableF 首次装载
// 要查 tunables 表（2 秒超时），在持写锁的事务内首次调用会超时并静默回退默认值。

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"rf4server/internal/store"
)

// tunableBaitDigEnergyCost 单次挖饵（铲子/长柄勺）消耗的体力，
// 刻度与 308.f0 相同（整条 = 1.0）；置 0 即关闭挖饵体力消耗。
const tunableBaitDigEnergyCost = "bait_dig_energy_cost"

// defBaitDigEnergyCost 是标定值，不是官服常量：约"连挖 5~6 次见底"。
const defBaitDigEnergyCost = 0.15

// errEnergyNotEnough 体力不足以支撑本次动作。
//
// 门槛与消耗同源：剩余体力必须够一次挖饵的消耗量才允许挖，不另设绝对门槛常量。
// 精力上限是 0.85 且可被装备加成抬高，写死绝对门槛（例如 0.75）会随上限失真，
// 甚至让满状态玩家也挖不了。
var errEnergyNotEnough = errors.New("精力不足")

// baitDigEnergyCost 读取挖饵体力消耗 tunable，钳到 [0,1]。
func (s *Server) baitDigEnergyCost() float64 {
	if s == nil {
		return defBaitDigEnergyCost
	}
	return clampFloat(s.tunableF(tunableBaitDigEnergyCost, defBaitDigEnergyCost), 0, 1)
}

// spendEnergyTx 在调用方事务内推进状态钟并扣减精力。
//
// 返回实际消耗量；精力不足时返回 errEnergyNotEnough 且不写库，
// 由调用方回滚整个事务，避免出现"给了产出没扣精力"或反之。
// 总开关关闭、或 amount <= 0 时是纯 no-op（返回 0, nil）。
func spendEnergyTx(ctx context.Context, tx *sql.Tx, uid int64, p conditionParams, env conditionEnv, amount float64) (float64, error) {
	if tx == nil || amount <= 0 || !p.Enabled {
		return 0, nil
	}
	cur, err := store.EnsurePlayerConditionTx(ctx, tx, uid)
	if err != nil {
		return 0, err
	}
	now := float64(time.Now().Unix())
	// 与 spendEnergy 同口径：先按在线钟推进，再扣减，保证门槛判定用的是当前
	// 有效值而不是上一次落库的旧值。env 由调用方在开事务前取好（事务内不查库）。
	next := advancePlayerCondition(*cur, now, p, env)
	if next.Primary < amount {
		return 0, errEnergyNotEnough
	}
	next.Primary = clampFloat(next.Primary-amount, 0, p.EnergyMax)
	next.UpdatedTS = now
	if err := store.UpsertPlayerConditionTx(ctx, tx, next); err != nil {
		return 0, err
	}
	return amount, nil
}
