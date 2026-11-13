# 摩擦片打滑速率拍平：删除逐档阶梯

Status: done

<!-- 状态取值：draft | ready | in-progress | done | cancelled
     完成时原地改这里，不要归档、不要搬家、不要另开第二份。
     2026-11-12 服主确认拍平方案后提交。 -->

## Problem Statement

打滑磨损率当前按 17/14 摩擦档位取六档阶梯（`reelSlipWearForSetting`：≤24 档 0.000001、
25~28 档 0.000025/0.00005/0.000075/0.000095、29 档 0.0001，后台 6 个 tunable）。2026-11-12
证据审查（用户裁定拍平）发现这套阶梯没有官服依据：

- 官服摩擦片磨损全部数据只有两个点（`docs/实测博鱼过程对装备磨损的影响分析.md` §5.2）：
  W5 = 112s 重度打滑磨 0.027%（≈0.00024%/s，当时泄力 19.49→19.70kgf、2.2× 过载）；
  W1 = 拉力仅超泄力 1.04× 零磨损。**没有任何会话对比过两个档位的磨损率**，W5 的档位号
  与轮型号文档也未记录，档位维度是单样本、不可辨识。
- W5 全程持续出线，而锁轮按定义不出线（`gear_physics.go` reelDragAtSetting 注释），
  实测点几乎必然不在锁轮档——把 0.00024%/s 锚到 29 档（0.0001/41.7 正好凑上）是数字
  自洽，不是归档事实。若实测档位其实是最大可调档（线上 28），当前 28 档速率低估约 105 倍。
- 25~28 阶梯是旧"按次计费"时代的硬编码遗产（specs/changes/reel-wear-tunables），逐档
  差异从未有官服对照；28→29 的 ~105× 悬崖在锁轮烧穿曲线删除后（wear-fight-time §5）已
  失去物理理由。
- wear-fight-time §5 对"过载比"维度已承认"单样本无法辨识，取平坦值"，档位维度同样是
  单样本、同样无法辨识，却保留了阶梯——同一证据等级、两种处理，不一致。

## Solution

- 打滑速率拍平：所有档位同一基准率，新 tunable `reel_slip_wear`（默认 0.0001）；
  每秒率 = 基准 ÷ `reel_slip_calibration_seconds`（缺省 41.7）≈ 0.00024%/s，对齐 W5 实测。
- 档位对磨损的全部剩余作用 = 触发侧 `reelDragAtSetting` 的 dragKg 曲线（决定打滑难易）；
  速率侧不再读档位。
- 摘除 6 个档位键（`reel_slip_wear_low/s25/s26/s27/s28/max`）：后台 UI 与 api.go 区间
  校验同步删除，历史存量 tunable 行不再被读取、不迁移（同锁轮烧穿键先例）。边界：
  旧脚本继续 POST 这 6 个键会被后台当作普通 tunable 入库（区间校验已随键删除），
  运行时不再读取；后台「摩擦片磨损」分组说明已点明停用，运营自定义过的档位值会
  静默回统一基准 0.0001。
- 保留：`reel_slip_calibration_seconds`（41.7）、`reel_slip_deadband_ratio`（1.05×）、
  轮油品质 0.8~1.2 与适配倍率——它们各自有独立实测或既有玩法层依据。

## User Stories

1. 作为服主，我希望后台只暴露有实测依据的校准参数，以便不会误以为档位阶梯有官服背书。
2. 作为玩家，我希望打滑磨损对齐官服唯一实测点（0.00024%/s 打滑时间），任何档位一致，
   以便模型诚实、行为可预期。
3. 边界：拍平后低档位打滑磨损从 ≈0 升到实测率（此前低档近乎免费）——这是拍平的直接
   后果，由 1.05× 门槛继续挡住非打滑段；接受。
4. 边界：`reel_slip_wear` 缺失、≤0、非数值、非有限值一律回落 0.0001，行为与硬编码一致。

## Implementation Decisions

- `reel_performance.go`：删除 `reelSlipWearForSetting` / `reelSlipWearForSettingDefault` /
  `defaultReelSlipWear25..28` / `reelSlipWearLow` / `reelSlipWearMax`；新增
  `reelSlipWearBase()`（读 `reel_slip_wear`，缺省回落 `reelSlipWear` = 0.0001）。顺带
  修正"锁轮另走发热曲线"过时注释（烧穿曲线已于 wear-fight-time 删除）。
- `reel_fight_wear.go`：`fightReelTickWear` 的 `slipRate` 改读 `reelSlipWearBase()`；
  头注释补记 2026-11-12 拍平裁定。
- 后台：`TUNABLE_DEFS` 删 6 键、加单键（区间 1e-12 ~ 1，沿用 float64 精度下限的既定
  口径）；"摩擦片磨损"分组收敛为 3 个字段。
- 测试：默认值表测试改为单一基准；锁轮同通道测试（`TestFightReelLockedUsesSameSlipChannel`）
  的"29 档应快于 25 档"断言改为"锁轮/未锁轮同速率"——这正是拍平的验收点；admin 区间
  测试换 `reel_slip_wear` 作代表键。
- 旧平率死入口 `damageReelSlip` / `degradeFrictionWasher`（无生产调用方，仅测试引用）
  不动，保持最小 diff。
- 回滚 = 还原阶梯函数、6 个键的后台登记与本 spec。

## 验收

1. 门禁：`go build ./...` + `go vet ./...` + `go test ./...` 全过。
2. 后台"玩法与钓组 → 摩擦片磨损"只剩 泄力打滑磨损率 / 打滑校准秒数 / 打滑门槛 3 个字段。
3. 实机：任意档位打滑，摩擦片磨损速率一致（改 `reel_slip_wear` 30 秒内生效、只影响后续结算）。
