# 消耗品使用、饱食度与体力

Status: done

## Problem Statement

三个问题叠在一起：

1. 背包里的食物、饮料使用后数量不减少，可以无限次吃同一个物品：`hItemUse`（协议 `4/8`）只把酒类交给 `useAlcoholDrink`、技能书交给 `useSkillBook`，其余物品统一走“返回单件快照”的兜底分支，从未扣减数量。
2. 进食没有任何饱食度概念，客户端左下角“食物条”与服务端毫无关系。
3. 挖饵、搏鱼不消耗体力，“体力条”永远是满的。

## Solution

- `4/8` 处理链新增 `useFoodConsumable`：食物/饮料扣 1 份，同时按类型增加饱食度。
- 饱食度与体力统一到 `player_condition`：`secondary` 对应客户端“食物条”，`primary` 对应“体力条”。
- 挖饵一次性消耗体力，搏鱼按秒消耗体力，体力按食物条高低自行恢复，恢复过程吃掉食物条。
- 饱食度随时间自然衰减。

## User Stories

1. 作为玩家，我希望吃掉一份食物后背包数量减少 1，而不是永远保持原数量。
2. 作为玩家，我希望吃满后无法继续进食，直到饱食度降下来。
3. 作为玩家，我希望体力会被挖饵和搏鱼消耗，并通过进食与饱食度慢慢回补。
4. 边界：重复点击同一次使用请求不能重复扣减；体力/食物条始终在 0~1 之间；离线一段时间后回归不会暴涨。

## Implementation Decisions

- 新增 `internal/gameserver/consumable_use.go`：`hItemUse` 中接在酒类、技能书之后；判定 `item_def.category = 26` 且不在 `drinkRuleForItem` 白名单；事务内扣 1、加饱食度、写回执（键 `food-use:<uid>:<tokenHash>:<main>:<sub>:<call>`）。
- 状态条映射：`player_condition.secondary` = 食物条（饱食度），`primary` = 体力。新增 `GetConditionTx` / `AddConditionTx` / `AddCondition` / `RegenEnergyFromFood`。
- 进食增量：固体食物 +0.10、饮料 +0.06、酒 +0.05；同时体力 +增量的 2/9（吃的直接回体力只占很小一部分）。
- 食物条自然衰减：每分钟 -0.08，读取饱食度时按 `updated_ts` 差值折算并写回。
- 体力恢复：每次请求入口（`handleEnvelopePayload`）按经过秒数逐秒积分，速率随当时食物条变化：

  | 食物条 | 每秒回复 | 从 0 回满 |
  |---|---|---|
  | 1.00 | 0.100 | 10 秒 |
  | 0.50 | 0.0167 | 60 秒 |
  | 0.25 | 0.0111 | 90 秒 |

  恢复 1 点体力消耗 0.5 食物条；单次补算上限 300 秒，避免离线暴涨。
- 消耗：挖饵一次 -0.8 体力（低于 0.8 拒绝并提示“体力不足”）；搏鱼 -0.01/秒（14/7 按秒节流）。体力满时既不恢复也不消耗食物条。
- 不新增协议命令，不改变响应形状：进食沿用 `buildRigSnapshot800`，挖饵沿用 803 报告。

## Testing Decisions

- `drink_use_test.go` 中原有断言“普通食物使用后数量不变”固化了缺陷，改为使用后 4 → 3；饮酒刷新用例在第二次饮酒前重置食物条，避开吃饱拦截。
- 新增 `consumable_use_test.go`：进食扣减并提高食物条；食物条满（≥0.995）时拒绝且数量不变。
- 挖饵相关用例（`baitdig_quantity_test.go`、`skills_craft_test.go`）在连续挖饵之间回满体力，避免被“体力不足”拦住。
- 完成前运行 `go build ./...`、`go vet ./...`、`go test ./...`；Windows 上 `t.TempDir()` 清理偶发失败与本改动无关，单独重跑通过。

## Out of Scope

不改酒兴与技能书的既有逻辑，不增加食物效果（buff/属性），不把饱食度/体力写进 307 档案字段（客户端 HUD 走 `2/2` 的 308 状态条），不处理“吃饱了”的弹窗文案（需要 `kOperationResult_PlayerFull` 的枚举值，客户端资源里未取到）。
