package gameserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math/rand"
	"strings"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

const (
	catLeaderLock = int64(14)
	catWireLeader = int64(15)
)

// gearDamageEvent 是 item_damage_log.event 的稳定取值。
const (
	damageEventWear            = "wear"
	damageEventLineBreak       = "line_break"
	damageEventLineExhausted   = "line_exhausted"
	damageEventLostOnLineBreak = "lost_on_line_break"
	damageEventRodBreak        = "rod_break"
	damageEventHookBreak       = "hook_break"
	damageEventRepair          = "repair"
)

type gearBreakOutcome struct {
	BrokenCategory int64
	LineBroken     bool
	BrokenGUID     string
	RemovedGUIDs   []string
	UpdatedTrees   []*proto.Value
}

// lineBreakTerminalCategories 是断线时随线一起丢失的终端组件。
// 路亚也在其中：线断后拟饵、钩、前导和饵都不可能留在竿上。
var lineBreakTerminalCategories = map[int64]bool{
	catHook: true, catLead: true, catLeaderLock: true, catWireLeader: true,
	catLure: true, catBait: true, catFloat: true,
}

// sourceLineCategories 包含主线及前导/钢丝前导。断任何一项都按断线处理。
var sourceLineCategories = map[int64]bool{
	catLine: true, catLead: true, catLeaderLock: true, catWireLeader: true,
}

func ptrFloat64(v float64) *float64 { return &v }
func ptrInt64(v int64) *int64       { return &v }

// itemLengthField 读取鱼线实例的剩余长度（字段 ljnnpokapig，单位米）。
func itemLengthField(row *store.Item, fields []proto.Field) int64 {
	if row == nil {
		return 0
	}
	if idx := fieldIdxByCS(row.WireClass, csQty); idx >= 0 && idx < len(fields) {
		return int64(u64Of(fields[idx].Num))
	}
	if row.Qty > 0 {
		return row.Qty
	}
	return 0
}

func (s *Server) itemLength(ctx context.Context, row *store.Item) int64 {
	if row == nil {
		return 0
	}
	if fields, err := FieldsFromJSON(row.FieldsJSON); err == nil {
		if idx := fieldIdxByCS(row.WireClass, csQty); idx >= 0 && idx < len(fields) {
			return int64(u64Of(fields[idx].Num))
		}
	}
	if def, err := s.itemDefForRow(ctx, row); err == nil && def != nil {
		return lineLengthM(def)
	}
	return 0
}

func setItemLengthField(row *store.Item, fields []proto.Field, length int64) bool {
	if row == nil || length < 0 {
		return false
	}
	idx := fieldIdxByCS(row.WireClass, csQty)
	if idx < 0 || idx >= len(fields) {
		return false
	}
	fields[idx].Num = ptrU64(uint64(length))
	return true
}

// lineBreakLoss 返回本次断线损失的线长。
// 客户端 17/9 只上报张力/冲量，没有断线长度字段；requested<=0 时按剩余长度的
// 1..80% 随机扣减，保证不会把剩余长度扣成负数。
func lineBreakLoss(remaining, requested int64) int64 {
	if remaining <= 0 {
		return 0
	}
	if requested > 0 {
		if requested > remaining {
			return remaining
		}
		return requested
	}
	maxLoss := remaining * 80 / 100
	if maxLoss < 1 {
		maxLoss = 1
	}
	return 1 + rand.Int63n(maxLoss)
}

func (s *Server) itemBrokenForMount(ctx context.Context, row *store.Item) bool {
	if row == nil || row.Durability <= 0 {
		return true
	}
	def, err := s.itemDefForRow(ctx, row)
	if err != nil || def == nil || def.Category != catLine {
		return false
	}
	return s.itemLength(ctx, row) <= 0
}

// breakLineOrRod 只损坏当前竿本体或实际装配的线材、鱼钩。
func (s *Server) breakLineOrRod(ctx context.Context, uid int64, rodGUID, brokenGUID string) (*gearBreakOutcome, error) {
	rodGUID = strings.TrimSpace(rodGUID)
	brokenGUID = strings.TrimSpace(brokenGUID)
	if rodGUID == "" || rodGUID == zeroGUID {
		return nil, sql.ErrNoRows
	}
	rod, err := s.DB.GetItemByGUID(ctx, rodGUID)
	if err != nil || rod == nil || rod.UID != uid {
		return nil, sql.ErrNoRows
	}
	rodDef, err := s.itemDefForRow(ctx, rod)
	if err != nil || rodDef == nil || rodDef.Category != gearCatRod {
		return nil, sql.ErrNoRows
	}
	if brokenGUID == "" || brokenGUID == zeroGUID {
		if g, gerr := s.storedRigItemGUID(ctx, rod, csLine); gerr == nil && g != "" {
			brokenGUID = g
		} else {
			brokenGUID = rodGUID
		}
	}
	if brokenGUID == rodGUID {
		return s.breakRodTx(ctx, uid, rodGUID, brokenGUID)
	}
	brokenRow, err := s.DB.GetItemByGUID(ctx, brokenGUID)
	if err != nil || brokenRow == nil || brokenRow.UID != uid {
		return nil, sql.ErrNoRows
	}
	brokenDef, err := s.itemDefForRow(ctx, brokenRow)
	if err != nil || brokenDef == nil || (!sourceLineCategories[brokenDef.Category] && brokenDef.Category != catHook) {
		return nil, errors.New("断件类型尚不支持，未扣除耐久")
	}
	mounted := false
	if fields, ferr := FieldsFromJSON(rod.FieldsJSON); ferr == nil {
		if idx := fieldIdxByCS(rod.WireClass, csLine); idx >= 0 && idx < len(fields) {
			mounted = fields[idx].GuidHex() == brokenGUID
		}
		for _, comp := range rigCompsOf(protoFieldAt(fields, rod.WireClass, csRigObject)) {
			if comp.GUID == brokenGUID && (sourceLineCategories[comp.Category] || comp.Category == catHook) {
				mounted = true
			}
		}
	}
	if !mounted {
		return nil, errors.New("断件不在当前钓组中，未扣除耐久")
	}
	if brokenDef.Category == catHook {
		return s.breakHookTx(ctx, uid, rodGUID, brokenGUID)
	}
	return s.breakLineTx(ctx, uid, rodGUID, brokenGUID)
}

func (s *Server) storedRigItemGUID(ctx context.Context, rod *store.Item, csName string) (string, error) {
	if rod == nil {
		return "", nil
	}
	fields, err := FieldsFromJSON(rod.FieldsJSON)
	if err != nil {
		return "", err
	}
	idx := fieldIdxByCS(rod.WireClass, csName)
	if idx < 0 || idx >= len(fields) {
		return "", nil
	}
	return fields[idx].GuidHex(), nil
}

func updateItemFieldsTx(ctx context.Context, db *store.DB, tx *sql.Tx, row *store.Item, fields []proto.Field) error {
	bumpCreatedTicks(fields, row.WireClass)
	fj, err := FieldsToJSON(fields)
	if err != nil {
		return err
	}
	if err := db.UpdateItemFieldsTx(ctx, tx, row.GUID, fj, row.Qty, row.Durability, row.Workmanship, row.RigGUID); err != nil {
		return err
	}
	row.FieldsJSON = fj
	return nil
}

func (s *Server) breakLineTx(ctx context.Context, uid int64, rodGUID, brokenGUID string) (*gearBreakOutcome, error) {
	tx, err := s.DB.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rod, err := s.DB.GetItemByGUIDTx(ctx, tx, rodGUID)
	if err != nil || rod == nil || rod.UID != uid {
		return nil, sql.ErrNoRows
	}
	rodFields, err := FieldsFromJSON(rod.FieldsJSON)
	if err != nil {
		return nil, err
	}

	lineGUID, _ := s.storedRigItemGUID(ctx, rod, csLine)
	if lineGUID == "" {
		if row, rerr := s.DB.GetItemByGUID(ctx, brokenGUID); rerr == nil && row != nil {
			if def, derr := s.itemDefForRow(ctx, row); derr == nil && def != nil && def.Category == catLine {
				lineGUID = brokenGUID
			}
		}
	}
	var lineRow *store.Item
	var lineFields []proto.Field
	beforeLength, afterLength, lossLength := int64(0), int64(0), int64(0)
	if lineGUID != "" {
		lineRow, _ = s.DB.GetItemByGUIDTx(ctx, tx, lineGUID)
		if lineRow != nil {
			lineFields, err = FieldsFromJSON(lineRow.FieldsJSON)
			if err != nil {
				return nil, err
			}
			beforeLength = itemLengthField(lineRow, lineFields)
			if beforeLength <= 0 {
				if def, derr := s.itemDefForRow(ctx, lineRow); derr == nil && def != nil {
					beforeLength = lineLengthM(def)
				}
			}
			lossLength = lineBreakLoss(beforeLength, 0)
			afterLength = beforeLength - lossLength
			if afterLength < 0 {
				afterLength = 0
			}
			if afterLength > 0 {
				if !setItemLengthField(lineRow, lineFields, afterLength) {
					return nil, errLineLengthField
				}
				if err := updateItemFieldsTx(ctx, s.DB, tx, lineRow, lineFields); err != nil {
					return nil, err
				}
			}
		}
	}

	var removed []string
	terminalRows := map[string]*store.Item{}
	var terminalGUIDs []string
	if rows, qerr := tx.QueryContext(ctx, `SELECT slot, COALESCE(guid,'') FROM rig_component WHERE rig_guid=?`, rodGUID); qerr == nil {
		for rows.Next() {
			var slot int64
			var guid string
			if err := rows.Scan(&slot, &guid); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if guid != "" && lineBreakTerminalCategories[slot] {
				terminalGUIDs = append(terminalGUIDs, guid)
			}
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
	}
	for _, guid := range terminalGUIDs {
		if row, ierr := s.DB.GetItemByGUIDTx(ctx, tx, guid); ierr == nil && row != nil {
			terminalRows[guid] = row
		}
	}
	// f5 中可能还有尚未投影到 rig_component 的组件，按类别一并清理。
	stored := protoFieldAt(rodFields, rod.WireClass, csRigObject)
	for _, comp := range rigCompsOf(stored) {
		if comp.GUID == "" || !lineBreakTerminalCategories[comp.Category] {
			continue
		}
		if _, ok := terminalRows[comp.GUID]; !ok {
			if row, ierr := s.DB.GetItemByGUIDTx(ctx, tx, comp.GUID); ierr == nil && row != nil {
				terminalRows[comp.GUID] = row
			}
		}
	}

	for guid, row := range terminalRows {
		if err := s.DB.DeleteItemCascadeTx(ctx, tx, guid); err != nil {
			return nil, err
		}
		removed = append(removed, guid)
		entry := store.ItemDamageLog{
			UID: uid, ItemGUID: guid, ItemID: row.ItemID, Event: damageEventLostOnLineBreak,
			BeforeDurability: ptrFloat64(row.Durability), BrokenGUID: brokenGUID,
			DetailJSON: damageDetailJSON(map[string]any{"rod_guid": rodGUID, "reason": "line_break"}),
		}
		if err := store.AddItemDamageLog(ctx, tx, entry); err != nil {
			return nil, err
		}
	}

	if lineRow != nil && afterLength <= 0 {
		if err := s.DB.DeleteItemCascadeTx(ctx, tx, lineRow.GUID); err != nil {
			return nil, err
		}
		removed = append(removed, lineRow.GUID)
		if idx := fieldIdxByCS(rod.WireClass, csLine); idx >= 0 && idx < len(rodFields) {
			rodFields[idx].Op = "OPTGUID"
			rodFields[idx].Hex = ""
		}
	}

	comps := rigCompsOf(stored)
	for cat := range lineBreakTerminalCategories {
		deleteRigCategory(comps, cat)
	}
	if lineRow != nil && afterLength <= 0 {
		deleteRigCategory(comps, catLine)
	}
	name, rigType, typeOK := rigIdentity(stored)
	if !typeOK {
		rigType = s.defaultRigTypeForRod(ctx, rod.ItemID)
		name = rigNameForType(rigType)
	}
	depth := rigDepthOf(stored, defaultDepthFor(name, rigType))
	if idx := fieldIdxByCS(rod.WireClass, csRigObject); idx >= 0 && idx < len(rodFields) {
		rodFields[idx].Op = "VAL"
		rodFields[idx].Val = buildRig153WithClip(name, rigType, comps, depth, rigClipOf(stored))
	}
	if err := updateItemFieldsTx(ctx, s.DB, tx, rod, rodFields); err != nil {
		return nil, err
	}

	if lineRow != nil {
		entry := store.ItemDamageLog{
			UID: uid, ItemGUID: lineRow.GUID, ItemID: lineRow.ItemID, Event: damageEventLineBreak,
			BeforeDurability: ptrFloat64(lineRow.Durability), AfterDurability: ptrFloat64(lineRow.Durability),
			BeforeLength: ptrInt64(beforeLength), AfterLength: ptrInt64(afterLength), BrokenGUID: brokenGUID,
			DetailJSON: damageDetailJSON(map[string]any{"rod_guid": rodGUID, "loss_m": lossLength}),
		}
		if err := store.AddItemDamageLog(ctx, tx, entry); err != nil {
			return nil, err
		}
		if afterLength <= 0 {
			entry.Event = damageEventLineExhausted
			if err := store.AddItemDamageLog(ctx, tx, entry); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	out := &gearBreakOutcome{BrokenCategory: catLine, LineBroken: true, BrokenGUID: brokenGUID, RemovedGUIDs: removed}
	updated := []string{rodGUID}
	if lineRow != nil && afterLength > 0 {
		updated = append(updated, lineRow.GUID)
	}
	out.UpdatedTrees = s.loadUpdatedTrees(ctx, updated)
	return out, nil
}

func (s *Server) breakRodTx(ctx context.Context, uid int64, rodGUID, brokenGUID string) (*gearBreakOutcome, error) {
	tx, err := s.DB.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rod, err := s.DB.GetItemByGUIDTx(ctx, tx, rodGUID)
	if err != nil || rod == nil || rod.UID != uid {
		return nil, sql.ErrNoRows
	}
	before := rod.Durability
	fields, err := FieldsFromJSON(rod.FieldsJSON)
	if err != nil {
		return nil, err
	}
	idx := fieldIdxByCS(rod.WireClass, csDurability)
	if idx < 0 || !setWireFloat(fields, rod.WireClass, idx, 0) {
		return nil, errDurabilityField
	}
	rod.Durability = 0
	if err := updateItemFieldsTx(ctx, s.DB, tx, rod, fields); err != nil {
		return nil, err
	}
	entry := store.ItemDamageLog{
		UID: uid, ItemGUID: rod.GUID, ItemID: rod.ItemID, Event: damageEventRodBreak,
		BeforeDurability: ptrFloat64(before), AfterDurability: ptrFloat64(0), BrokenGUID: brokenGUID,
		DetailJSON: damageDetailJSON(map[string]any{"rod_guid": rodGUID}),
	}
	if err := store.AddItemDamageLog(ctx, tx, entry); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &gearBreakOutcome{
		BrokenCategory: gearCatRod, LineBroken: false, BrokenGUID: brokenGUID,
		UpdatedTrees: s.loadUpdatedTrees(ctx, []string{rodGUID}),
	}, nil
}

func (s *Server) breakHookTx(ctx context.Context, uid int64, rodGUID, hookGUID string) (*gearBreakOutcome, error) {
	tx, err := s.DB.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rod, err := s.DB.GetItemByGUIDTx(ctx, tx, rodGUID)
	if err != nil || rod == nil || rod.UID != uid {
		return nil, sql.ErrNoRows
	}
	hook, err := s.DB.GetItemByGUIDTx(ctx, tx, hookGUID)
	if err != nil || hook == nil || hook.UID != uid {
		return nil, sql.ErrNoRows
	}
	rodFields, err := FieldsFromJSON(rod.FieldsJSON)
	if err != nil {
		return nil, err
	}
	_, comp, found := rigCompKeyForGUID(rigCompsOf(protoFieldAt(rodFields, rod.WireClass, csRigObject)), hookGUID)
	if !found || comp.Category != catHook {
		return nil, sql.ErrNoRows
	}
	fields, err := FieldsFromJSON(hook.FieldsJSON)
	if err != nil {
		return nil, err
	}
	if !setWireFloat(fields, hook.WireClass, fieldIdxByCS(hook.WireClass, csDurability), 0) {
		return nil, errDurabilityField
	}
	before := hook.Durability
	hook.Durability = 0
	if err := updateItemFieldsTx(ctx, s.DB, tx, hook, fields); err != nil {
		return nil, err
	}
	if err := store.AddItemDamageLog(ctx, tx, store.ItemDamageLog{
		UID: uid, ItemGUID: hook.GUID, ItemID: hook.ItemID, Event: damageEventHookBreak,
		BeforeDurability: ptrFloat64(before), AfterDurability: ptrFloat64(0), BrokenGUID: hookGUID,
		DetailJSON: damageDetailJSON(map[string]any{"rod_guid": rodGUID}),
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &gearBreakOutcome{BrokenCategory: catHook, BrokenGUID: hookGUID, UpdatedTrees: s.loadUpdatedTrees(ctx, []string{hookGUID})}, nil
}

func (s *Server) loadUpdatedTrees(ctx context.Context, guids []string) []*proto.Value {
	out := make([]*proto.Value, 0, len(guids))
	seen := map[string]bool{}
	for _, guid := range guids {
		if guid == "" || seen[guid] {
			continue
		}
		seen[guid] = true
		if row, err := s.DB.GetItemByGUID(ctx, guid); err == nil && row != nil {
			if tree, terr := s.itemTree(ctx, row); terr == nil {
				out = append(out, tree)
			}
		}
	}
	return out
}

func damageDetailJSON(v map[string]any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func protoFieldAt(fields []proto.Field, class, csName string) *proto.Value {
	idx := fieldIdxByCS(class, csName)
	if idx < 0 || idx >= len(fields) {
		return nil
	}
	return fields[idx].Val
}

func buildGearBreakResult(outcome *gearBreakOutcome) (*proto.Value, error) {
	if outcome == nil {
		outcome = &gearBreakOutcome{}
	}
	updated := proto.Null()
	if len(outcome.UpdatedTrees) > 0 {
		updated = proto.Args(proto.S("61"), outcome.UpdatedTrees...)
	}
	removed := proto.Null()
	if len(outcome.RemovedGUIDs) > 0 {
		removed = proto.GuidArr(outcome.RemovedGUIDs)
	}
	return wiregen.MustMakeObj("ikmdmfoojfg", map[int]*proto.Value{
		0: proto.U32(boolU32(outcome.LineBroken)), 1: proto.Guid(outcome.BrokenGUID),
		2: proto.Null(), 3: updated, 4: removed,
	}), nil
}

// 这些错误只在字段结构异常时返回，调用方可回退到兼容响应。
var (
	errLineLengthField = errors.New("鱼线长度字段不存在")
	errDurabilityField = errors.New("耐久字段不存在")
)
