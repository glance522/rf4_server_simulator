package gameserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// 经济、库存、邮件和买断功能共用的原子事务原语。
//
// store.DB 只允许一个 SQLite 连接。事务持有时不能调用任何 store.DB 方法，
// 文件内读写直接使用 *sql.Tx；依赖 item_def 的只读准备在开启事务前完成。

var errInsufficientFunds = errors.New("余额不足")

func beginEconomyTx(ctx context.Context, db *store.DB) (*sql.Tx, error) {
	if db == nil || db.SQL == nil {
		return nil, errors.New("数据库未初始化")
	}
	return db.SQL.BeginTx(ctx, nil)
}

func ensureProfileTx(ctx context.Context, tx *sql.Tx, uid int64) error {
	now := float64(time.Now().Unix())
	_, err := tx.ExecContext(ctx, `
		INSERT INTO profile(uid, name, created_ts, updated_ts)
		VALUES(?, ?, ?, ?) ON CONFLICT (uid) DO NOTHING`,
		uid, fmt.Sprintf("player_%d", uid), now, now)
	return err
}

func addTxnTx(ctx context.Context, tx *sql.Tx, uid int64, kind, itemID, guid string,
	silverDelta, goldDelta int64, note string) error {
	var itemArg, guidArg any
	if itemID != "" {
		itemArg = itemID
	}
	if guid != "" {
		guidArg = guid
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO txn(uid, ts, kind, item_id, guid, silver_delta, gold_delta, note)
		VALUES(?,?,?,?,?,?,?,?)`,
		uid, float64(time.Now().Unix()), kind, itemArg, guidArg, silverDelta, goldDelta, note)
	return err
}

// applyWalletTx 原子更新余额并写流水。条件 UPDATE 避免并发覆盖，负余额由 SQL 拒绝。
func applyWalletTx(ctx context.Context, tx *sql.Tx, uid, silverDelta, goldDelta int64,
	kind, itemID, guid, note string) error {
	if err := ensureProfileTx(ctx, tx, uid); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE profile
		SET silver=silver+?, gold=gold+?, updated_ts=?
		WHERE uid=? AND silver+?>=0 AND gold+?>=0`,
		silverDelta, goldDelta, float64(time.Now().Unix()), uid, silverDelta, goldDelta)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errInsufficientFunds
	}
	return addTxnTx(ctx, tx, uid, kind, itemID, guid, silverDelta, goldDelta, note)
}

const txItemCols = `
	guid, uid, COALESCE(item_id,''), COALESCE(numeric_id,0), COALESCE(wire_class,''),
	COALESCE(type_id,0), container, slot, COALESCE(rig_guid,''), qty, buy_price,
	COALESCE(fields_json,''), durability, workmanship, COALESCE(created_ts,0)`

func scanTxItem(row interface{ Scan(...any) error }) (*store.Item, error) {
	it := &store.Item{}
	err := row.Scan(&it.GUID, &it.UID, &it.ItemID, &it.NumericID, &it.WireClass,
		&it.TypeID, &it.Container, &it.Slot, &it.RigGUID, &it.Qty, &it.BuyPrice,
		&it.FieldsJSON, &it.Durability, &it.Workmanship, &it.CreatedTS)
	if err != nil {
		return nil, err
	}
	return it, nil
}

func getItemTx(ctx context.Context, tx *sql.Tx, guid string) (*store.Item, error) {
	return scanTxItem(tx.QueryRowContext(ctx,
		`SELECT `+txItemCols+` FROM item WHERE guid=?`, guid))
}

func getOwnedItemTx(ctx context.Context, tx *sql.Tx, uid int64, guid string) (*store.Item, error) {
	return scanTxItem(tx.QueryRowContext(ctx,
		`SELECT `+txItemCols+` FROM item WHERE guid=? AND uid=?`, guid, uid))
}

func listItemsTx(ctx context.Context, tx *sql.Tx, uid int64, containers []string) ([]*store.Item, error) {
	q := `SELECT ` + txItemCols + ` FROM item WHERE uid=? AND container IN (?,?) ORDER BY created_ts, guid`
	args := []any{uid}
	if len(containers) >= 2 {
		args = append(args, containers[0], containers[1])
	} else if len(containers) == 1 {
		args = append(args, containers[0], containers[0])
	} else {
		args = append(args, "backpack", "rig")
	}
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*store.Item
	for rows.Next() {
		it, err := scanTxItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func itemDefByItemIDTx(ctx context.Context, tx *sql.Tx, itemID string) (*store.ItemDef, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT numeric_id, COALESCE(item_id,''), COALESCE(category,0), COALESCE(kind,''),
		       COALESCE(brand,''), COALESCE(attrs_json,'')
		FROM item_def WHERE item_id=?`, itemID)
	def := &store.ItemDef{}
	if err := row.Scan(&def.NumericID, &def.ItemID, &def.Category, &def.Kind,
		&def.Brand, &def.AttrsJSON); err != nil {
		return nil, err
	}
	return def, nil
}

func itemDefByNumericIDTx(ctx context.Context, tx *sql.Tx, numericID int64) (*store.ItemDef, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT numeric_id, COALESCE(item_id,''), COALESCE(category,0), COALESCE(kind,''),
		       COALESCE(brand,''), COALESCE(attrs_json,'')
		FROM item_def WHERE numeric_id=?`, numericID)
	def := &store.ItemDef{}
	if err := row.Scan(&def.NumericID, &def.ItemID, &def.Category, &def.Kind,
		&def.Brand, &def.AttrsJSON); err != nil {
		return nil, err
	}
	return def, nil
}

func stackCountTx(ctx context.Context, tx *sql.Tx, it *store.Item) int64 {
	if it == nil {
		return 0
	}
	fields, err := FieldsFromJSON(it.FieldsJSON)
	if err == nil {
		if n, ok := itemQtyValue(it.WireClass, fields); ok && n > 0 {
			return int64(n)
		}
	}
	if def, derr := itemDefByNumericIDTx(ctx, tx, it.NumericID); derr == nil && def != nil {
		if v, ok := defAttrU64(def, csQty); ok && v > 0 {
			return int64(v)
		}
	}
	if it.Qty > 0 {
		return it.Qty
	}
	return 1
}

func insertItemTx(ctx context.Context, tx *sql.Tx, it *store.Item) error {
	if it == nil || it.GUID == "" || it.WireClass == "" {
		return errors.New("物品构造不完整")
	}
	var itemArg, rigArg any
	if it.ItemID != "" {
		itemArg = it.ItemID
	}
	if it.RigGUID != "" {
		rigArg = it.RigGUID
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO item(guid, uid, item_id, numeric_id, wire_class, type_id, container, slot,
		                 rig_guid, qty, buy_price, fields_json, created_ts, durability, workmanship)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		it.GUID, it.UID, itemArg, it.NumericID, it.WireClass, it.TypeID, it.Container, it.Slot,
		rigArg, it.Qty, it.BuyPrice, it.FieldsJSON, it.CreatedTS, it.Durability, it.Workmanship)
	return err
}

// prepareNewItem 构造物品对象但不落库；事务内的 grantPreparedItemTx 负责 INSERT。
func (s *Server) prepareNewItem(ctx context.Context, uid int64, itemID, container string,
	units int64, explicitGUID string) (*store.Item, error) {
	def, err := s.DB.GetItemDefByItemID(ctx, itemID)
	if err != nil || def == nil {
		return nil, fmt.Errorf("物品定义不存在: %s", itemID)
	}
	cls, ok := resolveWireClass(def)
	if !ok {
		cls = wireClassEntry{fallbackWireClass, fallbackTypeID}
	}
	obj, err := wiregen.MakeObj(cls.Class, nil)
	if err != nil {
		return nil, err
	}
	s.applyDefAttrs(def, cls.Class, obj)
	guid := explicitGUID
	if guid == "" {
		guid = NewItemGUID()
	}
	setHex := func(cs, v string) {
		if idx := fieldIdxByCS(cls.Class, cs); idx >= 0 {
			obj.Fields[idx].Hex = v
		}
	}
	setNum := func(cs string, v uint64) {
		if idx := fieldIdxByCS(cls.Class, cs); idx >= 0 {
			obj.Fields[idx].Num = ptrU64(v)
		}
	}
	setHex(csGUID, guid)
	setNum(csNumericID, uint64(def.NumericID))
	rowQty := int64(1)
	if countableCategories[def.Category] {
		if idx := itemQtyFieldIdx(cls.Class); idx >= 0 {
			defQty := u64Of(obj.Fields[idx].Num)
			if defQty == 0 {
				defQty = 1
			}
			if units < 1 {
				units = 1
			}
			fieldQty := defQty * uint64(units)
			writeItemQty(cls.Class, obj, fieldQty)
			rowQty = int64(fieldQty)
		}
	}
	s.materializeInto(ctx, cls.Class, def, obj, guid, 1, 1)
	if idx := fieldIdxByCS(cls.Class, csCreated); idx >= 0 {
		obj.Fields[idx].Num = ptrU64(uint64(netTicksNow()))
	}
	if idx := fieldIdxByCS(cls.Class, csUpdated); idx >= 0 {
		obj.Fields[idx].Num = ptrU64(uint64(proto.WindowsFiletimeNow()))
	}
	if idx := fieldIdxByCS(cls.Class, csStateEnum); idx >= 0 {
		st := u64Of(obj.Fields[idx].Num)
		if bits, ok := defAttrU64(def, "hgaijieklek"); ok {
			st = bits
		}
		switch cls.Class {
		case "cnolibojkao", "dgjinoebdop", "hgabifpnhmk", "knocoeggfdk", "dbmbnkpkafb":
			st |= 2048
		}
		obj.Fields[idx].Num = ptrU64(st)
	}
	it := &store.Item{
		GUID: guid, UID: uid, ItemID: def.ItemID, NumericID: def.NumericID,
		WireClass: cls.Class, TypeID: cls.TypeID, Container: container, Qty: rowQty,
		Durability: 1, Workmanship: 1, CreatedTS: float64(time.Now().Unix()),
	}
	if cls.Class == "cnolibojkao" {
		if err := s.rebuildRod(ctx, it, obj); err != nil {
			return nil, err
		}
	}
	s.canonicalizeItemPhysicalFields(ctx, it, obj.Fields)
	fj, err := FieldsToJSON(obj.Fields)
	if err != nil {
		return nil, err
	}
	it.FieldsJSON = fj
	return it, nil
}

// grantPreparedItemTx 将预处理物品入包；merge=true 时按类别语义并入已有堆。
func grantPreparedItemTx(ctx context.Context, tx *sql.Tx, prepared *store.Item, merge bool) (*store.Item, bool, error) {
	if prepared == nil {
		return nil, false, errors.New("空物品")
	}
	def, err := itemDefByItemIDTx(ctx, tx, prepared.ItemID)
	if err != nil {
		return nil, false, err
	}
	if merge && countableCategories[def.Category] {
		row, err := scanTxItem(tx.QueryRowContext(ctx, `SELECT `+txItemCols+`
			FROM item WHERE uid=? AND container=? AND item_id=?
			ORDER BY slot, created_ts, guid LIMIT 1`,
			prepared.UID, prepared.Container, prepared.ItemID))
		if err == nil {
			fields, _ := FieldsFromJSON(row.FieldsJSON)
			cur := stackCountTx(ctx, tx, row)
			add := prepared.Qty
			if add < 1 {
				add = 1
			}
			setItemQty(row.WireClass, fields, uint64(cur+add))
			bumpCreatedTicks(fields, row.WireClass)
			fj, ferr := FieldsToJSON(fields)
			if ferr != nil {
				return nil, false, ferr
			}
			newQty := cur + add
			if _, err := tx.ExecContext(ctx, `
				UPDATE item SET fields_json=?, qty=?, durability=?, workmanship=?, rig_guid=?
				WHERE guid=? AND uid=?`,
				fj, newQty, row.Durability, row.Workmanship, row.RigGUID, row.GUID, row.UID); err != nil {
				return nil, false, err
			}
			row.FieldsJSON = fj
			row.Qty = newQty
			return row, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, false, err
		}
	}
	if err := insertItemTx(ctx, tx, prepared); err != nil {
		return nil, false, err
	}
	return prepared, false, nil
}

func consumeItemQtyTx(ctx context.Context, tx *sql.Tx, row *store.Item, n int64) (int64, error) {
	if row == nil || row.GUID == "" {
		return 0, errors.New("空材料")
	}
	have := stackCountTx(ctx, tx, row)
	if have < 1 {
		have = 1
	}
	if n < 1 {
		n = 1
	}
	take := n
	if have < take {
		take = have
	}
	if have <= take {
		if _, err := tx.ExecContext(ctx, `DELETE FROM item WHERE guid=? AND uid=?`, row.GUID, row.UID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM container_slot WHERE guid=?`, row.GUID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM rig_component WHERE guid=?`, row.GUID); err != nil {
			return 0, err
		}
		row.Qty = 0
		return have, nil
	}
	left := have - take
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil || len(fields) == 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE item SET qty=? WHERE guid=? AND uid=?`, left, row.GUID, row.UID); err != nil {
			return 0, err
		}
	} else {
		setItemQty(row.WireClass, fields, uint64(left))
		bumpCreatedTicks(fields, row.WireClass)
		fj, ferr := FieldsToJSON(fields)
		if ferr != nil {
			return 0, ferr
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE item SET fields_json=?, qty=?, durability=?, workmanship=?, rig_guid=?
			WHERE guid=? AND uid=?`,
			fj, left, row.Durability, row.Workmanship, row.RigGUID, row.GUID, row.UID); err != nil {
			return 0, err
		}
		row.FieldsJSON = fj
	}
	row.Qty = left
	return take, nil
}

func effectiveCategoryTx(ctx context.Context, tx *sql.Tx, itemID string) (int64, bool) {
	def, err := itemDefByItemIDTx(ctx, tx, itemID)
	if err != nil || def == nil {
		return 0, false
	}
	if def.Category != bundleCategory {
		return def.Category, true
	}
	if bag, ok := defAttrs(def)["nbkhfhenbag"].([]any); ok {
		for _, ent := range bag {
			m, _ := ent.(map[string]any)
			inner, _ := m["cemjmlpafmc"].(string)
			if inner != "" {
				if di, derr := itemDefByItemIDTx(ctx, tx, inner); derr == nil && di != nil {
					return di.Category, true
				}
			}
		}
	}
	return def.Category, true
}

func removeItemsTx(ctx context.Context, tx *sql.Tx, uid int64, guids []string) (int64, error) {
	seen := map[string]bool{}
	var n int64
	for _, g := range guids {
		if g == "" || seen[g] {
			continue
		}
		seen[g] = true
		res, err := tx.ExecContext(ctx, `DELETE FROM item WHERE guid=? AND uid=?`, g, uid)
		if err != nil {
			return n, err
		}
		if k, _ := res.RowsAffected(); k > 0 {
			n += k
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM container_slot WHERE guid=?`, g); err != nil {
			return n, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM rig_component WHERE guid=?`, g); err != nil {
			return n, err
		}
	}
	return n, nil
}

// uniqueStrings 保持首次出现顺序，用于卖鱼/拆解/领取 GUID 去重。
func uniqueStrings(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
