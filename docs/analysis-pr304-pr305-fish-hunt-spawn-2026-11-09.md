# PR#304/#305（钩型确认修复 + 海图声呐狩猎）合并后发鱼逻辑分析（2026-11-09）

基于当前 main `829ba97`，对
[glance522/rf4_server_simulator#304](https://github.com/glance522/rf4_server_simulator/pull/304)
（`0ce3092→c14b5bf→bdb3b26`，替代旧 #285）与
[#305](https://github.com/glance522/rf4_server_simulator/pull/305)
（#304 之上 + `aeb6468`）的代码级分析。
本文修正 [analysis-pr285-fish-hunt-distribution-2026-11-08.md](analysis-pr285-fish-hunt-distribution-2026-11-08.md)
中一条已过时的关键结论（覆盖面，见 §四）。

## 〇、结论速览

1. **覆盖面 100%**：#285 分析的「209 种缺达标线回退旧系统」已过时——`b974ff7`（11-04）
   已把 `fish_grade_lines` 补齐到 252/252 且有测试门禁；pr304/305 基线包含它。
   合并后**全部 252 个鱼种统一走四阶段发鱼**，无窝日常一律 74.7% 小于达标线。
2. **「发鱼 ≠ 中鱼」**：引入入口闸（solid/nibble）+ 保持率闸 + 蓝冠天命免检；海钓 14/9
   从无条件上鱼改为与普通 14/11 同规则——这是体感最大的变化，也是最大回归面。
3. **海图狩猎**：区号未知永远日常；进声呐区即为「聚鱼」（比淡水容易）；发窝/巨物
   在默认生态关下锁死，巨物即使生态开也数学不可达（Q 封顶 0.805 < 0.82）。
4. **合并面干净**：与 main 实测仅 `fishing_weather_damage_test.go` 一处冲突（来自 #310）；
   与已合并的 #301（零行为交叠）、#302（默认关，代码不重叠）无冲突，仅有 §九 所述的
   分布总管组合语义需要注意。

## 一、PR 拓扑与合并面

| 项 | 内容 |
|---|---|
| #304 | 修复重复 14/11 幂等、蓝冠概率归一化、日常不累计保底、脱钩补偿逐尾消耗；替代 #285 |
| #305 | 叠加在 #304 上：声呐鱼群 + 海图狩猎状态；#304 合并后按其文档声明会 rebase 去重 |
| 基线 | pr304 base `07d1718`（11-08 13:57），**已含 #301、#302**，不含 #310 |
| main | `829ba97` = `07d1718` + #310（22:36，测试并行化） |
| 实测合并 | `git merge-tree --write-tree 829ba97 pr305`：仅 `internal/gameserver/fishing_weather_damage_test.go` 内容冲突（#310 与 PR 各自改了同一测试文件），其余全部自动合并 |
| 默认开关 | `fish_hunt_enabled=1`（合并即对 uid>0 生效）、`fish_ecology_enabled=0`、`grade_ratio_enabled=0`、`sonar_echo=0` |

## 二、当前 main 的发鱼基线

1. `scheduleBiteState` 按天气/技能排咬口 delay → `rollFishForRig` → `rollFishFromDBWithRules`；
2. 体重：`sampleWeightDB` 对数正态标定 + `applyTrophyLuck` 重抽；档位：`rollGradeForRoll`
   按 q/t/b 三线判 grade；
3. 窝料：5 米格 + 相邻格按 `groundbait_diffuse_fall=0.40` 衰减，影响候选鱼种权重（×2.60 封顶）与开口率；
4. 中钩：普通 14/11、海钓 14/9 **无条件 `Hooked=true`**（`handlers_game.go`）——发鱼必中；
5. 无狩猎状态持久化；蓝冠只是体重自然越过蓝冠线的 grade4，无任何保护。

## 三、合并后发鱼链路总览

```
排口：delay × hookBiteDelayFactor(0.90~1.20，大钩更慢)
判鱼（uid>0 且 fish_hunt_enabled=1）：
  fishHuntReadiness ──淡水──→ 窝料圆形距离(0-4/7/10m) → A；食性亲和 → G；生态+饵效 → E
                    ──海图──→ 声呐区号/动作/水深/习性 → A、G；生态 → E（无饵效项）
  fishHuntWeights(stage, pity, 补偿, 钩型偏置, 蓝冠归一化) → chooseHuntGrade
  sampleHuntBand(q,t,b 三角对数带) → 体重；modeMult 只在带内移动（泄漏修复）
  recordHuntBite（ValidBites++ 落库）
中钩确认（14/11 或海钓 14/9，首次）：
  蓝冠天命 → 必中；否则 hookRetentionFails 抽保持率（失败降级 nibble）
  nibble → finishNibbleHook：扣一份饵(:nibble 键) + 清鱼实体 + 立即重排下一口
  solid  → Hooked + HookConfirmed=true → 正式扣饵(FishGUID 键)
  重复确认 → 幂等恢复，不重抽不重扣
终局：14/12 脱钩（仅已中钩）→ recordHuntEscape（3/4 尾 10 分钟补偿，逐尾消耗）
      14/4 结算 → recordHuntCatch（按 FishGUID 幂等推进 pity/消耗补偿/阶段折扣）
```

## 四、档位判定与覆盖面（对 #285 分析的修正）

### 4.1 「209 种缺达标线」的来历与过时原因

旧结论基于 PR#285 分叉点（11-04 之前）的库状态：三线只存在于 `species_weight` 静态 dump
的零散列（约 43 种齐）。`b974ff7`（11-04「补齐鱼种三色标重量线」）新增
`00027_fish_grade_lines.sql`，252 种全量 `(qualified_g, trophy_g, blue_trophy_g)`，
来源 russiabots（2026-10-26）并经 RF4.Help/NEREST/RF4DB 交叉校验。
**pr304/305 基线（07d1718）包含该提交。**

测试门禁 `TestFishGradeLinesCoverEverySpecies`（对真实静态库断言）：252 行、
`species_weight` 缺三线数 0、q<t<b 乱序数 0（另有 CHECK 约束兜底）。

本轮实测复核（解析迁移文件 + `fish_record_weights.json` 逐鱼种复算）：
- hunt 实际 q 线违例（t≤q 或 b≤t）：**0/252**；
- q 线来源：94 种走 Wiki 纪录绿旗 / 内置表，158 种按奖杯线 ×0.173 估算；
- 无贴边鱼种（q≥0.5t 为 0）。

**结论：252/252 全部进四阶段系统。** 回退路径 `sampleWeightDB` 只剩边缘情况：
不在 `species_weight` 的伪鱼种（GM 武装鱼）、`settle_scale_debug`、钓组上限逐档降级
全失败（属于「不可钓」而非缺数据；若又开了 #302 grade-ratio，回退会被比例表接管）。

### 4.2 合并后的档位分布（252 种通用）

| 阶段 | 小(grade1) | 达标 | 奖杯 | 蓝冠 | 触发 |
|---|---:|---:|---:|---:|---|
| 日常 | 74.7% | 24% | 1% | 0.3% | 无窝料 / 区号未知 |
| 聚鱼 | 54.5% | 43% | 2% | 0.5% | 淡水 A≥.25∧E≥.45∧G≥.25；海洋 E≥.25 |
| 发窝 | 30% | 61.2% | 6.8% | 2% | 淡水 A≥.55∧Q≥.60；海洋 Q≥.58 |
| 巨物发窝 | 10% | 64% | 20% | 6% | A≥.70∧Q≥.85（海洋 .82） |

与 #285 的行为差异（#304 修复，相对 main 即新口径）：

- **蓝冠概率与钩型解耦**：钩型只偏置 w1/w3，然后 `w4 *= biasedNonBlue/baseNonBlue`
  同比缩放，蓝冠最终概率恒等于阶段表值；硬保底（pity≥60 或 BigMiss≥2）跳过钩型偏置，
  仅按 w3:w4 原比例在奖杯/蓝冠间分派。
- **modeMult 不再泄漏**：饵效倍率经 `clampHuntBandWeight` 限制在已选档位 [lo,hi] 带内，
  不会把 grade2 顶过奖杯线；fallback 只应用一次倍率。
- **保底只由聚鱼以上推进**：`recordHuntCatch` 仅对 `HuntStage≥聚鱼`（发鱼时快照、随
  `fishing_persist.go` 持久化）的非奖杯结算 `Pity++`。节奏：≥30 软保底 w3×1.5、
  ≥50 ×2、≥60 或 BigMiss≥2 强制奖杯；奖杯结算保留 50% 发窝度（蓝冠 20%）。
- **补偿逐尾消耗**：大奖脱钩 → 3 尾（蓝冠 4 尾）10 分钟窗口，
  `consumeFishHuntCompensation` 每次有效结算递减（#285 只过期不递减）；
  未中钩的 14/12 不再计 escapes/补偿。

钩型入口/保持率公式与 #285 相同（`hookSolidHold` 未改），发窝阶段实测入口率沿用旧分析 §四：
#18 小钩 82%（奖杯入口 0.9%）、#10 大钩 71%（奖杯 8.3%）、#4 巨物钩 59%（嘬饵 41%）。

## 五、发鱼 → 中鱼转化（行为上最大的变化）

main 上「发鱼即中鱼」。合并后：

1. **入口闸**（发鱼时抽样 `BiteQuality=solid/nibble`）：r=鱼重/钩口比，r>0.8 solid 线性惩罚
   （最低 0.15），r<0.25 小入口惩罚；蓝冠（grade4）一律 `LuckProtected` 免检。
2. **保持率闸**（首次确认抽样一次 `hookRetentionFails`）：失败把 solid 降级 nibble。
3. **嘬饵收尾** `finishNibbleHook`：语义 = **鱼没咬上、饵被吃了**：
   - 不是「跑鱼」——`Hooked` 从未置位，不播报脱钩、不计 escapes、不进补偿；
   - 扣饵走与中钩**同一条** `consumeBaitEvent` 链、同一消耗规则：天然饵（category 27）
     **直接消耗 1 份**，部分拟饵只磨损耐久（-0.02/-0.005），假饵不耗；
   - 「轻耗」指事件轻量独立：事件键 `FishGUID+":nibble"` 与中钩键 `FishGUID` 分开，防重放重复扣；
   - 清掉本轮鱼实体、`castActive` 时立即 `scheduleBiteState` 重排下一口（竿不下水外）。
4. **确认幂等**：`HookConfirmed` 随待结算鱼快照持久化（JSON 字段，不新增列，旧档兼容）；
   重复 14/11、后续 14/9 只恢复状态，不重抽、不重扣饵、不丢已中钩鱼。
5. **海钓 14/9 与普通 14/11 同规则**：main 海钓抬竿无条件上鱼；合并后海钓同样可能
   嘬饵/保持失败。蓝冠天命鱼的钩型跑鱼帧（14/12 f0=1/2/8）被拦截重推最多 2 次
   （`LuckRepush`），f0=7 显式终态不重推。

## 六、咬口节奏与窝料圆形化

- **节奏**：`scheduleBiteState` 与 `emptyWeatherBiteLocked` 的 delay 乘
  `0.90 + 0.30×hookScore`——小钩快 ~10%、大钩慢 ~20%，发鱼频率首次与装备挂钩；
  嘬饵即时重排使大钩打小鱼时口更密但转化更低。
- **窝料**：5 米格+邻格 → **以真实落点为圆的 0-4m 全额 / 4-7m 100→60% / 7-10m 60→0，
  10m 外归零**；扣份按距离排序；投新窝距旧锚点 >20m 重置狩猎进度（`huntAnchorGroundbait`），
  同窝补料只续窝。抛点偏离窝点 10m 外 → A 恒 0 → 永远日常。

## 七、海图狩猎与声呐鱼群（#305）

### 7.1 模式分派

`marineHuntContext = marineRigType(50-60) || chartKnown(loc12_*)` → `fishMarineHuntReadiness`；
`fish_hunt_state` 增 `hunt_mode/sea_zone/sea_zone_known`（迁移 00050，旧分支迁移号
00046/00047 自动重映射到 00049/00050）。淡↔海切换、**声呐区号变化**清空进度；
区号未知只降置信不抹掉上次已知区号。

### 7.2 声呐鱼群（纯显示）

- `rosterSonarSchools` 用与真实判鱼同一套数据生成 2-4 组连续椭圆鱼群：水域名册选种、
  生态水层定深度带、`fishHuntWeights(当前阶段)` 作回波档位权重——探鱼器上的大小分布
  ≈当前阶段真实发鱼档位分布；第一群锚在玩家附近；
- 回波 f0 从体长 cm 改为 **kg**（客户端 3kg/10kg 图标阈值）；协议码 15004/15005 → **496/497**
  （main 上 15004 客户端无此类型，开 `sonar_echo` 会断线；#305 实机已验证）；
  空 cell/空响应发标准 `args(496)` 空数组，符合「数组恒发空数组」红线；
- 仅 `sonar_echo=1` 发回波（默认仍空网格）；查看声呐不落库、不推进 P/保底
  （sonar 路径只 load 不 save，已核实）；1/10 请求带真实抛点/竿位/钩型/动作/技能上下文。

## 八、数值可达性推演

E（环境）口径：淡水 `E = 0.70×eco + 0.30×饵效匹配`（tier3=1/tier2=.75/tier1=.5/无=.35）；
海洋 `E = 0.70×eco + 0.105`（**无饵效项**）。生态关时 env=1 → eco=0.394。

| 场景 | 日常 | 聚鱼 | 发窝 | 巨物 |
|---|---|---|---|---|
| 淡水 · 生态关 | 默认 | 对口饵 tier≥2 即可达 | 不可达（Q 需 G×(0.25+0.75P)≥1.04 > 上限 1.0） | 不可达 |
| 淡水 · 生态开 | 默认 | 易 | 可达（env≥1.5 + tier3 + 12 份窝料） | 可达（贴公式上限） |
| 海图 · 生态关 | **区号未知时永久** | **进区即达**（A≈0.55~0.61） | 不可达（Q 需 ≥1.69 倍上限） | 不可达 |
| 海图 · 生态开 | 默认 | 易 | 理论贴边（需 env≈2.0 且 G≈1、P≥0.63 ≈15 分钟+6~8 口） | **数学不可达**：E 上限 0.805 → Q 封顶 0.805 < 0.82 |

要点：**海洋阶段事实上的天花板是「聚鱼」**；区号（在声呐区内抛竿且有坐标）是海洋一切
阶段进阶的硬前提。海图玩家的实际收益 = 免窝料进聚鱼档（54.5/43/2/0.5）+ pity/补偿跨次累积。

## 九、与 #301、#302、#310 的关系

- **#301（鱼饵偏好页，已合并、在 pr304 基线内）**：只往 `fishing_bait_rules.go` 加只读导出
  （`ExportedBaitRules`/`ExportedBaitRuleCount`），不改判鱼行为；pr304/305 不碰该文件。
  零冲突零交叠。
- **#302（按比例出鱼获档位，已合并、在 pr304 基线内）**：`sampleWeightByGradeRatio`
  挂在 `sampleWeightDB` 内部，`grade_ratio_enabled` 默认 0。代码与 pr304/305 不重叠
  （merge-tree 干净）。**语义交叠**：两者是并存的「档位分布总管」——
  hunt 管 252 种（uid>0，默认开，走 `sampleHuntBand` 不过 sampleWeightDB）；
  grade-ratio 一旦后台开启就接管**所有**走 `sampleWeightDB` 的路径（uid=0 机器人、
  伪鱼种、hunt 的钓组不可钓回退）。若把 #302 分析里的「209 种保持现状分布」当作
  开 grade-ratio 的依据将不再成立。建议明确分工或让 grade-ratio 仅在
  `fish_hunt_enabled=0` 时生效。
- **#310**：唯一实测冲突 `fishing_weather_damage_test.go`（测试文件，手工合即可）。

## 十、风险清单与合并建议

1. **海钓「抬竿不上鱼」是最大回归面**（P0）：14/9 从必中变概率中，按
   `docs/client-checklist.md` 实机回归海钓全流程，重点看嘬饵后重排的 14/11 形状、
   鱼实体 GUID 变化、`HookConfirmed` 跨重连恢复。
2. **海图巨物发窝不可达**（P1）：文档四阶段表与实际可玩性的落差比淡水更明显
   （Q 封顶 0.805<0.82）；建议实机确认后调海洋 E 的 0.35 常数或 Q 阈值。
3. **双分布总管**（P1）：#302 grade-ratio 开启会覆盖回退路径与机器人分布，与 hunt
   并存时同一图两种分布逻辑各管一半，见 §九。
4. **蓝冠 1.6×b 硬顶**（P2）：仍略超官方纪录模式（1.3~1.4×sup），沿袭 #285 分析结论。
5. **性能**（P2）：每口一次 hunt state 读 + `recordHuntBite` 落库 + 窝料按 uid,loc 全量
   行扫描（距离过滤在应用层）；SQLite 量级可接受，长会话大账本时留意。
6. **协议兼容**（P2）：496/497 与空 cell 0x00 已实机验证；`fish_hunt_state` 已入
   `PlayerTables`（随玩家存档备份）；迁移号重映射有 `migration_stale_branch_test` 覆盖。
7. **合并操作建议**：合 #305 一个即可（含 #304 全部）；先解
   `fishing_weather_damage_test.go` 冲突并重跑 `go test ./... -count=1` +
   `go vet` + `go build`；合并后优先实测 §十.1/2 两项。

## 附：证据与复验方法

- 分支：`git fetch upstream pull/304/head:pr304`、`pull/305/head:pr305`（#301/#302 已在
  基线内，用合并提交 `e9e5ba9`/`f9da384`/自身提交 `0856fb8`/`37fdcf3` 核对）；
- 合并实测：`git merge-tree --write-tree 829ba97 pr305`；
- 覆盖复核：解析 `internal/store/migrations/00027_fish_grade_lines.sql`（252 条）+
  `internal/gameserver/assets/fish_record_weights.json`（93 条）+ 内置
  `speciesFightStatics`（4 条），逐鱼种按 `settleStatics` 口径复算 hunt 带 q<t/b：违例 0；
- 关键代码：`internal/gameserver/fish_hunt.go`（pr305 全文）、`fishing.go`、
  `handlers_game.go`、`groundbait.go`、`sonar.go`、`fishing_persist.go`、
  `bait_consumption.go`、`progress_ops.go:224`、`internal/store/repos_market.go:139`、
  `internal/store/migrations/00049/00050`；
- 规格文档：`specs/changes/fish-hunt-hook-retention/spec.md`、
  `specs/changes/marine-sonar-fish-hunt/spec.md`（pr305 分支）。
- 顶部阶段分布表继承 #285 分析的蒙特卡洛结论（4 万样本/场景）；PR304 未改
  `fishHuntWeights` 基准表、`sampleHuntBand`、`hookSolidHold`，仅改归一化/钳制/保底口径，
  已逐函数 diff 核实。
