# rf4almanac 重量数据整理与可用性分析（2026-11-08）

## 数据来源

| 来源 | 内容 | 规模 |
|---|---|---|
| [rf4almanac](https://rf4almanac.pages.dev/#catches) | 社区鱼获逐条记录（英文名、重量、地点、饵、玩家、日期） | 90,622 条 / 251 鱼种（lastUpdated 2026-06-10） |
| [cn.rf4-stat.com/weight](https://cn.rf4-stat.com/weight/) | 中文名、奖杯/蓝冠线、站点历史纪录 | 254 鱼种 |
| 本库 `species_weight` | trophy_g / trophy_line_g / super_trophy_line_g / avg_g / min_g / max_g | 252 鱼种 |

almanac 数据经 `https://rf4-spots-worker.railzy5.workers.dev/catches` 获取（本机 workers.dev 被 DNS 污染，需走系统代理 127.0.0.1:34746）。

**来源构成**：`Weekly rec` 54,700 + `UL rec` 31,796 + `rf4stat` 4,126 —— **全部是纪录类上报**，没有普通渔获。这决定了它的统计语义（见下）。

逐鱼聚合结果（n/均值/中位/P10/纪录/分来源均值）与三源对照已写入同目录
`analysis-rf4almanac-weight-2026-11-08.json`（245/252 鱼种有 almanac 数据）。

## fish_key ↔ 英文名映射

almanac 用 RF4 官方英文名，cn.rf4-stat 的 slug 是俄语转写（vobla/vjun/barsch），
两者没有公共键。本次按 RF4 命名规则手工建立 252 键的英文名对照表，用
「档位线指纹」（库线与站点线完全一致）+ 中文名做交叉验证。

- 覆盖 **246/252**，无双射冲突
- 未映射：`roach`（库内无名，疑为 c.roach 旧重复）、`palia_lud`、`barb_toni`、`palia_kr`、`c_herring`
- almanac 侧未使用名（本库无对应种或存疑）：`Black-spined Herring`、`Char`、`Gray Char`、`Red Char`、`Siberian Char Loach`
- 已知易错点：本库中文与官方英文名存在系统性错位——`vjun`=泥鳅(вьюн=Loach) 不是 Vimba，
  `v_bream` 文鳊才是 Vimba；`e.pikeperch`=Zander（梭鲈）；`menek`=Cusk(менёк) 不是蓝魣鳕；
  `molva`=Blue ling、`m_molva`=Common ling；`crucian`=Gibel Carp(银鲫)、`c.carp`=Crucian Carp(金鲫)；
  `s.orfe`=Ide(圆腹雅罗鱼)。整理时已纠正。

## 核心问题：能否迭代 gamekee 补齐 avg_g？

**不能直接用均值补；p10 可作为低置信锚点。**

背景：`species_weight.avg_g` 当前**无任何运行时消费**（只有 `max_g` 被 `cafe_pricing.go:134` 使用）。
填它是为「avg 锚点」派鱼分布方案（见 analysis-fish-fight-physics 系列）备料。
库里 38 个有 avg_g 的鱼种全部来自 `passive/seed+passive`（本服渔获统计），gamekee 来源的 204 种完全没有均值。

实测对比（38 个有库 avg 且 almanac n≥30 的鱼种）：

| 指标 | 中位 | P25 | P75 | 结论 |
|---|---:|---:|---:|---|
| almanac **均值** / 库 avg_g | 3.22× | 2.40 | 4.65 | 偏差大且不稳定（0.8~8.2×），**不可用** |
| almanac **P10** / 库 avg_g | 1.19× | 0.95 | 1.54 | 关系稳定一个量级，可作低置信锚点 |

原因：almanac 样本全是纪录类上报（周纪录/UL 纪录/rf4stat 纪录），其均值是
「纪录级渔获的均值」，不是种群均值；且每鱼种的 UL/Weekly 样本配比差异极大
（分来源均值见 JSON `mean_by_source_g`，UL 纪录偏小、Weekly 纪录偏大），
比例系数无法跨鱼种复用。P10（纪录样本的下 10% 分位）接近「最小值得上报的鱼」，
与真实渔获均值的相关性显著更好。

若未来要给 204 个 gamekee 鱼种填 avg_g，建议公式：
`avg_g_est = almanac_p10_g / 1.19`，置信度标注为「低（±40%）」，
且必须与「sd 解绑」一起上（单独换锚点会被现有 sd 公式吃掉，见上一轮分析）。

## 派鱼上限交叉验证（附带收获）

almanac 每鱼种最大纪录 vs 我们采样上限 `hi = 蓝冠线×1.25`：

- 21 种纪录 > hi；其中 **>1.3× 的只有 3 种**：
  - `red_fish` 海平鲉：纪录 14,950g vs hi 5,000g（2.99×）——**库内档位线疑似错误**（蓝冠线 4,000g 对红鱼明显偏低），不是上限公式问题
  - `e.pikeperch` 梭鲈：纪录 20,000g vs hi 15,000g（1.33×）；`carp` 鲤鱼：49,880g vs 37,500g（1.33×）——与「官方按整数公斤设每鱼种上限、约蓝冠线×1.3~1.4」的模式一致（carp 纪录 49,880 ≈ 上限 50,000；zander 20,000 整）
- almanac 纪录普遍贴着「蓝冠×1.3」附近的整数，再次佐证上一轮 `sup×1.25 → sup×1.3` 的修正方向

**顺带修正上一轮结论**：cn.rf4-stat 的梭鲈纪录 3,498g 是错误的鱼种匹配（almanac 20,000g 为准），
上一轮「冲突B」里的 e.pikeperch 一条作废。

两站纪录交叉验证：244 个有种里仅 `kaluga` 差异大（rf4stat 782kg vs almanac 189kg，4.1×）——
样本深度差异（almanac 未观测到更大个体），非矛盾。其余鱼种两站纪录比值均落在 [0.67, 1.5]。

## 遗留

- 5 个 fish_key 未映射（roach/palia_lud/barb_toni/palia_kr/c_herring），量小，不影响统计
- `red_fish`（海平鲉）档位线错误是**新发现的独立数据问题**，应单独核对（同类的还有上一轮 s.orfe、crappie 家族）
- almanac 数据快照为 2026-06-10，站方仍在更新，重抓需走代理
