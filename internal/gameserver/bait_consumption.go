package gameserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"rf4server/internal/store"
)

type baitChange struct {
	Updated []string `json:"updated,omitempty"`
	Removed []string `json:"removed,omitempty"`
	// Groundbait 是本次入水被消耗的窝料/食笼/PVA 份数，用于把它们记进落点格子。
	Groundbait []groundbaitUse `json:"groundbait,omitempty"`
}

// groundbaitUse 一次入水消耗掉的窝料明细。
type groundbaitUse struct {
	ItemID   string  `json:"item_id"`
	Units    float64 `json:"units"`
	Category int64   `json:"category"`
	// GUID 是这份窝料的物品实例。自制窝料没有独立 item_id，能不能吸引对口的鱼
	// 全看这个实例当初是用什么原料做的。
	GUID string `json:"guid,omitempty"`
}

type baitedHookSet struct {
	ordinal   int64
	hookGUID  string
	baits     []string
	baitGUIDs []string
}

type rebaitRequest struct {
	ItemID   string `json:"item"`
	HookGUID string `json:"hook"`
}

// 单钩双饵保留组合，多钩按组件 ordinal 各自判鱼和消耗。
func (s *Server) baitedHookSets(ctx context.Context, rod string) []baitedHookSet {
	if s == nil || s.DB == nil || rod == "" {
		return nil
	}
	comps := s.storedRigComponents(ctx, rod)
	rigType := s.rigTypeForWrite(ctx, rod)
	// 钩位数按运行时实际钩组件算：倒钓臂展开后 rig 50/51 会有 250/1001/1005/1009
	// 多条钩，只看模板会把多钩当单钩，饵全部并进第一枚钩。
	count := hookPositionsIn(rigType, comps)
	assignments, err := s.baitHookAssignments(ctx, rod, comps, count <= 1)
	if err != nil {
		return nil
	}
	groups := map[int64]*baitedHookSet{}
	for _, category := range []int64{catBait, catLure} {
		for _, key := range rigCompKeysOfCategory(comps, category) {
			comp := comps[key]
			row, err := s.DB.GetItemByGUID(ctx, comp.GUID)
			if err != nil || row == nil || s.itemBrokenForMount(ctx, row) || category == catBait && baitQuantity(row) <= 0 {
				continue
			}
			ordinal := rigCompOrdinal(key)
			if count <= 1 {
				ordinal = 0
			} else {
				hook := assignments[row.GUID]
				found := false
				for _, hk := range rigCompKeysOfCategory(comps, catHook) {
					if comps[hk].GUID == hook {
						ordinal, found = rigCompOrdinal(hk), true
						break
					}
				}
				if !found && category == catLure && s.lureActiveHookGUID(ctx, row.GUID) == hook && hook != "" {
					found = true
				}
				if !found {
					continue
				}
			}
			group := groups[ordinal]
			if group == nil {
				group = &baitedHookSet{ordinal: ordinal}
				groups[ordinal] = group
			}
			id := row.ItemID
			if id == "" {
				if def, err := s.itemDefForRow(ctx, row); err == nil && def != nil {
					id = def.ItemID
				}
			}
			if id == "" {
				continue
			}
			group.baits = append(group.baits, id)
			group.baitGUIDs = append(group.baitGUIDs, row.GUID)
			// 同一钩位可能挂了多个组件（钩 + 软饵/珠子），钩位只认第一个
			// 分到钩上的那个，不能被后面拟饵自带的钩覆盖。
			if group.hookGUID == "" {
				group.hookGUID = assignments[row.GUID]
				if group.hookGUID == "" && category == catLure {
					group.hookGUID = s.lureActiveHookGUID(ctx, row.GUID)
				}
			}
		}
	}
	var out []baitedHookSet
	for _, group := range groups {
		if len(group.baits) > 0 && group.hookGUID != "" {
			out = append(out, *group)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ordinal < out[j].ordinal })
	return out
}

const (
	// 客户端 RigMarineBase 的钩位子槽：钩位从 1001 起每 4 个索引一组
	// （1001/1005/1009），组内 1002/1003/1004 是挂在该钩位上的拟饵与珠子。
	hookSlotBaseIndex = 1001
	hookSlotStride    = 4
)

// hookAnchorTable 把钩组件的扩展槽位（extra）映射成钩位，用于把饵归到真实钩位。
//
// 只在杆上确实存在海钓钩位子槽（extra ≥ 1001）时可用：钩位 extra 唯一，
// 饵的 extra 落在哪个钩位的组内就属于哪个钩位。主线上的饵（extra < 1001，
// 例如 300/325/326）没有子槽结构，区分不了同一层上的多枚钩，继续沿用
// bait_hook 记录与既有补齐规则。
type hookAnchorTable struct {
	byExtra map[int64]string
}

// hookAnchorsFromComps 在钩位 extra 完整且唯一时构造映射；否则 ok=false。
func hookAnchorsFromComps(comps map[int64]rigComp, hooks []int64) (hookAnchorTable, bool) {
	t := hookAnchorTable{byExtra: map[int64]string{}}
	if len(hooks) < 2 {
		return t, false
	}
	sub := 0
	for _, key := range hooks {
		comp := comps[key]
		if comp.Extra <= 0 {
			return t, false
		}
		if _, dup := t.byExtra[comp.Extra]; dup {
			return t, false
		}
		t.byExtra[comp.Extra] = comp.GUID
		if comp.Extra >= hookSlotBaseIndex {
			sub++
		}
	}
	if sub == 0 {
		return t, false
	}
	return t, true
}

// anchorOf 返回该扩展槽位所属的钩位 GUID；主线槽位（extra < 1001）不在此表内。
func (t hookAnchorTable) anchorOf(extra int64) (string, bool) {
	if extra < hookSlotBaseIndex {
		return "", false
	}
	band := hookSlotBaseIndex + (extra-hookSlotBaseIndex)/hookSlotStride*hookSlotStride
	hook, ok := t.byExtra[band]
	return hook, ok
}

// mainHook 返回主线钩位（extra < 1001，实机是 250）的 GUID；没有则返回空。
func (t hookAnchorTable) mainHook() string {
	best, hook := int64(-1), ""
	for extra, guid := range t.byExtra {
		if extra >= hookSlotBaseIndex {
			continue
		}
		if best < 0 || extra < best {
			best, hook = extra, guid
		}
	}
	return hook
}

// baitHookAssignments 把已挂载的饵/拟饵分到钓组钩位上，返回 饵GUID → 钩GUID。
//
// 分配优先级：
//  1. 海钓钩位子槽：钩位 extra ≥ 1001 时按 extra 所属钩位归组（见 hookAnchorTable）；
//  2. bait_hook 记录：客户端上饵时写下的钩位。组件数组删除后会紧缩，
//     记录才是原始钩位的可靠来源；
//  3. 结构配对：记录缺失时，第 N 个饵位对应第 N 个钩位；
//  4. 拟饵自带的钩：上面都没有时用，且该钩必须属于本钓组的钩组件。
//
// 必须处理的坑：装配是逐组件进行的，钩组件通常比饵晚装上。此时
// hookPositionsIn 只看到模板声明的 1 个钩位（rig 50 模板只有 250），
// 旧逻辑会把每条饵都兜底绑到第一枚钩并持久化；等钩装到 4 个，4 条记录
// 仍全指向钩 0，多钩判鱼只看到 1 个有饵的钩位
// （logs/gameserver.log 2026-11-13 07:17 "钩位=4 已挂饵钩位=1"）。
// 因此"多钩钓组里记录全指向同一枚钩、而饵位序号跨越多个钩位"判为中间态，
// 整批作废改用结构配对，并把纠正结果回写。
func (s *Server) baitHookAssignments(ctx context.Context, rod string, comps map[int64]rigComp, single bool) (map[string]string, error) {
	assigned := map[string]string{}
	hooks := rigCompKeysOfCategory(comps, catHook)
	mounted, rigHook := map[string]bool{}, map[string]bool{}
	for _, key := range hooks {
		rigHook[strings.ToLower(comps[key].GUID)] = true
	}
	keysByCat := map[int64][]int64{}
	validHook := map[string]bool{}
	for h := range rigHook {
		validHook[h] = true
	}
	for _, cat := range []int64{catBait, catLure} {
		keysByCat[cat] = rigCompKeysOfCategory(comps, cat)
		for _, key := range keysByCat[cat] {
			bait := comps[key].GUID
			mounted[bait] = true
			if cat == catLure {
				if hook := s.lureActiveHookGUID(ctx, bait); hook != "" {
					validHook[strings.ToLower(hook)] = true
				}
			}
		}
	}

	explicit := map[string]string{}
	rows, err := s.DB.SQL.QueryContext(ctx, `SELECT key,value FROM rod_setting WHERE guid=? AND key LIKE 'bait_hook:%'`, rod)
	if err != nil {
		return nil, err
	}
	var obsolete []string
	for rows.Next() {
		var key, hook string
		if err := rows.Scan(&key, &hook); err != nil {
			rows.Close()
			return nil, err
		}
		bait := strings.TrimPrefix(key, "bait_hook:")
		if !mounted[bait] || !validHook[strings.ToLower(hook)] {
			obsolete = append(obsolete, key)
			continue
		}
		explicit[bait] = hook
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, key := range obsolete {
		if _, err := s.DB.SQL.ExecContext(ctx, `DELETE FROM rod_setting WHERE guid=? AND key=?`, rod, key); err != nil {
			return nil, err
		}
	}

	// 天然钩位：同一序号上的钩组件。
	natural := func(key int64) string {
		if row, ok := comps[rigCompKey(catHook, rigCompOrdinal(key))]; ok {
			return row.GUID
		}
		return ""
	}
	writeHook := func(bait, hook string) error {
		if strings.EqualFold(explicit[bait], hook) {
			return nil
		}
		if hook == "" {
			_, err := s.DB.SQL.ExecContext(ctx, `DELETE FROM rod_setting WHERE guid=? AND key=?`, rod, "bait_hook:"+bait)
			return err
		}
		return s.DB.UpsertRodSetting(ctx, rod, "bait_hook:"+bait, hook)
	}

	// 单钩钓组：显式记录优先（拟饵自带的钩也走这里），其余并到唯一钩位。
	if single && len(hooks) > 0 {
		first := comps[hooks[0]].GUID
		for _, cat := range []int64{catBait, catLure} {
			for _, key := range keysByCat[cat] {
				bait := comps[key].GUID
				hook := explicit[bait]
				if hook == "" {
					hook = first
				}
				assigned[bait] = hook
				if err := writeHook(bait, hook); err != nil {
					return nil, err
				}
			}
		}
		return assigned, nil
	}

	used := map[string]bool{}
	take := func(bait, hook string) bool {
		if hook == "" {
			return false
		}
		lh := strings.ToLower(hook)
		if used[lh] {
			return false
		}
		used[lh] = true
		assigned[bait] = hook
		return true
	}

	// 海钓多钩：钩位在客户端是独立的 1000+ 命名空间（1001/1005/1009，步长 4），
	// 钩位下的拟饵与珠子用同族子槽（1002/1006/1010 等）。ordinal 只是数组下标，
	// 增删组件后会紧缩，和真实钩位并不对齐：实机 2026-11-13 07:40 那次装配是
	// 钩 extra=250/1001/1005/1009、饵 extra=300/325/326/1002…1012，按 ordinal
	// 配对会退化成 1 组（"钩位=4 已挂饵钩位=1"）。这里改按 extra 所属钩位归组。
	if anchors, ok := hookAnchorsFromComps(comps, hooks); ok {
		mainHook := anchors.mainHook()
		for _, cat := range []int64{catBait, catLure} {
			for _, key := range keysByCat[cat] {
				comp := comps[key]
				hook, found := anchors.anchorOf(comp.Extra)
				if !found {
					// 子槽对应钩位还没装上：沿用已记录的位置，不凭空猜。
					// 但记录必须落在本钓组的钩组件上：实机 2026-11-13 07:40
					// 主线拟饵被记到它自带的三本钩（内嵌引用 04047060… 早已失效），
					// 结果主线钩 250 整枚空置，4 个钩位只剩 3 个真钩能用。
					// 没有记录的主线装饰（亮片/发光管）仍然不分配，不凭空占钩位。
					hook = explicit[comp.GUID]
					if hook != "" && !rigHook[strings.ToLower(hook)] {
						hook = mainHook
					}
				}
				if hook == "" {
					continue
				}
				assigned[comp.GUID] = hook
				used[strings.ToLower(hook)] = true
			}
		}
		for _, cat := range []int64{catBait, catLure} {
			for _, key := range keysByCat[cat] {
				if err := writeHook(comps[key].GUID, assigned[comps[key].GUID]); err != nil {
					return nil, err
				}
			}
		}
		return assigned, nil
	}

	// 中间态检测：装配是逐组件进行的，钩组件通常比饵晚装上。此时
	// hookPositionsIn 只看到模板声明的 1 个钩位（rig 50 模板只有 250），
	// 旧逻辑会把每条饵都兜底绑到第一枚钩并持久化；钩装到 4 个之后仍是
	// "钩位=4 已挂饵钩位=1"（logs/gameserver.log 2026-11-13 07:17）。
	// 判据：落在本钓组钩位上的记录只有一枚钩，而饵位序号跨越了多个钩位。
	repaired := false
	if len(hooks) >= 2 {
		explicitRig, naturalSpan := map[string]bool{}, map[string]bool{}
		for _, cat := range []int64{catBait, catLure} {
			for _, key := range keysByCat[cat] {
				if h := explicit[comps[key].GUID]; h != "" && rigHook[strings.ToLower(h)] {
					explicitRig[strings.ToLower(h)] = true
				}
				if h := natural(key); h != "" {
					naturalSpan[strings.ToLower(h)] = true
				}
			}
		}
		repaired = len(explicitRig) == 1 && len(naturalSpan) >= 2
	}

	if repaired {
		// 整批作废，按饵位序号重排：第 N 个子线对应第 N 个钩位。
		for _, cat := range []int64{catBait, catLure} {
			for _, key := range keysByCat[cat] {
				take(comps[key].GUID, natural(key))
			}
		}
	} else {
		// 记录优先：组件数组删除后会紧缩，记录才是原始钩位的可靠来源。
		for _, cat := range []int64{catBait, catLure} {
			for _, key := range keysByCat[cat] {
				bait := comps[key].GUID
				take(bait, explicit[bait])
			}
		}
		// 没有记录时沿用旧规则补算，稀疏旧链不按数组位置猜配对。
		for _, cat := range []int64{catBait, catLure} {
			keys := keysByCat[cat]
			for _, key := range keys {
				bait := comps[key].GUID
				if assigned[bait] != "" {
					continue
				}
				hook := ""
				if len(keys) == len(hooks) {
					var free []string
					for _, hk := range hooks {
						if !used[strings.ToLower(comps[hk].GUID)] {
							free = append(free, comps[hk].GUID)
						}
					}
					if len(free) == 1 {
						hook = free[0]
					} else if hk, exists := comps[rigCompKey(catHook, rigCompOrdinal(key))]; exists && !used[strings.ToLower(hk.GUID)] {
						hook = hk.GUID
					}
				} else if cat == catLure {
					hook = s.lureActiveHookGUID(ctx, bait)
				}
				take(bait, hook)
			}
		}
	}

	for _, cat := range []int64{catBait, catLure} {
		for _, key := range keysByCat[cat] {
			bait := comps[key].GUID
			if err := writeHook(bait, assigned[bait]); err != nil {
				return nil, err
			}
		}
	}
	return assigned, nil
}

// 新装配时已有明确钩位，记录实例关联；旧稀疏数组不据此补猜。
//
// 注意：这里不能拿"当前唯一的那枚钩"兜底。装配是逐组件进行的，客户端先挂
// 饵后挂钩，此时 hookPositionsIn 只看到模板声明的 1 个钩位，兜底会把每条饵
// 都写成同一个钩，等钩装到 4 个就变成"钩位=4 已挂饵钩位=1"。配不到序号就
// 不写，交给 baitHookAssignments 在读时按结构配对补算。
func (s *Server) rememberMountedBaitHook(ctx context.Context, rod, bait, previousHook string, category, ordinal int64) error {
	comps := s.storedRigComponents(ctx, rod)
	hook := ""
	hooks := rigCompKeysOfCategory(comps, catHook)
	for _, key := range hooks {
		if comps[key].GUID == previousHook {
			hook = previousHook
			break
		}
	}
	if hook == "" {
		if row, ok := comps[rigCompKey(catHook, ordinal)]; ok {
			hook = row.GUID
		}
	}
	if hook == "" {
		return nil
	}
	return s.DB.UpsertRodSetting(ctx, rod, "bait_hook:"+bait, hook)
}

func (s *Server) applyBaitedHookSpec(ctx context.Context, group baitedHookSet, spec *gearPhysicsSpec) bool {
	if spec == nil || group.hookGUID == "" || s == nil || s.DB == nil {
		return false
	}
	def, durability := s.baitedHookDef(ctx, group)
	if def == nil || def.Category != catHook || durability <= 0 {
		return false
	}
	spec.HookPresent = true
	spec.HookItemID = def.ItemID
	spec.HookSize = itemDefInt(def, gearAttrHookSize)
	spec.HookLengthMM = itemDefF32(def, gearAttrHookLen)
	spec.HookStrengthKg = itemDefF32(def, gearAttrMaxLoad) * clampFloat(durability, 0, 1)
	return true
}

func (s *Server) baitedHookDef(ctx context.Context, group baitedHookSet) (*store.ItemDef, float64) {
	if row, err := s.DB.GetItemByGUID(ctx, group.hookGUID); err == nil && row != nil {
		def, _ := s.itemDefForRow(ctx, row)
		return def, row.Durability
	}
	for _, bait := range group.baitGUIDs {
		row, err := s.DB.GetItemByGUID(ctx, bait)
		if err != nil || row == nil {
			continue
		}
		tree, err := s.ItemToTree(ctx, row)
		if err != nil || tree == nil || len(tree.Fields) == 0 || tree.Fields[0].Val == nil {
			continue
		}
		for _, hook := range tree.Fields[0].Val.Items {
			if hook == nil {
				continue
			}
			for _, field := range hook.Fields {
				if field.Op != "GUID16" || !strings.EqualFold(field.Hex, group.hookGUID) {
					continue
				}
				idx := fieldIdxByCS(hook.Class, csNumericID)
				if idx < 0 || idx >= len(hook.Fields) {
					continue
				}
				def, err := s.DB.GetItemDef(ctx, int64(hook.Fields[idx].U32()))
				if err != nil {
					return nil, 0
				}
				return def, treeDurability(hook)
			}
		}
	}
	return nil, 0
}

func (s *Server) canFishWithBait(ctx context.Context, uid int64, gearGUID string) bool {
	if s.DB == nil || gearGUID == "" {
		return true
	}
	rod, _ := s.DB.GetItemByGUID(ctx, gearGUID)
	return rod == nil || rod.UID == uid && len(s.currentBaits(ctx, gearGUID)) > 0
}

// baitUseRule 区分可食饵、重复使用的仿生饵及入水消耗的窝料。
func baitUseRule(def *store.ItemDef, cast bool) (bool, float64) {
	if def == nil {
		return false, 0
	}
	if cast {
		return def.Category == 30 || def.Category == 46, 0
	}
	var attrs struct {
		BaitType int64 `json:"fopbhlmkcpe"`
		LureType int64 `json:"mbemakijdeh"`
	}
	if json.Unmarshal([]byte(def.AttrsJSON), &attrs) != nil {
		return false, 0
	}
	if def.Category == 27 {
		if attrs.BaitType == 17 {
			return false, 0.005
		}
		return true, 0
	}
	if def.Category == 17 {
		switch attrs.LureType {
		case 3, 8, 9, 13, 14, 16:
			return false, 0.02
		}
	}
	return false, 0
}

func (s *Server) biteBaitSnapshot(ctx context.Context, rod string) (string, bool) {
	if s.DB == nil {
		return "", false
	}
	row, err := s.DB.GetItemByGUID(ctx, rod)
	if err != nil || row == nil {
		return "", false
	}
	comps := s.storedRigComponents(ctx, rod)
	var ids []string
	for _, category := range []int64{27, 17} {
		for _, key := range rigCompKeysOfCategory(comps, category) {
			ids = append(ids, comps[key].GUID)
		}
	}
	return strings.Join(ids, " "), true
}

// consumeBaitEvent 与库存更新共用一个事务，重复咬口、重推及重启不会再扣。
func (s *Server) consumeBaitEvent(ctx context.Context, uid int64, rod, event string, ids []string, cast bool) (baitChange, error) {
	var out baitChange
	if s.DB == nil || rod == "" || event == "" {
		return out, nil
	}
	kind := "bait_bite"
	if cast {
		kind = "bait_cast"
	}
	tx, err := beginEconomyTx(ctx, s.DB)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var previous string
	lookup := tx.QueryRowContext(ctx, `SELECT COALESCE(note,'') FROM txn WHERE uid=? AND kind=? AND guid=?`, uid, kind, event).Scan(&previous)
	if lookup == nil {
		_ = json.Unmarshal([]byte(previous), &out)
		return out, nil
	}
	if lookup != sql.ErrNoRows {
		return out, lookup
	}
	parent, err := getOwnedItemTx(ctx, tx, uid, rod)
	if err != nil {
		return out, err
	}
	if err := addTxnTx(ctx, tx, uid, kind, "", event, 0, 0, "饵料使用"); err != nil {
		return out, err
	}
	fields, err := FieldsFromJSON(parent.FieldsJSON)
	if err != nil {
		return out, err
	}
	originalSlots := rigCompsOf(protoFieldAt(fields, parent.WireClass, csRigObject))
	seen := map[string]bool{}
	for _, guid := range ids {
		if seen[guid] {
			continue
		}
		seen[guid] = true
		row, err := getOwnedItemTx(ctx, tx, uid, guid)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return out, err
		}
		// 换饵后的背包物品和其他钓组不能被旧咬口扣除。
		if row.RigGUID == "" || row.Container != "rig" || !cast && row.RigGUID != rod {
			continue
		}
		if cast {
			owner := row.RigGUID
			visited := map[string]bool{}
			for owner != rod && owner != "" && !visited[owner] {
				visited[owner] = true
				p, e := getOwnedItemTx(ctx, tx, uid, owner)
				if e != nil {
					return out, e
				}
				owner = p.RigGUID
			}
			if owner != rod {
				continue
			}
		}
		def, err := itemDefByNumericIDTx(ctx, tx, row.NumericID)
		if err != nil {
			return out, err
		}
		consumed, wear := baitUseRule(def, cast)
		if consumed && baitQuantity(row) <= 0 {
			return out, fmt.Errorf("挂载饵料已用尽")
		}
		if !consumed {
			if wear == 0 {
				continue
			}
			fields, err := FieldsFromJSON(row.FieldsJSON)
			if err != nil {
				return out, err
			}
			row.Durability = math.Max(0, row.Durability-wear)
			if idx := fieldIdxByCS(row.WireClass, csDurability); idx >= 0 {
				if !setWireFloat(fields, row.WireClass, idx, row.Durability) {
					return out, fmt.Errorf("饵料耐久字段无效")
				}
			}
			if err := updateItemFieldsTx(ctx, s.DB, tx, row, fields); err != nil {
				return out, err
			}
			out.Updated = append(out.Updated, guid)
			continue
		}
		// 只消耗已挂载的一份，下一次抛竿前再补饵。
		if _, err := consumeItemQtyTx(ctx, tx, row, 1); err != nil {
			return out, err
		}
		if cast && (def.Category == 46 || def.Category == 30) {
			// 挂在竿上的窝子（食笼/PVA 棒）入水即投放：这份料要进落点格子的窝料账本，
			// 否则attraction 系统看不见它。
			out.Groundbait = append(out.Groundbait, groundbaitUse{ItemID: def.ItemID, Units: 1,
				Category: def.Category, GUID: row.GUID})
		}
		if row.Qty > 0 {
			out.Updated = append(out.Updated, guid)
			continue
		}
		if !cast && def.Category == 27 {
			if key, _, ok := rigCompKeyForGUID(originalSlots, guid); ok {
				ordinal, value := rigCompOrdinal(key), def.ItemID
				var hook string
				if err := tx.QueryRowContext(ctx, `SELECT value FROM rod_setting WHERE guid=? AND key=?`, rod, "bait_hook:"+guid).Scan(&hook); err != nil && err != sql.ErrNoRows {
					return out, err
				}
				if hook != "" {
					if len(rigCompKeysOfCategory(originalSlots, catHook)) > 1 {
						for _, hk := range rigCompKeysOfCategory(originalSlots, catHook) {
							if originalSlots[hk].GUID == hook {
								ordinal = rigCompOrdinal(hk)
								break
							}
						}
					}
					encoded, _ := json.Marshal(rebaitRequest{ItemID: def.ItemID, HookGUID: hook})
					value = string(encoded)
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO rod_setting(guid,key,value,updated_ts) VALUES(?,?,?,?) ON CONFLICT(guid,key) DO UPDATE SET value=excluded.value,updated_ts=excluded.updated_ts`, rod, fmt.Sprintf("rebait:%d", ordinal), value, float64(time.Now().Unix())); err != nil {
					return out, err
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM rod_setting WHERE guid=? AND key=?`, rod, "bait_hook:"+guid); err != nil {
					return out, err
				}
			}
		}
		out.Removed = append(out.Removed, guid)
		owner := parent
		if row.RigGUID != rod {
			owner, err = getOwnedItemTx(ctx, tx, uid, row.RigGUID)
			if err != nil {
				return out, err
			}
		}
		if err := removeConsumedRigReferenceTx(ctx, s.DB, tx, owner, guid); err != nil {
			return out, err
		}
		out.Updated = append(out.Updated, owner.GUID)
	}
	receipt, err := json.Marshal(out)
	if err != nil {
		return baitChange{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE txn SET note=? WHERE uid=? AND kind=? AND guid=?`, string(receipt), uid, kind, event); err != nil {
		return baitChange{}, err
	}
	if err := tx.Commit(); err != nil {
		return baitChange{}, err
	}
	return out, nil
}

func removeConsumedRigReferenceTx(ctx context.Context, db *store.DB, tx *sql.Tx, parent *store.Item, guid string) error {
	fields, err := FieldsFromJSON(parent.FieldsJSON)
	if err != nil {
		return err
	}
	idx := fieldIdxByCS(parent.WireClass, csRigObject)
	if idx < 0 || idx >= len(fields) || fields[idx].Val == nil {
		return nil
	}
	f5 := fields[idx].Val
	comps := rigCompsOf(f5)
	key, _, exists := rigCompKeyForGUID(comps, guid)
	if !exists {
		return nil
	}
	delete(comps, key)
	name, rt, _ := rigIdentity(f5)
	fields[idx].Val = buildRig153Like(f5, name, rt, comps, rigDepthOf(f5, 100), rigClipOf(f5))
	return updateItemFieldsTx(ctx, db, tx, parent, fields)
}

func (s *Session) pushBaitChange(change baitChange) {
	if len(change.Removed) > 0 {
		s.Push4_21(change.Removed)
	}
	s.Push4_22ItemGUIDs(change.Updated)
}

func (s *Server) pushBaitedFish(sess *Session, f *RolledFish, rod string, rigType int64) bool {
	hook := f.HookGUID
	if hook == "" {
		hook = s.mountedHookGuid(s.dbCtx(), rod)
	}
	// 14/14 只是把鱼放进客户端水下行为系统，此时鱼还没有真正咬钩。
	// 若在这里删除钩饵并推 4/21、4/22，客户端会立即看到空钩，随后让鱼以
	// 14/12 f0=1 离场。饵必须保留到客户端用 14/11 确认中鱼后再扣除。
	return sess.Push14_14(f, hook, rod, rigType)
}

// consumeHookedBait 在客户端确认中鱼后扣除本次咬口使用的饵。
// FishGUID 作为事务幂等键，重复 14/11 不会重复扣除。
func (s *Server) consumeHookedBait(sess *Session, f *RolledFish, rod string) error {
	if sess == nil || f == nil || !f.BaitsKnown {
		return nil
	}
	change, err := s.consumeBaitEvent(sess.pushCtx(), sess.Userid, rod, f.FishGUID, strings.Fields(f.BaitGUIDs), false)
	if err != nil {
		return err
	}
	sess.pushBaitChange(change)
	return nil
}

// consumeNibbleBait 处理“鱼碰到/嘬到饵但没中钩”的轻量消耗。
// 事件键加 :nibble，与同一条鱼的中钩扣饵分开，避免重放重复扣。
func (s *Server) consumeNibbleBait(sess *Session, f *RolledFish, rod string) error {
	if sess == nil || f == nil || !f.BaitsKnown {
		return nil
	}
	change, err := s.consumeBaitEvent(sess.pushCtx(), sess.Userid, rod,
		f.FishGUID+":nibble", strings.Fields(f.BaitGUIDs), false)
	if err != nil {
		return err
	}
	sess.pushBaitChange(change)
	return nil
}

func (s *Server) consumeCastBait(sess *Session, st *FishState) error {
	st.mu.Lock()
	rod, cast := st.GearGuid, st.CastTs
	st.mu.Unlock()
	if s.DB == nil || rod == "" || cast.IsZero() {
		return nil
	}
	seen := map[string]bool{rod: true}
	queue := []string{rod}
	var ids []string
	for len(queue) > 0 && len(seen) <= 64 {
		parent := queue[0]
		queue = queue[1:]
		rows, err := s.DB.ListRigComponents(sess.pushCtx(), parent)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if seen[row.GUID] {
				continue
			}
			seen[row.GUID] = true
			ids = append(ids, row.GUID)
			queue = append(queue, row.GUID)
		}
	}
	change, err := s.consumeBaitEvent(sess.pushCtx(), sess.Userid, rod, fmt.Sprintf("%s:%d", rod, cast.UnixNano()), ids, true)
	if err == nil {
		sess.pushBaitChange(change)
		s.depositCastGroundbait(sess, st, change, fmt.Sprintf("%s:%d", rod, cast.UnixNano()))
	}
	return err
}

// reloadBaitForCast 只在下一次抛竿前补同种钩饵，不提前消耗搏鱼中的备用饵。
func (s *Server) reloadBaitForCast(ctx context.Context, uid int64, rod string) (baitChange, error) {
	var out baitChange
	parent, err := s.DB.GetItemByGUID(ctx, rod)
	if err != nil {
		return out, err
	}
	if parent == nil {
		return out, nil
	}
	if parent.UID != uid {
		return out, fmt.Errorf("不能为其他玩家的钓组补饵")
	}
	rigType := s.rigTypeForWrite(ctx, rod)
	comps := s.storedRigComponents(ctx, rod)
	multi := hookPositionsIn(rigType, comps) > 1
	for ordinal := int64(0); ordinal < int64(baitSlotCountIn(rigType, comps)); ordinal++ {
		key := fmt.Sprintf("rebait:%d", ordinal)
		id, err := s.DB.GetRodSetting(ctx, rod, key)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return out, err
		}
		if id == "" {
			continue
		}
		request := rebaitRequest{ItemID: id}
		if strings.HasPrefix(id, "{") {
			if err := json.Unmarshal([]byte(id), &request); err != nil || request.ItemID == "" {
				return out, fmt.Errorf("自动补饵记录无效")
			}
			id = request.ItemID
		}
		comps := s.storedRigComponents(ctx, rod)
		occupied := false
		mountOrdinal := ordinal
		// 钩已随断线、断竿或拆卸离开钓组时，这条补饵请求作废：不能再把饵挂到
		// 没有钩的钓组上。旧格式记录没有 hook 字段：单钩钓组可推断唯一钩；
		// 多钩钓组无法确定挂哪只钩，按无法安全补饵处理。
		if request.HookGUID == "" {
			hooks := rigCompKeysOfCategory(comps, catHook)
			if multi || len(hooks) != 1 {
				if _, err := s.DB.SQL.ExecContext(ctx, `DELETE FROM rod_setting WHERE guid=? AND key=?`, rod, key); err != nil {
					return out, err
				}
				continue
			}
			request.HookGUID = comps[hooks[0]].GUID
		}
		if _, _, present := rigCompKeyForGUID(comps, request.HookGUID); !present {
			if _, err := s.DB.SQL.ExecContext(ctx, `DELETE FROM rod_setting WHERE guid=? AND key=?`, rod, key); err != nil {
				return out, err
			}
			continue
		}
		if multi && request.HookGUID != "" {
			assignments, err := s.baitHookAssignments(ctx, rod, comps, false)
			if err != nil {
				return out, err
			}
			for _, bk := range rigCompKeysOfCategory(comps, catBait) {
				if assignments[comps[bk].GUID] == request.HookGUID {
					occupied = true
					break
				}
			}
			mountOrdinal = int64(len(rigCompKeysOfCategory(comps, catBait)))
		} else {
			_, occupied = comps[rigCompKey(catBait, ordinal)]
		}
		if occupied {
			if _, err := s.DB.SQL.ExecContext(ctx, `DELETE FROM rod_setting WHERE guid=? AND key=?`, rod, key); err != nil {
				return out, err
			}
			continue
		}
		rows, err := s.DB.SQL.QueryContext(ctx, `SELECT guid FROM item WHERE uid=? AND item_id=? AND container='backpack' AND qty>0 ORDER BY created_ts,guid`, uid, id)
		if err != nil {
			return out, err
		}
		var candidates []string
		for rows.Next() {
			var guid string
			if err := rows.Scan(&guid); err != nil {
				rows.Close()
				return out, err
			}
			candidates = append(candidates, guid)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
		for _, guid := range candidates {
			row, err := s.DB.GetItemByGUID(ctx, guid)
			if err != nil {
				return out, err
			}
			if baitQuantity(row) <= 0 {
				continue
			}
			result := s.rigSetComponent(ctx, uid, rod, 27, guid, 350+mountOrdinal, rigSetOpts{ParentType: familyRod, EnforceCategory: true, SplitStack: true, KeepRebait: true})
			if result.Rejected || result.MountedGUID == "" {
				return out, fmt.Errorf("自动补饵失败")
			}
			if request.HookGUID != "" {
				if err := s.DB.UpsertRodSetting(ctx, rod, "bait_hook:"+result.MountedGUID, request.HookGUID); err != nil {
					return out, err
				}
				if multi {
					if err := s.orderBaitsByHook(ctx, uid, rod); err != nil {
						return out, err
					}
				}
			}
			if _, err := s.DB.SQL.ExecContext(ctx, `DELETE FROM rod_setting WHERE guid=? AND key=?`, rod, key); err != nil {
				return out, err
			}
			out.Updated = append(out.Updated, rod, guid, result.MountedGUID)
			// 客户端只按「新增/移除」更新背包计数，直接推同 guid 的数量变化会被忽略，
			// 表现为钩上补了饵但背包数量不变。先移除来源栈再随更新段重新加入，
			// 强制客户端丢弃本地旧栈并采用新数量（与船票租用状态的刷新方式一致）。
			if !containsGUID(out.Removed, guid) {
				out.Removed = append(out.Removed, guid)
			}
			out.Removed = append(out.Removed, result.RemovedGUIDs...)
			break
		}
	}
	return out, nil
}

func (s *Server) orderBaitsByHook(ctx context.Context, uid int64, rod string) error {
	comps := s.storedRigComponents(ctx, rod)
	assignments, err := s.baitHookAssignments(ctx, rod, comps, false)
	if err != nil {
		return err
	}
	hookOrder := map[string]int64{}
	for _, key := range rigCompKeysOfCategory(comps, catHook) {
		hookOrder[comps[key].GUID] = rigCompOrdinal(key)
	}
	keys := rigCompKeysOfCategory(comps, catBait)
	for _, key := range keys {
		if _, ok := hookOrder[assignments[comps[key].GUID]]; !ok {
			return nil
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		return hookOrder[assignments[comps[keys[i]].GUID]] < hookOrder[assignments[comps[keys[j]].GUID]]
	})
	var baits []rigComp
	for _, key := range keys {
		baits = append(baits, comps[key])
		delete(comps, key)
	}
	for i, bait := range baits {
		bait.Ordinal = int64(i)
		comps[rigCompKey(catBait, int64(i))] = bait
	}
	tx, err := beginEconomyTx(ctx, s.DB)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	parent, err := getOwnedItemTx(ctx, tx, uid, rod)
	if err != nil {
		return err
	}
	fields, err := FieldsFromJSON(parent.FieldsJSON)
	if err != nil {
		return err
	}
	idx := fieldIdxByCS(parent.WireClass, csRigObject)
	if idx < 0 || idx >= len(fields) || fields[idx].Val == nil {
		return fmt.Errorf("钓组字段无效")
	}
	f5 := fields[idx].Val
	name, rt, _ := rigIdentity(f5)
	fields[idx].Val = buildRig153Like(f5, name, rt, comps, rigDepthOf(f5, 100), rigClipOf(f5))
	if err := updateItemFieldsTx(ctx, s.DB, tx, parent, fields); err != nil {
		return err
	}
	components := make([]store.RigComponent, 0, len(comps))
	for _, comp := range comps {
		components = append(components, store.RigComponent{
			Slot: comp.Category, Ordinal: comp.Ordinal, GUID: comp.GUID, Extra: comp.Extra,
		})
	}
	if err := s.DB.ReplaceRigComponentsTx(ctx, tx, rod, components); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Session) prepareBaitForCast(st *FishState, rod string) error {
	st.mu.Lock()
	busy := st.CastActive && st.Pending != nil && !st.Resolved
	st.mu.Unlock()
	if busy || s.Srv.DB == nil || rod == "" {
		return nil
	}
	change, err := s.Srv.reloadBaitForCast(s.pushCtx(), s.Userid, rod)
	if err == nil {
		s.pushBaitChange(change)
	}
	return err
}
