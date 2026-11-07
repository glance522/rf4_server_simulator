package gameserver

// 窝料吸引系统 v1.1（specs/changes/groundbait-system/spec.md）。
//
// 一句话：往一个 5 米格里投窝料，这份料按「1 份 = 0.6 游戏小时」活着，
// 期间按鱼种食性把对口鱼的候选权重抬起来（最多 x2.60），并且能稍微顶掉
// 坏天气/低温的惩罚；投超过 15 份开始压开口率，22 份以上这个格子基本不开口。
// 掠食性鱼种一律 x1.00，不蹭窝。
//
// 食性亲和（契合度/专食性/掠食冻结）在运行时按实际用料现算：原料表 → 食性向量
// → 逐鱼种契合度，公式与离线探针同源（见 groundbait_profile.go）。这样自制窝料
// 才会真的跟着配方变：红虫蛆蛹的冬配抬动物口，玉米蜂蜜的夏配抬植物口。
// 随包发布的逐鱼种亲和表退成兜底，只在攻略吃饵表读不出来这类降级状态下才用。

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

//go:embed assets/groundbait_item_def.json
var groundbaitItemDefJSON []byte

//go:embed assets/groundbait_affinity.json
var groundbaitAffinityJSON []byte

const (
	// groundbaitCellSizeM 窝点格子边长（米）。落点取 5 米桶，相邻两窝互不影响。
	groundbaitCellSizeM = 5.0
	// groundbaitRealSecPerGameHour 1 游戏小时 = 150 真实秒。两条独立证据：
	// spawn.go 的 realSecPerGameHour=150，world_clock.go 的 24 游戏小时/真实小时。
	groundbaitRealSecPerGameHour = 150.0
	// groundbaitMaxTickRealSec 单次结算最多推进的真实秒数。判鱼/投放都会碰到
	// 游戏钟，正常间隔只有几秒；跨度超过这里说明是登录或掉线后的第一次结算，
	// 这段离线时间不该扣窝时。
	groundbaitMaxTickRealSec = 30.0
	// groundbaitSatExponent 契合饱和曲线的指数（spec 中的食性亲和公式）。
	groundbaitSatExponent = 1.35
	// catGroundbaitMix 品类 46：feed_* 与自制窝料都在这一类，实例按份数计数。
	catGroundbaitMix = int64(46)
	// catPVAStick 品类 30：PVA 棒/可溶袋，同样按份数消耗。
	catPVAStick = int64(30)
)

type groundbaitItemDef struct {
	ItemID     string             `json:"item_id"`
	Diet       map[string]float64 `json:"diet"`
	Potency    float64            `json:"potency"`
	Dispersion float64            `json:"dispersion"`
	Particle   float64            `json:"particle"`
	Scent      float64            `json:"scent"`
	Hold       float64            `json:"hold"`
	TargetFish string             `json:"target_fish_key"`
	Portions   float64            `json:"portions"`
	// Materials 是这款窝料的固定用料清单。具名商品的配方本来就不变，清单直接
	// 随资产发布；自制窝料没有自己的 item_id，用料按实例记在 groundbait_recipe。
	Materials []string `json:"materials"`
	// UnitGrams 一份窝料的单件克重，只用于物品实例的重量字段显示，
	// 不参与吸引判定（吸引用 potency/dispersion/hold 那一套）。
	UnitGrams float64 `json:"unit_grams"`
}

type groundbaitAffinityRow struct {
	ItemID   string  `json:"item_id"`
	FishKey  string  `json:"fish_key"`
	Appetite float64 `json:"appetite"`
	Affinity float64 `json:"affinity"`
	Special  float64 `json:"special"`
}

// groundbaitDefs 是离线探针产出的窝料档案与逐鱼种亲和表。
type groundbaitDefs struct {
	version int64
	source  string
	items   map[string]groundbaitItemDef
	// materials 是全部原料（基底/添加剂/香精）的食性与物理特性档案，
	// 用来把「实际投了什么」合成成食性向量。
	materials map[string]groundbaitMaterial
	// affinity: 物品 -> 鱼种 -> 亲和行。掠食者已被冻结，压根不在表里。
	affinity map[string]map[string]groundbaitAffinityRow
	badErr   string
}

var (
	groundbaitDefsOnce   sync.Once
	groundbaitDefsShared *groundbaitDefs
	// groundbaitClockMu 串行推进所有玩家的窝料在线钟，避免 keepalive 与
	// 投喂/咬钩请求并发做读改写时丢掉时间。
	groundbaitClockMu sync.Mutex
)

// groundbaitDefsLoad 解析包内资产。解析失败时返回空表（判定退化成「没有窝」），
// 不能因为资产坏了就把判鱼一起带崩。
func groundbaitDefsLoad() *groundbaitDefs {
	groundbaitDefsOnce.Do(func() {
		d := &groundbaitDefs{items: map[string]groundbaitItemDef{}, materials: map[string]groundbaitMaterial{},
			affinity: map[string]map[string]groundbaitAffinityRow{}}
		var itemsDoc struct {
			ProfileVersion int64               `json:"profile_version"`
			Source         string              `json:"source"`
			Items          []groundbaitItemDef `json:"items"`
		}
		if err := json.Unmarshal(groundbaitItemDefJSON, &itemsDoc); err != nil {
			d.badErr = "item_def: " + err.Error()
		}
		var affDoc struct {
			Rows []groundbaitAffinityRow `json:"rows"`
		}
		if err := json.Unmarshal(groundbaitAffinityJSON, &affDoc); err != nil {
			d.badErr = "affinity: " + err.Error()
		}
		var matDoc struct {
			Materials []groundbaitMaterial `json:"materials"`
		}
		if err := json.Unmarshal(groundbaitMaterialJSON, &matDoc); err != nil {
			d.badErr = "material: " + err.Error()
		}
		for _, m := range matDoc.Materials {
			if m.ItemID != "" {
				d.materials[m.ItemID] = m
			}
		}
		for _, it := range itemsDoc.Items {
			if it.ItemID != "" {
				d.items[it.ItemID] = it
			}
		}
		for _, row := range affDoc.Rows {
			if row.ItemID == "" || row.FishKey == "" {
				continue
			}
			byFish, ok := d.affinity[row.ItemID]
			if !ok {
				byFish = map[string]groundbaitAffinityRow{}
				d.affinity[row.ItemID] = byFish
			}
			byFish[row.FishKey] = row
		}
		d.version, d.source = itemsDoc.ProfileVersion, itemsDoc.Source
		groundbaitDefsShared = d
	})
	return groundbaitDefsShared
}

// groundbaitDefaultUnitGrams 是档案缺项时的兜底克重（一球 30 克）。
const groundbaitDefaultUnitGrams = 30.0

// groundbaitUnitGrams 返回一份窝料的单件克重。
//
// 原生 item_def 里 11 个窝料（品类 46）的 ijbjficbhog 全是 0 —— 实测连商店买的
// 「干混合」在客户端也显示「质量 0 克」，说明窝料克重是真实服务端**按实例**下发的，
// 不是定义带的。客户端 kItemDesc_Mass 读实例重量字段的 IEEE754 位模式、单位是克
// （2026-11-07 双值实测：写 30.0 显示「30 克」，写 30000.0 显示「30 公斤」）。
// 所以这里按窝料档案补值，档案缺项回落到一球 30 克。
func groundbaitUnitGrams(itemID string) float64 {
	if def, ok := groundbaitDefsLoad().itemDef(itemID); ok && def.UnitGrams > 0 {
		return def.UnitGrams
	}
	return groundbaitDefaultUnitGrams
}

// itemDef 取窝料档案；未知物品返回 ok=false，由调用方按中性档案处理。
func (d *groundbaitDefs) itemDef(itemID string) (groundbaitItemDef, bool) {
	it, ok := d.items[itemID]
	return it, ok
}

// affinityOf 取某款窝料对某鱼种的亲和行。掠食者不在表里，返回 false＝x1.00。
func (d *groundbaitDefs) affinityOf(itemID, fishKey string) (groundbaitAffinityRow, bool) {
	byFish, ok := d.affinity[itemID]
	if !ok {
		return groundbaitAffinityRow{}, false
	}
	row, ok := byFish[fishKey]
	return row, ok
}

func groundbaitClamp(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

// groundbaitEnabled 总开关。关掉之后判鱼流程与加这套系统之前完全一致。
func (s *Server) groundbaitEnabled() bool {
	return s.DB != nil && s.tunableF("groundbait_enabled", 0) == 1
}

// groundbaitAffinityValue 把食性契合度换算成饱和后的加成系数 A∈[0,1]。
// 低于饱和下限（默认 appetite 1.05）完全不加成，避免「一撒窝全员受益」。
func (s *Server) groundbaitAffinityValue(appetite float64) float64 {
	floor := s.tunableF("groundbait_sat_floor", 1.05)
	ceil := s.tunableF("groundbait_sat_cap", 2.05)
	if ceil <= floor {
		ceil = floor + 1
	}
	if appetite <= floor {
		return 0
	}
	return math.Pow(groundbaitClamp((appetite-floor)/(ceil-floor), 0, 1), groundbaitSatExponent)
}

// groundbaitCell 落点转 5 米网格桶。
func groundbaitCell(x, z float64) (int64, int64) {
	return int64(math.Floor(x / groundbaitCellSizeM)), int64(math.Floor(z / groundbaitCellSizeM))
}

// groundbaitCellFor 没拿到落点坐标时退化成「整个钓点一格」，只给中性窝效。
func groundbaitCellFor(x, z float64, hasPos bool, spot uint64) (int64, int64, int) {
	if hasPos {
		bx, bz := groundbaitCell(x, z)
		return bx, bz, 1
	}
	return int64(spot), -1, 0
}

// groundbaitGameHours 返回该玩家的在线游戏钟读数（游戏小时），并顺手推进它。
// 在线期间由会话 keepalive 持续调用；业务请求也会补一次。单次跨度封顶
// 30 秒，因此长期离线后的第一次调用最多只补算 30 秒。
func (s *Server) groundbaitGameHours(ctx context.Context, uid int64) float64 {
	if s.DB == nil || uid == 0 {
		return 0
	}
	groundbaitClockMu.Lock()
	defer groundbaitClockMu.Unlock()
	now := float64(time.Now().Unix())
	var realTS, acc float64
	err := s.DB.SQL.QueryRowContext(ctx, `SELECT real_ts, game_hours FROM groundbait_clock WHERE uid=?`, uid).Scan(&realTS, &acc)
	if errors.Is(err, sql.ErrNoRows) {
		if _, ierr := s.DB.SQL.ExecContext(ctx, `INSERT INTO groundbait_clock(uid,real_ts,game_hours,updated_ts)
			VALUES(?,?,?,?) ON CONFLICT(uid) DO UPDATE SET real_ts=excluded.real_ts, updated_ts=excluded.updated_ts`,
			uid, now, 0.0, now); ierr != nil {
			s.log.Warn("窝料游戏钟初始化失败", "err", ierr)
		}
		return 0
	}
	if err != nil {
		return acc
	}
	dt := now - realTS
	if dt <= 0 {
		return acc
	}
	if dt > groundbaitMaxTickRealSec {
		dt = groundbaitMaxTickRealSec
	}
	acc += dt / groundbaitRealSecPerGameHour
	if _, uerr := s.DB.SQL.ExecContext(ctx, `UPDATE groundbait_clock SET real_ts=?, game_hours=?, updated_ts=? WHERE uid=?`,
		now, acc, now, uid); uerr != nil {
		s.log.Warn("窝料游戏钟推进失败", "err", uerr)
	}
	return acc
}

// advanceGroundbaitClock 由会话 keepalive 调用，让窝料时效随在线游戏时间
// 连续推进，而不是等到下一次投喂/判鱼才补算。
func (sess *Session) advanceGroundbaitClock() {
	if sess == nil || sess.Srv == nil || sess.Userid == 0 || sess.Srv.DB == nil {
		return
	}
	if !sess.Srv.groundbaitEnabled() {
		return
	}
	_ = sess.Srv.groundbaitGameHours(sess.pushCtx(), sess.Userid)
}

// groundbaitDeposit 是四种投放方式的唯一入口：手抛/打窝竿/食笼 PVA/弹弓都调它，
// 通道只作为标签留账，方便以后分别调参。txnGuid 唯一约束提供幂等保护。
// instanceGUID 是这份料的物品实例，自制窝料的用料就记在它身上（可为空）。
func (s *Server) groundbaitDeposit(ctx context.Context, uid int64, loc string, x, z float64, hasPos bool,
	spot uint64, itemID string, portions float64, channel, txnGuid, instanceGUID string) {
	if !s.groundbaitEnabled() || uid == 0 || loc == "" || portions <= 0 || txnGuid == "" {
		return
	}
	defs := groundbaitDefsLoad()
	if defs.badErr != "" {
		return
	}
	_, known := defs.itemDef(itemID)
	// 自制窝料的产物没有独立 item_id（一律叫 feedcompleted），五种槽怎么配都叫
	// 这个名。所以按实例把实际用料查出来冻进这一行账本：之后那份料可能被吃掉、
	// 删掉，而窝还要活几个小时，判鱼时不该回头查一个已经不存在的实例。
	matsJSON := ""
	if mats := s.groundbaitInstanceMaterials(ctx, instanceGUID); len(mats) > 0 {
		if encoded, merr := json.Marshal(mats); merr == nil {
			matsJSON = string(encoded)
		}
	}
	// 配方表没登记的窝料（比如自制窝料实例）：照样入账，用中性档案，
	// 只是吃不到食性加成。宁可保守，不能让它凭空消失。
	if portions > 60 {
		portions = 60
	}
	per := s.tunableF("groundbait_portion_hours", 0.6)
	if per <= 0 {
		return
	}
	hours := portions * per
	// 单批窝时封顶：1 份 0.6 游戏小时，但一批最多 6 小时（产品口径：
	// 「打 10 份饵 = 游戏里 6 小时有效」）。再多份数只抬强度 S，不再拖长时效。
	capHours := s.tunableF("groundbait_hours_cap", 6.0)
	if capHours > 0 && hours > capHours {
		hours = capHours
	}
	bx, bz, hasPosInt := groundbaitCellFor(x, z, hasPos, spot)
	acc := s.groundbaitGameHours(ctx, uid)
	tx, err := s.DB.SQL.BeginTx(ctx, nil)
	if err != nil {
		s.log.Warn("窝料投放开事务失败", "err", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO groundbait_deposit
		(uid,loc,bx,bz,has_pos,channel,item_id,portions,units_left,hours0,deposit_hour,txn_guid,created_ts,materials_json)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		uid, loc, bx, bz, hasPosInt, channel, itemID, portions, portions, hours, acc, txnGuid,
		float64(time.Now().Unix()), matsJSON)
	if err != nil {
		s.log.Warn("窝料投放写账本失败", "err", err, "item", itemID, "channel", channel)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// 幂等命中：这条上报已经投过。这里必须留一行日志——幂等键撞车时
		// 饵照扣但窝账没记，静默吞掉过整批发窝（见 handitemThrowTxn）。
		s.log.Info("窝料投放重复上报已忽略", "uid", uid, "channel", channel,
			"item", itemID, "txn", txnGuid)
		return
	}
	if err := tx.Commit(); err != nil {
		s.log.Warn("窝料投放提交失败", "err", err)
		return
	}
	s.log.Info("窝料投放", "uid", uid, "loc", loc, "cell", fmt.Sprintf("%d,%d", bx, bz),
		"item", itemID, "channel", channel, "portions", portions, "hours", hours,
		"known", known, "mats", len(groundbaitDecodeMaterials(matsJSON)), "txn", txnGuid)
}

// groundbaitRecordInstanceMaterialsTx 记下这袋窝料制作时实际扣掉的原料。
// 空清单不写：那时没什么可说的，读的人会退回该 item_id 的固定档案。
func groundbaitRecordInstanceMaterialsTx(ctx context.Context, tx *sql.Tx, guid string, mats []string) error {
	if tx == nil || guid == "" || len(mats) == 0 {
		return nil
	}
	encoded, err := json.Marshal(mats)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO groundbait_recipe(instance_guid,mats_json,created_ts)
		VALUES(?,?,?) ON CONFLICT(instance_guid) DO UPDATE SET mats_json=excluded.mats_json,
			created_ts=excluded.created_ts`,
		guid, string(encoded), float64(time.Now().Unix()))
	return err
}

// groundbaitInstanceMaterials 读某个物品实例的制作用料；没有记录时返回 nil，
// 调用方按该 item_id 的固定档案处理（商店买的干混合、十款具名商品都是这条路）。
func (s *Server) groundbaitInstanceMaterials(ctx context.Context, guid string) []string {
	if s.DB == nil || guid == "" {
		return nil
	}
	var raw string
	if err := s.DB.SQL.QueryRowContext(ctx, `SELECT mats_json FROM groundbait_recipe WHERE instance_guid=?`, guid).
		Scan(&raw); err != nil {
		return nil
	}
	return groundbaitDecodeMaterials(raw)
}

// cloneGroundbaitRecipe 把自制窝料的用料从来源堆复制到拆出的子实例。
// 装填/挂饵会创建新 GUID；不复制的话，投放时按子 GUID 查不到配方，实际用料
// 会被错误回退成 feedcompleted 的通用档案。来源没有配方时是空操作。
func (s *Server) cloneGroundbaitRecipe(ctx context.Context, sourceGUID, childGUID string) error {
	if s.DB == nil || sourceGUID == "" || childGUID == "" || sourceGUID == childGUID {
		return nil
	}
	_, err := s.DB.SQL.ExecContext(ctx, `INSERT OR IGNORE INTO groundbait_recipe(instance_guid,mats_json,created_ts)
		SELECT ?, mats_json, created_ts FROM groundbait_recipe WHERE instance_guid=?`, childGUID, sourceGUID)
	return err
}

// groundbaitSpotState 是一个窝格子的即时状态。
type groundbaitSpotState struct {
	units     float64 // N：净窝份数（已按剩余窝时等比稀释）
	hoursLeft float64 // T：剩余窝时（游戏小时）
	batches   int     // 未过期批次数
	diffuse   int     // 其中落点未知的批数（按衰减系数计入）
	near      int     // 其中只差一格的批数（同样按衰减系数计入）
	hold      float64 // 按份数加权的留窝系数，用于流速门
	// diet 是这个格子里所有还活着的批次按剩余份数合成出来的食性向量（百分比）。
	// 一格里混投两种料时按份数加权合成，比逐批各算一遍再平均更贴近「这锅料
	// 实际是什么」，而且同一锅料只算一次逐鱼种契合度。
	diet      map[string]float64
	dietKnown bool   // 已知食性表（包括只有 PVA 载体的空饵）
	dietSig   string // diet 的稳定指纹，逐鱼种亲和缓存的键
	// targetShare：商品包装写明目标鱼的份数占比，用来给契合度加作者先验。
	// 两袋不同商品各投一半时，两个目标鱼各拿一半先验。
	targetShare map[string]float64
	// itemShare：这个格子里出现过哪些 item_id 及份数占比。只在运行时算不出
	// 亲和（攻略吃饵表读不出来）时退回预烘亲和表用。
	itemShare map[string]float64
}

func newGroundbaitSpotState() groundbaitSpotState {
	return groundbaitSpotState{
		diet:        map[string]float64{},
		targetShare: map[string]float64{},
		itemShare:   map[string]float64{},
	}
}

// groundbaitSpot 汇总某格子当前还活着的投放批次，顺手删掉已过期的行。
func (s *Server) groundbaitSpot(ctx context.Context, uid int64, loc string, bx, bz int64) groundbaitSpotState {
	st := newGroundbaitSpotState()
	if s.DB == nil || uid == 0 || loc == "" {
		return st
	}
	defs := groundbaitDefsLoad()
	if defs.badErr != "" {
		return st
	}
	acc := s.groundbaitGameHours(ctx, uid)
	// 同格投放全额生效；落点未知的投放（has_pos=0）按衰减系数计入，
	// 免得「先打窝后抛竿」的窝在玩家抛竿后凭空消失。
	fallDiffuse := s.groundbaitDiffuseFall()
	radius := s.groundbaitNeighborCells()
	rows, err := s.DB.SQL.QueryContext(ctx, `SELECT id,item_id,portions,units_left,hours0,deposit_hour,has_pos,bx,bz,materials_json
		FROM groundbait_deposit
		WHERE uid=? AND loc=? AND (has_pos=0 OR (bx BETWEEN ? AND ? AND bz BETWEEN ? AND ?))
		ORDER BY (bx=? AND bz=?) DESC, has_pos DESC, deposit_hour`,
		uid, loc, bx-radius, bx+radius, bz-radius, bz+radius, bx, bz)
	if err != nil {
		s.log.Warn("窝料账本读取失败", "err", err)
		return st
	}
	var deadRows []int64
	type live struct {
		id        int64
		itemID    string
		materials []string
		units     float64
		remain    float64
		ratio     float64
		fall      float64
		unknown   bool
	}
	var alive []live
	for rows.Next() {
		var (
			id                     int64
			itemID                 string
			portions, units, hours float64
			depositAt              float64
			hasPosRow              int
			rowBX, rowBZ           int64
			matsJSON               string
		)
		if serr := rows.Scan(&id, &itemID, &portions, &units, &hours, &depositAt, &hasPosRow, &rowBX, &rowBZ,
			&matsJSON); serr != nil {
			continue
		}
		remain := hours - (acc - depositAt)
		if remain <= 0 || units <= 0 {
			deadRows = append(deadRows, id)
			continue
		}
		ratio := groundbaitClamp(remain/hours, 0, 1)
		// 三档权重：本格全额；落点只知个大概、落在相邻格，或压根没落点，
		// 都按同一个衰减系数计入（宁可轻算，不能当它不存在）。
		fall := 1.0
		unknown := hasPosRow == 0
		if unknown || rowBX != bx || rowBZ != bz {
			fall = fallDiffuse
		}
		alive = append(alive, live{id: id, itemID: itemID, materials: groundbaitDecodeMaterials(matsJSON),
			units: units * ratio * fall, remain: remain * fall, ratio: ratio, fall: fall, unknown: unknown})
	}
	if cerr := rows.Close(); cerr != nil {
		s.log.Warn("窝料账本游标关闭失败", "err", cerr)
	}
	for _, dead := range deadRows {
		if _, derr := s.DB.SQL.ExecContext(ctx, `DELETE FROM groundbait_deposit WHERE id=?`, dead); derr != nil {
			s.log.Warn("窝料过期批次清理失败", "err", derr)
		}
	}
	if len(alive) == 0 {
		return st
	}
	// 同一格内多批投放按剩余份数加权合并；先累加再加权，避免逐批重复算。
	// 合的是「这锅料是什么」（食性向量与物理特性），不是「对某条鱼多对口」——
	// 后者留给判鱼时按鱼种现算，自制配方才能真正随用料变。
	dietSum := map[string]float64{}
	targetSum := map[string]float64{}
	itemSum := map[string]float64{}
	holdSum := 0.0
	for _, b := range alive {
		st.units += b.units
		st.hoursLeft += b.remain
		st.batches++
		if b.unknown {
			st.diffuse++
		} else if b.fall < 1 {
			st.near++
		}
		itemSum[b.itemID] += b.units
		prof, hasProf := defs.batchProfile(b.itemID, b.materials)
		if !hasProf {
			// 没档案也没用料：这份窝照样计份数（照样压死窝），只是抬不起食性加成。
			holdSum += b.units
			continue
		}
		st.dietKnown = true
		holdSum += b.units * prof.Hold
		for fam, w := range prof.Diet {
			dietSum[fam] += b.units * w
		}
		if it, ok := defs.itemDef(b.itemID); ok && it.TargetFish != "" {
			targetSum[it.TargetFish] += b.units
		}
	}
	if st.units <= 0 {
		return newGroundbaitSpotState()
	}
	for fam, v := range dietSum {
		st.diet[fam] = v / st.units
	}
	st.dietSig = groundbaitDietSig(st.diet)
	for fish, v := range targetSum {
		st.targetShare[fish] = v / st.units
	}
	for item, v := range itemSum {
		st.itemShare[item] = v / st.units
	}
	st.hold = holdSum / st.units
	return st
}

// groundbaitDecodeMaterials 读账本里存的实际用料。老行没有这一列（空串），
// 返回 nil 让调用方退回该 item_id 的固定档案。
func groundbaitDecodeMaterials(raw string) []string {
	if raw == "" {
		return nil
	}
	var mats []string
	if err := json.Unmarshal([]byte(raw), &mats); err != nil {
		return nil
	}
	return mats
}

// batchProfile 取一批窝料的档案：投的时候记下用料就按用料现算（自制配方的
// 唯一出路），否则退回该 item_id 的固定档案。
func (d *groundbaitDefs) batchProfile(itemID string, materials []string) (groundbaitProfile, bool) {
	if len(materials) > 0 {
		if p, ok := d.profileOf(materials); ok {
			return p, true
		}
	}
	return d.profileForItem(itemID)
}

// groundbaitStrength 窝点强度 S：份数越多越强，但收益递减（默认 4 份上下趋饱）。
func (s *Server) groundbaitStrength(units float64) float64 {
	sat := s.tunableF("groundbait_sat", 4.0)
	if sat <= 0 {
		sat = 4
	}
	if units <= 0 {
		return 0
	}
	return 1 - math.Exp(-units/sat)
}

// groundbaitOverdoseP 死窝闸门：净窝份数超过阈值后线性压开口率，22 份以上归零。
func (s *Server) groundbaitOverdoseP(units float64) float64 {
	from := s.tunableF("groundbait_overdose_from", 15.0)
	step := s.tunableF("groundbait_overdose_step", 0.15)
	if units <= from || step <= 0 {
		return 1
	}
	return groundbaitClamp(1-step*(units-from), 0, 1)
}

// groundbaitDiffuseFall 落点未知投放的衰减系数。弹弓与手抛只上报距离、不上报坐标，
// 这种窝记在「整个钓点一格」里；等玩家抛竿后判鱼用的是浮标落点格子，两者对不上。
// 与其让这份窝凭空失效，不如让它按衰减系数继续生效（实测：同一钓点打窝后抛竿，
// 本格 0 批 + 落点未知 1 批，全靠这条通道生效）。缺省 0.40，调 0 即只认同格。
func (s *Server) groundbaitDiffuseFall() float64 {
	return groundbaitClamp(s.tunableF("groundbait_diffuse_fall", 0.40), 0, 1)
}

// groundbaitNeighborCells 邻格容差（单位：格）。弹弓与手抛只报距离，落点最多算到
// 几米精度；实测客户端本次抛饵散布 scatter=3.0 米（活钩 805 到达时 dist=10.000
// scatter=3.000），窝团本来就摊在落点周围，一格 5 米很容易被切在边界上。
// 缺省 1 格：相邻一格的窝按同一个衰减系数计入，隔两格以上才算无关。调 0 退回严格同格。
func (s *Server) groundbaitNeighborCells() int64 {
	n := int64(s.tunableF("groundbait_neighbor_cells", 1))
	if n < 0 {
		return 0
	}
	if n > 4 {
		return 4
	}
	return n
}

// groundbaitGates 算习性门：硬门（水层/昼夜/季节/底质/流速/稀有度）与
// 软门（水温/低温/水色）。两者都夹在 [0.05,1]，软门稍后被天气软化往 1 拉近。
func (s *Server) groundbaitGates(ctx context.Context, c fishRollContext, fishKey string, hold float64) (float64, float64) {
	p := s.cachedFishEcology(ctx, c, fishKey)
	if !p.Enabled {
		return 0.05, 0.05
	}
	water := fishWaterConditionsFor(p, c)
	_, hour := worldDayAndHour(c.now.Unix())
	layer := groundbaitClamp(s.fishLayerWeightProfile(p, c), 0.05, 1)
	diel := groundbaitClamp(fishDielWeight(p, hour), 0.05, 1)
	season := groundbaitClamp(fishSeasonWeight(p, water.Season), 0.05, 1)
	rarity := groundbaitClamp(1-0.12*float64(p.RarityTier), 0.4, 1)
	substrate := 1.0
	if c.spotEnv.Known {
		if prefs := fishEcologyJSONMap(p.SubstrateJSON); len(prefs) > 0 {
			substrate = groundbaitClamp(0.85+prefs[c.spotEnv.Substrate]*0.18, 0.7, 1)
		}
	}
	flow := 1.0
	if c.spotEnv.Known {
		if hold <= 0 {
			hold = 1
		}
		flow = 1 + (hold-1)*0.30 - math.Max(0, c.spotEnv.Current-0.25)*(1.25-hold*0.35)*0.9
	}
	hard := groundbaitClamp(layer*diel*season*rarity*substrate*flow, 0.05, 1)

	tol := math.Max(2.2, (p.TempMaxC-p.TempMinC)/2)
	z := (water.WaterTempC - p.TempPrefC) / tol
	temp := 0.55 + 0.45*math.Exp(-0.5*z*z)
	cold := 1.0
	if water.WaterTempC < 10 {
		cold = groundbaitClamp(0.55+(water.WaterTempC/10)*0.45, 0.55, 1)
	}
	clarity := groundbaitClamp(1-math.Abs(p.ClarityPref-water.Clarity)*0.16, 0.05, 1)
	soft := groundbaitClamp(temp*cold*clarity, 0.05, 1)
	return hard, soft
}

// groundbaitSpeciesMultiplier 单个鱼种的窝料乘子 M。掠食性鱼种在食性这一步
// 就被冻结（见 groundbaitPredatorFrozen），压根进不到加权，恒为 1.00。
func (s *Server) groundbaitSpeciesMultiplier(ctx context.Context, c fishRollContext, st groundbaitSpotState,
	fishKey string, S, P float64) float64 {
	a, spec, decided := s.groundbaitAffinityForRoll(ctx, c, st, fishKey)
	if !decided || a <= 0 {
		return 1
	}
	gain := s.tunableF("groundbait_gain", 1.50)
	cap := s.tunableF("groundbait_cap", 2.60)
	specialGain := s.tunableF("groundbait_special_gain", 0.30)
	hard, soft := s.groundbaitGates(ctx, c, fishKey, st.hold)
	// 天气软化：只有对口的窝料才配把坏天气往 1 拉近，且永远不超过硬门。
	softMax := s.tunableF("groundbait_soft_max", 0.60)
	beta := s.tunableF("groundbait_soft_beta", 0.45)
	softened := soft
	if beta > 0 {
		beta = groundbaitClamp(beta*S*a, 0, softMax)
		softened = 1 + (soft-1)*(1-beta)
	}
	m := 1 + gain*S*a*(1+specialGain*spec)*math.Sqrt(hard*groundbaitClamp(softened, 0.05, 1))
	m = math.Min(cap, m)
	return 1 + (m-1)*P
}

// 逐鱼种亲和的三种结论。掠食冻结与「这条鱼不在生态表里」都要能明确区分：
// 前者是判定结论（恒 x1.00），后者才该退回预烘亲和表。
const (
	groundbaitAffComputed = iota
	groundbaitAffFrozen
	groundbaitAffUnavailable
)

// groundbaitAffKey 是逐鱼种亲和的缓存键：同一锅食性向量对同一条鱼的契合度
// 与天气无关，一次判鱼里每格每鱼种只算一次。
type groundbaitAffKey struct {
	dietSig string
	fishKey string
}

// groundbaitSpecialMargin 专食性的主力余量阈值（spec 中的食性亲和公式的「别太夸张」闸）：
// 窝里的主力食性必须明显压过次族，才算「专攻这一口」。两种料占比只差不到这个
// 余量时不给专食加成，免得原料微调 0.52 对 0.49 就把一整批鱼的加成换来换去。
// 缺省 0.08 卡在十款具名商品的最小主力余量（0.11）之下，标定集不受影响。
func (s *Server) groundbaitSpecialMargin() float64 {
	return groundbaitClamp(s.tunableF("groundbait_special_margin", 0.08), 0, 1)
}

// groundbaitAffinityForRoll 给出这个窝格对某条鱼的契合度 A 与专食性。
// 优先按实际用料现算；只有在攻略吃饵表读不出来这类降级状态下才退回预烘亲和表。
// 返回值 decided=false 表示「不该改动这条鱼」：冻结、不在生态表、且预烘表也没有。
func (s *Server) groundbaitAffinityForRoll(ctx context.Context, c fishRollContext,
	st groundbaitSpotState, fishKey string) (float64, float64, bool) {
	prior := s.tunableF("groundbait_prior", 0.35)
	if st.dietKnown && st.dietSig == "" {
		// 纯 PVA 水溶网这类没有饵料的载体不吸引鱼；不能退回固定商品表。
		return 0, 0, false
	}
	if st.dietSig != "" {
		row, status := s.cachedGroundbaitAffinity(ctx, c, st.dietSig, st.diet, fishKey)
		switch status {
		case groundbaitAffComputed:
			// 商品包装写明目标鱼时给食性契合度加一份作者先验，按份数占比折算。
			return s.groundbaitAffinityValue(row.Appetite + prior*st.targetShare[fishKey]), row.Special, true
		case groundbaitAffFrozen:
			return 0, 0, false
		}
	}
	return s.groundbaitAffinityFallback(st, fishKey, prior)
}

// groundbaitAffinityFallback 兜底：用随包发布的预烘亲和表（14 款成品 x 逐鱼种）。
// 只在运行时算不出亲和时走到，正常路径不经过它。
func (s *Server) groundbaitAffinityFallback(st groundbaitSpotState, fishKey string, prior float64) (float64, float64, bool) {
	defs := groundbaitDefsLoad()
	if defs.badErr != "" {
		return 0, 0, false
	}
	affSum, specSum, weight := 0.0, 0.0, 0.0
	for itemID, share := range st.itemShare {
		row, ok := defs.affinityOf(itemID, fishKey)
		if !ok {
			continue
		}
		app := row.Appetite
		if it, ok := defs.itemDef(itemID); ok && it.TargetFish == fishKey {
			app += prior
		}
		affSum += share * s.groundbaitAffinityValue(app)
		specSum += share * row.Special
		weight += share
	}
	if weight <= 0 {
		return 0, 0, false
	}
	return affSum / weight, specSum / weight, true
}

// cachedGroundbaitAffinity 在一次判鱼会话里复用「这锅料 x 这条鱼」的契合度。
func (s *Server) cachedGroundbaitAffinity(ctx context.Context, c fishRollContext,
	dietSig string, diet map[string]float64, fishKey string) (groundbaitAffinityRow, int) {
	key := groundbaitAffKey{dietSig: dietSig, fishKey: fishKey}
	cache := rollCache(c)
	if cache != nil {
		cache.mu.Lock()
		if hit, ok := cache.gbAff[key]; ok {
			cache.mu.Unlock()
			return hit.row, hit.status
		}
		cache.mu.Unlock()
	}
	row, status := s.groundbaitAffinityNow(ctx, c, diet, fishKey)
	if cache != nil {
		cache.mu.Lock()
		cache.gbAff[key] = cachedGroundbaitAff{row: row, status: status}
		cache.mu.Unlock()
	}
	return row, status
}

// groundbaitAffinityNow 现算一条鱼对这一锅料的食性契合度与专食性。
func (s *Server) groundbaitAffinityNow(ctx context.Context, c fishRollContext,
	diet map[string]float64, fishKey string) (groundbaitAffinityRow, int) {
	fish := s.cachedGroundbaitFishDiet(ctx, c, fishKey)
	if len(fish) == 0 {
		// 生态表里没有这条鱼（不在鱼名录里）：它吃什么都不知道，不该动它的权重。
		return groundbaitAffinityRow{}, groundbaitAffUnavailable
	}
	// 攻略吃饵表没收录这条鱼时按「没有可接受食性」算，只走生态食性那张票，
	// 不算冻结：攻略漏收的杂食鱼不该被当成纯掠食者。
	accepted, hasRule := s.groundbaitRuleVote(ctx, c, fishKey)
	if groundbaitPredatorFrozen(fish, hasRule, accepted,
		s.tunableF("groundbait_pred_lure_min", 1.5),
		s.tunableF("groundbait_pred_plant_max", 0.8),
		s.tunableF("groundbait_pred_boilie_max", 0.8)) {
		return groundbaitAffinityRow{}, groundbaitAffFrozen
	}
	appetite, special := groundbaitAppetite(diet, fish, accepted, s.groundbaitSpecialMargin())
	return groundbaitAffinityRow{FishKey: fishKey, Appetite: appetite, Special: special}, groundbaitAffComputed
}

// groundbaitRuleVote 攻略吃饵表这一票：这条鱼在攻略里接受哪些食性族，以及
// 攻略到底有没有收录它（没收录就不能只凭生态表判它是掠食者）。
// 中文名先取生态档案里的 source.name，读不到再退回数据库名字表。
func (s *Server) groundbaitRuleVote(ctx context.Context, c fishRollContext, fishKey string) (map[string]bool, bool) {
	if name := groundbaitFishName(fishKey); name != "" {
		if rule, ok := fishBaitRules[ruleName(name)]; ok {
			return groundbaitAcceptedFamilies(rule), true
		}
	}
	names, namesOK := s.cachedFishNames(ctx, c)
	if !namesOK {
		return nil, false
	}
	if name, ok := names[fishKey]; ok {
		return groundbaitAcceptedFamilies(fishBaitRules[ruleName(name)]), true
	}
	return nil, false
}

// cachedGroundbaitFishDiet 取鱼种生态食性向量（含 lure/plankton 这类窝料勾不到
// 的族，它们参与「这条鱼最执着的那一口」的比较）。生态表没有这一行时返回 nil。
func (s *Server) cachedGroundbaitFishDiet(ctx context.Context, c fishRollContext, fishKey string) map[string]float64 {
	cache := rollCache(c)
	if cache != nil {
		cache.mu.Lock()
		if diet, ok := cache.gbFishDiet[fishKey]; ok {
			cache.mu.Unlock()
			return diet
		}
		cache.mu.Unlock()
	}
	diet := map[string]float64{}
	if p := s.cachedFishEcology(ctx, c, fishKey); p.Source != "local-sim:fallback" {
		diet = fishEcologyJSONMap(p.DietJSON)
	}
	if cache != nil {
		cache.mu.Lock()
		cache.gbFishDiet[fishKey] = diet
		cache.mu.Unlock()
	}
	return diet
}

// applyGroundbaitWeight 在判鱼权重定稿前插入窝料乘子。开关关掉时一行都不执行。
func (s *Server) applyGroundbaitWeight(ctx context.Context, c *fishRollContext, keys []string, wts []float64) {
	if !s.groundbaitEnabled() || c.uid == 0 || len(keys) == 0 {
		return
	}
	bx, bz, _ := groundbaitCellFor(c.x, c.z, c.hasPos, c.spot)
	st := s.groundbaitSpot(ctx, c.uid, c.loc, bx, bz)
	if st.units <= 0 {
		return
	}
	S := s.groundbaitStrength(st.units)
	P := s.groundbaitOverdoseP(st.units)
	for i, key := range keys {
		if wts[i] <= 0 {
			continue
		}
		if m := s.groundbaitSpeciesMultiplier(ctx, *c, st, key, S, P); m > 1 {
			wts[i] *= m
		}
	}
}

// groundbaitBiteGate 咬钩闸门：这个格子腻到 22 份以上就直接不开口。
// 路亚族可以豁免（拟饵不是被窝料喂出来的口），由 tunable 控制。
func (s *Server) groundbaitBiteGate(ctx context.Context, uid int64, loc string, x, z float64,
	hasPos bool, spot uint64, rigType int64) float64 {
	if !s.groundbaitEnabled() || uid == 0 || loc == "" {
		return 1
	}
	if s.tunableF("groundbait_overdose_lure_exempt", 1) == 1 && rigTypeLureFamily[rigType] {
		return 1
	}
	bx, bz, _ := groundbaitCellFor(x, z, hasPos, spot)
	st := s.groundbaitSpot(ctx, uid, loc, bx, bz)
	if st.units <= 0 {
		return 1
	}
	P := s.groundbaitOverdoseP(st.units)
	// 这条日志是「窝到底有没有生效」的现场证据：投了多少份、还剩几小时、
	// 强度多少、开口率被压到多少。只在真的投过窝的格子上打，量不会大。
	s.log.Info("窝料影响咬钩", "loc", loc, "cell", fmt.Sprintf("%d,%d", bx, bz),
		"portions", st.units, "hours_left", st.hoursLeft, "batches", st.batches,
		"strength", s.groundbaitStrength(st.units), "bite_gate", P,
		"on_spot", st.batches-st.diffuse-st.near, "near", st.near, "no_pos", st.diffuse)
	return P
}

// groundbaitConsumeBite 鱼吃口扣份：窝被吃薄，死窝也就自己缓过来了。
func (s *Server) groundbaitConsumeBite(ctx context.Context, uid int64, loc string, x, z float64,
	hasPos bool, spot uint64, units float64) {
	if !s.groundbaitEnabled() || uid == 0 || loc == "" || units <= 0 {
		return
	}
	acc := s.groundbaitGameHours(ctx, uid)
	bx, bz, _ := groundbaitCellFor(x, z, hasPos, spot)
	radius := s.groundbaitNeighborCells()
	// 扣份顺序与生效权重一致：先扣本格，再扣邻格，最后才动落点未知那几行，
	// 免得把便宜的全局窝先吃空、本格窝却还满着。
	rows, err := s.DB.SQL.QueryContext(ctx, `SELECT id, units_left, hours0, deposit_hour
		FROM groundbait_deposit
		WHERE uid=? AND loc=? AND (has_pos=0 OR (bx BETWEEN ? AND ? AND bz BETWEEN ? AND ?))
		ORDER BY (bx=? AND bz=?) DESC, has_pos DESC, deposit_hour`,
		uid, loc, bx-radius, bx+radius, bz-radius, bz+radius, bx, bz)
	if err != nil {
		return
	}
	type batch struct {
		id      int64
		units   float64
		hours   float64
		deposit float64
	}
	var list []batch
	for rows.Next() {
		var b batch
		if serr := rows.Scan(&b.id, &b.units, &b.hours, &b.deposit); serr != nil {
			continue
		}
		list = append(list, b)
	}
	_ = rows.Close()
	left := units
	for _, b := range list {
		if left <= 0 {
			break
		}
		if b.hours-(acc-b.deposit) <= 0 {
			continue
		}
		take := math.Min(left, b.units)
		left -= take
		if _, uerr := s.DB.SQL.ExecContext(ctx,
			`UPDATE groundbait_deposit SET units_left=MAX(0, units_left-?) WHERE id=?`, take, b.id); uerr != nil {
			s.log.Warn("窝料咬口扣份失败", "err", uerr)
		}
	}
}

// depositCastGroundbait 把「钓组入水时消耗掉的窝料」投进落点格子。
// 食笼/PVA 棒/打窝器都走这条：扣料与投放同一次事件，幂等键沿用饵料事件键。
func (s *Server) depositCastGroundbait(sess *Session, st *FishState, change baitChange, eventKey string) {
	if !s.groundbaitEnabled() || len(change.Groundbait) == 0 || sess == nil || st == nil {
		return
	}
	st.mu.Lock()
	loc, x, z, has, spot, rigType := st.Loc, st.CastPosX, st.CastPosZ, st.HasCastPos, st.Spot, st.RigType
	st.mu.Unlock()
	ctx := sess.pushCtx()
	for i, use := range change.Groundbait {
		s.groundbaitDeposit(ctx, sess.Userid, loc, x, z, has, spot, use.ItemID, use.Units,
			groundbaitChannelFor(use.Category, rigType), fmt.Sprintf("%s#%d", eventKey, i), use.GUID)
	}
}

// groundbaitChannelFor 记下这份窝是从哪条通道投的，只作溯源与排查用，
// 不参与吸引计算（吸引只看格子、份数与时间）。
func groundbaitChannelFor(category, rigType int64) string {
	if category == catPVAStick {
		return "pva"
	}
	if spodRigTypes[rigType] {
		return "spod"
	}
	return "feeder"
}
