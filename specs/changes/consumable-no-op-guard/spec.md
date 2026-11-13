# 消耗品无效果时不得白扣

Status: done

## Problem Statement

玩家在精力满的时候喝咖啡，背包里的咖啡减少 1 份，但精力条和饱食度条都不动，
界面上没有任何提示。实机复现后服务端侧确认：

- 4/8 请求回的是正常 800 快照，紧接着推送 4/21 把该实例移出背包；
- 前后两条 2/2 保活帧逐字节相同：f0 恒为当前精力上限、f1 为 0。

即：`big_coffee` 命中消耗品通道 → `applyConsumableRestoreTx` 做
`Primary + 0.15` 后被 `clampFloat(..., 0, EnergyMax)` 钳回上限 →
状态零变化，但 `consumeItemQtyTx` 照扣。凡是"目标条已在上限"的消耗品都有此问题
（咖啡/茶/可可/药品/食品同理）。

同一条通道还带出第二个坑：`satiety-full-reject` 引入的饱食度满度检查是**无条件**
套在所有 `isConsumable` 物品上的，导致饱食度满时吃药（只影响 f4）和喝茶（只影响 f3）
也会被「已吃饱」拒绝并回结果码 55 —— 与这两类物品的真实效果无关。

## Solution

消耗品只有在**确实能推动它负责的那条（或那几条）状态条**时才生效；
推不动时不扣物品。食品类（含可可的饱食分量）在饱食度已满时仍回官服结果码 55
显示「已吃饱」；其它条已满时静默不消耗、物品保留。

## User Stories

1. 作为玩家，精力满时点咖啡，咖啡不该消失。
2. 作为玩家，饱食度满时吃药/喝茶，不该被「已吃饱」拦住，因为它们不回饱食度。
3. 边界：饱食度满但精力未满时喝热可可，仍按精力那一路生效并扣物。
4. 边界：总开关关闭时白名单物品维持旧行为（落回通用 handler，不扣不恢复）。

## Implementation Decisions

- `internal/gameserver/player_condition.go`：把 `applyConsumableRestoreTx` 里的分类
  计算结果抽成纯函数 `consumableRestoreTarget(cur, kind, p)`，返回推进后的状态；
  恢复是否"有效"由"目标条是否真的变化"判定（阈值复用 `conditionWriteBackDelta`），
  判定与应用共用同一份计算，避免两处规则漂移。
- `internal/gameserver/drink_use.go`：把原来的"无条件饱食度满检查"改成调用上述判定，
  在 `consumeItemQtyTx` 之前执行：
  - 无任何目标条变化且该物品的恢复只要涉及饱食度（`food`）→ `ReplyOperationResult(55)`，
    行为与 `satiety-full-reject` 一致（该码已有实机证据）；
  - 无任何目标条变化且与饱食度无关（咖啡精力满/茶舒适满/药品满血）→ 返回
    `handled=false` 落回通用 4/8 handler，物品原样保留，客户端不会看到扣物。
- 不改协议形状、不改表结构、不动回执键：拒绝仍发生在写回执之前。
- 回滚方式：单文件回退 `drink_use.go` + `player_condition.go` 两处改动即可。

## Testing Decisions

- 行为（真实 SQLite + 现有 `useAlcoholDrink` 接缝）：
  - 精力=上限 0.85 时喝 `big_coffee`：`useAlcoholDrink` 返回未处理，物品数量不变；
  - 精力=0.50 时喝 `big_coffee`：精力到 0.65、物品扣 1（期望值手算 0.50+0.15）；
  - 饱食度=1.0 时吃 `pill_pack`：仍生效（f4 0.60→0.10），不被 55 拦；
  - 饱食度=1.0 时喝 `ab_tea_can`：仍生效（f3 0.10→0.40）；
  - `carrot` 在饱食度=1.0 时仍回 55 且不扣物（回归 `satiety-full-reject`）。
- 期望值全部来自手算样例与实机 2/2 帧，不用被测代码回算。
- 回滚后变红：上面新增的 4 条行为用例全部变红；`TestFoodUseRejectedWhenSatietyFull`
  应保持绿（它守的是已被证实的 55 语义）。

## Out of Scope

- 精力恢复速率（`condition_energy_regen_per_hour`=100/h）是否过快的标定问题：
  它使精力常态顶满，从而让所有回精力的消耗品丧失意义，属于数值标定，另案处理。
- 空效果时的客户端本地化提示文案：`kOperationResult_*` 里没有「这条状态已满」
  的实证取值，本次不猜码。
- 酒类经验加成链路不变。
