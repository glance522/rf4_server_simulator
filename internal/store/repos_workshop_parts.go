package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// WorkshopPartPrice 是工坊配件价/工时的后台覆盖行（迁移 9100010）。
//
// 三列都可空，NULL 的语义是"这一列不覆盖，继续走代码里的兜底层"，
// 和"0"完全不同：轮油要按官服收 49 金币、0 小时，就必须显式写 0/49/0。
type WorkshopPartPrice struct {
	ItemID    string  `json:"item_id"`
	MapKey    string  `json:"map_key"`
	Silver    *int64  `json:"silver"`
	Gold      *int64  `json:"gold"`
	Hours     *int64  `json:"hours"`
	Source    string  `json:"source"`
	Note      string  `json:"note"`
	UpdatedAt float64 `json:"updated_at"`
}

// HasPrice 表示这行是否覆盖了价格（银或金任一列非 NULL）。
// 只覆盖工时（hours）的行不参与定价，仍要按标价 × 部位倍率走。
func (r *WorkshopPartPrice) HasPrice() bool {
	return r != nil && (r.Silver != nil || r.Gold != nil)
}

// WorkshopPartPrice 取某个配件在某张图的覆盖行：先查该图的精确行，
// 没有再退全局行（map_key 为空串）；都没有返回 (nil, nil)。
//
// 查询失败也返回 (nil, nil)：调用方一律按"没有覆盖"处理继续走兜底，
// 覆盖表坏了不能让玩家修不了装备。
func (d *DB) WorkshopPartPrice(ctx context.Context, itemID, mapKey string) (*WorkshopPartPrice, error) {
	if d == nil || itemID == "" {
		return nil, nil
	}
	get := func(key string) (*WorkshopPartPrice, error) {
		row := d.SQL.QueryRowContext(ctx, `
			SELECT item_id, map_key, silver, gold, hours, source, note, updated_at
			FROM workshop_part_price WHERE item_id=? AND map_key=?`, itemID, key)
		var rec WorkshopPartPrice
		var silver, gold, hours sql.NullInt64
		if err := row.Scan(&rec.ItemID, &rec.MapKey, &silver, &gold, &hours,
			&rec.Source, &rec.Note, &rec.UpdatedAt); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, nil
			}
			return nil, err
		}
		if silver.Valid {
			v := silver.Int64
			rec.Silver = &v
		}
		if gold.Valid {
			v := gold.Int64
			rec.Gold = &v
		}
		if hours.Valid {
			v := hours.Int64
			rec.Hours = &v
		}
		return &rec, nil
	}
	if mapKey != "" {
		if rec, err := get(mapKey); rec != nil || err != nil {
			return rec, err
		}
	}
	return get("")
}

// WorkshopPartHours 取某个配件的工时覆盖（游戏内小时），NULL 表示没有覆盖。
//
// 单独一条查询而不是复用 WorkshopPartPrice：工时要跨行找——该图的精确行
// 可能只写了价格没写工时，这时全局行的工时仍然有效；反过来也一样。选行的
// 优先级仍是「该图精确行 > 全局行」，但只在 hours IS NOT NULL 的行里挑，
// 避免"精确行没写工时就再也取不到全局工时"这个坑。
func (d *DB) WorkshopPartHours(ctx context.Context, itemID, mapKey string) (*int64, error) {
	if d == nil || itemID == "" {
		return nil, nil
	}
	row := d.SQL.QueryRowContext(ctx, `
		SELECT hours FROM workshop_part_price
		WHERE item_id=? AND hours IS NOT NULL AND (map_key=? OR map_key='')
		ORDER BY CASE WHEN map_key=? THEN 0 ELSE 1 END
		LIMIT 1`, itemID, mapKey, mapKey)
	var hours sql.NullInt64
	if err := row.Scan(&hours); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if !hours.Valid {
		return nil, nil
	}
	v := hours.Int64
	return &v, nil
}

// WorkshopPartPrices 读取全部覆盖行（后台列表用）。
func (d *DB) WorkshopPartPrices(ctx context.Context) ([]WorkshopPartPrice, error) {
	return d.workshopPartPricesWhere(ctx, "")
}

// WorkshopPartPricesByItem 只读某个配件的覆盖行（不分地图）。
func (d *DB) WorkshopPartPricesByItem(ctx context.Context, itemID string) ([]WorkshopPartPrice, error) {
	return d.workshopPartPricesWhere(ctx, itemID)
}

func (d *DB) workshopPartPricesWhere(ctx context.Context, itemID string) ([]WorkshopPartPrice, error) {
	if d == nil {
		return nil, nil
	}
	sqlText := `SELECT item_id, map_key, silver, gold, hours, source, note, updated_at
		FROM workshop_part_price`
	var args []any
	if itemID != "" {
		sqlText += ` WHERE item_id=?`
		args = append(args, itemID)
	}
	sqlText += ` ORDER BY item_id, map_key`
	rows, err := d.SQL.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]WorkshopPartPrice, 0, 64)
	for rows.Next() {
		var rec WorkshopPartPrice
		var silver, gold, hours sql.NullInt64
		if err := rows.Scan(&rec.ItemID, &rec.MapKey, &silver, &gold, &hours,
			&rec.Source, &rec.Note, &rec.UpdatedAt); err != nil {
			return nil, err
		}
		if silver.Valid {
			v := silver.Int64
			rec.Silver = &v
		}
		if gold.Valid {
			v := gold.Int64
			rec.Gold = &v
		}
		if hours.Valid {
			v := hours.Int64
			rec.Hours = &v
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// SaveWorkshopPartPrice 写入/更新一行覆盖。三列传 nil 表示"不覆盖该列"，
// 因此重复保存同一行时要把不想改的列原样传回来（或者干脆删行重建）。
func (d *DB) SaveWorkshopPartPrice(ctx context.Context, rec WorkshopPartPrice) error {
	if d == nil {
		return errors.New("store: nil db")
	}
	if rec.ItemID == "" {
		return errors.New("store: item_id required")
	}
	if rec.Source == "" {
		rec.Source = "manual"
	}
	if rec.UpdatedAt == 0 {
		rec.UpdatedAt = float64(time.Now().Unix())
	}
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO workshop_part_price(item_id, map_key, silver, gold, hours, source, note, updated_at)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(item_id, map_key) DO UPDATE SET
			silver=excluded.silver,
			gold=excluded.gold,
			hours=excluded.hours,
			source=excluded.source,
			note=excluded.note,
			updated_at=excluded.updated_at`,
		rec.ItemID, rec.MapKey, nullInt64(rec.Silver), nullInt64(rec.Gold), nullInt64(rec.Hours),
		rec.Source, rec.Note, rec.UpdatedAt)
	return err
}

// DeleteWorkshopPartPrice 删掉一行覆盖，删完即回到标价/兜底。
func (d *DB) DeleteWorkshopPartPrice(ctx context.Context, itemID, mapKey string) error {
	if d == nil {
		return errors.New("store: nil db")
	}
	_, err := d.SQL.ExecContext(ctx,
		`DELETE FROM workshop_part_price WHERE item_id=? AND map_key=?`, itemID, mapKey)
	return err
}

func nullInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}
