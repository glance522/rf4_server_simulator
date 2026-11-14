# 食性证据旁路分级放行 —— 孤例不再满分，强证据保留

Status: ready

<!-- 状态取值：draft | ready | in-progress | done | cancelled
     维护者批准后改 ready，随实现 PR 推进。 -->

## Problem Statement

玩家实测现象 B：浮钓天然饵能成批钓到食性上是拟饵向的鱼种（鲑科/狗鱼/黑鲈等），浮钓与路亚没有鱼种分割。

根因（`filterFishBaitSet`，fishing_bait_rules.go 证据旁路段）：

```go
if allowed || evidence[j][key] > 0 || localEvidence[j][key] {
    preferred++
    score = 1.0 + 0.5*variation   // 命中即满分档（×1.0~1.5）
} else if b.legalAlternative(r) {
    score = maxBaitPreference * 0.05  // 合法替代档 ≈0.086
}
```

companion 周纪录证据**只要 >0 就给满分档**：不设样本量阈值、不分证据强度。官服 1 个人
用面团钓到过 1 条梭鲈（evidence=1），服务端就永久把"面团×梭鲈"按满分放行——与规则
allow 列表（攻略文本明确认定的食性）同级权重。本服 `loc_bait_fish` 捕获行同样 >0 即满分。

规则 allow 列表与 `legalAlternative`（同食性大类替代）两条通道不在本 spec 范围：
它们是攻略文本与食性大类的意志，且"常见狗鱼 allow 各类饵"这类宽放行是文本本身的
口径问题，另案处理。

## Evidence（本地库只读统计，2026-11-14）

companion_bait_fish 31231 行（来源见 [fish-water-layer-v4 spec 的 Evidence 段](../fish-water-layer-v4/spec.md)：
`migrations/00005_companion.sql` 随仓库首个提交 `d51ecb4` 预置，git 历史到此为止、无 provenance；
代码注释称"companion 周纪录饵效"。**evidence 字段语义是推断**——按量级（1~5371，与
"统计窗口内该饵×该鱼的捕获记录数"吻合）解读为样本计数，无文档证实）：

- 拟饵证据占比 ≥80% 的"拟饵向鱼种" 85 种；它们的天然饵证据行 570 条（另 19 条饵 id
  对不上 item_def），evidence 分布：

| evidence | 行数 | 占比 | 判定 |
|---|---|---|---|
| <5 | 199 | 35% | 孤例噪声（按推断语义：统计窗口内仅 1~4 条记录） |
| 5~19 | 170 | 30% | 弱证据 |
| 20~49 | 84 | 15% | 中证据 |
| 50~99 | 47 | 8% | 中证据 |
| ≥100 | 70 | 12% | 强证据（tier 3 为主，官服真实钓法） |

- **孤例是全表现象**：全表 evidence ∈ [1,4] 的行有 16725 条（54%）；天然饵证据行 5162
  条中 <5 占 2904（56%）。分级对全部走证据旁路的鱼生效，不只拟饵向鱼。
- **强证据是真钓法，不能一刀切**：蚯蚓 worm_01×黑鲈 581（tier3）、海沙蚕 fb_nereis×海鲈
  533（tier3）、饵鱼 c_bleak×梭鲈 481（tier3）——这些是官服公认的天然饵钓法，砍掉会
  把"浮钓上拟饵向鱼"矫枉过正成"天然饵钓不到任何掠食鱼"。
- tier 与 evidence 部分独立：60 行 tier3 但 evidence<50（tier 是官服侧档位排序信号，
  不纯由样本量推出）。分级条件取两者并集正好互补。

### rf4records 实测推翻"孤例=噪声"假设（2026-11-15，重要修正）

给 rf4records_catch 补天然饵/活饵映射（tools/rf4records_bait_map.py 37 条 + 组合饵分号
拆分）后 bait_id 命中率 36%→79%，重测 companion evidence 分档在官服真实渔获里的份额：

| companion 档 | rf4 9 天窗口渔获份额 |
|---|---|
| evidence≥100（原判"满分"） | **12.5%** |
| evidence 20~99（原判"中档"） | 16.5% |
| evidence<20（原判"孤例噪声"） | **57.1%** |

**结论：长尾饵是官服真实渔获的绝对主流，不是噪声。** companion evidence 是小窗口
累计样本（Perch 总证据 7814 ≈ rf4 12 天量级），绝对阈值与真实占比相关性弱。原提案
"evidence<20 降弱口"会把机制产出砍到与官服分布严重背离。

### 占比分布（正确的分档判据，227 鱼 / 37.9 万纪录）

| 该饵占该鱼渔获比例 | 官服渔获份额 |
|---|---|
| >5%（头部） | 58.8% |
| 1~5% | 21.5% |
| 0.2~1% | 11.9% |
| <0.2% | 7.8% |

每鱼 top1 饵占比均值 30.7%。实证对照：面团/粥/面包类×梭鲈 = **0/2649 = 0.00%**（companion
孤例在最新官方数据里完全消失），饵鱼欧鲌×梭鲈 = 9.78%（真实主流）。

### 交叉验证源：rf4records.com 官方周纪录管道（2026-11-14 已建成）

`tools/fetch_rf4records.py` + `rf4records_catch` 表（迁移 00060）：rf4records.com 为免费
公开站（robots 通用 UA `Allow: /`、Content-Signal `use=reference` 允许参考使用），API
`/api/records/filtered?fish=<名>` 返回官方周纪录**全量捕获纪录**（每条含饵名/重量/水域/
钓法/日期，`bait_display` 字段）。已抓 479,789 条覆盖 252 鱼全部、日期窗口 9 天
（源站滚动保留，管道重跑按主键幂等增量，本地历史随每次抓取积累）。鱼名→fish_key
映射 244/252（tools/rf4records_fish_map.py，未映射 14 个纪录占 5.3%）；饵英文名→
item_id 自动命中 36%（拟饵类高、天然饵与组合饵待手工补表，未命中 bait_id 留空、
按英文名聚合统计不受阻）。

该管道对 companion 数据的三点印证：

1. **provenance 推断升级**：rf4records 口径（官方周纪录窗口内捕获计数）与 companion
   evidence 语义、量级、"周纪录"注释全部吻合——companion_bait_fish 高概率为同源
   口径的预置快照（从 rf4-stat 系站点或官方 records 抓取），evidence 按"样本计数"
   解读的可信度从"纯推断"升为"高置信推断"。
2. **强证据对照吻合**：蚯蚓×黑鲈（White bass）companion evidence=581 vs rf4records
   9 天窗口 Nightcrawler 371 + Worm 319 ≈ 690 条——量级一致，581 的口径即
   "统计窗口内捕获记录数"。
3. **孤例噪声证实**：companion 里 evidence=1 的面团×梭鲈，在 rf4records 梭鲈 top8
   饵（Nasty Worm 318 / Bleak 259 / 路亚系列）中无面团——佐证孤例是噪声而非真实钓法。

分级阈值上线后，rf4records 管道成为**持续校准通道**：每周重跑，本表增量即"官方
周纪录窗口计数"，可直接对照 companion evidence 校准 strong/medium 阈值的窗口长度。

## Solution（v2：占比驱动，2026-11-15 修订）

`filterFishBaitSet` 的证据旁路从"命中即满分"改为**按实测占比分档**；规则 allow 列表、
`legalAlternative`、轮换、前置条件解析全部不动。

分档判据优先级：rf4records_catch 实测占比（`count(bait,fish)/count(fish)`，每周随管道
更新，TTL 缓存）→ companion tier/evidence（占比数据缺失时兜底）→ 本服 catch_log。

| 档位 | 条件（满足其一） | score |
|---|---|---|
| 满分档 | allow 匹配；或 rf4 占比 >5%；或 companion tier ≥3 / evidence ≥100 | 1.0 + 0.5×variation（现状不变） |
| 中档 | rf4 占比 1~5%；或 tier ==2 / evidence ≥20；或本服 catches ≥1 | maxBaitPreference×0.35 ≈ 0.6 |
| 长尾档 | rf4 占比 0.2~1% | maxBaitPreference×0.18 ≈ 0.31 |
| 碎尾档 | rf4 占比 <0.2%（含面团×梭鲈这类 0% 饵） | 走 legalAlternative 判定（无替代则排除） |

- **0.35（中档）的数据后验**：实测 1~5% 占比桶的渔获份额 21.5%，头部 58.8%，比值 ≈37%；
  0.6 ≈ 满分均值 1.25 的 48%，落在数据支持区间上沿——保留"能钓到但明显稀有"的体验
  余量，上线后按 catch_log 校准。原 v1 只凭档位几何拍脑袋，现为占比分布后验支持。
- 长尾档（0.31）与碎尾档（替代档 0.086）分离：<1% 桶合计仍占官服 19.7% 渔获，不能砍
  到零；但 <0.2% 的碎尾（含 0% 饵）才有资格降为替代档。
- 阈值走 tunables：`bait_share_strong`=5、`bait_share_medium`=1、`bait_share_tail`=0.2
  （百分比值，管理端可调免重启）；任一 ≤0 时旁路回到旧行为（>0 即满分），秒回滚。
- 只有满分档进 `preferred` 计数（多饵命中 ×1.15 加成不被中档稀释）。
- 按具体饵 id 计证据天然携带钓法属性：拟饵证据行只对"用那个拟饵"放行，天然饵不继承
  拟饵证据——不需要再引入钓法维度。

## User Stories

1. 作为浮钓玩家，用蚯蚓钓黑鲈、饵鱼钓梭鲈这类官服真实钓法照常成立（强证据满分保留）。
2. 作为浮钓玩家，面团×梭鲈这类孤例放行基本消失——要么按合法替代弱口偶尔中鱼，要么空竿。
3. 作为路亚玩家，拟饵证据放行不受影响（拟饵行的 evidence 普遍数百上千，全部在满分档）。
4. 边界：规则文本、allow/deny/bottomOnly、前置条件、轮换 soft/strict 语义、legalAlternative、
   派鱼档位与发鱼模式全部不动。
5. 边界：`bait_evidence_strong`/`bait_evidence_medium` 置 0 即回旧行为，无需回滚代码。

## 验收

- 单元测试（fish_bait_rules_test 层）：构造 evidence=1/20/100、tier 1/2/3、本服 catches
  0/1/3 的假 companion 行，断言三档 score 落位；开关置 0 断言回到旧行为。
- 部署后统计对比（不依赖实机专场）：`catch_log` 按 `tackle_cat`（浮钓族 vs 路亚族）×
  鱼种食性倾向（拟饵向 85 种名单）分组，对比上线前后"浮钓钓到拟饵向鱼"的占比变化；
  同时观察蚯蚓×黑鲈等强证据钓法仍在上鱼（防矫枉过正）。

## 门禁记录

- 实现：filterFishBaitSet 分级（fishing_bait_rules.go）+ rf4BaitShare 占比缓存（异步预热 TTL 30 分钟，判鱼零阻塞；表空/未迁移时 -1 退 companion 兜底）
- 单测 TestBaitEvidenceThresholdGrading：占比 95.6%/3.8%/0.6% 三档落位、tier3 满分 / tier2 中档兜底、无证据排除、开关置 0 回旧行为
- seedBaitRuleTest 默认关分级（旧测试验证规则匹配/替代路径），分级路径由新测试显式开启
- 全量回归与 vet/doccheck 输出随 PR 提交

## 风险与开放问题

1. **中档倍率 0.35 是拍脑袋初值**：过强会让浮钓全面哑火（中档鱼几乎不咬），过弱等于
   没分级。上线后按 catch_log 观察调节，tunable 免重启。
2. **companion 数据 provenance 已缓解但未闭环**：rf4records 管道把"纯推断"升为"高置信
   推断"（见 Evidence 交叉验证段），但仍缺导入者口径确认；阈值 tunable 化兜底语义漂移。
3. 规则文本宽放行（狗鱼"各类饵"）不在本 spec：若分级上线后浮钓仍能钓到明显不该出现的
   鱼种，问题在 allow 文本，另立 spec 修文本。
