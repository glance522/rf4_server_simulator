# 文档索引

仓库根目录的 [README](../README.md) 是项目入口，本文档目录汇总部署、实现、玩法和开发资料。

## 使用与部署

- [部署指南](deploy.md)：构建、配置、初始化、启动、客户端接入与安全约束。
- [客户端兼容性检查清单](client-checklist.md)：登录、会话、玩家状态、钓鱼、物品与经济等回归项。
- [管理控制台](admin-console.md)：launcher 内嵌 Control Deck 的功能与 API。
- [静态数据隔离](static_runtime_isolation.md)：可写玩家库与只读静态目录的运行时隔离模型。

## 架构与实现

- [架构与实现概览](architecture.md)：组件关系、当前主链路、目录结构与已知边界。
- [机制代码索引](mechanism-map.md)：机制 → 入口文件、入口函数与测试；只给指针，不复制规则。
- [钓鱼机制](gameplay.md)：鱼群分布、吃饵偏好、天气鱼口、饵料消耗和实现边界。
- [游戏内查鱼：坐标](fish-query-coordinates.md)：查鱼回复里坐标口径与地图落点换算。
- [游戏内查鱼：水域](fish-query-water.md)：查鱼回复里水域/深度口径与工具用法。

## 机制专题

单个机制的实现边界与验收口径。新增一份，必须回到本文登记（归属规则见
[领域文档约定](agents/domain.md)）；玩法总纲仍在 `gameplay.md`，这里不复制。

- [钓获结算事务](catch-settlement.md)：鱼 GUID 回执、同事务提交范围与失败重试。
- [钓鱼状态恢复](fishing-state-recovery.md)：快照、重连与未结算鱼的恢复路径。
- [护网与钓获记录同步](keepnet-synchronization.md)：入护、记录与库存一致性。
- [当前装备的技能加成](equipped-fishing-skills.md)：装备加成如何进入判定。
- [欢乐时光](happy-hour.md)：计时口径、加成数值与资格持久化。
- [战利品箱](loot-box.md)：开箱产出与发放口径。
- [弹弓投掷视觉](slingshot-throw-visual.md)：投掷外观与服务端校验的边界。

## 内容与经济

- [商店目录](shop_catalog.md)：货架解析顺序、覆盖规则与支持范围。
- [个人中心](portal.md)：角色资料、余额、会员购买与身份边界。
- [咖啡厅订单](cafe_orders.md)：订单生成、交单事务与迁移兼容说明。

## 开发与协议

- [调试工作流](debugging-workflow.md)：本地调试、日志和协议排查流程。
- [官方协议工作坊](official-protocol-workshop.md)：协议观察、记录和整理资料。
- [钓鱼全链路分析（2026-11-07）](analysis-fishing-chain-2026-11-07.md)：一次性排查快照，
  含历史事故过程与行号，会过期；结论以代码与能力契约为准。
- [实测博鱼过程对装备磨损的影响分析（2026-11-11）](实测博鱼过程对装备磨损的影响分析.md)：
  一次性分析快照，官服抓包对拍磨损口径的证据源；结论已正式化为
  specs/changes/wear-fight-time/spec.md。

### 搏鱼物理与发鱼分析快照

- [搏鱼物理：总览与问题清单（2026-11-07）](analysis-fish-fight-physics-2026-11-07.md)：
  上鱼→轨迹→拉力→双方耐力四篇的入口篇。
- [搏鱼物理 1/4：鱼的运动轨迹（客户端 Fish AI）](analysis-fish-fight-physics-fish-ai-2026-11-07.md)。
- [搏鱼物理 2/4：动作拉力与线物理](analysis-fish-fight-physics-pull-line-2026-11-07.md)。
- [搏鱼物理 3/4：鱼的耐力（stamina）机制](analysis-fish-fight-physics-fish-stamina-2026-11-07.md)。
- [搏鱼物理 4/4：玩家耐力/体力条（308.f0）](analysis-fish-fight-physics-player-energy-2026-11-07.md)。
- [PR#285 合并后发鱼分布分析（2026-11-08）](analysis-pr285-fish-hunt-distribution-2026-11-08.md)：
  钩型嘬饵/四阶段大鱼狩猎/蓝冠天命。
- [PR#304/#305 合并后发鱼逻辑分析（2026-11-09）](analysis-pr304-pr305-fish-hunt-spawn-2026-11-09.md)：
  钩型确认修复 + 海图声呐狩猎。
- [rf4almanac 重量数据整理与可用性分析（2026-11-08）](analysis-rf4almanac-weight-2026-11-08.md)。
- [鱼口配置叠加/覆盖关系调查（2026-11-10）](后台配置对发鱼流程影响的调查.md)。
- [鱼的作用力对拍：官服抓包 vs 模拟器（2026-11-11）](根据实测包的鱼拉力修改后续建议.md)；
  后续方案见 [鱼拉力 P3 方案（存档，未开工）](鱼拉力P3方案-未测种覆盖与模型精化.md)。
- [升级礼物卡片机制（完整还原）](LEVEL_UP_GIFT_CARDS.md)。

## 工程约定

- [仓库入口](../CLAUDE.md)：硬约束（红线）与事实入口表，Agent 必读。
- [术语表](../CONTEXT.md)：跨模块共享术语，每条带反例。
- [工程工作流](agents/workflow.md)：快速路径与约束路径的分级、门禁级别。
- [变更记录约定](agents/issue-tracker.md)：`specs/changes` 目录与状态流转。
- [领域文档约定](agents/domain.md)：文档地图、证据等级、对账表。
- [ADR 0001](adr/0001-engineering-constraints.md)：为什么采用按风险分级的约束体系。

长期能力契约在 `specs/capabilities/`，一域一份（13 份）：协议契约、结算与终态、鱼口饵料与派鱼、
装备载荷与断损、钓组装配、管理端与热更参数、经济与发放、玩家状态条、成就、游戏内查鱼、
启动与进程装配、存储与幂等、工程质量。一次改动的规格在 `specs/changes/`。
