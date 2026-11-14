package gameserver

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"rf4server/internal/store"
)

const castAttrWaterWeight = "ijbjficbhog"

const (
	castAttrReelType      = "fopbhlmkcpe"
	castAttrLineMaterial  = "fopbhlmkcpe"
	castAttrDefaultSpool  = "dlfjnokncfl"
	castAttrSpoolSize     = "gkfbafmbpam"
	castAttrSpoolCapacity = "djcepbpigpe"
)

// castAttrFloatWeight 是浮漂克重。浮漂没有 dhlkahnodoo/ijbjficbhog，
// 只登记额定号数对应的克重，旧实现因此把整支浮漂算成 0 g。
const castAttrFloatWeight = "jpaemlfkohn"

// castAttrLureHooks 是拟饵内嵌的子配置数组（自带三本钩等）。子钩重量
// 记在各自的 ijbjficbhog 上，需要与饵体重量相加。
const castAttrLureHooks = "gpoaffnjbdo"

// castAttrLeaderLink 是成品前导内嵌的连接件（转环/别针）。
const castAttrLeaderLink = "lbadjgllglo"

// castWeightPart 是组合重量的一个分项，仅用于日志审计。
type castWeightPart struct {
	Label  string
	Weight float64
}

// itemDefCastWeightG 读取物品抛投重量。多数装备使用 dhlkahnodoo，
// 钩、沉子、喂食器和部分饵使用 ijbjficbhog（水中/工作重量），
// 浮漂单独用 jpaemlfkohn 登记克重。
func itemDefCastWeightG(def *store.ItemDef) float64 {
	if v := itemDefNumericScalar(def, gearAttrWeight); v > 0 {
		return v
	}
	if v := itemDefNumericScalar(def, castAttrWaterWeight); v > 0 {
		return v
	}
	return itemDefNumericScalar(def, castAttrFloatWeight)
}

// itemDefNumericScalar 同时兼容静态库中的 F32 位型和普通 JSON 数字。
func itemDefNumericScalar(def *store.ItemDef, key string) float64 {
	if def == nil {
		return 0
	}
	raw := defAttrs(def)[key]
	var n float64
	switch v := raw.(type) {
	case float64:
		n = v
	case float32:
		n = float64(v)
	case int:
		n = float64(v)
	case int64:
		n = float64(v)
	case uint32:
		n = float64(v)
	case uint64:
		n = float64(v)
	default:
		return 0
	}
	// IEEE-754 F32 位型通常在十亿量级；普通克重/Test 数字远小于此值。
	if n >= 1_000_000 {
		if f, ok := decodeU32F32(raw); ok {
			n = f
		}
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 1_000_000 {
		return 0
	}
	return n
}

// castLineStats 是当前主线的真实静态/实例参数。长度优先读取物品实例字段，
// 确保断线后剩余长度会直接限制抛投距离。
type castLineStats struct {
	DiameterMM       float64
	LengthM          float64
	Stretch          float64
	Material         int64
	LinearDensityGPM float64
}

func (s *Server) castLineStats(ctx context.Context, rodGUID, lineItemID string) castLineStats {
	var out castLineStats
	if s == nil || s.DB == nil || lineItemID == "" {
		return out
	}
	def, err := s.DB.GetItemDefByItemID(ctx, lineItemID)
	if err != nil || def == nil {
		return out
	}
	out.DiameterMM = lineDiameterMM(def)
	out.LengthM = float64(lineLengthM(def))
	out.Stretch = lineStretch(def)
	out.Material = itemDefInt(def, castAttrLineMaterial)
	out.LinearDensityGPM = lineMassGPM(def)
	lineGUID := s.rigComponentGuidOf(ctx, rodGUID, catLine)
	if row, rerr := s.DB.GetItemByGUID(ctx, lineGUID); rerr == nil && row != nil {
		if n := s.itemLength(ctx, row); n > 0 {
			out.LengthM = float64(n)
		}
	}
	return out
}

// lineMaterialDensity 是钓线材质体积密度（g/cm³）。编织线按含空隙的
// 有效密度取值，不用实体 PE 的 0.97。
func lineMaterialDensity(material int64) float64 {
	switch material {
	case 2:
		return 1.78 // 氟碳
	case 3:
		return 0.70 // 编织（PE，含编织空隙）
	case 6:
		return 7.85 // 钢丝
	case 7:
		return 4.50 // 钛
	default:
		return 1.14 // 尼龙
	}
}

// lineMassGPM 按“密度 × 截面积 × 单位长度”折算钓线单位长度质量。
//
// 静态表里的 lgjeahdobch 经核对是刚性/柔软度：同一材质下取值几乎不随
// 线径变化（编织线大量型号都固定 0.15），把它当 g/m 会让 1.25mm 与
// 0.14mm 的线得到相同线重。只有拿不到线径时才回退到该字段。
func lineMassGPM(def *store.ItemDef) float64 {
	if def == nil {
		return 0
	}
	d := lineDiameterMM(def)
	if d <= 0 {
		return lineDensityGPM(def)
	}
	rho := lineMaterialDensity(int64(itemDefInt(def, castAttrLineMaterial)))
	return rho * math.Pi / 4 * d * d
}

// leaderMassG 估算前导重量。成品前导（cat13）的 hkgndoipdlc 是厘米，
// 散装前导线（cat15）的长度在 ljnnpokapig（米）。线径与材质优先取
// 内嵌线段（mbnambppbom）的登记值，缺失时用前导自身字段。
//
// instanceLengthM 来自物品实例 csQty（itemLength，单位米），只在静态定义
// 没写长度时补位：cat15 有 82/193 条缺 ljnnpokapig，全靠定义会整类按 0 g。
// 定义里已有长度时以定义为准，不能让成品前导的实例数量（件数）盖掉厘米值。
func leaderMassG(def *store.ItemDef, instanceLengthM float64) float64 {
	if def == nil {
		return 0
	}
	lengthM := 0.0
	if v := itemDefNumericScalar(def, gearAttrLength); v > 0 {
		lengthM = v / 100 // 成品前导：厘米
	}
	if lengthM <= 0 {
		if v := itemDefNumericScalar(def, gearAttrLineLen); v > 0 {
			lengthM = v // 散装前导线：米
		}
	}
	if lengthM <= 0 && instanceLengthM > 0 {
		lengthM = instanceLengthM
	}
	if lengthM <= 0 {
		return 0
	}
	diameterMM := lineDiameterMM(def)
	material := int64(itemDefInt(def, gearAttrMaterial))
	if material <= 0 {
		material = int64(itemDefInt(def, castAttrLineMaterial))
	}
	if inner := itemDefNested(def, gearAttrLeaderIn); inner != nil {
		if v := nestedF32(inner, gearAttrDiameter); v > 0 {
			diameterMM = v
		}
		if v := nestedInt(inner, gearAttrMaterial); v > 0 {
			material = v
		} else if v := nestedInt(inner, castAttrLineMaterial); v > 0 {
			material = v
		}
	}
	if diameterMM <= 0 {
		// 无线径兜底：lgjeahdobch 在静态表里是刚性/柔软度（同材质下近似常数），
		// 这里只能把它近似当成"每米克重"再乘长度，量纲不是严格的物理量。
		// 之所以保留：cat13 有 52/181 条既无内嵌线段也无顶层线径，去掉兜底
		// 会让这 52 条整类按 0 g 计入抛投载荷。有实例长度时至少与长度成正比。
		lengthM *= itemDefNumericScalar(def, gearAttrDensity)
		if lengthM <= 0 {
			return 0
		}
		return lengthM
	}
	return lineMaterialDensity(material) * math.Pi / 4 * diameterMM * diameterMM * lengthM
}

// lureChildWeightG 汇总拟饵内嵌子配置（三本钩等）的水中重量。
func lureChildWeightG(def *store.ItemDef) float64 {
	if def == nil {
		return 0
	}
	raw, ok := defAttrs(def)[castAttrLureHooks]
	if !ok {
		return 0
	}
	total := 0.0
	switch list := raw.(type) {
	case []any:
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				total += nestedNumericScalar(m, castAttrWaterWeight)
			}
		}
	case map[string]any:
		total += nestedNumericScalar(list, castAttrWaterWeight)
	}
	return total
}

// rigComponentCastWeight 返回单个装配组件对组合重量的贡献。
// 只有线杯上的存线不计入终端重量（由调用方按运行线段单独折算）。
func rigComponentCastWeight(category int64, def *store.ItemDef, instanceLengthM float64) float64 {
	switch category {
	case catLead, 15:
		if w := leaderMassG(def, instanceLengthM); w > 0 {
			return w
		}
	case catLure:
		return itemDefCastWeightG(def) + lureChildWeightG(def)
	}
	return itemDefCastWeightG(def)
}

// castPayloadWeights 从竿实例的真实 f5 装配表汇总终端线组重量与饵重。
// 主线、轮子和鱼竿本体不属于飞行载荷；拟饵与天然饵按饵重单列。
// 每个子件按类别取正确的重量字段，避免浮漂、成品前导这类没有
// dhlkahnodoo 的物品被算成 0 g。
func (s *Server) castPayloadWeights(ctx context.Context, rodGUID string) (rigWeightG, baitWeightG float64, parts []castWeightPart) {
	if s == nil || s.DB == nil || rodGUID == "" {
		return 0, 0, nil
	}
	comps := s.storedRigComponents(ctx, rodGUID)
	if len(comps) == 0 {
		// 兼容尚未投影到 f5 的旧档案；正常新档案以 f5 为权威。
		if rows, err := s.DB.ListRigComponents(ctx, rodGUID); err == nil {
			for _, row := range rows {
				if row != nil {
					comps[rigCompKey(row.Slot, row.Ordinal)] = rigComp{
						Category: row.Slot, Ordinal: row.Ordinal, GUID: row.GUID, Extra: row.Extra,
					}
				}
			}
		}
	}
	seen := make(map[string]bool, len(comps))
	for _, comp := range comps {
		if comp.GUID == "" || seen[comp.GUID] || comp.Category == catReel || comp.Category == catLine {
			continue
		}
		seen[comp.GUID] = true
		row, err := s.DB.GetItemByGUID(ctx, comp.GUID)
		if err != nil || row == nil {
			continue
		}
		def, err := s.itemDefForRow(ctx, row)
		if err != nil || def == nil {
			continue
		}
		// 前导线静态定义可能没写长度，用实例 csQty 的米数补位。
		instanceLengthM := 0.0
		if comp.Category == catLead || comp.Category == 15 {
			instanceLengthM = float64(s.itemLength(ctx, row))
		}
		weight := rigComponentCastWeight(comp.Category, def, instanceLengthM)
		if weight <= 0 {
			continue
		}
		if comp.Category == catBait || comp.Category == catLure {
			baitWeightG += weight
		} else {
			rigWeightG += weight
		}
		parts = append(parts, castWeightPart{Label: rigPartLabel(comp.Category, row.ItemID), Weight: weight})
	}
	return rigWeightG, baitWeightG, parts
}

// rigPartLabel 生成日志里的子件标签：类别名 + itemID 尾部，便于对照。
func rigPartLabel(category int64, itemID string) string {
	names := map[int64]string{
		12: "hook", 13: "leader", 15: "wire", 17: "lure", 27: "bait",
		28: "float", 29: "sinker", 47: "feeder", 0: "other",
	}
	name, ok := names[category]
	if !ok {
		name = "cat" + strconv.FormatInt(category, 10)
	}
	return name + ":" + itemID
}

// formatCastWeightParts 把分项压成一行，按重量降序取前六项。
func formatCastWeightParts(parts []castWeightPart, lineRunningG float64) string {
	if len(parts) == 0 && lineRunningG <= 0 {
		return ""
	}
	sorted := make([]castWeightPart, 0, len(parts)+1)
	sorted = append(sorted, parts...)
	if lineRunningG > 0 {
		sorted = append(sorted, castWeightPart{Label: "running_line", Weight: lineRunningG})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Weight > sorted[j].Weight })
	limit := 6
	if len(sorted) < limit {
		limit = len(sorted)
	}
	var b strings.Builder
	for i := 0; i < limit; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s=%.2fg", sorted[i].Label, sorted[i].Weight)
	}
	return b.String()
}

// castSkillStats 合成当前钓法熟练度与已学投距技能百分比。
// 投距百分比来自 skill_cast_effect_def（官方文案逐字提取，见
// docs/cast-skill-official-effects.md），按当前竿/轮的 fopbhlmkcpe 匹配 gate；
// 不匹配的节点一律不计入（castPct=0）。
func (s *Server) castSkillStats(ctx context.Context, uid, rigType, rodType, reelType int64, gears ...string) (category int64, mastery, castPct float64) {
	category = fishingSkillCatForRig(rigType)
	effects, err := s.effectiveFishingSkills(ctx, uid, gears...)
	if err != nil {
		return category, 0, 0
	}
	return category, effects.mastery(category), s.learnedCastSkillPct(ctx, effects, rodType, reelType)
}

// castSkillGate 是一个 gate 槽位（竿或轮 × fopbhlmkcpe 类型）。
type castSkillGate struct {
	Kind string
	Type int64
}

// learnedCastSkillPct 按装备类型汇总已学投距技能的官方加成百分比。
// 轮侧行跨钓法大类生效（官方「只需学一次」，等级共享由 skill_link 投影处理）。
// 同一 gate 槽位只取最高一行的贡献：轻型/重型传统轮（718/721）这类兄弟行在
// fopbhlmkcpe 里同型不可区分，官服同时只会激活与当前轮对应的那一行。
func (s *Server) learnedCastSkillPct(ctx context.Context, effects effectiveFishingEffects, rodType, reelType int64) float64 {
	if s == nil || s.DB == nil {
		return 0
	}
	ident, err := s.DB.LoadSkillIdentity(ctx)
	if err != nil {
		return 0
	}
	learned := ident.Project(effects.Learned)
	gear := ident.Project(effects.GearNodes)
	rows, err := s.DB.SQL.QueryContext(ctx, `
		SELECT d.node_id, d.per_level_pct, d.gate_kind, d.gate_type,
		       COALESCE(sd.max_level, 0)
		FROM skill_cast_effect_def d
		LEFT JOIN skill_def sd ON sd.cat_id = d.cat_id AND sd.node_id = d.node_id`)
	if err != nil {
		return 0
	}
	defer rows.Close()
	best := map[castSkillGate]float64{}
	for rows.Next() {
		var nodeID, gateType, maxLevel int64
		var perLevelPct float64
		var gateKind string
		if err := rows.Scan(&nodeID, &perLevelPct, &gateKind, &gateType, &maxLevel); err != nil {
			return 0
		}
		switch gateKind {
		case "rod":
			if rodType != gateType {
				continue
			}
		case "reel":
			if reelType != gateType {
				continue
			}
		default:
			continue
		}
		level := max64(0, learned[nodeID]+gear[nodeID])
		if maxLevel > 0 && level > maxLevel {
			level = maxLevel
		}
		if contribution := perLevelPct * float64(level); contribution > best[castSkillGate{gateKind, gateType}] {
			best[castSkillGate{gateKind, gateType}] = contribution
		}
	}
	if err := rows.Err(); err != nil {
		return 0
	}
	total := 0.0
	for _, contribution := range best {
		total += contribution
	}
	return total
}

// castReelSpoolStats 从当前轮子实例优先读取工作坊安装的线杯；老轮子没有内嵌
// 子实例时回退到轮子静态定义里的出厂线杯。容量公式来自客户端静态契约。
func (s *Server) castReelSpoolStats(ctx context.Context, reel *store.Item, lineItemID string) (reelType, spoolSize int64, capacityM float64) {
	if s == nil || s.DB == nil || reel == nil {
		return 0, 0, 0
	}
	var reelDef *store.ItemDef
	if def, err := s.itemDefForRow(ctx, reel); err == nil {
		reelDef = def
		reelType = itemDefInt(def, castAttrReelType)
	}

	var spool map[string]any
	if fields, err := FieldsFromJSON(reel.FieldsJSON); err == nil {
		for i := range fields {
			def := s.reelPartDef(ctx, fields[i].Val)
			if def == nil || def.Category != 8 {
				continue
			}
			spool = defAttrs(def)
			break
		}
	}
	if spool == nil && reelDef != nil {
		spool = itemDefNested(reelDef, castAttrDefaultSpool)
	}
	spoolSize = nestedInt(spool, castAttrSpoolSize)
	coef := nestedNumericScalar(spool, castAttrSpoolCapacity)
	if coef <= 0 || lineItemID == "" {
		return reelType, spoolSize, 0
	}
	lineDef, err := s.DB.GetItemDefByItemID(ctx, lineItemID)
	if err != nil || lineDef == nil {
		return reelType, spoolSize, 0
	}
	diameter := lineDiameterMM(lineDef)
	if diameter <= 0 {
		return reelType, spoolSize, 0
	}
	// 客户端容量表标定：2000 杯（系数 100）0.14mm≈234m、0.18mm≈154m、
	// 0.22mm≈111m；4000 杯（系数 180）0.20mm≈245m。
	// 拟合式 C ≈ 0.093 × 系数 / 线径^1.65，误差 ≤8%。
	capacityM = reelSpoolCapacityM(coef, diameter)
	return reelType, spoolSize, capacityM
}

// reelSpoolCapacityM 按线径估算线杯容量（米）。
// 数据来源：游戏内“线轴容量”读数（2000/4000 杯各三个线径）。
func reelSpoolCapacityM(coef, diameterMM float64) float64 {
	if coef <= 0 || diameterMM <= 0 {
		return 0
	}
	return math.Max(0.093*coef/math.Pow(diameterMM, 1.65), 5)
}

// spoolFillRatio 计算线杯上杯率。线长是整盘标称长度，超过杯容量时按容量
// 截断——600m 线盘装在只能装 17m 的小杯上，杯上并不会真有 600m。
func spoolFillRatio(lineLengthM, capacityM float64) float64 {
	if lineLengthM <= 0 || capacityM <= 0 {
		return 0
	}
	if lineLengthM > capacityM {
		lineLengthM = capacityM
	}
	return lineLengthM / capacityM
}

func nestedInt(m map[string]any, key string) int64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case uint32:
		return int64(v)
	case uint64:
		return int64(v)
	default:
		return 0
	}
}

func nestedNumericScalar(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	var n float64
	switch v := m[key].(type) {
	case float64:
		n = v
	case float32:
		n = float64(v)
	case int:
		n = float64(v)
	case int64:
		n = float64(v)
	case uint32:
		n = float64(v)
	case uint64:
		n = float64(v)
	default:
		return 0
	}
	if n >= 1_000_000 {
		if f, ok := decodeU32F32(m[key]); ok {
			n = f
		}
	}
	return n
}

func (s *Server) castPlayerCondition(ctx context.Context, uid int64) (primary, secondary float64) {
	c := s.currentPlayerCondition(ctx, uid)
	return c.Primary, c.Secondary
}
