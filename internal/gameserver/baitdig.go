package gameserver

// 4/8 与 9/3 的挖饵逻辑。数据全部走作者现有 SQLite：
// - 工具：铲子 tool_shovel、红虫长柄勺 tool_scoop
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

// 一次挖饵能翻出几种不同的饵：第 2 种按固定概率出，第 3 种要熟练度达标。
// 想调"多饵"手感改这三个数即可。
const (
	baitSecondKindChance = 55   // 追加第 2 种饵的概率（%）
	baitThirdKindChance  = 25   // 追加第 3 种饵的概率（%）
	baitThirdKindMastery = 40.0 // 解锁第 3 种饵所需熟练度
)

type baitLootEntry struct {
	Key    string
	Weight int
}

type baitLootBand struct {
	Min   int
	Table []baitLootEntry
}

type baitDigDrop struct {
	Key string
	Qty int64
}

var baitDigBands = map[string][]baitLootBand{
	baitShovelItemID: {
		{0, []baitLootEntry{{"worm_01", 90}, {"maggot", 10}}},
		{20, []baitLootEntry{{"worm_01", 65}, {"maggot", 25}, {"naiad", 10}}},
		{40, []baitLootEntry{{"worm_01", 45}, {"maggot", 25}, {"naiad", 20}, {"caddisfly", 10}}},
		{60, []baitLootEntry{{"worm_01", 35}, {"maggot", 20}, {"naiad", 20}, {"caddisfly", 15}, {"unibeetle_larva", 10}}},
		{80, []baitLootEntry{{"worm_01", 25}, {"maggot", 15}, {"naiad", 15}, {"caddisfly", 15}, {"unibeetle_larva", 15}, {"stonefly_nymph", 10}, {"scolytinae", 5}}},
	},
	baitScoopItemID: {
		{0, []baitLootEntry{{"bloodworm", 70}, {"worm_01", 30}}},
		{20, []baitLootEntry{{"bloodworm", 80}, {"naiad", 10}, {"worm_01", 10}}},
		{40, []baitLootEntry{{"bloodworm", 70}, {"naiad", 15}, {"caddisfly", 10}, {"stonefly_nymph", 5}}},
		{60, []baitLootEntry{{"bloodworm", 60}, {"naiad", 15}, {"caddisfly", 10}, {"stonefly_nymph", 10}, {"scolytinae", 5}}},
		{80, []baitLootEntry{{"bloodworm", 50}, {"naiad", 15}, {"caddisfly", 10}, {"stonefly_nymph", 10}, {"scolytinae", 10}, {"unibeetle_larva", 5}}},
	},
}

func isBaitDigTool(itemID string) bool {
	itemID = strings.ToLower(strings.TrimSpace(itemID))
	return itemID == baitShovelItemID || itemID == "shovel_ny25" || itemID == baitScoopItemID
}

// baitLootTableFor 取当前熟练度档位的权重表；熟练度高于所有档位时用最高档。
func baitLootTableFor(tool string, mastery float64) []baitLootEntry {
	tool = strings.ToLower(strings.TrimSpace(tool))
	if tool == "shovel_ny25" {
		tool = baitShovelItemID
	}
	bands := baitDigBands[tool]
	if len(bands) == 0 {
		return nil
	}
	table := bands[0].Table
	for _, b := range bands {
		if mastery >= float64(b.Min) {
			table = b.Table
		} else {
			break
		}
	}
	return table
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

// baitDigKindCount 掷一次挖饵得到几种不同的饵（1-3）。
func baitDigKindCount(mastery float64, rng func(int) int) int {
	kinds := 1
	if rng(100) < baitSecondKindChance {
		kinds++
	}
	if mastery >= baitThirdKindMastery && rng(100) < baitThirdKindChance {
		kinds++
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
		return s.replyBaitDigFailed(ctx, "熟练度阶段没有可用饵表"), true
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

	tx, err := beginEconomyTx(cctx, s.DB)
	if err != nil {
		return s.replyBaitDigFailed(ctx, "挖饵事务开启失败: "+err.Error()), true
	}
	defer func() { _ = tx.Rollback() }()
	// 体力扣减与饵入包同事务：任一侧失败整体回滚，不会出现"白挖"或"白扣"。
	// conditionParams/消耗量必须在 beginEconomyTx 之前取好（见 player_condition_energy.go）。
	spent, err := spendEnergyTx(cctx, tx, ctx.Sess.Userid, condParams, condCost)
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
