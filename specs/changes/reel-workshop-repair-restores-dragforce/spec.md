# 工坊修理电轮后仍显示损坏且无法装配

Status: done

## Problem Statement

玩家在工坊修理电动渔线轮后，轮子仍显示为损坏（红框/损坏文案），也无法装配到竿上。
界面还会提示「零件没有磨损。无需更换」。反复点修理不扣费、状态也毫无变化，
玩家实际上无法通过工坊把这只轮子修回可用状态。

## Solution

工坊「同型号修理」会把该零件的真实物理状态恢复到额定值，而不只是改显示字段：
摩擦片的制动力回到额定制动力，其余零件的耐久回满。修理完成后轮子本体耐久按
零件聚合口径抬起，客户端不再显示损坏，可以直接装到竿上。工坊诊断面板按真实
磨损（制动力）显示完好度与报价，不再把已磨穿的摩擦片报成「无磨损」。

## User Stories

1. 作为玩家，我希望磨穿的摩擦片在工坊修好后制动力恢复额定值，以便轮子能正常收线。
2. 作为玩家，我希望修理完成后轮子立即脱离损坏态并能装上竿，不必重登或换件。
3. 边界：摩擦片制动力已经是额定值时重复修理不应无限扣费（磨损为 0 时报价 0）。
4. 边界：字段缺失的历史存档按额定值起算，不被当成「已磨穿」。

## Implementation Decisions

- `internal/gameserver`（`econ_market.go`）：
  - `hWorkshopOrder` 的「同型号 = 修理」分支：对摩擦片品类（`reelPartCatWasher`）
    按 `itemDefF32(def, gearAttrDragMax) * row.Workmanship` 恢复 `csDragForce`，
    并从恢复前的制动力比例计算磨损报价；其余零件按 `reelChildDurability` 计算磨损。
    最后统一 `setReelChildDurability(ch, 1)` 写镜像耐久。
  - `repairPartsOf` 的 `condOf`：摩擦片完好度改按 `csDragForce / 额定` 报告，
    而不是读 `csDurability` 镜像。
- 不改协议：命令号、响应类、字段 op、args code 全部不变；只改服务端计算与写回。
- 不改表结构、不新增迁移。既有的 `migration:reel_friction_zero:v1` 语义保持不变
  （0 仍是权威的「已磨穿」）。
- 兼容：客户端版本基线 4.0.25034 不变；坏档在玩家下次修理时自动修复，无需人工救档。
- 回滚方式：还原 `econ_market.go` 即可，无数据迁移残留。

## Testing Decisions

- 测哪一层：
  - 存储 + 行为（真实 SQLite，走 `hWorkshopOrder` 全链路）：
    `TestWorkshopRepairRestoresWasherDragForce`、`TestWorkshopDiagReportsWasherDragWear`。
- 期望值来源：
  - 额定制动力取自静态库 `item_def.attrs_json.omghfmdfjpc`（= 38.1kg），不是用同一段代码反算。
  - 本体耐久期望值按 `reelBodyDurabilityInFields` 的聚合口径独立手算核对。
- 回滚后哪些测试会变红：两条新测试都会红（制动力停在 0、诊断完好度报 1.0）。

## Out of Scope

- 不修 `21/3`、`21/4` 的处理器注册归属（当前都指向 `hWorkshopOrder`，`hWorkshopRepair`
  仍是未被注册的死代码）。这是独立的语义分派问题，需另开一张票用真实报文确认
  客户端「只修理」与「换件」各自发哪个子命令，避免拍脑袋改注册。
- 不改摩擦片磨损速率、本体聚合权重等平衡性数值。
- 不处理整机大保养（`restoreReelInternals`）与换件路径的合并语义。

## Further Notes

证据与来源：

- `[事实]` 磨穿的摩擦片在存档中表现为 `csDragForce=0`（字段存在）而
  `csDurability` 仍为 1.0，两者脱节；`degradeFrictionWasherFields` 已把
  `csDurability` 明确注释为制动力的显示镜像。
- `[事实]` 修理前该轮本体 `durability=0`、`reel_performance` 的
  `base_drag_kg`/`locked_drag_kg`/`durability` 同为 0，与客户端损坏态一致。
- `[事实]` 旧实现的同型号修理分支只写 `csDurability`，因此可用 0 费用反复
  「修理」而不改变任何物理状态；`workshop_part` 流水金额为 0 可佐证。
- `[事实]` 端到端跑 `hWorkshopOrder` 后：制动力恢复额定制动力、本体耐久按
  聚合口径抬起、`itemBrokenForMount` 返回 false。
- `[推断]` 客户端红框与「无法装配」由服务端下发的耐久/状态位推导，故修好耐久
  即可解除。
- `[假设]` 摩擦片 `csDurability` 在所有路径上都是镜像字段（由
  `degradeFrictionWasherFields` 的注释与写入点支持），故修理只以 `csDragForce` 为准。
