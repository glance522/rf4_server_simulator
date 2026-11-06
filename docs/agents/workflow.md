# 工程工作流（按风险分级）

本文只管"一次改动该走多重流程"。协议字段查法去 `docs/debugging-workflow.md`，提交粒度去 `CONTRIBUTING.md`。

## 两条路径

```
快速路径（默认）      核对当前事实 → 改 → 跑门禁 → commit
约束路径（显式触发）  写 specs/changes/<slug>/spec.md → 实现 → 双轴评审 → 回填状态
```

**默认走快速路径。** 高成本入口默认关闭，只有下面表里的情况才升级。

## 什么算"小改"（快速路径）

同时满足全部四条：

1. 一个包内（或跨包但只改调用点），行为边界清晰；
2. 不触碰协议契约、表结构、迁移、经济/结算/权限；
3. 能用现有测试或一条命令证明改对了；
4. diff 可在一个上下文窗口内读完。

例：修一个字段取错、补一条日志、修 launcher 托盘初始化顺序、补一个既有行为的测试。

## 什么必须走约束路径

| 触发条件 | 为什么 |
|---|---|
| 新增/修改命令响应形状、args code、字段语义 | 客户端强转失败会连锁崩，回滚成本高 |
| 改表结构、迁移、静态库内容 | 旧档升级与只读镜像，错了要人工救档 |
| 改经济、结算、奖励、剪线、断竿的幂等链路 | 直接对应刷物品/丢物品 |
| 改权限、鉴权、管理端接口 | 越权不可见，事后难发现 |
| 跨 3 个包以上的重构 | 无法单会话验证 |
| 引入新依赖或替换现有依赖 | 影响发布与体积/许可 |
| 不可回滚的数据变换 | 只能靠事前设计 |

以上任意一条成立 → 建 `specs/changes/<slug>/spec.md`（模板见 `specs/changes/_template/spec.md`）。

## 约束路径的最小动作

1. 写 `spec.md`：Problem / Solution / User Stories / Implementation Decisions / **Testing Decisions** / Out of Scope。
2. 实现时一次只做一件事；测试写在已约定的接缝上。
3. 完成后：`make test`（或 `go test ./...`）输出贴进 PR；同步更新受影响的能力契约；spec 状态改为 `done`。
4. 不新建 proposal / design / plan / tasks / delta 等旁支文件。

## 拆票时机

只有**工作超出一个上下文窗口**或**存在真实阻塞边**时才建 `tickets/`。
每张票声明 blocking edges；没有 blocker 的可以立刻开始；一次只做一张 frontier 票。

宽重构用 expand–contract：先并列加新形式 → 按爆炸半径分批迁移 → 无调用方后删旧形式。

## 门禁级别

| 级别 | 机制 | 覆盖 | 现状 |
|---|---|---|---|
| L1 | 本地钩子 | gofmt + build + vet + commit-msg | 可选，每台机器自启用 |
| L2 | `.github/workflows/ci.yml` | gofmt + build + vet + test | PR 触发，无 paths 过滤 |
| L3 | `CLAUDE.md` 红线 + 能力契约 + 评审 | 语义类约束 | 靠人 |

**gofmt 以 CI 判定为准**：Windows 检出为 CRLF 时本地 `gofmt -l` 会把每个文件判成未格式化；
CI 在 Linux 检出为 LF，判定才有意义。

## 完成前必须重跑

```powershell
go test ./...
go vet ./...
go build ./...
```

**旧证据作废**：上一个会话、上一个 commit 的输出不能作为本次完成的依据。
