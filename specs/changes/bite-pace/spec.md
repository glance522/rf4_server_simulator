# 咬钩节奏：饵效控速（bite-pace）

Status: done

## Problem Statement

现状判鱼链里「饵好不好」只影响钓到什么鱼（鱼种权重、档位），不影响多快来鱼：

- `bite_probability` 默认 1.0，每一轮判定必出鱼；
- 路亚族（含海钓铁板 50-54）延迟窗 1.5~6 秒，抛竿几秒内必见鱼；
- 普通钓法延迟样本 6.48~46.6s 被 `bite_delay_max=20` 截断，上限约 20 秒；
- 延迟链只有天气、技能 BiteSpeed、钩型三个乘数，窝料只影响「选什么鱼」与死窝闸门，
  没有任何「打窝缩短来鱼时间」的通路。

玩家体验是「每一杆下去一定有鱼，路亚几秒一定有鱼」，与目标手感不符。

## Solution

新增 tunable 总开关 `bite_pace_enabled`（**默认 0 = 完全保留现有手感**）。开启后：

1. **饵效对口分**：每杆按钩上饵算一个 0~1 的连续分数，综合四类既有证据源
   （companion 档位、本图饵证据 `loc_bait_fish`、rf4records 实测占比、生态食性
   `diet_json`），全部复用现有缓存/表，无新表无迁移。
2. **三档节奏**：按分数分好/中/差三档，映射到延迟乘数与单轮判定概率
   （底层杆与路亚轮各一套概率参数）。
3. **底层杆**（非路亚族，含海底钓 55-60）：延迟采样不变，乘档位乘数后按档位
   等待区间 clamp（好 `[pace_good_min=6, pace_good_max=60]`、中 `[10,60]`、
   差 `[pace_bad_min=20, pace_bad_max=60]`）；海钓海底钓族 clamp
   `[3, pace_marine_max=30]`。差饵单轮概率 0.12，1 分钟内大概率静默空轮。
4. **打窝压缩**（仅底层杆，路亚豁免与死窝豁免同口径）：
   `groundPace = clamp(1 - pace_groundbait_gain × S × overdoseP, 0.35, 1)`，
   延迟再乘它，clamp 下限降为 3 秒；死窝（overdoseP→0）不加速。
   满窝好饵 ≈ 6.48~46.6 × 0.38 ≈ **3~18 秒**。
5. **路亚**：1.5~6 秒窗口、上限、落水保护全部不动（保住「14/14 不晚于收线窗口」
   的既有约束），只把单轮判定按档位概率掷骰：对口 p=0.3（平均 ~12 秒）、
   中 p=0.1、不对口 p=0.02（分钟级无口）。

## User Stories

1. 作为玩家，用不对口的饵时，1 分钟也不一定有口；换对口饵后明显变快。
2. 作为玩家，底钓/浮钓用对口饵，来鱼时间在 6~60 秒内随机分布。
3. 作为玩家，打了窝之后，对口饵的来鱼时间压缩到 3~20 秒。
4. 作为玩家，路亚挂对口拟饵，来鱼在几秒到几十秒之间随机；不对口则长时间无口。
5. 作为玩家，海钓（含海底钓族）配鱼柳等对口饵并选对吸引，来鱼在 3~30 秒级。
6. 作为管理员，关闭 `bite_pace_enabled` 后回到改动前的手感，无需回滚代码。

## Implementation Decisions

- 模块：`internal/gameserver`，新文件 `bite_pace.go`（分数、档位、延迟函数、窝料压缩），
  接线点 `fishing.go`（scheduleBiteState / biteDelayInWeather / onRodBiteTimerTicket /
  emptyWeatherBiteLocked）与 `fish_query_rate.go`（理论条/小时同步）。
- 对口分数据源（全部复用）：`cachedLocRoster`（名册）、`cachedBaitDefinition`（饵归类）、
  `cachedBaitTiers`（companion 档位）、`cachedLocalBaitEvidence`（本图证据）、
  `rf4BaitShare`（实测占比，自带 30 分钟缓存）、`fishEcology`（食性；
  名册级 max 食性权重按 statLoc 做 30 秒进程内缓存，与 tunCache 同风格）。
- 映射：tier3=1.0 / tier2=0.8 / tier1=0.6；本图证据 0.5；rf4 占比 ≥5%=0.9、≥1%=0.7、
  ≥0.2%=0.4；无证据基线 0.30（天然饵与拟饵同口径）；食性票 = 名册内该食性最大 diet
  权重（拟饵取 `diet_json.lure/2`，天然饵取食性键最大值 ×0.8 **并封顶 0.6**——家族
  对口只保证「不差」（mid 档），「好」必须由证据票给出）；未识别水域/未识别饵 →
  中性 0.5。拟饵与天然饵共用同一套三类证据票（companion/本图/rf4，审查 P2-3）。
- 多钩钓组按 `baitedHookSets` 的钩位分组（与判鱼/扣饵同一来源），每钩独立评分、
  整杆取最好一钩（审查 P2-5）。产品决策：与 `rollFishForRig` 的「任意已挂饵钩位都
  可能出鱼」同语义——哪口饵先出鱼，哪口饵决定节奏；不引入「选中主钩」概念
  （判鱼时钩位是随机洗牌的，没有稳定的主钩）。
- 名册食性聚合缓存按 statLoc 独立 30 秒过期（一张新图写缓存不复活其他图的过期
  条目），`fish_ecology` 全量重载（reload_tunables）与后台覆盖失效时整体清空
  （审查 P2-4）。
- 开关热关闭语义（审查 P2-1）：`bite_pace_enabled` 运行中改回 0 后，延迟、
  单轮概率、落水保护在**下一次判定/重排立即**回到 legacy（三处消费点都受
  当前开关门控，不依赖快照里的 `PaceKnown`）；已排定 timer 的绝对时刻不重排，
  到点后按 legacy 落水保护重新校验。
- 查鱼助手口径：按候选饵乘同一档位概率与底层杆 clamp，但**不含**
  `hookBiteDelayFactorForRig`（钩型咬口倍率）——查询构造的 state 没有竿实例，
  与既有 `meanBiteDelay` 口径一致，实际等待按钩型略有出入（约 ±10%）。
- 档位（tunable 可调）：good ≥`pace_score_good`(0.75)、mid ≥`pace_score_mid`(0.45)、
  其余为 bad；底层杆概率 `pace_prob_good/mid/bad`=1.0/0.5/0.12；路亚轮概率
  `pace_lure_prob_good/mid/bad`=0.3/0.1/0.02。
- 等待区间（tunable 可调，2026-11-21 起取代 pace_bottom_max）：底钓/浮钓按档位
  好 `pace_good_min/max`=6/60、中 `pace_mid_min/max`=10/60、差 `pace_bad_min/max`=
  20/60（默认值对齐原乘数模型的有效区间）；乘数 `pace_factor_mid/bad`=1.6/3.0
  （good 恒 1.0）保留为区间内分布形状微调。路亚轮 `pace_lure_min/max`=1.5/6
  （默认对齐原版收线短窗，pace_lure_max 仍受 bite_delay_lure_max 约束）。
- 延迟链：pace 分支只替换**非路亚族**的采样后处理（乘数 + clamp），采样分布、
  天气系数、技能、钩型倍率、`bite_delay_scale` 全部沿用；路亚族在 pace 模式下
  走原分支（零差异）。
- 概率合成：`onRodBiteTimerTicket` 里 `probability ×= st.PaceProb`，
  位于 `groundbaitBiteGate` 之后；失败空轮走既有 `emptyWeatherBiteLocked` 重排，
  重排延迟乘同一组 pace 因子（存 `FishState`，同 `st.Bonus` 先例）。
- 落水保护：pace 模式非路亚族 floor ×GroundPace，下限 `pace_groundbait_floor`=3s
  （仅打窝压缩时生效，无窝不动）。
- 恢复：`PaceKnown=false`（重启/重连）时在 timer 触发前惰性重算一次，
  防止重启后差饵鱼变快。
- 参数面：全部走 tunables（POST /api/tunables + reload_tunables 秒级生效）。
  管理后台（2026-11-21 SPA 改版后）：开关在「玩法规则 → 其他」；数值在
  「数值调整 → 咬钩」——三组等待区间（好饵/差饵/路亚 min~max）直接可见，
  中饵区间/档位线/分布乘数/开口率/海钓窗/打窝在「进阶」折叠区；
  旧版页（legacy.html）只保留开关。

## Testing Decisions

- 分数映射单测：companion tier / 本图证据 / rf4 占比 / 食性票（拟饵与天然饵）/
  拟饵 rf4 证据票 / 无名册中性 / 未识别饵中性。
- 档位映射单测：三档阈值边界与乘数、概率（底层杆与路亚轮两套）。
- 延迟单测：clamp 边界（bottom 60、marine 30、groundbait 下限 3）、
  满窝好饵落在 3~18s、死窝不加速、legacy 等价（开关关闭时输出严格落在
  旧通道 [6,20]）。
- 审查回归（PR #531）：热关闭后落水保护/概率立即回 legacy（`TestPaceHotDisableStopsFloorAndProb`）；
  补排重排保留 `PaceKnown` 且延迟仍在 pace 通道（`TestPaceReschedulePreservesKnown`）；
  名册食性缓存按图独立过期、ecology 重载清空（`TestPaceDietCachePerLocTTLAndReloadClear`）；
  多钩分组评分取最好（`TestPaceScoreForGroups`）；拟饵 rf4 证据票并入
  `TestBaitPaceScoreEvidence`。
- 接线测试：scheduleBiteState 落 pace 字段、空轮重排复用因子、
  概率乘点在 groundbaitBiteGate 之后。
- 既有锁定测试必须保持绿：`bite_floor_test.go`、`gear_physics_test.go`
  （officialPushDelay 样本）、`fish_query_rate_test.go`、`groundbait_test.go`、
  `fishing_timer_test.go`。
- 回滚后变红的测试：`bite_pace_test.go` 全部（新增行为）。

## Out of Scope

- 不改判鱼候选与鱼种权重——饵效只控节奏，硬性鱼口门 / strict 规则 /
  派鱼管线 / 档位采样全部不动；两套开关可独立组合。
- 不改 14/14 协议形状、推送链、票据防串杆、待结算持久化。
- 不改 `standardBiteDelay` / `lureBiteDelay` / `biteFloorDelay` 本体（legacy 路径零改动）。
- 不改窝料吸引权重（`applyGroundbaitWeight`）、死窝闸门、天气系数与天气预报。
- 不改路亚收线窗口与 1.5~6 秒判定节奏。
- 不做管理页 UI（只走 tunables API）。
