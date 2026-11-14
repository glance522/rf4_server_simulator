// Package fishmaps 是纸地图贴图名与数值标定的单一事实源：
// gameserver（查鱼区域扫描）与 admin（后台地图编辑器）共用，不依赖任何内部包。
// 标定换算与范围推导见 docs/fish-query-coordinates.md。
package fishmaps

import (
	_ "embed"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// PaperMap 纸地图数值标定（非图片内容）。客户端地图像素是
// 场景相对世界坐标 × Scale + Offset；保留客户端序列化的 float32 精度。
// 注意 Y 方向：客户端"图片位置"从底部往上量（Unity 纹理坐标）；导出位图
// 行号从顶部往下数，消费方换算 Z 轴时要做 Height− 翻转（X 轴不用）。
// 实机对账证据：2026-11-14 克马羚诺也湖 HUD 81:61 画区验证。
type PaperMap struct {
	OffsetX, OffsetZ, Scale float32
	Width, Height           float64
}

// Scene 一个内陆场景：地形原点与边长（世界单位）+ 纸地图标定。
type Scene struct {
	OriginX, OriginZ, Size float64
	Paper                  *PaperMap
}

// Scenes 只收录显示原点、TerrainData 范围与纸地图标定都已核对的场景；
// 群岛用外层主地形，内层地形包含其中不得重复扫描。TerrainData.size 已是世界单位，
// 不得再乘 Transform 缩放或旋转其 X/Z 范围。扫描取地形与纸地图交集，两者都不是水域掩膜。
// 未知地图不得继承默认值。见 docs/fish-query-coordinates.md。
var Scenes = map[string]Scene{
	"level_001_lake":            {Size: 600, Paper: &PaperMap{-745, -835, 5, 2048, 2048}},
	"level_002_kirzah_river":    {OriginX: -500, OriginZ: -500, Size: 1000, Paper: &PaperMap{-945, -1113, 4.2, 2048, 2048}},
	"level_003_viiveri_lake":    {OriginX: -500, OriginZ: -500.0000305175781, Size: 1000, Paper: &PaperMap{-1116, -1115, 4.24, 2048, 2048}},
	"level_004_torfyanoe_ozero": {Size: 400, Paper: &PaperMap{56, 37, 4.824, 2048, 2048}},
	"level_005_volhov":          {Size: 1000, Paper: &PaperMap{77, 89, 1.88, 2048, 2048}},
	"level_006_ladoga":          {Size: 500, Paper: &PaperMap{-102, 8, 4.22, 2048, 2048}},
	"level_007_grass_lake":      {Size: 500, Paper: &PaperMap{-108.4, -72.8, 4.54, 2048, 2048}},
	"level_008_sura_river":      {Size: 800, Paper: &PaperMap{61, 28.6, 2.445, 2048, 2048}},
	"level_009_ahtuba":          {Size: 1000, Paper: &PaperMap{62, 74, 1.91, 2048, 2048}},
	"level_010_belaya":          {Size: 500, Paper: &PaperMap{15, 145, 3.56, 2048, 2048}},
	"level_011_ladoga_02":       {OriginX: -500, OriginZ: -500, Size: 3000, Paper: &PaperMap{78, 80, .63, 2048, 2048}},
	"level_013_sev_don":         {Size: 1000, Paper: &PaperMap{-55, -45, 2.15, 2048, 2048}},
	"level_014_carp_lake":       {Size: 1200, Paper: &PaperMap{15, -185, 2.1, 2048, 2048}},
	"level_015_tunguska":        {Size: 1200, Paper: &PaperMap{80, 80, 1.57, 2048, 2048}},
	"level_016_yama":            {Size: 1500, Paper: &PaperMap{78, 74, 1.27, 2048, 2048}},
	"level_018_moskitnoe":       {OriginX: -128, OriginZ: -128, Size: 512, Paper: &PaperMap{-906.3, -884.7, 7.58, 2048, 2048}},
	"level_019_american_pond":   {OriginX: -127.4000015258789, OriginZ: -126.9000015258789, Size: 1000, Paper: &PaperMap{-619, -619, 3.29, 2048, 2048}},
}

// SeaPaper 挪威海海图的纸地图标定（与内陆同一客户端公式）。
// 来源：maps bundle 内 MonoBehaviour 原始字节（offset 48/52/80 布局），
// 2026-11-14 用声呐区 120/121 矩形投影回图验证精确命中。海图世界覆盖
// z ≈ −205 以南（271~273 等深水区）不在图上。
var SeaPaper = &PaperMap{OffsetX: 78, OffsetZ: 78, Scale: 0.38, Width: 2048, Height: 2048}

// paperMapLevels 客户端 maps bundle 里已核对存在纸地图贴图的 level key
// （上表 17 个内陆场景 + 挪威海海图）。tools/extract_paper_maps.py 按本表生成贴图。
var paperMapLevels = map[string]bool{
	"level_001_lake":            true,
	"level_002_kirzah_river":    true,
	"level_003_viiveri_lake":    true,
	"level_004_torfyanoe_ozero": true,
	"level_005_volhov":          true,
	"level_006_ladoga":          true,
	"level_007_grass_lake":      true,
	"level_008_sura_river":      true,
	"level_009_ahtuba":          true,
	"level_010_belaya":          true,
	"level_011_ladoga_02":       true,
	"level_012_north_sea":       true,
	"level_013_sev_don":         true,
	"level_014_carp_lake":       true,
	"level_015_tunguska":        true,
	"level_016_yama":            true,
	"level_018_moskitnoe":       true,
	"level_019_american_pond":   true,
}

// textureNameExceptions 客户端贴图名 ≠ "map_"+level 的已核对特例（仅两张）。
var textureNameExceptions = map[string]string{
	"level_002_kirzah_river":    "map_level_002_kirzah",
	"level_004_torfyanoe_ozero": "map_level_004_torfyanoe",
}

// TextureFor 返回 level 的纸地图贴图名（不带扩展名）；
// known=false 表示客户端资源无此图（调用方应降级，不得猜测文件名）。
func TextureFor(level string) (name string, known bool) {
	if !paperMapLevels[level] {
		return "", false
	}
	if n, ok := textureNameExceptions[level]; ok {
		return n, true
	}
	return "map_" + level, true
}

// SeaZoneRect 海图（挪威海）一个声呐区号的世界坐标矩形。
type SeaZoneRect struct {
	Zone int     `json:"zone"`
	X0   float64 `json:"x0"` // X 起点（左）
	Z0   float64 `json:"z0"` // Z 大边（上）
	X1   float64 `json:"x1"` // X 终点（右，不含）
	Z1   float64 `json:"z1"` // Z 小边（下，不含）
}

// SeaZoneGrids 解析内嵌声呐网格资产，返回按区号升序的海图区矩形。
// 与 gameserver/assets 的运行时副本同源（source_sha256 一致）；管理后台
// 用它把海图区号渲染成可点选的世界坐标网格。
func SeaZoneGrids() ([]SeaZoneRect, error) {
	var asset struct {
		Grids map[string][5]float64 `json:"grids"`
	}
	if err := json.Unmarshal(seaGridsJSON, &asset); err != nil {
		return nil, err
	}
	out := make([]SeaZoneRect, 0, len(asset.Grids))
	for id, g := range asset.Grids {
		i := strings.LastIndexByte(id, '_')
		if i < 0 {
			continue
		}
		n, err := strconv.Atoi(id[i+1:])
		if err != nil || n <= 0 || g[2] <= 0 || g[3] <= 0 {
			continue
		}
		out = append(out, SeaZoneRect{Zone: n, X0: g[0], Z0: g[1], X1: g[0] + g[2], Z1: g[1] - g[3]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Zone < out[j].Zone })
	return out, nil
}

//go:embed assets/sonar_grids_4_0_24799.json
var seaGridsJSON []byte
