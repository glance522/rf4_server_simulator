# 工坊修理与换件的幂等口径和显示一致性

Status: done

## Problem Statement

玩家在工作坊修理/更换零件时遇到三类现象：

1. 客户端对同一修理操作会连发两次 21/3，第二次到达时目标已被第一次修满，
   按磨损计价为 0，服务端按"零金额交易"拒绝并走兜底链，客户端弹错。
2. 零件磨损极小（整机价四舍五入为 0）时，修理返回成功但本体耐久原地不动，
   耐久永远差千分之几。
3. 服务端状态正确后，客户端面板仍显示旧值：换同槽位第二种型号手柄显示不
   刷新；同型号修理后零件耐久显示停在磨损值。
4. 换到手柄的装配在服务端重启后被打回出厂默认件（RepairItemPhysics 与
   ItemToTree 的 rebuildRod 按定义重建 f0/f1/f2）。

## Solution

纯修理落到零价按幂等成功处理（不扣费、不写流水、回当前物品树并伴随 4/22
刷新）；凡有实际变化（本体耐久、子件耐久、子件 guid）必须真实落库并抬版本
戳——客户端只接受版本戳变大的 4/22 更新，只刷本体没有子件修理时也一样；
落库与成功路径同走事务，任何一步失败按失败拒绝，不能返回成功却没修。幂等
成功仅限纯修理（无换件条目，或条目全是同型号修理），带换件条目却算出零价
仍按定价缺失拒绝；竿类修理对齐渔轮整机保养口径，一次恢复全部出厂件并按磨
损总量计价；只有本次实际恢复耐久的子件与换装的新型号子件使用新 guid 驱动
客户端重渲染；rebuildRod 只整体保留换过型号的子件，出厂默认件仍按定义重建
（静态定义更新可通过默认槽位重新派生），重建时保留原子件 guid 与耐久。

## User Stories

1. 作为玩家，我点击修理后不应看到错误弹窗，即使这是同一操作的重复请求。
2. 作为玩家，修理返回成功后物品耐久应显示为满。
3. 作为玩家，我在同一槽位连续更换不同型号零件时，面板应立即显示新型号。
4. 作为玩家，修理一根竿应恢复全部出厂件耐久，费用为恢复的磨损总量。
5. 边界：重复请求第二次不扣费、不产生流水；有换件条目却算出零价仍拒绝
   （含本体修理条目搭带换件条目的组合——幂等成功返回会让换件被静默丢弃）。

## Implementation Decisions

- `internal/gameserver` econ_market.go：21/3/21/4 处理器 hWorkshopOrder 的
  零价幂等分支（有变化即事务落库并抬版本戳，失败拒绝）、整竿保养分支、
  子件 guid 策略；hWorkshopDiag 对竿报整机价。
- `internal/gameserver` items.go：rebuildRod 只整体保留换过型号的子件，
  出厂默认件按定义重建并保留原子件 guid 与耐久。
- 不改表结构、不改协议字段形状（21/3 请求/响应形状与字段语义不变）。
- 兼容：客户端基线 4.0.25034；旧档无需迁移；回滚即还原本次提交。

## Testing Decisions

- 单元/存储层（真实 SQLite，接缝为 hWorkshopOrder/hWorkshopDiag/RepairItemPhysics）：
  - 零价修理幂等成功且不扣费不写流水（TestWorkshopReelRepairZeroCostIsIdempotent）。
  - 同一请求连发两次只扣一次费（TestWorkshopSameModelRepairTwiceIsIdempotent）。
  - 零价修理把本体刷满落库（TestWorkshopZeroCostRepairRefillsBodyDurability）。
  - 轮油磨损被同型号修理恢复并正确计价
    （TestWorkshopReelOilWearRestoredBySameModelRepair）。
  - 换装手柄在启动修复与下发渲染后保留
    （TestWorkshopSwapHandleSurvivesStartupRepairAndRender）。
  - 换不同型号产生新 guid、同型号重复构造 guid 稳定
    （TestWorkshopSwapNewModelGetsFreshGuid）。
  - 整竿保养恢复全部出厂件并按总量计价
    （TestWorkshopRodRepairRestoresAllFactoryParts）。
  - 实际修理过的子件换新 guid（TestWorkshopSameModelRepairRefreshesChildGuid）。
  - 零价修理把循环顺带修掉的子件一并落库并换 guid、抬版本戳，重复请求
    guid 稳定（TestWorkshopZeroCostRepairPersistsChildRepair）。
  - 零价只刷本体也抬版本戳，推送树（ItemToTree）版本戳与落库一致、本体
    耐久投影为 1（TestWorkshopZeroCostRepairRefillsBodyDurability 扩展
    断言；TestWorkshopZeroCostRepairPushesVersionedTree 走真实信封链路
    断言 4/22 推送帧）。
  - 零价落库失败按失败拒绝，耐久与版本戳保持原值
    （TestWorkshopZeroCostRepairFailsWhenPersistFails）。
  - 本体修理搭带换件条目的零价请求被拒绝，不静默丢换件
    （TestWorkshopZeroCostBodyRepairWithLooseOrderRejected）。
  - 整机保养只给实际恢复耐久的子件换 guid，满耐久子件 guid 不变
    （TestWorkshopReelWholeServiceOnlyRefreshesWornGuids）。
  - 下发渲染/启动修复保留修理刷新的默认件 guid
    （TestWorkshopRenderKeepsRepairedDefaultPartGuid）。
  - 21/3 经统一回执中间件：同 call_id 重试/重连重放只扣一次费、响应逐字节
    重放（TestWorkshopRepairReplayThroughReceiptsChargesOnce）；并发同 key
    请求只执行一次、只扣一次（TestWorkshopRepairConcurrentDuplicatesChargeOnce）。
- 回滚本提交后以上测试变红（旧口径为拒绝零价/单件修理/重建换装/全量换
  guid/丢弃零价子件修理）。

## Out of Scope

- 21/4 金币通道的 useGold 字段解析（当前金币修理本就被拒绝，另行处理）。
- 客户端侧渲染逻辑。
- 渔轮性能表与磨损模型。
- 21/3/21/4 的扣费回执幂等由统一请求回执中间件承担，不在本 PR 范围内重复
  实现：dispatchRequest 对所有命令（豁免表仅 24/1）按 token 哈希 + main +
  sub + call_id 原子抢占回执（request_receipts 表），先落回执再发送响应；
  顺序重发与并发重复只执行一次，其余重放已存响应，body 指纹不一致则拒绝
  执行。（初稿把"并发重复会各自扣费"列为遗留问题，是对中间件机制的误读，
  已更正。）剩余边界：call_id 每次连接重新编号，重连后同一动作拿到新
  call_id 会按当次磨损重新计价（call_id 键控方案的共性，drink_use.go 同）；
  处理器事务提交后、回执落库前进程崩溃时，2 分钟 stale 接管按 at-least-once
  重执行（store 层注释明示）；回执重放只回 21/3 响应体，不再补推 4/22
  （首执行时已推送，极端丢帧场景面板重开即刷新）。
- 21/2 每个零件行都报整机保养价：这是报价契约——客户端按选中行的 f3 报价，
  只在首行报整机价会让非首行发起的修理因报价低于服务端价格被拒。多选累加
  的展示差异由 workshopQuoteCovers 容忍，服务端只收一次。
- 换回之前用过的型号复用确定性 guid（host|key|sysID）：客户端按 guid 缓存时
  可能显示旧耐久；换 guid 需把换装 guid 持久化，另行处理。

## Further Notes

- [事实] 客户端对同一修理连发两次 21/3：服务端 trace 同一物品毫秒级两条同
  内容请求。
- [事实] 客户端按子件 guid 缓存零件显示：4/22 携带新型号 nid 而面板不刷新，
  guid 变化后刷新；21/2 数值更新不驱动零件行重渲染。
- [推断] rebuildRod 打回换装：换手柄扣费流水存在而库存型号不变，重启后
  复现；副本环境跑 RepairItemPhysics 复现同路径。
