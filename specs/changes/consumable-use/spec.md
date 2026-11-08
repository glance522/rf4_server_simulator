# 消耗品使用后扣减数量

Status: done

## Problem Statement

背包里的食物、饮料等可食用消耗品在使用（协议 `4/8` 物品使用）后数量不减少。`hItemUse` 只把酒类交给 `useAlcoholDrink`、技能书交给 `useSkillBook`，其余物品统一走“返回单件快照”的分支，从未扣减数量，导致玩家可以无限次使用同一个食物。

## Solution

在 `hItemUse` 的处理链中新增 `useFoodConsumable`：对 `item_def.category = 26`（食物/饮料）且不在酒类白名单里的物品，在同一事务内扣减 1，数量归零时从背包移除，并把更新后的 800 快照返回给客户端。

## User Stories

1. 作为玩家，我希望吃掉一份食物后背包数量减少 1，而不是永远保持原数量。
2. 作为玩家，我希望重复点击同一次使用请求不会重复扣减（网络重传、断线重连重放）。
3. 边界：酒类仍走原有酒兴逻辑，技能书仍走熟练度逻辑，工具与装备等非消耗品保持“只返回快照”的现有行为。

## Implementation Decisions

- 新增 `internal/gameserver/consumable_use.go`，函数 `useFoodConsumable` 接入 `hItemUse`，位于 `useAlcoholDrink`、`useSkillBook` 之后、通用快照分支之前。
- 判定条件：`def.Category == 26` 且 `drinkRuleForItem` 未命中；不做白名单，任何食物/饮料都能消耗，避免新商品继续复现同一问题。
- 幂等：回执写入 `kv`，键为 `food-use:<uid>:<tokenHash>:<main>:<sub>:<call>`，同请求重放直接返回已提交的响应体。
- 事务：读取物品行、扣减数量、构造响应、写回执在同一事务内完成；提交后再通过 `AfterResponse` 推送背包变更。
- 数量归零时把 GUID 放进 `removed` 列表，否则放 `updated` 列表，沿用 `buildRigSnapshot800` 与 `stateFlagsZeroFill`。
- 不属于消耗品的物品（工具、装备、鱼饵）不进入该分支，行为与改动前一致。

## Testing Decisions

- `drink_use_test.go` 中原有断言“普通食物使用后数量不变”固化了本缺陷，改为使用后 4 → 3。
- 覆盖：普通食物扣减、酒类仍走酒兴、未知 GUID 仍走通用分支、移动路由不受影响。
- 完成前运行 `go test -run 'Item|Use|Bag|Inventory|Drink|SkillBook|Food' ./internal/gameserver`、`go vet ./...`、`go build ./...`。

## Out of Scope

不修改酒兴与技能书的既有逻辑，不增加食物效果（饱食度/体力）系统，不改变商店或背包协议形状。
