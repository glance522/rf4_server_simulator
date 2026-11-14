# 收窄玩家状态对抛投距离的惩罚幅度

Status: done

<!-- 状态取值：draft | ready | in-progress | done | cancelled -->

## Problem Statement

玩家发现精力、饱食度会明显改变抛投距离，而且幅度偏重：同一根 2.6m 枪柄竿、同一套 8g 拟饵，仅把 `player_secondary` 从 0.464 补到 0.988，服务端理论抛距就从 20.40 米跳到 26.75 米（+31.1%），客户端实际上报落点同步从 26.82 跳到 32.61 / 34.93 米。也就是说状态掉一半，抛投距离直接少三成，量级盖过竿长、线杯档位、线径等装配差异；玩家换竿换饵时很难分辨"是装备起作用还是体力掉了"，上一轮 45→60 米基准标定的效果也被状态波动抵消掉了。

这不是状态机制不该存在，而是幅度从未单独标定：`specs/changes/player-condition-core/spec.md` 的 Out of Scope 明确把「抛投状态系数的标定：分母 0.85/0.9946 与 clamp [0.70,1.03] 维持现状」留白，那次只修了 `<=0` 的边界，没有按实机手感重标幅度。幅度又写死在代码里，玩家想调只能重新编译。

## Solution

把"状态缺口"从距离乘数改成可调强度：默认只按 0.35 缩放，状态掉一半时距离约少 8%（原来约 24%），空状态最多少约 10.5%（原来 30%）；满状态、超满状态和全部既有标定距离完全不变。强度走后台参数 `cast_player_state_penalty_scale`（0~1），0 = 状态完全不影响抛投，1 = 回到旧口径，改完最多 30 秒生效，玩家不必重编译就能自己试几档。

## User Stories

1. 作为玩家，我希望精力/饱食度下降时抛投只是略微变钝，以便换竿、换饵、换线杯带来的差异仍是手感的主导项。
2. 作为玩家，我希望补满状态后落点不会突然跳三成，以便连续抛投可预期。
3. 作为运维，我希望在后台调一个 0~1 的数就能改这个幅度，以便按实机手感反复标定。
4. 边界：满状态（0.85 / 0.9946）与超满状态系数仍为 1.0 / 1.03；状态为空后不再继续恶化（沿用旧的 0.70 有效下限）；无轮手竿、缺装配数据时仍走旧协议基线，不进修正器管线；旧档没有该 tunable 键时回退代码缺省 0.35。

## Implementation Decisions

- 模块：`internal/gameserver/cast_distance.go`。`castPlayerStateFactor` 保留为缺省入口，新增纯函数 `castPlayerStateFactorScaled(primary, secondary, scale)`：`state = 0.55×primary/0.85 + 0.45×secondary/0.9946`；`state >= 1` 走原奖励分支 `clamp(state, 1, 1.03)`；否则 `effective = max(state, 0.70)`，返回 `1 - clamp(scale, 0, 1)×(1 - effective)`。`scale = 1` 时逐点等于旧的 `clamp(state, 0.70, 1.03)`，旧口径是本模型的特例而不是被删除。
- 常量：`defCastPlayerStatePenaltyScale = 0.35`、`castPlayerStateEffectiveFloor = 0.70`、`castPlayerStateBonusCap = 1.03`。0.35 是自设计标定值，无官服样本锚定。
- 热更：`Server.castPlayerStatePenaltyScale()` 读 `tunableF("cast_player_state_penalty_scale", 0.35)` 并钳到 [0,1]，NaN 回缺省；经 `CastDistanceContext.StatePenaltyScale` 进入 `player_state` 修正器，同时写入 profile 供日志审计。context 零值 0 是合法的"关闭"语义，线上路径永远写入 tunable 读数；直接构造 context 的测试必须显式赋值（本次已补齐三处）。
- 日志：`rigops.go` 的「抛投距离计算」新增 `state_penalty_scale`、`state_factor` 两个字段，供 A/B 对照与后续再标定。
- 后台：`internal/admin/api.go` 的 `handleTunableSet` 对越界值回 400（gameserver 侧会静默钳制，入口先拦以免界面显示与实际手感不一致）；`internal/admin/web/index.html` 的 `TUNABLE_DEFS` 与「玩法与钓组」页新增「抛投状态」分组。
- 协议 / 数据：不改 14/1、14/2、158 任何字段形状与语义，不改表结构、不加迁移、不动静态库。tunables 是既有热更表，缺键回退代码缺省，旧档完全兼容。
- 兼容：客户端基线 4.0.25034。回滚 = 后台把该键设为 1（立即回到旧口径，免重编译），或删键后把代码缺省改回 1.0 并重新编译；不存在数据回滚。
- 本改动触及下发给客户端的物理口径，按 `docs/agents/workflow.md` 的约束路径执行。与 `specs/changes/cast-lure-nominal-calibration-probe/spec.md`（60 米基准）互不影响、可分别回滚。

## Testing Decisions

**接缝与期望值来源**

- 单元（纯逻辑，手算）：`TestCastPlayerStateFactorZeroStateUsesFloor` 期望空状态 `1 - 0.35×(1-0.70) = 0.895`、默认状态 1.0、超满 1.03。`TestCastPlayerStatePenaltyScale` 用 `primary = 0.85×ratio, secondary = 0.9946×ratio` 把加权 state 精确构造成 {0, 0.5, 0.8, 1.0, 1.2}，逐点断言 scale=1 回到旧口径 {0.70, 0.70, 0.80, 1.00, 1.03}、scale=0 全中性、缺省 0.35 为 {0.895, 0.895, 0.93, 1.0, 1.03}，另验 >1 / <0 的钳制。期望值全部手算，不用被测函数回算。
- 管线（纯逻辑）：`TestCastDistancePlayerStatePenaltyScale` 走完整 `calculateCastDistance`，断言满状态距离不随强度漂移、0.8 状态落差由旧口径 25% 收窄到 `1/0.93 - 1 ≈ 7.53%`。
- 后台（handler 级，真实 SQLite）：新增 `internal/admin/cast_state_range_test.go`，用登录态断言 {-0.1, 1.01, 5, NaN, Inf, abc, 空串} 回 400、{0, 0.35, 1} 回 200 且落库可读。
- 门禁：`go test ./...`（含 `internal/doccheck` 仓库断言）、`go vet ./...`、`go build ./...`、`go run ./tools/doccheck`，本次改动后全部重跑，不引用上一会话输出。
- 行为 / 实机一次（判定标准）：同一根竿、同一套线饵，分别在状态偏低与补满后各满力抛 2 杆，读「抛投距离计算」的 `state_factor` / `distance_m` 与 14/2 的 `max_m` / `requested_m`。基线为改动前同场景 `secondary` 0.464→0.988 时 `max_m` +31.1%。判定：`state_factor` 从约 0.76 / 0.99 变为约 0.916 / 0.998，落差从约 31% 降到约 9%，且满状态距离与改动前基本一致。
- 回滚后变红：上述三个抛投状态断言按旧口径会变红，属预期；标定带与高阶装备距离断言不受影响（满状态中性），应保持绿。

## Out of Scope

- 不改路亚名义基准 60 米（见 `specs/changes/cast-lure-nominal-calibration-probe/spec.md`）。
- 不实现风力/风向修正、不解析客户端蓄力字段、不启用 `cast_power` 修正器（`CastPower` 恒为 1 属既有半成品）。
- 不改 `rodTestWeightFactor` 的对数曲线、不改线杯 / 线径 / 轮型系数；饵重对抛距影响较弱属该曲线设计，另案处理。
- 不改 14/2 的 `requested_m` 校验、成就与资格判定口径。
- 不改状态条推进、恢复速率、精力上限、消耗品映射（`specs/capabilities/player-condition.md` 管辖）。
- 不写 proposal / design / plan / tasks 等旁支文件。

## Further Notes

- [事实] `specs/changes/player-condition-core/spec.md` 的 Out of Scope 写明「抛投状态系数的标定：分母 0.85/0.9946 与 clamp [0.70,1.03] 维持现状，只修 <=0 边界」；本改动接手这项留白。
- [事实] `specs/capabilities/admin-and-tunables.md` 要求「玩法可调参数一律落 tunables 表、gameserver 侧用 tunableF(key, 默认值) 读」，并明确反对「在判定分支里写死常量而不走 tunable → 下次调参必须重编译」；缓存窗口 30 秒，后台写入会投递 `reload_tunables`。
- [事实] 实机日志（2026-11-13 22:34–22:35，同竿 `b21a4a89`）`player_secondary` 0.464→0.988、`max_m` 20.40→26.75、`requested_m` 26.82→32.61 / 34.93；按旧公式复算状态系数 0.7588→0.9946（+31.1%），与距离变化完全吻合。证据链见 `specs/changes/cast-lure-nominal-calibration-probe/spec.md` 的实机结论段。
- [事实] 全量日志跨 11-11 / 11-13、5 根不同竿、基准改动前后，满力抛投 `requested_m / max_m` 稳定在 1.25～1.34，说明 `158.f1` 下发的抛距参与客户端落点计算。
- [推断] 0.35 让状态影响仍可感知（半状态约 -8%）但不再盖过装备差异（线杯档位单项可达 -28%）；这是设计取舍，不是官服数据。
- [未验证] 官服精力 / 饱食度对抛投距离的实测幅度，无抓包样本；本改动只保证本机可调、可回滚。
- [未验证] 缺省 0.35 的长时段实机手感；强度在后台可调，回滚方式见 Implementation Decisions。
