package gameserver

// 竿组装配域。
//
// 物品 GUID 挂到父对象 GUID 上，槽位由物品品类决定。竿实例 f5 的 153 钓组对象
// 是装配数据源，rig 和 rig_component 表由它单向同步。
// 钓线(11)写入 f3，渔轮(6)写入 f4，其余组件写入 f5 的 152 组件表。
// 响应使用 800 容器快照：f0 新增实例，f1 更新实例，f2 移除 GUID。
//
// 请求形状：
//
//	17/1 [容器, 竿guid, 钓组种类]              —— 没有钓组 guid
//	17/2 [容器, 竿guid, 物品容器, 物品guid, int] —— 无槽位
//	17/3 [容器, 竿guid, 槽位品类, 物品guid, int]
//	17/4 [容器, 竿guid]
//	17/6 [容器, 竿guid, 挂饵方式位掩码]
//	16/1 [容器, 父guid, 容器, 物品guid]；15/1 同形但带尾部 int
//	15/2、16/2 与 17/3 同形
//	13/1 [容器, 父guid, 容器, 物品guid]

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// ---- 常量 ----

const zeroGUID = "00000000000000000000000000000000"

// 命令族号是 set_component 的 parent_type 参数，不是钓组类型。
const (
	familyHanditem = 13
	familyLure     = 15
	familyReel     = 16
	familyRod      = 17
)

// 装配使用的物品品类。
const (
	catReel  = 6  // 渔轮（f4 专用字段）
	catLine  = 11 // 钓线（f3 专用字段）
	catHook  = 12 // 鱼钩
	catLead  = 13 // 前导
	catLure  = 17 // 拟饵
	catBait  = 27 // 鱼饵（不占独立槽，挂饵路径）
	catFloat = 28 // 浮漂（沉底族无此槽）
)

// defaultRigDepthCM 是 153.f4 的默认钓棚深度，单位为厘米。
const defaultRigDepthCM = 100

// rigNameLure 是路亚钓组资源名；路亚钓组的 f4 固定为 0。
const (
	rigTypeLure = int64(5)
	rigNameLure = "rig_Lure"
)

// categorySlotField 标记使用专用字段而不是 f5 组件的品类。
var categorySlotField = map[int64]string{
	catLine: csLine,
	catReel: csReel,
}

// templateExemptCats 是不参与钓组模板槽校验的品类：
// 饵、尖梢、鱼线、渔轮和包装盒由各自的专用路径处理。
var templateExemptCats = map[int64]bool{27: true, 56: true, 11: true, 6: true, 55: true}

// handitemWireClasses 是手持工具的 wire 类；装填引用固定在字段 1。
var handitemWireClasses = map[string]bool{"ifnaghdaolg": true, "iknkgeomkip": true}

const handitemLoadedIdx = 1

// ---- 品类槽位校验 ----

// slotAccepts 定义槽位品类可以接受的物品品类。
var slotAccepts = map[int64]map[int64]bool{
	3:  {4: true, 5: true, 6: true, 47: true},                                                                                              // 鱼竿
	6:  {11: true, 13: true, 15: true, 7: true, 8: true, 9: true, 10: true},                                                                // 渔轮
	11: {12: true, 28: true, 29: true, 17: true, 31: true, 33: true, 13: true, 15: true, 35: true, 27: true},                               // 钓线
	13: {12: true, 28: true, 29: true, 17: true, 31: true, 33: true, 35: true},                                                             // 前导
	15: {12: true, 28: true, 29: true, 17: true, 31: true, 33: true, 35: true},                                                             // 编织前导
	35: {12: true, 27: true, 28: true, 29: true, 17: true, 33: true, 31: true, 20: true, 18: true, 19: true, 46: true, 30: true, 38: true}, // 线组主体
	28: {33: true, 29: true},                                                                                                               // 浮漂
	31: {27: true, 18: true, 19: true, 46: true},                                                                                           // 喂食器
	4:  {5: true},                                                                                                                          // 竿柄
}

// fitsSlot 判断物品品类能否装入槽位。
// 槽位或品类未知时放行；同品类放行；其余情况查询可接受集合。
func fitsSlot(itemCat int64, hasItemCat bool, slot int64) bool {
	if !hasItemCat {
		return true
	}
	allowed := slotAccepts[slot]
	if len(allowed) == 0 {
		return true
	}
	return itemCat == slot || allowed[itemCat]
}

// ---- 钓组槽位模板 ----
//
// 规范索引由 (钓组类型, 品类) 决定。例如前导 13 使用 150 或 175，
// 钩 12 使用 250/251/252，拟饵 17 使用 325/326/327。
// 同品类多槽位的规范索引必须唯一；客户端按索引渲染，重复索引会折叠成同一槽位。
// BottomSimple（12）官方就有引线槽：装引线不得触发自动兼容切换。

type rigSlotTemplate struct {
	Cats []int64
	Idx  int64
}

var rigSlotTemplates = map[int64][]rigSlotTemplate{
	1:  {{[]int64{28}, 125}, {[]int64{13}, 150}, {[]int64{12}, 250}},                                                             // FloatSimple
	2:  {{[]int64{28}, 125}, {[]int64{13}, 150}, {[]int64{12}, 250}},                                                             // FloatBolognese
	3:  {{[]int64{28}, 125}, {[]int64{13}, 150}, {[]int64{12}, 250}},                                                             // FloatSlider
	4:  {{[]int64{28}, 125}, {[]int64{13}, 150}, {[]int64{12}, 250}},                                                             // FloatLiveFish
	5:  {{[]int64{13}, 150}},                                                                                                     // Lure（拟饵走挂饵路径）
	6:  {{[]int64{13}, 150}, {[]int64{12}, 250}},                                                                                 // SimpleJig
	7:  {{[]int64{13}, 150}, {[]int64{29}, 200}, {[]int64{33}, 325}, {[]int64{12}, 250}},                                         // Texas
	8:  {{[]int64{13}, 150}, {[]int64{29}, 200}, {[]int64{12}, 250}},                                                             // DropShot
	9:  {{[]int64{29}, 200}, {[]int64{33}, 325}, {[]int64{13}, 150}, {[]int64{12}, 250}},                                         // Carolina
	10: {{[]int64{29}, 200}, {[]int64{13}, 150}, {[]int64{12}, 250}},                                                             // ThreeWay
	11: {{[]int64{13}, 150}, {[]int64{12}, 250}},                                                                                 // Wacky
	12: {{[]int64{47}, 100}, {[]int64{29, 31}, 200}, {[]int64{13}, 150}, {[]int64{12}, 250}},                                     // BottomSimple（沉子/喂食器共用槽；引线槽为官方行为）
	13: {{[]int64{47}, 100}, {[]int64{12}, 250}},                                                                                 // BottomPaternoster
	14: {{[]int64{47}, 100}, {[]int64{12}, 250}},                                                                                 // BottomInline
	15: {{[]int64{47}, 100}, {[]int64{12}, 250}},                                                                                 // BottomLoop（无食笼）
	16: {{[]int64{47}, 100}, {[]int64{57}, 175}, {[]int64{31}, 200}, {[]int64{12}, 250}},                                         // BottomMethod
	17: {{[]int64{47}, 100}, {[]int64{29}, 200}, {[]int64{13}, 150}, {[]int64{12}, 250}},                                         // BottomLiveFish
	18: {{[]int64{38}, 200}},                                                                                                     // BottomFeedRocket
	19: {{[]int64{44}, 125}, {[]int64{13}, 150}},                                                                                 // SbirolinoLure
	20: {{[]int64{44}, 125}, {[]int64{13}, 150}, {[]int64{12}, 250}},                                                             // SbirolinoSoftPlastic
	21: {{[]int64{28}, 125}, {[]int64{13}, 150}, {[]int64{12}, 250}},                                                             // FloatWaggler
	22: {{[]int64{13}, 150}},                                                                                                     // JerkbaitRig
	23: {{[]int64{28}, 125}, {[]int64{29}, 200}},                                                                                 // BottomMarker
	24: {{[]int64{47}, 100}, {[]int64{57}, 175}, {[]int64{29}, 200}, {[]int64{29}, 200}, {[]int64{13}, 150}, {[]int64{12}, 250}}, // BottomHair（鲤族）
	25: {{[]int64{47}, 100}, {[]int64{57}, 175}, {[]int64{29}, 200}, {[]int64{29}, 200}, {[]int64{13}, 150}, {[]int64{12}, 250}}, // BottomSnowman
	26: {{[]int64{47}, 100}, {[]int64{57}, 175}, {[]int64{29}, 200}, {[]int64{29}, 200}, {[]int64{13}, 150}, {[]int64{12}, 250}}, // BottomPopup
	27: {{[]int64{47}, 100}, {[]int64{57}, 175}, {[]int64{29}, 200}, {[]int64{29}, 200}, {[]int64{13}, 150}, {[]int64{12}, 250}}, // BottomHingedStiff
	28: {{[]int64{47}, 100}, {[]int64{57}, 175}, {[]int64{29}, 200}, {[]int64{29}, 200}, {[]int64{13}, 150}, {[]int64{12}, 250}}, // BottomChod
	29: {{[]int64{47}, 100}, {[]int64{57}, 175}, {[]int64{29}, 200}, {[]int64{29}, 200}, {[]int64{13}, 150}, {[]int64{12}, 250}}, // BottomMaggot
	30: {{[]int64{28}, 125}, {[]int64{12}, 250}},                                                                                 // （30 簇）
	31: {{[]int64{28}, 125}, {[]int64{12}, 250}},                                                                                 // FloatCarpPopUp
	32: {{[]int64{47}, 100}, {[]int64{57}, 175}, {[]int64{31}, 200}, {[]int64{12}, 250}},                                         // BottomMethodPopUp
	33: {{[]int64{11}, 0}, {[]int64{69}, 150}, {[]int64{70}, 151}, {[]int64{13}, 152}},                                           // （33 簇，海竿三钩位）
	34: {{[]int64{47}, 100}, {[]int64{57}, 175}, {[]int64{29}, 200}, {[]int64{29}, 200}, {[]int64{13}, 150}, {[]int64{12}, 250}}, // （34 簇）
	35: {{[]int64{13}, 150}},                                                                                                     // （35 簇）
	50: {{[]int64{13}, 175}, {[]int64{17}, 300}, {[]int64{17}, 325}, {[]int64{17}, 326}, {[]int64{12}, 250}},                     // 海竿：柳叶亮片 300 + 基本吸引成分 325 + 钓钩吸引成分 326
	51: {{[]int64{13}, 175}, {[]int64{17}, 300}, {[]int64{17}, 325}, {[]int64{17}, 326}, {[]int64{12}, 250}},
	52: {{[]int64{13}, 175}, {[]int64{17}, 300}, {[]int64{17}, 325}, {[]int64{17}, 326}, {[]int64{12}, 250}},
	53: {{[]int64{13}, 175}, {[]int64{17}, 300}, {[]int64{17}, 325}, {[]int64{17}, 326}},
	54: {{[]int64{13}, 175}, {[]int64{17}, 300}, {[]int64{17}, 325}, {[]int64{17}, 326}},
	55: {{[]int64{13}, 175}, {[]int64{29}, 200}, {[]int64{13}, 150}, {[]int64{17}, 325}, {[]int64{12}, 250}, {[]int64{13}, 151}, {[]int64{17}, 326}, {[]int64{12}, 251}}, // 海竿双路亚
	56: {{[]int64{13}, 175}, {[]int64{29}, 200}, {[]int64{13}, 150}, {[]int64{17}, 325}, {[]int64{12}, 250}, {[]int64{13}, 151}, {[]int64{17}, 326}, {[]int64{12}, 251}},
	57: {{[]int64{13}, 175}, {[]int64{29}, 200}, {[]int64{17}, 325}, {[]int64{17}, 326}, {[]int64{17}, 327}, {[]int64{12, 17}, 252}},
	58: {{[]int64{13}, 175}, {[]int64{29}, 200}, {[]int64{13}, 150}, {[]int64{12}, 250}},
	59: {{[]int64{13}, 175}, {[]int64{29}, 200}, {[]int64{13}, 150}, {[]int64{12}, 250}, {[]int64{12}, 251}, {[]int64{12}, 252}},
}

// canonicalRigIndex 保存钓组类型未知时使用的全局兜底索引。
// 已知品类使用固定索引，其余品类按模板出现顺序补齐。
var canonicalRigIndex = func() map[int64]int64 {
	m := map[int64]int64{28: 125, 12: 250, 27: 350, 17: 300, 13: 150}
	types := make([]int64, 0, len(rigSlotTemplates))
	for rt := range rigSlotTemplates {
		types = append(types, rt)
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
	for _, rt := range types {
		for _, tpl := range rigSlotTemplates[rt] {
			for _, c := range tpl.Cats {
				if _, special := categorySlotField[c]; special {
					continue // 钓线/渔轮走 f3/f4，不进 f5
				}
				if _, ok := m[c]; !ok {
					m[c] = tpl.Idx
				}
			}
		}
	}
	return m
}()

// slotIndicesFor 返回指定钓组类型和品类对应的规范索引列表。
func slotIndicesFor(rigType, category int64) []int64 {
	if category == 27 && rigType != 0 && len(slotIndicesFor(rigType, catHook)) <= 1 {
		return []int64{350, 351}
	}
	tpl := rigSlotTemplates[rigType]
	if len(tpl) == 0 {
		return nil
	}
	var out []int64
	for _, s := range tpl {
		for _, c := range s.Cats {
			if c == category {
				out = append(out, s.Idx)
			}
		}
	}
	return out
}

// uniqueIndices 去掉重复的规范索引。
// 鲤鱼钓组（24-29、34）的模板里有两个 {29,200} 沉子位，索引完全一致。
// 客户端按索引而非数组位置渲染组件，第二个实例会叠在同一个槽位上：
// 重量按两份计（甩线时误判超重、播炸线音效），收起一次也只卸掉其中一个。
// 去重后沉子退回单实例，第二次安装按替换处理。
func uniqueIndices(idxs []int64) []int64 {
	if len(idxs) < 2 {
		return idxs
	}
	seen := make(map[int64]struct{}, len(idxs))
	out := make([]int64, 0, len(idxs))
	for _, idx := range idxs {
		if _, ok := seen[idx]; ok {
			continue
		}
		seen[idx] = struct{}{}
		out = append(out, idx)
	}
	return out
}

// dedupeRigComponents 清理重复规范索引：规范索引唯一标识一个槽位，同一槽位
// 只保留一个组件，其余退回背包或来源堆。
//
// 客户端按规范索引渲染 f5 组件，两个组件落在同一索引上会互相覆盖：
// 轻则重量/耐久算两份，重则整条钓组被判定无效（海钓前导重复挂载时，
// 抛竿后客户端立即收回钓组，表现为饵一入水就回到手上）。
// 一个槽位可声明多个互斥品类（如 rig57 的 {12,17}@252，钩与拟饵互斥），
// 所以去重键是规范索引本身，而不是 (品类, 索引)。
// 冲突时优先保留"原始 Extra 已是当前模板合法索引"的组件（target-valid），
// 其次按 (品类, ordinal) 顺序——换钓组类型后保留的是当前有效组件，
// 而不是旧类型残留的低 ordinal 组件。
// 模板未声明的品类（钩饵 27、路亚/海钓拟饵 17 等）不参与去重，
// 它们的多实例由钩位序和挂饵方式决定。
// collect 可选：传入切片指针时，每个被退回组件的去向（rigReleased）会追加进去，
// 供调用方并入响应快照/安排来源堆刷新；交互路径必须收集，启动回填可忽略。
func (s *Server) dedupeRigComponents(ctx context.Context, uid int64, parentGUID string, rigType int64, comps map[int64]rigComp, collect ...*[]*rigReleased) bool {
	keys := make([]int64, 0, len(comps))
	for key := range comps {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		ci, cj := rigCompCategory(keys[i]), rigCompCategory(keys[j])
		if ci != cj {
			return ci < cj
		}
		return rigCompOrdinal(keys[i]) < rigCompOrdinal(keys[j])
	})
	type keptSlot struct {
		key         int64
		guid        string
		targetValid bool
	}
	seen := map[int64]keptSlot{}
	changed := false
	for _, key := range keys {
		comp := comps[key]
		category, ordinal := rigCompCategory(key), rigCompOrdinal(key)
		// 模板没有声明该品类时保留原样：钩饵按钩位序编号，
		// 拟饵的多实例由钓组模板的多个索引（325/326/327）区分。
		if len(slotIndicesFor(rigType, category)) == 0 {
			continue
		}
		index := canonicalIndexForOrdinal(category, comp.Extra, rigType, ordinal)
		targetValid := extraIsTargetIndex(category, comp.Extra, rigType)
		if kept, exists := seen[index]; exists {
			if targetValid && !kept.targetValid {
				// 当前组件才是当前模板合法索引，替换掉旧的。
				delete(comps, kept.key)
				if kept.guid != "" && kept.guid != comp.GUID {
					s.releaseDedupedComponent(ctx, uid, parentGUID, kept.guid, collect...)
				}
				seen[index] = keptSlot{key: key, guid: comp.GUID, targetValid: targetValid}
			} else {
				// 都有效/都无效时保留先遍历到的（低品类、低 ordinal）。
				delete(comps, key)
				if comp.GUID != "" && comp.GUID != kept.guid {
					s.releaseDedupedComponent(ctx, uid, parentGUID, comp.GUID, collect...)
				}
			}
			changed = true
			continue
		}
		seen[index] = keptSlot{key: key, guid: comp.GUID, targetValid: targetValid}
	}
	return changed
}

// extraIsTargetIndex 判断组件当前 Extra 是否已是钓组模板声明的合法槽位索引。
// 海钓钩位的 1000+ 客户端槽位索引不属于模板，但同样是客户端认定的有效槽位，
// 必须一并认作合法，否则 rigSetTarget 会把它当成无效索引走「找空闲序号」分支。
func extraIsTargetIndex(category, extra, rigType int64) bool {
	if hookSlotIndexNamespace(category, extra) {
		return true
	}
	for _, idx := range slotIndicesFor(rigType, category) {
		if extra == idx {
			return true
		}
	}
	return false
}

// hookSlotIndexNamespace 判断 (品类, 索引) 是否落在客户端的海钓钩位子槽命名空间。
//
// 客户端 RigMarineBase.faocjcaiac 从 1009 起每次 −4 遍历到 1001 构造钩组件，
// 因此 1000 基址上一组钩位是 1001/1005/1009。实机日志进一步确认每组钩位下
// 还有子槽：首钩位加钩上报 1001、首钩位装拟饵上报 1002（2026-11-09 20:57:37
// slot=17 extra=1002）；同一族里 1006/1010 是第二/第三钩位的拟饵子槽。
//
// 拟饵(17)必须一起放行：只放行钩(12) 时，1002 会被 canonicalIndexForOrdinal
// 按 ordinal 改写成 rig 50 的模板首索引 300，与柳叶亮片的 300 撞车——亮片被
// 去重退回背包，客户端在自己的 1002 槽位找不到组件，整组判无效（钩子、鱼片、
// 吸引成分一起变黑）。这是 2026-11-09 实机事故的直接原因。
func hookSlotIndexNamespace(category, extra int64) bool {
	if category != catHook && category != catLure {
		return false
	}
	return extra >= 1000
}

// releaseDedupedComponent 将被清理的组件实例退回背包或来源堆，
// collect 语义与 dedupeRigComponents 相同。
func (s *Server) releaseDedupedComponent(ctx context.Context, uid int64, parentGUID, guid string, collect ...*[]*rigReleased) {
	if guid == "" {
		return
	}
	row, err := s.DB.GetItemByGUID(ctx, guid)
	if err == nil && row != nil && row.UID == uid && row.Container == "rig" && row.RigGUID == parentGUID {
		rel := s.releaseRigComponentItem(ctx, row)
		if rel != nil {
			for _, c := range collect {
				if c != nil {
					*c = append(*c, rel)
				}
			}
		}
	}
}

// rigReleased 描述一个被去重清理的组件的去向：
//   - Returned=true：普通物品退回背包，ReturnTree 是退回后的新树（快照 f1 素材）；
//   - Returned=false：拆分子实例被删除、来源堆 +1，SourceGUID/SourceTree 用于
//     回包后的来源堆刷新（4/21 移除旧栈 + 4/22 加入新树）。
type rigReleased struct {
	GUID       string
	Returned   bool
	ReturnTree *proto.Value
	SourceGUID string
	SourceTree *proto.Value
}

// releasedReturnTrees 返回退回背包物品的新树（快照 f1 更新段素材）。
func releasedReturnTrees(released []*rigReleased) []*proto.Value {
	var out []*proto.Value
	for _, rel := range released {
		if rel != nil && rel.Returned && rel.ReturnTree != nil {
			out = append(out, rel.ReturnTree)
		}
	}
	return out
}

// queueReleasedStackRefresh 安排拆分子实例来源堆的回包刷新。
// 客户端只对「移除 + 新增」更新背包数量，同 guid 的数量更新会被忽略，
// 因此必须先 4/21 移除旧栈，再 4/22 加入新数量的树。
func (s *Server) queueReleasedStackRefresh(ctx *Ctx, released []*rigReleased) {
	if ctx == nil {
		return
	}
	var guids []string
	var trees []*proto.Value
	seen := map[string]bool{}
	for _, rel := range released {
		if rel == nil || rel.SourceGUID == "" || rel.SourceTree == nil || seen[rel.SourceGUID] {
			continue
		}
		seen[rel.SourceGUID] = true
		guids = append(guids, rel.SourceGUID)
		trees = append(trees, rel.SourceTree)
	}
	if len(guids) > 0 {
		s.queueStackRefresh(ctx, guids, trees)
	}
}

// releaseRigComponentItem 将一个被清理的组件实例退回背包或来源堆，
// 并返回去向描述供调用方并入响应快照/安排刷新。
func (s *Server) releaseRigComponentItem(ctx context.Context, row *store.Item) *rigReleased {
	if s.refundableSplitChild(ctx, row) {
		srcGUID := splitSourceGUID(row)
		_ = s.DB.DeleteItems(ctx, []string{row.GUID})
		rel := &rigReleased{GUID: row.GUID}
		if srcGUID != "" {
			rel.SourceGUID = srcGUID
			if src, err := s.DB.GetItemByGUID(ctx, srcGUID); err == nil && src != nil {
				s.setStackCount(ctx, src, s.stackCountOf(ctx, src)+1)
				if t, terr := s.itemTree(ctx, src); terr == nil {
					rel.SourceTree = t
				}
			}
		}
		return rel
	}
	s.returnToBackpack(ctx, row)
	rel := &rigReleased{GUID: row.GUID, Returned: true}
	if fresh, err := s.DB.GetItemByGUID(ctx, row.GUID); err == nil && fresh != nil {
		if t, terr := s.itemTree(ctx, fresh); terr == nil {
			rel.ReturnTree = t
		}
	}
	return rel
}

// canonicalIndex 返回品类在 f5 组件中的规范索引。
// 客户端给出的合法槽位索引会保留；其余情况使用模板规范值。
func canonicalIndex(category, clientIndex int64, rigType int64) int64 {
	c := category
	if rigType != 0 {
		if idxs := slotIndicesFor(rigType, c); len(idxs) > 0 {
			for _, idx := range idxs {
				if clientIndex == idx {
					return idx
				}
			}
			return idxs[0]
		}
	}
	if idx, ok := canonicalRigIndex[c]; ok {
		return idx
	}
	return clientIndex
}

// canonicalIndexForOrdinal 返回同品类第 ordinal 个实例的规范索引。
// 合法客户端索引优先，否则使用模板中对应序号的槽。
func canonicalIndexForOrdinal(category, clientIndex, rigType, ordinal int64) int64 {
	if category == 27 {
		if ordinal == 1 && len(slotIndicesFor(rigType, catHook)) <= 1 {
			return 351
		}
		return 350
	}
	// 客户端对海钓钩位使用独立的 1000+ 槽位命名空间（实机日志实测
	// 1001/1005/1009 钩位，1002 首钩位拟饵子槽）。它们不属于
	// rigSlotTemplates 里声明的 250/251/252 或 300/325/326，
	// 但客户端按自己的索引渲染：改写成模板首索引会让客户端在自己的槽位上
	// 找不到组件，整组判无效（槽位变灰、钩装不上）。这类索引必须原样保留。
	// 只放行钩(12) 会把拟饵的 1002 改写成 300，与柳叶亮片撞车并把它挤掉，
	// 所以拟饵(17)必须一并放行；其他品类仍走模板规范化。
	if hookSlotIndexNamespace(category, clientIndex) {
		return clientIndex
	}
	if rigType != 0 {
		idxs := slotIndicesFor(rigType, category)
		for _, idx := range idxs {
			if clientIndex == idx {
				return idx
			}
		}
		if ordinal >= 0 && ordinal < int64(len(idxs)) {
			return idxs[ordinal]
		}
	}
	return canonicalIndex(category, clientIndex, rigType)
}

// rigComp 使用 (category, ordinal) 复合键：低 32 位是品类，高 32 位是序号。
const rigCompOrdinalShift = 32

func rigCompKey(category, ordinal int64) int64 {
	return (ordinal << rigCompOrdinalShift) | (category & 0xffffffff)
}

func rigCompCategory(key int64) int64 { return key & 0xffffffff }

func rigCompOrdinal(key int64) int64 { return int64(uint64(key) >> rigCompOrdinalShift) }

// ---- 153 钓组对象的读/写（item_factory 侧）----

// rigComp f5 组件表里的一条 {(品类,ordinal): (组件guid, extra)}。
type rigComp struct {
	Category int64
	Ordinal  int64
	GUID     string
	Extra    int64
}

// rigCompsOf 从竿实例 f5 取出组件 map（item_factory._rig_components_of）。
// 结构：gbbhdkefnkn.f2 = args('81')[] → gcfcdnfmnbh{槽位, guid, extra}。
func rigCompsOf(f5 *proto.Value) map[int64]rigComp {
	out := map[int64]rigComp{}
	if f5 == nil || f5.Kind != proto.KindObj || f5.FieldCount() <= 2 {
		return out
	}
	comps := f5.Fields[2].Val
	if comps == nil {
		return out
	}
	_, rigType, _ := rigIdentity(f5)
	singleHook := len(slotIndicesFor(rigType, catHook)) <= 1
	counts := map[int64]int64{}
	for _, it := range comps.Items {
		if it == nil || it.Kind != proto.KindObj {
			continue
		}
		f0, f1, f2 := it.Field(0), it.Field(1), it.Field(2)
		g := f1.GuidHex()
		if g == "" {
			continue
		}
		cat := int64(f0.U32())
		ordinal := counts[cat]
		if cat == 27 && singleHook {
			switch f2.U32() {
			case 351:
				ordinal = 1
			case 350:
				if _, occupied := out[rigCompKey(cat, 0)]; !occupied {
					ordinal = 0
				}
			}
		}
		counts[cat] = ordinal + 1
		out[rigCompKey(cat, ordinal)] = rigComp{
			Category: cat, Ordinal: ordinal, GUID: g, Extra: int64(f2.U32()),
		}
	}
	return out
}

// rigCompKeysOfCategory 返回该品类全部实例键，按 ordinal 升序排列。
func rigCompKeysOfCategory(comps map[int64]rigComp, category int64) []int64 {
	var out []int64
	for key := range comps {
		if rigCompCategory(key) == category {
			out = append(out, key)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return rigCompOrdinal(out[i]) < rigCompOrdinal(out[j])
	})
	return out
}

func deleteRigCategory(comps map[int64]rigComp, category int64) int {
	n := 0
	for _, key := range rigCompKeysOfCategory(comps, category) {
		delete(comps, key)
		n++
	}
	return n
}

func rigCompKeyForGUID(comps map[int64]rigComp, guid string) (int64, rigComp, bool) {
	for key, comp := range comps {
		if comp.GUID == guid {
			return key, comp, true
		}
	}
	return 0, rigComp{}, false
}

// rigIdentity 从竿实例 f5 读取钓组名和类型。
// 类型缺失时 typeOK 为 false，调用方可按竿型派生默认值。
func rigIdentity(f5 *proto.Value) (name string, rigType int64, typeOK bool) {
	if f5 == nil || f5.Kind != proto.KindObj {
		return "", 0, false
	}
	f0 := f5.Field(0)
	name = f0.StrOr()
	if f5.FieldCount() > 1 && f5.Fields[1].Num != nil {
		return name, int64(u64Of(f5.Fields[1].Num)), true
	}
	return name, 0, false
}

// rigDepthOf 从 f5 读取钓棚深度，单位厘米；值不大于 0 时返回 default。
func rigDepthOf(f5 *proto.Value, def int64) int64 {
	if f5 != nil && f5.Kind == proto.KindObj && f5.FieldCount() > 4 && f5.Fields[4].Num != nil {
		if v := int64(u64Of(f5.Fields[4].Num)); v > 0 {
			return v
		}
	}
	return def
}

// defaultDepthFor 返回钓组类型的默认钓棚深度：路亚为 0，其余为 100。
func defaultDepthFor(name string, rigType int64) int64 {
	if name == rigNameLure || (rigType != 0 && rigType == rigTypeLure) {
		return 0
	}
	return defaultRigDepthCM
}

// rigNameForType 返回钓组类型对应的 rig_* 资源名；未知类型返回空字符串。
func rigNameForType(rigType int64) string {
	return rigTypeNames[rigType]
}

// rigCompObj 构造 152 组件对象：槽位、物品 GUID 和附加索引。
func rigCompObj(slot int64, c rigComp) *proto.Value {
	return wiregen.MustMakeObj("gcfcdnfmnbh", map[int]*proto.Value{
		0: proto.U32(uint32(slot)),
		1: proto.Guid(c.GUID),
		2: proto.U32(uint32(c.Extra)),
	})
}

// sortedRigCompItems 按规范索引升序排列组件，并过滤不应进入 f5 的品类。
// 钓线和渔轮使用专用字段；沉底钓组不保留浮漂组件。
func sortedRigCompItems(comps map[int64]rigComp, rigType int64) []*proto.Value {
	keys := make([]int64, 0, len(comps))
	for k := range comps {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		catI, ordI := rigCompCategory(keys[i]), rigCompOrdinal(keys[i])
		catJ, ordJ := rigCompCategory(keys[j]), rigCompOrdinal(keys[j])
		ci := canonicalIndexForOrdinal(catI, comps[keys[i]].Extra, rigType, ordI)
		cj := canonicalIndexForOrdinal(catJ, comps[keys[j]].Extra, rigType, ordJ)
		if ci != cj {
			return ci < cj
		}
		if catI != catJ {
			return catI < catJ
		}
		if ordI != ordJ {
			return ordI < ordJ
		}
		return comps[keys[i]].GUID < comps[keys[j]].GUID
	})
	out := make([]*proto.Value, 0, len(keys))
	for _, k := range keys {
		cat, ordinal := rigCompCategory(k), rigCompOrdinal(k)
		if _, special := categorySlotField[cat]; special {
			continue
		}
		if bottomRigTypes[rigType] && cat == catFloat {
			continue
		}
		c := comps[k]
		c.Extra = canonicalIndexForOrdinal(cat, c.Extra, rigType, ordinal)
		out = append(out, rigCompObj(cat, c))
	}
	return out
}

// buildRig153 构造 153 钓组对象。
// f0 是 rig_* 资源名，f1 是类型，f2 是 args(81) 组件列表；即使为空也必须保留
// args 容器，否则客户端无法解析。f4 是钓棚深度。
func buildRig153(name string, rigType int64, comps map[int64]rigComp, depth int64) *proto.Value {
	return buildRig153WithClip(name, rigType, comps, depth, 0)
}

// ---- 字段写入辅助 ----

// bumpCreatedTicks 将版本戳提高到严格大于当前值。
// 客户端只接受版本戳变大的 4/22 更新，否则会忽略修改。
func bumpCreatedTicks(fields []proto.Field, wireClass string) {
	idx := fieldIdxByCS(wireClass, csCreated)
	if idx < 0 || idx >= len(fields) {
		return
	}
	old := u64Of(fields[idx].Num)
	now := uint64(netTicksNow())
	if now > old {
		fields[idx].Num = ptrU64(now)
	} else {
		fields[idx].Num = ptrU64(old + 1)
	}
}

// saveItemFields 写回字段数组并提高版本戳；其余物品列保持不变。
func (s *Server) saveItemFields(ctx context.Context, row *store.Item, fields []proto.Field) {
	bumpCreatedTicks(fields, row.WireClass)
	fj, err := FieldsToJSON(fields)
	if err != nil {
		return
	}
	_ = s.DB.UpdateItemFields(ctx, row.GUID, fj, row.Qty, row.Durability, row.Workmanship, row.RigGUID)
}

// BackfillRigProjections 以竿实例的 f5/f3/f4 为真源，重建全部 rig_component 派生行。
//
// 历史数据只把装配结果写进了实例字段，派生表为空时修理面板拿不到任何可修组件
// （零件列表与报价都是空）。启动时跑一遍，幂等；失败逐条告警，不再静默丢弃。
func (s *Server) BackfillRigProjections(ctx context.Context) (int, error) {
	if s.DB == nil {
		return 0, fmt.Errorf("数据库未连接")
	}
	rows, err := s.DB.SQL.QueryContext(ctx,
		`SELECT guid FROM item WHERE wire_class=?`, "cnolibojkao")
	if err != nil {
		return 0, err
	}
	var guids []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			rows.Close()
			return 0, err
		}
		guids = append(guids, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	done := 0
	for _, g := range guids {
		if err := s.reconcileRigProjection(ctx, g); err != nil {
			s.log.Warn("装配投影回填失败", "rod", shortGUID(g), "err", err)
			continue
		}
		done++
	}
	return done, nil
}

// reconcileRigProjection 以实例 f5、f3 和 f4 为数据源，全量重建 rig_component。
// 派生表不反向合并，避免换类型或拆卸后留下失效索引。
// collect 语义与 dedupeRigComponents 相同：交互路径传入以收集被退回的组件。
func (s *Server) reconcileRigProjection(ctx context.Context, rodGUID string, collect ...*[]*rigReleased) error {
	row, err := s.DB.GetItemByGUID(ctx, rodGUID)
	if err != nil || row == nil {
		return err
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return err
	}
	idx := fieldIdxByCS(row.WireClass, csRigObject)
	if idx < 0 || idx >= len(fields) {
		return nil // 非竿容器：由调用方走各自旧投影逻辑
	}
	stored := fields[idx].Val
	name, rigType, _ := rigIdentity(stored)
	comps := rigCompsOf(stored)
	if s.dedupeRigComponents(ctx, row.UID, rodGUID, rigType, comps, collect...) {
		depth := rigDepthOf(stored, defaultDepthFor(name, rigType))
		fields[idx].Val = buildRig153Like(stored, name, rigType, comps, depth, rigClipOf(stored))
		s.saveItemFields(ctx, row, fields)
	}
	out := make([]store.RigComponent, 0, len(comps)+2)
	keys := make([]int64, 0, len(comps))
	for key := range comps {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		ci, cj := rigCompCategory(keys[i]), rigCompCategory(keys[j])
		if ci != cj {
			return ci < cj
		}
		return rigCompOrdinal(keys[i]) < rigCompOrdinal(keys[j])
	})
	for _, key := range keys {
		c := comps[key]
		if c.GUID == "" {
			continue
		}
		cat, ordinal := rigCompCategory(key), rigCompOrdinal(key)
		if _, special := categorySlotField[cat]; special {
			continue // 线/轮从 f3/f4 补，保持与客户端 153 结构一致
		}
		out = append(out, store.RigComponent{
			Slot: cat, Ordinal: ordinal, GUID: c.GUID,
			Extra: canonicalIndexForOrdinal(cat, c.Extra, rigType, ordinal),
		})
	}
	for _, cat := range []int64{catReel, catLine} {
		csName := categorySlotField[cat]
		sidx := fieldIdxByCS(row.WireClass, csName)
		if sidx < 0 || sidx >= len(fields) || !validRigGUID(fields[sidx].Hex) {
			continue
		}
		out = append(out, store.RigComponent{Slot: cat, Ordinal: 0, GUID: fields[sidx].Hex})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Extra != out[j].Extra {
			return out[i].Extra < out[j].Extra
		}
		if out[i].Slot != out[j].Slot {
			return out[i].Slot < out[j].Slot
		}
		return out[i].Ordinal < out[j].Ordinal
	})
	return s.DB.ReplaceRigComponents(ctx, rodGUID, out)
}

// cloneFields 深拷贝字段数组，供拆分子实例使用。
func cloneFields(fields []proto.Field) []proto.Field {
	fj, err := FieldsToJSON(fields)
	if err != nil {
		return nil
	}
	out, err := FieldsFromJSON(fj)
	if err != nil {
		return nil
	}
	return out
}

// validRigGUID 判断 GUID 非空且不是全零。
func validRigGUID(g string) bool {
	return g != "" && g != zeroGUID
}

// itemTree 将物品行转换为 wire 树。
func (s *Server) itemTree(ctx context.Context, row *store.Item) (*proto.Value, error) {
	return s.ItemToTree(ctx, row)
}

// itemCategoryOf 返回物品定义中的品类；未知时返回 (0,false)。
func (s *Server) itemCategoryOf(ctx context.Context, it *store.Item) (int64, bool) {
	def, err := s.DB.GetItemDef(ctx, it.NumericID)
	if err != nil || def == nil {
		return 0, false
	}
	return def.Category, true
}

// stackableItem 判断物品是否按个数堆叠。
// 钓线和前导虽然也有相关字段，但该字段表示长度而不是数量。
func (s *Server) stackableItem(ctx context.Context, it *store.Item) bool {
	def, err := s.DB.GetItemDef(ctx, it.NumericID)
	if err != nil || def == nil {
		return false
	}
	return countableCategories[def.Category]
}

// stackCountOf 返回计件物品的当前数量；非计件或缺失时返回 1。
func (s *Server) stackCountOf(ctx context.Context, it *store.Item) int64 {
	fields, err := FieldsFromJSON(it.FieldsJSON)
	if err != nil {
		return 1
	}
	q := fieldIdxByCS(it.WireClass, csQty)
	if q < 0 || q >= len(fields) {
		// 字段缺失时使用定义中的默认数量，仍缺失则返回 1。
		if def, derr := s.DB.GetItemDef(ctx, it.NumericID); derr == nil && def != nil {
			if v, ok := defAttrU64(def, csQty); ok && v > 1 {
				return int64(v)
			}
		}
		return 1
	}
	if v := int64(u64Of(fields[q].Num)); v > 1 {
		return v
	}
	return 1
}

// setStackCount 同步修改计件物品的字段数量和 qty 列。
func (s *Server) setStackCount(ctx context.Context, row *store.Item, count int64) {
	if count < 0 {
		count = 0
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return
	}
	if q := fieldIdxByCS(row.WireClass, csQty); q >= 0 && q < len(fields) {
		fields[q].Num = ptrU64(uint64(count))
	}
	bumpCreatedTicks(fields, row.WireClass)
	fj, jerr := FieldsToJSON(fields)
	if jerr != nil {
		return
	}
	_ = s.DB.UpdateItemFields(ctx, row.GUID, fj, count, row.Durability, row.Workmanship, row.RigGUID)
}

// setStackCountTx 是 setStackCount 的事务内版本：断件/拆组时把子实例的
// 数量并回来源堆，必须与删除子实例同事务提交。
func (s *Server) setStackCountTx(ctx context.Context, tx *sql.Tx, row *store.Item, count int64) error {
	if row == nil || row.GUID == "" {
		return nil
	}
	if count < 0 {
		count = 0
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return err
	}
	if q := fieldIdxByCS(row.WireClass, csQty); q >= 0 && q < len(fields) {
		fields[q].Num = ptrU64(uint64(count))
	}
	bumpCreatedTicks(fields, row.WireClass)
	fj, jerr := FieldsToJSON(fields)
	if jerr != nil {
		return jerr
	}
	if err := s.DB.UpdateItemFieldsTx(ctx, tx, row.GUID, fj, count, row.Durability, row.Workmanship, row.RigGUID); err != nil {
		return err
	}
	row.FieldsJSON = fj
	row.Qty = count
	return nil
}

// setWireParent 写入实例的父 GUID。客户端据此判断挂载归属；
// 退回背包时必须清空，否则物品仍显示为已挂载。
func (s *Server) setWireParent(ctx context.Context, row *store.Item, parentGUID string) {
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return
	}
	p := fieldIdxByCS(row.WireClass, csParent)
	if p < 0 || p >= len(fields) || (fields[p].Op != "OPTGUID" && fields[p].Op != "GUID16") {
		return // 字段不存在或类型不符时不修改
	}
	fields[p].Hex = parentGUID
	if sidx := fieldIdxByCS(row.WireClass, csSlotEnum); sidx >= 0 && fieldOpAt(row.WireClass, sidx) == "BYTE" {
		if parentGUID == "" {
			fields[sidx].Num = ptrU64(0) // 背包
		} else {
			fields[sidx].Num = ptrU64(4) // rig
		}
	}
	s.saveItemFields(ctx, row, fields)
}

// clearRigFieldsForBackpack 清理物品快照中的装配引用，使客户端按背包物品解析。
// 调用方负责落库；事务路径随后由 updateItemFieldsTx 更新时间戳。
func clearRigFieldsForBackpack(fields []proto.Field, wireClass string) {
	if p := fieldIdxByCS(wireClass, csParent); p >= 0 && p < len(fields) &&
		(fields[p].Op == "OPTGUID" || fields[p].Op == "GUID16") {
		fields[p].Hex = ""
	}
	if source := fieldIdxByCS(wireClass, csSplitSource); source >= 0 && source < len(fields) {
		fields[source].Hex = ""
	}
	if sidx := fieldIdxByCS(wireClass, csSlotEnum); sidx >= 0 && fieldOpAt(wireClass, sidx) == "BYTE" {
		fields[sidx].Num = ptrU64(rootItemStorageKind(wireClass))
	}
}

// returnToBackpack 将普通物品退回背包，并清空 rig_guid 和 wire 父引用。
// 父引用不清空时，客户端会继续把物品视为已挂载。
// 存储更新统一走 DetachReturnToBackpack，字段更新后再整体落库。
func (s *Server) returnToBackpack(ctx context.Context, row *store.Item) {
	_ = s.DB.DetachReturnToBackpack(ctx, row.GUID)
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return
	}
	clearRigFieldsForBackpack(fields, row.WireClass)
	bumpCreatedTicks(fields, row.WireClass)
	fj, jerr := FieldsToJSON(fields)
	if jerr != nil {
		return
	}
	_ = s.DB.UpdateItemFields(ctx, row.GUID, fj, row.Qty, row.Durability, row.Workmanship, "")
}

// refundableSplitChild 只向仍在背包的同类来源堆退回数量。
func (s *Server) refundableSplitChild(ctx context.Context, row *store.Item) bool {
	if !s.isSplitChild(ctx, row) {
		return false
	}
	source, err := s.DB.GetItemByGUID(ctx, splitSourceGUID(row))
	return err == nil && source != nil && source.UID == row.UID && source.NumericID == row.NumericID && source.Container == "backpack" && source.RigGUID == "" && source.Qty > 0
}

// splitSourceGUID 返回拆分子实例的来源堆 GUID。
func splitSourceGUID(row *store.Item) string {
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return ""
	}
	i := fieldIdxByCS(row.WireClass, csSplitSource)
	if i < 0 || i >= len(fields) {
		return ""
	}
	if fields[i].Op != "OPTGUID" && fields[i].Op != "GUID16" {
		return ""
	}
	return fields[i].Hex
}

// isSplitChild 判断实例是否为计件物品拆分出的挂载子实例：
// 计件品类且携带来源堆 GUID（数量可为挂饵方式拆出的多份）。
func (s *Server) isSplitChild(ctx context.Context, row *store.Item) bool {
	if row == nil || !s.stackableItem(ctx, row) {
		return false
	}
	return len(splitSourceGUID(row)) == 32
}

// splitMountableChild 将计件物品拆出 n 份挂载子实例（n=挂饵方式的一钩份数，
// 见 baitHookCount），返回 (子实例 GUID, 已删除来源堆 GUID)。
//   - 来源堆仍有剩余：保留在背包、数量减 n，子实例携带来源堆引用（f2），
//     同种饵还能挂到其他竿上；
//   - 整堆上钩：删除来源堆并清掉子实例的来源堆引用（与 materializeHanditemChild
//     同款语义，避免客户端移除来源堆时级联删除钩上子实例）。
//
// 子实例自身 GUID 字段必须改成新 GUID，否则客户端索引冲突。
func (s *Server) splitMountableChild(ctx context.Context, uid int64, parentGUID, itemGUID string, n int64) (string, string) {
	row, err := s.DB.GetItemByGUID(ctx, itemGUID)
	if err != nil || row == nil {
		return itemGUID, ""
	}
	if !s.stackableItem(ctx, row) {
		return itemGUID, "" // 非计件物品（钓线的同名字段是"米数"）不拆，整件挂装
	}
	count := s.stackCountOf(ctx, row)
	if count <= 1 {
		return itemGUID, ""
	}
	if n < 1 {
		n = 1
	}
	if n > count {
		n = count
	}
	fullTake := n >= count
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return itemGUID, ""
	}
	childFields := cloneFields(fields)
	if childFields == nil {
		return itemGUID, ""
	}
	childGUID := NewItemGUID()
	if q := fieldIdxByCS(row.WireClass, csQty); q >= 0 && q < len(childFields) {
		childFields[q].Num = ptrU64(uint64(n)) // 数量=一钩份数
	}
	if p := fieldIdxByCS(row.WireClass, csParent); p >= 0 && p < len(childFields) {
		childFields[p].Hex = parentGUID // parent=竿
	}
	if si := fieldIdxByCS(row.WireClass, csSplitSource); si >= 0 && si < len(childFields) {
		if fullTake {
			childFields[si].Hex = "" // 整堆上钩不写来源堆引用（见函数头注）
		} else {
			childFields[si].Hex = itemGUID // f2=来源堆 guid
		}
	}
	if c := fieldIdxByCS(row.WireClass, csCreated); c >= 0 && c < len(childFields) {
		childFields[c].Num = ptrU64(uint64(netTicksNow())) // 创建时间=现在
	}
	if g := fieldIdxByCS(row.WireClass, csGUID); g >= 0 && g < len(childFields) {
		childFields[g].Hex = childGUID // 嵌入 guid 必须=自己的新 guid
	}
	fj, jerr := FieldsToJSON(childFields)
	if jerr != nil {
		return itemGUID, ""
	}
	child := &store.Item{
		GUID: childGUID, UID: uid, ItemID: row.ItemID, NumericID: row.NumericID,
		WireClass: row.WireClass, TypeID: row.TypeID, Container: "rig", Slot: 0,
		RigGUID: parentGUID, Qty: n, FieldsJSON: fj,
		Durability: row.Durability, Workmanship: row.Workmanship,
		CreatedTS: float64(time.Now().Unix()),
	}
	if err := s.DB.InsertItem(ctx, child); err != nil {
		s.log.Warn("计件挂载拆分失败", "item", shortGUID(itemGUID), "err", err)
		return itemGUID, ""
	}
	if err := s.cloneGroundbaitRecipe(ctx, itemGUID, childGUID); err != nil {
		_ = s.DB.DeleteItems(ctx, []string{childGUID})
		return itemGUID, ""
	}
	if fullTake {
		_ = s.DB.DeleteItems(ctx, []string{itemGUID})
		s.log.Info("计件物品整堆上钩", "item", shortGUID(itemGUID), "count", count,
			"child", shortGUID(childGUID), "parent", shortGUID(parentGUID))
		return childGUID, itemGUID
	}
	// 来源堆数量减 n，剩余留在背包（调用方负责推更新树）。
	s.setStackCount(ctx, row, count-n)
	s.log.Info("计件物品挂载拆分", "item", shortGUID(itemGUID), "count", count, "take", n,
		"child", shortGUID(childGUID), "parent", shortGUID(parentGUID))
	return childGUID, ""
}

// materializeHanditemChild 将装填物搬入新的子实例。缺省只装 1 份
// （handitem_load_one=1），剩余留在背包；弹弓客户端按装填数量逐颗渲染并抛出
// （反编译 0xdf2db0 刷新按数量建球进皮兜、0xdf5230 抛投遍历整袋逐颗发射），
// 整堆装填会一次抛出一大堆而背包只扣 1。抛完由 reloadHanditemAfterThrow
// 自动从背包补 1 份。handitem_load_one=0 回退整堆装填（来源堆删除）。
// 返回新子实例 GUID 和已删除的来源堆 GUID（仅整堆搬走时非空）。
// 必须创建新物品，客户端只在创建时建立父子归属。子实例不写来源堆引用，
// 换弹时旧子实例按普通物品退回背包（不被 refundableSplitChild 回收）。
func (s *Server) materializeHanditemChild(ctx context.Context, uid int64, parentGUID, itemGUID string) (string, string) {
	row, err := s.DB.GetItemByGUID(ctx, itemGUID)
	if err != nil || row == nil {
		return itemGUID, ""
	}
	if !s.stackableItem(ctx, row) {
		return itemGUID, "" // 非计件（单件工具）：就地挂，没有可搬的堆
	}
	count := s.stackCountOf(ctx, row)
	if count < 1 {
		return itemGUID, ""
	}
	load := count
	if s.tunableF("handitem_load_one", 1) != 0 {
		load = 1 // 弹弓只装 1 颗 ready 球，剩余留背包，抛完自动补 1
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return itemGUID, ""
	}
	childFields := cloneFields(fields)
	if childFields == nil {
		return itemGUID, ""
	}
	childGUID := NewItemGUID()
	if q := fieldIdxByCS(row.WireClass, csQty); q >= 0 && q < len(childFields) {
		childFields[q].Num = ptrU64(uint64(load)) // 装填份数（缺省 1，整堆模式为 count）
	}
	if p := fieldIdxByCS(row.WireClass, csParent); p >= 0 && p < len(childFields) {
		childFields[p].Hex = parentGUID
	}
	if si := fieldIdxByCS(row.WireClass, csSplitSource); si >= 0 && si < len(childFields) {
		childFields[si].Hex = "" // ⚠️ 不写 f2 来源堆引用（来源堆已删除，见上）
	}
	if c := fieldIdxByCS(row.WireClass, csCreated); c >= 0 && c < len(childFields) {
		childFields[c].Num = ptrU64(uint64(netTicksNow()))
	}
	if g := fieldIdxByCS(row.WireClass, csGUID); g >= 0 && g < len(childFields) {
		childFields[g].Hex = childGUID
	}
	fj, jerr := FieldsToJSON(childFields)
	if jerr != nil {
		return itemGUID, ""
	}
	child := &store.Item{
		GUID: childGUID, UID: uid, ItemID: row.ItemID, NumericID: row.NumericID,
		WireClass: row.WireClass, TypeID: row.TypeID, Container: "rig", Slot: 0,
		RigGUID: parentGUID, Qty: load, FieldsJSON: fj,
		Durability: row.Durability, Workmanship: row.Workmanship,
		CreatedTS: float64(time.Now().Unix()),
	}
	if err := s.DB.InsertItem(ctx, child); err != nil {
		s.log.Warn("手持物装填失败", "item", shortGUID(itemGUID), "err", err)
		return itemGUID, ""
	}
	if err := s.cloneGroundbaitRecipe(ctx, itemGUID, childGUID); err != nil {
		_ = s.DB.DeleteItems(ctx, []string{childGUID})
		return itemGUID, ""
	}
	if load >= count {
		// 整堆搬走（整堆模式，或来源堆只剩这一份）：删除来源堆，
		// 否则同一批饵会显示两次。
		_ = s.DB.DeleteItems(ctx, []string{itemGUID})
		s.log.Info("手持物装填：整堆搬进新子实例", "src", shortGUID(itemGUID),
			"count", count, "child", shortGUID(childGUID), "parent", shortGUID(parentGUID))
		return childGUID, itemGUID
	}
	// 只装 1 份：剩余留在背包，数量减 load（调用方负责推更新树刷新计数）。
	s.setStackCount(ctx, row, count-load)
	s.log.Info("手持物装填：只装一份，剩余留背包", "src", shortGUID(itemGUID),
		"count", count, "load", load, "child", shortGUID(childGUID), "parent", shortGUID(parentGUID))
	return childGUID, ""
}

// setHanditemLoadedRef 将手持工具字段 f1 设为当前装填物 GUID；空字符串表示清空。
// 客户端通过该字段判断装填状态。
func (s *Server) setHanditemLoadedRef(ctx context.Context, parentGUID, itemGUID string) {
	if parentGUID == "" {
		return
	}
	row, err := s.DB.GetItemByGUID(ctx, parentGUID)
	if err != nil || row == nil || !handitemWireClasses[row.WireClass] {
		return
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil || len(fields) <= handitemLoadedIdx {
		return
	}
	if fields[handitemLoadedIdx].Op != "OPTGUID" {
		s.log.Warn("手持物装填引用未写：布局与预期不符", "item", row.ItemID, "wire", row.WireClass)
		return
	}
	fields[handitemLoadedIdx].Hex = itemGUID
	s.saveItemFields(ctx, row, fields)
}

// handitemLoadedGuid 返回手持工具当前装填物的 GUID。
func (s *Server) handitemLoadedGuid(ctx context.Context, parentGUID string) string {
	if parentGUID == "" {
		return ""
	}
	row, err := s.DB.GetItemByGUID(ctx, parentGUID)
	if err != nil || row == nil || !handitemWireClasses[row.WireClass] {
		return ""
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil || len(fields) <= handitemLoadedIdx {
		return ""
	}
	g := fields[handitemLoadedIdx].Hex
	if g == zeroGUID {
		return ""
	}
	return g
}

// ---- 装配与拆卸 ----

// rigSetOpts 保存装配操作的选项。
type rigSetOpts struct {
	KeepRebait        bool // 自动补挂成功后由调用方删除原请求。
	ParentType        int  // 命令族号（13/15/16/17），不是钓组种类
	EnforceCategory   bool // 显式槽位 ≠ 物品品类时用 fits_slot 校验（17/3/单测路径）
	SplitStack        bool // 计件物品挂载拆分（钓竿必须拆分；手持工具例外）
	HanditemFullStack bool // 手持工具 13/x：创建装填子实例（缺省只装 1 份）
}

// rigSetResult 保存装配结果和 800 快照素材。
type rigSetResult struct {
	Rejected     bool
	MountedGUID  string // 实际挂进钓组的 guid（计件拆分时 = 子实例，非调用方原 guid）
	ReplacedGUID string // 被替换卸下的旧组件 guid
	// ReplacedToBackpack 是被替换后退回背包的旧组件 guid（仅退回背包分支赋值）。
	// 手持工具换弹需要它做「移除+重加」；不复用 ReplacedGUID 是为了不影响
	// 17/2 等既有读者。
	ReplacedToBackpack string
	AddedTrees         []*proto.Value // nil 表示无新增段（非拆分挂载时 f0 为 null）
	UpdatedTrees       []*proto.Value
	RemovedGUIDs       []string
	SourceGUID         string         // 拆分挂载后的来源堆 guid（回包后强制刷新客户端计数）
	SourceTree         *proto.Value   // 来源堆减一后的新树
	Released           []*rigReleased // 去重清理退回的组件（树已并入 UpdatedTrees，来源堆刷新走 queueReleasedStackRefresh）
}

// hasMaterials 判断装配是否产生了新增、更新或移除。
func (r *rigSetResult) hasMaterials() bool {
	return r != nil && (r.AddedTrees != nil || len(r.UpdatedTrees) > 0 || len(r.RemovedGUIDs) > 0)
}

// rigSetComponent 将一个物品装到竿、轮、拟饵或手持工具上。
func (s *Server) rigSetComponent(ctx context.Context, uid int64, parentGUID string, slot int64, itemGUID string, extra int64, opts rigSetOpts) *rigSetResult {
	res := &rigSetResult{UpdatedTrees: []*proto.Value{}}
	if !validRigGUID(itemGUID) {
		s.log.Warn("装配被拒：物品 guid 缺失/全零", "parent", shortGUID(parentGUID))
		res.Rejected = true
		return res
	}
	it, err := s.DB.GetItemByGUID(ctx, itemGUID)
	if err != nil || it == nil {
		s.log.Warn("装配被拒：物品不在库中", "item", shortGUID(itemGUID), "parent", shortGUID(parentGUID))
		res.Rejected = true
		return res
	}

	itemCat, hasCat := s.itemCategoryOf(ctx, it)
	if s.itemBrokenForMount(ctx, it) {
		s.log.Warn("装配被拒：物品已损坏或鱼线长度耗尽", "item", shortGUID(itemGUID),
			"durability", it.Durability, "category", itemCat)
		res.Rejected = true
		return res
	}
	// 断竿不能继续装配：竿耐久归零后须先修理，再重组线组。
	if prow, perr := s.DB.GetItemByGUID(ctx, parentGUID); perr == nil && prow != nil {
		if pcat, _ := s.itemCategoryOf(ctx, prow); pcat == gearCatRod && prow.Durability <= 0 {
			s.log.Warn("装配被拒：父竿已损坏", "parent", shortGUID(parentGUID),
				"durability", prow.Durability)
			res.Rejected = true
			return res
		}
	}
	if slot <= 0 && hasCat {
		slot = itemCat // 槽位 = 物品品类（17/2 请求无槽位，见模块头注）
	}

	// 显式槽位与物品品类不同时执行 fits_slot 校验；不兼容则不写库。
	// 17/2 不带显式槽位，因此无法执行这项兼容性拒绝。
	if opts.EnforceCategory && hasCat && slot > 0 && slot != itemCat && !fitsSlot(itemCat, hasCat, slot) {
		s.log.Warn("装配被拒：品类不能装入槽位", "item", shortGUID(itemGUID),
			"cat", itemCat, "slot", slot)
		res.Rejected = true
		return res
	}

	// 拟饵的钩位在实例 f0 的出厂钩数组里，不走竿的 f5 组件表。
	// 走通用装配路径只会把钩从背包摘走、拟饵仍显示原钩（换钩无效）。
	if hasCat && itemCat == catHook {
		if prow, perr := s.DB.GetItemByGUID(ctx, parentGUID); perr == nil && isLureRow(prow) {
			return s.lureHookMount(ctx, uid, parentGUID, itemGUID, extra)
		}
	}

	// 当前钓组模板没有该品类槽位时只记录告警，不拒绝装配。
	// 饵、尖梢、线、轮和包装盒本来就不在模板槽中。
	if hasCat && !templateExemptCats[itemCat] {
		if prow, perr := s.DB.GetItemByGUID(ctx, parentGUID); perr == nil && prow != nil {
			if rt := s.rigTypeOfItem(ctx, prow); rt > 0 && len(slotIndicesFor(rt, itemCat)) == 0 {
				s.log.Warn("钓组模板提示：该类型槽位表里没有此品类（组件仍按请求安装）",
					"rig_type", rt, "cat", itemCat)
			}
		}
	}

	// 确保父对象存在索引行；钓组类型从实例 f5 读取，不能用命令族号代替。
	if prow, perr := s.DB.GetItemByGUID(ctx, parentGUID); perr == nil && prow != nil {
		_ = s.DB.UpsertRig(ctx, parentGUID, uid, s.rigTypeOfItem(ctx, prow), "", "{}")
	} else {
		_ = s.DB.UpsertRig(ctx, parentGUID, uid, 1, "", "{}")
	}

	// 800 快照素材：f0 新增，f1 更新（含竿），f2 移除原有子实例 GUID。
	added := []*proto.Value(nil)
	updated := []*proto.Value{}
	var removed []string
	materialized := false
	// 计件物品挂载分三种：
	//   * 饵料（qty>1）：按玩家在该竿选定的挂饵方式拆份数上钩（官方模型，
	//     baitHookCount）：默认一钩一份；玉米浮+蛆15 拆 15 份蛆等。
	//     来源堆保留在背包，同种饵可挂到其他竿；咬口由 consumeBaitEvent
	//     逐份扣减，用尽后写 rebait 记录、抛竿前自动补挂。
	//   * 其余计件物品（钩、窝料等）：拆出数量为 1 的挂载子实例；
	//     沉底族多钩需按钩逐份配饵。
	//   * 手持工具（13/x）：创建装填子实例（materializeHanditemChild）；
	//     缺省只装 1 份，整堆模式由 handitem_load_one=0 显式回退。
	// 注：曾短暂实现过浮钓/沉底单钩「整包装填」（来源堆整包搬上钩），
	// 被反编译证伪：客户端发丝族按子实例数量渲染并计重，整包上钩直接超重；
	// 整包删源栈还会让第二根竿选不到同种饵。已回归官方的按挂饵方式数量拆分。
	if opts.SplitStack && itemCat == catBait && s.stackCountOf(ctx, it) > 1 {
		n := s.baitHookCount(ctx, parentGUID, it)
		sourceGUID := itemGUID
		g, srcRemoved := s.splitMountableChild(ctx, uid, parentGUID, itemGUID, n)
		itemGUID = g
		if srcRemoved != "" {
			removed = append(removed, srcRemoved)
			materialized = true
		} else if itemGUID != sourceGUID {
			// 来源堆留在背包：数量减了 n。客户端只按「新增/移除」更新背包
			// 计数，这里同时提供新树（回包 f1）和回包后的移除+重加刷新。
			if src, serr := s.DB.GetItemByGUID(ctx, sourceGUID); serr == nil && src != nil {
				if t, terr := s.itemTree(ctx, src); terr == nil {
					res.SourceGUID = sourceGUID
					res.SourceTree = t
					updated = append(updated, t)
				}
			}
		}
	} else if opts.SplitStack {
		sourceGUID := itemGUID
		itemGUID, _ = s.splitMountableChild(ctx, uid, parentGUID, itemGUID, 1)
		if itemGUID != sourceGUID {
			// 拆分把来源堆数量减一。客户端只按「新增/移除」更新背包计数，
			// 这里同时提供新树（回包 f1）和回包后的移除+重加刷新。
			if src, serr := s.DB.GetItemByGUID(ctx, sourceGUID); serr == nil && src != nil {
				if t, terr := s.itemTree(ctx, src); terr == nil {
					res.SourceGUID = sourceGUID
					res.SourceTree = t
					updated = append(updated, t)
				}
			}
		}
	} else if opts.HanditemFullStack {
		sourceGUID := itemGUID
		g, srcRemoved := s.materializeHanditemChild(ctx, uid, parentGUID, itemGUID)
		itemGUID = g
		if itemGUID != sourceGUID {
			materialized = true // 新子实例必须进 f0，客户端才建立父子归属
			if srcRemoved != "" {
				removed = append(removed, srcRemoved)
			} else if src, serr := s.DB.GetItemByGUID(ctx, sourceGUID); serr == nil && src != nil {
				// 只装 1 份：来源堆留背包、数量减 1，提供新树刷新客户端计数。
				if t, terr := s.itemTree(ctx, src); terr == nil {
					res.SourceGUID = sourceGUID
					res.SourceTree = t
					updated = append(updated, t)
				}
			}
		}
	}

	// 同品类多槽时由实例 f5 选择空闲序号；只有目标序号已占用时才替换现有组件。
	ordinal, oldGUID := s.rigSetTarget(ctx, parentGUID, slot, itemGUID, extra)
	oldHook := ""
	if slot == catBait || slot == catLure {
		oldHook, _ = s.DB.GetRodSetting(ctx, parentGUID, "bait_hook:"+oldGUID)
	}
	if oldGUID != "" && oldGUID != itemGUID {
		if prev, oerr := s.DB.GetItemByGUID(ctx, oldGUID); oerr == nil && prev != nil {
			if s.refundableSplitChild(ctx, prev) {
				// 原有挂载是拆分子实例：整条删除，并把钩上剩余份数退回来源堆。
				srcGUID := splitSourceGUID(prev)
				refund := s.stackCountOf(ctx, prev)
				if refund < 1 {
					refund = 1
				}
				_ = s.DB.DeleteItems(ctx, []string{oldGUID})
				removed = append(removed, oldGUID)
				if srcGUID != "" {
					if src, serr := s.DB.GetItemByGUID(ctx, srcGUID); serr == nil && src != nil {
						s.setStackCount(ctx, src, s.stackCountOf(ctx, src)+refund)
						if fresh, ferr := s.DB.GetItemByGUID(ctx, srcGUID); ferr == nil && fresh != nil {
							if t, terr := s.itemTree(ctx, fresh); terr == nil {
								updated = append(updated, t)
							}
						}
					}
				}
			} else {
				// 普通物品退回背包并清空父引用，避免继续显示为已挂载。
				s.returnToBackpack(ctx, prev)
				res.ReplacedToBackpack = oldGUID
				if fresh, ferr := s.DB.GetItemByGUID(ctx, oldGUID); ferr == nil && fresh != nil {
					if t, terr := s.itemTree(ctx, fresh); terr == nil {
						updated = append(updated, t)
					}
				}
			}
		}
	}

	// 非拆分挂载需要写入 wire 父引用。
	if itemGUID != "" {
		if mounted, merr := s.DB.GetItemByGUID(ctx, itemGUID); merr == nil && mounted != nil {
			s.setWireParent(ctx, mounted, parentGUID)
		}
	}

	// 先修改实例字段，再重建 rig_component 派生索引。
	// 顺序反过来会导致拆卸只删索引，实例中仍保留组件。
	// 收集去重退回的组件：替换卸下的旧组件已在上面的 updated 段，
	// 这里只补去重路径的退回（换类型收敛、互斥槽清理等）。
	var released []*rigReleased
	s.rigComponentWriteAt(ctx, parentGUID, slot, ordinal, itemGUID, extra, &released)
	res.Released = released
	res.UpdatedTrees = append(res.UpdatedTrees, releasedReturnTrees(released)...)
	_ = s.DB.SetItemRigLink(ctx, itemGUID, parentGUID, slot)
	if slot == 27 && !opts.KeepRebait {
		_, _ = s.DB.SQL.ExecContext(ctx, `DELETE FROM rod_setting WHERE guid=? AND key=?`, parentGUID, fmt.Sprintf("rebait:%d", ordinal))
	}

	if slot == catBait || slot == catLure {
		if err := s.rememberMountedBaitHook(ctx, parentGUID, itemGUID, oldHook, slot, ordinal); err != nil {
			s.log.Warn("钩饵关联保存失败", "err", err)
		}
	}

	// 手持工具将装填物 GUID 写入父实例 f1。
	if opts.ParentType == familyHanditem && itemGUID != "" {
		s.setHanditemLoadedRef(ctx, parentGUID, itemGUID)
	}

	s.log.Info("装配", "parent", shortGUID(parentGUID), "slot", slot, "ordinal", ordinal,
		"item", shortGUID(itemGUID), "extra", extra)

	// 快照素材规则：
	//   * 新子实例必须进入 f0，客户端才会建立父子归属；
	//   * 非拆分挂载的物品进入 f1 更新段；
	//   * 拆分子实例同样进入 f0。
	res.MountedGUID = itemGUID
	if mounted, merr := s.DB.GetItemByGUID(ctx, itemGUID); merr == nil && mounted != nil {
		switch {
		case materialized:
			if t, terr := s.itemTree(ctx, mounted); terr == nil {
				added = append(added, t)
			}
		case !s.isSplitChild(ctx, mounted):
			if t, terr := s.itemTree(ctx, mounted); terr == nil {
				updated = append([]*proto.Value{t}, updated...)
			}
		default:
			if t, terr := s.itemTree(ctx, mounted); terr == nil {
				added = append(added, t)
			}
		}
	}
	if prow, perr := s.DB.GetItemByGUID(ctx, parentGUID); perr == nil && prow != nil {
		if t, terr := s.itemTree(ctx, prow); terr == nil {
			updated = append(updated, t)
		}
	}
	res.AddedTrees = added
	// released 中的普通物品必须保留在最终更新段，供 800 快照 f1 通知客户端
	// 已退回背包；不能用局部 updated 覆盖前面并入的退回树。
	res.UpdatedTrees = append(updated, releasedReturnTrees(released)...)
	res.RemovedGUIDs = removed
	return res
}

// storedRigComponents 读取竿实例 f5 中的组件；非竿或无 f5 时返回空 map。
func (s *Server) storedRigComponents(ctx context.Context, rodGUID string) map[int64]rigComp {
	row, err := s.DB.GetItemByGUID(ctx, rodGUID)
	if err != nil || row == nil {
		return map[int64]rigComp{}
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return map[int64]rigComp{}
	}
	idx := fieldIdxByCS(row.WireClass, csRigObject)
	if idx < 0 || idx >= len(fields) || fields[idx].Val == nil {
		return map[int64]rigComp{}
	}
	return rigCompsOf(fields[idx].Val)
}

// rigSetTarget 为新组件选择目标序号：已有同 GUID 组件保持原序号，
// 否则填入该品类的第一个空槽；没有 f5 时回退到 rig_component 索引。
func (s *Server) rigSetTarget(ctx context.Context, parentGUID string, slot int64, itemGUID string, extra int64) (int64, string) {
	comps := s.storedRigComponents(ctx, parentGUID)
	if _, special := categorySlotField[slot]; special || len(comps) == 0 {
		if rows, err := s.DB.ListRigComponents(ctx, parentGUID); err == nil {
			for _, rc := range rows {
				key := rigCompKey(rc.Slot, rc.Ordinal)
				if _, exists := comps[key]; !exists || rc.Slot == slot {
					comps[key] = rigComp{Category: rc.Slot, Ordinal: rc.Ordinal, GUID: rc.GUID, Extra: rc.Extra}
				}
			}
		}
	}
	if key, comp, ok := rigCompKeyForGUID(comps, itemGUID); ok {
		return rigCompOrdinal(key), comp.GUID
	}
	if _, special := categorySlotField[slot]; special {
		if comp, ok := comps[rigCompKey(slot, 0)]; ok {
			return 0, comp.GUID
		}
		return 0, ""
	}
	// 天然饵只有显式第二饵位才组成双饵；普通换饵仍替换第一饵。
	if slot == 27 {
		ordinal := int64(0)
		if slotCount := int64(baitSlotCount(s.rigTypeForWrite(ctx, parentGUID))); extra > 350 && extra < 350+slotCount {
			ordinal = extra - 350
		}
		return ordinal, comps[rigCompKey(slot, ordinal)].GUID
	}
	rt := s.rigTypeForWrite(ctx, parentGUID)
	idxs := uniqueIndices(slotIndicesFor(rt, slot))
	limit := len(idxs)
	if slot == catLure && limit < 2 {
		if row, err := s.DB.GetItemByGUID(ctx, itemGUID); err == nil && row != nil {
			if n := s.baitMountComponentLimit(ctx, parentGUID, row); n > int64(limit) {
				limit = int(n)
			}
		}
	}
	if limit < 1 {
		limit = 1
	}
	// 统计同品类已装件数与已占用的序号。
	// f5 数组顺序由规范索引决定，读回时 rigCompsOf 按同品类出现次序重新编号，
	// 所以写入序号必须是「未被占用的最小序号」，不能直接用槽位在模板中的位置：
	// 模板有空洞时（已有 300 与 326、缺 325），325 在模板里的位置是 1，
	// 而读回给 326 的序号也是 1，于是 325 会被当成「同槽换件」把 326 顶掉——
	// 基本吸引成分与钓钩吸引成分互相顶掉正是这样发生的。
	count := 0
	used := map[int64]bool{}
	for key := range comps {
		if rigCompCategory(key) != slot {
			continue
		}
		count++
		used[rigCompOrdinal(key)] = true
	}
	freeOrdinal := func() int64 {
		for ord := int64(0); ; ord++ {
			if !used[ord] {
				return ord
			}
		}
	}
	// 海钓钩位的 1000+ 客户端槽位索引是独立命名空间，不受模板声明的
	// 槽位个数限制：模板只声明了 250 一个钩位，但倒钓臂展开后客户端会
	// 用 1001/1005/1009 等索引装多个钩；每个钩位下还带拟饵子槽（1002/1006/
	// 1010）。若沿用 len(idxs) 当上限，第二件就会被判「无空闲槽」而顶掉
	// 第一件（三个钩位都装不上，或拟饵把亮片顶掉）。
	highIndexSlot := hookSlotIndexNamespace(slot, extra)
	if extra != 0 {
		// 同一规范索引已装：走替换，保留其序号与原组件 GUID。
		for key, c := range comps {
			if rigCompCategory(key) != slot {
				continue
			}
			if canonicalIndexForOrdinal(slot, c.Extra, rt, rigCompOrdinal(key)) == extra {
				return rigCompOrdinal(key), c.GUID
			}
		}
		// 空闲槽位：写入未占用的序号，实际索引仍由 extra 决定。
		if (highIndexSlot || extraIsTargetIndex(slot, extra, rt)) && (highIndexSlot || count < limit) {
			return freeOrdinal(), ""
		}
	}
	if count < limit {
		return freeOrdinal(), ""
	}
	// 没有空闲槽时替换序号 0。
	return 0, comps[rigCompKey(slot, 0)].GUID
}

func (s *Server) rigTypeForWrite(ctx context.Context, rodGUID string) int64 {
	if row, err := s.DB.GetItemByGUID(ctx, rodGUID); err == nil && row != nil {
		if rt := s.rigTypeOfItem(ctx, row); rt > 0 {
			return rt
		}
	}
	return 0
}

// rigComponentWriteAt 将指定序号的装配结果写入实例 f5，
// 然后由 reconcileRigProjection 重建 rig_component。
// collect 语义与 dedupeRigComponents 相同。
func (s *Server) rigComponentWriteAt(ctx context.Context, parentGUID string, slot, ordinal int64, itemGUID string, extra int64, collect ...*[]*rigReleased) {
	if itemGUID == "" {
		if !s.rigItemDeleteComponentAt(ctx, parentGUID, slot, ordinal, collect...) {
			_ = s.DB.DeleteRigComponentAt(ctx, parentGUID, slot, ordinal)
		}
		return
	}
	if !s.rigItemMutate(ctx, parentGUID, &slot, &rigComp{
		Category: slot, Ordinal: ordinal, GUID: itemGUID, Extra: extra,
	}, nil, collect...) {
		_ = s.DB.UpsertRigComponentAt(ctx, parentGUID, slot, ordinal, itemGUID, extra)
	}
}

// rigItemMutate 修改竿实例的装配结果并落库。
// 钓线写入 f3，渔轮写入 f4，其余组件写入 f5；空 GUID 表示拆下。
// f5 缺失时按竿型派生默认钓组。换类型时资源名与枚举必须同步，
// 仅保留前后钓组都支持的组件；其他组件退回背包。
// collect 语义与 dedupeRigComponents 相同：换类型等触发去重退回时，
// 交互路径传入以收集被退回的组件，供 handler 并入快照/安排刷新。
func (s *Server) rigItemMutate(ctx context.Context, rodGUID string, slot *int64, comp *rigComp, rigType *int64, collect ...*[]*rigReleased) bool {
	row, err := s.DB.GetItemByGUID(ctx, rodGUID)
	if err != nil || row == nil {
		return false
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return false
	}
	// 按字段名查找钓组对象，不能假定固定字段位置。
	idx := fieldIdxByCS(row.WireClass, csRigObject)
	if idx < 0 || idx >= len(fields) {
		return false // 不是竿类（无钓组字段）：不动它
	}

	// 钓线和渔轮写入专用字段 f3/f4，不进入 f5。
	if slot != nil {
		if special, ok := categorySlotField[*slot]; ok {
			sidx := fieldIdxByCS(row.WireClass, special)
			if sidx < 0 || sidx >= len(fields) {
				return false
			}
			g := ""
			if comp != nil {
				g = comp.GUID
			}
			fields[sidx].Op = "OPTGUID"
			fields[sidx].Hex = g
			s.saveItemFields(ctx, row, fields)
			_ = s.reconcileRigProjection(ctx, rodGUID, collect...)
			return true
		}
	}

	stored := fields[idx].Val
	if stored == nil || stored.Kind != proto.KindObj {
		stored = nil
	}
	comps := rigCompsOf(stored)
	name, curType, typeOK := rigIdentity(stored)
	if !typeOK {
		// f5 缺失时按竿型派生默认钓组。
		curType = s.defaultRigTypeForRod(ctx, row.ItemID)
		if name == "" {
			name = rigNameForType(curType)
		}
	}
	curDepth := rigDepthOf(stored, defaultDepthFor(name, curType))
	originalType := curType

	if slot != nil {
		switch {
		case comp == nil:
			keys := rigCompKeysOfCategory(comps, *slot)
			if len(keys) == 0 {
				return false // rod_rig_del：确无该槽位
			}
			delete(comps, keys[0])
		default:
			key := rigCompKey(*slot, comp.Ordinal)
			if existingKey, _, ok := rigCompKeyForGUID(comps, comp.GUID); ok {
				key = existingKey
			}
			next := rigComp{
				Category: *slot, Ordinal: rigCompOrdinal(key), GUID: comp.GUID,
				Extra: canonicalIndexForOrdinal(*slot, comp.Extra, curType, rigCompOrdinal(key)),
			}
			// 正常装配到共用槽位时，新组件必须替换该规范索引上的旧组件。
			// 例如 rig57 的 {钩12,拟饵17}@252：先去重兜底会按品类顺序保留
			// 旧钩并丢弃刚装的拟饵，表现为“装配成功但没换上去”。这里在
			// 写入新组件前清掉所有同索引占用者，确保最后一次装配生效。
			for otherKey, other := range comps {
				if otherKey == key {
					continue
				}
				otherCat, otherOrdinal := rigCompCategory(otherKey), rigCompOrdinal(otherKey)
				if len(slotIndicesFor(curType, otherCat)) == 0 {
					continue
				}
				if canonicalIndexForOrdinal(otherCat, other.Extra, curType, otherOrdinal) != next.Extra {
					continue
				}
				delete(comps, otherKey)
				if other.GUID != "" && other.GUID != next.GUID {
					s.releaseDedupedComponent(ctx, row.UID, rodGUID, other.GUID, collect...)
				}
			}
			comps[key] = next
		}
		// 装配写侧收口：组件一装上就必须与当前钓组模板兼容，不能先把冲突组合
		// 落库、再等下一次 ensureRig 修。FD390 装食笼(31) 时由 12 自动切到 16。
		if comp != nil && rigType == nil && !s.rigTypeExplicit(ctx, rodGUID) &&
			!rigTypeFitsInstalledComps(curType, comps) {
			if def, derr := s.DB.GetItemDef(ctx, row.NumericID); derr == nil && def != nil {
				if fitted, ok := firstCompatibleRigType(rodRigAllowed[rodDefRodType(def)], comps); ok {
					curType = fitted
					if mapped := rigNameForType(curType); mapped != "" {
						name = mapped
					}
					for key, c := range comps {
						cat, ordinal := rigCompCategory(key), rigCompOrdinal(key)
						c.Extra = canonicalIndexForOrdinal(cat, c.Extra, curType, ordinal)
						comps[key] = c
					}
				}
			}
		}
	}
	if rigType != nil {
		curType = *rigType
		// 切换钓组不是保存多个预设：一根竿只有一份当前装配。
		// 因此只让前后模板共同支持的部件继续留在竿上；目标模板
		// 不支持的浮漂、前导、食笼等要退回背包，不能作为旧钓组残留。
		s.retainSharedRigComponents(ctx, row.UID, rodGUID, originalType, curType, comps, collect...)
		// 保留下来的组件在新模板中的规范索引可能收敛到同一个槽位
		// （例如两种海钓钓组都用 13/175），必须把重复项退回背包，
		// 否则客户端渲染钓组时两个前导叠在一位，抛竿后整组被判无效。
		s.dedupeRigComponents(ctx, row.UID, rodGUID, curType, comps, collect...)
		// f0 资源名与 f1 类型必须同步。
		if mapped := rigNameForType(curType); mapped != "" {
			name = mapped
			// 路亚钓组的钓棚深度固定为 0。
			if mapped == rigNameLure {
				curDepth = 0
			}
		}
	}

	fields[idx].Op = "VAL"
	fields[idx].Val = buildRig153Like(stored, name, curType, comps, curDepth, rigClipOf(stored))
	s.saveItemFields(ctx, row, fields)
	_ = s.reconcileRigProjection(ctx, rodGUID, collect...)
	if curType != originalType {
		// f5 是装配数据源，类型变化后必须同步 rig 派生索引。
		_ = s.DB.UpdateRigType(ctx, rodGUID, curType)
	}
	return true
}

// rigItemDeleteComponentAt 精确删除指定品类和序号的组件，
// 避免误删同品类其他实例。单槽调用使用序号 0。
// collect 语义与 dedupeRigComponents 相同。
func (s *Server) rigItemDeleteComponentAt(ctx context.Context, rodGUID string, slot, ordinal int64, collect ...*[]*rigReleased) bool {
	row, err := s.DB.GetItemByGUID(ctx, rodGUID)
	if err != nil || row == nil {
		return false
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return false
	}
	idx := fieldIdxByCS(row.WireClass, csRigObject)
	if idx < 0 || idx >= len(fields) {
		return false
	}
	if special, ok := categorySlotField[slot]; ok {
		if ordinal != 0 {
			return false
		}
		sidx := fieldIdxByCS(row.WireClass, special)
		if sidx < 0 || sidx >= len(fields) {
			return false
		}
		fields[sidx].Op = "OPTGUID"
		fields[sidx].Hex = ""
		s.saveItemFields(ctx, row, fields)
		_ = s.reconcileRigProjection(ctx, rodGUID, collect...)
		return true
	}
	stored := fields[idx].Val
	comps := rigCompsOf(stored)
	key := rigCompKey(slot, ordinal)
	if _, ok := comps[key]; !ok {
		return false
	}
	delete(comps, key)
	name, curType, typeOK := rigIdentity(stored)
	if !typeOK {
		curType = s.defaultRigTypeForRod(ctx, row.ItemID)
		if name == "" {
			name = rigNameForType(curType)
		}
	}
	depth := rigDepthOf(stored, defaultDepthFor(name, curType))
	fields[idx].Op = "VAL"
	fields[idx].Val = buildRig153Like(stored, name, curType, comps, depth, rigClipOf(stored))
	s.saveItemFields(ctx, row, fields)
	_ = s.reconcileRigProjection(ctx, rodGUID, collect...)
	return true
}

// rigDetachResult 保存拆卸结果和 800 快照素材。
// 客户端按 f0、f1、f2 顺序处理快照，f2 会从物品索引删除 GUID：
//   - 退回背包的普通物品树放入 f1，其 GUID 不能进入 f2；
//   - 整条删除的拆分子实例 GUID 进入 f2，来源堆增加后的树放入 f1。
type rigDetachResult struct {
	CompGUID     string       // 被拆下的组件 guid
	DeletedGUID  string       // 整条删除的子实例 guid（f2 移除段素材；退回路径为空）
	ReturnedTree *proto.Value // 退回背包物品的新树（f1 更新段素材）
	RefundTree   *proto.Value // 拆出厂钩物化退包的新物品树（新 guid → f0 新增段素材）
	SourceTree   *proto.Value // 拆分子实例来源堆 +1 后的新树（f1 更新段素材）
	SourceGUID   string       // 拆分子实例的来源堆 guid（回包后强制刷新客户端计数）
}

// trees 是应进入 f1 的物品树。
func (r *rigDetachResult) trees() []*proto.Value {
	if r == nil {
		return nil
	}
	var out []*proto.Value
	if r.ReturnedTree != nil {
		out = append(out, r.ReturnedTree)
	}
	if r.SourceTree != nil {
		out = append(out, r.SourceTree)
	}
	return out
}

// removedGUIDs 是应进入 f2 的 GUID，仅包含整条删除的子实例。
func (r *rigDetachResult) removedGUIDs() []string {
	if r == nil || r.DeletedGUID == "" {
		return nil
	}
	return []string{r.DeletedGUID}
}

// rigRemoveComponent 按槽位拆下该品类的第一件组件。
func (s *Server) rigRemoveComponent(ctx context.Context, uid int64, parentGUID string, slot int64) *rigDetachResult {
	return s.rigRemoveComponentInstance(ctx, uid, parentGUID, slot, "", 0)
}

// rigRemoveComponentInstance 处理 17/3，优先按请求中的 GUID 或序号定位组件；
// 两者都缺失时才拆该品类第一件，避免误删同品类其他实例。
func (s *Server) rigRemoveComponentInstance(ctx context.Context, uid int64, parentGUID string, slot int64, itemGUID string, extra int64) *rigDetachResult {
	// 拟饵钩位在实例 f0，不在 f5 组件表：按钩 GUID/序号直接改数组。
	if prow, perr := s.DB.GetItemByGUID(ctx, parentGUID); perr == nil && isLureRow(prow) && (slot == catHook || slot == 0) {
		return s.lureHookRemove(ctx, uid, parentGUID, itemGUID, extra)
	}
	comps := s.storedRigComponents(ctx, parentGUID)
	if validRigGUID(itemGUID) {
		if key, comp, ok := rigCompKeyForGUID(comps, itemGUID); ok && rigCompCategory(key) == slot {
			return s.rigDetachAt(ctx, parentGUID, slot, rigCompOrdinal(key), comp.GUID)
		}
	}
	keys := rigCompKeysOfCategory(comps, slot)
	for _, key := range keys {
		if extra != 0 && comps[key].Extra != extra {
			continue
		}
		return s.rigDetachAt(ctx, parentGUID, slot, rigCompOrdinal(key), comps[key].GUID)
	}
	if len(keys) > 0 {
		key := keys[0]
		return s.rigDetachAt(ctx, parentGUID, slot, rigCompOrdinal(key), comps[key].GUID)
	}
	// 非竿容器没有 f5，回退到索引表的第一件。
	if rows, err := s.DB.ListRigComponents(ctx, parentGUID); err == nil {
		for _, c := range rows {
			if c.Slot == slot && (extra == 0 || c.Extra == extra) {
				return s.rigDetachAt(ctx, parentGUID, c.Slot, c.Ordinal, c.GUID)
			}
		}
	}
	s.log.Info("拆卸：槽位无组件（忽略）", "parent", shortGUID(parentGUID), "slot", slot)
	return nil
}

// rigRemoveItemComponent 按物品 GUID 拆下组件，供不带显式槽位的删除命令使用。
// 这些命令的槽位由物品品类决定，因此直接按 GUID 反查。
func (s *Server) rigRemoveItemComponent(ctx context.Context, uid int64, parentGUID, itemGUID string) *rigDetachResult {
	comps := s.storedRigComponents(ctx, parentGUID)
	if key, comp, ok := rigCompKeyForGUID(comps, itemGUID); ok {
		return s.rigDetachAt(ctx, parentGUID, rigCompCategory(key), rigCompOrdinal(key), comp.GUID)
	}
	if rows, err := s.DB.ListRigComponents(ctx, parentGUID); err == nil {
		for _, c := range rows {
			if c.GUID == itemGUID {
				return s.rigDetachAt(ctx, parentGUID, c.Slot, c.Ordinal, c.GUID)
			}
		}
	}
	if validRigGUID(itemGUID) {
		if row, err := s.DB.GetItemByGUID(ctx, itemGUID); err == nil && row != nil {
			if cat, ok := s.itemCategoryOf(ctx, row); ok && cat > 0 {
				// 回退：使用物品品类作为槽位，选择第一件。
				if keys := rigCompKeysOfCategory(comps, cat); len(keys) > 0 {
					key := keys[0]
					return s.rigDetachAt(ctx, parentGUID, cat, rigCompOrdinal(key), comps[key].GUID)
				}
			}
		}
	}
	s.log.Info("拆组件：父对象上未找到该物品（忽略）",
		"parent", shortGUID(parentGUID), "item", shortGUID(itemGUID))
	return nil
}

// rigDetach 从父对象摘除一件组件。
// 拆分子实例整条删除并把钩上剩余份数退回来源堆；普通物品退回背包并清空父引用。
// 两种退回树都通过 rigDetachResult 进入响应 f1。
func (s *Server) rigDetach(ctx context.Context, parentGUID string, slot int64, compGUID string) *rigDetachResult {
	comps := s.storedRigComponents(ctx, parentGUID)
	if key, comp, ok := rigCompKeyForGUID(comps, compGUID); ok {
		return s.rigDetachAt(ctx, parentGUID, slot, rigCompOrdinal(key), comp.GUID)
	}
	return s.rigDetachAt(ctx, parentGUID, slot, 0, compGUID)
}

// rigDetachAt 精确摘除指定品类和序号对应的实例。
func (s *Server) rigDetachAt(ctx context.Context, parentGUID string, slot, ordinal int64, compGUID string) *rigDetachResult {
	res := &rigDetachResult{CompGUID: compGUID}
	if slot == 27 {
		_, _ = s.DB.SQL.ExecContext(ctx, `DELETE FROM rod_setting WHERE guid=? AND key=?`, parentGUID, fmt.Sprintf("rebait:%d", ordinal))
	}
	if compGUID != "" {
		if prev, oerr := s.DB.GetItemByGUID(ctx, compGUID); oerr == nil && prev != nil {
			if s.refundableSplitChild(ctx, prev) {
				// 拆分子实例整条删除，并把钩上剩余份数退回来源堆。
				srcGUID := splitSourceGUID(prev)
				refund := s.stackCountOf(ctx, prev)
				if refund < 1 {
					refund = 1
				}
				_ = s.DB.DeleteItems(ctx, []string{compGUID})
				res.DeletedGUID = compGUID
				if srcGUID != "" {
					if src, serr := s.DB.GetItemByGUID(ctx, srcGUID); serr == nil && src != nil {
						s.setStackCount(ctx, src, s.stackCountOf(ctx, src)+refund)
						if fresh, ferr := s.DB.GetItemByGUID(ctx, srcGUID); ferr == nil && fresh != nil {
							res.SourceTree, _ = s.itemTree(ctx, fresh)
							res.SourceGUID = srcGUID
						}
					}
				}
			} else {
				// 普通物品退回背包并清空父引用，新树进入 f1。
				s.returnToBackpack(ctx, prev)
				if fresh, ferr := s.DB.GetItemByGUID(ctx, compGUID); ferr == nil && fresh != nil {
					res.ReturnedTree, _ = s.itemTree(ctx, fresh)
				}
			}
		}
	}
	// 以实例为准，精确删除 f5 中的 (category, ordinal)；
	// 非竿容器再删除对应索引行，最后由 reconcile 重建。
	if !s.rigItemDeleteComponentAt(ctx, parentGUID, slot, ordinal) {
		_ = s.DB.DeleteRigComponentAt(ctx, parentGUID, slot, ordinal)
	}
	// 手持工具拆下装填物时同时清空 f1 的装填引用。
	s.setHanditemLoadedRef(ctx, parentGUID, "")
	s.log.Info("拆卸", "parent", shortGUID(parentGUID), "slot", slot, "ordinal", ordinal,
		"item", shortGUID(compGUID), "deleted", res.DeletedGUID != "")
	return res
}

// rigRemoveAll 拆下父对象上的全部组件。
func (s *Server) rigRemoveAll(ctx context.Context, uid int64, parentGUID string) []*rigDetachResult {
	_, _ = s.DB.SQL.ExecContext(ctx, `DELETE FROM rod_setting WHERE guid=? AND key LIKE 'rebait:%'`, parentGUID)
	rows, _ := s.DB.ListRigComponents(ctx, parentGUID)
	comps := s.storedRigComponents(ctx, parentGUID)
	seen := map[string]bool{}
	all := make([]store.RigComponent, 0, len(rows)+len(comps))
	for _, c := range rows {
		if c.GUID != "" && !seen[c.GUID] {
			seen[c.GUID] = true
			all = append(all, *c)
		}
	}
	for key, c := range comps {
		if c.GUID != "" && !seen[c.GUID] {
			seen[c.GUID] = true
			all = append(all, store.RigComponent{
				Slot: rigCompCategory(key), Ordinal: rigCompOrdinal(key), GUID: c.GUID, Extra: c.Extra,
			})
		}
	}
	out := make([]*rigDetachResult, 0, len(all))
	for _, c := range all {
		// 删除后同品类实例会重排，因此每轮都按 GUID 重新定位。
		if r := s.rigDetach(ctx, parentGUID, c.Slot, c.GUID); r != nil {
			out = append(out, r)
		}
	}
	return out
}

// ---- 钓组类型与兼容性矫正 ----

// rigSetType 处理 17/1，为竿选择钓组类型。
// 请求为 [容器, 竿 GUID, 钓组类型]，没有钓组 GUID。
// 必须先把类型写入实例 f5 并保留现有组件，再同步 rig 派生表。
// rigSetType 切换竿钓组类型。collect 可选：交互路径传入以收集去重退回的组件
// （17/1 快照与来源堆刷新素材）；测试与登录矫正路径可省略。
func (s *Server) rigSetType(ctx context.Context, uid int64, rodGUID string, rigType int64, collect ...*[]*rigReleased) bool {
	if !validRigGUID(rodGUID) {
		return false
	}
	row, err := s.DB.GetItemByGUID(ctx, rodGUID)
	if err != nil || row == nil || row.UID != uid {
		return false
	}
	// 17/1 来自客户端，不能只信任物品归属。已知竿型必须只能选用
	// 自己的钓组集合，否则会把海钓、路亚等无效类型写进普通鱼竿实例。
	// 未知竿型沿用矩阵的一贯口径：不在此处拦截，交给后续兼容性矫正。
	def, derr := s.DB.GetItemDef(ctx, row.NumericID)
	if derr != nil || def == nil || def.Category != gearCatRod {
		return false
	}
	if allowed, known := rodRigAllowed[rodDefRodType(def)]; known && len(allowed) > 0 && !allowedContains(allowed, rigType) {
		s.log.Warn("竿钓组类型被拒绝：与竿型不兼容", "rod", shortGUID(rodGUID), "rig_type", rigType)
		return false
	}
	// 玩家显式选择优先。已装组件与新类型不完全匹配时也不拒绝，
	// 后续矫正不能用组件推断覆盖该选择。
	// 钓组与挂饵方式联动：mutate 里 buildRig153Like 已按新类型重算 f9，
	// 并把失效的 f8 清零；旧选择由 savedBaitMount 按同一可选集判为未设置。
	s.rigItemMutate(ctx, rodGUID, nil, nil, &rigType, collect...)
	_ = s.DB.UpsertRodSetting(ctx, rodGUID, "rig_type_explicit", "1")
	_ = s.DB.UpsertRig(ctx, rodGUID, uid, rigType, "", "{}")
	_ = s.DB.UpdateRigType(ctx, rodGUID, rigType)
	s.log.Info("竿钓组类型更新", "rod", shortGUID(rodGUID), "rig_type", rigType)
	return true
}

// retainSharedRigComponents 在切换钓组时保留两个模板共同支持的部件。
// 一根竿目前没有按钓组类型保存多份装配预设；若不在这里卸下目标模板
// 不支持的组件，切换后它们会继续写进新钓组的 f5，形成“外观已切换、
// 实际仍带着旧钓组配件”的混合状态。
//
// 渔轮、主线、尖梢等 templateExemptCats 是竿的通用部件，不随钓组切换。
// 饵依附钩位：目标钓组没有钩位时一并退回，防止留下孤立的饵实例。
func (s *Server) retainSharedRigComponents(ctx context.Context, uid int64, parentGUID string, fromType, toType int64, comps map[int64]rigComp, collect ...*[]*rigReleased) {
	if fromType == toType || len(comps) == 0 {
		return
	}
	keys := make([]int64, 0, len(comps))
	for key := range comps {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		ci, cj := rigCompCategory(keys[i]), rigCompCategory(keys[j])
		if ci != cj {
			return ci < cj
		}
		return rigCompOrdinal(keys[i]) < rigCompOrdinal(keys[j])
	})
	for _, key := range keys {
		comp, exists := comps[key]
		if !exists {
			continue
		}
		category, ordinal := rigCompCategory(key), rigCompOrdinal(key)
		if !rigComponentSharedByTypes(fromType, toType, category) {
			delete(comps, key)
			s.releaseDedupedComponent(ctx, uid, parentGUID, comp.GUID, collect...)
			continue
		}
		// 共同槽位也必须使用新模板的索引；海钓 1000+ 钩位子槽会由
		// canonicalIndexForOrdinal 原样保留。
		if len(slotIndicesFor(toType, category)) > 0 {
			comp.Extra = canonicalIndexForOrdinal(category, comp.Extra, toType, ordinal)
			comps[key] = comp
		}
	}
}

func rigComponentSharedByTypes(fromType, toType, category int64) bool {
	if category == catBait {
		return len(slotIndicesFor(fromType, catHook)) > 0 && len(slotIndicesFor(toType, catHook)) > 0
	}
	if templateExemptCats[category] {
		return true
	}
	// 静态表没有描述的扩展组件不能安全判断归属，维持旧行为而不强制退回。
	if _, known := canonicalRigIndex[category]; !known {
		return true
	}
	return len(slotIndicesFor(fromType, category)) > 0 && len(slotIndicesFor(toType, category)) > 0
}

func (s *Server) rigTypeExplicit(ctx context.Context, rodGUID string) bool {
	if s.DB == nil || rodGUID == "" {
		return false
	}
	v, err := s.DB.GetRodSetting(ctx, rodGUID, "rig_type_explicit")
	return err == nil && v == "1"
}

// correctStaleRigType 在实例或索引中的钓组类型不兼容竿型时，
// 按竿定义派生默认类型并落库。合法且由玩家显式选择的类型保持不变。
func (s *Server) correctStaleRigType(ctx context.Context, rigGUID string) {
	if rigGUID == "" {
		return
	}
	row, err := s.DB.GetItemByGUID(ctx, rigGUID)
	if err != nil || row == nil {
		return
	}
	def, derr := s.DB.GetItemDef(ctx, row.NumericID)
	if derr != nil || def == nil {
		return
	}
	allowed, ok := rodRigAllowed[rodDefRodType(def)]
	if !ok || len(allowed) == 0 {
		return // 未知竿型不过滤（与矩阵口径一致）
	}
	rowType := int64(0)
	if rt, found, gerr := s.DB.GetRigType(ctx, rigGUID); gerr == nil && found {
		rowType = rt
	}
	instType := int64(-1) // -1 = 实例 f5 无类型
	if fields, ferr := FieldsFromJSON(row.FieldsJSON); ferr == nil {
		if idx := fieldIdxByCS(row.WireClass, csRigObject); idx >= 0 && idx < len(fields) {
			if f5 := fields[idx].Val; f5 != nil && f5.FieldCount() > 1 && f5.Fields[1].Num != nil {
				instType = int64(u64Of(f5.Fields[1].Num))
			}
		}
	}
	badRow := !allowedContains(allowed, rowType)
	badInst := instType >= 0 && !allowedContains(allowed, instType)
	if !badRow && !badInst {
		return
	}
	newType := int64(0)
	if instType >= 0 && !badInst {
		// 实例类型合法时以实例为准，不回退玩家的选择。
		newType = instType
	} else {
		newType = s.defaultRigTypeForRod(ctx, row.ItemID)
		if !allowedContains(allowed, newType) {
			newType = allowed[0] // 双保险：派生值也必须合法
		}
	}
	if badInst {
		// 同步实例的名称和类型，必要时移除浮漂组件。
		s.rigItemMutate(ctx, rigGUID, nil, nil, &newType)
	}
	if badRow || rowType != newType {
		_ = s.DB.UpdateRigType(ctx, rigGUID, newType) // rig 表（rigSetType 同款写法）
	}
	s.log.Info("竿陈旧钓组类型矫正", "rod", shortGUID(rigGUID),
		"row_type", rowType, "inst_type", instType, "new_type", newType)
}

// rigTypeFitsInstalledComps 判断已装组件是否都能放入当前钓组模板。
// 未知品类和模板豁免品类不参与判断。
func rigTypeFitsInstalledComps(rigType int64, comps map[int64]rigComp) bool {
	perCategory := map[int64]int{}
	for key := range comps {
		perCategory[rigCompCategory(key)]++
	}
	for cat, count := range perCategory {
		if templateExemptCats[cat] {
			continue
		}
		if _, known := canonicalRigIndex[cat]; !known {
			continue
		}
		if len(slotIndicesFor(rigType, cat)) < count {
			return false
		}
	}
	return true
}

// firstCompatibleRigType 按竿型允许顺序返回第一个能容纳全部已装组件的钓组。
func firstCompatibleRigType(allowed []int64, comps map[int64]rigComp) (int64, bool) {
	for _, rt := range allowed {
		if rigNameForType(rt) == "" {
			continue
		}
		if rigTypeFitsInstalledComps(rt, comps) {
			return rt, true
		}
	}
	return 0, false
}

func allowedContains(allowed []int64, v int64) bool {
	for _, a := range allowed {
		if a == v {
			return true
		}
	}
	return false
}

// ---- 装配快照 ----

// buildRigSnapshot800 由三段素材构造 800 容器快照：
// f0 是新增物品，f1 是更新物品，f2 是移除的 GUID；空段为 null。
func buildRigSnapshot800(added []*proto.Value, updated []*proto.Value, removed []string) *proto.Value {
	f0 := proto.Null()
	if len(added) > 0 {
		f0 = proto.Args(proto.S("61"), added...)
	}
	f1 := proto.Null()
	if len(updated) > 0 {
		f1 = proto.Args(proto.S("61"), updated...)
	}
	f2 := proto.Null()
	if len(removed) > 0 {
		f2 = proto.GuidArr(removed)
	}
	return build800(f0, f1, f2)
}

// buildRigSnapshotForParent 根据数据库中的装备状态构造快照：
// 竿进入 f1，物品进入 f0，移除的 GUID 进入 f2。
// released 可选（variadic 保持旧调用点兼容）：去重退回背包物品的树并入 f1，
// 让客户端立即看到组件回到背包，避免残留"已挂载"显示。
func (s *Server) buildRigSnapshotForParent(ctx context.Context, parentGUID, itemGUID, removedGUID string, released ...[]*rigReleased) *proto.Value {
	var added []*proto.Value
	var updated []*proto.Value
	var removed []string
	if parentGUID != "" {
		if row, err := s.DB.GetItemByGUID(ctx, parentGUID); err == nil && row != nil {
			if t, terr := s.itemTree(ctx, row); terr == nil {
				updated = append(updated, t)
			}
		}
	}
	if itemGUID != "" {
		if row, err := s.DB.GetItemByGUID(ctx, itemGUID); err == nil && row != nil {
			if t, terr := s.itemTree(ctx, row); terr == nil {
				added = append(added, t)
			}
		}
	}
	if removedGUID != "" {
		removed = append(removed, removedGUID)
	}
	for _, group := range released {
		updated = append(updated, releasedReturnTrees(group)...)
	}
	return buildRigSnapshot800(added, updated, removed)
}

// queueSourceStackRefresh 在响应写回后强制刷新来源堆的客户端计数。
// 客户端只对「移除 + 新增」更新背包数量，同 guid 的数量更新会被忽略；
// 因此退回拆分子实例后要先 4/21 移除旧栈，再用 4/22 重新加入新数量的树。
func (s *Server) queueSourceStackRefresh(ctx *Ctx, results ...*rigDetachResult) {
	if ctx == nil {
		return
	}
	var guids []string
	var trees []*proto.Value
	seen := map[string]bool{}
	for _, res := range results {
		if res == nil || res.SourceGUID == "" || res.SourceTree == nil || seen[res.SourceGUID] {
			continue
		}
		seen[res.SourceGUID] = true
		guids = append(guids, res.SourceGUID)
		trees = append(trees, res.SourceTree)
	}
	if len(guids) == 0 {
		return
	}
	s.queueStackRefresh(ctx, guids, trees)
}

// queueStackRefresh 在回包后「先移除再重新加入」背包栈，强制客户端采用新数量。
func (s *Server) queueStackRefresh(ctx *Ctx, guids []string, trees []*proto.Value) {
	if ctx == nil || len(guids) == 0 || len(trees) == 0 {
		return
	}
	sess := ctx.Sess
	ctx.AfterResponse(func() {
		sess.Push4_21(guids)
		sess.Push(4, 22, proto.Args(proto.S("61"), trees...))
	})
}

// queueSplitSourceRefresh 在回包后刷新拆分挂载的来源堆计数。
func (s *Server) queueSplitSourceRefresh(ctx *Ctx, res *rigSetResult) {
	if res == nil || res.SourceGUID == "" || res.SourceTree == nil {
		return
	}
	s.queueStackRefresh(ctx, []string{res.SourceGUID}, []*proto.Value{res.SourceTree})
}

// buildDetachSnapshot 构造单件拆卸的响应快照：
// f1 包含退回树和竿，f2 包含整条删除的子实例 GUID。
func (s *Server) buildDetachSnapshot(ctx context.Context, parentGUID string, res *rigDetachResult) *proto.Value {
	var updated []*proto.Value
	updated = append(updated, res.trees()...)
	if parentGUID != "" {
		if row, err := s.DB.GetItemByGUID(ctx, parentGUID); err == nil && row != nil {
			if t, terr := s.itemTree(ctx, row); terr == nil {
				updated = append(updated, t)
			}
		}
	}
	return buildRigSnapshot800(nil, updated, res.removedGUIDs())
}

// buildDetachSnapshotAll 构造全部拆卸的响应快照：f1 包含全部退回树和竿。
func (s *Server) buildDetachSnapshotAll(ctx context.Context, parentGUID string, results []*rigDetachResult) *proto.Value {
	var updated []*proto.Value
	var removed []string
	for _, r := range results {
		updated = append(updated, r.trees()...)
		removed = append(removed, r.removedGUIDs()...)
	}
	if parentGUID != "" {
		if row, err := s.DB.GetItemByGUID(ctx, parentGUID); err == nil && row != nil {
			if t, terr := s.itemTree(ctx, row); terr == nil {
				updated = append(updated, t)
			}
		}
	}
	return buildRigSnapshot800(nil, updated, removed)
}

// buildRigSnapshotFull 构造 15/x、16/x、13/x 返回的全量快照。
func (s *Server) buildRigSnapshotFull(ctx context.Context, uid int64) *proto.Value {
	out := proto.Args(proto.S("61"))
	guids, _ := s.DB.ItemGUIDsByContainers(ctx, uid, []string{"backpack", "rig"})
	for _, g := range guids {
		if row, err := s.DB.GetItemByGUID(ctx, g); err == nil {
			if t, terr := s.itemTree(ctx, row); terr == nil {
				out.Items = append(out.Items, t)
			}
		}
	}
	return build800(out, proto.Null(), proto.Null())
}

// ---- 当前钓组的 157 对象 ----
//
// 14/2 和 4/22 共用。数据源是手持竿的实例树：f0 竿坯、f1 手柄、f2 竿座、
// f3 线、f4 轮、f5 钓组。渲染时重建 f5：
//   * 组件按规范索引升序排列，线和轮不进入 152 表；
//   * 不兼容的类型按竿型派生默认值；
//   * 沉底钓组移除浮漂，路亚钓组深度固定为 0。
// 实例 f5 是装配数据源，这里只做渲染期规范化。

// liveRig153 根据实例 f5 重建 153 钓组对象。
func (s *Server) liveRig153(ctx context.Context, row *store.Item, storedF5 *proto.Value) *proto.Value {
	comps := rigCompsOf(storedF5)
	name, curType, typeOK := rigIdentity(storedF5)
	if def, derr := s.DB.GetItemDef(ctx, row.NumericID); derr == nil && def != nil {
		if allowed, ok := rodRigAllowed[rodDefRodType(def)]; ok && len(allowed) > 0 {
			if !typeOK || !allowedContains(allowed, curType) {
				// 类型缺失或不兼容时按竿定义派生默认类型，并同步资源名。
				curType = s.defaultRigTypeForRod(ctx, row.ItemID)
				if mapped := rigNameForType(curType); mapped != "" {
					name = mapped
				}
			}
		}
	}
	depth := rigDepthOf(storedF5, defaultDepthFor(name, curType))
	if name == rigNameLure || (curType != 0 && curType == rigTypeLure) {
		depth = 0 // 路亚钓组没有钓棚深度
	}
	clipM := rigClipOf(storedF5)
	if saved, ok := s.savedReelClipM(ctx, row.GUID); ok {
		clipM = saved
	}
	return buildRig153Like(storedF5, name, curType, comps, depth, clipM)
}

// buildRigObject 构造当前钓组的 157 对象。
// 找不到竿物品行时使用兜底形状，包含数字 ID、工艺和耐久。
func (s *Server) buildRigObject(ctx context.Context, uid int64, gear string) *proto.Value {
	rodRow, rodGUID := s.rodRowForRigObject(ctx, uid, gear)
	if rodRow == nil {
		return s.rigItem157Fallback(ctx, uid, gear)
	}
	tree, err := s.itemTree(ctx, rodRow)
	if err != nil || tree == nil || tree.Kind != proto.KindObj || tree.FieldCount() <= 5 {
		return s.rigItem157Fallback(ctx, uid, rodGUID)
	}
	// 渲染期重建 f5；不能返回 null，否则客户端会清空插槽列表。
	storedF5 := tree.Fields[5].Val
	if storedF5 == nil || storedF5.Kind != proto.KindObj {
		storedF5 = s.defaultRig153(ctx, rodRow)
	} else {
		storedF5 = s.liveRig153(ctx, rodRow, storedF5)
	}
	out := *tree
	out.Fields = append([]proto.Field(nil), tree.Fields...)
	out.Fields[5] = proto.Field{Op: "VAL", Val: storedF5}
	return &out
}

// rodRowForRigObject 返回当前手持竿；没有手持竿时回退到背包或钓组中的第一支竿。
func (s *Server) rodRowForRigObject(ctx context.Context, uid int64, gear string) (*store.Item, string) {
	if gear != "" {
		if row, err := s.DB.GetItemByGUID(ctx, gear); err == nil && row != nil {
			return row, gear
		}
	}
	guids, _ := s.DB.ItemGUIDsByContainers(ctx, uid, []string{"backpack", "rig"})
	rodPrefixes := []string{"tele_", "spin_", "cast_", "match_", "feed_"}
	for _, g := range guids {
		row, err := s.DB.GetItemByGUID(ctx, g)
		if err != nil || row == nil {
			continue
		}
		if row.WireClass == "cnolibojkao" {
			return row, g
		}
		for _, p := range rodPrefixes {
			if strings.HasPrefix(row.ItemID, p) {
				return row, g
			}
		}
	}
	return nil, ""
}

// rigItem157Fallback 在没有竿实例行时构造兜底 157 对象。
// f3 是线，f5 是默认钓组，f10 是实例 GUID，f12 是容器，
// f13 是数字 ID，f14/f15 是工艺和耐久。
func (s *Server) rigItem157Fallback(ctx context.Context, uid int64, rigGUID string) *proto.Value {
	if rigGUID == "" {
		rigGUID = zeroGUID
	}
	var rigNID uint64
	var work, dur float64 = 1.0, 1.0
	if row, err := s.DB.GetItemByGUID(ctx, rigGUID); err == nil && row != nil {
		if row.NumericID > 0 {
			rigNID = uint64(row.NumericID)
		}
		work, dur = row.Workmanship, row.Durability
	}
	var comps map[int64]rigComp
	var name string
	var rigType int64 = 1
	if row, err := s.DB.GetItemByGUID(ctx, rigGUID); err == nil && row != nil {
		if fields, ferr := FieldsFromJSON(row.FieldsJSON); ferr == nil {
			if idx := fieldIdxByCS(row.WireClass, csRigObject); idx >= 0 && idx < len(fields) {
				if f5 := fields[idx].Val; f5 != nil && f5.Kind == proto.KindObj {
					comps = rigCompsOf(f5)
					n, t, ok := rigIdentity(f5)
					if n != "" {
						name = n
					}
					if ok && t > 0 {
						rigType = t
					}
				}
			}
		}
	}
	if len(comps) == 0 {
		comps = map[int64]rigComp{
			28: {GUID: "28000000000000000000000000000001", Extra: 125},
			12: {GUID: "12000000000000000000000000000001", Extra: 250},
			27: {GUID: "27000000000000000000000000000001", Extra: 350},
		}
		rigType = 1
		name = "rig_FloatSimple"
	}
	lineGUID := ""
	if g, gerr := s.DB.GetItemGUIDByRigSlot(ctx, rigGUID, 11); gerr == nil {
		lineGUID = g
	}
	clipM := int64(0)
	if saved, ok := s.savedReelClipM(ctx, rigGUID); ok {
		clipM = saved
	}
	f5 := buildRig153Full(name, rigType, comps, defaultDepthFor(name, rigType), clipM,
		s.baitMountFor(ctx, rigGUID), baitMountAvailForRig(rigType, 0))
	vals := map[int]*proto.Value{
		3:  proto.Guid(lineGUID),
		5:  f5,
		10: proto.Guid(rigGUID),
		12: proto.U32(0), // f12 容器枚举：0=背包
		13: proto.U32(uint32(rigNID)),
		14: proto.F32(float32(work)),
		15: proto.F32(float32(dur)),
		16: proto.U32(0), 17: proto.U32(0), 18: proto.U32(0),
		21: proto.U32(0), 22: proto.U32(0),
	}
	return wiregen.MustMakeObj("cnolibojkao", vals)
}

// ---- 请求参数扫描 ----
//
// 深度扫描按 DFS 前序读取 args 和嵌套容器中的值节点；
// 对象字段节点不参与 GUID 和整数过滤。

func rigScanValues(v *proto.Value, guids *[]string, ints *[]uint64) {
	if v == nil {
		return
	}
	switch v.Kind {
	case proto.KindGuid:
		if v.Hex != "" {
			*guids = append(*guids, v.Hex)
		}
	case proto.KindU32, proto.KindU64, proto.KindDateTime:
		*ints = append(*ints, v.Num)
	case proto.KindGuidArr:
		for _, it := range v.Items {
			if it != nil && it.Hex != "" {
				*guids = append(*guids, it.Hex)
			}
		}
	case proto.KindArgs, proto.KindValList, proto.KindArray, proto.KindU32Arr:
		for _, it := range v.Items {
			rigScanValues(it, guids, ints)
		}
	case proto.KindObj:
		// 对象字段不进入 GUID 和整数扫描。
	}
}

// rigCompArgs 解析装配请求中的 GUID 和整数列表。
func rigCompArgs(tree *proto.Value) (guids []string, ints []uint64) {
	rigScanValues(tree, &guids, &ints)
	return guids, ints
}

func shortGUID(g string) string {
	if len(g) > 8 {
		return g[:8]
	}
	return g
}

// ---- 命令处理器（13/x、15/x、16/x、17/x）----

func registerRigHandlers(d *Dispatch) {
	d.Register(13, 1, hHanditemSetComp)    // handitem/set_comp 装填（fire-and-forget+推送）
	d.Register(13, 2, hHanditemRemoveComp) // handitem/remove_comp 按物品 guid 拆
	d.Register(13, 3, hHanditemRemoveAll)  // handitem/remove_all 全拆
	d.Register(15, 1, hLureSetComp)        // lure/set_comp
	d.Register(15, 2, hLureRemoveComp)     // lure/remove_comp（显式槽位）
	d.Register(16, 1, hReelSetComp)        // reel/set_comp
	d.Register(16, 2, hReelRemoveComp)     // reel/remove_comp（显式槽位）
	d.Register(17, 1, hRigSetType)         // rod/set_rig 钓组类型
	d.Register(17, 2, hRodSetComp)         // rod/set_comp 安装配件（无槽位）
	d.Register(17, 3, hRodRemoveComp)      // rod/remove_comp 拆卸（显式槽位）
	d.Register(17, 4, hRigRemoveAll)       // rod/remove_all 全拆
	d.Register(17, 6, hRigBaitMount)       // rod/set_baitmount 挂饵方式
	d.Register(17, 7, hRodTake)            // rod/take 取竿到手上（158 手持上下文）
	d.Register(17, 8, hRodPut)             // rod/put 放竿，不结束这根竿的抛投
	d.Register(13, 4, hHanditemPrepare)    // handitem/prepare 手持物拿到手上（158）
	d.Register(13, 7, hHanditemSetDist)    // handitem/set_dist 手持物距离（158）
}

// hRodPut 是 fire-and-forget。竿仍在水中，继续保留自己的鱼和计时器；
// 放竿不修改背包或钓组，不能用全量物品同步重建客户端的水中装备。
func hRodPut(ctx *Ctx) ([]byte, bool) {
	st := ctx.Sess.Srv.fishingStateForRequest(ctx)
	st.mu.Lock()
	gear, pending, hooked := st.GearGuid, st.Pending, st.Hooked
	st.mu.Unlock()
	fish := ""
	if pending != nil {
		fish = shortGUID(pending.FishGUID)
	}
	ctx.Log.Info("放竿：保留水中状态", "rod", shortGUID(gear), "fish", fish, "hooked", hooked)
	return nil, true
}

// hHanditemPrepare 处理 13/4，将手持物拿到手上并返回 158 手持上下文。
// 响应必须包含有效钓组 GUID，否则客户端会丢失钓组状态。
func hHanditemPrepare(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	guids := deepGUIDs(ctx.Tree)
	gear := ""
	if len(guids) > 0 {
		gear = guids[0]
	}
	st := srv.selectFishingRod(ctx.Sess.Userid, gear)
	st.mu.Lock()
	if gear != "" {
		st.GearGuid = gear // 13/7 读它，不写会让后续 set_dist 回全零
	}
	cur := st.GearGuid
	depthCM := st.DepthCM
	rigType := st.RigType
	st.mu.Unlock()
	if rigType == 0 && cur != "" {
		if row, err := srv.DB.GetItemByGUID(ctx.Sess.pushCtx(), cur); err == nil && row != nil {
			rigType = srv.rigTypeOfItem(ctx.Sess.pushCtx(), row)
			st.mu.Lock()
			st.RigType = rigType
			st.mu.Unlock()
		}
	}
	body := castReady158(ctx, cur, depthCM, rigType)
	if body == nil {
		return nil, false
	}
	ctx.Log.Info("手持物准备", "gear", shortGUID(cur))
	return body, true
}

// hHanditemSetDist 处理 13/7，设置手持物距离并返回 158 手持上下文。
func hHanditemSetDist(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	st := srv.stateFor(ctx.Sess.Userid)
	st.mu.Lock()
	gear := st.GearGuid
	depthCM := st.DepthCM
	rigType := st.RigType
	st.mu.Unlock()
	if rigType == 0 && gear != "" {
		if row, err := srv.DB.GetItemByGUID(ctx.Sess.pushCtx(), gear); err == nil && row != nil {
			rigType = srv.rigTypeOfItem(ctx.Sess.pushCtx(), row)
			st.mu.Lock()
			st.RigType = rigType
			st.mu.Unlock()
		}
	}
	body := castReady158(ctx, gear, depthCM, rigType)
	if body == nil {
		return nil, false
	}
	return body, true
}

// castReady158 构造 158 手持上下文，供 14/1 和 17/7 共用。
// f0 是钓组 GUID，f1 是投掷距离，f2 是水深，f3 是当前线末端钩的 GUID。
// f3 为空或错误时客户端会认为装备未组装，后续 14/1 不会发出。
func castReady158(ctx *Ctx, gear string, depthCM int64, rigType int64) []byte {
	depthBits := castDepthBits
	if depthCM > 0 {
		depthBits = proto.F32Bits(float32(depthCM) / 100.0)
	}
	hookGUID := ""
	if gear != "" {
		hookGUID = ctx.Sess.Srv.mountedHookGuid(ctx.Sess.pushCtx(), gear)
	}
	profile := ctx.Sess.Srv.castDistanceForRig(ctx.Sess.pushCtx(), ctx.Sess.Userid, gear, rigType)
	obj := wiregen.MustMakeObj("lfnjkiffphn", map[int]*proto.Value{
		0: proto.Guid(guidOrZero(gear)),
		1: proto.U32(proto.F32Bits(float32(profile.DistanceM))),
		2: proto.U32(depthBits),
		3: proto.Guid(guidOrZero(hookGUID)),
	})
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		return nil
	}
	overTest := profile.RodTestMaxG > 0 && profile.PayloadWeightG > profile.RodTestMaxG
	payloadTestRatio := 0.0
	if profile.RodTestMaxG > 0 {
		payloadTestRatio = profile.PayloadWeightG / profile.RodTestMaxG
	}
	ctx.Log.Info("抛投距离计算", "rod", shortGUID(gear), "reel", shortGUID(profile.ReelGUID),
		"distance_m", math.Round(profile.DistanceM*100)/100,
		"base_m", math.Round(profile.BaseM*100)/100,
		"action", profile.CastAction, "power", profile.CastPower,
		"reel_type", profile.ReelType, "spool_size", profile.SpoolSize,
		"spool_capacity_m", math.Round(profile.SpoolCapacityM*100)/100,
		"spool_fill", math.Round(profile.SpoolFill*1000)/1000,
		"rod_length_m", math.Round(profile.RodLengthM*100)/100,
		"rod_action", profile.RodAction,
		"rod_stiffness", profile.RodStiffness,
		"rod_sensitivity", math.Round(profile.RodSensitivity*1000)/1000,
		"rod_test_g", fmt.Sprintf("%.1f-%.1f", profile.RodTestMinG, profile.RodTestMaxG),
		"line_diameter_mm", math.Round(profile.LineDiameterMM*1000)/1000,
		"line_length_m", math.Round(profile.LineLengthM*100)/100,
		"line_material", profile.LineMaterial,
		"line_stretch", math.Round(profile.LineStretch*1000)/1000,
		"line_density_gpm", math.Round(profile.LineLinearDensityGPM*1000)/1000,
		"line_running_weight_g", math.Round(profile.LineRunningWeightG*1000)/1000,
		"rig_weight_g", math.Round(profile.RigWeightG*100)/100,
		"bait_item", profile.BaitItemID,
		"bait_weight_g", math.Round(profile.BaitWeightG*100)/100,
		"payload_g", math.Round(profile.PayloadWeightG*100)/100,
		"payload_parts", profile.PayloadParts,
		"rod_over_test", overTest,
		"payload_to_test_max", math.Round(payloadTestRatio*1000)/1000,
		"weight_factor", math.Round(profile.WeightFactor*1000)/1000,
		"skill_cat", profile.SkillCategory, "skill_mastery", profile.SkillMastery,
		"skill_cast_pct", math.Round(profile.SkillCastPct*1000)/1000,
		"skill_factor", math.Round(profile.SkillFactor*1000)/1000,
		"special_skill_factor", math.Round(profile.SpecialSkillFactor*1000)/1000,
		"player_primary", math.Round(profile.PlayerPrimary*1000)/1000,
		"player_secondary", math.Round(profile.PlayerSecondary*1000)/1000,
		"friction", math.Round(profile.Friction*1000)/1000,
		"efficiency", math.Round(profile.Efficiency*1000)/1000,
		"bearing_quality", profile.BearingQuality, "oil_quality", profile.OilQuality,
		"line_limit_m", profile.LineLimitM, "clip_m", profile.ClipLimitM)
	return body
}

// guidOrZero 将空 GUID 编码为 32 个零。
// 空字符串会产生零长度 GUID，客户端无法将其解析为 Guid.Empty。
func guidOrZero(h string) string {
	if h == "" {
		return "00000000000000000000000000000000"
	}
	return h
}

// hRodTake 处理 17/7，将指定竿拿到手上并返回 158 手持上下文。
// 请求参数是竿 GUID。会话中的 gear_guid 必须同步更新，后续 14/1、14/2 依赖它。
func hRodTake(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	guids := deepGUIDs(ctx.Tree)
	rod := ""
	if len(guids) > 0 {
		rod = guids[0]
	}
	st := srv.stateFor(ctx.Sess.Userid)
	if !srv.pendingCatchAllowsCast(ctx, st) {
		return nil, true
	}
	st = srv.selectFishingRod(ctx.Sess.Userid, rod)
	st.mu.Lock()
	if rod != "" {
		st.GearGuid = rod
	}
	gear := st.GearGuid
	depthCM := st.DepthCM
	rigType := st.RigType
	st.mu.Unlock()

	if gear == "" {
		// 没有可取竿时返回全零 GUID。
		if body := castReady158(ctx, "", depthCM, rigType); body != nil {
			return body, true
		}
		return nil, false
	}
	row, err := srv.DB.GetItemByGUID(ctx.Sess.pushCtx(), gear)
	category, categoryKnown := int64(0), false
	if row != nil {
		category, categoryKnown = srv.itemCategoryOf(ctx.Sess.pushCtx(), row)
	}
	if err != nil || row == nil || row.UID != ctx.Sess.Userid || row.Durability <= 0 || !categoryKnown || category != gearCatRod {
		st.mu.Lock()
		st.GearGuid = ""
		st.mu.Unlock()
		ctx.Log.Info("取竿被拒：鱼竿无效或已损坏", "rod", shortGUID(gear))
		if body := castReady158(ctx, "", depthCM, 0); body != nil {
			return body, true
		}
		return nil, false
	}
	// 水域装备限制：当前地图禁用的竿不可取（客户端库存格已灰置时
	// 正常走不到这里，兜底直发消息的情况）。
	if reason := srv.placeRodViolation(ctx.Sess.pushCtx(), ctx.Sess.Userid, gear); reason != "" {
		st.mu.Lock()
		st.GearGuid = ""
		st.mu.Unlock()
		ctx.Log.Info("17/7 被拒：装备限制", "rod", shortGUID(gear), "reason", reason)
		if body := castReady158(ctx, "", depthCM, 0); body != nil {
			return body, true
		}
		return nil, false
	}
	// 同步建档和尖梢收敛；新竿的尖梢补齐后立即推送完整竿树。
	if srv.ensureRig(ctx.Sess.pushCtx(), ctx.Sess.Userid, gear) {
		ctx.Sess.Push4_22Full()
	}
	// 每次换竿都读取该竿的钓组，不能沿用上一根竿的浮钓/路亚类型。
	row, err = srv.DB.GetItemByGUID(ctx.Sess.pushCtx(), gear)
	if err != nil || row == nil {
		ctx.ReplyFailure("System.IO.IOException", "鱼竿状态未能读取，请重试")
		return nil, true
	}
	rigType = srv.rigTypeOfItem(ctx.Sess.pushCtx(), row)
	st.mu.Lock()
	st.RigType = rigType
	st.mu.Unlock()
	body := castReady158(ctx, gear, depthCM, rigType)
	if body == nil {
		return nil, false
	}
	ctx.Log.Info("取竿", "rod", shortGUID(gear), "hook", shortGUID(srv.mountedHookGuid(ctx.Sess.pushCtx(), gear)))
	return body, true
}

// hRodSetComp 处理 17/2：
// [容器, 竿 GUID, 物品容器, 物品 GUID, int]，槽位由物品品类推导。
// 响应必须是 800 快照；裸 args 无法通过客户端类型转换。
func hRodSetComp(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	guids, ints := rigCompArgs(ctx.Tree)
	if len(guids) < 2 {
		// 参数缺失时记录原始 body，便于核对请求形状。
		ctx.Log.Warn("17/2 guid 不足", "guids", len(guids), "ints", len(ints),
			"tree_nil", ctx.Tree == nil, "body", hexHead(ctx.Body, 64))
		return emptyArgsBody(), true
	}
	parentGUID, itemGUID := guids[0], guids[1]
	extra := int64(0)
	if len(ints) > 2 {
		extra = int64(ints[2])
	} else if len(ints) > 1 {
		extra = int64(ints[1])
	}
	cctx := ctx.Sess.pushCtx()
	// 注意：官服允许装配限制渔轮（>6000 型在铜壶可装但不可抛），
	// 这里不做水域装备校验；拦截统一放在 14/2 抛竿入口。
	res := srv.rigSetComponent(cctx, ctx.Sess.Userid, parentGUID, 0, itemGUID, extra,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true, SplitStack: true})
	var snap *proto.Value
	if res.hasMaterials() {
		snap = buildRigSnapshot800(res.AddedTrees, res.UpdatedTrees, res.RemovedGUIDs)
	} else {
		snap = srv.buildRigSnapshotForParent(cctx, parentGUID, itemGUID, res.ReplacedGUID)
	}
	body, err := proto.EncodeValue(snap, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	srv.queueSplitSourceRefresh(ctx, res)
	srv.queueReleasedStackRefresh(ctx, res.Released)
	return body, true
}

// hRodRemoveComp 处理 17/3：
// [容器, 竿 GUID, 槽位品类, 物品 GUID, int]。
// 响应为 800 快照：退回物品进入 f1，只有整条删除的子实例进入 f2。
func hRodRemoveComp(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	guids, ints := rigCompArgs(ctx.Tree)
	if len(guids) < 1 {
		ctx.Log.Warn("17/3 guid 不足", "guids", len(guids), "body", hexHead(ctx.Body, 64))
		return emptyArgsBody(), true
	}
	slot := int64(0)
	if len(ints) > 1 {
		slot = int64(ints[1])
	}
	itemGUID := ""
	if len(guids) > 1 {
		itemGUID = guids[1]
	}
	extra := int64(0)
	if len(ints) > 2 {
		extra = int64(ints[2])
	}
	parentGUID := guids[0]
	cctx := ctx.Sess.pushCtx()
	res := srv.rigRemoveComponentInstance(cctx, ctx.Sess.Userid, parentGUID, slot, itemGUID, extra)
	body, err := proto.EncodeValue(
		srv.buildDetachSnapshot(cctx, parentGUID, res), ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	srv.queueSourceStackRefresh(ctx, res)
	// 800 f1 只更新物品数据，客户端的竿 3D 建模不在 f1 原位更新时重建——
	// 装轮可见是随后 17/7 取竿的 158 重建在起作用，拆轮没有后续取竿，模型
	// 就把轮留在竿上（线上 2026-11-12 实锤：17/3 已回 f4=空的竿树，建模仍挂轮）。
	// 拆卸真实改动了装配，回包后按工坊口径补推 4/22 竿树，客户端按版本戳
	// 更大的新快照重建模型（02ed55c 同因同修法）。
	if res != nil {
		srv.pushRodModelRefresh(ctx, parentGUID)
	}
	return body, true
}

// hRigRemoveAll 处理 17/4：[容器, 竿 GUID]。
// f1 包含全部退回树和竿，f2 只包含整条删除的子实例。
func hRigRemoveAll(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	guids, _ := rigCompArgs(ctx.Tree)
	if len(guids) < 1 {
		return emptyArgsBody(), true
	}
	parentGUID := guids[0]
	cctx := ctx.Sess.pushCtx()
	results := srv.rigRemoveAll(cctx, ctx.Sess.Userid, parentGUID)
	body, err := proto.EncodeValue(
		srv.buildDetachSnapshotAll(cctx, parentGUID, results), ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	srv.queueSourceStackRefresh(ctx, results...)
	// 与 17/3 同因：全拆后竿模型不随 800 f1 重建，回包后补推 4/22 竿树。
	if len(results) > 0 {
		srv.pushRodModelRefresh(ctx, parentGUID)
	}
	return body, true
}

// pushRodModelRefresh 回包后抬版本戳并补推 4/22 竿树，驱动客户端重建竿建模。
// 800 f1 的竿树已把拆卸落库时的版本戳发给客户端，而客户端只接受版本戳严格
// 变大的 4/22 更新（bumpCreatedTicks 口径），且 f1 应用与推送处理存在竞态：
// 推送树与 f1 同版本会被按「版本未变」丢弃（线上 2026-11-12 实锤：多次拆轮
// 仅一次消失，成功那次是推送赶在 f1 应用前被处理）。推送前再抬一次版本戳
// 并落库，保证 4/22 的树严格新于 f1，重建不再依赖竞态。
func (s *Server) pushRodModelRefresh(ctx *Ctx, rodGUID string) {
	ctx.AfterResponse(func() {
		pctx := ctx.Sess.pushCtx()
		if row, err := s.DB.GetItemByGUID(pctx, rodGUID); err == nil && row != nil &&
			row.UID == ctx.Sess.Userid {
			if fields, ferr := FieldsFromJSON(row.FieldsJSON); ferr == nil {
				s.saveItemFields(pctx, row, fields)
			}
		}
		ctx.Sess.Push4_22ItemGUIDs([]string{rodGUID})
	})
}

// hRigSetType 处理 17/1：[容器, 竿 GUID, 钓组类型]。
// 请求没有钓组 GUID；类型缺失时使用 1。
func hRigSetType(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	guids, ints := rigCompArgs(ctx.Tree)
	if len(guids) < 1 {
		return emptyArgsBody(), true
	}
	rodGUID := guids[0]
	rigType := int64(1)
	if len(ints) > 0 {
		if v := int64(ints[len(ints)-1]); v > 0 && v <= 255 {
			rigType = v
		}
	}
	cctx := ctx.Sess.pushCtx()
	var released []*rigReleased
	if !srv.rigSetType(cctx, ctx.Sess.Userid, rodGUID, rigType, &released) {
		ctx.Log.Warn("17/1 竿不属于当前玩家，已忽略", "竿", shortGUID(rodGUID))
		return emptyArgsBody(), true
	}
	body, err := proto.EncodeValue(
		srv.buildRigSnapshotForParent(cctx, rodGUID, "", "", released), ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	srv.queueReleasedStackRefresh(ctx, released)
	return body, true
}

// hRigBaitMount 处理 17/6：[容器, 竿 GUID, BaitMountType 位掩码]。
// 第三个参数不是物品 GUID；请求只有容器号时不写入。
func hRigBaitMount(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	guids, ints := rigCompArgs(ctx.Tree)
	if len(guids) < 1 {
		return emptyArgsBody(), true
	}
	cctx := ctx.Sess.pushCtx()
	if len(ints) > 1 {
		rodGUID := guids[0]
		if srv.ownedRodRow(cctx, ctx.Sess.Userid, rodGUID) == nil {
			ctx.Log.Warn("17/6 竿不属于当前玩家，已忽略", "竿", shortGUID(rodGUID))
			return emptyArgsBody(), true
		}
		mask := clampBaitMount(int64(ints[len(ints)-1]))
		// 客户端 17/6 只带一个整数，没有语义名；落库前先记一条。
		ctx.Log.Info("17/6 挂饵方式", "rod", shortGUID(rodGUID), "mask", mask, "guids", len(guids), "ints", len(ints))
		_ = srv.DB.UpsertRodSetting(cctx, rodGUID, baitMountSettingKey, strconv.FormatInt(mask, 10))
		if err := srv.applyRigBaitMount(cctx, rodGUID, mask); err != nil {
			ctx.Log.Warn("写入挂饵方式失败", "rod", shortGUID(rodGUID), "mask", mask, "err", err)
		}
	}
	body, err := proto.EncodeValue(srv.buildRigSnapshotFull(cctx, ctx.Sess.Userid), ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// hLureSetComp 处理 15/1：[容器, 父 GUID, 容器, 物品 GUID, int]。
// 尾部 int 是 0 基槽位下标（客户端 dump：agfonbnkeoh.dihplofkffm 按
// 数组 Length 循环 new ogilnngmlnp(父, i)，i 即下标；发送端
// bbpmdcodcee.hbckkobipmd 把它作为最后一个参数）。旧实现读 ints[1]
// ——那是容器枚举，永远解析不中，多槽拟饵只能换活动钩 0。
// 返回全量快照；钩的列表归属回包后用 4/21+4/22 重建。
func hLureSetComp(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	guids, ints := rigCompArgs(ctx.Tree)
	if len(guids) < 2 {
		return emptyArgsBody(), true
	}
	extra := int64(0)
	if len(ints) > 0 {
		extra = int64(ints[len(ints)-1])
	}
	ctx.Log.Info("15/1 拟饵换钩请求", "lure", shortGUID(guids[0]), "hook", shortGUID(guids[1]), "ints", ints)
	cctx := ctx.Sess.pushCtx()
	res := srv.rigSetComponent(cctx, ctx.Sess.Userid, guids[0], 0, guids[1], extra,
		rigSetOpts{ParentType: familyLure, EnforceCategory: true, SplitStack: true})
	// 三段增量响应（与 17/2 装配件同构）：f0=新装钩树+出厂钩退包新物品，
	// f1=拟饵/退回旧钩树。全量快照会把同 guid 树放进 f0 新增段，客户端
	// 面板与背包都不按预期刷新（rigops.go 13/1 教训）。
	var snap *proto.Value
	if res.hasMaterials() {
		snap = buildRigSnapshot800(res.AddedTrees, res.UpdatedTrees, res.RemovedGUIDs)
	} else {
		snap = srv.buildRigSnapshotFull(cctx, ctx.Sess.Userid)
	}
	body, err := proto.EncodeValue(snap, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	srv.queueSplitSourceRefresh(ctx, res)
	srv.queueReleasedStackRefresh(ctx, res.Released)
	// 被替换的玩家钩（带物品行）从拟饵挪回背包：f1 更新段不重建列表
	// 归属，补一次 4/21+4/22。新装钩由响应 f0 完成归属重建、出厂钩退包
	// 是新 guid 走 f0——这两者绝不能推送：重加新装钩会让客户端把它挂回
	// 背包（v4 实测），清合成 GUID 会让注册表查不到重物化的钩。
	if res.ReplacedGUID != "" {
		for _, t := range res.UpdatedTrees {
			if treeRootGUID(t) == res.ReplacedGUID {
				srv.queueStackRefresh(ctx, []string{res.ReplacedGUID}, []*proto.Value{t})
				break
			}
		}
	}
	return body, true
}

// hLureRemoveComp 处理 15/2：[容器, 父 GUID, 槽位品类, 物品 GUID, int]。
// 槽位品类取第二个整数；返回全量快照。
func hLureRemoveComp(ctx *Ctx) ([]byte, bool) {
	return hRigRemoveCompFamily(ctx)
}

// hReelSetComp 处理 16/1：[容器, 父 GUID, 容器, 物品 GUID]，不带槽位。
func hReelSetComp(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	guids, _ := rigCompArgs(ctx.Tree)
	if len(guids) < 2 {
		return emptyArgsBody(), true
	}
	cctx := ctx.Sess.pushCtx()
	res := srv.rigSetComponent(cctx, ctx.Sess.Userid, guids[0], 0, guids[1], 0,
		rigSetOpts{ParentType: familyReel, EnforceCategory: true, SplitStack: true})
	body, err := proto.EncodeValue(srv.buildRigSnapshotFull(cctx, ctx.Sess.Userid), ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	srv.queueSplitSourceRefresh(ctx, res)
	srv.queueReleasedStackRefresh(ctx, res.Released)
	return body, true
}

// hReelRemoveComp 处理 16/2：[容器, 父 GUID, 槽位品类, 物品 GUID]。
func hReelRemoveComp(ctx *Ctx) ([]byte, bool) {
	return hRigRemoveCompFamily(ctx)
}

// hRigRemoveCompFamily 供 15/2 和 16/2 共用，读取第二个整数作为槽位品类。
func hRigRemoveCompFamily(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	guids, ints := rigCompArgs(ctx.Tree)
	if len(guids) < 1 {
		ctx.Log.Warn("17/3 guid 不足", "guids", len(guids), "body", hexHead(ctx.Body, 64))
		return emptyArgsBody(), true
	}
	slot := int64(0)
	if len(ints) > 1 {
		slot = int64(ints[1])
	}
	// 请求带物品 GUID 时按 GUID 精确拆（15/2 换钩/拆钩必须拆指定那颗），
	// 不带 GUID 时退回按槽位拆第一件。
	itemGUID := ""
	if len(guids) > 1 {
		itemGUID = guids[1]
	}
	extra := int64(0)
	if len(ints) > 2 {
		extra = int64(ints[2])
	}
	cctx := ctx.Sess.pushCtx()
	res := srv.rigRemoveComponentInstance(cctx, ctx.Sess.Userid, guids[0], slot, itemGUID, extra)
	// 拟饵拆钩走三段增量（与 17/3 同构）：f1=拟饵+退回钩树刷新详情面板
	// 与背包；f0=出厂钩退包的新物品；f2 为空（退回不删除）。全量快照的
	// 同 guid f0 段不刷新客户端；对正展示中的拟饵发 4/21 移除则会把
	// 详情页弹回背包界面（2026-11-09 两轮实机验证）。
	var snap *proto.Value
	if res != nil && isLureGUID(cctx, srv, guids[0]) {
		detachTrees := srv.lureDetachTrees(cctx, guids[0], res)
		var added []*proto.Value
		if res.RefundTree != nil {
			added = append(added, res.RefundTree)
		}
		snap = buildRigSnapshot800(added, detachTrees, res.removedGUIDs())
		body, err := proto.EncodeValue(snap, ctx.Sess.Table())
		if err != nil {
			return nil, false
		}
		// 仅当被拆的是玩家钩（带物品行、ReturnedTree 非空）时补 4/21+4/22
		// 重进背包列表——f1 更新段不重建列表归属。拟饵树、出厂钩合成 GUID、
		// 退包新实例一律不推：拟饵重加会弹掉详情页、清合成 GUID 会让注册
		// 表查不到重物化的钩（v1/v4 两轮实测）。
		if res.ReturnedTree != nil && res.CompGUID != "" {
			srv.queueStackRefresh(ctx, []string{res.CompGUID}, []*proto.Value{res.ReturnedTree})
		}
		return body, true
	}
	body, err := proto.EncodeValue(srv.buildRigSnapshotFull(cctx, ctx.Sess.Userid), ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// treeRootGUID 提取树根对象的 GUID 字段；读不出返回空串。
func treeRootGUID(t *proto.Value) string {
	if t == nil || t.Kind != proto.KindObj {
		return ""
	}
	if i := fieldIdxByCS(t.Class, csGUID); i >= 0 && i < len(t.Fields) && t.Fields[i].Hex != "" {
		return t.Fields[i].Hex
	}
	return ""
}

// isLureGUID 判断 guid 是否为拟饵实例（读不到或非拟饵返回 false）。
func isLureGUID(ctx context.Context, srv *Server, guid string) bool {
	row, err := srv.DB.GetItemByGUID(ctx, guid)
	return err == nil && isLureRow(row)
}

// lureTreeByGUID 重新读取拟饵实例的完整树；读不到返回 nil。
func (s *Server) lureTreeByGUID(ctx context.Context, guid string) *proto.Value {
	row, err := s.DB.GetItemByGUID(ctx, guid)
	if err != nil || row == nil {
		return nil
	}
	t, err := s.itemTree(ctx, row)
	if err != nil {
		return nil
	}
	return t
}

// lureDetachTrees 组装 15/2 拟饵拆钩响应的 f1 更新段：退回钩树 + 拟饵树。
func (s *Server) lureDetachTrees(ctx context.Context, lureGUID string, res *rigDetachResult) []*proto.Value {
	updated := res.trees()
	if t := s.lureTreeByGUID(ctx, lureGUID); t != nil {
		updated = append(updated, t)
	}
	return updated
}

// hHanditemSetComp 处理 13/1：[容器, 父 GUID, 容器, 物品 GUID]。
// 缺省只装 1 份，其余留背包，抛后自动补填；handitem_load_one=0 回退整堆装填。
// 该命令无需客户端回包，
// 物品变化通过 4/22 和 4/21 推送；响应仍保留完整增量结构。
func hHanditemSetComp(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	guids, _ := rigCompArgs(ctx.Tree)
	if len(guids) < 2 {
		return srv.rigFullSnapshotBody(ctx), true
	}
	parentGUID, itemGUID := guids[0], guids[1]
	cctx := ctx.Sess.pushCtx()
	res := srv.rigSetComponent(cctx, ctx.Sess.Userid, parentGUID, 0, itemGUID, 0,
		rigSetOpts{ParentType: familyHanditem, EnforceCategory: true,
			SplitStack: false, HanditemFullStack: true})
	if res.hasMaterials() {
		srv.pushHanditemInventory(ctx, res, itemGUID)
		// 返回三段增量：f0 新子实例，f1 来源堆和父对象，f2 移除项。
		// 不能返回全量快照。
		snap := buildRigSnapshot800(res.AddedTrees, res.UpdatedTrees, res.RemovedGUIDs)
		// 客户端只在创建时建立父子归属，同 guid 的数量更新会被忽略：被替换
		// 下来的旧弹药只作为 f1 更新回传时，弹药格里仍留着它（2026-11-07 实测
		// 连装两袋后格子里出现两份、切换看起来无效）。回包后用 4/21 移除 +
		// 4/22 重加把它真正落回背包；tunable handitem_switch_refresh 缺省开。
		if res.ReplacedToBackpack != "" && srv.tunableF("handitem_switch_refresh", 1) != 0 {
			if prev, perr := srv.DB.GetItemByGUID(cctx, res.ReplacedToBackpack); perr == nil && prev != nil {
				if t, terr := srv.itemTree(cctx, prev); terr == nil {
					srv.queueStackRefresh(ctx, []string{res.ReplacedToBackpack}, []*proto.Value{t})
					srv.log.Info("13/1 替换旧弹药：回包后移除+重加", "old", shortGUID(res.ReplacedToBackpack),
						"parent", shortGUID(parentGUID))
				}
			}
		}
		body, err := proto.EncodeValue(snap, ctx.Sess.Table())
		if err != nil {
			return nil, false
		}
		return body, true
	}
	return srv.rigFullSnapshotBody(ctx), true
}

// pushHanditemInventory 将挂载产生的物品变化通过 4/22 和 4/21 推送。
// 替换组件时来源堆已在 updated_trees 中；首次装填需要显式读取一次，
// 来源堆已删除时自动跳过。
func (s *Server) pushHanditemInventory(ctx *Ctx, res *rigSetResult, sourceGUID string) {
	add := res.AddedTrees
	upd := res.UpdatedTrees
	rem := res.RemovedGUIDs
	if sourceGUID != "" {
		if src, err := s.DB.GetItemByGUID(ctx.Sess.pushCtx(), sourceGUID); err == nil && src != nil {
			if t, terr := s.itemTree(ctx.Sess.pushCtx(), src); terr == nil {
				upd = append(append([]*proto.Value{}, upd...), t)
			}
		}
	}
	if len(add)+len(upd) > 0 {
		items := append(append([]*proto.Value{}, add...), upd...)
		ctx.Sess.Push(4, 22, proto.Args(proto.S("61"), items...))
	}
	if len(rem) > 0 {
		ctx.Sess.Push(4, 21, proto.Args(proto.S("524"), proto.GuidArr(rem)))
	}
	ctx.Log.Info("13/1 推背包增量", "added", len(add), "updated", len(upd), "removed", len(rem))
}

// hHanditemRemoveComp 处理 13/2：[容器, 父 GUID, 物品 GUID]。
// 请求不含槽位，按物品 GUID 反查槽位；返回全量快照。
func hHanditemRemoveComp(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	guids, _ := rigCompArgs(ctx.Tree)
	if len(guids) < 1 {
		return emptyArgsBody(), true
	}
	itemGUID := ""
	if len(guids) > 1 {
		itemGUID = guids[1]
	}
	cctx := ctx.Sess.pushCtx()
	res := srv.rigRemoveItemComponent(cctx, ctx.Sess.Userid, guids[0], itemGUID)
	body, err := proto.EncodeValue(srv.buildRigSnapshotFull(cctx, ctx.Sess.Userid), ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	srv.queueSourceStackRefresh(ctx, res)
	return body, true
}

// hHanditemRemoveAll 处理 13/3，返回全量快照。
func hHanditemRemoveAll(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	guids, _ := rigCompArgs(ctx.Tree)
	if len(guids) < 1 {
		return emptyArgsBody(), true
	}
	cctx := ctx.Sess.pushCtx()
	results := srv.rigRemoveAll(cctx, ctx.Sess.Userid, guids[0])
	body, err := proto.EncodeValue(srv.buildRigSnapshotFull(cctx, ctx.Sess.Userid), ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	srv.queueSourceStackRefresh(ctx, results...)
	return body, true
}

// rigFullSnapshotBody 返回全量快照；编码失败时返回空 args。
func (s *Server) rigFullSnapshotBody(ctx *Ctx) []byte {
	snap := s.buildRigSnapshotFull(ctx.Sess.pushCtx(), ctx.Sess.Userid)
	body, err := proto.EncodeValue(snap, ctx.Sess.Table())
	if err != nil {
		return emptyArgsBody()
	}
	return body
}

// hexHead 返回字节切片前 n 字节的十六进制文本。
func hexHead(b []byte, n int) string {
	if len(b) > n {
		b = b[:n]
	}
	return fmt.Sprintf("%x", b)
}

// baitSlotCount 返回补饵记录要遍历的饵位数量。
func baitSlotCount(rigType int64) int {
	return max(2, len(slotIndicesFor(rigType, catHook)))
}

// baitSlotCountIn 是 baitSlotCount 的运行时版本：饵位数量必须覆盖实际钩位，
// 否则钩序号大于 1 的钩上断饵后，rebait:<钩序号> 记录永远读不到、补不上饵。
func baitSlotCountIn(rigType int64, comps map[int64]rigComp) int {
	return max(2, hookPositionsIn(rigType, comps))
}

// hookPositionsFor 返回钓组模板声明的钩位数量。
//
// 只数 catHook 槽位。拟饵族（catLure=17）在同一品类下同时承载“亮片”和
// “基本吸引成分/钓钩吸引成分/鱼片”这些挂件，海钓铁板族因此有 300+325+
// 326+327 多个拟饵槽位，但钩位始终只有一个。把拟饵槽位数并进来会把单钩
// 铁板竿误判成多钩：挂饵分组被拆碎，判鱼时每组各选一次。
//
// 这是静态上限；运行时钩位数见 hookPositionsIn。
func hookPositionsFor(rigType int64) int {
	n := len(slotIndicesFor(rigType, catHook))
	if n < 1 {
		return 1
	}
	return n
}

// hookPositionsIn 返回运行时钩位数：模板声明数与 f5 实际已装钩组件的较大者。
//
// 海钓倒钓臂展开后，客户端会往 f5 里装 250/1001/1005/1009 多条 catHook
// 组件（logs/gameserver.log 2026-11-09 10:26:54–10:28:59 实测四条共存）。
// 服务端没有单独的“倒钓臂已展开”开关事件：钩组件本身就是运行时状态，
// 因此钩位数只能按实际组件数取，不能只看 rigSlotTemplates（rig 50/51
// 模板只声明 250 一个钩位）。只看模板会把多钩当单钩：所有饵并进第一枚钩，
// 其余钩永远不判鱼、不吃饵、断件也不算。
//
// 取较大者是为了兼容模板已声明多钩位的钓组（如 rig 59 的 250/251/252）：
// 钩还没装满时不能因为实际少了几条就把上限缩小。
func hookPositionsIn(rigType int64, comps map[int64]rigComp) int {
	n := hookPositionsFor(rigType)
	if k := len(rigCompKeysOfCategory(comps, catHook)); k > n {
		return k
	}
	return n
}
