package store

import (
	"context"
	"database/sql"
	"fmt"
)

// repos_rigcomponent.go 提供 rig_component 的 (slot, ordinal) 投影写入口。
// f5 是业务真源；调用方通过 ReplaceRigComponents 全量重建，避免残留陈旧行。

// UpsertRigComponentAt 精确写某品类的第 ordinal 个槽。
func (d *DB) UpsertRigComponentAt(ctx context.Context, rigGUID string, slot, ordinal int64, guid string, extra int64) error {
	if ordinal < 0 {
		return fmt.Errorf("rig_component ordinal 不能为负: %d", ordinal)
	}
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO rig_component(rig_guid, slot, ordinal, guid, extra)
		VALUES(?,?,?,?,?)
		ON CONFLICT (rig_guid, slot, ordinal) DO UPDATE SET guid=?, extra=?`,
		rigGUID, slot, ordinal, nullIfEmpty(guid), extra, nullIfEmpty(guid), extra)
	return err
}

// UpsertRigComponentAuto 在同一 guid 已存在时更新 extra，否则追加到该品类
// 下一个 ordinal。它不删除其他行，适用于轮、拟饵、手具等非 f5 容器；
// 竿实例应使用 ReplaceRigComponents。
func (d *DB) UpsertRigComponentAuto(ctx context.Context, rigGUID string, slot int64, guid string, extra int64) error {
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		UPDATE rig_component SET extra=?
		WHERE rig_guid=? AND slot=? AND guid=?`,
		extra, rigGUID, slot, guid)
	if err != nil {
		return err
	}
	if n, aerr := res.RowsAffected(); aerr != nil {
		return aerr
	} else if n > 0 {
		return tx.Commit()
	}

	var ordinal int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(ordinal)+1,0) FROM rig_component
		WHERE rig_guid=? AND slot=?`, rigGUID, slot).Scan(&ordinal); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO rig_component(rig_guid, slot, ordinal, guid, extra)
		VALUES(?,?,?,?,?)`,
		rigGUID, slot, ordinal, nullIfEmpty(guid), extra); err != nil {
		return err
	}
	return tx.Commit()
}

// ReplaceRigComponents 原子替换一个 rig 的全部派生组件行。空列表也会清空，
// 这是“派生表永不留幽灵行”的唯一重建入口。
func (d *DB) ReplaceRigComponents(ctx context.Context, rigGUID string, comps []RigComponent) error {
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := d.ReplaceRigComponentsTx(ctx, tx, rigGUID, comps); err != nil {
		return err
	}
	return tx.Commit()
}

// ReplaceRigComponentsTx 在调用方事务内全量重建派生组件行。
func (d *DB) ReplaceRigComponentsTx(ctx context.Context, tx *sql.Tx, rigGUID string, comps []RigComponent) error {
	seen := make(map[[2]int64]bool, len(comps))
	for _, c := range comps {
		key := [2]int64{c.Slot, c.Ordinal}
		if seen[key] {
			return fmt.Errorf("rig_component 重复槽: slot=%d ordinal=%d", c.Slot, c.Ordinal)
		}
		seen[key] = true
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM rig_component WHERE rig_guid=?`, rigGUID); err != nil {
		return err
	}
	for _, c := range comps {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO rig_component(rig_guid, slot, ordinal, guid, extra)
			VALUES(?,?,?,?,?)`,
			rigGUID, c.Slot, c.Ordinal, nullIfEmpty(c.GUID), c.Extra); err != nil {
			return err
		}
	}
	return nil
}

// DeleteRigComponentAt 精确删一个投影槽；不再按品类误删同品类的其它实例。
func (d *DB) DeleteRigComponentAt(ctx context.Context, rigGUID string, slot, ordinal int64) error {
	_, err := d.SQL.ExecContext(ctx,
		`DELETE FROM rig_component WHERE rig_guid=? AND slot=? AND ordinal=?`,
		rigGUID, slot, ordinal)
	return err
}

// DeleteRigComponentsBySlot 删除某品类的全部 ordinal。
func (d *DB) DeleteRigComponentsBySlot(ctx context.Context, rigGUID string, slot int64) error {
	_, err := d.SQL.ExecContext(ctx,
		`DELETE FROM rig_component WHERE rig_guid=? AND slot=?`, rigGUID, slot)
	return err
}
