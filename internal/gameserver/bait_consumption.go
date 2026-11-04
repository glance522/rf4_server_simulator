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

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

type baitChange struct {
	Updated []string `json:"updated,omitempty"`
	Removed []string `json:"removed,omitempty"`
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
	count := max(len(slotIndicesFor(rigType, catHook)), len(slotIndicesFor(rigType, catLure)))
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
			group.hookGUID = assignments[row.GUID]
			if group.hookGUID == "" && category == catLure {
				group.hookGUID = s.lureActiveHookGUID(ctx, row.GUID)
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

// 组件数组删除后会紧缩，用实例关联保留原钩位；稀疏旧链不按数组猜配对。
func (s *Server) baitHookAssignments(ctx context.Context, rod string, comps map[int64]rigComp, single bool) (map[string]string, error) {
	assigned := map[string]string{}
	hooks := rigCompKeysOfCategory(comps, catHook)
	mounted, validHook := map[string]bool{}, map[string]bool{}
	for _, key := range hooks {
		validHook[comps[key].GUID] = true
	}
	for _, cat := range []int64{catBait, catLure} {
		for _, key := range rigCompKeysOfCategory(comps, cat) {
			mounted[comps[key].GUID] = true
			if cat == catLure {
				if hook := s.lureActiveHookGUID(ctx, comps[key].GUID); hook != "" {
					validHook[hook] = true
				}
			}
		}
	}
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
		if !mounted[bait] || !validHook[hook] {
			obsolete = append(obsolete, key)
		} else {
			assigned[bait] = hook
		}
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
	for _, cat := range []int64{catBait, catLure} {
		keys := rigCompKeysOfCategory(comps, cat)
		used := map[string]bool{}
		for _, key := range keys {
			if hook := assigned[comps[key].GUID]; hook != "" {
				used[hook] = true
			}
		}
		for _, key := range keys {
			bait := comps[key].GUID
			if assigned[bait] != "" {
				continue
			}
			hook := ""
			if single && len(hooks) > 0 {
				hook = comps[hooks[0]].GUID
			} else if len(keys) == len(hooks) {
				var free []string
				for _, hk := range hooks {
					if !used[comps[hk].GUID] {
						free = append(free, comps[hk].GUID)
					}
				}
				if len(free) == 1 {
					hook = free[0]
				} else if hk, exists := comps[rigCompKey(catHook, rigCompOrdinal(key))]; exists && !used[hk.GUID] {
					hook = hk.GUID
				}
			} else if cat == catLure {
				hook = s.lureActiveHookGUID(ctx, bait)
			}
			if hook == "" {
				continue
			}
			if err := s.DB.UpsertRodSetting(ctx, rod, "bait_hook:"+bait, hook); err != nil {
				return nil, err
			}
			assigned[bait], used[hook] = hook, true
		}
	}
	return assigned, nil
}

// 新装配时已有明确钩位，记录实例关联；旧稀疏数组不据此补猜。
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
		rigType := s.rigTypeForWrite(ctx, rod)
		if max(len(slotIndicesFor(rigType, catHook)), len(slotIndicesFor(rigType, catLure))) <= 1 && len(hooks) > 0 {
			hook = comps[hooks[0]].GUID
		} else if row, ok := comps[rigCompKey(catHook, ordinal)]; ok {
			hook = row.GUID
		}
	}
	if hook == "" && category == catLure {
		hook = s.lureActiveHookGUID(ctx, bait)
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
				durability := 1.0
				if idx := fieldIdxByCS(hook.Class, csDurability); idx >= 0 && idx < len(hook.Fields) && hook.Fields[idx].Num != nil {
					durability = float64(proto.F32FromBits(uint32(u64Of(hook.Fields[idx].Num))))
				}
				return def, durability
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
	var trees []*proto.Value
	seen := map[string]bool{}
	for _, guid := range change.Updated {
		if seen[guid] {
			continue
		}
		seen[guid] = true
		if row, err := s.Srv.DB.GetItemByGUID(s.pushCtx(), guid); err == nil && row != nil {
			if tree, err := s.Srv.itemTree(s.pushCtx(), row); err == nil {
				trees = append(trees, tree)
			}
		}
	}
	if len(trees) > 0 {
		s.Push(4, 22, proto.Args(proto.S("61"), trees...))
	}
}

func (s *Server) pushBaitedFish(sess *Session, f *RolledFish, rod string) bool {
	hook := f.HookGUID
	if hook == "" {
		hook = s.mountedHookGuid(s.dbCtx(), rod)
	}
	// 14/14 只是把鱼放进客户端水下行为系统，此时鱼还没有真正咬钩。
	// 若在这里删除钩饵并推 4/21、4/22，客户端会立即看到空钩，随后让鱼以
	// 14/12 f0=1 离场。饵必须保留到客户端用 14/11 确认中鱼后再扣除。
	return sess.Push14_14(f, hook, rod)
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
	multi := max(len(slotIndicesFor(rigType, catHook)), len(slotIndicesFor(rigType, catLure))) > 1
	for ordinal := int64(0); ordinal < int64(baitSlotCount(rigType)); ordinal++ {
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
		if multi && request.HookGUID != "" {
			if _, _, present := rigCompKeyForGUID(comps, request.HookGUID); !present {
				if _, err := s.DB.SQL.ExecContext(ctx, `DELETE FROM rod_setting WHERE guid=? AND key=?`, rod, key); err != nil {
					return out, err
				}
				continue
			}
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
