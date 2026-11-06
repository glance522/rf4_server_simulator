# 领域文档约定

本文只管"某个问题该由哪份文档回答"。内容本身去各自文件。

## 文档地图（一题一主）

| 问题 | 唯一归属 |
|---|---|
| 要什么去哪找、硬约束 | `CLAUDE.md` |
| 这个词什么意思 | `CONTEXT.md` |
| 这个能力长期怎么工作 | `specs/capabilities/` |
| 这次改什么、在哪个接缝测 | `specs/changes/<slug>/spec.md` |
| 为什么当初这么决定 | `docs/adr/` |
| 日常流程、什么算小改 | `docs/agents/workflow.md` |
| 提交粒度、PR 要求 | `CONTRIBUTING.md` |
| 具体怎么排查协议 bug | `docs/debugging-workflow.md` |
| 模块划分、目录 | `docs/architecture.md` |
| 玩法数值与机制 | `docs/gameplay.md` |
| 部署与端口 | `docs/deploy.md` |

## 三条禁止

1. **禁止复制**：不要在 spec、能力契约、ADR、术语表之间复制同一段结论。要引用就给路径。
   代码符号、表字段、协议字段 op、测试断言由代码维护，文档不复述。
2. **禁止把"计划"写成"现状"**：`specs/capabilities/` 只描述**已实现**的能力。
   计划中的能力写进 `specs/changes/`，并标 `draft`。
3. **禁止在文档里放**：本机绝对路径、账号、token、玩家数据、抓包原始 hex、历史事故排查过程。

## 证据等级（引用结论时必须标）

| 标记 | 含义 | 本仓库的例子 |
|---|---|---|
| `[事实]` | 能指到代码行、日志原文或字节 | `docs/official-protocol-workshop.md` 的抓包表 |
| `[推断]` | 由事实推出，未直接验证 | "f1 应该是升级数" |
| `[假设]` | 待验证的猜想 | "f5 可能是首次钓获" |

无法给出 `[事实]` 的结论必须降级。三条判定规则：

- 没有调用点或读取点的代码不能作为依据（定义了不等于被使用）。
- 命名与注释不作证据（字段名 `personalRecordFlag` 不等于它真是新纪录标记）。
- "搜不到"不等于"没有"（反编译可能把访问编译成偏移量）。

## 对账（防漂移）

改了下面任一项时，同一次改动里同步对应文档，否则就是漂移：

| 改了 | 同步到 |
|---|---|
| 命令响应形状/args code | `specs/capabilities/protocol-contract.md`（若引出新规则） |
| 钓鱼、结算、奖励规则 | `specs/capabilities/fishing-and-settlement.md` |
| 表结构、迁移、幂等 | `specs/capabilities/store-and-idempotency.md` |
| 部署方式、端口 | `docs/deploy.md` |
| 玩法数值 | `docs/gameplay.md` |
| 新的高频陷阱 | 对应能力契约的 ✅/❌ 清单 |
