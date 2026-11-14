# 钓场与鱼口后台重构：纸地图拖拽画区 + 本图名册选鱼 + 页面重排

Status: done

## Problem Statement

上一轮区域名册（bite-gates-and-zones）上线后，管理员基本无法使用后台管理鱼口：

1. 划区域要在弹窗里手填 gx0/gz0/gx1/gz1 网格数字，与游戏地图毫无对应关系，
   管理员不知道自己在划哪块水域；
2. 区域选鱼用全鱼库通用搜索，看不到「这张图本来有哪些鱼」，无凭无据；
3. 「钓场与鱼口」页把规则开关、鱼汛模式、水域列表、鱼种表、区域表全部堆在一页，混乱。

## Solution

管理后台改用游戏客户端自带的纸地图（18 张 2048×2048 贴图随 launcher 内嵌，离线可用）：
「钓场与鱼口」页顶部合并为一张模式卡，编辑区分「鱼种名册 / 地图区域」双页签；
地图区域页在纸地图上叠加 40m 网格，按住拖拽即可画区域（自动吸附网格、悬停显示
游戏内「显示坐标」读数），点已有区域即可编辑；选鱼默认列出本图鱼种名册（带基础权重），
外来鱼种再切全库搜索。挪威海显示海图按声呐区号建区；无标定地图保留手填弹窗兜底。

## User Stories

1. 作为管理员，我希望在真实地图上拖拽画矩形并看到吸附的网格与显示坐标，以便精确对应
   游戏内点位划区。
2. 作为管理员，我希望区域选鱼默认展示本图名册（含基础权重），以便按地图实际鱼种组名单；
   需要外来鱼时再切「全库搜索」。
3. 边界：水域无别名或贴图缺失时显示「手动输入」兜底（原弹窗保留），不阻塞其他水域；
   拖拽不足 1×1 格视为点击；未保存切换水域/页签会先确认；海图（挪威海）不做网格拖拽，
   只按声呐区号。

## Implementation Decisions

- 新叶子包 `internal/fishmaps`：纸地图标定（原 `fishQueryRegionScenes` 17 场景整表搬出，
  字段导出）+ 贴图名名单（18 level，含 `level_012_north_sea`）与两处贴图名特例 +
  `TextureFor`。gameserver 用类型别名引用（`fishQueryRegionScene = fishmaps.Scene`），
  查鱼行为不变；admin 不 import gameserver（守住 mechanism-map 结构观察 9 的边界）。
- 贴图资产：`tools/extract_paper_maps.py`（UnityPy 1.25.4 离线工具，期望名单从
  fishmaps.go 源码解析、不自带常量）导出 WebP q80 到 `internal/admin/web/maps/`，
  随 `//go:embed all:web` 内嵌 launcher（约 +7.5MB）。与 fish-query-water.md
  「生成物不入库」条款的范围区分：水陆表是运行时数据，纸地图是管理端随包分发的静态资产。
- API：`/api/fish/areas` 每行追加 `level / map_kind(grid|sea|none) / texture / calibration`
  （中文名→level key 用新增 `store.ResolveLevelKeyByName`，查无静默降级 none）；
  区域保存 API（`POST /api/fish/zones`）与判鱼链路零改动。
- 前端 `internal/admin/web/index.html`：顶部「鱼口模式」卡（模式按钮+六开关两行）；
  编辑卡双页签；地图编辑器（img+三层 canvas、Pointer Events、坐标换算与
  docs/fish-query-coordinates.md 同源：图像像素=世界×scale+offset、网格=floor(世界/40)、
  显示坐标=(世界-原点)/5）；全部既有元素 id 保留（roster/弹窗字段/cjs 断言依赖）。
- 兼容：无迁移、无 schema 变化；gameserver 重建仅为别名改造（行为等价由既有 17 图
  1e-9 回归测试保证）；回滚 = 换回旧 launcher.exe/gameserver.exe。

## Testing Decisions

- `internal/fishmaps/fishmaps_test.go`：17 行形状守卫 + 标定抽查 + TextureFor 特例/海图/未知。
- gameserver 不新增测试：既有 `TestFishQueryRegionBoundsAndCancellation`（1e-9）等
  17 图回归搬家后全绿即行为等价证明。
- `internal/admin/fish_areas_enrich_test.go`：富化字段按文档公式手算期望
  （torfyanoe grid / north_sea sea / 无别名 none 三分支）。
- UI（惯例接缝 cjs，本地手跑不入 make test，需 `npm i --no-save playwright` +
  `RF4_TEST_BROWSER` 指向系统 Edge）：`zones_map_ui_test.cjs` 用 page.mouse 按
  标定公式换算屏幕坐标拖拽 world(85,85)→(195,155)，断言面板 gx 2:2~4:3、
  保存 POST 体、名册基础权重、点选既有区、海图/无图分支；期望值手算。
  `fishing_rules_ui_test.cjs` 同步修正过期断言（rule-item 计数 4→6、
  event 开关需先进参数页 event 子页）。
- 回滚变红项：fishmaps 形状/数值测试、admin 富化测试、zones_map_ui_test。

## Out of Scope

- 不改区域保存协议与判鱼/查鱼行为（bite-gates-and-zones 已定）。
- 海图不做网格拖拽（声呐区号语义不变）。
- 不做静态库区域预置、不做 rf4-stat 外链/在线地图源（客户端自带图已覆盖）。
- 水陆掩膜不参与画区（区域边界≠水域边界，与查鱼助手现状一致）。
