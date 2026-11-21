# 饵效控速：路亚/海钓铁板动作感知（lure-action-pace）

Status: done

## Problem Statement

`bite_pace_enabled` 开启后，路亚族（含海钓铁板 50-54）的节奏只看**挂的什么饵**：
对口分是排 timer 时的静态饵分快照，延迟窗恒为 `pace_lure_min/max`（默认 1.5~6 秒）
且受 `bite_delay_lure_max` 封顶，节奏只靠 fire 时低概率掷骰
（`pace_lure_prob_good/mid/bad` = 0.30/0.10/0.02）。

后果（2026-11-21 用户实测反馈）：

1. 动作完全不影响来鱼速度——同一支 jerkbait 匀速收和狂抽停，来鱼节奏一模一样；
2. 「路亚最长等待」6 秒封顶让差饵/差动作也几乎每 1.5~6 秒掷一次骰，
   与底钓差饵「长时间才有鱼」的手感完全不同；
3. 「好的动作出来了才视为好的饵效」的玩法语义没有落点——动作只影响
   「出什么鱼」（判鱼链），不影响「多快来鱼」（节奏通路）。

## Solution

pace 开启时，路亚族的档位由**复合分**决定（乘法复合，用户拍板方案 A）：

```
复合分 = 饵分 × (0.4 + 0.6 × 动作分)
动作分 = clamp((lureActionTypeWeight(路亚类型, 当前动作) − 0.8) / 0.54, 0, 1)
无动作上报（刚落水、尚未收线）= 0.45（中性，不给好档）
```

- 饵分沿用既有 `st.PaceScore`（证据票 + 食性票，排 timer 时算好，一杆不变）；
- 动作分按「路亚类型 × 当前动作」实时计算（复用鱼猎阶段 `lureActionTypeWeight`
  的 19 类路亚表与归一口径）；
- 三档阈值沿用 `pace_score_good/mid`（0.75/0.45）。

三档行为：

| 档位 | 等待窗（秒） | fire 开口率 | `bite_delay_lure_max` 封顶 |
|---|---|---|---|
| 好 ≥0.75 | `[pace_lure_min 1.5, pace_lure_max 6]`（键不变） | `pace_lure_prob_good` **0.30→1.0** | 仍封顶（沿用） |
| 中 ≥0.45 | `[pace_lure_mid_min 10, pace_lure_mid_max 60]`（新键，默认对齐底钓中饵） | `pace_lure_prob_mid` **0.10→0.5** | 不再封顶 |
| 差 <0.45 | `[pace_lure_bad_min 20, pace_lure_bad_max 60]`（新键，默认对齐底钓差饵） | `pace_lure_prob_bad` **0.02→0.12** | 不再封顶 |

- **fire 动态取档**：`onRodBiteTimerTicket` 与 `biteDelayInWeather` 都用
  `st.PaceScore`（饵分快照）+ 当前 `st.LureAction/LureType` 现算复合档位——
  装排后动作变差的轮次按低开口率空掷，随后空轮重排按差档长窗重排。
- **动作改善重排**：14/7/14/9 更新动作后，复合档位**升高**时立即按新档重排
  timer（只升不降 + 2 秒防抖 `PaceRearmAt`）——好动作一出来即进 1.5~6 秒短窗；
  恶化不重排（由 fire 动态开口率自然处理）。
- 天气 × 技能 BiteSpeed × `hookBiteDelayFactorForRig` × `bite_delay_scale` ×
  0.8s 地板 × 1.2s 落水保护全保留；pace 关闭回 legacy 逐位一致。

## 契约修约（约束路径）

`specs/capabilities/bite-bait-and-spawn.md` §9 原「路亚族延迟链零改动 /
❌ 放宽路亚延迟窗」条目按本次改动修约。原担忧「鱼在收线停顿/收回到位后到达，
与客户端行为系统打架」由既有机制兜住，修约后不变量为：

- 14/14 只允许在饵仍在水中（`st.InWater`，fire 时检查）时到达；
- 「收回到位」由 14/4 收竿感知（`fishingEndSettle` Stop + `CastActive=false`），
  timer 不过夜；
- 票据机制（`castGeneration`+`biteTicket`）使任何重排天然作废旧 timer；
- 收线停顿/水柱悬停/水底静置是合法咬口姿态（客户端动作枚举已知），
  长窗 fire 落在这些状态是正常表现。

本次修约由维护者（项目所有者）2026-11-21 直接提出需求并拍板方案，视为已确认。

## User Stories

1. 作为玩家，对口饵配对口动作（如 jerkbait 抽停、spinner 匀速、铁板跳底），
   1.5~6 秒内来鱼。
2. 作为玩家，饵对口但动作一般，来鱼落到 10~60 秒的中档；做出对口动作的
   一瞬间等待窗立即缩短。
3. 作为玩家，差饵或明显不对口的动作，和底钓差饵一样长时间才有偶发慢口。
4. 作为玩家，好动作做到一半停下，之后的轮次大概率空掷（fire 按当前动作降档）。
5. 作为管理员，`bite_delay_lure_max` 在 pace 模式下只约束好档；中/差档有
   独立的等待区间与开口率参数可调。
6. 作为管理员，关闭 `bite_pace_enabled` 后路亚手感与改动前逐位一致。

## Implementation Decisions

- 核心新逻辑在 `bite_pace.go`：`paceLureActionQuality` / `paceLureCombinedScore` /
  `paceLureWindowFor` / `paceLureProbForTier` / `paceLureStateFor` /
  `paceLureActionRearm`（防抖常量 `paceLureRearmDebounce = 2s`）。
  路亚轮概率默认值收敛到 `paceLureProbForTier` 单一来源，`paceTierFor` 委托之。
- 接线点：
  - `fishing.go`：`FishState` 新增 `LureType`（14/1 播种）与 `PaceRearmAt`
    （均仅进程内）；`biteDelayInWeather` 路亚分支按复合档位取窗，tier<2 时
    cap 传 0 绕过 `bite_delay_lure_max`；
  - `handlers_game.go`：14/1 播种/清空 `LureType`；14/7、14/9 解锁后调
    `paceLureActionRearm`（函数内部自持锁并在锁内复核全部守卫）；
  - `fish_bite_environment.go`：`lureDiveForGear` → `lureCoreForGear`
    （泳层 + 类型名一次取回，类型取最深拟饵——多拟饵钓组动作权重按主导拟饵计）；
  - 查鱼同源：`fish_query_rate.go` 的 `meanBiteDelay` 路亚积分窗改由调用方
    按复合档位传入（好档保留封顶、中/差档 cap=0），`fishQueryPaceState`
    路亚族用复合分（假设动作沿用 `fishQueryMotion` 的 drift/jig 口径）。
- 动作分与饵分的时标差异：饵分（含 `LureType`）是排 timer 时快照，
  动作是实时状态。**换饵 mid-cast 不刷新**两者——与既有 `PaceScore`/
  `LureDive` 口径一致（14/1 才重置），不是本次引入。
- 重排守卫（锁内复核）：pace 开、路亚族、`PaceKnown`、`CastActive`、
  `InWater`、`!NeedBite`、`!Hooked`、`!Resolved`、`Pending==nil`、
  `!PushSent`、`pushing==nil`；`armBiteTimerLocked` 票据机制使与
  `emptyWeatherBiteLocked` 的重排互斥安全。
- 日志：`咬钩节奏` 增 `action`/`combined` 字段；重排打
  `路亚动作改善，重排咬钩`。
- 参数面：新键 `pace_lure_mid_min/max`、`pace_lure_bad_min/max`；
  改默认值 `pace_lure_prob_good/mid/bad` = 0.30/0.10/0.02 → **1.0/0.5/0.12**
  （旧值 DB 行若存在仍生效，需后台清行或改值）。SPA「数值调整 → 咬钩」
  新增路亚中/差档区间四项；开关说明同步动作语义；旧版页同步开关与
  路亚上限提示。

## Testing Decisions

- `TestPaceLureCombinedTier`：复合分档位映射（好饵+对口动作→好、好饵+中性/
  无动作→中、中饵+对口动作到不了好档、好饵+不对口动作、差饵恒差档）。
- `TestPaceLureProbFollowsAction`：fire 动态开口率（1.0/0.5/0.12）、
  `PaceKnown=false` 中性、热关闭回 1。
- `TestPaceLureActionRearm`：改善重排进短窗、降档不重排、Pending 守卫、
  热关闭不重排。
- `TestPaceDelayBoundsAndLureUnchanged` 改写：pace off 逐位 legacy；
  pace on 好/中/差窗越界断言；`bite_delay_lure_max` 只封好档、中档绕过。
- `TestMeanBiteDelay` 扩展 `lure-mid-window` 用例；`TestFishQueryDelayUsesWaterOverride`
  适配新签名。
- 既有锁定测试保持绿：`bite_floor_test.go`、`fish_query_rate_test.go`、
  `bite_pace_test.go` 其余用例。

## Out of Scope

- 不改判鱼候选与鱼种权重——动作仍只通过判鱼链（action_json /
  `lureActionTypeWeight`）影响「出什么鱼」，本次只是让节奏通路也看动作。
- 不改海底钓族 55-60（已有档位窗 3~30s，挂天然饵为主，动作语义不适用）。
- 不改 14/7/14/9 协议形状与动作位映射（`classifyLureMotion` 位表不动）。
- 不改非路亚族的 pace 行为（延迟链、打窝压缩、落水保护全部沿用）。
- 换饵 mid-cast 不刷新 `LureType`/`PaceScore`（既有口径，非本次引入）。
- 硬性鱼口门（bite_gate_*）与节奏通路保持互相独立，不合并。
