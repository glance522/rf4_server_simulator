# 变更记录约定（specs/changes）

本文只管"一次改动的 spec 放哪、怎么写状态"。工作流分级见 `docs/agents/workflow.md`。

## 目录

```text
specs/changes/<feature-slug>/spec.md     唯一主规格
specs/changes/<feature-slug>/tickets/    仅在工作超出一个上下文窗口时创建
specs/changes/_template/spec.md          模板，不参与状态流转
```

## 规则

- 一个**实质功能**一个目录，slug 用短横线英文（`fix-bite-bait-lifecycle`）。
- 主规格固定叫 `spec.md`，不建 `proposal.md` / `design.md` / `plan.md` / `tasks.md` / `delta.md`。
- 状态写在 spec 第一屏：`Status: draft | ready | in-progress | done | cancelled`。
- 完成时**原地改状态**，不 archive、不搬目录、不留第二份。
- 只有需要拆票时才建 `tickets/`，命名 `NN-<slug>.md`，每张票开头写 `blocked by:`（没有就写 `none`）。
- spec 结论与代码冲突时，**以代码为准**，并当场修 spec。

## 什么时候不建 spec

"小改"（定义见 `docs/agents/workflow.md` 第一张表）不建。改动本身就是记录。

## 与其他文档的分工

| 问题 | 归属 |
|---|---|
| 这次改什么、在哪个接缝测 | `specs/changes/<slug>/spec.md` |
| 这个能力长期怎么工作 | `specs/capabilities/` |
| 为什么当初这么决定 | `docs/adr/` |
| 这个词什么意思 | `CONTEXT.md` |
| 提交粒度与 PR 要求 | `CONTRIBUTING.md` |
