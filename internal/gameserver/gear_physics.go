package gameserver

import (
	"context"
	"math"
	"strconv"
	"strings"
	"sync"

	"rf4server/internal/store"
)

// 钓具物理约束。
//
// 数据源为 item_def.attrs_json。物理标量以 U32 承载 F32 位型，
// 例如 1096821190 表示 14.01kg，不能直接按整数使用。
const (
	gearCatRod        = int64(3)
	gearCatReel       = int64(6)
	gearCatLine       = int64(11)
	gearCatLeader     = int64(13)
	gearCatLeaderWire = int64(15)

	gearAttrMaxLoad  = "kngaaefkpka"
	gearAttrDiameter = "ciichedlnjh"
	gearAttrLength   = "hkgndoipdlc"
	gearAttrWeight   = "dhlkahnodoo"
	gearAttrTestMin  = "dakogfmkclb"
	gearAttrTestMax  = "jfebacgmlmb"
	gearAttrTaper    = "ddebfbbjppk"
	gearAttrStretch  = "aomkdnjcgji"
	gearAttrDensity  = "lgjeahdobch"
	gearAttrLineLen  = "ljnnpokapig"
	gearAttrDrag     = "mlfjkaoicdn"
	gearAttrGear     = "oiaplokhcii"
	gearAttrDragMax  = "omghfmdfjpc"
	gearAttrRatio    = "dpgbopbanal"
	gearAttrLeaderIn = "mbnambppbom"
	gearAttrHookSize = "gkfbafmbpam"
	gearAttrHookLen  = "najajfakhdo"

	// hookWeightVolumeFactor 把钩体长度(mm)换算成"能入口"的鱼重上限(g)：
	// cap = factor * length^3。这是假想模型，本体没有对应判定，
	// 只在 hook_entry_cap=1 时用于对照调试（默认关闭）。
	hookWeightVolumeFactor = 8.0
	// gearOverloadFactor 只在管理员打开派鱼截断调试时使用：旧承力口径和
	// 钩入口口径都乘这个系数，给玩家 35% 控鱼余量。17/9 断件由客户端上报，
	// 服务端据此持久化断件，不在这里重新判定承力。
	gearOverloadFactor = 1.35
)

// rigGearIDs 返回当前钓组用于物理判定的 systemID。
// 空字符串表示数据缺失，该部件不参与最弱环计算。
type rigGearIDs struct {
	Rod              string
	Reel             string
	ReelGUID         string
	Line             string
	Leader           string
	RodDurability    float64
	ReelDurability   float64
	LineDurability   float64
	LeaderDurability float64
}

// gearPhysicsSpec 是 item_def 解码后的物理量快照（kg/mm/m/比例）。
type gearPhysicsSpec struct {
	RodPowerKg        float64
	LineStrengthKg    float64
	LeaderStrengthKg  float64
	ReelPresent       bool
	ReelDragKg        float64 // 当前耐久阶段下的锁轮承力
	ReelCurrentDragKg float64 // 当前摩擦档位下的制动力，第 30 档锁轮时等于锁轮承力
	LineStretch       float64
	HookPresent       bool
	HookStrengthKg    float64
	HookLengthMM      float64
	HookSize          int64
}

// decodeU32F32 把 attrs_json 中的 U32 数值按 IEEE-754 F32 位型还原。
// JSON 数字经 encoding/json 解到 any 后是 float64，因此先转回 uint32。
func decodeU32F32(v any) (float64, bool) {
	if v == nil {
		return 0, false
	}
	var u uint64
	switch n := v.(type) {
	case float64:
		if n < 0 || n > float64(^uint32(0)) || math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, false
		}
		u = uint64(n)
	case float32:
		if n < 0 || float64(n) > float64(^uint32(0)) || math.IsNaN(float64(n)) || math.IsInf(float64(n), 0) {
			return 0, false
		}
		u = uint64(n)
	case int:
		if n < 0 {
			return 0, false
		}
		u = uint64(n)
	case int64:
		if n < 0 {
			return 0, false
		}
		u = uint64(n)
	case uint64:
		u = n
	case uint32:
		u = uint64(n)
	default:
		return 0, false
	}
	if u > math.MaxUint32 {
		return 0, false
	}
	f := float64(math.Float32frombits(uint32(u)))
	if math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) > 1e9 {
		return 0, false
	}
	return f, true
}

func itemDefF32(def *store.ItemDef, key string) float64 {
	if def == nil {
		return 0
	}
	attrs := defAttrs(def)
	f, _ := decodeU32F32(attrs[key])
	return f
}

func itemDefInt(def *store.ItemDef, key string) int64 {
	if def == nil {
		return 0
	}
	attrs := defAttrs(def)
	switch n := attrs[key].(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case uint64:
		return int64(n)
	case uint32:
		return int64(n)
	default:
		return 0
	}
}

func itemDefNested(def *store.ItemDef, key string) map[string]any {
	if def == nil {
		return nil
	}
	attrs := defAttrs(def)
	m, _ := attrs[key].(map[string]any)
	return m
}

func nestedF32(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	f, _ := decodeU32F32(m[key])
	return f
}

func rodPowerKg(def *store.ItemDef) float64 { return itemDefF32(def, gearAttrMaxLoad) }
func rodLengthM(def *store.ItemDef) float64 { return itemDefF32(def, gearAttrLength) }

// missingRodDataLogged 记录已经告警过"缺物理字段"的竿 itemID，每个竿只告警一次，
// 避免每次抛竿/断件都刷日志。
var missingRodDataLogged sync.Map

// logMissingRodPhysics 竿有定义但缺 MaxLoad 或长度时告警一次。
// 只观察、不改判定：缺 MaxLoad 的竿在 gearBreakLimitKg 里被静默跳过，
// 等于这根竿永远不会成为最弱环（不会断）；缺长度则抛投距离拿不到竿长修正。
func (s *Server) logMissingRodPhysics(rodItemID string, def *store.ItemDef) {
	if s == nil || s.log == nil || strings.TrimSpace(rodItemID) == "" || def == nil {
		return
	}
	power := rodPowerKg(def)
	length := rodLengthM(def)
	if power > 0 && length > 0 {
		return
	}
	if _, dup := missingRodDataLogged.LoadOrStore(rodItemID, true); dup {
		return
	}
	s.log.Warn("竿缺少物理字段，最弱环/抛投将静默跳过", "rod", rodItemID,
		"max_load_kg", power, "length_m", length)
}
func lineStrengthKg(def *store.ItemDef) float64 { return itemDefF32(def, gearAttrMaxLoad) }
func lineDiameterMM(def *store.ItemDef) float64 { return itemDefF32(def, gearAttrDiameter) }
func lineLengthM(def *store.ItemDef) int64      { return itemDefInt(def, gearAttrLineLen) }
func lineStretch(def *store.ItemDef) float64    { return itemDefF32(def, gearAttrStretch) }
func lineDensityGPM(def *store.ItemDef) float64 { return itemDefF32(def, gearAttrDensity) }

func leaderStrengthKg(def *store.ItemDef) float64 {
	if def == nil {
		return 0
	}
	if inner := itemDefNested(def, gearAttrLeaderIn); inner != nil {
		if f := nestedF32(inner, gearAttrMaxLoad); f > 0 {
			return f
		}
	}
	return itemDefF32(def, gearAttrMaxLoad)
}

func reelDragMaxKg(def *store.ItemDef) float64 {
	if def == nil {
		return 0
	}
	return nestedF32(itemDefNested(def, gearAttrDrag), gearAttrDragMax)
}

func reelGearRatio(def *store.ItemDef) float64 {
	if def == nil {
		return 0
	}
	return nestedF32(itemDefNested(def, gearAttrGear), gearAttrRatio)
}

// 渔轮摩擦档位：1~29 是制动力刻度，第 30 档是锁轮。
const (
	fullReelDragStop = 29
	maxReelDragStop  = 30
)

// reelDragAtSetting 按摩擦档位给出实际作用在线上的制动力。
// 1~29 档超过该值时渔轮打滑放线，竿与线不会承受高于它的张力；
// 第 30 档锁轮后不再打滑，张力上限换成锁轮承力。
func reelDragAtSetting(perf *store.ReelPerformance, setting int64) float64 {
	if setting >= maxReelDragStop {
		return perf.LockedDragKg
	}
	if setting <= 0 {
		return 0
	}
	return perf.BaseDragKg * float64(setting) / float64(fullReelDragStop) * perf.BearingFactor
}

// rodReelDragSetting 读取客户端 17/14 保存的摩擦档位；没有记录时返回 false。
func (s *Server) rodReelDragSetting(ctx context.Context, rodGUID string) (int64, bool) {
	if s == nil || s.DB == nil || strings.TrimSpace(rodGUID) == "" {
		return 0, false
	}
	raw, err := s.DB.GetRodSetting(ctx, rodGUID, reelDragSettingKey)
	if err != nil {
		return 0, false
	}
	setting, cerr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if cerr != nil || setting <= 0 {
		return 0, false
	}
	if setting > maxReelDragStop {
		setting = maxReelDragStop
	}
	return setting, true
}

func rodTestG(def *store.ItemDef) (float64, float64) {
	return itemDefF32(def, gearAttrTestMin), itemDefF32(def, gearAttrTestMax)
}

func rodTaper(def *store.ItemDef) float64 { return itemDefF32(def, gearAttrTaper) }

// gearBreakLimitKg 返回整副钓组最弱一环的拉力；0 表示没有可信物理数据。
// 手竿没有轮子，只比较竿/主线/子线；带轮竿额外比较锁轮承力。
func gearBreakLimitKg(spec gearPhysicsSpec) float64 {
	parts := []float64{spec.RodPowerKg, spec.LineStrengthKg, spec.LeaderStrengthKg}
	if spec.ReelPresent {
		parts = append(parts, spec.ReelDragKg)
	}
	limit := 0.0
	for _, p := range parts {
		if p <= 0 {
			continue
		}
		if limit == 0 || p < limit {
			limit = p
		}
	}
	return limit
}

// gearStrengthCapKg 是按当前钓具承力换算的旧派鱼诊断口径。
//
// 默认不参与派鱼：鱼咬不咬钩取决于饵、水域、深度和鱼种分布，跟玩家手里
// 这根线有多粗无关。弱线组照样会遇到大鱼，结果是在搏鱼阶段由客户端触发
// 17/9 断件，而不是“大鱼不来”。只有 fish_weight_cap 打开时才恢复此口径。
func gearStrengthCapKg(spec gearPhysicsSpec) float64 {
	limit := gearBreakLimitKg(spec)
	if limit <= 0 {
		return 0
	}
	return limit * gearOverloadFactor
}

// hookEntryCapKg 是钩的入口换算：钩体太小，大鱼即使咬中也挂不住。
// 这是"钩号 × 鱼嘴"的几何关系，不是承力，所以和强度上限分开控制。
//
// 注意：这是**假想模型**，不是本体规则。本体里钩号只决定钩的强度与外观，
// 没有"鱼嘴太大咬不住小钩"的判定；默认关闭（见 fishWeightCapKg），
// 只在管理员想对照调试时打开。
func hookEntryCapKg(spec gearPhysicsSpec) float64 {
	if !spec.HookPresent {
		return 0
	}
	hookCapG := hookWeightCapG(spec.HookLengthMM, spec.HookSize)
	if hookCapG <= 0 {
		return 0
	}
	return (hookCapG / 1000.0) * gearOverloadFactor
}

// gearWeightCapKg 返回强度上限与钩入口上限中的较小值，保留给测试与诊断对照。
// 真实派鱼必须先经过 fishWeightCapKg 的两个独立开关，不要直接用这个函数。
func gearWeightCapKg(spec gearPhysicsSpec) float64 {
	strength := gearStrengthCapKg(spec)
	entry := hookEntryCapKg(spec)
	if strength <= 0 {
		return entry
	}
	if entry <= 0 {
		return strength
	}
	return math.Min(strength, entry)
}

// fishWeightCapKg 是派鱼实际生效的重量上限，两条限制分别开关，**默认都关**：
//   - 承力截断 fish_weight_cap：默认 0。线多粗不影响鱼来不来，只影响搏鱼时断不断；
//   - 钩入口截断 hook_entry_cap：默认 0。钩号大小不影响鱼来不来，所有钩都可能上所有鱼。
//
// 两个都关时返回 0，表示不截断——这是默认状态：鱼种由饵、水域、深度和鱼种分布决定，
// 钓具只决定"拉不拉得上来"。管理员显式写 1 才恢复对应的旧模型，用于对照调试。
func (s *Server) fishWeightCapKg(spec gearPhysicsSpec) float64 {
	capKg := 0.0
	if s.tunableF("fish_weight_cap", 0) > 0 {
		capKg = gearStrengthCapKg(spec)
	}
	if s.tunableF("hook_entry_cap", 0) > 0 {
		if entry := hookEntryCapKg(spec); entry > 0 && (capKg <= 0 || entry < capKg) {
			capKg = entry
		}
	}
	return capKg
}

func gearWeakestPart(spec gearPhysicsSpec) string {
	reelKg := 0.0
	if spec.ReelPresent {
		reelKg = spec.ReelDragKg
	}
	parts := []struct {
		name string
		kg   float64
	}{
		{"reel", reelKg},
		{"rod", spec.RodPowerKg},
		{"line", spec.LineStrengthKg},
		{"leader", spec.LeaderStrengthKg},
	}
	name := ""
	limit := 0.0
	for _, p := range parts {
		if p.kg <= 0 || (limit > 0 && p.kg >= limit) {
			continue
		}
		name, limit = p.name, p.kg
	}
	return name
}

// effectiveLineBuffer 是线材吸收冲击后的拉力系数（1=刚性，越大越能缓冲）。
func effectiveLineBuffer(stretch float64) float64 {
	if stretch < 0 {
		stretch = 0
	}
	if stretch > 1 {
		stretch = 1
	}
	return 1.0 - 0.5*stretch
}

// hookWeightCapG 以钩体长度估算鱼入口的重量上限。缺少长度时用钩号
// 做单调兜底；返回值 0 表示没有可用钩数据，不额外限制。
func hookWeightCapG(lengthMM float64, size int64) float64 {
	if lengthMM <= 0 && size >= 0 {
		lengthMM = 1.5 + 0.35*float64(size)
	}
	if lengthMM <= 0 {
		return 0
	}
	cap := hookWeightVolumeFactor * math.Pow(lengthMM, 3)
	if cap < 50 {
		cap = 50
	}
	if cap > 2_000_000 {
		cap = 2_000_000
	}
	return cap
}

func capWeightGrams(weightG, capG int64) int64 {
	if capG > 0 && weightG > capG {
		weightG = capG
	}
	if weightG < 1 {
		weightG = 1
	}
	return weightG
}

// loadGearSpec 读取四个物理件的 item_def 并生成快照。
func (s *Server) loadGearSpec(ctx context.Context, ids rigGearIDs) gearPhysicsSpec {
	var spec gearPhysicsSpec
	if s == nil || s.DB == nil {
		return spec
	}
	load := func(itemID string) *store.ItemDef {
		itemID = strings.TrimSpace(itemID)
		if itemID == "" {
			return nil
		}
		def, err := s.DB.GetItemDefByItemID(ctx, itemID)
		if err != nil {
			return nil
		}
		return def
	}
	applyDurability := func(v, durability float64) float64 {
		if v <= 0 {
			return 0
		}
		if durability < 0 {
			durability = 0
		}
		if durability > 1 {
			durability = 1
		}
		return v * durability
	}
	spec.RodPowerKg = applyDurability(rodPowerKg(load(ids.Rod)), ids.RodDurability)
	s.logMissingRodPhysics(ids.Rod, load(ids.Rod))
	spec.ReelPresent = strings.TrimSpace(ids.Reel) != ""
	if spec.ReelPresent {
		spec.ReelDragKg = reelDragMaxKg(load(ids.Reel)) * reelLockMultiplier(ids.ReelDurability)
	}
	lineDef := load(ids.Line)
	spec.LineStrengthKg = applyDurability(lineStrengthKg(lineDef), ids.LineDurability)
	spec.LineStretch = lineStretch(lineDef)
	spec.LeaderStrengthKg = applyDurability(leaderStrengthKg(load(ids.Leader)), ids.LeaderDurability)
	return spec
}

// loadGearSpecForRig 在竿/轮/线/前导之外读取当前线末端鱼钩。
// 拟饵出厂钩可能没有独立 item 行，因此通过物化后的钩树反查 numeric_id。
func (s *Server) loadGearSpecForRig(ctx context.Context, ids rigGearIDs, rodGUID string) gearPhysicsSpec {
	spec := s.loadGearSpec(ctx, ids)
	if ids.ReelGUID != "" {
		if row, err := s.DB.GetItemByGUID(ctx, ids.ReelGUID); err == nil && row != nil {
			if perf, perr := s.syncReelPerformance(ctx, row); perr == nil {
				spec.ReelPresent = true
				spec.ReelDragKg = perf.LockedDragKg
				if setting, ok := s.rodReelDragSetting(ctx, rodGUID); ok {
					spec.ReelCurrentDragKg = reelDragAtSetting(perf, setting)
				} else {
					// 客户端还没保存过摩擦档位时只有锁轮数据可用，按锁轮兜底。
					spec.ReelCurrentDragKg = perf.LockedDragKg
				}
			}
		}
	}
	def, durability := s.mountedHookDef(ctx, rodGUID)
	if def == nil {
		return spec
	}
	spec.HookPresent = true
	spec.HookSize = itemDefInt(def, gearAttrHookSize)
	spec.HookLengthMM = itemDefF32(def, gearAttrHookLen)
	if durability < 0 {
		durability = 0
	}
	if durability > 1 {
		durability = 1
	}
	spec.HookStrengthKg = itemDefF32(def, gearAttrMaxLoad) * durability
	return spec
}

// mountedHookDef 返回当前线末端钩的静态定义和实例耐久。
func (s *Server) mountedHookDef(ctx context.Context, rodGUID string) (*store.ItemDef, float64) {
	if s == nil || s.DB == nil || rodGUID == "" {
		return nil, 1
	}
	hookGUID := s.mountedHookGuid(ctx, rodGUID)
	if hookGUID == "" {
		return nil, 1
	}
	if row, err := s.DB.GetItemByGUID(ctx, hookGUID); err == nil && row != nil {
		if def, derr := s.itemDefForRow(ctx, row); derr == nil && def != nil {
			return def, row.Durability
		}
	}
	tree := s.buildMountedHookTree(ctx, rodGUID)
	if tree == nil {
		return nil, 1
	}
	idx := fieldIdxByCS(tree.Class, csNumericID)
	if idx < 0 || idx >= len(tree.Fields) || tree.Fields[idx].Num == nil {
		return nil, 1
	}
	numericID := int64(u32Of(tree.Fields[idx].Num))
	def, err := s.DB.GetItemDef(ctx, numericID)
	if err != nil {
		return nil, 1
	}
	return def, 1
}

// gearWeightCapForRig 读取当前钓组并返回实际生效的派鱼重量上限（kg）；0 表示不截断。
func (s *Server) gearWeightCapForRig(ctx context.Context, uid int64, rodGUID string) (float64, rigGearIDs, string) {
	if s == nil || s.DB == nil {
		return 0, rigGearIDs{}, ""
	}
	ids := s.rigGearIDs(ctx, uid, rodGUID)
	spec := s.loadGearSpecForRig(ctx, ids, rodGUID)
	// 两个截断开关默认都关；日志中的 weakest 仍用于诊断承力链。
	return s.fishWeightCapKg(spec), ids, gearWeakestPart(spec)
}

// rigGearIDs 解析当前钓组，优先读取竿实例：
//   - f3 = 线、f4 = 轮（专用字段）；
//   - f5 组件表 = 前导/其它挂载件；
//   - rig_component 是对应的可查询派生索引。
func (s *Server) rigGearIDs(ctx context.Context, uid int64, rodGUID string) rigGearIDs {
	var out rigGearIDs
	if s == nil || s.DB == nil {
		return out
	}
	row := s.itemByGUIDOrOwnedCategory(ctx, uid, rodGUID, gearCatRod)
	if row == nil {
		return out
	}
	out.Rod = s.itemSystemID(ctx, row)
	out.RodDurability = row.Durability

	compGUIDs := map[int64]string{}
	if comps, err := s.DB.ListRigComponents(ctx, row.GUID); err == nil {
		for _, c := range comps {
			if c != nil && c.GUID != "" {
				compGUIDs[c.Slot] = c.GUID
			}
		}
	}
	if fields, err := FieldsFromJSON(row.FieldsJSON); err == nil {
		if idx := fieldIdxByCS(row.WireClass, csLine); idx >= 0 && idx < len(fields) {
			if g := fields[idx].GuidHex(); g != "" {
				compGUIDs[gearCatLine] = g
			}
		}
		if idx := fieldIdxByCS(row.WireClass, csReel); idx >= 0 && idx < len(fields) {
			if g := fields[idx].GuidHex(); g != "" {
				compGUIDs[gearCatReel] = g
			}
		}
		if idx := fieldIdxByCS(row.WireClass, csRigObject); idx >= 0 && idx < len(fields) {
			for slot, comp := range rigCompsOf(fields[idx].Val) {
				if comp.GUID != "" {
					compGUIDs[slot] = comp.GUID
				}
			}
		}
	}

	out.ReelGUID = compGUIDs[gearCatReel]
	out.Reel, out.ReelDurability = s.itemSystemIDDurabilityByGUID(ctx, out.ReelGUID)
	out.Line, out.LineDurability = s.itemSystemIDDurabilityByGUID(ctx, compGUIDs[gearCatLine])
	out.Leader, out.LeaderDurability = s.itemSystemIDDurabilityByGUID(ctx, compGUIDs[gearCatLeaderWire])
	if out.Leader == "" {
		out.Leader, out.LeaderDurability = s.itemSystemIDDurabilityByGUID(ctx, compGUIDs[gearCatLeader])
	}

	// 没有挂轮就是手竿；轮子和主线都只认当前竿上的真实装配，
	// 不能拿背包里的任意物品补位。
	return out
}

func (s *Server) itemByGUIDOrOwnedCategory(ctx context.Context, uid int64, guid string, category int64) *store.Item {
	if guid != "" {
		if row, err := s.DB.GetItemByGUID(ctx, guid); err == nil && row != nil && row.UID == uid {
			return row
		}
	}
	guids, err := s.DB.ItemGUIDsByContainers(ctx, uid, []string{"backpack", "rig"})
	if err != nil {
		return nil
	}
	for _, g := range guids {
		row, err := s.DB.GetItemByGUID(ctx, g)
		if err != nil || row == nil {
			continue
		}
		def, err := s.itemDefForRow(ctx, row)
		if err == nil && def != nil && def.Category == category {
			return row
		}
	}
	return nil
}

func (s *Server) itemSystemID(ctx context.Context, row *store.Item) string {
	if row == nil {
		return ""
	}
	if strings.TrimSpace(row.ItemID) != "" {
		return row.ItemID
	}
	if def, err := s.itemDefForRow(ctx, row); err == nil && def != nil {
		return def.ItemID
	}
	return ""
}

func (s *Server) itemSystemIDByGUID(ctx context.Context, guid string) string {
	if s == nil || s.DB == nil || guid == "" {
		return ""
	}
	row, err := s.DB.GetItemByGUID(ctx, guid)
	if err != nil || row == nil {
		return ""
	}
	return s.itemSystemID(ctx, row)
}

func (s *Server) itemSystemIDDurabilityByGUID(ctx context.Context, guid string) (string, float64) {
	if s == nil || s.DB == nil || guid == "" {
		return "", 1
	}
	row, err := s.DB.GetItemByGUID(ctx, guid)
	if err != nil || row == nil {
		return "", 1
	}
	return s.itemSystemID(ctx, row), row.Durability
}

func (s *Server) firstOwnedItemSystemID(ctx context.Context, uid, category int64) string {
	guids, err := s.DB.ItemGUIDsByContainers(ctx, uid, []string{"backpack", "rig"})
	if err != nil {
		return ""
	}
	for _, g := range guids {
		row, err := s.DB.GetItemByGUID(ctx, g)
		if err != nil || row == nil {
			continue
		}
		def, err := s.itemDefForRow(ctx, row)
		if err == nil && def != nil && def.Category == category {
			return s.itemSystemID(ctx, row)
		}
	}
	return ""
}

func (s *Server) firstOwnedItemSystemIDDurability(ctx context.Context, uid, category int64) (string, float64) {
	guids, err := s.DB.ItemGUIDsByContainers(ctx, uid, []string{"backpack", "rig"})
	if err != nil {
		return "", 1
	}
	for _, g := range guids {
		row, err := s.DB.GetItemByGUID(ctx, g)
		if err != nil || row == nil {
			continue
		}
		def, err := s.itemDefForRow(ctx, row)
		if err == nil && def != nil && def.Category == category {
			return s.itemSystemID(ctx, row), row.Durability
		}
	}
	return "", 1
}

func (s *Server) itemDefForRow(ctx context.Context, row *store.Item) (*store.ItemDef, error) {
	if row == nil {
		return nil, nil
	}
	if row.NumericID != 0 {
		if def, err := s.DB.GetItemDef(ctx, row.NumericID); err == nil && def != nil {
			return def, nil
		}
	}
	if row.ItemID != "" {
		return s.DB.GetItemDefByItemID(ctx, row.ItemID)
	}
	return nil, nil
}

// itemDefStr 读取 item_def.attrs_json 里的字符串字段（如地图的 dhaenhabecc）。
func itemDefStr(def *store.ItemDef, key string) string {
	if def == nil {
		return ""
	}
	s, _ := defAttrs(def)[key].(string)
	return s
}
