package store

import (
	"context"
	"database/sql"
)

// 技能、配方和物品关联的存储入口。
//
// 单件数量的读改写位于 gameserver，因为同步字段和 qty 列需要物品类元数据。

// SkillDef 是 skill_def 表中的技能节点定义。
type SkillDef struct {
	CatID     int64
	NodeID    int64
	UnlockPct float64
	MaxLevel  int64
	EffectPct float64
	GroupID   int64
	Enabled   int64
	Ordinal   int64
}

// GetSkillDef 按技能类别和节点 ID 读取定义；无记录时返回 nil。
func (d *DB) GetSkillDef(ctx context.Context, catID, nodeID int64) (*SkillDef, error) {
	row := d.SQL.QueryRowContext(ctx, `
		SELECT cat_id, node_id, unlock_pct, max_level, effect_pct, group_id, enabled, ordinal
		FROM skill_def WHERE cat_id=? AND node_id=?`, catID, nodeID)
	sd := &SkillDef{}
	err := row.Scan(&sd.CatID, &sd.NodeID, &sd.UnlockPct, &sd.MaxLevel, &sd.EffectPct,
		&sd.GroupID, &sd.Enabled, &sd.Ordinal)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return sd, nil
}

// GetSkillPoints 返回技能大类熟练度；无记录时返回 0。
func (d *DB) GetSkillPoints(ctx context.Context, uid, skillID int64) (float64, error) {
	var pts float64
	err := d.SQL.QueryRowContext(ctx,
		`SELECT MIN(points, ?) FROM skill WHERE uid=? AND skill_id=?`,
		maxSkillPoints, uid, skillID).Scan(&pts)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}
	return pts, nil
}

// GetSkillNode 返回节点当前等级；无记录时返回 0。
func (d *DB) GetSkillNode(ctx context.Context, uid, nodeID int64) (int64, error) {
	var lv int64
	if pair, shared := sharedSkillPair(nodeID); shared {
		err := d.SQL.QueryRowContext(ctx, `SELECT MAX(0,COALESCE(MAX(level),0))
			FROM skill_node WHERE uid=? AND node_id IN (?,?)`, uid, pair[0], pair[1]).Scan(&lv)
		return lv, err
	}
	err := d.SQL.QueryRowContext(ctx,
		`SELECT level FROM skill_node WHERE uid=? AND node_id=?`, uid, nodeID).Scan(&lv)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}
	return lv, nil
}

// SetSkillNode 写入节点等级；同一节点使用 UPSERT 覆盖。
func (d *DB) SetSkillNode(ctx context.Context, uid, nodeID, level int64) error {
	if pair, shared := sharedSkillPair(nodeID); shared {
		tx, err := d.SQL.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, node := range pair {
			if _, err := tx.ExecContext(ctx, `INSERT INTO skill_node(uid,node_id,level) VALUES(?,?,?)
				ON CONFLICT(uid,node_id) DO UPDATE SET level=excluded.level`, uid, node, level); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO skill_node(uid, node_id, level) VALUES(?,?,?)
		ON CONFLICT (uid, node_id) DO UPDATE SET level=?`,
		uid, nodeID, level, level)
	return err
}

// SkillNodeLevelSum 返回永久已投入点数，共享节点只计一份。
func (d *DB) SkillNodeLevelSum(ctx context.Context, uid int64) (int64, error) {
	nodes, err := d.ListSkillNodes(ctx, uid)
	if err != nil {
		return 0, err
	}
	return CountSkillNodePoints(nodes), nil
}

// DeleteSkillNodes 删除玩家的全部技能节点等级。
func (d *DB) DeleteSkillNodes(ctx context.Context, uid int64) error {
	_, err := d.SQL.ExecContext(ctx, `DELETE FROM skill_node WHERE uid=?`, uid)
	return err
}

// Recipe 是 recipe 表中的制作配方。
type Recipe struct {
	RecipeID      string
	Name          string
	OutputID      string
	OutputQty     int64
	MaterialsJSON string
	DurationSec   float64
	Kind          string
}

const recipeCols = `
	recipe_id, COALESCE(name,''), COALESCE(output_id,''), COALESCE(output_qty,1),
	COALESCE(materials_json,''), COALESCE(duration_sec,0), COALESCE(kind,'')`

func scanRecipe(row *sql.Row) (*Recipe, error) {
	r := &Recipe{}
	err := row.Scan(&r.RecipeID, &r.Name, &r.OutputID, &r.OutputQty,
		&r.MaterialsJSON, &r.DurationSec, &r.Kind)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

// GetRecipe 按配方 ID 读取配方。
func (d *DB) GetRecipe(ctx context.Context, recipeID string) (*Recipe, error) {
	return scanRecipe(d.SQL.QueryRowContext(ctx,
		`SELECT `+recipeCols+` FROM recipe WHERE recipe_id=?`, recipeID))
}

// GetRecipeByOutput 按产出物 ID 反查配方。同一产出可能对应多条材料组合，
// 此时返回 recipe_id 最小的一条。
func (d *DB) GetRecipeByOutput(ctx context.Context, outputID string) (*Recipe, error) {
	return scanRecipe(d.SQL.QueryRowContext(ctx,
		`SELECT `+recipeCols+` FROM recipe WHERE output_id=? ORDER BY recipe_id LIMIT 1`, outputID))
}

// ListRecipes 按 recipe_id 升序返回全部配方。
func (d *DB) ListRecipes(ctx context.Context) ([]*Recipe, error) {
	rows, err := d.SQL.QueryContext(ctx,
		`SELECT `+recipeCols+` FROM recipe ORDER BY recipe_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Recipe
	for rows.Next() {
		r := &Recipe{}
		if err := rows.Scan(&r.RecipeID, &r.Name, &r.OutputID, &r.OutputQty,
			&r.MaterialsJSON, &r.DurationSec, &r.Kind); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteItemCascade 删除物品及其 container_slot、rig_component 关联行。
// 三张表必须同时清理，否则已删除的 GUID 仍会残留在槽位或钓组中。
func (d *DB) DeleteItemCascade(ctx context.Context, guid string) error {
	if _, err := d.SQL.ExecContext(ctx, `DELETE FROM item WHERE guid=?`, guid); err != nil {
		return err
	}
	if _, err := d.SQL.ExecContext(ctx, `DELETE FROM container_slot WHERE guid=?`, guid); err != nil {
		return err
	}
	_, err := d.SQL.ExecContext(ctx, `DELETE FROM rig_component WHERE guid=?`, guid)
	return err
}

// DeleteItemCascadeTx 在事务内删除物品及其槽位、钓组关联行。
func (d *DB) DeleteItemCascadeTx(ctx context.Context, tx *sql.Tx, guid string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM item WHERE guid=?`, guid); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM container_slot WHERE guid=?`, guid); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM rig_component WHERE guid=?`, guid)
	return err
}

// DeleteItemOfUID 删除指定玩家的一件物品，并返回是否确有行被删除。
func (d *DB) DeleteItemOfUID(ctx context.Context, uid int64, guid string) (bool, error) {
	r, err := d.SQL.ExecContext(ctx, `DELETE FROM item WHERE guid=? AND uid=?`, guid, uid)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n > 0, nil
}

// DeleteItemRefs 清理物品的槽位和钓组关联行。
func (d *DB) DeleteItemRefs(ctx context.Context, guid string) error {
	if _, err := d.SQL.ExecContext(ctx, `DELETE FROM container_slot WHERE guid=?`, guid); err != nil {
		return err
	}
	_, err := d.SQL.ExecContext(ctx, `DELETE FROM rig_component WHERE guid=?`, guid)
	return err
}

// ListItemsByContainers 返回指定容器中的物品，包括已装到钓组上的配件。
// 结果按 created_ts、guid 稳定排序。
func (d *DB) ListItemsByContainers(ctx context.Context, uid int64, containers []string) ([]*Item, error) {
	q, args := placeholders(`
		SELECT `+itemCols+` FROM item WHERE uid=? AND container IN (`, containers, ") ORDER BY created_ts, guid")
	rows, err := d.SQL.QueryContext(ctx, q, append([]interface{}{uid}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Item
	for rows.Next() {
		it := &Item{}
		if err := rows.Scan(&it.GUID, &it.UID, &it.ItemID, &it.NumericID, &it.WireClass,
			&it.TypeID, &it.Container, &it.Slot, &it.RigGUID, &it.Qty, &it.BuyPrice,
			&it.FieldsJSON, &it.Durability, &it.Workmanship, &it.CreatedTS); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// UpdateItemQty 只更新 qty 列。没有字段快照的实例适用此函数，
// 避免把 fields_json 写成空而抹掉协议内容。
func (d *DB) UpdateItemQty(ctx context.Context, guid string, qty int64) error {
	_, err := d.SQL.ExecContext(ctx, `UPDATE item SET qty=? WHERE guid=?`, qty, guid)
	return err
}
