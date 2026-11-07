package gameserver

// 窝料成分与逐鱼种食性亲和（spec specs/changes/groundbait-system/spec.md）。
//
// 分两步算，免得「配方」和「鱼口」两套口径互相漂移：
//  1. 原料表 → 食性向量：按实际投的原料加权，再按食性族归一成百分比；
//  2. 食性向量 × 鱼种生态食性 + 攻略吃饵表 → 食性契合度与专食性。
//
// 具名商品窝料的配方本来就是固定的，成分直接随资产发布；自制窝料没有自己的
// item_id（产物一律叫 feedcompleted），只能按玩家实际选用的原料现算，这样
// 「红虫+蛆蛹的冬配」和「玉米+蜂蜜的夏配」才会真的吸引到不同的鱼。

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
)

var (
	groundbaitFishNamesOnce sync.Once
	groundbaitFishNames     map[string]string
)

//go:embed assets/groundbait_material_def.json
var groundbaitMaterialJSON []byte

// groundbaitFamilies 窝料能勾到的食性族。鱼种生态表里还有 lure/plankton 这类
// 窝料勾不到的族，它们只参与「这条鱼最执着的那一口」的比较。
var groundbaitFamilies = []string{"plant", "worm", "crustacean", "boilie", "fish"}

// groundbaitFamilyKeywords 攻略吃饵词 → 食性族。关键词口径与
// fishing_bait_rules.txt 里的中文吃饵词条同源（离线探针 gb_probe2.KW）。
var groundbaitFamilyKeywords = map[string][]string{
	"worm":       {"蠕虫", "蚯蚓", "红虫", "苍蝇", "蛆虫", "毛翅", "树皮甲虫", "昆虫", "地龙", "土蚕", "水蛭", "蜉蝣", "襀翅", "牛虻", "草蜢", "蟋蟀", "蜣螂", "金龟", "龙虱", "蝼蛄", "浮游幼虫"},
	"plant":      {"面包", "面团", "粥", "玉米", "豌豆", "坚果", "奶酪", "水果", "蜂蜜", "奶油", "椰子", "中立", "自然风味", "菜叶", "马铃薯", "藻类", "奶渣", "大麦"},
	"boilie":     {"煮饵", "煮成饵", "boilie", "popup", "pop up"},
	"crustacean": {"螯虾", "磷虾", "虾", "蟹", "贻贝", "钩虾", "水蚤"},
	"fish":       {"饵鱼", "活饵", "鱼肉", "鱼块", "鱼片", "鲑", "吞拿", "鱿鱼", "鱼子", "虾仁"},
}

const (
	// 原料在食性向量里的话语权：基底最有分量，添加剂次之，香精只算点缀。
	groundbaitMixWeight      = 1.0
	groundbaitAdditiveWeight = 0.8
	groundbaitAromaWeight    = 0.35

	// 双票阈值：攻略吃饵表没列这一族时，只有生态食性表里这一族本来就高
	// 才算「打折的吃」（0.85），两票都不认才真削弱（0.50）。
	// 窝料是「吃」不是「钓」，攻略没写不等于不吃。
	groundbaitAcceptSoft    = 0.85
	groundbaitAcceptPenalty = 0.50
	groundbaitEcoSoft       = 1.15
)

// groundbaitNeutralMaterials 是说不清用料时的中性配方。商品「干混合」在游戏里
// 就是一条空配方（recipe 表里没有原料行），离线探针也用同一组原料标定它。
var groundbaitNeutralMaterials = []string{"mix_combi", "feedadds_breadcrumbs", "feedadds_pelets", "aroma_honey"}

// groundbaitCarrierMaterial 是只负责把饵包住、本身不该参与食性计算的载体。
// PVA 棒的关键在棒里装了什么，水溶网本身不是鱼饵，不能按“植物料”去吸引鱼。
func groundbaitCarrierMaterial(id string) bool {
	return id == "hvz_pva_material"
}

// groundbaitMaterial 是一种原料的食性与物理特性档案。
type groundbaitMaterial struct {
	ItemID   string             `json:"item_id"`
	Diet     map[string]float64 `json:"diet"`
	Bulk     float64            `json:"bulk"`
	Disp     float64            `json:"disp"`
	Particle float64            `json:"particle"`
	Scent    float64            `json:"scent"`
	Hold     float64            `json:"hold"`
}

// groundbaitProfile 是一团窝料算出来的食性向量与物理特性。
// diet 已按食性族归一成百分比（加起来 1.0），所以两种料只要 diet 接近，
// 对同一条鱼的契合度就接近，不会因为原料表里换了个近义词就翻脸。
type groundbaitProfile struct {
	Diet       map[string]float64
	Potency    float64
	Dispersion float64
	Particle   float64
	Scent      float64
	Hold       float64
}

// groundbaitMaterialWeight 按 item_id 前缀给原料分级。原料命名本来就是官方客户端
// 带的（mix_* 基底 / feedadds_* 添加剂 / aroma_* 香精），不额外维护清单。
func groundbaitMaterialWeight(id string) float64 {
	switch {
	case strings.HasPrefix(id, "mix_"):
		return groundbaitMixWeight
	case strings.HasPrefix(id, "aroma_"):
		return groundbaitAromaWeight
	}
	return groundbaitAdditiveWeight
}

// groundbaitFallbackMaterial 给没登记的原料一档保守档案：按前缀猜成品类，
// 宁可当成普通素食料，也不能因为原料表缺一行就让整团窝料凭空失效。
func groundbaitFallbackMaterial(id string) groundbaitMaterial {
	m := groundbaitMaterial{ItemID: id, Bulk: 0.8, Disp: 0.8, Particle: 0.3, Scent: 0.4, Hold: 1.0}
	switch {
	case strings.HasPrefix(id, "mix_"):
		m.Diet = map[string]float64{"plant": 0.8}
	case strings.HasPrefix(id, "feedadds_"):
		m.Diet = map[string]float64{"plant": 0.6}
	default:
		m.Diet = map[string]float64{"plant": 0.2}
		m.Scent = 0.7
	}
	return m
}

// profileOf 按实际用料合成一团窝料的档案。mats 为空时退回中性配方，
// 与离线探针的口径一致（空配方就是商品干混合那档）。
func (d *groundbaitDefs) profileOf(mats []string) (groundbaitProfile, bool) {
	return d.profileOfSeen(mats, map[string]bool{})
}

// profileOfSeen 允许 PVA 棒把一袋成品窝料当填充料再合成一次。
// 递归时带着 seen 防止商品配方互相引用形成环。
func (d *groundbaitDefs) profileOfSeen(mats []string, seen map[string]bool) (groundbaitProfile, bool) {
	if d.badErr != "" {
		return groundbaitProfile{}, false
	}
	if len(mats) == 0 {
		mats = groundbaitNeutralMaterials
	}
	weighted := map[string]float64{}
	var bulk, disp, particle, scent float64
	hold := 1.0
	foodEntries := 0
	for _, id := range mats {
		if groundbaitCarrierMaterial(id) {
			continue
		}
		if d.materials[id].ItemID == "" && !strings.HasPrefix(id, "mix_") &&
			!strings.HasPrefix(id, "feedadds_") && !strings.HasPrefix(id, "aroma_") {
			if def, ok := d.itemDef(id); ok && !seen[id] {
				seen[id] = true
				child, ok := d.profileOfSeen(def.Materials, seen)
				delete(seen, id)
				if ok {
					if len(child.Diet) == 0 {
						continue
					}
					for fam, w := range child.Diet {
						weighted[fam] += w
					}
					bulk += child.Potency
					disp += child.Dispersion
					particle += child.Particle * child.Potency
					scent += child.Scent
					hold = math.Max(hold, child.Hold)
					foodEntries++
					continue
				}
			}
		}
		foodEntries++
		weight := groundbaitMaterialWeight(id)
		spec, ok := d.materials[id]
		if !ok {
			spec = groundbaitFallbackMaterial(id)
		}
		for _, fam := range groundbaitFamilies {
			weighted[fam] += spec.Diet[fam] * weight
		}
		bulk += spec.Bulk * weight
		disp += spec.Disp * weight
		particle += spec.Particle * spec.Bulk * weight
		scent += spec.Scent * weight
		hold = math.Max(hold, spec.Hold)
	}
	total := 0.0
	for _, fam := range groundbaitFamilies {
		total += weighted[fam]
	}
	if total <= 0 {
		// 非空但只有 PVA 载体这类惰性材料：档案是已知的空饵，不该退回固定商品表。
		return groundbaitProfile{Diet: map[string]float64{}, Hold: 1}, true
	}
	diet := map[string]float64{}
	for _, fam := range groundbaitFamilies {
		if weighted[fam] > 0 {
			diet[fam] = weighted[fam] / total
		}
	}
	// 香精不占「一份料」的数：disp 按实际料数取均值，scent 按总条目数取均值，
	// particle 按 bulk 归一（碎屑多但体积大的料不算细粉）。这一段与探针同源。
	n := 0
	for _, id := range mats {
		if !strings.HasPrefix(id, "aroma_") && !groundbaitCarrierMaterial(id) {
			n++
		}
	}
	if n == 0 {
		n = 1
	}
	return groundbaitProfile{
		Diet:       diet,
		Potency:    math.Round(bulk*100) / 100,
		Dispersion: math.Round((disp/float64(n))*100) / 100,
		Particle:   math.Round((particle/math.Max(bulk, 0.01))*100) / 100,
		Scent:      math.Round((scent/float64(max(foodEntries, 1)))*100) / 100,
		Hold:       math.Round(hold*100) / 100,
	}, true
}

// profileForItem 取具名窝料档案。有成分就走成分（与自制窝料同一条路），
// 成分表坏了才退回资产里预烘好的那一份。
func (d *groundbaitDefs) profileForItem(itemID string) (groundbaitProfile, bool) {
	def, ok := d.itemDef(itemID)
	if !ok {
		return groundbaitProfile{}, false
	}
	if p, ok := d.profileOf(def.Materials); ok {
		return p, true
	}
	// 成分表坏了才走到这里：退回资产里预烘好的那一份档案，判定不该因为
	// 一张原料表解析失败就整体退化。
	return groundbaitProfile{Diet: def.Diet, Potency: def.Potency, Dispersion: def.Dispersion,
		Particle: def.Particle, Scent: def.Scent, Hold: def.Hold}, len(def.Diet) > 0
}

// groundbaitDietSig 食性向量的稳定指纹，用作逐鱼种亲和的缓存键。
// 量化到小数点后 4 位，并按固定族序拼串：原料微调带来的万分位差异不该
// 被当成两种不同的料，map 遍历顺序也不该影响结果。
func groundbaitDietSig(diet map[string]float64) string {
	keys := make([]string, 0, len(diet))
	for fam, w := range diet {
		if w > 0 {
			keys = append(keys, fam)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, fam := range keys {
		fmt.Fprintf(&b, "%s:%.4f;", fam, diet[fam])
	}
	return b.String()
}

// groundbaitFishName 取鱼种中文名。攻略吃饵表是按中文名索引的，而中文名在
// 两份地方都有：生态档案的 source.name（随包发布，必有）和数据库的名字表
// （按鱼名录同步，可能缺条目）。冻结与可接受食性都要靠攻略这张表投票，
// 所以先读随包的档案名，读不到再退回数据库。
func groundbaitFishName(fishKey string) string {
	groundbaitFishNamesOnce.Do(func() {
		groundbaitFishNames = map[string]string{}
		var rows []struct {
			FishKey string `json:"fish_key"`
			Source  struct {
				Name string `json:"name"`
			} `json:"source"`
		}
		if json.Unmarshal(fishEcologyDefsJSON, &rows) != nil {
			return
		}
		for _, row := range rows {
			if name := strings.TrimSpace(row.Source.Name); name != "" {
				groundbaitFishNames[row.FishKey] = name
			}
		}
	})
	return groundbaitFishNames[fishKey]
}

// groundbaitAcceptedFamilies 攻略吃饵表里这条鱼接受的食性族。
// allow 是攻略原文按 / 切开的吃饵词，逐个比对关键词即可，不做模糊匹配。
func groundbaitAcceptedFamilies(r fishBaitRule) map[string]bool {
	out := map[string]bool{}
	for _, term := range r.allow {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		for fam, keywords := range groundbaitFamilyKeywords {
			for _, kw := range keywords {
				if strings.Contains(term, kw) {
					out[fam] = true
					break
				}
			}
		}
	}
	return out
}

// groundbaitPredatorFrozen 掠食性鱼种冻结（双票）：生态食性表说它几乎不吃素
// （lure 高、plant 与 boilie 都低），**并且**攻略吃饵表里也没有任何植物/煮饵族
// 的记录，两条都成立才冻结。单票一律不冻结 —— 少见的「攻略没收录的杂食鱼」
// 会被误伤，离线穷举里九款商品窝料的目标鱼一条都没被冻掉。
func groundbaitPredatorFrozen(fish map[string]float64, hasRule bool, accepted map[string]bool,
	lureMin, plantMax, boilieMax float64) bool {
	if !hasRule || len(fish) == 0 {
		return false
	}
	if fish["lure"] < lureMin || fish["plant"] >= plantMax || fish["boilie"] >= boilieMax {
		return false
	}
	return !accepted["plant"] && !accepted["boilie"]
}

// groundbaitAppetite 一团窝料对一条鱼的食性契合度与专食性。
//
// 契合度是加权平均：窝料里每一族食性按它在料里的占比计权，攻略认不认这一族
// 决定打折力度（认＝1.0；不认但生态表里这条鱼确实吃＝0.85；两票都不认＝0.50）。
// 这样「属性接近的两团料」算出来的契合度必然接近，不会出现换个原料就翻倍。
//
// 专食性额外要求主力族在料里明显压过次族（specialMargin）：0.52 对 0.49 这种
// 半斤八两的组合不算「专攻某一口」，不给专食加成。
func groundbaitAppetite(bait, fish map[string]float64, accepted map[string]bool, specialMargin float64) (float64, float64) {
	num, den := 0.0, 0.0
	for _, fam := range groundbaitFamilies {
		w := bait[fam]
		if w <= 0 {
			continue
		}
		mult := 1.0
		if !accepted[fam] {
			if fish[fam] >= groundbaitEcoSoft {
				mult = groundbaitAcceptSoft
			} else {
				mult = groundbaitAcceptPenalty
			}
		}
		num += w * mult * fish[fam]
		den += w * mult
	}
	if den <= 0 {
		return 0, 0
	}
	appetite := num / den

	// 料的主力族：按固定族序取最大值，平手时算前面那一族，保证可复现。
	dom, domW := "", 0.0
	for _, fam := range groundbaitFamilies {
		if bait[fam] > domW {
			dom, domW = fam, bait[fam]
		}
	}
	if dom == "" {
		return appetite, 0
	}
	runnerUp := 0.0
	for _, fam := range groundbaitFamilies {
		if fam != dom && bait[fam] > runnerUp {
			runnerUp = bait[fam]
		}
	}
	if domW-runnerUp < specialMargin {
		return appetite, 0
	}

	// 鱼侧：主力这一口必须是这条鱼最执着的食性，且明显强过它的平均水平。
	top, sum := 0.0, 0.0
	for _, v := range fish {
		if v > top {
			top = v
		}
		sum += v
	}
	if len(fish) == 0 || top <= sum/float64(len(fish)) {
		return appetite, 0
	}
	mean := sum / float64(len(fish))
	if fish[dom] < top-1e-9 {
		return appetite, 0
	}
	margin := (fish[dom] - mean) / math.Max(0.25, top-mean)
	return appetite, groundbaitClamp(margin, 0, 1)
}
