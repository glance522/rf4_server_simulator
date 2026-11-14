package gameserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"rf4server/internal/store"
)

// couponGrantRepairKey 标记「梦鱼优惠券数量塌陷」补偿只执行一次。
//
// 19/3 领取曾走「非计件」分支，把邮件里的 999 张压成一行 qty=1，字段里的
// 权威数量槽 dnmfciolgep 也没写。类别与数量槽都修好后新券正常，存量那一行
// 却仍然只有 1 张——玩家手里的券凭空少掉。这里按发放审计 + 制作流水补回来。
const couponGrantRepairKey = "migration:coupon_chuchelo_grant:v1"

// couponGrantRecord 是 audit_log.detail 里一段发放记录的精简视图。
type couponGrantRecord struct {
	UID   int64 `json:"uid"`
	Items []struct {
		ItemID string `json:"item_id"`
		Qty    int64  `json:"qty"`
	} `json:"items"`
}

// couponGrant 单个玩家的发放快照。
type couponGrant struct {
	Total   int64   // 累计发放张数
	FirstTS float64 // 最早一次发放时间，用于只统计之后的制作
}

// repairCouponGrantQuantities 依据发放审计与制作流水修复存量券数量。
//
// 推导规则（全部取自库内事实，不写死 uid、不写死数字）：
//  1. 从 audit_log 找出所有发过 coupon_chuchelo 的记录，按玩家累加发放量；
//  2. 统计首发之后该玩家完成的标本板制作次数——券是「一次制作一张」，这些
//     制作本该扣券（当时服务端没扣，见 craft_ops 的 consumeCouponForTrophyTx）；
//  3. 应余量 = 发放量 − 制作次数，钳到 >= 0；
//  4. 只有在该玩家恰好只有一行券、且行内数量小于应余量时才改写，避免覆盖
//     修复上线后已经正常扣过的数据。
//
// 边界：同一玩家存在多行券时，发放量无法映射到具体行——跳过并 WARN，且不写
// 完成标记，让偏差保持可见，而不是猜一个分配方案。
func (s *Server) repairCouponGrantQuantities(ctx context.Context) (int, error) {
	if _, done, err := s.DB.KVGet(ctx, couponGrantRepairKey); err == nil && done {
		return 0, nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("读取梦鱼券补偿标记: %w", err)
	}

	grants, err := s.couponGrants(ctx)
	if err != nil {
		return 0, err
	}
	if len(grants) == 0 {
		if err := s.DB.KVSet(ctx, couponGrantRepairKey, "done;grants=0"); err != nil {
			return 0, fmt.Errorf("写入梦鱼券补偿标记: %w", err)
		}
		return 0, nil
	}

	// 标本板产物的 item_id 集合：一次制作消耗一张券，按产出物反查制作次数，
	// 不依赖 recipe 表在玩家库还是静态库。
	trophyOutputs, err := s.trophyBoardOutputIDs(ctx)
	if err != nil {
		return 0, err
	}

	fixed, skippedMulti := 0, 0
	for uid, grant := range grants {
		used := s.trophyCraftCountSince(ctx, uid, grant.FirstTS, trophyOutputs)
		want := grant.Total - used
		if want < 0 {
			want = 0
		}
		guids, gerr := s.couponRowGUIDs(ctx, uid)
		if gerr != nil {
			return fixed, gerr
		}
		switch {
		case len(guids) == 0:
			continue // 券已被消耗干净或从未落库
		case len(guids) > 1:
			skippedMulti++
			s.log.Warn("梦鱼券补偿跳过：同一玩家多行券，无法映射发放量",
				"uid", uid, "rows", len(guids), "granted", grant.Total, "want", want)
			continue
		}
		cur, cerr := s.couponRowQuantity(ctx, guids[0])
		if cerr != nil {
			return fixed, cerr
		}
		if cur >= want {
			continue // 已正常：修复后扣过，或本来就在合理范围
		}
		if err := s.setCouponRowQuantity(ctx, guids[0], want); err != nil {
			return fixed, err
		}
		fixed++
		s.log.Info("梦鱼券存量补偿", "uid", uid, "guid", guids[0],
			"granted", grant.Total, "crafted", used, "from", cur, "to", want)
	}

	if skippedMulti > 0 {
		// 多行券玩家无法把发放量映射到具体行：本轮跳过且绝不写完成标记，
		// 让偏差保持可见、下次启动继续重试，而不是用一个全局 marker 永久隐藏。
		s.log.Warn("梦鱼券补偿存在多行玩家，保留标记待重试",
			"skipped", skippedMulti, "fixed", fixed)
		return fixed, nil
	}
	marker := fmt.Sprintf("done;fixed=%d;skip_multi=%d", fixed, skippedMulti)
	if err := s.DB.KVSet(ctx, couponGrantRepairKey, marker); err != nil {
		return fixed, fmt.Errorf("写入梦鱼券补偿标记: %w", err)
	}
	return fixed, nil
}

// couponRowRepairKey 标记「梦鱼券一券一实例」布局迁移只执行一次。
//
// 背景：券实例类 ahgeldclmdj 没有客户端 UI 数量槽（ljnnpokapig），一行 N 张
// 在背包里显示不出张数；实测两张独立券会渲染成两张卡片。本迁移只做布局转换：
// 把每个「qty>1」的券行展开成 qty 行各 1 张，总张数守恒，不增不删。
// 账目（历史塌陷补回正确余量）由 v1 repairCouponGrantQuantities 负责——
// v1 管「有多少张」，v2 管「怎么摆放」，两个迁移职责分开。
const couponRowRepairKey = "migration:coupon_chuchelo_rows:v2"

// couponLayoutRow 一行券当前代表的张数。
type couponLayoutRow struct {
	GUID string
	Qty  int64
}

// repairCouponRowLayout 把存量「一行 N 张」的券展开成 N 行各 1 张。
//
// 只读库内现状，不回查审计账目：玩家当前有多少张就摆成多少行。这样运营手动
// 调整过张数后（例如为验收改成 4 张）不会被历史发放记录拉回去。
func (s *Server) repairCouponRowLayout(ctx context.Context) (int, error) {
	if _, done, err := s.DB.KVGet(ctx, couponRowRepairKey); err == nil && done {
		return 0, nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("读取梦鱼券展开标记: %w", err)
	}

	groups, err := s.couponLayoutRows(ctx)
	if err != nil {
		return 0, err
	}
	if len(groups) == 0 {
		if err := s.DB.KVSet(ctx, couponRowRepairKey, "done;rows=0"); err != nil {
			return 0, fmt.Errorf("写入梦鱼券展开标记: %w", err)
		}
		return 0, nil
	}

	fixed, failed := 0, 0
	for uid, rows := range groups {
		changed, eerr := s.expandCouponRowsForUID(ctx, uid, rows)
		if eerr != nil {
			s.log.Warn("梦鱼券展开失败", "uid", uid, "err", eerr)
			failed++
			continue
		}
		fixed += changed
		if changed > 0 {
			s.log.Info("梦鱼券展开为一券一实例", "uid", uid, "expanded_rows", changed)
		}
	}

	if failed > 0 {
		// 有玩家没展开成功：不写完成标记，下次启动重试；已是一券一行的玩家
		// 会被快速跳过，重试成本只是一次扫表。
		s.log.Warn("梦鱼券展开存在未完成玩家，下次启动重试", "failed", failed, "fixed", fixed)
		return fixed, nil
	}
	marker := fmt.Sprintf("done;fixed=%d", fixed)
	if err := s.DB.KVSet(ctx, couponRowRepairKey, marker); err != nil {
		return fixed, fmt.Errorf("写入梦鱼券展开标记: %w", err)
	}
	return fixed, nil
}

// couponLayoutRows 列出所有玩家的券行及张数，按 uid 分组。
func (s *Server) couponLayoutRows(ctx context.Context) (map[int64][]couponLayoutRow, error) {
	rows, err := s.DB.SQL.QueryContext(ctx, `
		SELECT guid, uid FROM item WHERE item_id=? ORDER BY uid, created_ts, guid`,
		couponChucheloItemID)
	if err != nil {
		return nil, fmt.Errorf("读取梦鱼券行: %w", err)
	}
	defer rows.Close()
	type raw struct {
		guid string
		uid  int64
	}
	var list []raw
	for rows.Next() {
		var r raw
		if err := rows.Scan(&r.guid, &r.uid); err != nil {
			return nil, err
		}
		list = append(list, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := map[int64][]couponLayoutRow{}
	for _, r := range list {
		qty, qerr := s.couponRowQuantity(ctx, r.guid)
		if qerr != nil {
			return nil, qerr
		}
		out[r.uid] = append(out[r.uid], couponLayoutRow{GUID: r.guid, Qty: qty})
	}
	return out, nil
}

// expandCouponRowsForUID 在一个经济事务里把该玩家所有 qty>1 的券行展开。
// 原行归一为 1 张，差量部分补建新行；qty<1 的空行直接删除（不补成 1 张），
// 总张数保持不变，GUID 稳定可追溯。
func (s *Server) expandCouponRowsForUID(ctx context.Context, uid int64, rows []couponLayoutRow) (int, error) {
	toNormalize := map[string]int64{}
	toDelete := []string{}
	var total int64
	var addTotal int64
	for _, r := range rows {
		q := r.Qty
		if q < 1 {
			// 0 张行是脏数据：删掉即可，不能补成 1 张，否则总张数凭空增加。
			toDelete = append(toDelete, r.GUID)
			continue
		}
		total += q
		if q > 1 {
			toNormalize[r.GUID] = q
			addTotal += q - 1
		}
	}
	if total > maxBundlePreparedItems {
		return 0, fmt.Errorf("梦鱼券总张数 %d 超过上限 %d", total, maxBundlePreparedItems)
	}
	if len(toNormalize) == 0 && len(toDelete) == 0 {
		return 0, nil // 已经是一券一行
	}
	// addTotal==0 时不能调 prepareCouponRows：count 下限会把它夹成 1，凭空造一行。
	prepared, perr := s.prepareCouponExpansionRows(ctx, uid, addTotal)
	if perr != nil {
		return 0, perr
	}

	tx, err := beginEconomyTx(ctx, s.DB)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, guid := range toDelete {
		if err := deleteCouponRowTx(ctx, tx, uid, guid); err != nil {
			return 0, err
		}
	}
	for guid := range toNormalize {
		if err := s.setCouponRowQuantityTx(ctx, tx, guid, 1); err != nil {
			return 0, err
		}
	}
	for _, it := range prepared {
		if _, _, gerr := grantPreparedItemTx(ctx, tx, it, false); gerr != nil {
			return 0, gerr
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(toNormalize) + len(toDelete), nil
}

// prepareCouponExpansionRows 只在确实要补行时构造，避免 addTotal==0 时被
// prepareCouponRows 的 count 下限夹成 1 张而凭空多发。
func (s *Server) prepareCouponExpansionRows(ctx context.Context, uid int64, addTotal int64) ([]*store.Item, error) {
	if addTotal < 1 {
		return nil, nil
	}
	return s.prepareCouponRows(ctx, uid, couponChucheloItemID, "backpack", addTotal)
}

// deleteCouponRowTx 删除一行 0 张券及其槽位/钓组投影，照 consumeItemQtyTx 的
// 扣空分支，保证不留孤立的 container_slot / rig_component。
func deleteCouponRowTx(ctx context.Context, tx *sql.Tx, uid int64, guid string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM item WHERE guid=? AND uid=?`, guid, uid); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM container_slot WHERE guid=?`, guid); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM rig_component WHERE guid=?`, guid)
	return err
}

// setCouponRowQuantityTx 在事务内把券行改写为指定张数（字段槽 + item.qty 同步）。
func (s *Server) setCouponRowQuantityTx(ctx context.Context, tx *sql.Tx, guid string, qty int64) error {
	if qty < 1 {
		qty = 1
	}
	row, err := getItemTx(ctx, tx, guid)
	if err != nil {
		return fmt.Errorf("梦鱼券展开读取 %s: %w", guid, err)
	}
	if row == nil {
		return nil
	}
	fields, ferr := FieldsFromJSON(row.FieldsJSON)
	if ferr != nil || len(fields) == 0 {
		return fmt.Errorf("梦鱼券 %s 字段解析失败: %w", guid, ferr)
	}
	if !setItemQty(row.WireClass, fields, uint64(qty)) {
		return fmt.Errorf("梦鱼券 %s 无数量槽（wire_class=%s）", guid, row.WireClass)
	}
	bumpCreatedTicks(fields, row.WireClass)
	fj, jerr := FieldsToJSON(fields)
	if jerr != nil {
		return fmt.Errorf("梦鱼券 %s 字段序列化失败: %w", guid, jerr)
	}
	if _, uerr := tx.ExecContext(ctx, `
		UPDATE item SET fields_json=?, qty=? WHERE guid=? AND uid=?`,
		fj, qty, guid, row.UID); uerr != nil {
		return fmt.Errorf("梦鱼券 %s 写回失败: %w", guid, uerr)
	}
	return nil
}

// couponGrants 从 audit_log 汇总每个玩家的券发放量与首发时间。
func (s *Server) couponGrants(ctx context.Context) (map[int64]couponGrant, error) {
	rows, err := s.DB.SQL.QueryContext(ctx, `
		SELECT COALESCE(target,''), COALESCE(detail,''), ts
		FROM audit_log
		WHERE action='mail_send' AND detail LIKE '%' || ? || '%'
		ORDER BY ts, id`, couponChucheloItemID)
	if err != nil {
		return nil, fmt.Errorf("读取梦鱼券发放记录: %w", err)
	}
	defer rows.Close()
	out := map[int64]couponGrant{}
	for rows.Next() {
		var target, detail string
		var ts float64
		if err := rows.Scan(&target, &detail, &ts); err != nil {
			return nil, err
		}
		var rec couponGrantRecord
		if jerr := json.Unmarshal([]byte(detail), &rec); jerr != nil {
			continue // 单条审计文本损坏不阻断其它记录
		}
		uid := rec.UID
		if uid == 0 {
			if _, serr := fmt.Sscan(target, &uid); serr != nil {
				continue
			}
		}
		var qty int64
		for _, it := range rec.Items {
			if it.ItemID != couponChucheloItemID {
				continue
			}
			n := it.Qty
			if n < 1 {
				n = 1
			}
			qty += n
		}
		if qty == 0 {
			continue
		}
		g := out[uid]
		g.Total += qty
		if g.FirstTS == 0 || ts < g.FirstTS {
			g.FirstTS = ts
		}
		out[uid] = g
	}
	return out, rows.Err()
}

// trophyBoardOutputIDs 标本板/支架/吊顶的产物 item_id（item_def.category=71）。
func (s *Server) trophyBoardOutputIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.DB.SQL.QueryContext(ctx,
		`SELECT COALESCE(item_id,'') FROM item_def WHERE category=?`, trophyMountCategory)
	if err != nil {
		return nil, fmt.Errorf("读取标本产物清单: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id != "" {
			out[id] = true
		}
	}
	return out, rows.Err()
}

// trophyCraftCountSince 统计 since 之后玩家完成的标本制作次数。
//
// txn.item_id 记的就是产物 id（实测 fish_board_l_16 / fish_board_l_15 /
// fish_board_m_14 三行），所以直接按产物聚合，不必回调 recipe 表。
func (s *Server) trophyCraftCountSince(ctx context.Context, uid int64, since float64, outputs map[string]bool) int64 {
	if len(outputs) == 0 {
		return 0
	}
	rows, err := s.DB.SQL.QueryContext(ctx, `
		SELECT COALESCE(item_id,''), COUNT(*) FROM txn
		WHERE uid=? AND kind='craft' AND ts>=? GROUP BY item_id`, uid, since)
	if err != nil {
		s.log.Warn("梦鱼券补偿：读取制作流水失败", "uid", uid, "err", err)
		return 0
	}
	defer rows.Close()
	var n int64
	for rows.Next() {
		var itemID string
		var c int64
		if err := rows.Scan(&itemID, &c); err != nil {
			s.log.Warn("梦鱼券补偿：解析制作流水失败", "uid", uid, "err", err)
			return n
		}
		if outputs[itemID] {
			n += c
		}
	}
	return n
}

// couponRowGUIDs 玩家背包/钓组里的券行。
func (s *Server) couponRowGUIDs(ctx context.Context, uid int64) ([]string, error) {
	rows, err := s.DB.SQL.QueryContext(ctx, `
		SELECT guid FROM item WHERE uid=? AND item_id=? ORDER BY created_ts, guid`,
		uid, couponChucheloItemID)
	if err != nil {
		return nil, fmt.Errorf("读取玩家梦鱼券: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var guid string
		if err := rows.Scan(&guid); err != nil {
			return nil, err
		}
		out = append(out, guid)
	}
	return out, rows.Err()
}

// couponRowQuantity 读券行当前张数，优先取字段里的权威数量槽。
func (s *Server) couponRowQuantity(ctx context.Context, guid string) (int64, error) {
	row, err := s.DB.GetItemByGUID(ctx, guid)
	if err != nil {
		return 0, fmt.Errorf("读取梦鱼券行 %s: %w", guid, err)
	}
	if row == nil {
		return 0, nil
	}
	if fields, ferr := FieldsFromJSON(row.FieldsJSON); ferr == nil {
		if n, ok := itemQtyValue(row.WireClass, fields); ok && n > 0 {
			return int64(n), nil
		}
	}
	return row.Qty, nil
}

// setCouponRowQuantity 把券行改写为指定张数（字段槽 + item.qty 同步）。
// 走事务：数量变更属于经济写操作，中途失败必须整体回滚。
func (s *Server) setCouponRowQuantity(ctx context.Context, guid string, qty int64) error {
	if qty < 0 {
		return fmt.Errorf("券数量不能为负: %d", qty)
	}
	tx, err := s.DB.SQL.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("梦鱼券补偿开启事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	row, err := getItemTx(ctx, tx, guid)
	if err != nil {
		return fmt.Errorf("梦鱼券补偿读取 %s: %w", guid, err)
	}
	if row == nil {
		return nil
	}
	fields, ferr := FieldsFromJSON(row.FieldsJSON)
	if ferr != nil || len(fields) == 0 {
		return fmt.Errorf("梦鱼券 %s 字段解析失败: %w", guid, ferr)
	}
	if !setItemQty(row.WireClass, fields, uint64(qty)) {
		return fmt.Errorf("梦鱼券 %s 无数量槽（wire_class=%s）", guid, row.WireClass)
	}
	bumpCreatedTicks(fields, row.WireClass)
	fj, jerr := FieldsToJSON(fields)
	if jerr != nil {
		return fmt.Errorf("梦鱼券 %s 字段序列化失败: %w", guid, jerr)
	}
	if _, uerr := tx.ExecContext(ctx, `
		UPDATE item SET fields_json=?, qty=? WHERE guid=? AND uid=?`,
		fj, qty, guid, row.UID); uerr != nil {
		return fmt.Errorf("梦鱼券 %s 写回失败: %w", guid, uerr)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("梦鱼券 %s 提交失败: %w", guid, err)
	}
	return nil
}
