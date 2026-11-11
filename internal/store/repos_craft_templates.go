package store

import (
	"context"
	"database/sql"
)

// 配方模板（9/5/9/6/9/7）的存储入口。
//
// 模板是玩家自建的「配方 + 材料组合」，与静态库的 recipe 表无关：
// recipe 只描述官方配方，craft_template 记录玩家保存的实例，可以同名覆盖。
// 所有读写都带 uid 约束，跨玩家隔离由 SQL 保证，不依赖调用方过滤。

// CraftTemplate 是一条玩家保存的配方模板。
type CraftTemplate struct {
	UID           int64
	GUID          string // 模板实例 id（16 字节 hex），9/7 删除键
	RecipeID      string
	Name          string
	Kind          string
	ToolsJSON     string
	MaterialsJSON string
	CreatedTs     float64
	UpdatedTs     float64
}

const craftTemplateCols = `uid, guid, recipe_id, name, kind, tools_json, materials_json, created_ts, updated_ts`

// SaveCraftTemplate 写入模板；同 uid 同 guid 覆盖，同 (recipe_id, name) 也覆盖。
//
// 两次唯一约束可能命中不同行（客户端换了模板 id 但名字没变），所以先按
// (recipe_id, name) 删掉旧行再插入，整体在一个事务里完成，避免留下孤儿。
func (d *DB) SaveCraftTemplate(ctx context.Context, t *CraftTemplate) error {
	if t == nil {
		return sql.ErrNoRows
	}
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM craft_template WHERE uid=? AND recipe_id=? AND name=? AND guid<>?`,
		t.UID, t.RecipeID, t.Name, t.GUID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO craft_template(`+craftTemplateCols+`)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(uid, guid) DO UPDATE SET
			recipe_id=excluded.recipe_id,
			name=excluded.name,
			kind=excluded.kind,
			tools_json=excluded.tools_json,
			materials_json=excluded.materials_json,
			updated_ts=excluded.updated_ts`,
		t.UID, t.GUID, t.RecipeID, t.Name, t.Kind,
		t.ToolsJSON, t.MaterialsJSON, t.CreatedTs, t.UpdatedTs); err != nil {
		return err
	}
	return tx.Commit()
}

// ListCraftTemplates 按创建时间倒序返回该玩家的全部模板（新的在前）。
func (d *DB) ListCraftTemplates(ctx context.Context, uid int64) ([]*CraftTemplate, error) {
	rows, err := d.SQL.QueryContext(ctx,
		`SELECT `+craftTemplateCols+` FROM craft_template WHERE uid=? ORDER BY created_ts DESC, guid`,
		uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CraftTemplate
	for rows.Next() {
		t := &CraftTemplate{}
		if err := rows.Scan(&t.UID, &t.GUID, &t.RecipeID, &t.Name, &t.Kind,
			&t.ToolsJSON, &t.MaterialsJSON, &t.CreatedTs, &t.UpdatedTs); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteCraftTemplate 删除本人指定 GUID 的模板；返回是否真的删到一行。
// 别人的 guid 传进来同样删不到（uid 在 WHERE 里），不泄露存在性。
func (d *DB) DeleteCraftTemplate(ctx context.Context, uid int64, guid string) (bool, error) {
	res, err := d.SQL.ExecContext(ctx,
		`DELETE FROM craft_template WHERE uid=? AND guid=?`, uid, guid)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
