package gameserver

// 搏鱼期间的装备受力磨损结算（渔轮摩擦片/轮件 + 竿侧零件 + 前导）。
//
// 官服抓包口径（specs/changes/wear-fight-time/spec.md）：磨损只按"受力时间"扣——
// 14/8 负载遥测之间的真实秒数就是受力账，空竿/等待/收线零磨损；拉力明显超过泄力
// （>1.05×）才打滑磨摩擦片。打滑速率不分档位、统一 0.0001/41.7 ≈ 0.00024%/s：
// 官服只有 W5 一个速率点（112s 重度打滑磨 0.027%），逐档阶梯无对照数据，2026-11-12
// 拍平（specs/changes/reel-slip-rate-flat）；锁轮（29 档）的承力上限由断件判定链路
// 承担，不在磨损层。14/8 只作为采样边界：不能把客户端上报密度直接当成磨损次数。
// 实测节拍 1.8~2.0 秒只用于首条兜底和校准。
// 受力账按官服帧节奏（~2s）中途落库并下发裸零件树，客户端搏鱼中就能看到耐久下降；
// 损伤日志每竿仍只写最终一笔。

import (
	"context"
	"math"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

const (
	// defaultReelLoadTickSeconds 是本竿第一条 14/8 的兜底时长（tunable reel_load_tick_seconds）：
	// 还没收到过上一条上报时服务端不知道前面已经过了多久，按实测节拍 1.9 秒起算。
	defaultReelLoadTickSeconds = 1.9
	// reelSlipCalibrationSeconds 把统一基准换算成每秒磨损率：
	// 0.01% / 41.7s ≈ 0.00024%/s，对齐官服 W5 实测（112s 重度打滑磨 0.027%）。
	// 旧值 1.8 会把摩擦片烧快约 23 倍。tunable（reel_slip_calibration_seconds）可覆盖，
	// 此处保留为缺省回落。
	reelSlipCalibrationSeconds = 41.7
	// defaultReelLoadMaxTickSeconds 是两条 14/8 之间最多折算的秒数（tunable
	// reel_load_max_tick_seconds）：客户端卡顿、切场景或掉线会把间隔拉长，超过上限
	// 就不再补记，宁可少记一点受力，也不让一条迟到上报把整段间隔一次记满。
	defaultReelLoadMaxTickSeconds = 5.0
	// reelSlipDeadbandRatioDefault 是打滑门槛倍率（tunable reel_slip_deadband_ratio）：
	// 拉力 ≤ 泄力×该值不算打滑。官服边界样本：1.04× 零磨损，缺省取 1.05。
	reelSlipDeadbandRatioDefault = 1.05
	// leaderStressWearPerSecDefault 是前导受力磨损速率（百分点/受力秒，
	// tunable wear_leader_per_stress_sec）：底钓2 单场实测 ≈0.000047（412s 磨 0.019%）。
	leaderStressWearPerSecDefault = 0.00005
)

// fightReelWear 是本竿搏鱼期间的装备受力账：承力参数缓存 + 累计磨损。
// 承力参数一次搏鱼只查一次库，玩家改摩擦档位即作废重取。
type fightReelWear struct {
	valid      bool
	fishGUID   string
	rodGUID    string
	reelGUID   string
	leaderGUID string // 本竿挂装的前导实例（无前导装配为空）；落库后由下发使用

	setting  int64   // 17/14 线上摩擦档位，0~29；29 为锁轮（dragKg 已按档位取到锁轮承力）
	dragKg   float64 // 当前摩擦档位的泄力（已含摩擦片剩余寿命与轴承效率）
	ratedKg  float64 // 摩擦片额定制动力（含母轮工艺）
	oilWear  float64 // 轮油磨损倍率：好油 <1，差油 >1
	oilMatch float64 // 轮油与当前轮子的适配倍率：专用 < 通用 < 不匹配
	pullKg   float64 // 服务端已知的鱼拉力（公斤力）

	StressSecs float64 // 受力累计秒数：轮件/竿件/前导按秒磨损
	SlipTicks  int
	SlipSecs   float64 // 打滑累计的真实秒数（摩擦片磨损口径与诊断）
	SlipWear   float64 // 泄力打滑累计的摩擦片寿命比例
	BreakHits  int     // 断件次数（仅诊断：断件冲击磨损无官服数据，不扣寿命）
	settled    bool    // 本竿是否已落库（每竿一次）
	// lastLoadAt 是上一条 14/8 到达服务端的时刻，用来把受力秒数换成真实时长；
	// lastFlushAt 是上次中途落库的时刻（节流）。两者与 loadProbe 都只是进程内
	// 状态，做成非导出字段，不进钓组状态的 JSON 快照。
	lastLoadAt time.Time
	// Preserve the online switch boundary when rebuilding the strength cache.
	protectionChangedAt time.Time
	lastFlushAt         time.Time
	loadProbe           fightLoadProbe
}

// reset 清空本竿账本（新的一杆）。
func (w *fightReelWear) reset() { *w = fightReelWear{} }

// invalidate 丢掉承力缓存（玩家改了摩擦档位），已累计的受力账保留，
// 下一条 14/8 用新档位重新取缓存。
func (w *fightReelWear) invalidate() {
	kept := *w
	kept.valid = false
	*w = kept
}

// claimLocked 取走本竿待落库的受力账（调用方必须持有 FishState 锁）。已落库或
// 没有任何受力/打滑时返回 false，保证同一竿的账只写一次。
func (w *fightReelWear) claimLocked() (fightReelWear, bool) {
	if w.settled {
		return *w, false
	}
	if w.StressSecs <= 0 && w.SlipWear <= 0 {
		return *w, false
	}
	w.settled = true
	return *w, true
}

// markRodBreakLocked 记下本竿发生过断件（17/9）。断件冲击磨损无官服数据、不扣寿命，
// 次数只进损伤日志的诊断字段。
func (w *fightReelWear) markRodBreakLocked() {
	if w.settled {
		return
	}
	w.BreakHits++
}

// rollFightWearLedgerLocked 结束当前受力账段并开新段：未落库的部分取走由调用方
// 在锁外落库。每竿基准已随官服口径删除：抛竿/空竿不再产生任何磨损账。
func rollFightWearLedgerLocked(st *FishState, rodGUID string) (fightReelWear, bool) {
	pending, has := st.reelWear.claimLocked()
	st.reelWear.reset()
	return pending, has
}

// loadTickSecondsLocked 把两条 14/8 之间的真实间隔折算成结算时长（秒）。
// 首条上报按 tunable 的兜底节拍起算：在那之前过了多久服务端无从得知。间隔为负
// （时钟回拨）算 0 秒，超过上限只记上限，免得一条迟到上报把整段间隔一次记满。
func (w *fightReelWear) loadTickSecondsLocked(now time.Time, def, maxTick float64) float64 {
	tick := def
	if !w.lastLoadAt.IsZero() {
		tick = math.Max(0, math.Min(now.Sub(w.lastLoadAt).Seconds(), maxTick))
	}
	if !w.protectionChangedAt.IsZero() {
		tick = math.Min(tick, math.Max(0, now.Sub(w.protectionChangedAt).Seconds()))
	}
	return tick
}

// partCounts 把本竿受力账折算成零件消耗计数：受力秒是唯一磨损来源，
// 打滑秒数与断件次数只供摩擦片通道与诊断日志。
func (w fightReelWear) partCounts() reelPartCounts {
	return reelPartCounts{
		StressSecs: w.StressSecs,
		SlipSecs:   w.SlipSecs,
		SlipTicks:  w.SlipTicks,
		BreakHits:  w.BreakHits,
	}
}

// reelSlipOilFactor 把轮油磨损倍率收进 0.8~1.2：好油省、差油费。
func reelSlipOilFactor(v float64) float64 {
	if v <= 0 {
		return 1
	}
	if v < 0.8 {
		return 0.8
	}
	if v > 1.2 {
		return 1.2
	}
	return v
}

// childWireFloat 读子实例上的浮点字段，兼容 FLOAT/DOUBLE 与 U32 承载 IEEE754
// 两种 registry 布局；字段缺失返回 0。
func childWireFloat(obj *proto.Value, csName string) float64 {
	if obj == nil || obj.Kind != proto.KindObj {
		return 0
	}
	idx := fieldIdxByCS(obj.Class, csName)
	if idx < 0 || idx >= obj.FieldCount() {
		return 0
	}
	f := obj.Fields[idx]
	switch fieldOpAt(obj.Class, idx) {
	case "FLOAT", "DOUBLE":
		return f.Float
	case "U32":
		if f.Num == nil {
			return 0
		}
		return float64(proto.F32FromBits(uint32(*f.Num)))
	default:
		return 0
	}
}

// fightReelCache 读取本竿搏鱼需要的渔轮承力、摩擦片额定值、轮油状态、前导实例与
// 鱼拉力。只在缓存失效时调用（一次搏鱼一次），所以允许读库。
func (s *Server) fightReelCache(ctx context.Context, uid int64, rodGUID string, f *RolledFish) fightReelWear {
	var out fightReelWear
	if s == nil || s.DB == nil || f == nil || rodGUID == "" {
		return out
	}
	reelGUID, err := s.DB.GetItemGUIDByRigSlot(ctx, rodGUID, gearCatReel)
	if err != nil || reelGUID == "" {
		return out
	}
	row, err := s.DB.GetItemByGUID(ctx, reelGUID)
	if err != nil || row == nil || row.UID != uid {
		return out
	}
	perf, err := s.DB.GetReelPerformance(ctx, row.GUID)
	if err != nil || perf == nil {
		perf, err = s.syncReelPerformance(ctx, row)
		if err != nil || perf == nil {
			return out
		}
	}
	rated, ok := s.reelWasherRatedFromRow(ctx, row)
	if !ok || rated <= 0 {
		return out
	}
	reelBrand := ""
	if def, derr := s.itemDefForRow(ctx, row); derr == nil && def != nil {
		reelBrand = def.Brand
	}
	setting, _ := s.rodReelDragSetting(ctx, rodGUID)
	leaderGUID, _ := s.resolveRigLeaderGUID(ctx, rodGUID)
	return fightReelWear{
		valid:      true,
		fishGUID:   f.FishGUID,
		rodGUID:    rodGUID,
		reelGUID:   reelGUID,
		leaderGUID: leaderGUID,
		setting:    setting,
		dragKg:     reelDragAtSetting(perf, setting),
		ratedKg:    rated,
		oilWear:    perf.OilWearFactor,
		oilMatch:   reelOilMatchFactor(row.ItemID, reelBrand, perf.OilItemID),
		pullKg:     s.fishFightPullKgfFor(f),
	}
}

// reelSlipCalibrationSecondsTunable 返回打滑磨损率的校准秒数（tunable
// reel_slip_calibration_seconds）：值越大，同样打滑时长磨得越少。
// 缺失、非数值、≤0 或非有限值一律回落到代码默认，保证未配置时行为不变。
func (s *Server) reelSlipCalibrationSeconds() float64 {
	v := s.tunableF("reel_slip_calibration_seconds", reelSlipCalibrationSeconds)
	if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return reelSlipCalibrationSeconds
	}
	return v
}

// reelSlipDeadbandRatio 返回打滑门槛倍率（tunable reel_slip_deadband_ratio）：
// 拉力不超过泄力×该值不算打滑。官服边界样本 1.04× 零磨损，缺省 1.05；
// 小于 1 的配置没有意义（等于放宽到"未超泄力也磨"），回落缺省。
func (s *Server) reelSlipDeadbandRatio() float64 {
	v := s.tunableF("reel_slip_deadband_ratio", reelSlipDeadbandRatioDefault)
	if v < 1 || math.IsNaN(v) || math.IsInf(v, 0) {
		return reelSlipDeadbandRatioDefault
	}
	return v
}

// leaderStressWearPerSec 返回前导受力磨损速率（tunable
// wear_leader_per_stress_sec）：底钓2 单场实测 ≈0.00005 百分点/秒。
func (s *Server) leaderStressWearPerSec() float64 {
	v := s.tunableF("wear_leader_per_stress_sec", leaderStressWearPerSecDefault)
	if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return leaderStressWearPerSecDefault
	}
	return v
}

// fightWearStreamIntervalSeconds 是搏鱼中受力账中途落库/下发的节流间隔秒数，
// 对齐官服搏鱼 4/22 帧节奏（~2s/帧）。
const fightWearStreamIntervalSeconds = 2.0

// fightReelTickWear 把一条 14/8 折算成受力磨损增量，tick 是它与上一条上报之间的真实秒数。
//
// 受力通道：hooked 期间的每条上报都累计受力秒（官服 n.pike 全程无打滑仍磨损），
// 轮件/竿件/前导按秒掉耐久。
// 打滑通道：拉力超过泄力×门槛（缺省 1.05）才算打滑；摩擦片按真实出线秒数磨损，
// 速率 = 当前档位曲线 / 校准秒数（对齐官服实测），轮油品质与适配倍率降低每秒磨损。
// 返回值：打滑判定次数、摩擦片寿命增量、受力秒数、打滑秒数。
func (s *Server) fightReelTickWear(w fightReelWear, tick float64) (ticks int, slipWear, stressSecs, slipSecs float64) {
	if tick <= 0 {
		return 0, 0, 0, 0
	}
	stressSecs = tick
	if w.dragKg <= 0 || w.pullKg <= w.dragKg*s.reelSlipDeadbandRatio()+1e-9 {
		return 0, 0, stressSecs, 0
	}
	// 摩擦片按真实出线秒数磨，不按 14/8 报文条数磨。鱼力超出泄力只决定是否打滑。
	match := w.oilMatch
	if match <= 0 {
		match = 1
	}
	slipRate := s.reelSlipWearBase() / s.reelSlipCalibrationSeconds()
	return 1, slipRate * tick * reelSlipOilFactor(w.oilWear) * match, stressSecs, tick
}

// resolveRigLeaderGUID 找竿上挂装的前导实例：rig 组件表优先（成品/自制前导，
// 槽 15 线材前导、13 前导），找不到返回 false。无前导装配（含旧档）跳过前导磨损。
func (s *Server) resolveRigLeaderGUID(ctx context.Context, rodGUID string) (string, bool) {
	if s == nil || s.DB == nil || rodGUID == "" {
		return "", false
	}
	comps, err := s.DB.ListRigComponents(ctx, rodGUID)
	if err != nil {
		return "", false
	}
	guids := map[int64]string{}
	for _, c := range comps {
		if c != nil && c.GUID != "" {
			guids[c.Slot] = c.GUID
		}
	}
	if g := guids[gearCatLeaderWire]; g != "" {
		return g, true
	}
	if g := guids[gearCatLeader]; g != "" {
		return g, true
	}
	return "", false
}

// recordFightReelLoad 结算一条 14/8 负载上报：只有正在搏鱼的那根竿算账。
// 查库放在锁外，写回只加本条增量，避免并发上报互相覆盖。
// 受力账按官服帧节奏（2s 节流）中途落库并下发裸零件树；
// 损伤日志每竿仍只写最终一笔。
func (s *Server) recordFightReelLoad(ctx *Ctx) {
	if s == nil || s.DB == nil || ctx == nil || ctx.Sess == nil {
		return
	}
	uid := ctx.Sess.Userid
	st := s.fishingStateForRequest(ctx)
	st.mu.Lock()
	fish, gear, castActive, hooked, resolved, cached := st.Pending, st.GearGuid, st.CastActive, st.Hooked, st.Resolved, st.reelWear
	st.mu.Unlock()
	if !castActive || !hooked || resolved || fish == nil || gear == "" {
		return
	}
	if !cached.valid || cached.fishGUID != fish.FishGUID || cached.rodGUID != gear {
		sameLedger := cached.fishGUID == fish.FishGUID && cached.rodGUID == gear
		lastLoadAt := cached.lastLoadAt
		changedAt := cached.protectionChangedAt
		cached = s.fightReelCache(s.dbCtx(), uid, gear, fish)
		if sameLedger {
			// 改摩擦档只让承力缓存失效，不应把上一条 14/8 的真实时间边界清掉。
			cached.lastLoadAt = lastLoadAt
		}
		cached.protectionChangedAt = changedAt
	}
	if !cached.valid || cached.ratedKg <= 0 {
		return
	}
	// 受力秒数按真实时长结算：上一条 14/8 到这一条之间隔了多久就记多久，
	// 只有本竿第一条上报才用兜底节拍（那之前服务端不知道已经过了多久）。
	now := time.Now()
	tick := s.tunableF("reel_load_tick_seconds", defaultReelLoadTickSeconds)
	if tick <= 0 || tick > 30 {
		tick = defaultReelLoadTickSeconds
	}
	maxTick := s.tunableF("reel_load_max_tick_seconds", defaultReelLoadMaxTickSeconds)
	if maxTick <= 0 || maxTick > 60 {
		maxTick = defaultReelLoadMaxTickSeconds
	}
	probeOn := s.tunableF("fight_load_probe", 1) != 0
	tick = cached.loadTickSecondsLocked(now, tick, maxTick)
	ticks, slipWear, stressSecs, slipSecs := s.fightReelTickWear(cached, tick)

	st.mu.Lock()
	if st.Pending != fish || st.GearGuid != gear {
		st.mu.Unlock()
		return // 期间换竿或换鱼：这条上报作废
	}
	previous, hasPrevious := fightReelWear{}, false
	cur := &st.reelWear
	if !cur.valid || cur.fishGUID != fish.FishGUID || cur.rodGUID != gear {
		// 换鱼即换一本账：旧鱼那笔若还没落库（客户端没报离场也要兜底），先取走再开新账。
		previous, hasPrevious = cur.claimLocked()
		cached.BreakHits += cur.BreakHits
		cached.lastFlushAt = cur.lastFlushAt
		*cur = cached
	}
	cur.SlipTicks += ticks
	cur.SlipSecs += slipSecs
	cur.SlipWear += slipWear
	cur.StressSecs += stressSecs
	cur.lastLoadAt = now
	if probeOn {
		// 只观测不结算：14/8 里哪个载荷是鱼拉力还没有第二份证据，先把每竿的字段
		// 范围写进损伤日志，攒够凭据再决定要不要用它。
		cur.loadProbe.observe(ctx.Tree, ctx.DecodeOK)
	}
	// 按官服帧节奏中途落库：节流 2s，取走账本立即重开新账（继承时间边界与
	// 承力缓存），损伤日志留给最终结算一笔汇总。
	flush, doFlush := fightReelWear{}, false
	if cur.StressSecs > 0 &&
		now.Sub(cur.lastFlushAt).Seconds() >= fightWearStreamIntervalSeconds {
		if pending, has := cur.claimLocked(); has {
			reopen := pending
			reopen.SlipTicks, reopen.SlipSecs, reopen.SlipWear = 0, 0, 0
			reopen.StressSecs, reopen.BreakHits = 0, 0
			reopen.settled = false
			reopen.lastFlushAt = now
			*cur = reopen
			flush, doFlush = pending, true
		}
	}
	st.mu.Unlock()
	// 以 14/8 为心跳做 5 秒节流播报；只读取上报里的耐力比例，不进磨损账。
	s.maybeReportStamina(ctx, st, now)
	if hasPrevious {
		s.applyFightReelWear(s.dbCtx(), uid, previous, "14/8", true)
	}
	if doFlush {
		s.applyFightReelWear(s.dbCtx(), uid, flush, "14/8", false)
		s.pushFightWearComponents(uid, flush)
	}
}

// resolveFightReelGUID 在账上缺渔轮实例时按竿的当前装配补出 GUID。
// 返回 true 表示账本已有可用的渔轮 GUID；对无轮手竿返回 false。
func (s *Server) resolveFightReelGUID(ctx context.Context, w *fightReelWear) bool {
	if w == nil {
		return false
	}
	if w.reelGUID != "" {
		return true
	}
	if s == nil || s.DB == nil || w.rodGUID == "" {
		return false
	}
	parsed, err := s.DB.GetItemGUIDByRigSlot(ctx, w.rodGUID, gearCatReel)
	if err != nil || parsed == "" {
		return false
	}
	w.reelGUID = parsed
	return true
}

// applyFightReelWear 把一笔已取走的受力账写进装备：摩擦片制动力、轮内零件耐久、
// 竿侧零件耐久、前导耐久与渔轮本体耐久各自落库。轮树内摩擦片与零件在同一次物品树
// 写入里完成，避免出现"片磨了但零件没动"的两套数字；本体耐久取 min(现值, 零件聚合值)。
// writeLog=false 用于搏鱼中的中途落库：损伤日志每竿只写最终一笔。
// event 只用于损伤日志（14/4 收竿、17/9 断件、14/12 鱼离场、14/7 抛竿兜底、14/8 换鱼）。
func (s *Server) applyFightReelWear(ctx context.Context, uid int64, w fightReelWear, event string, writeLog bool) bool {
	if s == nil || s.DB == nil {
		return false
	}
	if !s.resolveFightReelGUID(ctx, &w) {
		return false
	}
	slipWear := clampUnit(w.SlipWear)
	counts := w.partCounts()
	if slipWear <= 0 && counts.StressSecs <= 0 {
		return false
	}
	reel, err := s.DB.GetItemByGUID(ctx, w.reelGUID)
	if err != nil || reel == nil || reel.UID != uid {
		return false
	}
	fields, err := FieldsFromJSON(reel.FieldsJSON)
	if err != nil {
		return false
	}
	var wornOut bool
	if slipWear > 0 {
		var found bool
		wornOut, found = s.degradeFrictionWasherFields(ctx, reel, fields, slipWear)
		if !found {
			// 老档没有独立的摩擦片子件：回退到既有的本体耐久通道，继续走零件经济只会读不出东西。
			_, derr := s.damageReelSlipWear(ctx, uid, w.rodGUID, w.reelGUID, slipWear, fightWearDetailFields(w, slipWear, nil))
			return derr == nil
		}
	}
	parts := s.wearReelPartsInFields(ctx, fields, counts)
	durability := reel.Durability
	if body, ok := s.reelBodyDurabilityInFields(ctx, reel, fields); ok && body < durability {
		durability = body
	}
	before := reel.Durability
	bumpCreatedTicks(fields, reel.WireClass)
	fj, err := FieldsToJSON(fields)
	if err != nil {
		return false
	}
	// 摩擦片与母轮受力上限必须一起写，否则下次下发会用旧制动力覆盖新磨损。
	snapshot := *reel
	snapshot.FieldsJSON = fj
	if s.canonicalizeReelStrength(ctx, &snapshot, fields) {
		if fj, err = FieldsToJSON(fields); err != nil {
			return false
		}
	}
	if err := s.DB.UpdateItemFields(ctx, reel.GUID, fj, reel.Qty, durability, reel.Workmanship, reel.RigGUID); err != nil {
		return false
	}
	reel.Durability = durability
	reel.FieldsJSON = fj
	perf, perr := s.syncReelPerformance(ctx, reel)
	if perr != nil {
		return false
	}
	// 竿侧零件与前导随同一笔受力账落库；前导 GUID 回填账本供下发使用。
	rodParts := s.applyRodStressWear(ctx, uid, w.rodGUID, counts)
	s.applyLeaderStressWear(ctx, uid, &w, counts.StressSecs)
	if writeLog {
		detail := fightWearDetailFields(w, slipWear, parts)
		if len(rodParts) > 0 {
			merged, _ := detail["part_wear_bp"].(map[string]float64)
			if merged == nil {
				merged = map[string]float64{}
			}
			for id, dropped := range rodParts {
				merged["rod:"+id] = math.Round(dropped*1000000) / 100
			}
			detail["part_wear_bp"] = merged
		}
		if err := s.DB.AddItemDamageLog(ctx, store.ItemDamageLog{
			UID: uid, ItemGUID: reel.GUID, ItemID: reel.ItemID, Event: damageEventReelOverload,
			BeforeDurability: ptrFloat64(before), AfterDurability: ptrFloat64(reel.Durability),
			DetailJSON: frictionWearDetail(w.rodGUID, perf, reel.Durability, wornOut, detail),
		}); err != nil {
			return false
		}
	}
	return true
}

// applyRodStressWear 把竿侧零件受力磨损落库：缺出厂子件的竿先物化（GUID 派生与
// 推送快照同源），再按受力秒扣竿坯/导环。返回零件定义 ID → 本次扣掉比例（诊断）。
func (s *Server) applyRodStressWear(ctx context.Context, uid int64, rodGUID string, counts reelPartCounts) map[string]float64 {
	if counts.StressSecs <= 0 || rodGUID == "" {
		return nil
	}
	row, err := s.DB.GetItemByGUID(ctx, rodGUID)
	if err != nil || row == nil || row.UID != uid {
		return nil
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return nil
	}
	if !s.ensureRodWearChildren(ctx, row, fields) {
		return nil
	}
	parts := s.wearRodPartsInFields(ctx, fields, counts)
	if len(parts) == 0 {
		return nil // 没磨到就不写库（子件齐全但速率关到 0 的场合）
	}
	bumpCreatedTicks(fields, row.WireClass)
	fj, err := FieldsToJSON(fields)
	if err != nil {
		return nil
	}
	if err := s.DB.UpdateItemFields(ctx, row.GUID, fj, row.Qty, row.Durability, row.Workmanship, row.RigGUID); err != nil {
		return nil
	}
	row.FieldsJSON = fj
	return parts
}

// ensureRodWearChildren 把竿出厂子件（f0/f1/f2）物化进竿的字段树（只填空槽），
// 使竿侧耐久可以落库。GUID 派生与 buildFactoryChild 同源（md5(竿GUID|槽名)）；
// 推送快照的 rebuildRod 会把这些已持久化的耐久合并回现建子件。
func (s *Server) ensureRodWearChildren(ctx context.Context, row *store.Item, fields []proto.Field) bool {
	if s == nil || s.DB == nil || row == nil {
		return false
	}
	def, err := s.itemDefForRow(ctx, row)
	if err != nil || def == nil {
		return false
	}
	slots := []string{"ajkflmejnih", "ofpkgghjmmf", "ddbadcmdkcl"}
	for i, attr := range slots {
		if i >= len(fields) {
			break
		}
		if fields[i].Val != nil && fields[i].Val.Kind != proto.KindNull {
			continue
		}
		sysID, ok := rodPartSystemID(def, attr)
		if !ok || sysID == "" {
			continue
		}
		child, cerr := s.buildFactoryChild(ctx, row, sysID, i)
		if cerr != nil || child == nil {
			continue
		}
		fields[i].Val = child
	}
	return true
}

// applyLeaderStressWear 把前导受力磨损落库：行耐久单调只降，与承力链的
// applyDurability 联动（磨了承力就跟着降）。无前导装配静默跳过；
// 落库成功时把前导 GUID 回填账本，供下发通道使用。
func (s *Server) applyLeaderStressWear(ctx context.Context, uid int64, w *fightReelWear, stressSecs float64) {
	if w == nil || stressSecs <= 0 || w.rodGUID == "" {
		return
	}
	leaderGUID, ok := s.resolveRigLeaderGUID(ctx, w.rodGUID)
	if !ok {
		return
	}
	row, err := s.DB.GetItemByGUID(ctx, leaderGUID)
	if err != nil || row == nil || row.UID != uid || row.Durability <= 0 {
		return
	}
	rate := s.leaderStressWearPerSec()
	if rate <= 0 {
		return
	}
	leaderDef, derr := s.itemDefForRow(ctx, row)
	if derr != nil {
		leaderDef = nil
	}
	worn := clampUnit(rate * stressSecs / 100 / reelPartLifeFactor(reelPartQuality(leaderDef)))
	next := row.Durability - worn
	if next < 0 {
		next = 0
	}
	if next >= row.Durability {
		return
	}
	if err := s.DB.SetItemDurability(ctx, row.GUID, next); err != nil {
		return
	}
	row.Durability = next
	w.leaderGUID = leaderGUID
}

// fightWearDetailFields 把本竿受力账折算成损伤日志里的诊断字段（万分比），
// parts 是本次各零件扣掉的寿命比例。
func fightWearDetailFields(w fightReelWear, wear float64, parts map[string]float64) map[string]any {
	round2 := func(v float64) float64 { return math.Round(v*100) / 100 }
	detail := map[string]any{
		"stress_seconds": round2(w.StressSecs),
		"slip_ticks":     w.SlipTicks,
		"slip_seconds":   round2(w.SlipSecs),
		"slip_wear":      round2(w.SlipWear * 10000),
		"wear_bp":        round2(wear * 10000),
		"drag_setting":   w.setting, "pull_kg": round2(w.pullKg), "drag_kg": round2(w.dragKg),
		"washer_rated_kg": round2(w.ratedKg),
	}
	if w.BreakHits > 0 {
		detail["break_hits"] = w.BreakHits
	}
	// 负载观测只进日志：字段范围是下一步挑出鱼拉力的凭据，不参与结算。
	for key, val := range w.loadProbe.logFields() {
		detail[key] = val
	}
	if len(parts) > 0 {
		partWear := map[string]float64{}
		for itemID, dropped := range parts {
			partWear[itemID] = round2(dropped * 10000)
		}
		detail["part_wear_bp"] = partWear
	}
	return detail
}

// settleFightReelWearState 结算指定竿的装备受力磨损：同一竿只写一次，重发不重复扣。
// 落库后把渔轮/竿/前导推给在线客户端，黄标/红标与损耗% 都读物品树上的耐久。
func (s *Server) settleFightReelWearState(ctx context.Context, st *FishState, uid int64, event string) {
	if s == nil || s.DB == nil || st == nil {
		return
	}
	st.mu.Lock()
	w, ok := st.reelWear.claimLocked()
	if w.rodGUID == "" {
		// 账上只有竿 GUID 时也能结算：状态里的竿就是这一竿的钓组。
		w.rodGUID = st.GearGuid
	}
	st.mu.Unlock()
	if !ok {
		return
	}
	// 空竿账本里可能只有竿 GUID；先在调用方账本上解析出渔轮，
	// 这样 apply 成功后 push 仍能拿到正确的 reelGUID。
	if !s.resolveFightReelGUID(ctx, &w) {
		return
	}
	if s.applyFightReelWear(ctx, uid, w, event, true) {
		guids := []string{w.reelGUID}
		if w.rodGUID != "" {
			guids = append(guids, w.rodGUID)
		}
		if w.leaderGUID != "" {
			guids = append(guids, w.leaderGUID)
		}
		for _, sess := range s.sessionsForUID(uid) {
			sess.Push4_22Items(guids)
		}
	}
}

// settleFightReelWear 按竿 GUID 解析状态后结算一次，供手上没有状态指针的调用方使用。
func (s *Server) settleFightReelWear(ctx context.Context, uid int64, rodGUID, event string) {
	if s == nil || s.DB == nil || rodGUID == "" {
		return
	}
	s.settleFightReelWearState(ctx, s.fishingStateForRod(uid, rodGUID), uid, event)
}

// markFightReelBreak 记下本竿发生过断件（17/9 的断线/断竿/断轮）。断件冲击磨损
// 无官服数据、不扣寿命，次数只在最终结算时进损伤日志诊断字段。
// 必须在结算之前调用：磨损账一旦取走就不再追记。
func (s *Server) markFightReelBreak(uid int64, rodGUID string) {
	if s == nil || rodGUID == "" {
		return
	}
	st := s.fishingStateForRod(uid, rodGUID)
	st.mu.Lock()
	st.reelWear.markRodBreakLocked()
	st.mu.Unlock()
}
