package gameserver

// 玩家状态条（308 f0/f1/f3/f4）服务端权威状态机。
//
// 客户端四条状态条为纯显示组件，只做约 10 秒的平滑过渡，
// 不做任何衰减/消耗计算（协议基线 4.0.25034 实测）。
// 机制依据与证据等级见 specs/changes/player-condition-core/spec.md；
// 全部速率与消耗量是标定值（tunable），不是官服常量。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// tunables 键名。改值走 tunables 表，30 秒缓存生效，无需重启。
const (
	tunableConditionEnabled       = "player_condition_enabled"
	tunableHungerDecayPerHour     = "condition_hunger_decay_per_hour"
	tunableEnergyRegenPerHour     = "condition_energy_regen_per_hour"
	tunableEnergyMax              = "condition_energy_max"
	tunableEnergyRegenCap         = "condition_energy_regen_cap"
	tunableEnergyRegenFishingMult = "condition_energy_regen_fishing_mult"
	tunableHungerRegenFloor       = "condition_hunger_regen_floor"
	tunableCastEnergyCost         = "condition_cast_energy_cost"
	tunablePullEnergyCost         = "condition_pull_energy_cost"
	tunableMaxBackfillHours       = "condition_max_backfill_hours"
	tunableFoodHungerRestore      = "condition_food_hunger_restore"
	tunableCoffeeEnergyRestore    = "condition_coffee_energy_restore"
	tunableCocoaHungerRestore     = "condition_cocoa_hunger_restore"
	tunableCocoaEnergyRestore     = "condition_cocoa_energy_restore"
	tunableTeaComfortRestore      = "condition_tea_comfort_restore"
	tunableComfortRainPenalty     = "condition_comfort_rain_penalty"
	tunableComfortColdPenalty     = "condition_comfort_cold_penalty"
	tunableComfortColdTempC       = "condition_comfort_cold_temp_c"
	tunableComfortCampfireBoost   = "condition_comfort_campfire_boost"
	tunableComfortSunBoost        = "condition_comfort_sun_boost"
	tunableComfortDriftPerHour    = "condition_comfort_drift_per_hour"
	tunableSunEnergyRegenMult     = "condition_sun_energy_regen_mult"
	tunableGearEnergyRegenCap     = "condition_gear_energy_regen_cap"
	tunableGearEnergyMaxCap       = "condition_gear_energy_max_cap"
	tunableEnergyRegenFoodCost    = "condition_energy_regen_food_cost"

	// 健康（f4）动态：饥寒压力掉血、压力消失后自愈。
	tunableHealthDecayPerHour     = "condition_health_decay_per_hour"
	tunableHealthRegenPerHour     = "condition_health_regen_per_hour"
	tunableHealthHungerThreshold  = "condition_health_hunger_threshold"
	tunableHealthComfortThreshold = "condition_health_comfort_threshold"
	tunableMedicineHealthRestore  = "condition_medicine_health_restore"
)

// 缺省标定值：
// 饱食衰减 0.24/h 来自抓包（~0.0019/30s）；精力自然恢复封顶 0.85 来自抓包；
// 其余为占位标定，各值就近写明来源与取舍。
const (
	defHungerDecayPerHour = 0.24
	// 满饱食度时约 30 秒回满一管（0.85 / 100 * 3600 ≈ 30.6s）：
	// 挖掘/搏鱼的消耗靠“吃饱→快速回精力→再消耗”的循环，不再等几十分钟。
	defEnergyRegenPerHour = 100.0
	// 垂钓中自然恢复乘数：0 表示竿在水中时恢复完全停摆。
	// 官服一小时会话的精力是净下降的（0.85 → 0.83）；而本仓单次抛投/拉拽
	// 只扣 0.01/0.005，若垂钓时仍按 100/h 恢复，0.36 秒就把一次抛投的消耗
	// 抹平，精力条永远画满、机制形同不存在。停竿后照常快速回精力，
	// 保留"打完吃顿饭半分钟再战"的节奏（见 specs/changes/energy-regen-fast）。
	defEnergyRegenFishingMult = 0.0
	// 精力硬上限 1.0：客户端状态条按 1.0 归一化画满（0.85 只画到 85%）。
	// 官服抓包 f0 静止恒 0.85 是"自然恢复封顶"，不是上限——动作掉下去后
	// 15 秒内只回升到 0.85 就停住。最后那 15% 要靠咖啡/可可这类消耗品补满，
	// 把硬上限设成 0.85 会让所有回精力消耗品恒为零效果。
	defEnergyMax        = 1.0
	defEnergyRegenCap   = 0.85
	defHungerRegenFloor = 0.25
	defCastEnergyCost   = 0.01
	defPullEnergyCost   = 0.005
	defMaxBackfillHours = 12

	// 消耗品恢复量（饮用/食用一次的即时恢复，全部为标定值）。
	defFoodHungerRestore   = 0.25
	defCoffeeEnergyRestore = 0.15
	defCocoaHungerRestore  = 0.35
	defCocoaEnergyRestore  = 0.05
	defTeaComfortRestore   = 0.30

	// 舒适度环境均衡点修正与漂移速率（标定值，无官服样本）。
	defComfortRainPenalty   = 0.35
	defComfortColdPenalty   = 0.35
	defComfortColdTempC     = 5
	defComfortCampfireBoost = 0.5
	defComfortSunBoost      = 0.1
	defComfortDriftPerHour  = 0.5
	defSunEnergyRegenMult   = 1.25

	// 装备被动加成封顶。
	defGearEnergyRegenCap = 0.5
	defGearEnergyMaxCap   = 0.15
	// 恢复精力要消耗饱食度：每恢复 1 点精力扣这么多饱食度（0 关闭）。
	defEnergyRegenFoodCost = 0.5

	// 健康（f4）动态。全部为自设计标定值：官服没有任何"健康下降/恢复"
	// 样本，取值只保证"饿到快见底、冻到很不舒服才开始掉，且掉得比恢复慢"。
	defHealthDecayPerHour     = 0.10
	defHealthRegenPerHour     = 0.05
	defHealthHungerThreshold  = 0.10
	defHealthComfortThreshold = 0.30
	defMedicineHealthRestore  = 0.50
)

// conditionWriteBackDelta：读路径推进后，变化量低于该阈值不写回，
// 避免每 5 秒保活都对全量在线玩家做一次无效写库。0.0005 远低于
// float32 在 0.85 量级的显示噪声。
const conditionWriteBackDelta = 0.0005

type conditionParams struct {
	Enabled                bool
	HungerDecayPerHour     float64
	EnergyRegenPerHour     float64
	EnergyMax              float64
	EnergyRegenCap         float64
	EnergyRegenFishingMult float64
	HungerRegenFloor       float64
	CastEnergyCost         float64
	PullEnergyCost         float64
	MaxBackfillHours       float64
	FoodHungerRestore      float64
	CoffeeEnergyRestore    float64
	CocoaHungerRestore     float64
	CocoaEnergyRestore     float64
	TeaComfortRestore      float64
	ComfortRainPenalty     float64
	ComfortColdPenalty     float64
	ComfortColdTempC       float64
	ComfortCampfireBoost   float64
	ComfortSunBoost        float64
	ComfortDriftPerHour    float64
	SunEnergyRegenMult     float64
	GearEnergyRegenCap     float64
	GearEnergyMaxCap       float64
	EnergyRegenFoodCost    float64

	// 健康（f4）动态。
	HealthDecayPerHour     float64
	HealthRegenPerHour     float64
	HealthHungerThreshold  float64
	HealthComfortThreshold float64
	MedicineHealthRestore  float64
}

func (s *Server) conditionParams() conditionParams {
	return conditionParams{
		Enabled:                s.tunableF(tunableConditionEnabled, 1) >= 0.5,
		HungerDecayPerHour:     s.tunableF(tunableHungerDecayPerHour, defHungerDecayPerHour),
		EnergyRegenPerHour:     s.tunableF(tunableEnergyRegenPerHour, defEnergyRegenPerHour),
		EnergyMax:              s.tunableF(tunableEnergyMax, defEnergyMax),
		EnergyRegenCap:         s.tunableF(tunableEnergyRegenCap, defEnergyRegenCap),
		EnergyRegenFishingMult: s.tunableF(tunableEnergyRegenFishingMult, defEnergyRegenFishingMult),
		HungerRegenFloor:       s.tunableF(tunableHungerRegenFloor, defHungerRegenFloor),
		CastEnergyCost:         s.tunableF(tunableCastEnergyCost, defCastEnergyCost),
		PullEnergyCost:         s.tunableF(tunablePullEnergyCost, defPullEnergyCost),
		MaxBackfillHours:       s.tunableF(tunableMaxBackfillHours, defMaxBackfillHours),
		FoodHungerRestore:      s.tunableF(tunableFoodHungerRestore, defFoodHungerRestore),
		CoffeeEnergyRestore:    s.tunableF(tunableCoffeeEnergyRestore, defCoffeeEnergyRestore),
		CocoaHungerRestore:     s.tunableF(tunableCocoaHungerRestore, defCocoaHungerRestore),
		CocoaEnergyRestore:     s.tunableF(tunableCocoaEnergyRestore, defCocoaEnergyRestore),
		TeaComfortRestore:      s.tunableF(tunableTeaComfortRestore, defTeaComfortRestore),
		ComfortRainPenalty:     s.tunableF(tunableComfortRainPenalty, defComfortRainPenalty),
		ComfortColdPenalty:     s.tunableF(tunableComfortColdPenalty, defComfortColdPenalty),
		ComfortColdTempC:       s.tunableF(tunableComfortColdTempC, defComfortColdTempC),
		ComfortCampfireBoost:   s.tunableF(tunableComfortCampfireBoost, defComfortCampfireBoost),
		ComfortSunBoost:        s.tunableF(tunableComfortSunBoost, defComfortSunBoost),
		ComfortDriftPerHour:    s.tunableF(tunableComfortDriftPerHour, defComfortDriftPerHour),
		SunEnergyRegenMult:     s.tunableF(tunableSunEnergyRegenMult, defSunEnergyRegenMult),
		GearEnergyRegenCap:     s.tunableF(tunableGearEnergyRegenCap, defGearEnergyRegenCap),
		GearEnergyMaxCap:       s.tunableF(tunableGearEnergyMaxCap, defGearEnergyMaxCap),
		EnergyRegenFoodCost:    s.tunableF(tunableEnergyRegenFoodCost, defEnergyRegenFoodCost),

		HealthDecayPerHour:     s.tunableF(tunableHealthDecayPerHour, defHealthDecayPerHour),
		HealthRegenPerHour:     s.tunableF(tunableHealthRegenPerHour, defHealthRegenPerHour),
		HealthHungerThreshold:  s.tunableF(tunableHealthHungerThreshold, defHealthHungerThreshold),
		HealthComfortThreshold: s.tunableF(tunableHealthComfortThreshold, defHealthComfortThreshold),
		MedicineHealthRestore:  s.tunableF(tunableMedicineHealthRestore, defMedicineHealthRestore),
	}
}

// conditionParamsForUID 返回叠加当前已装配竿/轮被动加成后的有效参数。
// 所有会推进或消耗精力状态的口径都必须使用该结果，避免基础上限 0.85
// 把装备提供的上限加成截断。
func (s *Server) conditionParamsForUID(ctx context.Context, uid int64) conditionParams {
	if s == nil {
		return conditionParams{}
	}
	p := s.conditionParams()
	if s.DB == nil || !p.Enabled {
		return p
	}
	regen, maxAdd := s.rigEnergyBonuses(ctx, uid, s.heldRodGUID(uid))
	if regen != 0 {
		p.EnergyRegenPerHour *= 1 + regen
	}
	if maxAdd != 0 {
		p.EnergyMax = clampFloat(p.EnergyMax+maxAdd, 0, 1)
	}
	return p
}

// conditionEnv 环境快照：舒适度均衡点与晒阳精力恢复的输入。
// 由会话侧解析天气/昼夜/篝火，推进函数只消费布尔结果；
// 零值表示"环境未知"，均衡点回到 1.0（与无环境联动时行为一致）。
type conditionEnv struct {
	Rain     bool // 降雨或雷暴
	Cold     bool // 气温低于阈值
	Sun      bool // 白昼且无雨（晒太阳）
	Campfire bool // 有未熄灭的篝火
	Fishing  bool // 有竿已抛投、入水且未结算（垂钓中）
}

// advancePlayerCondition 按时间差推进精力、饱食度与舒适度；纯函数，单测用手算样例覆盖。
//
// 只用于在线钟口径：登录/重连时 resumePlayerConditionClock 冻结离线跨度，
// 因此这里收到的时间差都来自在线会话；MaxBackfillHours 只是会话钟停滞的安全阀。
//   - 饱食度随时间衰减，到 0 为止；离线停扣（维护者决策，见 spec）。
//   - 精力按"基础速率 × 饱食因子"恢复（官方手册：恢复速度取决于饱食度和舒适度），
//     封顶 energy_max；晒太阳时乘以加成倍率；离线同样停恢复。
//   - 舒适度向环境均衡点漂移：1 − 降雨惩罚 − 寒冷惩罚 + 篝火加成 + 晒阳加成，
//     速率 ComfortDriftPerHour。
//   - 健康（f4，HUD 显示 1-f4）按饥寒压力掉血：饱食度低于阈值、舒适度低于阈值
//     各算一个压力源，压力源越多掉得越快；两个压力源都消失后按自愈速率回升。
func advancePlayerCondition(cur store.PlayerCondition, now float64, p conditionParams, env conditionEnv) store.PlayerCondition {
	out := cur
	out.UpdatedTS = now
	if !p.Enabled || now <= cur.UpdatedTS {
		return out
	}
	elapsedH := (now - cur.UpdatedTS) / 3600
	if elapsedH > p.MaxBackfillHours {
		elapsedH = p.MaxBackfillHours
	}
	out.Secondary = clampFloat(cur.Secondary-p.HungerDecayPerHour*elapsedH, 0, 1)
	regen := p.EnergyRegenPerHour
	if env.Sun {
		regen *= p.SunEnergyRegenMult
	}
	// 垂钓中停摆：动作消耗只发生在竿在水里的时候，恢复若同时进行就把消耗
	// 抹平（0.01 的消耗在 100/h 下 0.36 秒补回）。乘数默认 0，停竿即恢复。
	if env.Fishing {
		regen *= p.EnergyRegenFishingMult
	}
	factor := p.HungerRegenFloor + (1-p.HungerRegenFloor)*out.Secondary
	prevEnergy := out.Primary
	// 自然恢复只能把精力推到 regenCap，推不到硬上限：官服抓包显示精力静止时
	// 稳定停在 0.85、动作掉下去约 15 秒回升到 0.85 就不再涨。硬上限 1.0 留给
	// 咖啡/可可这类消耗品，否则"喝咖啡回精力"在没有消耗的空闲期永远无效。
	//
	// 封的是"自然恢复能到多高"这个天花板，不是"当前值不得超过多少"：
	// 已经在封顶之上的值（消耗品补上去的）原样保留，不会被保活拍按回来。
	naturalCap := math.Min(p.EnergyMax, p.EnergyRegenCap)
	regenTo := clampFloat(cur.Primary+regen*factor*elapsedH, 0, naturalCap)
	out.Primary = clampFloat(math.Max(cur.Primary, regenTo), 0, p.EnergyMax)
	// 恢复精力要吃掉饱食度：按实际回复量折算，系数为 tunable（0 关闭）。
	// 放在饱食度衰减之后、健康判定之前，保证同一拍里 f1 与健康压力源自洽。
	if gained := out.Primary - prevEnergy; gained > 0 && p.EnergyRegenFoodCost > 0 {
		out.Secondary = clampFloat(out.Secondary-gained*p.EnergyRegenFoodCost, 0, 1)
	}
	equilibrium := 1.0
	if env.Rain {
		equilibrium -= p.ComfortRainPenalty
	}
	if env.Cold {
		equilibrium -= p.ComfortColdPenalty
	}
	if env.Campfire {
		equilibrium += p.ComfortCampfireBoost
	}
	if env.Sun {
		equilibrium += p.ComfortSunBoost
	}
	equilibrium = clampFloat(equilibrium, 0, 1)
	drift := p.ComfortDriftPerHour * elapsedH
	out.Comfort = clampFloat(out.Comfort+clampFloat(equilibrium-out.Comfort, -drift, drift), 0, 1)

	// 健康：饥寒各算一个压力源，压力期线性掉血、无压力时线性自愈。
	// 门槛用推进后的饱食度/舒适度判定，与同一拍下发的 f1/f3 自洽。
	stress := 0
	if out.Secondary < p.HealthHungerThreshold {
		stress++
	}
	if out.Comfort < p.HealthComfortThreshold {
		stress++
	}
	delta := -p.HealthRegenPerHour * elapsedH
	if stress > 0 {
		delta = p.HealthDecayPerHour * float64(stress) * elapsedH
	}
	out.LuckPenalty = clampFloat(cur.LuckPenalty+delta, 0, 1)
	return out
}

// resumePlayerConditionClock 登录/重连时冻结状态钟：把 updated_ts 推到当前时刻
// 但不做任何衰减/恢复，从而吞掉离线跨度——离线期间饱食度停扣、精力停恢复。
// （维护者决策，非官服验证口径；与窝料/饮酒在线钟同一范式。）
func (s *Server) resumePlayerConditionClock(ctx context.Context, uid int64) {
	if s == nil || s.DB == nil {
		return
	}
	if !s.conditionParams().Enabled {
		return
	}
	cur, err := s.DB.EnsurePlayerCondition(ctx, uid)
	if err != nil || cur == nil {
		return
	}
	now := float64(time.Now().Unix())
	if cur.UpdatedTS >= now {
		return
	}
	cur.UpdatedTS = now
	if err := s.DB.UpsertPlayerCondition(ctx, *cur); err != nil {
		s.log.Warn("状态钟冻结失败", "uid", uid, "err", err)
	}
}

// currentPlayerCondition 懒计算读取口：Ensure → 推进 → 变化超阈值才写回。
// 总开关关闭时回退协议默认常量，与改动前行为逐字段一致（不写库）。
func (s *Server) currentPlayerCondition(ctx context.Context, uid int64) store.PlayerCondition {
	return s.currentPlayerConditionP(ctx, uid, s.conditionParamsForUID(ctx, uid), s.conditionEnvForUID(uid))
}

// currentPlayerConditionEnv 带环境快照的读取口（2/2 保活专用）：
// 舒适度向环境均衡点漂移、晒阳精力加成在这里生效。
func (s *Server) currentPlayerConditionEnv(ctx context.Context, uid int64, env conditionEnv) store.PlayerCondition {
	return s.currentPlayerConditionP(ctx, uid, s.conditionParams(), env)
}

func (s *Server) currentPlayerConditionP(ctx context.Context, uid int64, p conditionParams, env conditionEnv) store.PlayerCondition {
	now := float64(time.Now().Unix())
	def := store.PlayerCondition{
		Primary:     store.DefaultPlayerConditionPrimary,
		Secondary:   store.DefaultPlayerConditionSecondary,
		Comfort:     store.DefaultPlayerConditionComfort,
		LuckPenalty: store.DefaultPlayerConditionLuckPenalty,
		UpdatedTS:   now,
	}
	if s == nil || s.DB == nil {
		return def
	}
	cur, err := s.DB.EnsurePlayerCondition(ctx, uid)
	if err != nil || cur == nil {
		return def
	}
	if !p.Enabled {
		return def
	}
	next := advancePlayerCondition(*cur, now, p, env)
	if math.Abs(next.Primary-cur.Primary) > conditionWriteBackDelta ||
		math.Abs(next.Secondary-cur.Secondary) > conditionWriteBackDelta ||
		math.Abs(next.Comfort-cur.Comfort) > conditionWriteBackDelta ||
		math.Abs(next.LuckPenalty-cur.LuckPenalty) > conditionWriteBackDelta {
		// 写回失败不阻断下发：本次返回推进值，下次读取再补写。
		if err := s.DB.UpsertPlayerCondition(ctx, next); err != nil {
			s.log.Warn("玩家状态写回失败", "uid", uid, "err", err)
		}
	}
	return next
}

// peekPlayerCondition 只读当前状态，不做在线钟推进。
// 307/档案响应里的 308 使用该口径：环境感知和在线推进只由 2/2 保活负责，
// 避免任意 307 以空环境消耗环境漂移区间。读取时仍按有效装备上限做显示钳制，
// 但不回写。
func (s *Server) peekPlayerCondition(ctx context.Context, uid int64) store.PlayerCondition {
	now := float64(time.Now().Unix())
	def := store.PlayerCondition{
		Primary:     store.DefaultPlayerConditionPrimary,
		Secondary:   store.DefaultPlayerConditionSecondary,
		Comfort:     store.DefaultPlayerConditionComfort,
		LuckPenalty: store.DefaultPlayerConditionLuckPenalty,
		UpdatedTS:   now,
	}
	if s == nil || s.DB == nil {
		return def
	}
	p := s.conditionParamsForUID(ctx, uid)
	if !p.Enabled {
		return def
	}
	cur, err := s.DB.EnsurePlayerCondition(ctx, uid)
	if err != nil || cur == nil {
		return def
	}
	out := *cur
	out.Primary = clampFloat(out.Primary, 0, p.EnergyMax)
	out.Secondary = clampFloat(out.Secondary, 0, 1)
	out.Comfort = clampFloat(out.Comfort, 0, 1)
	out.LuckPenalty = clampFloat(out.LuckPenalty, 0, 1)
	return out
}

// spendEnergy 立即扣减精力并写回。动作写入总是落库（不受读路径阈值约束）。
// 总开关关闭时不产生任何状态变化。
func (s *Server) spendEnergy(ctx context.Context, uid int64, amount float64) {
	if s == nil || s.DB == nil || amount <= 0 {
		return
	}
	p := s.conditionParamsForUID(ctx, uid)
	if !p.Enabled {
		return
	}
	c := s.currentPlayerConditionP(ctx, uid, p, s.conditionEnvForUID(uid))
	c.Primary = clampFloat(c.Primary-amount, 0, p.EnergyMax)
	c.UpdatedTS = float64(time.Now().Unix())
	if err := s.DB.UpsertPlayerCondition(ctx, c); err != nil {
		s.log.Warn("精力扣减写回失败", "uid", uid, "err", err)
	}
}

// 日出/日落取游戏时刻 6:00/20:00。世界钟 24 倍速（一游戏日 = 一现实小时），
// 昼夜随现实时间一小时轮转；漂移速率远慢于轮转，表现为轻微起伏。
func isDaylightGameMinute(minute uint32) bool {
	return minute >= 6*60 && minute < 20*60
}

// conditionEnvForSession 解析玩家当前环境（天气/昼夜/篝火）。
// 天气计划或位置未知时相应位为 false，均衡点回到 1.0。
func (s *Session) conditionEnvForSession(now time.Time, p conditionParams) conditionEnv {
	var env conditionEnv
	if s == nil || s.Srv == nil || s.Srv.DB == nil {
		return env
	}
	st := s.Srv.stateFor(s.Userid)
	st.mu.Lock()
	loc, spot := st.Loc, int64(st.Spot)
	st.mu.Unlock()
	if loc != "" {
		if w := s.weatherAt(loc, now); w.known {
			env.Rain = w.kind >= fishingWeatherRain
			env.Cold = w.airTemp < p.ComfortColdTempC
			env.Sun = !env.Rain && isDaylightGameMinute(gameMinuteOfDayAt(now))
		}
	} else {
		env.Sun = isDaylightGameMinute(gameMinuteOfDayAt(now))
	}
	if state, err := s.Srv.DB.GetCampfireState(s.pushCtx(), s.Userid); err == nil && state != nil &&
		state.ExpiresFT > int64(proto.WindowsFiletimeNow()) {
		// 篝火加成只对同一地点/钓点生效；玩家离开后不能继续享受。
		env.Campfire = loc != "" && state.Loc == loc && state.Spot == spot
	}
	env.Fishing = s.Srv.playerFishingActive(s.Userid)
	return env
}

// playerFishingActive 报告是否有竿处于"已抛投、入水、未结算"状态。
// 判据与在线垂钓识别同源（CastActive && InWater && !Resolved）；只读快照，
// 不推进鱼状态。逐竿加锁，锁内不调用任何会再加同一把锁的函数。
func (s *Server) playerFishingActive(uid int64) bool {
	if s == nil {
		return false
	}
	// 只读内存里的既有状态：不走 fishingStatesFor —— 那条路径会为从未钓过鱼的
	// 玩家懒建鱼状态并可能触发 LoadFishingStates 查库，读精力不该有这种副作用。
	s.fishMu.Lock()
	list := make([]*FishState, 0, len(s.fishStates[uid]))
	for _, st := range s.fishStates[uid] {
		list = append(list, st)
	}
	s.fishMu.Unlock()
	for _, st := range list {
		if st == nil {
			continue
		}
		st.mu.Lock()
		active := st.CastActive && st.InWater && !st.Resolved
		st.mu.Unlock()
		if active {
			return true
		}
	}
	return false
}

// conditionEnvForUID 动作路径用的环境快照：只带"是否垂钓中"。
// 天气/昼夜/篝火仅在 2/2 保活拍解析（见 conditionEnvForSession），
// 动作路径沿用改动前的空环境口径，避免扣精力/喝咖啡的行为随天气抖动。
func (s *Server) conditionEnvForUID(uid int64) conditionEnv {
	return conditionEnv{Fishing: s.playerFishingActive(uid)}
}

// rigEnergyBonuses 汇总已装配竿/轮的精力被动加成。数据源为 item_def.attrs_json
// 的 oiicogcccao（精力恢复加成）与 oodjilnjeen（精力上限加成），0..1 比例，
// 与客户端 kItemDesc_Bonus_Energy* tooltip 同键；当前静态库未携带这些值时恒为 0，
// 标定写入 attrs 后客户端显示与服务端效果同源生效。总和受 tunables 封顶。
func (s *Server) rigEnergyBonuses(ctx context.Context, uid int64, rodGUID string) (regen, maxAdd float64) {
	if s == nil || s.DB == nil || rodGUID == "" {
		return 0, 0
	}
	p := s.conditionParams()
	guids := []string{rodGUID}
	if ids := s.rigGearIDs(ctx, uid, rodGUID); ids.ReelGUID != "" {
		guids = append(guids, ids.ReelGUID)
	}
	for _, guid := range guids {
		row, err := s.DB.GetItemByGUID(ctx, guid)
		if err != nil || row == nil {
			continue
		}
		def, err := s.itemDefForRow(ctx, row)
		if err != nil || def == nil || def.AttrsJSON == "" {
			continue
		}
		var attrs map[string]any
		if json.Unmarshal([]byte(def.AttrsJSON), &attrs) != nil {
			continue
		}
		regen += jsonFloatAttr(attrs, "oiicogcccao")
		maxAdd += jsonFloatAttr(attrs, "oodjilnjeen")
	}
	return clampFloat(regen, 0, p.GearEnergyRegenCap), clampFloat(maxAdd, 0, p.GearEnergyMaxCap)
}

func jsonFloatAttr(attrs map[string]any, key string) float64 {
	v, ok := attrs[key]
	if !ok {
		return 0
	}
	if n, ok := v.(float64); ok {
		return n
	}
	return 0
}

// buildPose308Env 带环境与装备加成的 308 构建（2/2 保活专用）：
// 装备加成调整精力恢复速率与上限后按环境推进。
func (s *Server) buildPose308Env(ctx context.Context, uid int64, env conditionEnv) *proto.Value {
	p := s.conditionParamsForUID(ctx, uid)
	c := s.currentPlayerConditionP(ctx, uid, p, env)
	return wiregen.MustMakeObj("pnbphpdmppe", map[int]*proto.Value{
		0: proto.F32(float32(c.Primary)), 1: proto.F32(float32(c.Secondary)), 2: proto.F32(0.0),
		3: proto.F32(float32(c.Comfort)), 4: proto.F32(float32(c.LuckPenalty)),
		5: proto.F32(11.375), 6: proto.F32(0.0),
		7: proto.U64(uint64(proto.WindowsFiletimeNow())),
		8: proto.U32(0),
	})
}

func (s *Server) heldRodGUID(uid int64) string {
	st := s.stateFor(uid)
	st.mu.Lock()
	guid := st.GearGuid
	st.mu.Unlock()
	return guid
}

// consumableRestoreTarget 计算某个消耗品分类作用在 cur 上的新状态。
// 分类映射见 consumableRestoreForItem：咖啡→精力、茶→舒适度、
// 可可→饱食+少量精力、药品→健康（f4 下降）、其余食品→饱食度。
// 恢复量全部为 tunables 标定值。
//
// 判定与写入共用这一个函数：只有它真的让某条状态变化，这次使用才算有效，
// 避免扣掉物品而状态条没有任何可见变化。
func consumableRestoreTarget(cur store.PlayerCondition, kind string, p conditionParams) store.PlayerCondition {
	next := cur
	switch kind {
	case "coffee":
		next.Primary = clampFloat(next.Primary+p.CoffeeEnergyRestore, 0, p.EnergyMax)
	case "tea":
		next.Comfort = clampFloat(next.Comfort+p.TeaComfortRestore, 0, 1)
	case "cocoa":
		next.Secondary = clampFloat(next.Secondary+p.CocoaHungerRestore, 0, 1)
		next.Primary = clampFloat(next.Primary+p.CocoaEnergyRestore, 0, p.EnergyMax)
	case "medicine":
		// 健康是 f4 的反向刻度：治疗后 f4 下降（HUD 的 1-f4 回升）。
		next.LuckPenalty = clampFloat(next.LuckPenalty-p.MedicineHealthRestore, 0, 1)
	default: // "food"
		next.Secondary = clampFloat(next.Secondary+p.FoodHungerRestore, 0, 1)
	}
	return next
}

// consumableRestoreChanges 判断该分类在当前状态下是否真的能推动状态条。
// 阈值复用写回判定阈值，低于它的变化玩家看不出来，按"没生效"处理。
func consumableRestoreChanges(cur store.PlayerCondition, kind string, p conditionParams) bool {
	next := consumableRestoreTarget(cur, kind, p)
	return math.Abs(next.Primary-cur.Primary) > conditionWriteBackDelta ||
		math.Abs(next.Secondary-cur.Secondary) > conditionWriteBackDelta ||
		math.Abs(next.Comfort-cur.Comfort) > conditionWriteBackDelta ||
		math.Abs(next.LuckPenalty-cur.LuckPenalty) > conditionWriteBackDelta
}

// applyConsumableRestoreTx 在经济事务内应用食品/热饮的即时状态恢复。
// 计算口径与 consumableRestoreChanges 共用 consumableRestoreTarget。
func applyConsumableRestoreTx(ctx context.Context, tx *sql.Tx, uid int64, kind string, p conditionParams, now time.Time) error {
	cur, err := store.EnsurePlayerConditionTx(ctx, tx, uid)
	if err != nil {
		return err
	}
	next := consumableRestoreTarget(*cur, kind, p)
	next.UpdatedTS = float64(now.Unix())
	return store.UpsertPlayerConditionTx(ctx, tx, next)
}

// conditionFreeMealKV 免费餐冷却戳（kv 表，值为世界钟游戏日号）。
const conditionFreeMealKV = "condition_free_meal:%d"

// operationResultFoodAidOnceADay 官服结果码 16：
// kOperationResult_FoodAidAvailableOnceADay。
const operationResultFoodAidOnceADay = 16

// gameDayNumberAt 世界钟游戏日号（24 倍速，1 游戏日 = 1 现实小时）。
func gameDayNumberAt(now time.Time) int64 {
	return int64(float64(now.Unix()-worldDayEpochUnix) * gameMinutesPerRealSecond / gameMinutesPerDay)
}

// hKitchenEat 处理 3/13 player/kitchen_eat：客户端在野外厨房/餐车物件上确认进食。
//
// 请求只携带厨房物件 ID，不携带食物实例；
// 官服由服务端厨房食材账本扣减，本服未建模食材账本，故按"免费餐车"语义实现：
// 每游戏日限一次免费餐（1 游戏日 = 1 现实小时），冷却中回官服结果码 16。
// 恢复对齐抓包：进食后下一条 2/2 饱食度回到 1.0。成功响应回 null（与官服
// resp-3-13 模板一致）；幂等由通用请求回执表兜底。
func hKitchenEat(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	p := srv.conditionParams()
	if !p.Enabled {
		return []byte{0x00}, true
	}
	cctx, uid := ctx.Sess.pushCtx(), ctx.Sess.Userid
	// 官服口径：免费餐每游戏日一次（一天=游戏时间，见 Further Notes [决策]）；
	// 冷却中回官服结果码 16，客户端弹 kOperationResult_FoodAidAvailableOnceADay 文案。
	gameDay := gameDayNumberAt(time.Now())
	key := fmt.Sprintf(conditionFreeMealKV, uid)
	var last string
	if err := srv.DB.SQL.QueryRowContext(cctx, `SELECT v FROM kv WHERE k=?`, key).Scan(&last); err == nil {
		if lastDay, perr := strconv.ParseInt(last, 10, 64); perr == nil && lastDay == gameDay {
			ctx.Log.Info("免费餐本游戏日已用，拒绝", "uid", uid, "game_day", gameDay)
			ctx.ReplyOperationResult(operationResultFoodAidOnceADay)
			return nil, true
		}
	}
	next := srv.currentPlayerCondition(cctx, uid)
	next.Secondary = 1.0
	next.UpdatedTS = float64(time.Now().Unix())
	if err := srv.DB.UpsertPlayerCondition(cctx, next); err != nil {
		ctx.Log.Warn("进食恢复饱食度失败", "uid", uid, "err", err)
		ctx.ReplyFailure("System.InvalidOperationException", "进食未完成，请重试")
		return nil, true
	}
	if _, err := srv.DB.SQL.ExecContext(cctx, `
		INSERT INTO kv(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`,
		key, strconv.FormatInt(gameDay, 10)); err != nil {
		ctx.Log.Warn("免费餐冷却戳写入失败", "uid", uid, "err", err)
	}
	if kitchen := argFirstStr(ctx.Tree); kitchen != "" {
		ctx.Log.Info("玩家进食", "uid", uid, "kitchen", kitchen)
	} else {
		ctx.Log.Info("玩家进食", "uid", uid)
	}
	// 立即补推一次 2/2，让饱食度条在客户端平滑回升。
	ctx.AfterResponse(func() { pushKeepalive(ctx.Sess) })
	return []byte{0x00}, true
}
