# 垂钓中暂停精力自然恢复

Status: done

## Problem Statement

喝完咖啡把精力补满之后，精力条四五分钟一动不动；实际抛投/拉拽的消耗小到看不见——0.01 和 0.005 的扣减，在 100/h 的自然恢复下 0.36 秒就被抹平。玩家看不到"钓鱼在消耗体力"，精力机制在实际游戏里不存在。官服一小时会话的精力是净下降的（0.85→0.83），本服却是恒满。

## Solution

竿在水中（已抛投、入水、未结算）时，精力自然恢复停摆，动作消耗开始累积，精力条随之可见地往下走；收竿即恢复，仍保持"打完吃顿饭半分钟再战"的节奏。停摆强度走 tunable，维护者可调成慢速恢复而不是完全停。

## User Stories

1. 作为玩家，我希望钓鱼过程中精力条看得见地往下掉，以便体力是有代价的资源。
2. 作为玩家，我希望收竿后精力照旧快速回升，以便不用站着干等半小时。
3. 边界：多竿时任一竿在水中就算垂钓中；已结算/已收线/未抛投都不算。
4. 边界：从未钓过鱼的玩家读精力不产生任何副作用（不为判据懒建鱼状态、不查库）。
5. 边界：动作路径（抛投扣精力、喝咖啡）的环境口径不变，只有"是否垂钓中"这一位生效。

## Implementation Decisions

- `internal/gameserver/player_condition.go`：`conditionEnv` 增加 `Fishing` 布尔位；`advancePlayerCondition` 在算完晒阳倍率后乘 `EnergyRegenFishingMult`。新增 tunable `condition_energy_regen_fishing_mult`（缺省 0），走既有 `conditionParams` 装载，30 秒缓存生效。
- 判据 `playerFishingActive(uid)`：读内存里既有的鱼状态，逐竿判 `CastActive && InWater && !Resolved`（与在线垂钓识别同源）。不走 `fishingStatesFor`——那条路径会为冷玩家懒建鱼状态并可能触发 `LoadFishingStates` 查库，读精力不该有这种写副作用。
- 动作路径新增 `conditionEnvForUID(uid)`，只带 `Fishing` 位，替换 `currentPlayerCondition` 与 `spendEnergy` 里的空环境。天气/昼夜/篝火仍只在 2/2 保活拍解析，动作路径不随天气抖动。
- `spendEnergyTx` 增加 `env conditionEnv` 形参：env 由调用方在开经济事务之前取好，事务内不查库（`tunableF` 首次装载有 2 秒超时，持锁事务内首次调用会超时并静默回退默认值）。
- 锁序：`playerFishingActive` 只在 `fishStateMutex` 内读字段，锁内不调用任何会再加同一把锁的函数；已有的 `pushPendingBite` 等路径都在 `st.mu.Unlock()` 之后才走精力链路，无环路。
- 数据与迁移：不改表、不加迁移。新 tunable 按既有"不 seed、缺省走代码"的口径生效。
- 兼容与回滚：`condition_energy_regen_fishing_mult` 设成 1 即完全回到改动前的行为，无需改代码。客户端零改动。

## Testing Decisions

- 单元（纯逻辑，手算）：竿在水中 60 秒精力恒为初值；同一初值下空环境 60 秒回到 `EnergyRegenCap`；停竿 5 秒恢复约 0.1385（`100 × 0.997 × 5/3600`）；乘数 0.5 时恢复量减半。
- 行为（`playerFishingActive` 接缝）：四组状态判定（抛投且入水未结算=真、已收线未入水=假、未抛投=假、已结算=假）；多竿任一在水中为真；冷玩家为假且不为其创建鱼状态。
- 期望值来自手算（见 `fishing_regen_pause_test.go` 注释里的算式）与官方小时级净下降的观测方向，不用被测代码回算。
- 回滚后变红：`TestFishingPausesNaturalRegen`、`TestFishingPauseLiftsImmediatelyAfterReelIn`、`TestFishingRegenMultIsTunable` 会红；`TestPlayerFishingActive*` 与既有 `TestAdvance*` 应保持绿。

## Out of Scope

- 动作消耗量本身的标定（`condition_cast_energy_cost`=0.01、`condition_pull_energy_cost`=0.005、`bait_dig_energy_cost`=0.15 均不动）。
- 自然恢复速率 `condition_energy_regen_per_hour`=100/h 的绝对值（保持 `specs/changes/energy-regen-fast` 的决策）。
- 精力硬上限 1.0 与自然恢复封顶 0.85 的分离（已由 `specs/changes/energy-cap-vs-regen-cap` 处理）。
- 离线期间的精力/饱食口径（沿用 `player-condition-core` 的维护者决策）。

## Further Notes

- [事实] 官服一小时会话的精力取值区间为 [0.830159, 0.85]，是净下降（逐帧抓包统计，结论已记入 `specs/changes/player-condition-core` 的 Further Notes）。
- [事实] 实机库中高强度一小时约为 100 次抛投 + 120 次拉拽，折算总消耗 1.6 精力/小时；恢复 100/h 是它的 60 倍以上。
- [推断] "竿在水中"是消耗发生的唯一场景，因此把恢复停摆绑在这个状态上，两个目标（钓鱼时可见下降、空闲时快速回满）才能同时成立；若按全局降速处理，回满一管会退回 25 分钟级，与 `energy-regen-fast` 的决策直接冲突。
- [未验证] 官服是否真的在垂钓时暂停恢复——客户端无法把恢复/消耗的净速率分离，本条的取舍是"让机制有实感"，不是官服复刻。
