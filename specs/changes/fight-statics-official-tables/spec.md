# 搏鱼静态参数真值落表与习性组兜底（f9~f20/f5/f6）

Status: in-progress

<!-- 状态取值：draft | ready | in-progress | done | cancelled
     完成时原地改这里，不要归档、不要搬家、不要另开第二份。 -->

## Problem Statement

海钓/路亚连续出现"真脱钩"（客户端 f0=2 钩保持归零，2026-11-13 半小时 3 起，外加
f0=1 秒脱钩 2 起）。根因是发鱼参数缺口：海鱼种没有官方静态参数，27 对象 f9~f20
全部走 `fallbackFightStatics` 淡水小鲫鱼兜底表（按 94~405g 样本标定）——

- f14 搏斗强度：官方海鱼 20~25 vs 兜底 0.5（差 40~50 倍）→ 鱼拉不动线、零外冲 →
  收线期钩保持目标≈0 → 进度衰减归零离场（f0=2 主路径）；
- f15 动作幅度：官方 200~250 vs 兜底 4.0（差 6~60 倍）；
- f16 激进度/挣脱概率：官方海鱼 0.13~0.25 vs 兜底 0.834（挣脱门槛低 3~8 倍）；
- f5 每克体力池：官方海鱼 6.4~10.5 vs 未测种旧口径 2.388×Scale（差 ~2.5 倍）。

对照样本中脱钩鱼体力都没耗尽（menek 逃走时还剩 87%），是钩先掉——"体力 vs 钩保持"
赛跑输在钩保持。词表还有一处误判：piksha（海水鲽科）被 `fishFightArchetypeFor`
按 "pike" 子串归进淡水伏击组。

## Solution

发鱼时未实测鱼种按习性组拿到官方量级的搏鱼参数：海水鱼 f14≈22.5、f15≈225、
f16≈0.19、f5≈8.4/g，鲤科未测种 f14=1.0、f15=6、f16=1.0、f5=2.5/g；已实测的
8 个鱼种逐种落官方真值。鱼的表现与官服一致——海鱼有外冲、能拉弯竿，
搏鱼时长回到官方量级，f0=2 脱钩显著下降。

## User Stories

1. 作为海钓玩家，我希望中鱼后鱼会外冲、收线时张力有起伏（14/8 快照 f6 出现 0.3+
   脉冲、f3 收线累计回落），以便搏鱼过程与官服观感一致、不再被"无声脱钩"。
2. 作为路亚玩家，我希望 warm/agar 等未测种的挣脱概率与激进度对齐官方（f16 组均值
   0.19 而非 0.834），以便同杆同景的脱钩率与官服相当。
3. 边界：已实测种（P1/P2 覆盖的 f8/f5/f6 与本次落表的 f9~f20）行为不变；未命中
   任何组的鱼种维持现行 fallbackFightStatics（第三层，可回滚）；27 对象形状与
   字段 op 不变，只改值，客户端 4.0.25034 基线无需变动。

## Implementation Decisions

- 全部改动集中在 `internal/gameserver/fishing.go`（27 静态表 + f5/f6 组回退）与
  `internal/gameserver/fish_fight_profile.go`（词表），三层回退结构：
  **实测种（officialSpeciesStatics）→ 习性组（fishGroupStaticsFor）→
  旧 fallbackFightStatics（保持现状）**。
- A. 真值落表：`officialSpeciesStatics` 追加 8 种（g_carp/b_carp/carp/n.pike/
  merlang/piksha/taran/b_gill）。结构体新增 f16/f18 常数字段（官方逐鱼种常数）；
  既有 7 种显式补 f16=1/f18=1 维持现行为。f13/f14/f15/f17 按档 gradeMapVal；
  f9/f10/f11 多样本 pow 最小二乘拟合（g_carp 5 样本）、单样本存常数（p=0）。
- B. 习性组兜底：归组复用 `fishFightArchetypeFor` 词表（marine → 海水组、
  cyprinid → 鲤科组，其余组与未命中走第三层）。海水组 = merlang+piksha 两样本
  均值（鳕科/鲽科细分待 P3 锚点补抓）；鲤科组 f9 沿用 g_carp pow、其余取方案
  约值。f5 组回退：海水 8.445/g、鲤科 2.5/g（官方口径直接 rate×体重，不乘
  StaminaScale）；f6 组回退仅海水组 0.50。
- C. 词表修正：piksha 移出 ambush 关键词；menek/agar/warm/merlang 补进 marine
  （merlang 此前落 default 组，方案 §五B 归组明确其属海水组）。CurvePool 随
  marine 归 perch 池（现有海兜底池，302 主导形状与官方一致），不再落 crucian 池。
- f19 特例：仅 taran 有浮钓真值（0.00204695@611g，存常数）；其余落表种样本均为
  非浮钓 rig（官方非浮钓 f19 恒 1，buildFish27 已按钓法覆盖），种级 f19 留零、
  浮钓沿用 fallbackFightStatics 体重插值——浮钓族口径不回退。
- 数据与迁移：不改表结构、不写库；全部静态表驱动，回滚 = 还原对应表/词表条目。

## Testing Decisions

- 单元（`internal/gameserver/fish_fight_statics_test.go`，纯逻辑接缝）：
  - 官方锚点：8 新种 12 样本（19 条抓包证据库中 7 个既有种样本不落表——既有表
    f16/f17/f20 与新样本档位差 >2%，重校准属范围外；其 f8/f5/f6 已由 P1/P2 锚点
    覆盖）钉 f9~f11/f13~f18/f20，容差 ±2%（g_carp 行 ±3%：f9 五样本 pow 拟合
    最大残差 2.7%）。期望值 = 抓包解码真值，非同代码复算。
  - 组兜底：menek（海水组）钉 f9/f14/f16/f20 与 f5 每克 8.445、f6 0.50；
    karasekarp（鲤科组）钉 f14=1.0/f16=1.0/f5 每克 2.5；未命中种钉 f14=0.5
    （第三层不变）。
  - 词表：piksha/menek/agar/warm/merlang → marine（CurvePool=perch），
    e.pikeperch 仍 ambush、crucian 仍 default。
  - 回滚即红：还原任一表值/词表条目，对应锚点断言失败。
- 门禁：`go test ./...` + `go vet ./...` + `go build ./...` 输出贴 PR。
- 实机回归（判据，修完必须可见）：14/8 快照 f6 出现 0.3+ 脉冲、f3 收线累计出现
  回落（外冲实证）；同场景复测 f0=2 脱钩显著下降；搏鱼时长向官方量级靠。

## Out of Scope

- f0=1 构造期拉力×钩件比值校验路径（秒脱钩，需客户端逆向续作，方案 §七）。
- f12（n.pike 0.7/merlang 0.02）与 f7 不落表：既有代码恒 0，方案字段表未含，
  单独评估。
- 既有 7 种（bream/c.carp/c.roach/crucian/e.chub/perch/s.bream）的 f16/f17/f18/
  f20 重校准（新样本与既有表档位差 >2%，涉及档位建模，另行推进）。
- f8 K 表、曲线池直配、f6 逐鱼随机带、amp 列独立缩放（P3 既定范围）。
- 海水组细分为鳕科/鲽科（P3 锚点清单补抓后）。

## Further Notes

- `[事实]` 19 条官方 14/14 抓包（2026-11-10~13 三会话，`logs/captures/`）：
  official_trace/{float_14_14_16,44,92,118,177,216, lure_14_14_11,157,37,
  marine_14_14_56,87}.hex + diaodiao2/{dd2_1414_359,447,818,184,1163,950,44,1165}.hex。
  解码工具 `tmp_analysis/extract`（f5~f20 全字段）与 `tmp_analysis/grade`（f2 档位）。
  关键对照（官方 vs 兜底）：merlang 252g f14 25→0.5、f15 200→4、f16 0.252→0.834、
  f9 4.609→1.341；piksha 2975g f14 20→0.5、f15 250→4、f16 0.129→0.834；
  g_carp 21742g f9 2.391→钳位 2.047、f13 0.6→0.35。
- `[事实]` f0=2 脱钩的客户端机制链：收线中钩保持目标显式清零
  （kejihbadeej.cs:254-268），进度 ≤1e-45 置离场（lphjocjojcp.cs:338-365）；
  进度起点余量仅 0.22%（1−27.f20）。f16 影响挣脱概率与激进度、f20 影响挣脱节奏
  为团队既有结论（鱼拉力P3方案 §六）。
- `[推断]` 海水组取两点均值：鳕科两点（merlang 252g / piksha 2975g）体重差 10 倍，
  均值对中间体重的误差未知；P3 §四已列绿青鳕/单鳍鳕补抓为第三锚点。
- `[推断]` 单样本种（b_carp/carp/n.pike/merlang/piksha/taran/b_gill）f9~f11 存
  常数，体重偏离样本点时精度未知；spec 与代码注释均标"待补包改 pow"。
- `[事实]` 词表误判：`fishFightArchetypeFor("piksha")` 命中 ambush（"pike" 子串），
  实测为海水鲽科；menek/agar/warm 落 default（crucian 池）。
- 样本档位（f2 解码）：g_carp ×5 全 grade 2；b_carp/taran/n.pike/piksha grade 2；
  carp/merlang/b_gill grade 1。
