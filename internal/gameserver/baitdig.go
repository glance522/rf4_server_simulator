package gameserver

// 9/3 的挖饵逻辑（4/8 的物品使用不参与挖饵）。数据全部走作者现有 SQLite：
// - 工具：铲子 tool_shovel / 新年铲 shovel_ny25、红虫长柄勺 tool_scoop
// - 熟练度：技能大类 4（捕获鱼饵）
// - 产物：普通 item 行，复用现有可堆叠物品机制
import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"strings"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

const (
	captureBaitSkillID = 4
	baitShovelItemID   = "tool_shovel"
	baitScoopItemID    = "tool_scoop"
	baitMasteryStep    = 0.1
	baitMasteryCap     = 100.0
)

// baitDigKindBand 定义“熟练度达到 MinMastery 起，一次挖饵能翻出几种不同的饵”。
// 种类数随熟练度提升而增长，最高 baitMaxKinds 种（想调手感改 baitDigKindBands）；
// 实际取值还会被当前档位表的饵种数收敛（表里只有 N 种时不可能翻出 N+1 种）。
type baitDigKindBand struct {
	MinMastery int
	MinKind    int
	MaxKind    int
}

// baitMaxKinds 单次挖饵上限：7 种。
const baitMaxKinds = 7

// baitDigKindBands 种类数推进表（与掉落档位同一套熟练度阈值）：
// 0 档 1 种 → 20 档 1~2 种 → 40 档 2~3 种 → 60 档 3~5 种 → 80 档 4~7 种。
var baitDigKindBands = []baitDigKindBand{
	{0, 1, 1},
	{20, 1, 2},
	{40, 2, 3},
	{60, 3, 5},
	{80, 4, 7},
}

type baitLootEntry struct {
	Key    string
	Weight int
}

type baitDigDrop struct {
	Key string
	Qty int64
}

// baitPoolEntry 是饵池里的一种饵：解锁锚点 + 基准权重。
// 锚点表示“捕获熟练度达到该值后才会出现”，未达锚点一律不进入掉落表；
// 权重只在本次已解锁的集合内归一，所以越晚解锁的饵不影响早期档位的相对概率。
type baitPoolEntry struct {
	Key    string
	Unlock int
	Weight int
}

// 饵料池按“土生 / 水生”硬性互斥：每种饵只属于一个工具，换工具就换饵种。
// 每池内的解锁锚点由策划给定（熟练度未达锚点不得提前挖出）：
//
//	铲子 tool_shovel / 新年铲 shovel_ny25：
//	  蚯蚓 worm_01(0)、红蚯蚓 muckworm(0)、地龙 vypolzok_01(75)、蜣螂 dorbeetle(80)、
//	  土蚕 maybug_larva(90)、蝼蛄 gryllotalpa(95)、金龟幼虫 unibeetle_larva(100)
//	红虫长柄勺 tool_scoop：
//	  红虫 bloodworm(45)、一束藻类 watergrass(45)、浮游幼虫 naiad(55)、
//	  毛翅目 caddisfly(70)、水蛭 black_leech(80)、龙虱甲虫 waterbug(100)、
//	  淡水钩虾 gammarus_fresh(100)
//
// ⚠️ 勺子的锚点从 45 起，因此熟练度 <45 时长柄勺的可用饵表为空、挖饵直接失败。
// ⚠️ 权重与锚点都是本地标定值，不是官服数据；替换本表即调整，逻辑不用动。
var baitDigPools = map[string][]baitPoolEntry{
	baitShovelItemID: {
		{Key: "worm_01", Unlock: 0, Weight: 30},
		{Key: "muckworm", Unlock: 0, Weight: 20},
		{Key: "vypolzok_01", Unlock: 75, Weight: 15},
		{Key: "dorbeetle", Unlock: 80, Weight: 10},
		{Key: "maybug_larva", Unlock: 90, Weight: 10},
		{Key: "gryllotalpa", Unlock: 95, Weight: 8},
		{Key: "unibeetle_larva", Unlock: 100, Weight: 7},
	},
	baitScoopItemID: {
		{Key: "bloodworm", Unlock: 45, Weight: 30},
		{Key: "watergrass", Unlock: 45, Weight: 20},
		{Key: "naiad", Unlock: 55, Weight: 15},
		{Key: "caddisfly", Unlock: 70, Weight: 12},
		{Key: "black_leech", Unlock: 80, Weight: 10},
		{Key: "waterbug", Unlock: 100, Weight: 8},
		{Key: "gammarus_fresh", Unlock: 100, Weight: 5},
	},
}

// baitLootTableFor 返回该工具在给定熟练度下已解锁的饵与权重。
// 未达锚点的饵不会出现在结果里；全部未解锁时返回空表（调用方按挖饵失败处理）。
func baitLootTableFor(tool string, mastery float64) []baitLootEntry {
	tool = strings.ToLower(strings.TrimSpace(tool))
	if tool == "shovel_ny25" {
		tool = baitShovelItemID
	}
	pool := baitDigPools[tool]
	if len(pool) == 0 {
		return nil
	}
	out := make([]baitLootEntry, 0, len(pool))
	for _, entry := range pool {
		// 锚点是整数熟练度；留 1e-9 容差避免 44.999999 这类浮点误差挡住 45 档。
		if mastery+1e-9 < float64(entry.Unlock) {
			continue
		}
		out = append(out, baitLootEntry{Key: entry.Key, Weight: entry.Weight})
	}
	return out
}

func isBaitDigTool(itemID string) bool {
	itemID = strings.ToLower(strings.TrimSpace(itemID))
	return itemID == baitShovelItemID || itemID == "shovel_ny25" || itemID == baitScoopItemID
}

// pickWeightedBait 按权重抽一种饵，返回下标；抽空表返回 -1。
// 返回下标而不是 key，是为了多饵抽取时能把已抽中的条目摘掉。
func pickWeightedBait(table []baitLootEntry, rng func(int) int) int {
	total := 0
	for _, entry := range table {
		total += entry.Weight
	}
	if total <= 0 {
		return -1
	}
	n := rng(total)
	if n < 0 {
		n = 0
	}
	for i, entry := range table {
		if n < entry.Weight {
			return i
		}
		n -= entry.Weight
	}
	return len(table) - 1
}

// baitQuantityForMastery 单种饵的数量：1-3，熟练度越高越容易多给。
func baitQuantityForMastery(mastery float64, rng func(int) int) int64 {
	qty := int64(1)
	if mastery >= 20 && rng(2) == 1 {
		qty++
	}
	if mastery >= 60 && rng(2) == 1 {
		qty++
	}
	return qty
}

// baitDigKindCount 掷一次挖饵得到几种不同的饵：按熟练度取档，档内区间均匀取值。
func baitDigKindCount(mastery float64, rng func(int) int) int {
	band := baitDigKindBands[0]
	for _, b := range baitDigKindBands {
		if mastery >= float64(b.MinMastery) {
			band = b
		} else {
			break
		}
	}
	span := band.MaxKind - band.MinKind + 1
	if span <= 0 {
		return band.MinKind
	}
	kinds := band.MinKind + rng(span)
	if kinds > baitMaxKinds {
		kinds = baitMaxKinds
	}
	if kinds < 1 {
		kinds = 1
	}
	return kinds
}

// rollBaitDigLoot 按熟练度权重表抽取 1-3 种不重复的饵，每种 1-3 个。
// 第二次及以后从"已抽走"的表里继续按权重抽，保证不会重复同一种饵。
func rollBaitDigLoot(tool string, mastery float64, rng func(int) int) []baitDigDrop {
	table := baitLootTableFor(tool, mastery)
	if len(table) == 0 {
		return nil
	}
	kinds := baitDigKindCount(mastery, rng)
	if kinds > len(table) {
		kinds = len(table)
	}
	pool := append([]baitLootEntry(nil), table...)
	out := make([]baitDigDrop, 0, kinds)
	for i := 0; i < kinds; i++ {
		idx := pickWeightedBait(pool, rng)
		if idx < 0 {
			break
		}
		out = append(out, baitDigDrop{
			Key: pool[idx].Key,
			Qty: baitQuantityForMastery(mastery, rng),
		})
		pool = append(pool[:idx], pool[idx+1:]...)
	}
	return out
}

// tryBaitDig 只处理 9/3 中包含本号铲子/长柄勺的请求。
// 返回 handled=false 时，hCraftMine2 继续走作者原有的分解逻辑。
func (s *Server) tryBaitDig(ctx *Ctx) ([]byte, bool) {
	guids := uniqueStrings(deepGUIDs(ctx.Tree))
	if len(guids) == 0 {
		return nil, false
	}
	cctx := ctx.Sess.pushCtx()
	var tool *store.Item
	for _, guid := range guids {
		row, err := s.DB.GetItemByGUID(cctx, guid)
		if err != nil || row == nil || row.UID != ctx.Sess.Userid {
			continue
		}
		if isBaitDigTool(row.ItemID) {
			tool = row
			break
		}
	}
	if tool == nil {
		return nil, false
	}

	mastery, err := s.DB.GetSkillPoints(cctx, ctx.Sess.Userid, captureBaitSkillID)
	if err != nil {
		return s.replyBaitDigFailed(ctx, "读取捕获鱼饵熟练度失败: "+err.Error()), true
	}
	if mastery < 0 {
		mastery = 0
	}
	if mastery > baitMasteryCap {
		mastery = baitMasteryCap
	}
	loot := rollBaitDigLoot(tool.ItemID, mastery, rand.Intn)
	if len(loot) == 0 {
		return s.replyBaitDigFailed(ctx, "熟练度未解锁任何可挖饵料"), true
	}

	prepared := make([]*store.Item, 0, len(loot))
	for _, drop := range loot {
		it, err := s.prepareNewItem(cctx, ctx.Sess.Userid, drop.Key, "backpack", 1, "")
		if err != nil {
			return s.replyBaitDigFailed(ctx, "饵记录构造失败: "+err.Error()), true
		}
		if err := setPreparedItemQuantity(it, drop.Qty); err != nil {
			return s.replyBaitDigFailed(ctx, "饵数量写入失败: "+err.Error()), true
		}
		prepared = append(prepared, it)
	}

	// 体力参数必须在开经济事务之后不再查库，故这里先取（tunableF 有 2 秒装载超时）。
	condParams := s.conditionParamsForUID(cctx, ctx.Sess.Userid)
	condCost := s.baitDigEnergyCost()
	condEnv := s.conditionEnvForUID(ctx.Sess.Userid)

	tx, err := beginEconomyTx(cctx, s.DB)
	if err != nil {
		return s.replyBaitDigFailed(ctx, "挖饵事务开启失败: "+err.Error()), true
	}
	defer func() { _ = tx.Rollback() }()
	// 体力扣减与饵入包同事务：任一侧失败整体回滚，不会出现"白挖"或"白扣"。
	// conditionParams/消耗量必须在 beginEconomyTx 之前取好（见 player_condition_energy.go）。
	spent, err := spendEnergyTx(cctx, tx, ctx.Sess.Userid, condParams, condEnv, condCost)
	if err != nil {
		if errors.Is(err, errEnergyNotEnough) {
			ctx.Log.Info("9/3 挖饵被拒：精力不足", "tool", tool.ItemID,
				"cost", condCost, "enabled", condParams.Enabled)
			return s.replyBaitDigFailed(ctx, "精力不足以挖饵"), true
		}
		return s.replyBaitDigFailed(ctx, "挖饵精力结算失败: "+err.Error()), true
	}
	granted := make([]*store.Item, 0, len(prepared))
	parts := make([]string, 0, len(prepared))
	for i, it := range prepared {
		row, merged, err := grantPreparedItemTx(cctx, tx, it, true)
		if err != nil {
			return s.replyBaitDigFailed(ctx, "饵入包失败: "+err.Error()), true
		}
		granted = append(granted, row)
		parts = append(parts, fmt.Sprintf("%s x%d merged=%t", loot[i].Key, loot[i].Qty, merged))
	}
	if err := addTxnTx(cctx, tx, ctx.Sess.Userid, "baitdig", loot[0].Key, granted[0].GUID, 0, 0,
		fmt.Sprintf("tool=%s baits=%s", tool.ItemID, strings.Join(parts, ","))); err != nil {
		return s.replyBaitDigFailed(ctx, "挖饵流水写入失败: "+err.Error()), true
	}
	if err := tx.Commit(); err != nil {
		return s.replyBaitDigFailed(ctx, "挖饵事务提交失败: "+err.Error()), true
	}

	after := mastery
	if mastery < baitMasteryCap {
		after = math.Min(baitMasteryCap, math.Round((mastery+baitMasteryStep)*10)/10)
		if err := s.DB.AddSkillPoints(cctx, ctx.Sess.Userid, captureBaitSkillID, baitMasteryStep); err != nil {
			ctx.Log.Warn("捕获鱼饵熟练度写入失败", "err", err)
			after = mastery
		}
	}
	gain := after - mastery

	previews := make([]*proto.Value, 0, len(granted))
	trees := make([]*proto.Value, 0, len(granted))
	keys := make([]string, 0, len(granted))
	for i, row := range granted {
		tree, err := s.ItemToTree(cctx, row)
		if err != nil {
			return s.replyBaitDigFailed(ctx, "饵物品树构造失败: "+err.Error()), true
		}
		// 803.f1 是"本次获得"预览，数量用本次增量；803.f6/f7 是背包快照，用堆总数。
		previewRow := *row
		if err := setPreparedItemQuantity(&previewRow, loot[i].Qty); err != nil {
			return s.replyBaitDigFailed(ctx, "饵获得数量构造失败: "+err.Error()), true
		}
		preview, err := s.ItemToTree(cctx, &previewRow)
		if err != nil {
			return s.replyBaitDigFailed(ctx, "饵获得预览构造失败: "+err.Error()), true
		}
		previews = append(previews, preview)
		trees = append(trees, tree)
		keys = append(keys, loot[i].Key)
	}

	body, ok := s.replyBaitDigResult(ctx, previews, trees, gain)
	if !ok {
		return nil, true
	}
	stacks := make([]string, 0, len(granted))
	for _, row := range granted {
		stacks = append(stacks, fmt.Sprintf("%s=%d", row.ItemID, stackCountFromRow(row)))
	}
	ctx.Log.Info("9/3 挖饵", "tool", tool.ItemID, "baits", strings.Join(parts, ","),
		"stacks", strings.Join(stacks, ","), "mastery", fmt.Sprintf("%.1f->%.1f", mastery, after),
		"energy_spent", spent)
	ctx.AfterResponse(func() {
		ctx.Sess.Push2_4SkillTree("挖饵 " + strings.Join(keys, ","))
		for _, tree := range trees {
			ctx.Sess.Push(4, 22, proto.Args(proto.S("61"), tree))
		}
	})
	if spent > 0 {
		// 体力条不在 4/22 里，扣完立刻补推一条 2/2，否则要等下一拍保活才看得见。
		ctx.AfterResponse(func() { pushKeepalive(ctx.Sess) })
	}
	// 同一次挖到多种饵时逐种上报，避免“挖到某饵”成就因只记录首件而漏判。
	for _, key := range keys {
		s.evaluateAchievements(ctx, achievementEvent{Kind: evBaitDug, BaitKey: key})
	}
	return body, true
}

func setPreparedItemQuantity(it *store.Item, qty int64) error {
	if it == nil || qty < 1 {
		return fmt.Errorf("无效数量 %d", qty)
	}
	fields, err := FieldsFromJSON(it.FieldsJSON)
	if err != nil {
		return err
	}
	idx := itemQtyFieldIdx(it.WireClass)
	if idx < 0 || idx >= len(fields) {
		return fmt.Errorf("物品 %s 没有数量字段", it.ItemID)
	}
	fields[idx].Num = ptrU64(uint64(qty))
	bumpCreatedTicks(fields, it.WireClass)
	fj, err := FieldsToJSON(fields)
	if err != nil {
		return err
	}
	it.FieldsJSON = fj
	it.Qty = qty
	return nil
}

func stackCountFromRow(it *store.Item) int64 {
	if it == nil {
		return 0
	}
	if it.Qty > 0 {
		return it.Qty
	}
	return 1
}

// replyBaitDigResult 将本次获得预览放入 803.f1，库存快照仍保留累计数量。
// 预览与库存使用独立物品树，避免修改提示数量时覆盖背包数量。
//
// replyBaitDigResult puts this acquisition's preview in 803.f1 while inventory snapshots retain the stack total.
// The preview and inventory use separate item trees so changing the displayed gain cannot overwrite inventory counts.
func (s *Server) replyBaitDigResult(ctx *Ctx, previews, trees []*proto.Value, masteryGain float64) ([]byte, bool) {
	items := proto.Args(proto.S("61"), trees...)
	obj := wiregen.MustMakeObj("beckiiffkem", map[int]*proto.Value{
		0: proto.U32(1),
		1: proto.Args(proto.S("61"), previews...),
		2: masteryGainValue(captureBaitSkillID, masteryGain),
		3: proto.Null(),
		6: items,
		7: items,
	})
	obj.Fields[3].Val = s.buildSingleSkill312(ctx.Sess.pushCtx(), ctx.Sess.Userid, captureBaitSkillID)
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		ctx.Log.Warn("挖饵 803 编码失败", "err", err)
		return nil, false
	}
	return body, true
}

func (s *Server) replyBaitDigFailed(ctx *Ctx, reason string) []byte {
	ctx.Log.Warn("9/3 挖饵失败", "reason", reason)
	obj := wiregen.MustMakeObj("beckiiffkem", map[int]*proto.Value{
		0: proto.U32(0), 1: proto.Args(proto.S("61")), 2: proto.Null(),
	})
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		return []byte{0x0F}
	}
	return body
}

func masteryGainValue(skillID int64, gain float64) *proto.Value {
	if gain < 0 {
		gain = 0
	}
	return wiregen.MustMakeObj("kegeppimjbm", map[int]*proto.Value{
		0: proto.U64(0),
		1: proto.U32(0),
		2: proto.U32(uint32(skillID)),
		3: proto.F32(float32(gain)),
		4: proto.Args(proto.S("245")),
		5: proto.U32(0),
		6: proto.Null(),
	})
}

func (s *Session) Push2_4SkillTree(why string) {
	tree, err := s.Srv.BuildSkillTree311(s.pushCtx(), s.Userid, ^uint64(0))
	if err != nil {
		s.log.Warn("2/4 技能树构造失败", "why", why, "err", err)
		return
	}
	s.Push(2, 4, proto.Args524(tree))
}
