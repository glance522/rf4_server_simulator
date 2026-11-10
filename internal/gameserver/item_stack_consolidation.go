package gameserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

type itemStackGroupKey struct {
	uid       int64
	container string
	itemID    string
}

func nonReturnableCountableCategory(category int64) bool {
	return countableCategories[category] && !storeReturnCategoryAllowed(category)
}

func clearStackStoreReceipt(class string, fields []proto.Field) bool {
	idx := fieldIdxByCS(class, csOrigin)
	if idx < 0 || idx >= len(fields) || fields[idx].Val == nil || fields[idx].Val.Kind == proto.KindNull {
		return false
	}
	fields[idx].Op = "VAL"
	fields[idx].Val = proto.Null()
	return true
}

// ConsolidateNonReturnableItemStacks 把升级前遗留的不可退计件重复堆按
// uid、容器和精确 item_id 合并。装到钓组上的实例不属于物品仓储，保持原样。
// 全部修改在一个玩家库事务内完成；任一组无法无损合并时整笔回滚。
func (s *Server) ConsolidateNonReturnableItemStacks(ctx context.Context) (int, error) {
	if s.DB == nil || s.DB.SQL == nil {
		return 0, errors.New("整理计件物品：数据库未连接")
	}
	tx, err := beginEconomyTx(ctx, s.DB)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT i.guid,i.uid,COALESCE(i.item_id,''),COALESCE(i.numeric_id,0),
		       COALESCE(i.wire_class,''),COALESCE(i.type_id,0),i.container,i.slot,
		       COALESCE(i.rig_guid,''),i.qty,i.buy_price,COALESCE(i.fields_json,''),
		       i.durability,i.workmanship,COALESCE(i.created_ts,0),COALESCE(d.category,0)
		FROM item i JOIN item_def d ON d.item_id=i.item_id
		WHERE COALESCE(i.rig_guid,'')=''
		ORDER BY i.uid,i.container,i.item_id,i.slot,i.created_ts,i.guid`)
	if err != nil {
		return 0, err
	}
	type categorizedItem struct {
		row      *store.Item
		category int64
	}
	items := make([]categorizedItem, 0)
	for rows.Next() {
		it := &store.Item{}
		var category int64
		if err := rows.Scan(&it.GUID, &it.UID, &it.ItemID, &it.NumericID, &it.WireClass,
			&it.TypeID, &it.Container, &it.Slot, &it.RigGUID, &it.Qty, &it.BuyPrice,
			&it.FieldsJSON, &it.Durability, &it.Workmanship, &it.CreatedTS, &category); err != nil {
			_ = rows.Close()
			return 0, err
		}
		items = append(items, categorizedItem{row: it, category: category})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	groups := make(map[itemStackGroupKey][]*store.Item)
	order := make([]itemStackGroupKey, 0)
	for _, item := range items {
		if !nonReturnableCountableCategory(item.category) {
			continue
		}
		row := item.row
		key := itemStackGroupKey{uid: row.UID, container: row.Container, itemID: row.ItemID}
		if _, exists := groups[key]; !exists {
			order = append(order, key)
		}
		groups[key] = append(groups[key], row)
	}

	removed := 0
	for _, key := range order {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		group := groups[key]
		if len(group) == 0 {
			continue
		}
		keep := group[0]
		// 干净的单堆无需解析或写库；这使启动时的重复整理真正零写入。
		if len(group) == 1 && keep.BuyPrice == 0 {
			continue
		}
		keepFields, err := FieldsFromJSON(keep.FieldsJSON)
		if err != nil {
			return 0, fmt.Errorf("整理 %s：解析保留堆 %s: %w", key.itemID, keep.GUID, err)
		}
		limit := bundleQuantityLimit(keep.WireClass)
		total := int64(0)
		for _, row := range group {
			fields, err := FieldsFromJSON(row.FieldsJSON)
			if err != nil {
				return 0, fmt.Errorf("整理 %s：解析堆 %s: %w", key.itemID, row.GUID, err)
			}
			qty, ok := itemQtyValue(row.WireClass, fields)
			if !ok || row.Qty < 0 || qty != uint64(row.Qty) {
				return 0, fmt.Errorf("整理 %s：堆 %s 数量字段不一致", key.itemID, row.GUID)
			}
			if row.WireClass != keep.WireClass || row.NumericID != keep.NumericID || row.TypeID != keep.TypeID ||
				row.Durability != keep.Durability || row.Workmanship != keep.Workmanship {
				return 0, fmt.Errorf("整理 %s：堆 %s 实例属性不一致", key.itemID, row.GUID)
			}
			if row.Qty > limit-total {
				return 0, fmt.Errorf("整理 %s：合并数量溢出", key.itemID)
			}
			total += row.Qty
		}

		changed := clearStackStoreReceipt(keep.WireClass, keepFields) || keep.BuyPrice != 0
		if len(group) > 1 {
			if !setItemQty(keep.WireClass, keepFields, uint64(total)) {
				return 0, fmt.Errorf("整理 %s：实例类 %s 没有数量字段", key.itemID, keep.WireClass)
			}
			bumpCreatedTicks(keepFields, keep.WireClass)
			changed = true
		}
		if changed {
			fieldsJSON, err := FieldsToJSON(keepFields)
			if err != nil {
				return 0, fmt.Errorf("整理 %s：编码保留堆: %w", key.itemID, err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE item SET fields_json=?,qty=?,buy_price=0 WHERE guid=? AND uid=?`,
				fieldsJSON, total, keep.GUID, keep.UID); err != nil {
				return 0, err
			}
		}

		for _, row := range group {
			if _, err := tx.ExecContext(ctx, `DELETE FROM kv WHERE k=?`, storePurchaseKey(row.UID, row.GUID)); err != nil {
				return 0, err
			}
		}
		if len(group) > 1 {
			var bindContainer, bindSlot int64
			hasBinding := false
			for _, row := range group {
				var container, slot int64
				err := tx.QueryRowContext(ctx, `SELECT container,slot FROM container_slot WHERE uid=? AND guid=?`, row.UID, row.GUID).
					Scan(&container, &slot)
				if err == nil && !hasBinding {
					bindContainer, bindSlot, hasBinding = container, slot, true
				} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return 0, err
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM container_slot WHERE uid=? AND guid=?`, row.UID, row.GUID); err != nil {
					return 0, err
				}
			}
			if hasBinding {
				if _, err := tx.ExecContext(ctx, `INSERT INTO container_slot(uid,container,slot,guid) VALUES(?,?,?,?)`,
					keep.UID, bindContainer, bindSlot, keep.GUID); err != nil {
					return 0, err
				}
			}
		}

		for _, drop := range group[1:] {
			if _, err := tx.ExecContext(ctx, `UPDATE drink_bonus_effect SET source_guid=? WHERE uid=? AND source_guid=?`,
				keep.GUID, drop.UID, drop.GUID); err != nil {
				return 0, err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM rig_component WHERE guid=?`, drop.GUID); err != nil {
				return 0, err
			}
			result, err := tx.ExecContext(ctx, `DELETE FROM item WHERE guid=? AND uid=?`, drop.GUID, drop.UID)
			if err != nil {
				return 0, err
			}
			n, err := result.RowsAffected()
			if err != nil || n != 1 {
				return 0, fmt.Errorf("整理 %s：删除重复堆 %s 影响 %d 行: %w", key.itemID, drop.GUID, n, err)
			}
			removed++
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return removed, nil
}
