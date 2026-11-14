package gameserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

const (
	catLeaderLock = int64(14)
	catWireLeader = int64(15)
)

// gearDamageEvent 是 item_damage_log.event 的稳定取值。
const (
	damageEventWear                     = "wear"
	damageEventLineBreak                = "line_break"
	damageEventLineExhausted            = "line_exhausted"
	damageEventLostOnLineBreak          = "lost_on_line_break"
	damageEventLineCut                  = "line_cut"
	damageEventLostOnLineCut            = "lost_on_line_cut"
	damageEventDetachedOnRodBreak       = "detached_on_rod_break"
	damageEventDetachedOnReelBreak      = "detached_on_reel_break"
	damageEventDetachedOnComponentBreak = "detached_on_component_break"
	damageEventRodBreak                 = "rod_break"
	damageEventReelOverload             = "reel_overload"
	damageEventComponentBreak           = "component_break"
	damageEventRepair                   = "repair"
	damageEventLostOnRodBreak           = "lost_on_rod_break"
)

type gearBreakOutcome struct {
	OverloadProtected bool // 取消破坏，不生成成功断件响应或改变当前鱼
	LineBroken        bool
	ReelDamaged       bool
	ReelBroken        bool // 轮子耐久本次被打到 0：已损坏，须修理才能再抛投。
	FrictionWorn      bool // 摩擦片(cat9)磨穿：泄力失效，最大泄力归零，需换摩擦片。
	ComponentBroken   bool
	BreakPart         string // hook / leader / line / reel / rod / component
	LineLossM         int64
	BrokenGUID        string
	RemovedGUIDs      []string
	// StackRefreshGUIDs 是被删计件事件的背包来源堆（或同物品号剩余堆），
	// 装配拆堆时服务端数量已减但客户端不知道，删除时需要一并刷新。
	StackRefreshGUIDs []string
	UpdatedTrees      []*proto.Value
	// KeepFight 表示渔轮损坏但钓组与鱼保留：搏鱼继续，等客户端断线(17/9)
	// 或剪线(17/10)收尾，不能按跑鱼处理。
	KeepFight bool
}

type gearBreakDecision struct {
	Part     string
	GUID     string
	Category int64
	// ReelSlip 区分渔轮两种损坏：true=泄力打滑（磨摩擦片 csDragForce），
	// false=锁轮超载（磨锁轮装置/本体耐久）。
	ReelSlip bool
}

// lineBreakTerminalCategories 是断线时随线一起丢失的终端组件。
// 路亚也在其中：线断后拟饵、钩、前导和饵都不可能留在竿上。
var lineBreakTerminalCategories = map[int64]bool{
	catHook: true, catLead: true, catLeaderLock: true, catWireLeader: true,
	catLure: true, catBait: true, catFloat: true,
}

// 断件不在钓组模板里时的兜底集合：子线断只损失钩组，浮漂挂在上方主线，
// 不应跟着消失；铅芯前导(cat57)在鲤族钓组里位于子线上游，同样不能列入，
// 只有铅芯自己断或主线断时才丢。模板能定位断件时一律走
// downstreamLossCategories，不使用这张表。
var leaderBreakLossCategories = map[int64]bool{
	catHook: true, catLead: true, catLeaderLock: true, catWireLeader: true,
	catLure: true, catBait: true,
}

// terminalConsumableCategories 是挂在钓组最末端的消耗件，无论断在哪一节都会
// 随下游一起丢失。浮漂、铅坠、铅芯、子线等上游件不在此列。
var terminalConsumableCategories = map[int64]bool{
	catHook: true, catLure: true, catBait: true,
}

// mainLineLossCategories 是主线崩断/剪断时随线一起丢失的全部品类。
// 不能只盯着钩和前导：铅坠、食笼、PVA、线组主体、铅芯前导这些同样挂在线上，
// 实机就出现过剪线后沉子原地留下的情况。
// 数据源用仓库自己的槽位表：slotAccepts 说明主线/前导/线组主体上能装什么，
// rigSlotTemplates 说明各钓组把哪些品类挂到线上，两者取闭包后再并上
// 历史实现已确认会丢失的终端件。竿体件（竿坯/竿柄/竿梢/渔轮/主线/灵敏梢）不进入。
var mainLineLossCategories = buildMainLineLossCategories()

func buildMainLineLossCategories() map[int64]bool {
	lost := map[int64]bool{}
	// 从主线、前导、钢丝前导出发沿槽位表向下扩展。
	queue := []int64{catLine, catLead, catWireLeader}
	visited := map[int64]bool{}
	for len(queue) > 0 {
		slot := queue[0]
		queue = queue[1:]
		if visited[slot] {
			continue
		}
		visited[slot] = true
		for cat := range slotAccepts[slot] {
			lost[cat] = true
			queue = append(queue, cat)
		}
	}
	// 钓组模板里的组件品类一律挂在线上（含 47 底钓组主体、57 铅芯前导、44 线组漂）。
	for _, tmpl := range rigSlotTemplates {
		for _, comp := range tmpl {
			for _, cat := range comp.Cats {
				lost[cat] = true
			}
		}
	}
	for cat := range lineBreakTerminalCategories {
		lost[cat] = true
	}
	delete(lost, catLine)
	delete(lost, catReel)
	return lost
}

// downstreamLossCategories 按断件在当前钓组模板中的位置给出"下游丢失"品类。
//
// rigSlotTemplates 的数组顺序就是主线往下的装配顺序，例如 24 号鲤族钓组是
// 47 食笼 → 57 铅芯 → 29 铅坠 → 13 子线 → 12 钩。旧实现把同一张品类表同时
// 用于子线、连接件和铅芯断裂，导致断子线时把上游仍在主线上的铅芯一起删掉。
// 这里只取断件位置及其之后的品类，再并上永远挂在末端的消耗件（钩/饵/拟饵）。
// 模板里同品类多槽位（如 13 出现两次）仍按品类处理：现有 rig_component
// 只按 slot 分类，没有可靠的上下游序号，不去臆造顺序。
func downstreamLossCategories(rigType, breakCategory int64) map[int64]bool {
	lost := map[int64]bool{breakCategory: true}
	for cat := range terminalConsumableCategories {
		lost[cat] = true
	}
	tmpl := rigSlotTemplates[rigType]
	pos := -1
	for i, comp := range tmpl {
		for _, cat := range comp.Cats {
			if cat == breakCategory {
				pos = i
				break
			}
		}
		if pos >= 0 {
			break
		}
	}
	if pos < 0 {
		// 断件不在模板（如独立连接件 14）或钓组类型未知：沿用窄集合，
		// 其中已不含上游的铅芯。
		for cat := range leaderBreakLossCategories {
			lost[cat] = true
		}
		return lost
	}
	for _, comp := range tmpl[pos:] {
		for _, cat := range comp.Cats {
			lost[cat] = true
		}
	}
	return lost
}

// sourceLineCategories 是可上报的线源承力件：主线、前导/钢丝前导，以及已
// 进入承力链的铅芯前导(cat57)。断任何一项都交给 resolveGearBreak 按最弱环
// 裁决；breakLineTx 再按实际品类决定是否扣主线长度（57 不扣）。
var sourceLineCategories = map[int64]bool{
	catLine: true, catLead: true, catLeaderLock: true, catWireLeader: true,
	gearCatLeadcore: true,
}

// terminalBreakCategories 是不承力的终端消耗件：拟饵、饵、浮漂。
// 这类件由客户端在打斗中直接上报断裂，上报可以采信：没有作弊收益，误判
// 成本也只是消耗件本身。钩已经进入承力链，不能在物理裁决前无条件采信，
// 否则强钩配弱线只会丢钩、不会判弱线。
var terminalBreakCategories = map[int64]bool{
	catLure: true, catBait: true, catFloat: true,
}

func ptrFloat64(v float64) *float64 { return &v }
func ptrInt64(v int64) *int64       { return &v }

// itemLengthField 读取鱼线实例的剩余长度（字段 ljnnpokapig，单位米）。
func itemLengthField(row *store.Item, fields []proto.Field) int64 {
	if row == nil {
		return 0
	}
	if idx := itemQtyFieldIdx(row.WireClass); idx >= 0 && idx < len(fields) {
		return int64(u64Of(fields[idx].Num))
	}
	if row.Qty > 0 {
		return row.Qty
	}
	return 0
}

func (s *Server) itemLength(ctx context.Context, row *store.Item) int64 {
	if row == nil {
		return 0
	}
	if fields, err := FieldsFromJSON(row.FieldsJSON); err == nil {
		if idx := itemQtyFieldIdx(row.WireClass); idx >= 0 && idx < len(fields) {
			return int64(u64Of(fields[idx].Num))
		}
	}
	if def, err := s.itemDefForRow(ctx, row); err == nil && def != nil {
		return lineLengthM(def)
	}
	return 0
}

func setItemLengthField(row *store.Item, fields []proto.Field, length int64) bool {
	if row == nil || length < 0 {
		return false
	}
	idx := itemQtyFieldIdx(row.WireClass)
	if idx < 0 || idx >= len(fields) {
		return false
	}
	fields[idx].Num = ptrU64(uint64(length))
	return true
}

func (s *Server) itemBrokenForMount(ctx context.Context, row *store.Item) bool {
	if row == nil || row.Durability <= 0 {
		return true
	}
	def, err := s.itemDefForRow(ctx, row)
	if err != nil || def == nil || def.Category != catLine {
		return false
	}
	return s.itemLength(ctx, row) <= 0
}

// breakLineOrRod 处理 17/9：客户端明确上报断件，服务端在确认归属后按上报件
// 处置。只有上报件缺失、空/zero GUID 或出厂内嵌件时才回退到最弱环裁决；
// 已存在但不受支持的装配品类视为无效上报，避免任意物品触发断件。
func (s *Server) breakLineOrRod(ctx context.Context, uid int64, rodGUID, reportedGUID string) (*gearBreakOutcome, error) {
	return s.breakLineOrRodWithLineOut(ctx, uid, rodGUID, reportedGUID, 0)
}

// breakLineOrRodWithLineOut 与 breakLineOrRod 是同一套处置逻辑，额外带上客户端
// 上报的实际放线米数（17/9 body 偏移 0x3f）：断主线与断竿都按它扣线，而不是按
// 官服的清杯口径把整卷线一起判死。断主线 lineOutM 为 0 时退回 tunable / 清杯口径，
// 断竿为 0 时主线一米不扣（竿断了不等于杯上的线断了）。
func (s *Server) breakLineOrRodWithLineOut(ctx context.Context, uid int64, rodGUID, reportedGUID string, lineOutM float64) (*gearBreakOutcome, error) {
	rodGUID = strings.TrimSpace(rodGUID)
	reportedGUID = strings.TrimSpace(reportedGUID)
	// 先确认这条上报本身合法：无效上报不能留下任何磨损，也不能把本竿的账
	// 标记成已落库，否则真正的断件上报再来时断件冲击就记不上了。
	report, err := s.validateBreakReport(ctx, uid, rodGUID, reportedGUID)
	if err != nil {
		return nil, err
	}
	s.overloadProtectionMu.RLock()
	defer s.overloadProtectionMu.RUnlock()
	protected, err := readOverloadProtection(ctx, s.DB.SQL, uid)
	if err != nil {
		return nil, err
	}
	if protected {
		return &gearBreakOutcome{OverloadProtected: true}, nil
	}
	// 断件即终局：把本竿已累计的渔轮磨损落库，否则这条账会随状态重置丢掉。
	s.markFightReelBreak(uid, rodGUID)
	s.settleFightReelWear(ctx, uid, rodGUID, "17/9")
	if report.terminalCategory != 0 {
		return s.breakComponentTx(ctx, uid, rodGUID, reportedGUID, report.terminalCategory)
	}
	// 只有客户端明确上报竿 GUID（或上报件本身是竿类）时才直接断竿。
	// 空/zero GUID 表示“未识别断件”，继续走下面的最弱环回退，
	// 物理数据也缺失时 resolveGearBreak 才兜底判竿。
	if reportedGUID == rodGUID || report.category == gearCatRod {
		return s.breakRodTx(ctx, uid, rodGUID, rodGUID, lineOutM)
	}
	if report.category != 0 {
		if s.log != nil {
			s.log.Info("17/9 按客户端上报断件", "rod", guidShort(rodGUID),
				"reported", guidShort(reportedGUID), "category", report.category)
		}
		switch {
		case report.category == catHook:
			return s.breakComponentTx(ctx, uid, rodGUID, reportedGUID, catHook)
		case report.category == gearCatReel:
			return s.damageReelLockOverload(ctx, uid, rodGUID, reportedGUID)
		case sourceLineCategories[report.category]:
			return s.breakLineTxWithLineOut(ctx, uid, rodGUID, reportedGUID, report.category, lineOutM)
		}
	}
	// 空/zero 或出厂内嵌件：回退到最弱环裁决。
	dec, err := s.resolveGearBreak(ctx, uid, rodGUID, reportedGUID)
	if err != nil {
		return nil, err
	}
	switch dec.Part {
	case "leader", "line", "leadcore", "connector":
		// 前导/子线、铅芯前导、连接件都是终端承力件，断了走断子线逻辑：
		// 主线长度不动，主线以下的终端件随断件丢失。
		return s.breakLineTxWithLineOut(ctx, uid, rodGUID, dec.GUID, dec.Category, lineOutM)
	case "hook":
		// 钩被拉直/拉断：摘除该终端件，不动主线长度。
		return s.breakComponentTx(ctx, uid, rodGUID, dec.GUID, dec.Category)
	case "reel":
		return s.damageReelLockOverload(ctx, uid, rodGUID, dec.GUID)
	default:
		brokenGUID := dec.GUID
		if report.embedded {
			brokenGUID = reportedGUID
		}
		return s.breakRodTx(ctx, uid, rodGUID, brokenGUID, lineOutM)
	}
}

type validatedBreakReport struct {
	terminalCategory int64
	category         int64
	embedded         bool
}

// validateBreakReport 只接受本人的竿和该竿上的受支持部件。终端件走独立摘除。
func (s *Server) validateBreakReport(ctx context.Context, uid int64, rodGUID, reportedGUID string) (validatedBreakReport, error) {
	rod, err := s.DB.GetItemByGUID(ctx, rodGUID)
	if err != nil || rod == nil || rod.UID != uid {
		return validatedBreakReport{}, sql.ErrNoRows
	}
	rodDef, err := s.itemDefForRow(ctx, rod)
	if err != nil || rodDef == nil || rodDef.Category != gearCatRod {
		return validatedBreakReport{}, sql.ErrNoRows
	}
	if reportedGUID == "" || reportedGUID == zeroGUID || reportedGUID == rodGUID {
		return validatedBreakReport{}, nil
	}

	part, err := s.DB.GetItemByGUID(ctx, reportedGUID)
	if errors.Is(err, sql.ErrNoRows) {
		// 出厂竿件和拟饵出厂钩只存在于母物品树内，不是独立 item 行。
		// 必须按整棵竿树深扫，不能只看竿坯/竿柄/导环三槽；否则客户端上报
		// 拟饵钩 GUID 会被误判成“不存在”，17/9 连续失败，满力度也不断件。
		embedded, treeErr := s.embeddedGUIDMountedOnRod(ctx, uid, rod, reportedGUID)
		if treeErr != nil {
			return validatedBreakReport{}, treeErr
		}
		if embedded {
			return validatedBreakReport{embedded: true}, nil
		}
		return validatedBreakReport{}, sql.ErrNoRows
	}
	if err != nil || part == nil || part.UID != uid {
		return validatedBreakReport{}, sql.ErrNoRows
	}
	def, err := s.itemDefForRow(ctx, part)
	if err != nil || def == nil {
		return validatedBreakReport{}, sql.ErrNoRows
	}
	terminal := terminalBreakCategories[def.Category]
	// 钩不再是无条件采信的终端件，但仍是客户端会主动上报的合法断件：
	// 只允许终端件和承力链上可判定的品类。已确认挂在该竿上的其他品类
	// 不能冒充断件，否则任意背包/装饰物品都可能触发真实损坏。
	if !terminal && !sourceLineCategories[def.Category] &&
		def.Category != catHook && def.Category != gearCatReel {
		return validatedBreakReport{}, errors.New("断件类型不支持")
	}

	fields, err := FieldsFromJSON(rod.FieldsJSON)
	if err != nil {
		return validatedBreakReport{}, err
	}
	mounted, err := breakReportMounted(ctx, s.DB.SQL, rod, fields, part, def.Category)
	if err != nil {
		return validatedBreakReport{}, err
	}
	if !mounted {
		return validatedBreakReport{}, errors.New("断件不在该钓组中")
	}
	if terminal {
		return validatedBreakReport{terminalCategory: def.Category, category: def.Category}, nil
	}
	return validatedBreakReport{category: def.Category}, nil
}

// embeddedGUIDMountedOnRod 检查一个没有独立 item 行的 GUID 是否属于该竿的
// 子物品树。出厂竿件在竿树里；拟饵出厂钩则在已挂拟饵自己的树里，因此不能只扫
// 竿树顶层的三个槽。
func (s *Server) embeddedGUIDMountedOnRod(ctx context.Context, uid int64, rod *store.Item, want string) (bool, error) {
	if s == nil || s.DB == nil || rod == nil || rod.UID != uid || want == "" {
		return false, nil
	}
	seen := map[string]bool{}
	check := func(row *store.Item) (bool, error) {
		if row == nil || row.UID != uid || seen[row.GUID] {
			return false, nil
		}
		seen[row.GUID] = true
		tree, err := s.ItemToTree(ctx, row)
		if err != nil {
			return false, err
		}
		return containsGUID(deepGUIDs(tree), want), nil
	}
	if ok, err := check(rod); err != nil || ok {
		return ok, err
	}
	components, err := s.DB.ListRigComponents(ctx, rod.GUID)
	if err != nil {
		return false, err
	}
	for _, comp := range components {
		row, rerr := s.DB.GetItemByGUID(ctx, comp.GUID)
		if rerr != nil {
			if errors.Is(rerr, sql.ErrNoRows) {
				continue
			}
			return false, rerr
		}
		if ok, cerr := check(row); cerr != nil || ok {
			return ok, cerr
		}
	}
	if lureGUID := s.mountedLureGUID(ctx, rod.GUID); lureGUID != "" {
		row, rerr := s.DB.GetItemByGUID(ctx, lureGUID)
		if rerr == nil {
			return check(row)
		}
		if !errors.Is(rerr, sql.ErrNoRows) {
			return false, rerr
		}
	}
	return false, nil
}

type breakReportQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func breakReportMounted(ctx context.Context, db breakReportQueryer, rod *store.Item, fields []proto.Field, part *store.Item, category int64) (bool, error) {
	if part.RigGUID != "" && part.RigGUID != rod.GUID {
		return false, nil
	}
	if name, dedicated := categorySlotField[category]; dedicated {
		if idx := fieldIdxByCS(rod.WireClass, name); idx >= 0 && idx < len(fields) && fields[idx].GuidHex() == part.GUID {
			return true, nil
		}
	} else {
		for _, comp := range rigCompsOf(protoFieldAt(fields, rod.WireClass, csRigObject)) {
			if comp.GUID == part.GUID && comp.Category == category {
				return true, nil
			}
		}
	}
	// 兼容尚未同步到竿快照的钓组投影。
	var mounted bool
	err := db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM rig_component WHERE rig_guid=? AND slot=? AND guid=?)`, rod.GUID, category, part.GUID,
	).Scan(&mounted)
	return mounted, err
}

// ---- 断件后的残留清理 ----
//
// 断钩时挂在钩上的饵必须一起离组；断竿/断轮时整条钓组都要退回背包。
// 拆分子实例（从背包大堆拆出的子件）不能在背包里再变成独立一堆，否则看起来
// 像复制物品：这里把数量并回来源堆并删除子实例。

// refundSplitChildTx 是 refundableSplitChild 的事务内版本。返回来源堆 GUID；
// ok=false 表示不是可退款的拆分子实例，调用方按普通物品退回背包。
func (s *Server) refundSplitChildTx(ctx context.Context, tx *sql.Tx, row *store.Item, qty int64) (string, bool, error) {
	if row == nil {
		return "", false, nil
	}
	srcGUID := splitSourceGUID(row)
	if len(srcGUID) != 32 || srcGUID == row.GUID {
		return "", false, nil
	}
	src, err := s.DB.GetItemByGUIDTx(ctx, tx, srcGUID)
	if err != nil || src == nil {
		return "", false, nil
	}
	// 来源堆必须仍属于同一玩家、同一物品号且留在背包，才允许并回。
	if src.UID != row.UID || src.NumericID != row.NumericID ||
		src.Container != "backpack" || src.RigGUID != "" || src.Qty <= 0 {
		return "", false, nil
	}
	if qty < 1 {
		qty = 1
	}
	if err := s.setStackCountTx(ctx, tx, src, stackCountTx(ctx, tx, src)+qty); err != nil {
		return "", false, err
	}
	if err := s.DB.DeleteItemCascadeTx(ctx, tx, row.GUID); err != nil {
		return "", false, err
	}
	return srcGUID, true, nil
}

// detachComponentToBackpackTx 把完好组件摘回背包：清钓组引用、落字段并清
// rig_guid。拆分子实例不要走这里，先由 refundSplitChildTx 处理。
func (s *Server) detachComponentToBackpackTx(ctx context.Context, tx *sql.Tx, row *store.Item) error {
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return err
	}
	row.Container = "backpack"
	row.Slot = 0
	row.RigGUID = ""
	clearRigFieldsForBackpack(fields, row.WireClass)
	if err := updateItemFieldsTx(ctx, s.DB, tx, row, fields); err != nil {
		return err
	}
	return s.DB.DetachReturnToBackpackTx(ctx, tx, row.GUID)
}

// detachOnBreakTx 断竿/断轮/断钩时把完好组件摘回背包；拆分子实例并回来源堆。
// 返回需要刷新的来源堆 GUID（为空表示没有退款）。
func (s *Server) detachOnBreakTx(ctx context.Context, tx *sql.Tx, uid int64, rodGUID string, row *store.Item, event, reason, brokenGUID string) (string, error) {
	srcGUID, refunded, err := s.refundSplitChildTx(ctx, tx, row, baitQuantity(row))
	if err != nil {
		return "", err
	}
	if !refunded {
		if err := s.detachComponentToBackpackTx(ctx, tx, row); err != nil {
			return "", err
		}
	}
	if err := store.AddItemDamageLog(ctx, tx, store.ItemDamageLog{
		UID: uid, ItemGUID: row.GUID, ItemID: row.ItemID, Event: event,
		BeforeDurability: ptrFloat64(row.Durability), AfterDurability: ptrFloat64(row.Durability),
		BrokenGUID: brokenGUID,
		DetailJSON: damageDetailJSON(map[string]any{"rod_guid": rodGUID, "reason": reason}),
	}); err != nil {
		return "", err
	}
	return srcGUID, nil
}

// clearRodBaitSettingsTx 删除该竿上的自动补饵请求和饵-钩映射。断竿/断轮后
// 钓组已清空，这些记录继续留着会在修好竿第一次抛竿时把饵挂回去。
func clearRodBaitSettingsTx(ctx context.Context, tx *sql.Tx, rodGUID string) error {
	for _, pattern := range []string{"rebait:%", "bait_hook:%"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM rod_setting WHERE guid=? AND key LIKE ?`, rodGUID, pattern); err != nil {
			return err
		}
	}
	return nil
}

// clearRebaitForHookTx 清除会往该钩补饵的 rebait 记录：JSON 里带同一钩 GUID
// 的记录，以及按钩位序号记录（旧格式）的那一条。
func (s *Server) clearRebaitForHookTx(ctx context.Context, tx *sql.Tx, rodGUID, hookGUID string, comps map[int64]rigComp) error {
	keys := map[string]bool{}
	if hookGUID != "" {
		rows, err := tx.QueryContext(ctx, `SELECT key,value FROM rod_setting WHERE guid=? AND key LIKE 'rebait:%'`, rodGUID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var key, value string
			if err := rows.Scan(&key, &value); err != nil {
				rows.Close()
				return err
			}
			if !strings.HasPrefix(value, "{") {
				continue
			}
			var req rebaitRequest
			if json.Unmarshal([]byte(value), &req) == nil && req.HookGUID == hookGUID {
				keys[key] = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	if key, _, ok := rigCompKeyForGUID(comps, hookGUID); ok {
		keys[fmt.Sprintf("rebait:%d", rigCompOrdinal(key))] = true
	}
	for key := range keys {
		if _, err := tx.ExecContext(ctx, `DELETE FROM rod_setting WHERE guid=? AND key=?`, rodGUID, key); err != nil {
			return err
		}
	}
	return nil
}

// clearComponentBaitSettingsTx 处理终端件自身断裂：删掉它的 bait_hook 映射，
// 并清掉会把它补回同一钩的 rebait 记录，避免断掉的饵又被自动补一份。
func (s *Server) clearComponentBaitSettingsTx(ctx context.Context, tx *sql.Tx, rodGUID, compGUID string, comps map[int64]rigComp) error {
	hook := ""
	if err := tx.QueryRowContext(ctx, `SELECT value FROM rod_setting WHERE guid=? AND key=?`,
		rodGUID, "bait_hook:"+compGUID).Scan(&hook); err != nil && err != sql.ErrNoRows {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM rod_setting WHERE guid=? AND key=?`,
		rodGUID, "bait_hook:"+compGUID); err != nil {
		return err
	}
	if hook != "" {
		return s.clearRebaitForHookTx(ctx, tx, rodGUID, hook, comps)
	}
	if key, _, ok := rigCompKeyForGUID(comps, compGUID); ok {
		if _, err := tx.ExecContext(ctx, `DELETE FROM rod_setting WHERE guid=? AND key=?`,
			rodGUID, fmt.Sprintf("rebait:%d", rigCompOrdinal(key))); err != nil {
			return err
		}
	}
	return nil
}

// detachHookBaitsTx 断钩时把挂在断钩上的饵/拟饵一并摘回背包（或并回来源堆），
// 并删除对应 bait_hook 映射与自动补饵请求。没有显式映射的单钩钓组也按钩位
// 推断，避免留下"有饵无钩"。
func (s *Server) detachHookBaitsTx(ctx context.Context, tx *sql.Tx, uid int64, rodGUID, hookGUID string, comps map[int64]rigComp) (removed, stackRefresh []string, err error) {
	mapped := map[string]string{}
	rows, qerr := tx.QueryContext(ctx, `SELECT key,value FROM rod_setting WHERE guid=? AND key LIKE 'bait_hook:%'`, rodGUID)
	if qerr != nil {
		return nil, nil, qerr
	}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			rows.Close()
			return nil, nil, err
		}
		mapped[strings.TrimPrefix(key, "bait_hook:")] = value
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}

	hooks := rigCompKeysOfCategory(comps, catHook)
	singleHook := len(hooks) <= 1
	hookExists := map[string]bool{}
	for _, key := range hooks {
		hookExists[comps[key].GUID] = true
	}
	var baitGUIDs []string
	for _, cat := range []int64{catBait, catLure} {
		for _, key := range rigCompKeysOfCategory(comps, cat) {
			baitGUID := comps[key].GUID
			if baitGUID == "" {
				continue
			}
			mappedHook := mapped[baitGUID]
			switch {
			case mappedHook == hookGUID:
			case singleHook && mappedHook == "":
			case singleHook && mappedHook != "" && !hookExists[mappedHook]:
				// 单钩钓组的映射指向已经不存在的钩：饵同样无法继续挂载。
			default:
				continue
			}
			baitGUIDs = append(baitGUIDs, baitGUID)
		}
	}
	sort.Strings(baitGUIDs)
	for _, baitGUID := range baitGUIDs {
		row, ierr := s.DB.GetItemByGUIDTx(ctx, tx, baitGUID)
		if ierr != nil {
			return nil, nil, ierr
		}
		delete(mapped, baitGUID)
		if _, derr := tx.ExecContext(ctx, `DELETE FROM rod_setting WHERE guid=? AND key=?`, rodGUID, "bait_hook:"+baitGUID); derr != nil {
			return nil, nil, derr
		}
		if row != nil && row.UID == uid && row.RigGUID == rodGUID {
			srcGUID, derr := s.detachOnBreakTx(ctx, tx, uid, rodGUID, row,
				damageEventDetachedOnComponentBreak, "hook_break", hookGUID)
			if derr != nil {
				return nil, nil, derr
			}
			if srcGUID != "" {
				stackRefresh = append(stackRefresh, srcGUID)
			}
			removed = append(removed, baitGUID)
		}
		if _, derr := tx.ExecContext(ctx, `DELETE FROM rig_component WHERE rig_guid=? AND guid=?`, rodGUID, baitGUID); derr != nil {
			return nil, nil, derr
		}
		if key, _, ok := rigCompKeyForGUID(comps, baitGUID); ok {
			delete(comps, key)
		}
	}
	// 指向断钩的所有映射都作废（包括饵已不在钓组的脏记录）。
	for baitGUID, mappedHook := range mapped {
		if mappedHook == hookGUID {
			if _, derr := tx.ExecContext(ctx, `DELETE FROM rod_setting WHERE guid=? AND key=?`, rodGUID, "bait_hook:"+baitGUID); derr != nil {
				return nil, nil, derr
			}
		}
	}
	if err := s.clearRebaitForHookTx(ctx, tx, rodGUID, hookGUID, comps); err != nil {
		return nil, nil, err
	}
	return removed, stackRefresh, nil
}

// breakComponentTx 处理终端件断裂：钩/拟饵/饵/浮漂断裂即丢失销毁（官服口径），
// 竿本体与承力线不受影响。拟饵出厂钩没有独立物品行，客户端看到的是拟饵没了。
func (s *Server) breakComponentTx(ctx context.Context, uid int64, rodGUID, compGUID string, category int64) (*gearBreakOutcome, error) {
	tx, err := s.DB.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rod, err := s.DB.GetItemByGUIDTx(ctx, tx, rodGUID)
	if err != nil || rod == nil || rod.UID != uid {
		return nil, sql.ErrNoRows
	}
	comp, err := s.DB.GetItemByGUIDTx(ctx, tx, compGUID)
	if err != nil || comp == nil || comp.UID != uid {
		return nil, sql.ErrNoRows
	}
	rodFields, err := FieldsFromJSON(rod.FieldsJSON)
	if err != nil {
		return nil, err
	}
	mounted, err := breakReportMounted(ctx, tx, rod, rodFields, comp, category)
	if err != nil {
		return nil, err
	}
	if !mounted {
		return nil, errors.New("断件不在该钓组中")
	}
	before := comp.Durability
	// 投影可能先于 f5 存在；摘除投影不能依赖 f5 是否命中。
	if _, err := tx.ExecContext(ctx, `DELETE FROM rig_component WHERE rig_guid=? AND guid=?`, rodGUID, comp.GUID); err != nil {
		return nil, err
	}

	// 从 f5 钓组存储中摘除该组件；断钩时还要把挂在钩上的饵一起摘掉。
	stored := protoFieldAt(rodFields, rod.WireClass, csRigObject)
	comps := rigCompsOf(stored)
	var removed, stackRefresh []string
	if category == catHook {
		baitsRemoved, stacks, derr := s.detachHookBaitsTx(ctx, tx, uid, rodGUID, comp.GUID, comps)
		if derr != nil {
			return nil, derr
		}
		removed = append(removed, baitsRemoved...)
		stackRefresh = append(stackRefresh, stacks...)
	} else if err := s.clearComponentBaitSettingsTx(ctx, tx, rodGUID, comp.GUID, comps); err != nil {
		return nil, err
	}
	detached := false
	for key, c := range comps {
		if c.GUID == comp.GUID {
			delete(comps, key)
			detached = true
			break
		}
	}
	if detached || len(removed) > 0 {
		name, rigType, typeOK := rigIdentity(stored)
		if !typeOK {
			rigType = s.defaultRigTypeForRod(ctx, rod.ItemID)
			name = rigNameForType(rigType)
		}
		depth := rigDepthOf(stored, defaultDepthFor(name, rigType))
		if idx := fieldIdxByCS(rod.WireClass, csRigObject); idx >= 0 && idx < len(rodFields) {
			rodFields[idx].Op = "VAL"
			rodFields[idx].Val = buildRig153Like(stored, name, rigType, comps, depth, rigClipOf(stored))
		}
		if err := updateItemFieldsTx(ctx, s.DB, tx, rod, rodFields); err != nil {
			return nil, err
		}
	}
	// 官服口径：断件即丢失销毁，不再退背包（钩/拟饵/饵/浮漂都是消耗件）。
	stackNumericIDs := map[int64]bool{}
	stackSourceGUIDs := map[string]bool{}
	if countableCategories[category] {
		if srcGUID := splitSourceGUID(comp); len(srcGUID) == 32 {
			stackSourceGUIDs[srcGUID] = true
		}
		stackNumericIDs[comp.NumericID] = true
	}
	if err := s.DB.DeleteItemCascadeTx(ctx, tx, comp.GUID); err != nil {
		return nil, err
	}
	entry := store.ItemDamageLog{
		UID: uid, ItemGUID: comp.GUID, ItemID: comp.ItemID, Event: damageEventComponentBreak,
		BeforeDurability: ptrFloat64(before), AfterDurability: ptrFloat64(0), BrokenGUID: comp.GUID,
		DetailJSON: damageDetailJSON(map[string]any{"rod_guid": rodGUID}),
	}
	if err := store.AddItemDamageLog(ctx, tx, entry); err != nil {
		return nil, err
	}
	removed = append(removed, comp.GUID)
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// 子实例销毁后来源堆数量在装配时已减、客户端没收到推送，需要 4/22 对齐；
	// 事务提交后再查（单连接测试库在事务内另开连接会死锁）。
	stackRefresh = append(stackRefresh, s.backpackStackGUIDsForItems(ctx, uid, stackNumericIDs, stackSourceGUIDs)...)
	stackRefresh = uniqueStrings(stackRefresh)
	updated := append([]string{rodGUID}, stackRefresh...)
	out := &gearBreakOutcome{
		ComponentBroken: true, BreakPart: "component", BrokenGUID: comp.GUID,
		RemovedGUIDs: removed, StackRefreshGUIDs: stackRefresh,
		UpdatedTrees: s.loadUpdatedTrees(ctx, updated),
	}
	return out, nil
}

// resolveGearBreak 按真实装配决定超载对象。手竿比较竿/主线/子线；
// 带轮竿额外比较当前耐久阶段的锁轮承力。
func (s *Server) resolveGearBreak(ctx context.Context, uid int64, rodGUID, reportedGUID string) (gearBreakDecision, error) {
	rod, err := s.DB.GetItemByGUID(ctx, rodGUID)
	if err != nil || rod == nil || rod.UID != uid {
		return gearBreakDecision{}, sql.ErrNoRows
	}
	ids := s.rigGearIDs(ctx, uid, rodGUID)
	spec := s.loadGearSpecForRig(ctx, ids, rodGUID)

	lineGUID := ""
	if g, gerr := s.DB.GetItemGUIDByRigSlot(ctx, rodGUID, catLine); gerr == nil {
		lineGUID = g
	}
	if lineGUID == "" {
		lineGUID, _ = s.storedRigItemGUID(ctx, rod, csLine)
	}
	// 引线 GUID 与最弱环计算同源：rigGearIDs 已经把竿实例的钓组对象和
	// rig_component 派生索引合并解析过（优先 15 前导线，回退 13 子线），
	// 这里不能再只读实例字段，否则只在派生索引里有装配时断件又会漏掉引线。
	leaderGUID := ids.LeaderGUID
	if leaderGUID == "" {
		leaderGUID = s.rigComponentGuidOf(ctx, rodGUID, catWireLeader)
	}
	if leaderGUID == "" {
		leaderGUID = s.rigComponentGuidOf(ctx, rodGUID, gearCatLeader)
	}
	// 钩也承力：大鱼吃小钩时，断的是钩而不是主线。有独立物品行就断钩；
	// 拟饵出厂钩没有物品行，退而断整个拟饵（客户端看到的是拟饵没了）。
	hookGUID := ""
	hookCat := int64(0)
	if g := s.mountedHookGuid(ctx, rodGUID); g != "" {
		if row, rerr := s.DB.GetItemByGUID(ctx, g); rerr == nil && row != nil && row.UID == uid {
			hookGUID, hookCat = g, catHook
		}
	}
	if hookGUID == "" {
		if lure := s.mountedLureGUID(ctx, rodGUID); lure != "" {
			if row, rerr := s.DB.GetItemByGUID(ctx, lure); rerr == nil && row != nil && row.UID == uid {
				hookGUID, hookCat = lure, catLure
			}
		}
	}

	type candidate struct {
		part     string
		guid     string
		category int64
		kg       float64
		priority int
	}
	var cs []candidate
	if lineGUID != "" && spec.LineStrengthKg > 0 {
		cs = append(cs, candidate{part: "line", guid: lineGUID, category: catLine, kg: spec.LineStrengthKg, priority: 1})
	}
	if leaderGUID != "" && spec.LeaderStrengthKg > 0 {
		leaderCat := catWireLeader
		leaderRow, _ := s.DB.GetItemByGUID(ctx, leaderGUID)
		if leaderRow != nil {
			if def, derr := s.itemDefForRow(ctx, leaderRow); derr == nil && def != nil {
				leaderCat = def.Category
			}
		}
		cs = append(cs, candidate{part: "leader", guid: leaderGUID, category: leaderCat, kg: spec.LeaderStrengthKg, priority: 0})
	}
	// 铅芯前导（57）与独立连接件（14）是并列的终端承力件，各自有拉力，
	// 与前导/子线一样是短链短板，同强度时先断这些便宜件。
	if ids.LeadcoreGUID != "" && spec.LeadcoreStrengthKg > 0 {
		cs = append(cs, candidate{part: "leadcore", guid: ids.LeadcoreGUID, category: gearCatLeadcore,
			kg: spec.LeadcoreStrengthKg, priority: 0})
	}
	if ids.ConnectorGUID != "" && spec.ConnectorStrengthKg > 0 {
		cs = append(cs, candidate{part: "connector", guid: ids.ConnectorGUID, category: catLeaderLock,
			kg: spec.ConnectorStrengthKg, priority: 0})
	}
	if spec.RodPowerKg > 0 {
		cs = append(cs, candidate{part: "rod", guid: rodGUID, category: gearCatRod, kg: spec.RodPowerKg, priority: 3})
	}
	// 钩与线组同级：同强度时先断子线，再断钩。
	if hookGUID != "" && spec.HookStrengthKg > 0 {
		cs = append(cs, candidate{part: "hook", guid: hookGUID, category: hookCat,
			kg: spec.HookStrengthKg, priority: 1})
	}

	// 17/9 是客户端明确上报的"断件"事件：只要来了就必须断，摩擦档位不能把
	// 它改成无损坏的泄力打滑。即使 23/24 档，鱼力尖峰仍可能拉坏最弱件；
	// 旧实现把"最弱件强于泄力上限"判成打滑，导致客户端已提示断线、服务端
	// 却保留整组钓具。线上 29（UI 30）锁轮后锁轮承力才作为候选参与比较；
	// 泄力打滑是另一条独立事件，不再从 17/9 推断。
	setting, _ := s.rodReelDragSetting(ctx, rodGUID)
	if spec.ReelPresent && ids.ReelGUID != "" && setting >= maxReelDragStop && spec.ReelDragKg > 0 {
		cs = append(cs, candidate{part: "reel", guid: ids.ReelGUID, category: gearCatReel,
			kg: spec.ReelDragKg, priority: 2})
	}

	if len(cs) > 0 {
		best := cs[0]
		for _, c := range cs[1:] {
			if c.kg < best.kg-1e-9 || (math.Abs(c.kg-best.kg) <= 1e-9 && c.priority < best.priority) {
				best = c
			}
		}
		if s.log != nil {
			kg2 := func(v float64) float64 { return math.Round(v*100) / 100 }
			s.log.Info("17/9 断件裁决", "rod", guidShort(rodGUID), "part", best.part,
				"drag_setting", setting, "kg", kg2(best.kg), "rod_kg", kg2(spec.RodPowerKg),
				"line_kg", kg2(spec.LineStrengthKg), "leader_kg", kg2(spec.LeaderStrengthKg),
				"hook_kg", kg2(spec.HookStrengthKg),
				"reel_lock_kg", kg2(spec.ReelDragKg), "reported", guidShort(reportedGUID))
		}
		return gearBreakDecision{Part: best.part, GUID: best.guid, Category: best.category}, nil
	}

	// 物理数据缺失时，按客户端上报品类做兼容回退。
	if reportedGUID == "" || reportedGUID == zeroGUID {
		return gearBreakDecision{Part: "rod", GUID: rodGUID, Category: gearCatRod}, nil
	}
	if row, rerr := s.DB.GetItemByGUID(ctx, reportedGUID); rerr == nil && row != nil {
		if def, derr := s.itemDefForRow(ctx, row); derr == nil && def != nil {
			switch {
			case def.Category == catHook || def.Category == catLure:
				return gearBreakDecision{Part: "hook", GUID: reportedGUID, Category: def.Category}, nil
			case def.Category == gearCatLeadcore:
				return gearBreakDecision{Part: "leadcore", GUID: reportedGUID, Category: def.Category}, nil
			case sourceLineCategories[def.Category]:
				return gearBreakDecision{Part: "line", GUID: reportedGUID, Category: def.Category}, nil
			}
		}
	}
	return gearBreakDecision{Part: "rod", GUID: rodGUID, Category: gearCatRod}, nil
}

// damageReelSlip 处理"泄力打滑"：鱼力超过泄力档时，摩擦片打滑放线、发热磨损，
// 制动力 csDragForce 线性衰减（基准 reelSlipWear=0.01%/次）→ 泄力失效。
// 摩擦片是"只能摩擦不能损坏"的消耗件，磨穿只泄力失效、不伤本体，换片即恢复。
func (s *Server) damageReelSlip(ctx context.Context, uid int64, rodGUID, reelGUID string) (*gearBreakOutcome, error) {
	return s.damageReelSlipWear(ctx, uid, rodGUID, reelGUID, reelSlipWear, nil)
}

// damageReelSlipWear 按 wear（摩擦片寿命比例）磨一次摩擦片并落库。wear 由调用方汇总
// 给出：搏鱼结算把整竿累计的泄力打滑与锁轮过载合成一次写入，避免每条上报都写库。
// extra 是本次结算的诊断字段（打滑次数、过载秒数、当时张力状态）。
func (s *Server) damageReelSlipWear(ctx context.Context, uid int64, rodGUID, reelGUID string, wear float64, extra map[string]any) (*gearBreakOutcome, error) {
	reel, err := s.DB.GetItemByGUID(ctx, reelGUID)
	if err != nil || reel == nil || reel.UID != uid {
		return nil, sql.ErrNoRows
	}
	before := reel.Durability

	wornOut, found, derr := s.degradeFrictionWasherBy(ctx, reel, wear)
	if derr != nil {
		return nil, derr
	}
	if !found {
		// 旧档回退：没有独立摩擦片子件，磨本体耐久。
		return s.damageReelLockOverload(ctx, uid, rodGUID, reelGUID)
	}

	// 本体那一格是轮内零件的综合：摩擦片被磨薄了，外面必须跟着降（只降不升，
	// 换件与修理才允许把它抬回去）。
	if fresh, ferr := s.DB.GetItemByGUID(ctx, reel.GUID); ferr == nil && fresh != nil {
		if fields, ferr := FieldsFromJSON(fresh.FieldsJSON); ferr == nil {
			if body, ok := s.reelBodyDurabilityInFields(ctx, fresh, fields); ok && body < fresh.Durability {
				if derr := s.DB.SetItemDurability(ctx, fresh.GUID, body); derr == nil {
					fresh.Durability = body
				}
			}
		}
		reel = fresh
	}
	perf, perr := s.syncReelPerformance(ctx, reel)
	if perr != nil {
		return nil, perr
	}
	if err := s.DB.AddItemDamageLog(ctx, store.ItemDamageLog{
		UID: uid, ItemGUID: reel.GUID, ItemID: reel.ItemID, Event: damageEventReelOverload,
		BeforeDurability: ptrFloat64(before), AfterDurability: ptrFloat64(reel.Durability),
		DetailJSON: frictionWearDetail(rodGUID, perf, reel.Durability, wornOut, extra),
	}); err != nil {
		return nil, err
	}
	updated := s.loadUpdatedTrees(ctx, []string{rodGUID, reelGUID})
	return &gearBreakOutcome{
		ReelDamaged: true, FrictionWorn: wornOut, KeepFight: true,
		BreakPart: "reel", BrokenGUID: reelGUID, UpdatedTrees: updated,
	}, nil
}

// damageReelLockOverload 处理"渔轮被拉坏"：客户端上报渔轮断件（或回退裁决判渔轮）时，
// 官方表现是耐久直接归 0、轮子留在竿上但不可用，而不是分多档磨损。
// 官服断轮口径：钓组（主线/引线/钩/饵等）原样保留，鱼仍挂钩，只是无法收线；
// 搏鱼继续，等客户端断线(17/9)按最弱环裁决或玩家剪线(17/10)收尾。
// 再次抛投由 rigBrokenReelGUID 拦截；泄力打滑磨摩擦片是另一条独立通道，
// 仍由 damageReelSlip 处理。
func (s *Server) damageReelLockOverload(ctx context.Context, uid int64, rodGUID, reelGUID string) (*gearBreakOutcome, error) {
	tx, err := s.DB.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rod, err := s.DB.GetItemByGUIDTx(ctx, tx, rodGUID)
	if err != nil || rod == nil || rod.UID != uid {
		return nil, sql.ErrNoRows
	}
	reel, err := s.DB.GetItemByGUIDTx(ctx, tx, reelGUID)
	if err != nil || reel == nil || reel.UID != uid {
		return nil, sql.ErrNoRows
	}
	before := reel.Durability
	reelFields, err := FieldsFromJSON(reel.FieldsJSON)
	if err != nil {
		return nil, err
	}
	cIdx := fieldIdxByCS(reel.WireClass, csDurability)
	if cIdx < 0 || !setWireFloat(reelFields, reel.WireClass, cIdx, 0) {
		return nil, errDurabilityField
	}
	reel.Durability = 0
	if err := updateItemFieldsTx(ctx, s.DB, tx, reel, reelFields); err != nil {
		return nil, err
	}

	// 官服断轮口径：轮子耐久归零、留在竿上；主线、前导、钩饵等钓组组件
	// 原样保留，鱼也没跑，只是收不动线。后续由客户端断线(17/9)或剪线(17/10)
	// 收尾，那时再按最弱环/清杯流程清理钓组。
	if err := store.AddItemDamageLog(ctx, tx, store.ItemDamageLog{
		UID: uid, ItemGUID: reel.GUID, ItemID: reel.ItemID, Event: damageEventReelOverload,
		BeforeDurability: ptrFloat64(before), AfterDurability: ptrFloat64(0),
		DetailJSON: damageDetailJSON(map[string]any{
			"rod_guid": rodGUID, "lock_multiplier": reelLockMultiplier(0),
			"wear_progress": 0, "reel_broken": true,
		}),
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	if fresh, ferr := s.DB.GetItemByGUID(ctx, reelGUID); ferr == nil && fresh != nil {
		perf, perr := s.syncReelPerformance(ctx, fresh)
		if perr != nil {
			return nil, perr
		}
		perf.WearProgress = 0
		if err := s.DB.UpsertReelPerformance(ctx, *perf); err != nil {
			return nil, err
		}
	}
	updated := []string{rodGUID, reelGUID}
	return &gearBreakOutcome{
		ReelDamaged: true, ReelBroken: true, KeepFight: true,
		BreakPart: "reel", BrokenGUID: reelGUID,
		UpdatedTrees: s.loadUpdatedTrees(ctx, updated),
	}, nil
}

// rigBrokenReelGUID 返回竿上那只耐久归零的渔轮 GUID；没有轮子或轮子完好时返回空串。
// 轮子损坏后仍留在竿上（不像线会从钓组消失），所以抛竿前必须单独校验，
// 否则损坏的轮子还能继续上鱼。
func (s *Server) rigBrokenReelGUID(ctx context.Context, uid int64, rodGUID string) string {
	if s == nil || s.DB == nil || rodGUID == "" || rodGUID == zeroGUID {
		return ""
	}
	ids := s.rigGearIDs(ctx, uid, rodGUID)
	if strings.TrimSpace(ids.ReelGUID) == "" {
		return ""
	}
	row, err := s.DB.GetItemByGUID(ctx, ids.ReelGUID)
	if err != nil || row == nil || row.UID != uid {
		return ""
	}
	if row.Durability > 0 {
		return ""
	}
	return row.GUID
}

func (s *Server) storedRigItemGUID(ctx context.Context, rod *store.Item, csName string) (string, error) {
	if rod == nil {
		return "", nil
	}
	fields, err := FieldsFromJSON(rod.FieldsJSON)
	if err != nil {
		return "", err
	}
	idx := fieldIdxByCS(rod.WireClass, csName)
	if idx < 0 || idx >= len(fields) {
		return "", nil
	}
	return fields[idx].GuidHex(), nil
}

func updateItemFieldsTx(ctx context.Context, db *store.DB, tx *sql.Tx, row *store.Item, fields []proto.Field) error {
	bumpCreatedTicks(fields, row.WireClass)
	fj, err := FieldsToJSON(fields)
	if err != nil {
		return err
	}
	if err := db.UpdateItemFieldsTx(ctx, tx, row.GUID, fj, row.Qty, row.Durability, row.Workmanship, row.RigGUID); err != nil {
		return err
	}
	row.FieldsJSON = fj
	return nil
}

// lineLossPolicy 描述一次主线/子线长度损失的来源与记账方式。
// 断线（17/9）和剪线（17/10）共用同一套"清掉主线以下终端件"的
// 落库逻辑：断主线=清杯整条丢失，断子线不动主线，剪线按上报米数扣。
type lineLossPolicy struct {
	// requestedM 本次要扣掉的米数（仅剪线使用）。
	requestedM int64
	// lineOutM 客户端上报的实际放线米数（17/9 断件时携带）：>0 时按它扣主线，
	// 否则退回 tunable line_break_loss_m / 清杯口径。
	lineOutM float64
	// destroyLine 断主线语义：清杯后整条线丢失（17/9）；剪线只按米扣。
	destroyLine bool
	// eventLine 记录主线长度变化的事件名；eventLost 记录随线丢失的配件。
	eventLine string
	eventLost string
	// reason 写进 damage log detail 的成因标记。
	reason string
}

// breakLineTx 处理 17/9 断线：主线固定损失 5 米，断子线不扣主线长度。
// rodLineLengthLeft 返回竿上主线的剩余米数，仅用于日志；没有主线时返回 0。
func (s *Server) rodLineLengthLeft(ctx context.Context, rodGUID string) int64 {
	if s.DB == nil {
		return 0
	}
	rod, err := s.DB.GetItemByGUID(ctx, rodGUID)
	if err != nil || rod == nil {
		return 0
	}
	lineGUID, _ := s.storedRigItemGUID(ctx, rod, csLine)
	if lineGUID == "" {
		return 0
	}
	lineRow, err := s.DB.GetItemByGUID(ctx, lineGUID)
	if err != nil || lineRow == nil {
		return 0
	}
	return s.itemLength(ctx, lineRow)
}

func (s *Server) breakLineTx(ctx context.Context, uid int64, rodGUID, brokenGUID string, breakCategory int64) (*gearBreakOutcome, error) {
	return s.breakLineTxWithLineOut(ctx, uid, rodGUID, brokenGUID, breakCategory, 0)
}

// breakLineTxWithLineOut 同 breakLineTx，额外带上客户端上报的放线米数。
func (s *Server) breakLineTxWithLineOut(ctx context.Context, uid int64, rodGUID, brokenGUID string, breakCategory int64, lineOutM float64) (*gearBreakOutcome, error) {
	return s.lineLossTx(ctx, uid, rodGUID, brokenGUID, breakCategory, lineLossPolicy{
		destroyLine: true,
		lineOutM:    lineOutM,
		eventLine:   damageEventLineBreak,
		eventLost:   damageEventLostOnLineBreak,
		reason:      "line_break",
	})
}

// cutLineTx 处理 17/10 剪线：客户端上报本次放出多少米，竿上主线就少多少米；
// 主线以下的前导、子线、钩、饵、拟饵、浮漂全部随线丢失，主线本体保留。
func (s *Server) cutLineTx(ctx context.Context, uid int64, rodGUID string, cutM int64) (*gearBreakOutcome, error) {
	if cutM <= 0 {
		return nil, errors.New("剪线长度必须大于 0")
	}
	return s.lineLossTx(ctx, uid, rodGUID, "", catLine, lineLossPolicy{
		requestedM: cutM,
		eventLine:  damageEventLineCut,
		eventLost:  damageEventLostOnLineCut,
		reason:     "line_cut",
	})
}

func (s *Server) lineLossTx(ctx context.Context, uid int64, rodGUID, brokenGUID string, breakCategory int64, policy lineLossPolicy) (*gearBreakOutcome, error) {
	tx, err := s.DB.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rod, err := s.DB.GetItemByGUIDTx(ctx, tx, rodGUID)
	if err != nil || rod == nil || rod.UID != uid {
		return nil, sql.ErrNoRows
	}
	rodFields, err := FieldsFromJSON(rod.FieldsJSON)
	if err != nil {
		return nil, err
	}
	brokenRow, _ := s.DB.GetItemByGUIDTx(ctx, tx, brokenGUID)

	lineGUID, _ := s.storedRigItemGUID(ctx, rod, csLine)
	if lineGUID == "" {
		if row, rerr := s.DB.GetItemByGUID(ctx, brokenGUID); rerr == nil && row != nil {
			if def, derr := s.itemDefForRow(ctx, row); derr == nil && def != nil && def.Category == catLine {
				lineGUID = brokenGUID
			}
		}
	}
	var lineRow *store.Item
	var lineFields []proto.Field
	beforeLength, afterLength, lossLength := int64(0), int64(0), int64(0)
	mainLineBreak := breakCategory == catLine
	if lineGUID != "" {
		lineRow, _ = s.DB.GetItemByGUIDTx(ctx, tx, lineGUID)
		if lineRow != nil {
			lineFields, err = FieldsFromJSON(lineRow.FieldsJSON)
			if err != nil {
				return nil, err
			}
			beforeLength = itemLengthField(lineRow, lineFields)
			if beforeLength <= 0 {
				if def, derr := s.itemDefForRow(ctx, lineRow); derr == nil && def != nil {
					beforeLength = lineLengthM(def)
				}
			}
			afterLength = beforeLength
			if mainLineBreak {
				lossLength = policy.requestedM
				if policy.destroyLine {
					// 官服清杯口径：主线被拉断时整条丢失（含挂线配件）。
					// afterLength=0 走下方"线耗尽删除"分支，f3 引用一并清理。
					lossLength = beforeLength
					// 断主线按实际放出量扣线：客户端上报值优先，其次 tunable
					// line_break_loss_m。都没给才保持清杯口径。
					applied := false
					if policy.lineOutM > 0 {
						// 客户端上报的实际放线量：断线时真正被拉出去的那段。
						lossLength = int64(policy.lineOutM + 0.5)
						applied = true
					} else if fixed := s.tunableF("line_break_loss_m", 0); fixed > 0 {
						lossLength = int64(fixed)
						applied = true
					}
					// 上报了正数但不足 1 米：至少扣 1 米。否则 0.01 这种值
					// 取整成 0，断线会变成"零损耗"。
					if applied && lossLength < 1 {
						lossLength = 1
					}
				}
				if lossLength > beforeLength {
					lossLength = beforeLength
				}
				if lossLength < 0 {
					lossLength = 0
				}
				afterLength = beforeLength - lossLength
				if afterLength > 0 {
					if !setItemLengthField(lineRow, lineFields, afterLength) {
						return nil, errLineLengthField
					}
					if err := updateItemFieldsTx(ctx, s.DB, tx, lineRow, lineFields); err != nil {
						return nil, err
					}
				}
			}
		}
	}

	stored := protoFieldAt(rodFields, rod.WireClass, csRigObject)
	// 主线断了就按"挂在线上的东西全部没了"处理；只断下游某一节时，按该节在
	// 钓组模板中的位置取下游集合，线上方的浮漂、铅坠和铅芯必须留在主线上。
	lossCategories := mainLineLossCategories
	if !mainLineBreak {
		_, rigType, typeOK := rigIdentity(stored)
		if !typeOK {
			rigType = s.defaultRigTypeForRod(ctx, rod.ItemID)
		}
		lossCategories = downstreamLossCategories(rigType, breakCategory)
	}
	var removed []string
	terminalRows := map[string]*store.Item{}
	var terminalGUIDs []string
	if rows, qerr := tx.QueryContext(ctx, `SELECT slot, COALESCE(guid,'') FROM rig_component WHERE rig_guid=?`, rodGUID); qerr == nil {
		for rows.Next() {
			var slot int64
			var guid string
			if err := rows.Scan(&slot, &guid); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if guid != "" && lossCategories[slot] {
				terminalGUIDs = append(terminalGUIDs, guid)
			}
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
	}
	for _, guid := range terminalGUIDs {
		if row, ierr := s.DB.GetItemByGUIDTx(ctx, tx, guid); ierr == nil && row != nil {
			terminalRows[guid] = row
		}
	}
	// f5 中可能还有尚未投影到 rig_component 的组件，按类别一并清理。
	for _, comp := range rigCompsOf(stored) {
		if comp.GUID == "" || !lossCategories[comp.Category] {
			continue
		}
		if _, ok := terminalRows[comp.GUID]; !ok {
			if row, ierr := s.DB.GetItemByGUIDTx(ctx, tx, comp.GUID); ierr == nil && row != nil {
				terminalRows[comp.GUID] = row
			}
		}
	}

	stackNumericIDs := map[int64]bool{}
	stackSourceGUIDs := map[string]bool{}
	terminalCategories := map[string]int64{}
	for _, row := range terminalRows {
		// 事务内不能走 stackableItem（它会另开连接查 item_def，测试库单连接会死锁）。
		category, ok := effectiveCategoryTx(ctx, tx, row.ItemID)
		if !ok {
			continue
		}
		terminalCategories[row.GUID] = category
		if !countableCategories[category] {
			continue
		}
		if srcGUID := splitSourceGUID(row); len(srcGUID) == 32 {
			stackSourceGUIDs[srcGUID] = true
		}
		stackNumericIDs[row.NumericID] = true
	}

	for guid, row := range terminalRows {
		// 最新存档会把浮钓/沉底的一整包饵装进钩饵子实例。剪线只损失其中
		// 一份，剩余数量必须退回背包；直接删子实例会把整包饵一起吞掉。
		category := terminalCategories[guid]
		if policy.reason == "line_cut" &&
			(category == catBait || category == catLure) && row.Container == "rig" && row.RigGUID == rodGUID {
			qty := baitQuantity(row)
			if qty > 1 {
				compFields, ferr := FieldsFromJSON(row.FieldsJSON)
				if ferr != nil {
					return nil, ferr
				}
				if !setItemQty(row.WireClass, compFields, uint64(qty-1)) {
					return nil, errors.New("钩饵实例缺少数量字段")
				}
				row.Qty = qty - 1
				row.Container = "backpack"
				row.Slot = 0
				row.RigGUID = ""
				clearRigFieldsForBackpack(compFields, row.WireClass)
				if err := updateItemFieldsTx(ctx, s.DB, tx, row, compFields); err != nil {
					return nil, err
				}
				if err := s.DB.DetachReturnToBackpackTx(ctx, tx, guid); err != nil {
					return nil, err
				}
			} else {
				if err := s.DB.DeleteItemCascadeTx(ctx, tx, guid); err != nil {
					return nil, err
				}
				removed = append(removed, guid)
			}
		} else {
			if err := s.DB.DeleteItemCascadeTx(ctx, tx, guid); err != nil {
				return nil, err
			}
			removed = append(removed, guid)
		}
		entry := store.ItemDamageLog{
			UID: uid, ItemGUID: guid, ItemID: row.ItemID, Event: policy.eventLost,
			BeforeDurability: ptrFloat64(row.Durability), BrokenGUID: brokenGUID,
			DetailJSON: damageDetailJSON(map[string]any{"rod_guid": rodGUID, "reason": policy.reason}),
		}
		if err := store.AddItemDamageLog(ctx, tx, entry); err != nil {
			return nil, err
		}
	}
	// 线路类断裂会移除所有末端钩/饵/拟饵；对应的自动补饵和饵钩映射
	// 必须与组件删除同事务清掉，不能留到修好或下一次抛竿才碰巧清理。
	if err := clearRodBaitSettingsTx(ctx, tx, rodGUID); err != nil {
		return nil, err
	}

	if lineRow != nil && afterLength <= 0 {
		if err := s.DB.DeleteItemCascadeTx(ctx, tx, lineRow.GUID); err != nil {
			return nil, err
		}
		removed = append(removed, lineRow.GUID)
		if idx := fieldIdxByCS(rod.WireClass, csLine); idx >= 0 && idx < len(rodFields) {
			rodFields[idx].Op = "OPTGUID"
			rodFields[idx].Hex = ""
		}
	}

	comps := rigCompsOf(stored)
	for cat := range lossCategories {
		deleteRigCategory(comps, cat)
	}
	if lineRow != nil && afterLength <= 0 {
		deleteRigCategory(comps, catLine)
	}
	name, rigType, typeOK := rigIdentity(stored)
	if !typeOK {
		rigType = s.defaultRigTypeForRod(ctx, rod.ItemID)
		name = rigNameForType(rigType)
	}
	depth := rigDepthOf(stored, defaultDepthFor(name, rigType))
	if idx := fieldIdxByCS(rod.WireClass, csRigObject); idx >= 0 && idx < len(rodFields) {
		rodFields[idx].Op = "VAL"
		rodFields[idx].Val = buildRig153Like(stored, name, rigType, comps, depth, rigClipOf(stored))
	}
	if err := updateItemFieldsTx(ctx, s.DB, tx, rod, rodFields); err != nil {
		return nil, err
	}

	if mainLineBreak && lineRow != nil {
		entry := store.ItemDamageLog{
			UID: uid, ItemGUID: lineRow.GUID, ItemID: lineRow.ItemID, Event: policy.eventLine,
			BeforeDurability: ptrFloat64(lineRow.Durability), AfterDurability: ptrFloat64(lineRow.Durability),
			BeforeLength: ptrInt64(beforeLength), AfterLength: ptrInt64(afterLength), BrokenGUID: brokenGUID,
			DetailJSON: damageDetailJSON(map[string]any{"rod_guid": rodGUID, "reason": policy.reason, "loss_m": lossLength}),
		}
		if err := store.AddItemDamageLog(ctx, tx, entry); err != nil {
			return nil, err
		}
		if afterLength <= 0 {
			entry.Event = damageEventLineExhausted
			if err := store.AddItemDamageLog(ctx, tx, entry); err != nil {
				return nil, err
			}
		}
	} else {
		if brokenRow != nil {
			entry := store.ItemDamageLog{
				UID: uid, ItemGUID: brokenRow.GUID, ItemID: brokenRow.ItemID, Event: policy.eventLine,
				BeforeDurability: ptrFloat64(brokenRow.Durability), AfterDurability: ptrFloat64(brokenRow.Durability),
				BrokenGUID: brokenGUID,
				DetailJSON: damageDetailJSON(map[string]any{"rod_guid": rodGUID, "reason": "leader_break", "loss_m": 0}),
			}
			if err := store.AddItemDamageLog(ctx, tx, entry); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	part := "leader"
	if mainLineBreak {
		part = "line"
	}
	out := &gearBreakOutcome{LineBroken: true, BreakPart: part, LineLossM: lossLength, BrokenGUID: brokenGUID, RemovedGUIDs: removed}
	updated := []string{rodGUID}
	if mainLineBreak && lineRow != nil && afterLength > 0 {
		updated = append(updated, lineRow.GUID)
	}
	// 计件配件（饵、拟饵等）来自背包堆的拆分：装配时来源堆数量已经减一，
	// 但当时没有推给客户端，剪线/断线删掉子实例后数量仍显示旧值。
	// 这里把来源堆（含同物品号的其它背包堆）一并放进更新段并推送。
	if stackGUIDs := s.backpackStackGUIDsForItems(ctx, uid, stackNumericIDs, stackSourceGUIDs); len(stackGUIDs) > 0 {
		out.StackRefreshGUIDs = stackGUIDs
		updated = append(updated, stackGUIDs...)
	}
	out.UpdatedTrees = s.loadUpdatedTrees(ctx, updated)
	return out, nil
}

// backpackStackGUIDsForItems 返回背包里这些物品号的堆 GUID：sourceGUIDs 是
// 被删子实例记录的来源堆（仍需校验属于本人、同物品号且在背包，防止误刷别的
// 物品），numericIDs 兜底覆盖来源堆已消耗/合并的情形。
func (s *Server) backpackStackGUIDsForItems(ctx context.Context, uid int64, numericIDs map[int64]bool, sourceGUIDs map[string]bool) []string {
	if len(numericIDs) == 0 && len(sourceGUIDs) == 0 {
		return nil
	}
	keep := map[string]bool{}
	if len(numericIDs) > 0 {
		placeholders := make([]string, 0, len(numericIDs))
		args := make([]any, 0, len(numericIDs)+2)
		args = append(args, uid)
		for id := range numericIDs {
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		args = append(args, "backpack")
		query := `SELECT guid FROM item WHERE uid=? AND numeric_id IN (` +
			strings.Join(placeholders, ",") + `) AND container=?`
		rows, err := s.DB.SQL.QueryContext(ctx, query, args...)
		if err == nil {
			for rows.Next() {
				var g string
				if err := rows.Scan(&g); err == nil && len(g) == 32 {
					keep[g] = true
				}
			}
			_ = rows.Err()
			_ = rows.Close()
		}
	}
	for g := range sourceGUIDs {
		row, err := s.DB.GetItemByGUID(ctx, g)
		if err != nil || row == nil || row.UID != uid || row.Container != "backpack" {
			continue
		}
		if len(numericIDs) > 0 && !numericIDs[row.NumericID] {
			continue
		}
		keep[g] = true
	}
	if len(keep) == 0 {
		return nil
	}
	guids := make([]string, 0, len(keep))
	for g := range keep {
		guids = append(guids, g)
	}
	sort.Strings(guids)
	return guids
}

// breakRodTx 处理 17/9 断竿：竿本体耐久归零、渔轮保留；主线只按客户端上报的
// 实际放线米数扣减（lineOutM<=0 视为未上报、一米不扣；线长读不出来按未上报
// 处理，不判死），扣剩的线与渔轮一样留在断竿上，可拆卸换竿；f3 指向不在本竿
// 上的陈旧引用时只清引用不动物品。f5 钓组与挂线配件全部丢失销毁。
func (s *Server) breakRodTx(ctx context.Context, uid int64, rodGUID, brokenGUID string, lineOutM float64) (*gearBreakOutcome, error) {
	tx, err := s.DB.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rod, err := s.DB.GetItemByGUIDTx(ctx, tx, rodGUID)
	if err != nil || rod == nil || rod.UID != uid {
		return nil, sql.ErrNoRows
	}
	before := rod.Durability
	fields, err := FieldsFromJSON(rod.FieldsJSON)
	if err != nil {
		return nil, err
	}
	idx := fieldIdxByCS(rod.WireClass, csDurability)
	if idx < 0 || !setWireFloat(fields, rod.WireClass, idx, 0) {
		return nil, errDurabilityField
	}
	rod.Durability = 0
	if err := updateItemFieldsTx(ctx, s.DB, tx, rod, fields); err != nil {
		return nil, err
	}

	// 断竿只报废竿本体：耐久归零、不能取竿也不能抛投；竿子是"断了竿梢"，
	// 竿本体和竿上的渔轮都保留（轮子没坏，仍挂在竿上）。主线不整卷判死：
	// 只按 17/9 上报的实际放线米数扣（与断主线同源），扣剩的线留在竿上；
	// f5 钓组与挂线配件（引线/钩/饵/浮漂/铅坠等）全部丢失销毁。
	// 销毁的组件进 11028 的移除列表让客户端清掉线组 UI；竿本体与保留的线走
	// 4/22 刷新（断竿树带损坏位）。移除列表不能省：客户端只按它清线组，缺了会继续
	// 认为鱼还挂在钩上，搏鱼状态无法退出。
	reelGUID, _ := s.storedRigItemGUID(ctx, rod, csReel)
	lineGUID, _ := s.storedRigItemGUID(ctx, rod, csLine)
	var removed, stackRefresh []string

	// 主线扣减：读剩余长度（缺字段回退定义全长），按上报米数取整、封顶；
	// 未上报（<=0）一米不扣，与"只有放出去的线才会断"的事实一致。
	var lineRow *store.Item
	var lineFields []proto.Field
	beforeLength, lossLength := int64(0), int64(0)
	if lineGUID != "" {
		lineRow, _ = s.DB.GetItemByGUIDTx(ctx, tx, lineGUID)
	}
	if lineRow != nil && (lineRow.Container != "rig" || lineRow.RigGUID != rodGUID) {
		// f3 是陈旧引用（线已被拆回背包或挂到别的竿）：只清引用不动物品，
		// 否则会把玩家手里的线改短甚至删掉。lineRow 置空走"无主线"分支。
		if s.log != nil {
			s.log.Info("断竿跳过陈旧主线引用", "rod", guidShort(rodGUID),
				"line", guidShort(lineGUID), "container", lineRow.Container)
		}
		lineRow = nil
	}
	if lineRow != nil {
		lineFields, err = FieldsFromJSON(lineRow.FieldsJSON)
		if err != nil {
			return nil, err
		}
		beforeLength = itemLengthField(lineRow, lineFields)
		if beforeLength <= 0 {
			if def, derr := s.itemDefForRow(ctx, lineRow); derr == nil && def != nil {
				beforeLength = lineLengthM(def)
			}
		}
		// 长度读不出来（字段缺失且定义全长不可得）时按未上报处理：
		// 一米不扣、整卷保留，不能因为读不到长度就把主线判死。
		if lineOutM > 0 && beforeLength > 0 {
			lossLength = int64(lineOutM + 0.5)
			if lossLength < 1 {
				lossLength = 1
			}
			if lossLength > beforeLength {
				lossLength = beforeLength
			}
		}
	}
	afterLength := beforeLength - lossLength
	// lossLength==0 时线必然保留：未上报或长度不可读都不产生扣减。
	lineKept := lineRow != nil && (lossLength == 0 || afterLength > 0)

	stored := protoFieldAt(fields, rod.WireClass, csRigObject)
	compGUIDs := map[string]bool{}
	if rows, qerr := tx.QueryContext(ctx,
		`SELECT COALESCE(guid,'') FROM rig_component WHERE rig_guid=?`, rodGUID); qerr == nil {
		for rows.Next() {
			var g string
			if err := rows.Scan(&g); err == nil && g != "" {
				compGUIDs[g] = true
			}
		}
		_ = rows.Err()
		_ = rows.Close()
	}
	for _, c := range rigCompsOf(stored) {
		if c.GUID != "" {
			compGUIDs[c.GUID] = true
		}
	}
	delete(compGUIDs, reelGUID)
	delete(compGUIDs, lineGUID)
	guids := make([]string, 0, len(compGUIDs))
	for g := range compGUIDs {
		guids = append(guids, g)
	}
	sort.Strings(guids)
	stackNumericIDs := map[int64]bool{}
	stackSourceGUIDs := map[string]bool{}
	for _, g := range guids {
		row, gerr := s.DB.GetItemByGUIDTx(ctx, tx, g)
		if gerr != nil || row == nil {
			continue
		}
		// 官服口径：断竿时挂线配件全部丢失销毁（含拆分子饵），不再摘回背包。
		if category, ok := effectiveCategoryTx(ctx, tx, row.ItemID); ok && countableCategories[category] {
			if srcGUID := splitSourceGUID(row); len(srcGUID) == 32 {
				stackSourceGUIDs[srcGUID] = true
			}
			stackNumericIDs[row.NumericID] = true
		}
		if err := s.DB.DeleteItemCascadeTx(ctx, tx, g); err != nil {
			return nil, err
		}
		entry := store.ItemDamageLog{
			UID: uid, ItemGUID: g, ItemID: row.ItemID, Event: damageEventLostOnRodBreak,
			BeforeDurability: ptrFloat64(row.Durability), BrokenGUID: brokenGUID,
			DetailJSON: damageDetailJSON(map[string]any{"rod_guid": rodGUID, "reason": "rod_break"}),
		}
		if err := store.AddItemDamageLog(ctx, tx, entry); err != nil {
			return nil, err
		}
		removed = append(removed, g)
	}
	// 主线随断竿只扣放出去的米数：扣剩的线更新长度后仍挂在断竿上（f3 与
	// catLine 投影行保留）；上报米数 ≥ 剩余线长时耗尽删除，与断主线耗尽同形。
	// 未扣（lossLength==0）不动线字段——长度不可读的线没有字段可写。
	if lineRow != nil && lineKept && lossLength > 0 {
		if !setItemLengthField(lineRow, lineFields, afterLength) {
			return nil, errLineLengthField
		}
		if err := updateItemFieldsTx(ctx, s.DB, tx, lineRow, lineFields); err != nil {
			return nil, err
		}
	}
	if lineRow != nil && !lineKept {
		if err := s.DB.DeleteItemCascadeTx(ctx, tx, lineRow.GUID); err != nil {
			return nil, err
		}
		removed = append(removed, lineRow.GUID)
	}
	// 只有真扣了米数才记 line_break；耐久字段不填——断竿不动线的耐久，
	// 长度变化看 BeforeLength/AfterLength。
	if lineRow != nil && lossLength > 0 {
		entry := store.ItemDamageLog{
			UID: uid, ItemGUID: lineRow.GUID, ItemID: lineRow.ItemID, Event: damageEventLineBreak,
			BeforeLength: ptrInt64(beforeLength), AfterLength: ptrInt64(afterLength), BrokenGUID: brokenGUID,
			DetailJSON: damageDetailJSON(map[string]any{"rod_guid": rodGUID, "reason": "rod_break", "loss_m": lossLength}),
		}
		if err := store.AddItemDamageLog(ctx, tx, entry); err != nil {
			return nil, err
		}
		if !lineKept {
			entry.Event = damageEventLineExhausted
			if err := store.AddItemDamageLog(ctx, tx, entry); err != nil {
				return nil, err
			}
		}
	}
	// 钓组已整体清空：补饵请求和饵-钩映射必须一起清掉，否则断竿后
	// 第一次抛竿（换竿或换轮）会把饵挂回一条空钓组。
	if err := clearRodBaitSettingsTx(ctx, tx, rodGUID); err != nil {
		return nil, err
	}
	delCond := `DELETE FROM rig_component WHERE rig_guid=? AND COALESCE(guid,'')<>?`
	delArgs := []any{rodGUID, reelGUID}
	if lineKept {
		// 线留在断竿上：catLine 投影行与 f3 同步保留，避免投影漂移出幽灵行。
		delCond += ` AND COALESCE(guid,'')<>?`
		delArgs = append(delArgs, lineGUID)
	}
	if _, err := tx.ExecContext(ctx, delCond, delArgs...); err != nil {
		return nil, err
	}
	// 清空竿上的钓组存储与主线引用；渔轮引用（f4）保留——轮子仍挂在断竿上。
	comps := rigCompsOf(stored)
	for k := range comps {
		delete(comps, k)
	}
	name, rigType, typeOK := rigIdentity(stored)
	if !typeOK {
		rigType = s.defaultRigTypeForRod(ctx, rod.ItemID)
		name = rigNameForType(rigType)
	}
	depth := rigDepthOf(stored, defaultDepthFor(name, rigType))
	if i := fieldIdxByCS(rod.WireClass, csRigObject); i >= 0 && i < len(fields) {
		fields[i].Op = "VAL"
		fields[i].Val = buildRig153WithClip(name, rigType, comps, depth, rigClipOf(stored))
	}
	// csReel（f4）不清：断竿后轮子仍挂在竿上；线扣剩时 csLine（f3）也不清。
	if !lineKept {
		if i := fieldIdxByCS(rod.WireClass, csLine); i >= 0 && i < len(fields) {
			fields[i].Op = "OPTGUID"
			fields[i].Hex = ""
		}
	}
	if err := updateItemFieldsTx(ctx, s.DB, tx, rod, fields); err != nil {
		return nil, err
	}

	entry := store.ItemDamageLog{
		UID: uid, ItemGUID: rod.GUID, ItemID: rod.ItemID, Event: damageEventRodBreak,
		BeforeDurability: ptrFloat64(before), AfterDurability: ptrFloat64(0), BrokenGUID: brokenGUID,
		DetailJSON: damageDetailJSON(map[string]any{"rod_guid": rodGUID}),
	}
	if err := store.AddItemDamageLog(ctx, tx, entry); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// 被销毁的拆分子饵：装配时来源堆数量已减但客户端没收到推送，
	// 删除子实例后要把来源堆（含同物品号背包堆）一并刷新对齐数量。
	stackRefresh = s.backpackStackGUIDsForItems(ctx, uid, stackNumericIDs, stackSourceGUIDs)
	updated := []string{rodGUID}
	if lineKept {
		// 线扣剩仍挂竿：长度变了，竿树与线树都进 11028 更新段。
		updated = append(updated, lineGUID)
	}
	updated = append(updated, stackRefresh...)
	return &gearBreakOutcome{
		LineBroken: false, BreakPart: "rod", BrokenGUID: brokenGUID, RemovedGUIDs: removed,
		LineLossM:         lossLength,
		StackRefreshGUIDs: stackRefresh,
		UpdatedTrees:      s.loadUpdatedTrees(ctx, updated),
	}, nil
}

func (s *Server) loadUpdatedTrees(ctx context.Context, guids []string) []*proto.Value {
	out := make([]*proto.Value, 0, len(guids))
	seen := map[string]bool{}
	for _, guid := range guids {
		if guid == "" || seen[guid] {
			continue
		}
		seen[guid] = true
		if row, err := s.DB.GetItemByGUID(ctx, guid); err == nil && row != nil {
			if tree, terr := s.itemTree(ctx, row); terr == nil {
				out = append(out, tree)
			}
		}
	}
	return out
}

func damageDetailJSON(v map[string]any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func protoFieldAt(fields []proto.Field, class, csName string) *proto.Value {
	idx := fieldIdxByCS(class, csName)
	if idx < 0 || idx >= len(fields) {
		return nil
	}
	return fields[idx].Val
}

func buildGearBreakResult(outcome *gearBreakOutcome) (*proto.Value, error) {
	if outcome == nil {
		outcome = &gearBreakOutcome{}
	}
	updated := proto.Null()
	if len(outcome.UpdatedTrees) > 0 {
		updated = proto.Args(proto.S("61"), outcome.UpdatedTrees...)
	}
	removed := proto.Null()
	if len(outcome.RemovedGUIDs) > 0 {
		removed = proto.GuidArr(outcome.RemovedGUIDs)
	}
	return wiregen.MustMakeObj("ikmdmfoojfg", map[int]*proto.Value{
		0: proto.U32(boolU32(outcome.LineBroken)), 1: proto.Guid(outcome.BrokenGUID),
		2: proto.Null(), 3: updated, 4: removed,
	}), nil
}

// 这些错误只在字段结构异常时返回，调用方可回退到兼容响应。
var (
	errLineLengthField = errors.New("鱼线长度字段不存在")
	errDurabilityField = errors.New("耐久字段不存在")
)

// frictionWearDetail 组装渔轮磨损日志的 detail。extra 是本次结算的补充诊断字段，
// 同名键覆盖默认值，便于从搏鱼结算里带回打滑次数与过载时长。
func frictionWearDetail(rodGUID string, perf *store.ReelPerformance, durability float64, wornOut bool, extra map[string]any) string {
	detail := map[string]any{
		"rod_guid": rodGUID, "base_drag_kg": perf.BaseDragKg,
		"locked_drag_kg": perf.LockedDragKg, "lock_multiplier": reelLockMultiplier(durability),
		"bearing_quality": perf.BearingQuality, "oil_quality": perf.OilQuality,
		"oil_wear_factor": perf.OilWearFactor, "friction_worn": wornOut,
	}
	for k, v := range extra {
		detail[k] = v
	}
	return damageDetailJSON(detail)
}
