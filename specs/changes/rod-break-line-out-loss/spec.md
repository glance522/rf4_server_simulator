# 断竿只损失实际放出的主线

Status: done

## Problem Statement

满红炸钓组、断的是竿时，`breakRodTx` 把主线整卷一起销毁（`lost_on_rod_break`）。
玩家手里断的是竿梢，杯上没放出去的线根本没有断——整卷 1000+ 米跟着竿一起判死，
代价与事实不符。断主线（同一 17/9 上报）已经按 body 0x3f 的实际放线米数扣线
（`line-break-loss-m`），断竿却还是清杯口径，同一条上报两种口径。

## Solution

断竿时主线只按 17/9 上报的实际放线米数扣减：扣完仍有剩余 → 主线物品保留、
仍挂在断竿上（与渔轮同待遇），f5 钓组与挂线配件照旧全部销毁；扣到 0 → 照旧
整条销毁并清 f3。上报未带有效放线米数（视为未上报）→ 主线完整保留，一米不扣。

## User Stories

1. 作为玩家，满红搏鱼断竿后，我损失的只有已经放出去的那段线，杯上的线还能
   拆下来装到别的竿上继续用。
2. 边界：上报放线米数 ≥ 剩余线长时按剩余线长封顶，主线耗尽删除，与断主线
   耗尽路径同形，不出现负数长度。
3. 边界：17/9 没带有效放线米数（body 过短、0、负数、NaN、超 5000）时视为
   未上报，主线一米不扣——竿断了不等于杯上的线断了。
4. 边界：竿上没挂主线或 f3 指向已不存在的行时，跳过扣减，f3/投影照旧清理，
   不产生幽灵引用。

## Implementation Decisions

- `internal/gameserver/gear_damage.go`
  - `breakRodTx` 增参 `lineOutM float64`；`breakLineOrRodWithLineOut` 的两个
    断竿调用点（显式上报竿 GUID / 最弱环回退判竿）透传同一值。17/9 解析
    `rodBreakLineOutM` 不动。
  - 扣减与 `lineLossTx` 主线分支同源：`itemLengthField` 读剩余长度（缺字段
    回退定义全长），`int64(lineOutM+0.5)` 取整，正数不足 1 米按 1 米，
    `beforeLength` 封顶。两处不扣减的护栏（评审补充）：线长读不出来
    （字段缺失且定义全长不可得）按未上报处理，整卷保留不判死；f3 指向
    `Container != "rig" || RigGUID != 竿` 的陈旧引用时只清引用与投影，
    不动物品本身。只有真扣了米数才写线字段与 `line_break` 日志。
  - 线保留时：`setItemLengthField` + `updateItemFieldsTx`；f3 引用与
    rig_component 的 catLine 投影行保留（从销毁集合与 `DELETE FROM
    rig_component` 的豁免条件里把线与渔轮并排）；线 GUID 进 `UpdatedTrees`
    （11028 arg3），不进移除列表。
  - 线耗尽（或行缺失）时：`DeleteItemCascadeTx` + 清 f3 + 清投影行 + 进移除
    列表，与现状一致。
  - 伤害日志：扣减记 `line_break`（detail `reason: rod_break` + `loss_m`），
    耗尽再补 `line_exhausted`，与 `lineLossTx` 的记账形状一致；不填耐久字段
    （断竿不动线的耐久，长度看 BeforeLength/AfterLength，避免误读）；
    `lost_on_rod_break` 仍只记挂线配件。
  - `gearBreakOutcome.LineBroken` 保持 false（断竿计数、跑鱼播报、
    `st.GearGuid` 清理都不变），`LineLossM` 带上扣减米数供日志。
- 协议形状不变：11028 四字段、4/21/4/22 时机不变；不新增协议字段，
  不改客户端行为。客户端基线 `4.0.25034`；回滚方式为 revert 本提交。

## Testing Decisions

- 测哪一层：存储层真实 SQLite（`newRigTestServer`），走 `hDynamicRodBreak`
  与 `breakRodTx` 两个入口，不打桩；期望值为手算字面量。
- 更新 `TestRodBreakDestroysRigGearKeepsBrokenRod` 与
  `TestRodBreakKeepsReelMounted`：线保留口径（钩/漂仍销毁、轮仍挂竿）。
- 新增三态：上报 12.5m/50m 线 → 剩 37m 且线仍挂竿（f3 与 catLine 投影行
  保留、不在移除列表）；上报 200m/50m 线 → 耗尽删除；body 无放线字段 →
  50m 全保留。
- 评审补充：上报 0.5m → 钳到扣 1m（剩 49）；线长不可读 + 上报 12.5m →
  不删线（修复前会误删）；f3 陈旧引用（线在背包）→ 断竿不动背包线、只清
  引用与投影；断竿后 17/3 拆线 → 37m 整卷退回背包、f3 与投影清掉。
- 回滚后以上用例变红，可据此确认覆盖有效。

## Out of Scope

- 不改断主线、剪线（17/10）、断子线、断轮的口径。
- 不给断竿接 tunable `line_break_loss_m`：未上报就一米不扣，不引入猜测性兜底。
- 不改 17/9 body 解析与 `rodBreakLineOutM` 边界。

## Further Notes

- `[事实]` 17/9 body 偏移 0x3f 为实际放线米数：`line-break-loss-m` 已用真实
  报文验证，同一命令同一 body 布局，断竿上报同样携带。
- `[事实]` 现状整卷销毁：`gear_damage.go` `breakRodTx` 把 f3 主线 GUID 并入
  销毁集合并记 `lost_on_rod_break`。
- `[推断→已测]` 拆卸（17/3）不受断竿耐久拦截，只挡装配——保留在断竿上的线
  可拆下换竿，不存在"线卡死在断竿上"。`TestRodBreakLineDetachableAfterBreak`
  已覆盖模拟侧；实机回归时再确认。
- `[事实]` 全量 `go test ./...` 中 `TestFishMarketFlow` /
  `TestCafeNativeFlowPaysAndPushesRemoval` 在满套件并行下偶发"等待推送超时"
  （固定 2s WaitPush）；干净 HEAD 基线（stash 本改动后）复现同样两例失败，
  与本改动无关。单跑或两例同跑均通过。
