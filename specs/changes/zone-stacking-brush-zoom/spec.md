# 区域叠加语义 + 格子刷选 + 地图缩放

Status: done

## Problem Statement

管理员反馈三个问题：
1. 重叠区域只有一块生效（旧规则独占优先、同模式取 id 最小），被遮住的区**静默失效**——
   「画了没反应」，排查只能靠翻日志；
2. 只能画矩形，湾口、岸线等不规则形状框不准；
3. 地图固定 1:1 显示，画精细区域看不清。

## Solution

- **叠加语义**：每块画出来的区都生效——独占名单取交集（每个硬承诺都兑现，权重连乘，
  交集空=不出鱼）、增益区连乘（名单内 ×boost×weight、名单外 ×keep_others）、
  独占先过滤后增益在幸存者上继续乘。不再按 id 择一，无静默死区。
- **格子刷选（grid_cells）**：新区域类型，地图上按住左键刷 40m 格子（Alt=擦除），
  任意形状；存储为 fish_zone.cells_json（[[gx,gz],...]，上限 2048 格，迁移 00061）。
  判鱼、查鱼助手与 grid_rect 同口径（落点格在集合内即命中）。
- **地图缩放/平移**：滚轮缩放（光标锚定，上限 12×）、✋ 移动模式拖拽平移、⤢ 复位；
  视口模型=stage 窗口 + 2048px Inner 画布 transform。
- **重叠可视化**：被 ≥2 个启用区覆盖的格子铺红斜纹，图下注明格数——叠加语义下重叠
  不是错误，但应当看得见。

## Implementation Decisions

- `resolveFishZones`（复数）替代 `resolveFishZone`；`applyFishZoneRoster` 聚合
  （独占交集表 + 增益权重表预构建）；`fishZoneFactorForPoint` 查鱼口径同聚合。
- 审计日志 zoneHitNote 列出全部命中区（名字+模式，+ 连接）。
- store.FishZone 增加 `Cells [][2]int64`；grid_rect/sea_zone 完全不受影响；
  旧区数据无需迁移。
- UI：矩形拖拽与刷选并存（+ 矩形区 / 🖌 涂格子区）；cells 区按格渲染、
  面板提供「继续涂抹/清空」；所有绘制/命中数学经 zmapStagePos 走同一视口变换。

## Testing Decisions

- Go：`TestFishZoneStackingSemantics`（A独占∩B独占∩C增益 系数 6/0/1 + 判鱼只出 frog）、
  `TestFishZoneGridCells`（集合内/外命中）；既有 zone 测试改叠加期望。
- 期望值手算（3×1×2×1=6），非代码复算。
- cjs `zones_map_ui_test.cjs`：视口改造后拖拽/保存/海图/无图回归（w2pxZ 带 Z 轴翻转）。
- 回滚变红项：上述三个测试。

## Out of Scope

- 多边形自由绘制（格子刷选已覆盖任意形状需求）。
- 刷选区间的布尔运算 UI（合并/差集）。
- 海图区号的图形化（声呐区号几何未知，维持区号输入）。
