# 铲子与长柄勺的饵池、解锁锚点与多饵上限

Status: done

## Problem Statement

`9/3` 挖饵此前的掉落是“5 段替换表”（0/20/40/60/80 各一套表，取当前命中的最高档），
带来三个问题：

1. **跨类交叉**：同一饵种同时出现在铲子与长柄勺的表里，换工具不换饵种。
2. **无法表达逐项解锁**：策划要求的是“每种饵在指定熟练度才出现”，替换表只能整档切换，
   既会提前给出本该晚解锁的饵，也会让某些饵在高熟练度时反而消失。
3. **单次种类数固定上限 3**，且与熟练度关系弱（第 2 种 55%、第 3 种需 40 熟练度）。
4. **池内有名单外的饵**：蛆虫、蛆虫蛹、树皮甲虫幼虫、襀翅幼虫。

仓库内没有官服挖饵权重表：静态库 20 张表里与饵相关的只有 `loc_bait`、`loc_bait_fish`、
`fish_bait_pref`（水域饵料偏好），没有工具或采集维度。

## Solution

饵池按“土生 / 水生”互斥，每种饵带**解锁锚点**：熟练度未达锚点一律不出现在掉落表里。

| 工具 | 饵（解锁锚点） |
|---|---|
| 铲子 `tool_shovel`、新年铲 `shovel_ny25` | 蚯蚓 `worm_01`(0)、红蚯蚓 `muckworm`(0)、地龙 `vypolzok_01`(75)、蜣螂 `dorbeetle`(80)、土蚕 `maybug_larva`(90)、蝼蛄 `gryllotalpa`(95)、金龟幼虫 `unibeetle_larva`(100) |
| 红虫长柄勺 `tool_scoop` | 红虫 `bloodworm`(45)、一束藻类 `watergrass`(45)、浮游幼虫 `naiad`(55)、毛翅目 `caddisfly`(70)、水蛭 `black_leech`(80)、龙虱甲虫 `waterbug`(100)、淡水钩虾 `gammarus_fresh`(100) |

**已删除的池内饵**：蛆虫 `maggot`、蛆虫蛹 `maggot_caster`、树皮甲虫幼虫 `scolytinae`、
襀翅幼虫 `stonefly_nymph`。

**单次种类数仍随熟练度增长，上限 7**：0~19 → 1 种；20~39 → 1~2；40~59 → 2~3；
60~79 → 3~5；80~100 → 4~7（实际数量再被“已解锁饵种数”收敛）。同次挖取不重复；
权重只在**已解锁集合**内归一。

**空池行为**：长柄勺锚点从 45 起，熟练度 <45 时其可用饵表为空 → `9/3` 回 803 `f0=0`
（挖饵失败），符合“未达解锁锚点不得提前挖出”。

## 名单名称映射（静态库核对）

| 名单名 | 采用的 item_id | 说明 |
|---|---|---|
| 浮游幼虫 | `naiad` | 静态库该 id 中文名为「蜉蝣幼虫」，全库无「浮游」条目 |
| 淡水钩虾 | `gammarus_fresh` | 中文名即「淡水钩虾」，category=27 |
| 金龟幼虫 | `unibeetle_larva` | 中文名「犀角金龟幼虫」 |
| 水生甲虫 | `waterbug` | 中文名「龙虱甲虫」 |
| 蜣螂 / 蝼蛄 / 土蚕 / 地龙 / 红蚯蚓 / 毛翅目 / 一束藻类 | `dorbeetle` / `gryllotalpa` / `maybug_larva` / `vypolzok_01` / `muckworm` / `caddisfly` / `watergrass` | 中文名逐字对应 |

## User Stories

1. 作为玩家，我希望每种饵在达到指定熟练度之后才出现，之前绝不会提前挖到。
2. 作为玩家，我希望铲子只挖土生的 7 种、长柄勺只抄水生的 7 种，两个池不交叉。
3. 作为玩家，我希望熟练度越高，一次挖到的饵种越多，最多 7 种。
4. 边界：同一次挖饵不会翻出两种相同的饵。
5. 边界：已解锁饵种少于该档目标种类数时按实际种数收敛，不报错也不补饵。
6. 边界：熟练度不足 45 使用长柄勺时应挖不到任何饵（803 f0=0），而不是提前给红虫。

## Implementation Decisions

- 模块：`internal/gameserver/baitdig.go`。
- 新增 `baitPoolEntry{Key, Unlock, Weight}` 与 `baitDigPools`（工具 → 固定饵池），删除原
  `baitDigBands` / `baitLootBand` 分段表。
- `baitLootTableFor(tool, mastery)` 改为“过滤已解锁项后返回权重表”，保留新年铲到铲子池的别名；
  锚点比较留 `1e-9` 容差，避免 44.999999 这类浮点误差卡住 45 档。
- 种类数沿用 `baitDigKindBands`（0/20/40/60/80 → 1 / 1~2 / 2~3 / 3~5 / 4~7）与
  `baitMaxKinds = 7`，由 `rollBaitDigLoot` 按档位表长度收敛。
- 空饵表时的失败文案改为“熟练度未解锁任何可挖饵料”。
- 熟练度成长（每次 +0.1、上限 100）、精力消耗 `bait_dig_energy_cost`、单种数量 1~3、
  803 响应形状、幂等回执一律不动。
- 无表结构、迁移、协议改动。回滚方式：revert 本提交。

## Testing Decisions

- 单元 `TestBaitDigUnlockAnchors`：逐个饵种断言“锚点前 0.1 不出现、锚点上出现”；
  另断言铲子 0 熟练度恰好 2 种、长柄勺 44.9 为空、45 恰好 2 种。
- 单元 `TestBaitDigPoolsAreToolExclusive`：两池无交集；每池饵种与预期清单完全一致；
  每个饵的锚点必须等于策划给定值；权重为正；新年铲无独立池且与铲子池同长。
- 单元 `TestBaitDigKindCountGrowsWithMastery`：逐档最小/最大种类数、0~100 全区间扫描、
  上界随熟练度单调不减。
- 单元 `TestBaitDigRollStaysInToolPoolWithoutDuplicates`：工具 × 熟练度（含 0/10/20 等未解锁档）
  × 200 固定种子；未解锁档必须挖不出任何饵，其余档不得越池/重复/超上限。
- 单元 `TestBaitDigAchievementBaitsAreReachable`：成就点名的每种饵只属于一个工具，且在满熟练度表内。
- 行为 `TestAchievementNewBaitDigRules`：逐个挖到 8 种新饵，对应成就点亮且不误点亮其它饵的成就。
- 行为 `TestBaitScoopDigIsHandled`、`TestBaitDigReportsAchievementForEveryProducedKind`：
  长柄勺用例改为满熟练度（锚点从 45 起）。
- 既有回归保持绿：`TestRollBaitDigLootKinds`、`TestBaitDigReportsGainAndKeepsStackTotal`、
  `TestBaitDigInventoryPushKeepsStackTotal`、`TestShovelDigReturnsBaitsAndKeepsShovel`、
  `TestBaitDigFlow`、`TestAchievementRegistryShape`。
- 回滚后变红：把任一饵的锚点调低/调高、把池外饵放回、或把种类上限改小，对应用例立刻失败。

## Out of Scope

- 不改官服权重（尚未取得）、不改熟练度成长（每次 +0.1、上限 100）、不改精力消耗
  `bait_dig_energy_cost`、不改单种数量（1~3，熟练度 20/60 各一次加成）。
- 不改 `craft_ops.go` 里不可达的 `shovelBaitDrops` / `rollShovelBaitLoot`（见 Further Notes）。
- 不实现 33 条剩余留空成就（竿型/轮型、成分等级、制作历史等，均缺运行期数据）。

## Further Notes

- [事实] 静态库无工具→饵料权重表：20 张表中与饵相关的只有 `loc_bait`、`loc_bait_fish`、
  `fish_bait_pref`，没有工具或采集维度。
- [事实] 锚点与权重由策划给定，属本地配置；`浮游幼虫` 在静态库无同名条目，取同一阶段的 `naiad`。
- [事实] 因为锚点是逐项解锁，低档不再“整档替换”，高熟练度玩家仍能挖到早期饵
  （先前“最高档必须覆盖全池”的约束由锚点模型天然满足）。
- [推断] 各饵权重（30/20/15/12/10/8/7 与 30/20/15/12/10/8/5）是标定值，不是官服数据。
- [未验证] 官服的锚点、权重与单次种类上限，待取得官服样本后校准。
