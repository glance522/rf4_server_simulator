package store

import (
	"context"
	"database/sql"
	"math"
	"strconv"
	"strings"
)

// 钓鱼经济域存储：地点解析、鱼种名册、饵效、工坊和命名鱼读取。
// 静态鱼库由 seed 从 server_data/static.sqlite3 导入；本文件只做读取和最小写入。

// ---- 地点解析 ----

// sublocPrefix 从场景名（loc1_02 / lvl2_01 / lvl_004_05）提取 level 前缀（level_001_）。
// 客户端在钓鱼时上报的是场景名，编号可能带前导下划线和三位补零，必须一并兼容。
func sublocPrefix(loc string) string {
	body := loc
	if strings.HasPrefix(body, "loc") {
		body = body[3:]
	} else if strings.HasPrefix(body, "lvl") {
		body = body[3:]
	} else {
		return ""
	}
	body = strings.TrimLeft(body, "_")
	if body == "" {
		return ""
	}
	numPart := body
	if us := strings.IndexByte(body, '_'); us > 0 {
		numPart = body[:us]
	}
	n, err := strconv.Atoi(numPart)
	if err != nil || n <= 0 {
		return ""
	}
	return "level_" + fmt03(n) + "_"
}

func fmt03(n int) string {
	s := strconv.Itoa(n)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}

// ResolveStatLoc 游戏地点 → 统计站地点名（loc_alias.loc）。
// level_ 前缀按全名精确查；场景名按 level_NNN_ 前缀 LIKE 查。
// 无法解析时返回空串。
func (d *DB) ResolveStatLoc(ctx context.Context, loc string) string {
	loc = strings.TrimSpace(loc)
	if loc == "" {
		return ""
	}
	if strings.HasPrefix(loc, "level_") {
		var out string
		_ = d.SQL.QueryRowContext(ctx,
			`SELECT loc FROM loc_alias WHERE alias=?`, loc).Scan(&out)
		return out
	}
	if prefix := sublocPrefix(loc); prefix != "" {
		var out string
		_ = d.SQL.QueryRowContext(ctx,
			`SELECT loc FROM loc_alias WHERE alias LIKE ? ORDER BY alias LIMIT 1`,
			prefix+"%").Scan(&out)
		return out
	}
	return ""
}

// ResolveLevelKey 将场景名转换为游戏 level key。无法解析时返回空串。
func (d *DB) ResolveLevelKey(ctx context.Context, loc string) string {
	loc = strings.TrimSpace(loc)
	if loc == "" {
		return ""
	}
	if strings.HasPrefix(loc, "level_") {
		return loc
	}
	if prefix := sublocPrefix(loc); prefix != "" {
		var out string
		_ = d.SQL.QueryRowContext(ctx,
			`SELECT alias FROM loc_alias WHERE alias LIKE ? ORDER BY alias LIMIT 1`,
			prefix+"%").Scan(&out)
		return out
	}
	return ""
}

// ---- 鱼库名册 ----

// FishRosterRow 鱼种权重行（wiki 名册为全集，统计捕获数作权重，缺统计保底 1）。
type FishRosterRow struct {
	FishKey string
	Catches float64
}

// LocRoster 该统计站地点的鱼种名册：
// 优先读取 loc_wiki_fish 全集并连接 loc_fish 的捕获计数；为空时回退到
// catches>0 的 loc_fish 行。
func (d *DB) LocRoster(ctx context.Context, statLoc string) ([]*FishRosterRow, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT w.fish_key, COALESCE(l.catches, 1)
		FROM loc_wiki_fish w LEFT JOIN loc_fish l
		  ON l.loc = w.loc AND l.fish_key = w.fish_key
		WHERE w.loc=?`, statLoc)
	if err == nil {
		out, scanErr := scanRoster(rows)
		if scanErr == nil && len(out) > 0 {
			return out, nil
		}
	}
	rows, err = d.SQL.QueryContext(ctx,
		`SELECT fish_key, catches FROM loc_fish WHERE loc=? AND catches>0`, statLoc)
	if err != nil {
		return nil, err
	}
	return scanRoster(rows)
}

func scanRoster(rows *sql.Rows) ([]*FishRosterRow, error) {
	defer rows.Close()
	var out []*FishRosterRow
	for rows.Next() {
		r := &FishRosterRow{}
		if err := rows.Scan(&r.FishKey, &r.Catches); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GradeLines 该鱼种三档位线（g）：(达标线 trophy_g, 奖杯线 trophy_line_g,
// 蓝冠线 super_trophy_line_g)；缺失字段返回 nil。
func (d *DB) GradeLines(ctx context.Context, fishKey string) (val, tro, sup *int64) {
	var v, t, s sql.NullInt64
	err := d.SQL.QueryRowContext(ctx, `
		SELECT COALESCE(g.qualified_g, w.trophy_g),
		       COALESCE(g.trophy_g, w.trophy_line_g),
		       COALESCE(g.blue_trophy_g, w.super_trophy_line_g)
		FROM species_weight w
		LEFT JOIN fish_grade_lines g ON g.fish_key = w.fish_key
		WHERE w.fish_key=?`, fishKey).Scan(&v, &t, &s)
	if err != nil {
		return nil, nil, nil
	}
	if v.Valid {
		x := v.Int64
		val = &x
	}
	if t.Valid {
		x := t.Int64
		tro = &x
	}
	if s.Valid {
		x := s.Int64
		sup = &x
	}
	return
}

// SpeciesLengthKP 返回体长公式 k*w^p 的系数。NULL 或缺少记录时使用
// LENGTH_K_DEFAULT 和 LENGTH_P_DEFAULT。
func (d *DB) SpeciesLengthKP(ctx context.Context, fishKey string) (k, p float64) {
	k, p = 0.0355, 1.0/3.0
	var lk, lp sql.NullFloat64
	if err := d.SQL.QueryRowContext(ctx,
		`SELECT length_k, length_p FROM species_weight WHERE fish_key=?`,
		fishKey).Scan(&lk, &lp); err != nil {
		return
	}
	if lk.Valid && lk.Float64 > 0 {
		k = lk.Float64
	}
	if lp.Valid && lp.Float64 > 0 {
		p = lp.Float64
	}
	return
}

// ---- companion 饵效 ----

// BaitTiers 饵 systemID → {fish_key: tier}（top5=3 / top20=2 / 在列=1）。
// 表缺失或无数据时返回 nil，不调整鱼种权重。
func (d *DB) BaitTiers(ctx context.Context, baitID string) map[string]int64 {
	if baitID == "" {
		return nil
	}
	rows, err := d.SQL.QueryContext(ctx,
		`SELECT fish_key, tier FROM companion_bait_fish WHERE bait_id=?`, baitID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var fk string
		var tier int64
		if err := rows.Scan(&fk, &tier); err != nil {
			return nil
		}
		out[fk] = tier
	}
	if err := rows.Err(); err != nil || len(out) == 0 {
		return nil
	}
	return out
}

// CompanionEvidence fish_key → 该鱼 companion 数据覆盖度（total_evidence）。
// 缺表返回 nil（覆盖率按 0 处理）。
func (d *DB) CompanionEvidence(ctx context.Context) map[string]int64 {
	rows, err := d.SQL.QueryContext(ctx,
		`SELECT fish_key, total_evidence FROM companion_fish_meta`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var fk string
		var ev int64
		if err := rows.Scan(&fk, &ev); err != nil {
			return nil
		}
		out[fk] = ev
	}
	return out
}

// ---- 工坊 ----

// ListWornItems 耐久低于 threshold 的物品（修理面板）。
// 结果按 durability 升序排列。
func (d *DB) ListWornItems(ctx context.Context, uid int64, threshold float64) ([]*Item, error) {
	rows, err := d.SQL.QueryContext(ctx,
		`SELECT `+itemCols+` FROM item WHERE uid=? AND durability<? ORDER BY durability`,
		uid, threshold)
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

// ListRepairItems 返回修理面板所需的完整物品清单（全部随身物品，含满耐久）。
//
// 客户端修理页左侧展示的是“随身物品表”，点击任意一件都会在 21/1 的返回列表里
// 按 GUID 查修理信息；只返回磨损件会让客户端查不到模型，得到空详情。
// 结果按 durability 升序排列，磨损最重的排在前面。
func (d *DB) ListRepairItems(ctx context.Context, uid int64) ([]*Item, error) {
	rows, err := d.SQL.QueryContext(ctx,
		`SELECT `+itemCols+` FROM item WHERE uid=? ORDER BY durability, slot`, uid)
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

// SetItemDurability 将耐久限制在 0..1 后写入列。读取路径以该列为准重建字段，
// 不需要同步修改 fields_json。
func (d *DB) SetItemDurability(ctx context.Context, guid string, value float64) error {
	value = math.Max(0.0, math.Min(1.0, value))
	_, err := d.SQL.ExecContext(ctx,
		`UPDATE item SET durability=? WHERE guid=?`, value, guid)
	return err
}

// ---- 工坊修理排队（银币修理要等，金币即时） ----

// WorkshopRepairJob 是"银币修理进行中"的一件在办任务。
//
// 只保存剩余工时尚和修理完成后的快照：完工时把 FieldsJSON/Durability 写回 item
// 行即可，不重放计价逻辑（钱在入队时就扣过了）。
//
// 工时按**在线时长**计（官服口径：下线倒计时冻结）：
//   - RemainingS 是唯一判定依据，只有玩家在线时才被 TickWorkshopRepairJobs 扣减；
//   - TickTS 是上一次扣时的 unix 秒，下线后不再前进，下次登录会先重置再结算；
//   - DueTS 只是"假设一直在线"的展示投影（= TickTS + RemainingS），不要拿它判定。
type WorkshopRepairJob struct {
	GUID       string
	UID        int64
	DueTS      float64
	RemainingS float64
	TickTS     float64
	FieldsJSON string
	Durability float64
	SilverPaid int64
	Parts      int
	CreatedTS  float64
	// StoreID 是排队时用的工坊 id（21/3 请求的 args[0]，例如 "workshop_01"）。
	// 在办期间物品树的时效对象要把它原样回显给客户端（见
	// gameserver/workshop_repair_lock.go），所以必须落库而不是现猜。
	StoreID string
}

// workshopJobCols 是 WorkshopRepairJob 的固定列序，读写两处必须一致。
const workshopJobCols = `guid, uid, due_ts, remaining_s, tick_ts, fields_json,
	durability, silver_paid, parts, created_ts, store_id`

// workshopJobScanner 抽出 *sql.Row / *sql.Rows 共有的 Scan，省一份重复的列序。
type workshopJobScanner interface {
	Scan(dest ...any) error
}

func scanWorkshopRepairJob(sc workshopJobScanner) (*WorkshopRepairJob, error) {
	j := &WorkshopRepairJob{}
	if err := sc.Scan(&j.GUID, &j.UID, &j.DueTS, &j.RemainingS, &j.TickTS,
		&j.FieldsJSON, &j.Durability, &j.SilverPaid, &j.Parts, &j.CreatedTS,
		&j.StoreID); err != nil {
		return nil, err
	}
	return j, nil
}

// UpsertWorkshopRepairJobTx 在事务内登记（或覆盖）一件在办修理。
//
// 用 UPSERT 而不是 INSERT：客户端会连发两次相同 21/3（同 GUID 同件，间隔 17ms），
// 两次都必须落在同一行上，否则会排两次队、第二次覆盖第一次的到期时间。
func UpsertWorkshopRepairJobTx(ctx context.Context, tx *sql.Tx, j WorkshopRepairJob) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO workshop_repair_job(guid, uid, due_ts, remaining_s, tick_ts,
		                                fields_json, durability, silver_paid, parts, created_ts,
		                                store_id)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(guid) DO UPDATE SET
			due_ts=excluded.due_ts, remaining_s=excluded.remaining_s,
			tick_ts=excluded.tick_ts, fields_json=excluded.fields_json,
			durability=excluded.durability, silver_paid=excluded.silver_paid,
			parts=excluded.parts, created_ts=excluded.created_ts,
			store_id=excluded.store_id`,
		j.GUID, j.UID, j.DueTS, j.RemainingS, j.TickTS, j.FieldsJSON, j.Durability,
		j.SilverPaid, j.Parts, j.CreatedTS, j.StoreID)
	return err
}

// WorkshopRepairJobFor 读取某件物品在办的任务；没有任务返回 (nil, nil)。
func (d *DB) WorkshopRepairJobFor(ctx context.Context, guid string) (*WorkshopRepairJob, error) {
	row := d.SQL.QueryRowContext(ctx,
		`SELECT `+workshopJobCols+` FROM workshop_repair_job WHERE guid=?`, guid)
	j, err := scanWorkshopRepairJob(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return j, nil
}

// PendingWorkshopRepairJobs 返回该玩家**仍在等**（remaining_s>0）的在办任务，
// 按物品 guid 索引。
//
// 为什么不复用 DueWorkshopRepairJobs：那个是"到期该落地"的判定入口，这里要的
// 正好相反——只有还在等的物品才该画锁和倒计时。已完工但尚未结算（remaining_s
// 归零、快照还没写回）的行故意不返回：倒计时归零那一刻锁就该消失，之后再显示
// 一个 0:00 的"修理中"是错的表现。
//
// 一次查询取该玩家的全部任务，供物品树组装做 map 命中，避免每件物品各查一次。
func (d *DB) PendingWorkshopRepairJobs(ctx context.Context, uid int64) (map[string]*WorkshopRepairJob, error) {
	rows, err := d.SQL.QueryContext(ctx,
		`SELECT `+workshopJobCols+` FROM workshop_repair_job
		  WHERE uid=? AND remaining_s>0`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]*WorkshopRepairJob)
	for rows.Next() {
		j, serr := scanWorkshopRepairJob(rows)
		if serr != nil {
			return nil, serr
		}
		out[j.GUID] = j
	}
	return out, rows.Err()
}

// DueWorkshopRepairJobs 返回该玩家所有已完工（remaining_s<=0）的在办任务。
func (d *DB) DueWorkshopRepairJobs(ctx context.Context, uid int64) ([]WorkshopRepairJob, error) {
	rows, err := d.SQL.QueryContext(ctx,
		`SELECT `+workshopJobCols+` FROM workshop_repair_job
		  WHERE uid=? AND remaining_s<=0 ORDER BY created_ts`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkshopRepairJob
	for rows.Next() {
		j, serr := scanWorkshopRepairJob(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// TickWorkshopRepairJobs 给该玩家的在办任务扣一次**在线**工时。
//
// 只在玩家在线时调用（后台扫描按在线名单逐个来、打开工坊时也会补一次）。
// 扣减量 = now - tick_ts，所以调用方必须保证"离线期间不调"——一调就会把
// 离线时段算成在线。玩家上线时先用 ResetWorkshopRepairTick 把 tick_ts 拨到
// 当前时刻，才不会把下线那段补扣进去。
//
// 一条 UPDATE 搞定所有行：SQLite 的 SET 右侧都取原值，所以 remaining_s 和
// due_ts 用同一个 delta 计算不会互相污染。
func (d *DB) TickWorkshopRepairJobs(ctx context.Context, uid int64, now float64) error {
	_, err := d.SQL.ExecContext(ctx, `
		UPDATE workshop_repair_job
		   SET remaining_s = MAX(0, remaining_s -
		         CASE WHEN tick_ts>0 THEN MAX(0, ?-tick_ts) ELSE 0 END),
		       due_ts = ? + MAX(0, remaining_s -
		         CASE WHEN tick_ts>0 THEN MAX(0, ?-tick_ts) ELSE 0 END),
		       tick_ts = ?
		 WHERE uid=? AND remaining_s>0`,
		now, now, now, now, uid)
	return err
}

// ResetWorkshopRepairTick 把该玩家在办任务的计时基准拨到 now，**不扣任何时间**。
//
// 登录时调用：玩家离线那段必须冻结，所以不能按 now-tick_ts 补扣，只能重置。
// 代价是"掉线到重登之间最后不到 5 秒的在线时长"会丢，这是冻结语义的必然，
// 也正好是官服的表现（下线即停表）。
func (d *DB) ResetWorkshopRepairTick(ctx context.Context, uid int64, now float64) error {
	_, err := d.SQL.ExecContext(ctx, `
		UPDATE workshop_repair_job SET tick_ts=?, due_ts=?+remaining_s
		 WHERE uid=?`, now, now, uid)
	return err
}

// DeleteWorkshopRepairJobTx 在事务内删除任务（应用完快照后调用）。
func DeleteWorkshopRepairJobTx(ctx context.Context, tx *sql.Tx, guid string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM workshop_repair_job WHERE guid=?`, guid)
	return err
}

// ---- 命名鱼 ----

// NamedFishIsNamed 报告该鱼是否已被命名。
func (d *DB) NamedFishIsNamed(ctx context.Context, fishID string) bool {
	if fishID == "" {
		return false
	}
	nf, err := d.NamedFishGet(ctx, fishID)
	return err == nil && nf != nil
}

// NamedFishNoteRecatch 将被命名鱼的钓获次数加一。
func (d *DB) NamedFishNoteRecatch(ctx context.Context, fishID string) {
	if fishID == "" {
		return
	}
	_, _ = d.SQL.ExecContext(ctx,
		`UPDATE named_fish SET catches = catches + 1 WHERE fish_id=?`, fishID)
}
