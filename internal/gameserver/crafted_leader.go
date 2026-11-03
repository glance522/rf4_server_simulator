package gameserver

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func isCraftedLeader(row *store.Item) bool {
	return row != nil && row.WireClass == "oldfpclaabo" && strings.HasPrefix(row.ItemID, "leader_crafted_")
}

// root 的耐久与各材料自己的剩余强度分别生效，不把已磨损的材料恢复成新件。
func leaderFieldsStrength(fields []proto.Field, durability float64) float64 {
	limit := 0.0
	for _, key := range []string{gearAttrLeaderIn, gearAttrLeaderLock, "baadhdmaonp"} {
		child := protoFieldAt(fields, "oldfpclaabo", key)
		if child == nil || child.Kind != proto.KindObj {
			continue
		}
		if si := fieldIdxByCS(child.Class, csDurability); si >= 0 && si < len(child.Fields) && child.Fields[si].Num != nil && proto.F32FromBits(uint32(u64Of(child.Fields[si].Num))) <= 0 {
			return 0
		}
		idx := fieldIdxByCS(child.Class, csStrength)
		if idx < 0 || idx >= len(child.Fields) {
			continue
		}
		v := float64(proto.F32FromBits(uint32(u64Of(child.Fields[idx].Num))))
		if !isFiniteStrength(v) {
			return 0
		}
		if limit == 0 || v < limit {
			limit = v
		}
	}
	return limit * math.Max(0, math.Min(1, durability))
}

func isFiniteStrength(v float64) bool { return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }

type leaderCraftMaterial struct {
	row   *store.Item
	units int64
	key   string
	tree  *proto.Value
}

// 选材及物化在事务前完成；扣料时重新校验原实例，失败整体回滚。
func (s *Server) leaderCraftMaterials(ctx context.Context, uid int64, req craftPayload, demands []craftDemand) ([]leaderCraftMaterial, error) {
	rows, err := s.DB.ListItemsByContainers(ctx, uid, []string{"backpack"})
	if err != nil {
		return nil, err
	}
	selected := map[string]string{}
	for _, slot := range req.Materials {
		if _, exists := selected[slot.ItemID]; exists {
			return nil, fmt.Errorf("材料槽重复")
		}
		selected[slot.ItemID] = slot.GUID
	}
	if len(selected) != len(demands) {
		return nil, fmt.Errorf("材料槽不完整")
	}
	used := map[string]bool{}
	var out []leaderCraftMaterial
	for _, demand := range demands {
		guid, ok := selected[demand.DemandID]
		if !ok {
			return nil, fmt.Errorf("材料需求不匹配")
		}
		var picked *store.Item
		var def *store.ItemDef
		for _, row := range rows {
			if row.GUID != guid {
				continue
			}
			picked = row
			def, err = s.itemDefForRow(ctx, row)
			break
		}
		if picked == nil || err != nil || def == nil || picked.Durability <= 0 || used[guid] {
			return nil, fmt.Errorf("材料不存在或不可用")
		}
		allowed := false
		for _, category := range demand.Categories {
			if def.Category == category {
				allowed = true
			}
		}
		if !allowed {
			return nil, fmt.Errorf("材料品类不匹配")
		}
		key := gearAttrLeaderLock
		if def.Category == catLine {
			key = "baadhdmaonp"
		} else if def.Category == catWireLeader {
			key = gearAttrLeaderIn
		} else if def.Category != catLeaderLock {
			return nil, fmt.Errorf("前导材料品类不支持")
		}
		tree, err := s.ItemToTree(ctx, picked)
		if err != nil {
			return nil, err
		}
		picked, err = s.DB.GetItemByGUID(ctx, guid)
		if err != nil {
			return nil, err
		}
		out = append(out, leaderCraftMaterial{row: picked, units: demand.need(), key: key, tree: tree})
		used[guid] = true
	}
	return out, nil
}

func materializeCraftedLeader(row *store.Item, materials []leaderCraftMaterial) error {
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return err
	}
	for _, material := range materials {
		// 通过 JSON 深拷贝，批量制作的内嵌材料不能共用 GUID 或可变树。
		raw, err := FieldsToJSON(material.tree.Fields)
		if err != nil {
			return err
		}
		childFields, err := FieldsFromJSON(raw)
		if err != nil {
			return err
		}
		child := *material.tree
		child.Fields = childFields
		for _, pair := range []struct{ key, value string }{{csGUID, uuidLike(row.GUID, material.key)}, {csParent, row.GUID}} {
			if idx := fieldIdxByCS(child.Class, pair.key); idx >= 0 {
				child.Fields[idx].Hex = pair.value
			}
		}
		if idx := fieldIdxByCS(child.Class, csSlotEnum); idx >= 0 {
			child.Fields[idx].Num = ptrU64(4)
		}
		if idx := fieldIdxByCS(child.Class, csStateEnum); idx >= 0 {
			child.Fields[idx].Num = ptrU64(u64Of(child.Fields[idx].Num) | subMountFlag)
		}
		if idx := fieldIdxByCS(child.Class, csQty); idx >= 0 {
			child.Fields[idx].Num = ptrU64(uint64(material.units))
		}
		idx := fieldIdxByCS(row.WireClass, material.key)
		if idx < 0 {
			return fmt.Errorf("前导材料字段不存在")
		}
		fields[idx].Val = &child
	}
	strength := leaderFieldsStrength(fields, row.Durability)
	if strength <= 0 {
		return fmt.Errorf("前导材料没有有效强度")
	}
	setWireFloat(fields, row.WireClass, fieldIdxByCS(row.WireClass, csStrength), strength)
	row.FieldsJSON, err = FieldsToJSON(fields)
	return err
}

func consumeLeaderMaterials(ctx context.Context, tx *sql.Tx, materials []leaderCraftMaterial, times int64) ([]string, error) {
	var touched []string
	for _, material := range materials {
		fresh, err := scanTxItem(tx.QueryRowContext(ctx, `SELECT `+txItemCols+` FROM item WHERE guid=?`, material.row.GUID))
		if err != nil || fresh == nil || fresh.UID != material.row.UID || fresh.Container != "backpack" || fresh.FieldsJSON != material.row.FieldsJSON || fresh.Durability != material.row.Durability || fresh.Workmanship != material.row.Workmanship {
			return nil, fmt.Errorf("制作材料已变化")
		}
		need := material.units * times
		if need <= 0 || stackCountTx(ctx, tx, fresh) < need {
			return nil, fmt.Errorf("制作材料不足")
		}
		got, err := consumeItemQtyTx(ctx, tx, fresh, need)
		if err != nil || got != need {
			return nil, fmt.Errorf("扣除制作材料失败")
		}
		touched = append(touched, fresh.GUID)
	}
	return touched, nil
}
