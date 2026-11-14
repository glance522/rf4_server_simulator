package gameserver

import (
	"strconv"
	"strings"
)

// 资格任务条件判定 helper。Counted 工厂把「一组布尔过滤 + 一个去重键」组合成
// 规则表里的一行；过滤全是纯函数，只读事件携带的本次数据，不查库。
//
// 拟饵分类按 item_id 前缀（item_def category=17 实测前缀）：spinner→旋转亮片、
// spoon→勺子路亚、crank/plug/shad→鱼形硬饵(wobbler)、topwater/popper→表水路亚、
// jerkbait→抽停硬饵。与成就系统 baitPrefix 同源，前缀匹配兼容复数形式
// （spinnerbaits/crankbaits/jerkbaits/poppers）。

// 拟饵分类优先按 item_def 的拟饵类型（mbemakijdeh）判族，类型未知时回落到
// item_id 前缀。前缀会误判少数族：X-Walker 系列 item_id 是 plug_w_*（看着像
// 鱼形硬饵），拟饵类型却是 5（水表），只按前缀会漏算「水表路亚」任务。
//
// 类型→族：1/20 旋转亮片、2 勺子路亚、4 鱼形硬饵、5/10 水表路亚、7 抽停硬饵。
var lureTypeFamilies = map[int64]string{
	1: "spinner", 20: "spinner",
	2: "spoon",
	4: "wobbler",
	5: "topwater", 10: "topwater",
	7: "jerkbait",
}

// lureFamilyOf 返回本尾鱼拟饵所属的族：拟饵类型优先，未知时按 item_id 前缀。
func lureFamilyOf(ev qualificationEvent) string {
	if fam, ok := lureTypeFamilies[ev.LureType]; ok {
		return fam
	}
	return lureFamily(ev.Bait)
}

// lureFamily 返回拟饵 item_id 所属的拟饵族；非上述硬饵/亮片族返回空串。
// 自制拟饵（hm_*）按子类型前缀归族：它们的 item_id 不带商店货的族前缀
// （hm_walker/hm_popper、hm_jerk、hm_spoon、hm_spinner、hm_pl/hm_crank/
// hm_shad/hm_minnow），不补映射会让「使用某类拟饵」的任务在手工饵上永不计数。
//
// 这是拟饵类型缺失时的兜底，不是权威判定；新代码请用 lureFamilyOf。
func lureFamily(bait string) string {
	switch {
	case strings.HasPrefix(bait, "hm_spinner"):
		return "spinner"
	case strings.HasPrefix(bait, "hm_spoon"):
		return "spoon"
	case strings.HasPrefix(bait, "hm_walker"), strings.HasPrefix(bait, "hm_popper"):
		return "topwater"
	case strings.HasPrefix(bait, "hm_jerk"):
		return "jerkbait"
	case strings.HasPrefix(bait, "hm_pl"), strings.HasPrefix(bait, "hm_crank"),
		strings.HasPrefix(bait, "hm_shad"), strings.HasPrefix(bait, "hm_minnow"),
		strings.HasPrefix(bait, "hm_j_minnow"):
		return "wobbler"
	case strings.HasPrefix(bait, "spinner"):
		return "spinner"
	case strings.HasPrefix(bait, "spoon"):
		return "spoon"
	case strings.HasPrefix(bait, "topwater"), strings.HasPrefix(bait, "popper"):
		return "topwater"
	case strings.HasPrefix(bait, "jerkbait"):
		return "jerkbait"
	case strings.HasPrefix(bait, "crank"), strings.HasPrefix(bait, "plug"), strings.HasPrefix(bait, "shad"):
		return "wobbler"
	default:
		return ""
	}
}

type qFilter func(qualificationEvent) bool

func qWeightAtLeast(g int64) qFilter {
	return func(ev qualificationEvent) bool { return ev.WeightG >= g }
}

func qWeightAtMost(g int64) qFilter {
	return func(ev qualificationEvent) bool { return ev.WeightG > 0 && ev.WeightG <= g }
}

// qRarityAtLeast 物种稀有度（loc_wiki_fish/species_rarity：0 普通 1 稀有 2 超稀有）。
// 仅用于文案明确说「鱼种」的任务（8603「罕见或极其罕见的鱼种」按 >=1）。
// 注意：文案里的「稀有鱼/trophy」是个体档位而非物种稀有度，见 qGradeAtLeast。
func qRarityAtLeast(n int64) qFilter {
	return func(ev qualificationEvent) bool { return ev.Rarity >= n }
}

// qGradeAtLeast 个体鱼档位（1 未达标 2 合格 3 奖杯 4 蓝冠）。
// 文案「trophy/稀有鱼（星）」按档位 >=3；「rare trophy/超稀有」按 >=4。
// 依据：官服 3/2 统计页稀有计数=grade>=3（statsbuild.go 抓包实证）；
// 英文任务文案为 "Catch a trophy Perch" 等——常见鱼种也有奖杯个体，
// 若按物种稀有度判，perch/atl_cod/halibut_w 等任务永远无法完成。
func qGradeAtLeast(n int64) qFilter {
	return func(ev qualificationEvent) bool { return ev.Grade >= n }
}

func qSpeciesIn(keys ...string) qFilter {
	return func(ev qualificationEvent) bool {
		for _, k := range keys {
			if ev.Species == k {
				return true
			}
		}
		return false
	}
}

func qBaitCategory(cat int64) qFilter {
	return func(ev qualificationEvent) bool { return ev.BaitCat == cat }
}

func qLocLevelIn(levels ...string) qFilter {
	return func(ev qualificationEvent) bool {
		for _, lv := range levels {
			if ev.LocLevel == lv {
				return true
			}
		}
		return false
	}
}

func qLureFamilyIn(families ...string) qFilter {
	return func(ev qualificationEvent) bool {
		f := lureFamilyOf(ev)
		for _, want := range families {
			if f == want {
				return true
			}
		}
		return false
	}
}

// qArtificialLure 人造拟饵钓组：本杆钓组属收索拟饵族（rigmeta.go:rigTypeLureFamily）。
func qArtificialLure() qFilter {
	return func(ev qualificationEvent) bool { return rigTypeLureFamily[ev.RigType] }
}

// jigRigTypes 汲钩钓族（铅头钩/软饵系钓组）。分支 2 文案里的「任何汲钩钓钓组」
// 覆盖 SimpleJig/Texas/DropShot/Carolina/ThreeWay/Wacky/HairyJig；2601 显式要求
// 「除了维基钩组」，说明维基本就属于该族。钓组中文名取自客户端本地化表。
var jigRigTypes = map[int64]bool{
	6: true, 7: true, 8: true, 9: true, 10: true, 11: true, 35: true,
}

func qJigRig() qFilter {
	return func(ev qualificationEvent) bool { return jigRigTypes[ev.RigType] }
}

func qRigIn(rts ...int64) qFilter {
	return func(ev qualificationEvent) bool {
		for _, rt := range rts {
			if ev.RigType == rt {
				return true
			}
		}
		return false
	}
}

func qDepthAtLeast(cm int64) qFilter {
	return func(ev qualificationEvent) bool { return ev.DepthCM >= cm }
}

func qFromShore() qFilter {
	return func(ev qualificationEvent) bool { return ev.IsShore }
}

// qFromBoat 船上钓（「在船上」）：与岸钓互斥，事件 IsShore 由 OnBoat 取反装配。
func qFromBoat() qFilter {
	return func(ev qualificationEvent) bool { return !ev.IsShore }
}

// rodPowerClass 取竿 item_id 尾缀的硬度档（UL/L/ML/M/MH/H/XH…）。
// 厂商命名即以此区分超轻/轻型；无尾缀（手竿/浮钓/底钓/海竿）返回空串。
func rodPowerClass(itemID string) string {
	i := len(itemID)
	for i > 0 && itemID[i-1] >= 'A' && itemID[i-1] <= 'Z' {
		i--
	}
	return itemID[i:]
}

func qUltralight() qFilter {
	return func(ev qualificationEvent) bool {
		if ev.RodPower == "UL" || ev.RodPower == "ULL" {
			return true
		}
		// 手竿等没有硬度尾缀，按测试上限 <=5g 视为超轻。
		return ev.RodTestMax > 0 && ev.RodTestMax <= 5
	}
}

// qLight 轻型渔具：含超轻（UL/ULL/测试上限 <=5g）与轻（L）。
func qLight() qFilter {
	ul := qUltralight()
	return func(ev qualificationEvent) bool { return ul(ev) || ev.RodPower == "L" }
}

// qLightSpinning / qULSpinning 轻型/超轻型纺车竿（竿型 1）。
func qLightSpinning() qFilter {
	light := qLight()
	return func(ev qualificationEvent) bool { return ev.RodType == 1 && light(ev) }
}

func qULSpinning() qFilter {
	ul := qUltralight()
	return func(ev qualificationEvent) bool { return ev.RodType == 1 && ul(ev) }
}

// qLureLenAtMost 拟饵长度上限（厘米）；「小拟饵」按不超过 4cm 判。
func qLureLenAtMost(cm float64) qFilter {
	return func(ev qualificationEvent) bool {
		return ev.BaitCat == 17 && ev.LureLenCM > 0 && ev.LureLenCM <= cm
	}
}

// qBaitClassIn 天然饵饵类（2/7=昆虫饵）。
func qBaitClassIn(classes ...int64) qFilter {
	return func(ev qualificationEvent) bool {
		for _, c := range classes {
			if ev.BaitClass == c {
				return true
			}
		}
		return false
	}
}

func qXPAtLeast(xp int64) qFilter {
	return func(ev qualificationEvent) bool { return ev.XP >= xp }
}

func qIsNight() qFilter {
	return func(ev qualificationEvent) bool { return ev.IsNight }
}

// qDrifting 顺水漂流：咬钩时客户端上报过 0x40 StreamMoving。
func qDrifting() qFilter {
	return func(ev qualificationEvent) bool { return ev.Drift }
}

// qLineMaterialIn 主线或前导线任一命中材质档（1 尼龙 2 碳线）。
func qLineMaterialIn(materials ...int64) qFilter {
	return func(ev qualificationEvent) bool {
		for _, m := range materials {
			if ev.MainLine == m || ev.LeaderMat == m {
				return true
			}
		}
		return false
	}
}

// qRodIn 竿型命中（博洛尼亚=4、竞赛=5、Picker=9、飞德=10）。
func qRodIn(types ...int64) qFilter {
	return func(ev qualificationEvent) bool {
		for _, t := range types {
			if ev.RodType == t {
				return true
			}
		}
		return false
	}
}

func qBaitPrefix(prefix string) qFilter {
	return func(ev qualificationEvent) bool { return strings.HasPrefix(ev.Bait, prefix) }
}

// qBaitSizeAtLeast / qBaitSizeAtMost 天然饵尺寸（毫米）。
func qBaitSizeAtLeast(mm float64) qFilter {
	return func(ev qualificationEvent) bool { return ev.BaitSizeMM >= mm }
}

func qBaitSizeAtMost(mm float64) qFilter {
	return func(ev qualificationEvent) bool { return ev.BaitSizeMM > 0 && ev.BaitSizeMM <= mm }
}

func qCastDistanceAtLeast(m float64) qFilter {
	return func(ev qualificationEvent) bool { return ev.CastM >= m }
}

// qWeightBelow 体重严格小于 g 克（「重量小于 50 克」），与 qWeightAtMost 的含等号区分。
func qWeightBelow(g int64) qFilter {
	return func(ev qualificationEvent) bool { return ev.WeightG > 0 && ev.WeightG < g }
}

// qFloatRig 浮子钓具/浮钓：本杆钓组属浮钓族（rigmeta.go:floatRigTypes）。
func qFloatRig() qFilter {
	return func(ev qualificationEvent) bool { return floatRigTypes[ev.RigType] }
}

// qBottomRig 水底钓具/底钓渔具：本杆钓组属沉底族（rigmeta.go:bottomRigTypes）。
func qBottomRig() qFilter {
	return func(ev qualificationEvent) bool { return bottomRigTypes[ev.RigType] }
}

// qMediumLight 中轻型竿（硬度档尾缀 ML）。
func qMediumLight() qFilter {
	return func(ev qualificationEvent) bool { return ev.RodPower == "ML" }
}

// qMediumLightOrLight 中轻/轻/超轻竿（ML/L/UL/ULL，含手竿测试上限 <=5g 的超轻兜底）。
func qMediumLightOrLight() qFilter {
	light := qLight()
	return func(ev qualificationEvent) bool { return ev.RodPower == "ML" || light(ev) }
}

// qLineStrengthAtMost 鱼线强度上限（公斤）；主线/前导线取已装配最小值。
// 强度未知（0）时拒绝计数：与 qWeightAtMost/qBaitSizeAtMost 等上限过滤一致，
// 无法核实「不超过 X 公斤」时不造假进度。
func qLineStrengthAtMost(kg float64) qFilter {
	return func(ev qualificationEvent) bool {
		return ev.LineStrengthKG > 0 && ev.LineStrengthKG <= kg
	}
}

// qBaitFlavorIn 天然饵味型命中（item_def acclagajcdn）。
func qBaitFlavorIn(flavors ...int64) qFilter {
	return func(ev qualificationEvent) bool {
		for _, f := range flavors {
			if ev.BaitFlavor == f {
				return true
			}
		}
		return false
	}
}

func qPassAll(ev qualificationEvent, filters []qFilter) bool {
	for _, f := range filters {
		if !f(ev) {
			return false
		}
	}
	return true
}

// qAllOf 把多个过滤组合成一个「与」过滤（用于嵌套 OR 条件）。
func qAllOf(filters ...qFilter) qFilter {
	return func(ev qualificationEvent) bool { return qPassAll(ev, filters) }
}

// qAnyOf 把多个过滤组合成一个「或」过滤（单尾二选一：A 或 B 命中即计）。
func qAnyOf(filters ...qFilter) qFilter {
	return func(ev qualificationEvent) bool {
		for _, f := range filters {
			if f(ev) {
				return true
			}
		}
		return false
	}
}

// qCountSpecies 过滤通过则按鱼种去重计数（「钓获 N 种鱼」）。
func qCountSpecies(filters ...qFilter) func(qualificationEvent) string {
	return func(ev qualificationEvent) string {
		if !qPassAll(ev, filters) {
			return ""
		}
		return ev.Species
	}
}

// qCountSingle 过滤通过则计一尾（单尾条件型，目标值通常为 1）；按本尾鱼 GUID 去重。
func qCountSingle(filters ...qFilter) func(qualificationEvent) string {
	return func(ev qualificationEvent) string {
		if !qPassAll(ev, filters) || ev.CatchGUID == "" {
			return ""
		}
		return ev.CatchGUID
	}
}

// qCountLureItem 过滤通过则按拟饵 item_id 去重计数（「用 N 种不同拟饵钓到鱼」）。
func qCountLureItem(filters ...qFilter) func(qualificationEvent) string {
	return func(ev qualificationEvent) string {
		if !qPassAll(ev, filters) || ev.Bait == "" {
			return ""
		}
		return ev.Bait
	}
}

// qCountBaitItem 过滤通过则按饵 item_id 去重计数（「用 N 种不同的鱼饵」）。
func qCountBaitItem(filters ...qFilter) func(qualificationEvent) string {
	return func(ev qualificationEvent) string {
		if !qPassAll(ev, filters) || ev.Bait == "" {
			return ""
		}
		return ev.Bait
	}
}

// qCountBaitFlavor 过滤通过则按饵的味道去重计数（「N 种不同味道的颗粒」）。
// 味道不能取 item_def.acclagajcdn：颗粒/煮饵整族都是同一档位（实测 25），
// 按它数永远到不了文案要求的种数。可区分味道的是饵本身——
// plt_cb_pch14/18/24 是同一味道的不同尺寸，去掉尾部尺寸数字后同键。
func qCountBaitFlavor(filters ...qFilter) func(qualificationEvent) string {
	return func(ev qualificationEvent) string {
		if !qPassAll(ev, filters) || ev.Bait == "" {
			return ""
		}
		key := strings.TrimRightFunc(ev.Bait, func(r rune) bool { return r >= '0' && r <= '9' })
		if key == "" {
			return ""
		}
		return "flavor:" + key
	}
}

// qCountRig 过滤通过则按钓组类型去重计数（「使用每种 X 钓组」）。
func qCountRig(filters ...qFilter) func(qualificationEvent) string {
	return func(ev qualificationEvent) string {
		if !qPassAll(ev, filters) || ev.RigType <= 0 {
			return ""
		}
		return "rig:" + strconv.FormatInt(ev.RigType, 10)
	}
}

// qCountLureFamily 过滤通过则按拟饵族去重计数（「用每种以下拟饵钓获稀有鱼」）。
func qCountLureFamily(filters ...qFilter) func(qualificationEvent) string {
	return func(ev qualificationEvent) string {
		if !qPassAll(ev, filters) {
			return ""
		}
		fam := lureFamilyOf(ev)
		if fam == "" {
			return ""
		}
		return fam
	}
}
