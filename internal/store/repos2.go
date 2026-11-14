package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"
)

// ---- 技能 ----

// maxSkillPoints 是单个技能大类的熟练度上限。
const maxSkillPoints = 100.0

// QuantizeSkillPoints 把熟练度对齐到 0.1 显示格。官服增量只有 0.1/0.2，
// 客户端 UI 也按 0.1 渲染；浮点累加（SQLite REAL / Go float64）会攒出
// 99.99999999999996 这类尾差——显示为 100% 却仍小于上限，钳位后产生
// 3.6e-14 的正残渣增量，客户端渲染出「+0 = 100%」技能行（2026-11-11 实测）。
//
// 四舍五入方向的安全性前提：熟练度增量恒为 0.1/0.2，且 profCap 减去量化后
// 的旧值仍是 0.1 的整数倍，正常路径下本函数是恒等变换，不改变任何值。
// 若未来引入非 0.1 网格的增量或修改 profCap（100.0），四舍五入最多会向上
// 多给 0.05——届时需改为截断（math.Floor）并重新评估显示语义。
func QuantizeSkillPoints(v float64) float64 {
	return math.Round(v*10) / 10
}

// ListSkills 取玩家技能熟练度。
func (d *DB) ListSkills(ctx context.Context, uid int64) (map[int64]float64, error) {
	rows, err := d.SQL.QueryContext(ctx, `SELECT skill_id, points FROM skill WHERE uid=?`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]float64{}
	for rows.Next() {
		var id int64
		var pts float64
		if err := rows.Scan(&id, &pts); err != nil {
			return nil, err
		}
		if pts > maxSkillPoints {
			pts = maxSkillPoints
		}
		out[id] = pts
	}
	return out, rows.Err()
}

// AddSkillPoints 增加熟练度（UPSERT 累加，0.1 显示格量化防浮点尾差）。
func (d *DB) AddSkillPoints(ctx context.Context, uid, skillID int64, delta float64) error {
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO skill(uid, skill_id, points) VALUES(?,?,MIN(ROUND(?*10)/10,?))
		ON CONFLICT (uid, skill_id) DO UPDATE
		SET points = MIN(ROUND((points + excluded.points)*10)/10, ?)`,
		uid, skillID, delta, maxSkillPoints, maxSkillPoints)
	return err
}

// GetSkillPointsTx 在事务内读取技能大类熟练度；无记录时返回 0。
func (d *DB) GetSkillPointsTx(ctx context.Context, tx *sql.Tx, uid, skillID int64) (float64, error) {
	var pts float64
	err := tx.QueryRowContext(ctx,
		`SELECT MIN(points, ?) FROM skill WHERE uid=? AND skill_id=?`,
		maxSkillPoints, uid, skillID).Scan(&pts)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return pts, err
}

// AddSkillPointsTx 在事务内增加熟练度，供“扣物 + 加熟练度”原子提交使用。
func (d *DB) AddSkillPointsTx(ctx context.Context, tx *sql.Tx, uid, skillID int64, delta float64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO skill(uid, skill_id, points) VALUES(?,?,MIN(ROUND(?*10)/10,?))
		ON CONFLICT (uid, skill_id) DO UPDATE
		SET points = MIN(ROUND((points + excluded.points)*10)/10, ?)`,
		uid, skillID, delta, maxSkillPoints, maxSkillPoints)
	return err
}

// ListSkillNodes 取玩家技能节点等级，并把共享身份投影到组内全部节点。
func (d *DB) ListSkillNodes(ctx context.Context, uid int64) (map[int64]int64, error) {
	out, err := d.listRawSkillNodes(ctx, uid)
	if err != nil {
		return nil, err
	}
	ident, err := d.LoadSkillIdentity(ctx)
	if err != nil {
		return nil, err
	}
	return ident.Project(out), nil
}

func (d *DB) listRawSkillNodes(ctx context.Context, uid int64) (map[int64]int64, error) {
	rows, err := d.SQL.QueryContext(ctx, `SELECT node_id, level FROM skill_node WHERE uid=?`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var n, lv int64
		if err := rows.Scan(&n, &lv); err != nil {
			return nil, err
		}
		out[n] = lv
	}
	return out, rows.Err()
}

// ---- 快捷栏 container_slot ----

// Shortcut 快捷栏绑定。
type Shortcut struct {
	Slot int64
	GUID string
}

// ListShortcuts 取绑定（container=0）。
func (d *DB) ListShortcuts(ctx context.Context, uid int64) ([]Shortcut, error) {
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	bindings, err := loadShortcutBindings(ctx, tx, uid)
	if err != nil {
		return nil, err
	}
	return sortedShortcuts(bindings), nil
}

// BindShortcut 绑定/解绑（guid 空串=解绑）。
func (d *DB) BindShortcut(ctx context.Context, uid, slot int64, guid string) error {
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	bindings, err := loadShortcutBindings(ctx, tx, uid)
	if err != nil {
		return err
	}
	if guid == "" {
		delete(bindings, slot)
	} else {
		bindings[slot] = guid
	}
	if err := SaveShortcutsTx(ctx, tx, uid, bindings); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- 鱼护 keepnet ----

// KeepnetEntry 鱼护条目。
type KeepnetEntry struct {
	EchoGUID  string  `json:"echo_guid"`
	UID       int64   `json:"uid"`
	Species   string  `json:"species"`
	NameZh    string  `json:"name_zh"`
	WeightG   int64   `json:"weight_g"`
	LengthM   float64 `json:"length_m"`
	Cat       int64   `json:"cat"`
	KeepLoc   string  `json:"keep_loc"`
	KeepSpot  int64   `json:"keep_spot"`
	KeepBait  string  `json:"keep_bait"`
	KeepLevel string  `json:"keep_level"`
	TS        float64 `json:"ts"`
	// ModelVariant 这尾鱼在客户端用的 FishFamily 族名（a_salmon_f2 等）；
	// 空=用鱼种默认族。不入库：每次序列化按（鱼种、体重、体长、GUID）重算，
	// 同一尾鱼在任何界面都得到同一个族名。
	ModelVariant string `json:"-"`
	// TackleCat 是钓法大类快照（1=浮钓 2=路亚 3=底钓 11=海钓，0=未知），
	// 入护时随 catch_log 落库，供 321 统计按钓法分族。
	TackleCat int64 `json:"tackle_cat"`
}

// KeepnetList 全量条目（按时间升序）。
func (d *DB) KeepnetList(ctx context.Context, uid int64) ([]*KeepnetEntry, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT echo_guid, uid, COALESCE(species,''), COALESCE(name_zh,''), weight_g, length_m,
		       cat, COALESCE(keep_loc,''), COALESCE(keep_spot,0), COALESCE(keep_bait,''),
		       COALESCE(keep_level,''), COALESCE(ts,0)
		FROM keepnet_items WHERE uid=? ORDER BY ts`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*KeepnetEntry
	for rows.Next() {
		e := &KeepnetEntry{}
		if err := rows.Scan(&e.EchoGUID, &e.UID, &e.Species, &e.NameZh, &e.WeightG, &e.LengthM,
			&e.Cat, &e.KeepLoc, &e.KeepSpot, &e.KeepBait, &e.KeepLevel, &e.TS); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// KeepnetInsert 入护。
func (d *DB) KeepnetInsert(ctx context.Context, e *KeepnetEntry) error {
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO keepnet_items(echo_guid, uid, species, name_zh, weight_g, length_m, cat,
		                          keep_loc, keep_spot, keep_bait, keep_level, ts)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (echo_guid) DO NOTHING`,
		e.EchoGUID, e.UID, e.Species, e.NameZh, e.WeightG, e.LengthM, e.Cat,
		e.KeepLoc, e.KeepSpot, e.KeepBait, e.KeepLevel, e.TS)
	return err
}

// KeepnetListTx KeepnetList 的事务版：制作扣料需要在同一事务内
// "列出-匹配-消费"鱼护条目时使用（鱼护渔获当制作材料）。
func (d *DB) KeepnetListTx(ctx context.Context, tx *sql.Tx, uid int64) ([]*KeepnetEntry, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT echo_guid, uid, COALESCE(species,''), COALESCE(name_zh,''), weight_g, length_m,
		       cat, COALESCE(keep_loc,''), COALESCE(keep_spot,0), COALESCE(keep_bait,''),
		       COALESCE(keep_level,''), COALESCE(ts,0)
		FROM keepnet_items WHERE uid=? ORDER BY ts`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*KeepnetEntry
	for rows.Next() {
		e := &KeepnetEntry{}
		if err := rows.Scan(&e.EchoGUID, &e.UID, &e.Species, &e.NameZh, &e.WeightG, &e.LengthM,
			&e.Cat, &e.KeepLoc, &e.KeepSpot, &e.KeepBait, &e.KeepLevel, &e.TS); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// KeepnetGet 按 echo_guid 取。
func (d *DB) KeepnetGet(ctx context.Context, uid int64, echoGUID string) (*KeepnetEntry, error) {
	row := d.SQL.QueryRowContext(ctx, `
		SELECT echo_guid, uid, COALESCE(species,''), COALESCE(name_zh,''), weight_g, length_m,
		       cat, COALESCE(keep_loc,''), COALESCE(keep_spot,0), COALESCE(keep_bait,''),
		       COALESCE(keep_level,''), COALESCE(ts,0)
		FROM keepnet_items WHERE uid=? AND echo_guid=?`, uid, echoGUID)
	e := &KeepnetEntry{}
	err := row.Scan(&e.EchoGUID, &e.UID, &e.Species, &e.NameZh, &e.WeightG, &e.LengthM,
		&e.Cat, &e.KeepLoc, &e.KeepSpot, &e.KeepBait, &e.KeepLevel, &e.TS)
	if err != nil {
		return nil, err
	}
	return e, nil
}

// KeepnetGetTx KeepnetGet 的事务版：标本制作等需要在同一事务内
// "读取-校验-消费"鱼护条目时使用。
func (d *DB) KeepnetGetTx(ctx context.Context, tx *sql.Tx, uid int64, echoGUID string) (*KeepnetEntry, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT echo_guid, uid, COALESCE(species,''), COALESCE(name_zh,''), weight_g, length_m,
		       cat, COALESCE(keep_loc,''), COALESCE(keep_spot,0), COALESCE(keep_bait,''),
		       COALESCE(keep_level,''), COALESCE(ts,0)
		FROM keepnet_items WHERE uid=? AND echo_guid=?`, uid, echoGUID)
	e := &KeepnetEntry{}
	err := row.Scan(&e.EchoGUID, &e.UID, &e.Species, &e.NameZh, &e.WeightG, &e.LengthM,
		&e.Cat, &e.KeepLoc, &e.KeepSpot, &e.KeepBait, &e.KeepLevel, &e.TS)
	if err != nil {
		return nil, err
	}
	return e, nil
}

// KeepnetDelete 卖鱼出护。
func (d *DB) KeepnetDelete(ctx context.Context, uid int64, echoGUIDs []string) error {
	if len(echoGUIDs) == 0 {
		return nil
	}
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, guid := range echoGUIDs {
		if _, err := d.DeleteKeepnetTx(ctx, tx, uid, guid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---- 钓获日志 ----

// RareSpeciesCount 统计玩家钓到过 grade≥minGrade（重量稀有/超级稀有）的
// 鱼种数，按 fish_key 去重。面板等级旁星号与 24/19 玩家卡共用该口径
// （客户端两路同落 ifbfdldbana.gebeadddmlp）；统计页 3/2 的逐鱼种条数
// 是另一口径，不经此函数。
//
// catch_log 在 14/4 结算就落库（source='catch'），入护只抬成 'keep'，
// 放生保持 'catch' —— 所以「钓到过」包含放生的鱼，与稀有鱼奖座同步。
func (d *DB) RareSpeciesCount(ctx context.Context, uid, minGrade int64) (int64, error) {
	var n int64
	err := d.SQL.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT fish_key) FROM catch_log WHERE uid=? AND COALESCE(grade,0)>=?`, uid, minGrade).Scan(&n)
	return n, err
}

// CatchLogInsert 记录一次新增钓获；入护重试由 KeepFishingCatch 按鱼 GUID 去重。
// 钓法快照未知（0）时使用，正常钓获请用 CatchLogInsertTackle。
func (d *DB) CatchLogInsert(ctx context.Context, uid int64, loc string, spot int64, fishKey, nameZh string, weightG int64, lengthM float64, source, bait string, grade, xp, silver int64, locLevel string) error {
	return d.CatchLogInsertTackle(ctx, uid, loc, spot, fishKey, nameZh, weightG, lengthM,
		source, bait, grade, xp, silver, locLevel, 0)
}

// CatchLogInsertTackle 同上，并写入钓法大类快照（1=浮钓 2=路亚 3=底钓 11=海钓，0=未知）。
// 321 统计的浮钓/路亚/底钓纪录卡与竿/钓组聚合按该列分族，缺快照只能回退旧口径。
func (d *DB) CatchLogInsertTackle(ctx context.Context, uid int64, loc string, spot int64, fishKey, nameZh string, weightG int64, lengthM float64, source, bait string, grade, xp, silver int64, locLevel string, tackleCat int64) error {
	var identifier [16]byte
	if _, err := rand.Read(identifier[:]); err != nil {
		return err
	}
	// 保留亚秒精度，避免同秒事件排序退化；记录卡时间可精确到 100ns。
	ts := float64(time.Now().UnixNano()) / 1e9
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO catch_log(ts, loc, spot, fish_key, name_zh, weight_g, length_m, source,
		                      bait, grade, xp, silver, loc_level, uid, catch_guid, tackle_cat)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		ts, loc, spot, fishKey, nameZh, weightG, lengthM, source,
		bait, grade, xp, silver, locLevel, uid, fmt.Sprintf("%x", identifier), tackleCat)
	return err
}

// CatchLogMaxWeight 玩家某鱼种历史最大重量。
func (d *DB) CatchLogMaxWeight(ctx context.Context, uid int64, fishKey string) (int64, error) {
	var mw int64
	err := d.SQL.QueryRowContext(ctx, `SELECT COALESCE(MAX(weight_g),0) FROM catch_log WHERE uid=? AND fish_key=?`,
		uid, fishKey).Scan(&mw)
	return mw, err
}

// CatchLogTotals 统计（3/2 用）：总钓获/奖杯/超稀有。
func (d *DB) CatchLogTotals(ctx context.Context, uid int64) (total, trophy, superN int64, err error) {
	err = d.SQL.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       SUM(CASE WHEN grade>=3 THEN 1 ELSE 0 END),
		       SUM(CASE WHEN grade>=4 THEN 1 ELSE 0 END)
		FROM catch_log WHERE uid=? AND source='catch'`, uid).Scan(&total, &trophy, &superN)
	return
}

// ---- 地点买断 ----

// PlaceAccessList 玩家已买断地点（档案 307 f22 与 placeOwned 买断判断同源）。
// 只含 kind='buyout'：房屋走 HouseAccessList 进 f20（客户端据 f20 把已购
// 房屋显示为"进入"），f22 一律渲染成"永久买断"——混入房屋会让仅有房契的
// 玩家在地图上呈现买断态，也污染买断判断与地图免费准入（审查 P2）。
func (d *DB) PlaceAccessList(ctx context.Context, uid int64) ([]string, error) {
	rows, err := d.SQL.QueryContext(ctx, `SELECT place FROM place_access WHERE uid=? AND kind='buyout'`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// HouseAccessList 玩家已拥有房屋（place 存房屋 item_id，档案 307 f20）。
func (d *DB) HouseAccessList(ctx context.Context, uid int64) ([]string, error) {
	rows, err := d.SQL.QueryContext(ctx, `SELECT place FROM place_access WHERE uid=? AND kind='house'`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PlaceAccessInsert 记录买断。
func (d *DB) PlaceAccessInsert(ctx context.Context, uid int64, place, kind string) error {
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO place_access(uid, place, kind, ts) VALUES(?,?,?,?)
		ON CONFLICT (uid, place, kind) DO NOTHING`,
		uid, place, kind, float64(time.Now().Unix()))
	return err
}

// ---- 物品 CRUD ----

// InsertItem 新增物品行。
func (d *DB) InsertItem(ctx context.Context, it *Item) error {
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO item(guid, uid, item_id, numeric_id, wire_class, type_id, container, slot,
		                 rig_guid, qty, buy_price, fields_json, created_ts, durability, workmanship)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		it.GUID, it.UID, nullIfEmpty(it.ItemID), it.NumericID, it.WireClass, it.TypeID, it.Container, it.Slot,
		nullIfEmpty(it.RigGUID), it.Qty, it.BuyPrice, it.FieldsJSON, it.CreatedTS, it.Durability, it.Workmanship)
	return err
}

// UpdateItemWire 改写实例的 wire 类、类型号、字段快照和数量。
func (d *DB) UpdateItemWire(ctx context.Context, guid, wireClass string, typeID int64, fieldsJSON string, qty int64) error {
	_, err := d.SQL.ExecContext(ctx, `
		UPDATE item SET wire_class=?, type_id=?, fields_json=?, qty=?
		WHERE guid=?`, wireClass, typeID, fieldsJSON, qty, guid)
	return err
}

// UpdateItemFields 更新字段快照/数量/耐久/工艺/装配。
func (d *DB) UpdateItemFields(ctx context.Context, guid string, fieldsJSON string, qty int64, durability, workmanship float64, rigGUID string) error {
	_, err := d.SQL.ExecContext(ctx, `
		UPDATE item SET fields_json=?, qty=?, durability=?, workmanship=?, rig_guid=?
		WHERE guid=?`, fieldsJSON, qty, durability, workmanship, nullIfEmpty(rigGUID), guid)
	return err
}

// UpdateItemFieldsTx 在事务内更新字段快照、数量、耐久、工艺和装配。
func (d *DB) UpdateItemFieldsTx(ctx context.Context, tx *sql.Tx, guid string, fieldsJSON string, qty int64, durability, workmanship float64, rigGUID string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE item SET fields_json=?, qty=?, durability=?, workmanship=?, rig_guid=?
		WHERE guid=?`, fieldsJSON, qty, durability, workmanship, nullIfEmpty(rigGUID), guid)
	return err
}

// DeleteItems 删除物品。
func (d *DB) DeleteItems(ctx context.Context, guid []string) error {
	if len(guid) == 0 {
		return nil
	}
	q, args := placeholders(`DELETE FROM item WHERE guid IN (`, guid, ")")
	_, err := d.SQL.ExecContext(ctx, q, args...)
	return err
}

// CountItemsByContainers 统计某 uid 在指定容器集合的物品数。
func (d *DB) CountItemsByContainers(ctx context.Context, uid int64, containers []string) (int64, error) {
	var n int64
	q, args := placeholders(`SELECT COUNT(*) FROM item WHERE uid=? AND container IN (`, containers, ")")
	err := d.SQL.QueryRowContext(ctx, q, append([]interface{}{uid}, args...)...).Scan(&n)
	return n, err
}

// ItemGUIDsByContainers 指定容器集合的 guid 列表（按 slot/created 排序）。
func (d *DB) ItemGUIDsByContainers(ctx context.Context, uid int64, containers []string) ([]string, error) {
	q, args := placeholders(`
		SELECT guid FROM item WHERE uid=? AND container IN (`, containers, ") ORDER BY slot, created_ts, guid")
	rows, err := d.SQL.QueryContext(ctx, q, append([]interface{}{uid}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// placeholders 生成 "?,?,?" 占位串（SQLite 顺序参数）。
func placeholders(prefix string, vals []string, suffix string) (string, []interface{}) {
	marks := make([]string, len(vals))
	args := make([]interface{}, len(vals))
	for i, v := range vals {
		marks[i] = "?"
		args[i] = v
	}
	return prefix + strings.Join(marks, ",") + suffix, args
}

// ---- 静态定义 ----

// ItemDef 物品定义行。
type ItemDef struct {
	NumericID int64
	ItemID    string
	Category  int64
	Kind      string
	Brand     string
	AttrsJSON string
}

// GetItemDef 按 numeric_id 取定义。
func (d *DB) GetItemDef(ctx context.Context, numericID int64) (*ItemDef, error) {
	row := d.SQL.QueryRowContext(ctx, `
		SELECT numeric_id, COALESCE(item_id,''), COALESCE(category,0), COALESCE(kind,''),
		       COALESCE(brand,''), COALESCE(attrs_json,'')
		FROM item_def WHERE numeric_id=?`, numericID)
	def := &ItemDef{}
	if err := row.Scan(&def.NumericID, &def.ItemID, &def.Category, &def.Kind, &def.Brand, &def.AttrsJSON); err != nil {
		return nil, err
	}
	return def, nil
}

// ItemDefCategories 批量按 numeric_id 取品类（4/22 搏鱼推送热路径专用）。
// 树重建一次查询拿全量，调用方禁止逐对象单查；查询不到的 id 不在返回 map 里。
func (d *DB) ItemDefCategories(ctx context.Context, numericIDs []int64) (map[int64]int64, error) {
	out := make(map[int64]int64, len(numericIDs))
	if len(numericIDs) == 0 {
		return out, nil
	}
	const chunk = 500 // SQLite 变量上限余量
	for start := 0; start < len(numericIDs); start += chunk {
		end := start + chunk
		if end > len(numericIDs) {
			end = len(numericIDs)
		}
		part := numericIDs[start:end]
		marks := make([]string, len(part))
		qargs := make([]interface{}, len(part))
		for i, id := range part {
			marks[i] = "?"
			qargs[i] = id
		}
		rows, err := d.SQL.QueryContext(ctx,
			`SELECT numeric_id, COALESCE(category,0) FROM item_def WHERE numeric_id IN (`+
				strings.Join(marks, ",")+`)`, qargs...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, cat int64
			if err := rows.Scan(&id, &cat); err == nil {
				out[id] = cat
			}
		}
		rows.Close()
	}
	return out, nil
}

// GetItemDefByItemID 按 systemID 取定义。
func (d *DB) GetItemDefByItemID(ctx context.Context, itemID string) (*ItemDef, error) {
	row := d.SQL.QueryRowContext(ctx, `
		SELECT numeric_id, COALESCE(item_id,''), COALESCE(category,0), COALESCE(kind,''),
		       COALESCE(brand,''), COALESCE(attrs_json,'')
		FROM item_def WHERE item_id=?`, itemID)
	def := &ItemDef{}
	if err := row.Scan(&def.NumericID, &def.ItemID, &def.Category, &def.Kind, &def.Brand, &def.AttrsJSON); err != nil {
		return nil, err
	}
	return def, nil
}

// ListItemDefsByCategory 按品类读取全部物品定义。
// 引线制作需要用普通主线的物理参数匹配对应的前导线定义。
func (d *DB) ListItemDefsByCategory(ctx context.Context, category int64) ([]*ItemDef, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT numeric_id, COALESCE(item_id,''), COALESCE(category,0), COALESCE(kind,''),
		       COALESCE(brand,''), COALESCE(attrs_json,'')
		FROM item_def WHERE category=? ORDER BY numeric_id`, category)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*ItemDef
	for rows.Next() {
		def := &ItemDef{}
		if err := rows.Scan(&def.NumericID, &def.ItemID, &def.Category, &def.Kind,
			&def.Brand, &def.AttrsJSON); err != nil {
			return nil, err
		}
		out = append(out, def)
	}
	return out, rows.Err()
}

// GetItemByGUID 取单个物品行。
func (d *DB) GetItemByGUID(ctx context.Context, guid string) (*Item, error) {
	row := d.SQL.QueryRowContext(ctx, `SELECT `+itemCols+` FROM item WHERE guid=?`, guid)
	return scanItem(row)
}

// GetItemByGUIDTx 在事务内读取单个物品行。
func (d *DB) GetItemByGUIDTx(ctx context.Context, tx *sql.Tx, guid string) (*Item, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+itemCols+` FROM item WHERE guid=?`, guid)
	return scanItem(row)
}

// GetItemGUIDByRigSlot 查钓组槽位装的物品 guid（rig_component）。
func (d *DB) GetItemGUIDByRigSlot(ctx context.Context, rigGUID string, slot int64) (string, error) {
	var g string
	err := d.SQL.QueryRowContext(ctx, `
		SELECT COALESCE(guid,'') FROM rig_component
		WHERE rig_guid=? AND slot=? ORDER BY ordinal LIMIT 1`, rigGUID, slot).Scan(&g)
	if err != nil {
		return "", err
	}
	return g, nil
}

// UpsertRig 幂等建档钓组。
func (d *DB) UpsertRig(ctx context.Context, guid string, uid, rigType int64, name, fieldsJSON string) error {
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO rig(guid, uid, rig_type, name, fields_json, created_ts)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT (guid) DO NOTHING`,
		guid, uid, rigType, name, fieldsJSON, float64(time.Now().Unix()))
	return err
}

// UpsertRigComponent 幂等登记组件。
func (d *DB) UpsertRigComponent(ctx context.Context, rigGUID string, slot int64, guid string, extra int64) error {
	return d.UpsertRigComponentAuto(ctx, rigGUID, slot, guid, extra)
}

// ListRigComponents 取钓组组件（按 extra 升序）。
type RigComponent struct {
	Slot    int64
	Ordinal int64
	GUID    string
	Extra   int64
}

func (d *DB) ListRigComponents(ctx context.Context, rigGUID string) ([]*RigComponent, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT slot, ordinal, COALESCE(guid,''), extra FROM rig_component
		WHERE rig_guid=? ORDER BY extra, slot, ordinal, guid`,
		rigGUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RigComponent
	for rows.Next() {
		rc := &RigComponent{}
		if err := rows.Scan(&rc.Slot, &rc.Ordinal, &rc.GUID, &rc.Extra); err != nil {
			return nil, err
		}
		out = append(out, rc)
	}
	return out, rows.Err()
}

// UpsertRodSetting 竿设置持久记录（quivertip/baitmount；17/5 双写之一）。
func (d *DB) UpsertRodSetting(ctx context.Context, rodGUID, key, value string) error {
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO rod_setting(guid, key, value, updated_ts) VALUES(?,?,?,?)
		ON CONFLICT (guid, key) DO UPDATE SET value=?, updated_ts=?`,
		rodGUID, key, value, float64(time.Now().Unix()), value, float64(time.Now().Unix()))
	return err
}

// ---- 商店/价目 ----

// ShopPrice 货架价（分）。
type ShopPrice struct {
	ItemID string
	Silver int64
	Gold   int64
	Stock  int64
}

// ShopItems 返回某店最终生效货架：优先精确门店数据，其次同 kind/品牌，
// 再使用同类代表货架，最后按 store_id 应用后台覆盖。未知店或缺少货架定义时
// 返回空货架，不使用全局价目伪造商品集合。
func (d *DB) ShopItems(ctx context.Context, storeID string) ([]*ShopPrice, error) {
	shelf, err := d.effectiveShopShelf(ctx, storeID)
	if err != nil {
		return nil, err
	}
	return shelf.Effective, nil
}

// ShopItemGet 单 SKU 的最终价格/库存；商品不在该店生效货架时返回 sql.ErrNoRows。
func (d *DB) ShopItemGet(ctx context.Context, storeID, itemID string) (*ShopPrice, error) {
	shelf, err := d.effectiveShopShelf(ctx, storeID)
	if err != nil {
		return nil, err
	}
	itemID = strings.TrimSpace(itemID)
	for _, p := range shelf.Effective {
		if p.ItemID == itemID {
			v := *p
			return &v, nil
		}
	}
	return nil, sql.ErrNoRows
}

// ItemPriceGet 全局价目兜底。
func (d *DB) ItemPriceGet(ctx context.Context, itemID string) (*ShopPrice, error) {
	row := d.SQL.QueryRowContext(ctx, `
		SELECT item_id, silver, gold, stock FROM item_price WHERE item_id=?`, itemID)
	p := &ShopPrice{}
	if err := row.Scan(&p.ItemID, &p.Silver, &p.Gold, &p.Stock); err != nil {
		return nil, err
	}
	return p, nil
}

// CraftIngredientSilverPrice 贩卖机配料的银币单价。
// 先取全部基础货架（shop_item）与后台覆盖价（shop_item_override）的最低
// 非零银价——贩卖机配方（9/1/9/4）没有门店上下文，用全货架最低价保证
// "机器做不会比去店里买贵"；仍无价时（自制窝料的添加槽可选到未上架的
// 成品饵，如 fplt_op_ccp7 只有金币价目）按商店货架补齐同款口径估价：
// 同类同级最贵 → 同类型中位价 → 同品类中位价 → 1000，再按 item_id 稳定
// 上浮 0~10%（与 augmentShopShelf 同公式，同一商品估价恒定）。全链路
// 无价才返回 sql.ErrNoRows，由调用方决定整体失败。
func (d *DB) CraftIngredientSilverPrice(ctx context.Context, itemID string) (int64, error) {
	row := d.SQL.QueryRowContext(ctx, `
		SELECT COALESCE(MIN(s), 0) FROM (
			SELECT silver AS s FROM shop_item WHERE item_id=? AND silver>0
			UNION ALL
			SELECT silver AS s FROM shop_item_override WHERE item_id=? AND silver IS NOT NULL AND silver>0
		)`, itemID, itemID)
	var price int64
	if err := row.Scan(&price); err != nil {
		return 0, err
	}
	if price > 0 {
		return price, nil
	}
	if err := d.loadShopSupplement(ctx); err != nil {
		return 0, err
	}
	m, ok := d.shopSupplement.metas[itemID]
	if !ok {
		return 0, sql.ErrNoRows
	}
	est := d.shopSupplement.seriesMax[shopSeriesKey(m.Kind, m.Name)]
	if est <= 0 {
		est = d.shopSupplement.medianByKind[m.Kind]
	}
	if est <= 0 {
		est = d.shopSupplement.medianByCat[m.Category]
	}
	if est <= 0 {
		est = 1000
	}
	return est + est*int64(stableHash(itemID)%11)/100, nil
}

// ItemPriceFallbackList 价目兜底货架（非空价前 N 件）。
func (d *DB) ItemPriceFallbackList(ctx context.Context, limit int64) ([]*ShopPrice, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT item_id, silver, gold, stock FROM item_price
		WHERE silver>0 OR gold>0 LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ShopPrice
	for rows.Next() {
		p := &ShopPrice{}
		if err := rows.Scan(&p.ItemID, &p.Silver, &p.Gold, &p.Stock); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- 被命名鱼 ----

// NamedFish 命名鱼行。
type NamedFish struct {
	FishID  string
	Species string
	Name    string
	Owner   string
	Place   string
	WeightG int64
	LengthM float64
	Grade   int64
	TS      float64
	Catches int64
}

func (d *DB) NamedFishGet(ctx context.Context, fishID string) (*NamedFish, error) {
	row := d.SQL.QueryRowContext(ctx, `
		SELECT fish_id, species, name, COALESCE(owner,''), COALESCE(place,''),
		       COALESCE(weight_g,0), COALESCE(length_m,0), COALESCE(grade,0), COALESCE(ts,0), catches
		FROM named_fish WHERE fish_id=?`, fishID)
	nf := &NamedFish{}
	if err := row.Scan(&nf.FishID, &nf.Species, &nf.Name, &nf.Owner, &nf.Place,
		&nf.WeightG, &nf.LengthM, &nf.Grade, &nf.TS, &nf.Catches); err != nil {
		return nil, err
	}
	return nf, nil
}

// NamedFishInsert 首次命名。并发下只有第一个 INSERT 成功；冲突必须返回错误，
// 不能把 ON CONFLICT DO NOTHING 的 0 行影响误报成命名成功。
func (d *DB) NamedFishInsert(ctx context.Context, nf *NamedFish) error {
	res, err := d.SQL.ExecContext(ctx, `
		INSERT INTO named_fish(fish_id, species, name, owner, place, weight_g, length_m, grade, ts, catches)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (fish_id) DO NOTHING`,
		nf.FishID, nf.Species, nf.Name, nf.Owner, nf.Place, nf.WeightG, nf.LengthM, nf.Grade, nf.TS, nf.Catches)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("named fish %s already exists", nf.FishID)
	}
	return nil
}

// ---- 运行参数 / KV / 队列（跨进程共享）----

// TunableGet / TunableSet 运行参数（管理后台热更）。
func (d *DB) TunableGet(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := d.SQL.QueryRowContext(ctx, `SELECT value FROM tunables WHERE key=?`, key).Scan(&v)
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// TunableSet 写参数。
func (d *DB) TunableSet(ctx context.Context, key, value, updatedBy string) error {
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO tunables(key, value, updated_by, updated_ts) VALUES(?,?,?,?)
		ON CONFLICT (key) DO UPDATE SET value=?, updated_by=?, updated_ts=?`,
		key, value, updatedBy, float64(time.Now().Unix()),
		value, updatedBy, float64(time.Now().Unix()))
	return err
}

// SpawnModeSet 原子写入发鱼模式三开关：grade_ratio_enabled、fish_hunt_enabled、
// two_stage_hunt_enabled。三种模式互斥，事务保证不会出现中间态。
func (d *DB) SpawnModeSet(ctx context.Context, mode, updatedBy string) error {
	values := map[string]string{
		"grade_ratio_enabled":    "0",
		"fish_hunt_enabled":      "0",
		"two_stage_hunt_enabled": "0",
	}
	switch mode {
	case "grade_ratio":
		values["grade_ratio_enabled"] = "1"
	case "fish_hunt":
		values["fish_hunt_enabled"] = "1"
	case "two_stage":
		values["two_stage_hunt_enabled"] = "1"
	default:
		return fmt.Errorf("未知发鱼模式: %s", mode)
	}
	now := float64(time.Now().Unix())
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, key := range []string{"grade_ratio_enabled", "fish_hunt_enabled", "two_stage_hunt_enabled"} {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO tunables(key, value, updated_by, updated_ts) VALUES(?,?,?,?)
			ON CONFLICT (key) DO UPDATE SET value=?, updated_by=?, updated_ts=?`,
			key, values[key], updatedBy, now,
			values[key], updatedBy, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// biteModeKeys 是「鱼汛模式」预设统管的五个开关。与发鱼模式不同，
// 五个开关也可以在鱼群规则条里逐个手调（那是合法的 mixed 状态）。
var biteModeKeys = []string{
	"bite_gate_enabled", "fish_ecology_enabled", "fish_weather_enabled",
	"fish_bait_rules_enabled", "fish_distribution_enabled",
}

// BiteModeFor 由当前 tunables 推断鱼汛模式：realistic=五开、legacy=五关，
// 其余为 mixed（管理员手动改过单个开关）。
func BiteModeFor(t map[string]string) string {
	all, none := true, true
	for _, key := range biteModeKeys {
		if t[key] == "1" {
			none = false
		} else {
			all = false
		}
	}
	switch {
	case all:
		return "realistic"
	case none:
		return "legacy"
	}
	return "mixed"
}

// BiteModeSet 原子切换鱼汛模式：realistic=五开，legacy=五关。
func (d *DB) BiteModeSet(ctx context.Context, mode, updatedBy string) error {
	value := "0"
	switch mode {
	case "realistic":
		value = "1"
	case "legacy":
	default:
		return fmt.Errorf("未知鱼汛模式: %s", mode)
	}
	now := float64(time.Now().Unix())
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, key := range biteModeKeys {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO tunables(key, value, updated_by, updated_ts) VALUES(?,?,?,?)
			ON CONFLICT (key) DO UPDATE SET value=?, updated_by=?, updated_ts=?`,
			key, value, updatedBy, now,
			value, updatedBy, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TunableList 全量参数。
func (d *DB) TunableList(ctx context.Context) (map[string]string, error) {
	rows, err := d.SQL.QueryContext(ctx, `SELECT key, value FROM tunables ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// KVGet / KVSet 共享键值（在线会话统计等）。
func (d *DB) KVGet(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := d.SQL.QueryRowContext(ctx, `SELECT v FROM kv WHERE k=?`, key).Scan(&v)
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// KVSet 写共享键值。
func (d *DB) KVSet(ctx context.Context, key, value string) error {
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO kv(k, v) VALUES(?,?) ON CONFLICT (k) DO UPDATE SET v=?`,
		key, value, value)
	return err
}

// QueuePush 管理指令入队（admin 进程投递）。
func (d *DB) QueuePush(ctx context.Context, payload string) error {
	_, err := d.SQL.ExecContext(ctx,
		`INSERT INTO admin_queue(payload, created_ts) VALUES(?,?)`,
		payload, float64(time.Now().Unix()))
	return err
}

// QueuePop 取最旧一条指令并删除（gameserver 轮询消费）；空队列返回 ok=false。
func (d *DB) QueuePop(ctx context.Context) (string, bool, error) {
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback() }()
	var id int64
	var payload string
	err = tx.QueryRowContext(ctx,
		`SELECT id, payload FROM admin_queue ORDER BY id LIMIT 1`).Scan(&id, &payload)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM admin_queue WHERE id=?`, id); err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return payload, true, nil
}

var _ = fmt.Sprintf

// RemoveRigComponent 移除钓组某槽位组件行。
func (d *DB) RemoveRigComponent(ctx context.Context, rigGUID string, slot int64) error {
	return d.DeleteRigComponentsBySlot(ctx, rigGUID, slot)
}

// UpdateItemContainer 改物品容器归属（backpack/rig/keepnet）。
func (d *DB) UpdateItemContainer(ctx context.Context, guid, container string) error {
	_, err := d.SQL.ExecContext(ctx,
		`UPDATE item SET container=? WHERE guid=?`, container, guid)
	return err
}

// NextFreeSlot 取某容器内下一个可用槽位（空容器从 0 开始）。
// 新建物品默认 slot=0，多件同槽时客户端只会显示一件，批量发放必须逐件分配。
func (d *DB) NextFreeSlot(ctx context.Context, uid int64, container string) (int64, error) {
	var next int64
	err := d.SQL.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(slot),-1)+1 FROM item WHERE uid=? AND container=?`,
		uid, container).Scan(&next)
	if err != nil {
		return 0, err
	}
	return next, nil
}

// SetItemSlot 更新物品在容器内的槽位。
func (d *DB) SetItemSlot(ctx context.Context, guid string, slot int64) error {
	_, err := d.SQL.ExecContext(ctx, `UPDATE item SET slot=? WHERE guid=?`, slot, guid)
	return err
}

// ---- 钓组装配 ----

// GetRigType 读 rig 表行的 rig_type（行不存在返回 (0,false)）。
func (d *DB) GetRigType(ctx context.Context, guid string) (int64, bool, error) {
	var rt int64
	err := d.SQL.QueryRowContext(ctx,
		`SELECT rig_type FROM rig WHERE guid=?`, guid).Scan(&rt)
	if err != nil {
		return 0, false, err
	}
	return rt, true, nil
}

// UpdateRigType 更新 rig 表的钓组类型；行不存在时无操作，由 UpsertRig 建档。
func (d *DB) UpdateRigType(ctx context.Context, guid string, rigType int64) error {
	_, err := d.SQL.ExecContext(ctx,
		`UPDATE rig SET rig_type=? WHERE guid=?`, rigType, guid)
	return err
}

// SetItemRigLink 将物品挂到钓组，一次写入 rig_guid、slot 和 container。
// rig_component 行由 UpsertRigComponent 单独维护。
func (d *DB) SetItemRigLink(ctx context.Context, guid, rigGUID string, slot int64) error {
	_, err := d.SQL.ExecContext(ctx,
		`UPDATE item SET rig_guid=?, slot=?, container='rig' WHERE guid=?`, rigGUID, slot, guid)
	return err
}

// SetItemBackpack 将物品退回背包：清空 rig_guid，并将 container 设为 backpack。
// 物品字段中的父引用由调用方另行清理。
func (d *DB) SetItemBackpack(ctx context.Context, guid string) error {
	_, err := d.SQL.ExecContext(ctx,
		`UPDATE item SET rig_guid=NULL, container='backpack' WHERE guid=?`, guid)
	return err
}
