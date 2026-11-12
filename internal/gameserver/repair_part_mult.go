package gameserver

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"sync"
)

// 按**部位**取维修倍率（2026-11-12 定案，玩家提供「各图装备维修参考图 2021-06-11」）。
//
// 背景：原来 map_price_multipliers.json 只有单一 `repair` 列（GameKee 599560
// 的整件维修倍率），一个地图一个数。玩家给的参考图把同一张地图拆成了六列：
//
//	鱼竿导环 / 装置 / 轴承 / 摩擦制动器 / 线轴 / 润滑油
//
// 而且规律是"装置与润滑油普遍比其它四部位低一档"（廓里湖 +102% / 装置+35%、
// 拉多加湖 +37% / 装置+25%、琥珀湖 +15% / 装置+10%、阿赫图巴·下通古斯卡·亚马
// +10% / 装置基础价）。只按一个数收钱，等于把同一只轮子上的齿轮（装置档）和
// 线杯（线轴档）按同一个倍率算，跟图上对不上。
//
// 因此数据层新增了 `parts`（每图六列）与 `brand_parts`（品牌专卖店六列），
// 本文件负责把它们解析出来、按品类归到六个部位上，再乘进"要发给客户端的
// 新件价"里。
//
// 为什么倍率必须乘在**新件价**上，而不是单独加一笔"维修费"：
// 客户端算报价的公式是 Σ(新件价 f2 − 旧件残值 f3)，服务端也按同一个差额收钱
// （见 workshop_official_price.go 的注释）。倍率只乘 f2、不乘 f3 会让客户端
// 复算出的价格和我们收的钱对不上（报价校验 5% 容差直接拒单，玩家看到
// 「发生内部错误」）。所以 f2 与 f3 必须**同源放大**——f2 = round(标价 × 倍率)、
// f3 = round(f2 × 残值率)，这样 (f2 − f3) 天然成比例，客户端复算自洽。
const (
	// tunableWorkshopPartMapMult 是部位倍率的总开关（1=启用，0=回到标价）。
	//
	// 留这个开关的理由：抓包里只有 **一单** 21/4（在 workshop_01 = 克马羚诺也湖，
	// 该图六列全是 1.00），所以"客户端自己在显示端也乘了一次 location_index"
	// 这件事无法从现有证据排除。若实机发现界面价是我们报价的 N 倍（也就是
	// 双乘了），把这个 tunable 置 0 就能退回"全局标价"，不需要重新编译。
	tunableWorkshopPartMapMult = "workshop_part_map_multiplier"
)

// workshopPartMapMultDefault 默认启用：图上的六列是玩家明确要求接进来的数据，
// 默认关闭等于方案 A 白做。实机验一次再决定是否长留。
const workshopPartMapMultDefault = 1.0

// partMultRecord 是 map_price_multipliers.json 里单张地图的维修倍率。
//
// `parts` / `brand_parts` 里的值用 *float64：JSON 里的 null 有语义
// （参考图写「无」= 该图没有维修点；品牌店 null = 该品牌不卖这一格），
// 用 0 会把"没有数据"和"倍率是 0"混在一起。
type partMultRecord struct {
	Parts      map[string]*float64            `json:"parts"`
	BrandParts map[string]map[string]*float64 `json:"brand_parts"`
	// 旧列保留作回退：图上没列出的地图、以及 parts 缺格时用。
	Repair       float64 `json:"repair"`
	RepairBeluga float64 `json:"repair_beluga"`
	RepairZeiman float64 `json:"repair_zeiman"`
}

var (
	partMultOnce  sync.Once
	partMultByMap map[string]partMultRecord
)

func loadPartMultipliers() {
	var doc struct {
		ByMapKey map[string]partMultRecord `json:"by_map_key"`
	}
	if err := json.Unmarshal(mapPriceMultipliersJSON, &doc); err != nil {
		partMultByMap = map[string]partMultRecord{}
	} else {
		partMultByMap = doc.ByMapKey
	}
}

// partKeyForCategory 把零件品类号归到参考图的六个部位之一。
//
// 参考图只有六个部位，而我们的零件品类有十来个，映射依据是"这个件属于轮子/
// 竿子的哪一块"：
//
//	5  导环           → guide   （图上独立一列）
//	7  轴承           → bearing
//	9  摩擦制动器     → drag
//	8  线轴/线杯      → spool
//	10 润滑油         → oil
//	52 主动齿轮       → device  （齿轮是传动机构的一部分，图上没有独立列，
//	                             归"装置"；它与电机/引擎同性质）
//	3  竿身 / 4 竿柄 / 6 渔轮本体 / 36 竿坯 / 34 摇柄
//	63 电机 / 81 引擎 → device  （整机/结构件，同为"装置"）
//
// 未知品类返回空串，调用方按"没有倍率"处理（乘 1）。宁可少乘，也不要把
// 一个没归类的品类胡乱塞进某个部位。
func partKeyForCategory(cat int64) string {
	switch cat {
	case 5:
		return "guide"
	case 7:
		return "bearing"
	case 9:
		return "drag"
	case 8:
		return "spool"
	case 10:
		return "oil"
	case 3, 4, 6, 34, 36, 52, 63, 81:
		return "device"
	}
	return ""
}

// partMultValue 取一格倍率，非法值（null / ≤0）返回 false。
func partMultValue(m map[string]*float64, key string) (float64, bool) {
	if m == nil || key == "" {
		return 0, false
	}
	v, ok := m[key]
	if !ok || v == nil || *v <= 0 {
		return 0, false
	}
	return *v, true
}

// partMultForMap 返回某地图某部位在某品牌店的倍率。
//
// 取值优先级（每一级都可能缺格，逐级往下掉）：
//
//  1. brand_parts[品牌][部位]   —— 品牌专卖店按部位取值
//  2. parts[部位]               —— 该图参考图的六列
//  3. 旧列 repair_beluga / repair_zeiman / repair
//  4. 1.0
//
// 第 1 级里 null 的语义是"该店这一格不单列"：
//   - 导环 null：截图显示品牌店的导环那一格是空的（不卖/缺货），退到第 2 级；
//   - 润滑油 null：Beluga 的轮油是**按轮型定价**（14.5 黑轮 +45%、16.5 鼓轮
//     +15%、大金轮 +0.5%），不是单一倍率，退到第 2 级取地图档（拉多加湖 1.25
//     / 琥珀湖 1.10）。这三个轮型加权下来大约 +20%，与该地图档同量级，
//     属于可接受的近似；哪天把油价改成按轮型取值，这里要跟着改。
func partMultForMap(mapKey, brand, partKey string) float64 {
	partMultOnce.Do(loadPartMultipliers)
	rec, ok := partMultByMap[mapKey]
	if !ok {
		return 1
	}
	if brand != "" {
		if v, ok := partMultValue(rec.BrandParts[brand], partKey); ok {
			return v
		}
	}
	if v, ok := partMultValue(rec.Parts, partKey); ok {
		return v
	}
	// 回退旧列：品牌列优先，与 repairMultForMap 的口径保持一致。
	switch brand {
	case "beluga":
		if rec.RepairBeluga > 0 {
			return rec.RepairBeluga
		}
	case "zeiman":
		if rec.RepairZeiman > 0 {
			return rec.RepairZeiman
		}
	}
	if rec.Repair > 0 {
		return rec.Repair
	}
	return 1
}

// workshopPartMapMultOn 读部位倍率总开关。
func (s *Server) workshopPartMapMultOn() bool {
	if s == nil {
		return false
	}
	return s.tunableF(tunableWorkshopPartMapMult, workshopPartMapMultDefault) > 0.5
}

// partMultiplierForStore 解析店铺 →（地图, 品牌）→ 该品类的部位倍率。
//
// storeID 是工坊 id（"workshop_01" / "belugaworkshop_006" …），来自
// 21/x 请求的 args(524)[0]；店铺不在 shop_repair_defs.json 里时用当前所在地图
// 兜底，倍率仍按该地图的六列取（否则玩家在没登记的工坊修理会突然全价）。
func (s *Server) partMultiplierForStore(storeID, fallbackMapKey string, cat int64) float64 {
	if !s.workshopPartMapMultOn() {
		return 1
	}
	partKey := partKeyForCategory(cat)
	if partKey == "" {
		return 1
	}
	// repairStoreDefs 由 loadRepairMultipliers 装填，这里先确保它跑过。
	repairMultiplierOnce.Do(loadRepairMultipliers)
	mapKey := fallbackMapKey
	brand := ""
	if def, ok := repairStoreDefs[strings.TrimSpace(storeID)]; ok {
		if def.MapKey != "" {
			mapKey = def.MapKey
		}
		// 一只店最多挂一个品牌（Beluga / Zeiman），取第一个即可。
		for _, b := range def.Brands {
			brand = strings.ToLower(strings.TrimSpace(b))
			if brand != "" {
				break
			}
		}
	}
	return partMultForMap(mapKey, brand, partKey)
}

// applyPartMult 把倍率乘到 (银, 金) 上。银币和金币同时放大，保证 partPrice
// 里"金币 = 银币/110"这个官方汇率关系不会被倍率拆开。
func applyPartMult(silver, gold int64, mult float64) (int64, int64) {
	if mult == 1 || mult <= 0 || math.IsNaN(mult) || math.IsInf(mult, 0) {
		return silver, gold
	}
	return int64(math.Round(float64(silver) * mult)), int64(math.Round(float64(gold) * mult))
}

// partPriceForStore 返回"发出去并被客户端拿来做差额运算"的那个新件价。
//
// 取值顺序（玩家 2026-11-12 定的口径：库里有值优先用库里的，库里没有走兜底）：
//
//  1. 后台覆盖行 workshop_part_price（该图精确行 > 全局行）——命中就原样用，
//     不再乘部位倍率：运营把价写死了，再乘一次就不是他填的数了；
//  2. 标价（item_price 静态价 > 属性推导 > partBasePrice > 500 兜底）
//     × 该店铺该部位的维修倍率。
//
// 覆盖层实现在 workshop_part_price.go；倍率在 repair_part_mult.go 上半段。
func (s *Server) partPriceForStore(ctx context.Context, storeID, mapKey, itemID string, category int64) (int64, int64) {
	silver, gold := s.partPrice(ctx, itemID, category)
	if os, og, ok := workshopPartPriceOverride(s.workshopPartOverride(ctx, itemID, mapKey), silver, gold); ok {
		return os, og
	}
	return applyPartMult(silver, gold, s.partMultiplierForStore(storeID, mapKey, category))
}
