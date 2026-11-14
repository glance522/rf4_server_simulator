package gameserver

import (
	"context"
	"fmt"
	"strings"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// 资格/活动分支完成奖杯：整分支任务全部达标后，把对应奖杯发到 house
// （奖赏面板 3/16 的数据源，打开面板即查询，无需专用推送命令）。
//
// 奖杯 item_id 与中文名逐项核对过静态 item_def（category=72）与客户端
// resources.assets 本地化（如 qual_spin_cup=纺车竿高手、reward_bday26=
// 俄钓4-九周年纪念），不是推测。含缺口任务的分支按已下发（非缺口）任务
// 全完成判定：缺口任务不在面板上，不能拿玩家看不到的任务卡奖杯。
//
// 发放与 kv 幂等键同事务（升级礼物同型）：重放/重连/重复巡检不重发，
// 奖杯卖掉/丢掉也不重发（标记仍在）。阶段礼物无官服物品样本，不做。

type qualificationCup struct {
	ItemID string
	NameZh string
}

var qualificationCupByBranch = map[int64]qualificationCup{
	1:  {"qual_spin_cup", "纺车竿高手"},
	2:  {"qual_jig_cup", "汲钩钓(jigging)高手"},
	3:  {"qual_l_ul_cup", "轻渔具高手"},
	4:  {"qual_tele_cup", "手竿高手"},
	5:  {"qual_bolo_match_cup", "博洛尼亚和赛竿高手"},
	6:  {"qual_feeder_cup", "飞德竿高手"},
	7:  {"qual_carp_cup", "鲤鱼高手"},
	8:  {"qual_mlo_cup", "海洋钓鱼高手"},
	9:  {"ny_2024_cup", "新年奖品 MMXXIV"},
	10: {"ny_2024_extra_cup", "新年奖品 MMXXIV EXTRA"},
	11: {"reward_bday24", "RF4-七周年纪念"},
	12: {"reward_bday24_prem", "RF4-七周年纪念-特别版"},
	13: {"ny_2025_cup", "新年大奖 2025"},
	14: {"ny_2025_extra_cup", "新年大奖 2025 特别版"},
	15: {"reward_bday25", "俄钓8周年纪念"},
	16: {"reward_bday25_prem", "俄钓8周年纪念-特别版"},
	17: {"ny_2026_cup", "新年奖品 2026"},
	18: {"ny_2026_extra_cup", "新年奖品 2026 特别奖"},
	19: {"reward_bday26", "俄钓4-九周年纪念"},
	20: {"reward_bday26_prem", "俄钓4-九周年特别纪念"},
}

var qualificationTotalCup = qualificationCup{"qual_total_cup", "超级高手"}

// qualificationCupKey 幂等键；branch=0 表示全 8 分支的总杯。
func qualificationCupKey(uid, branch int64) string {
	return fmt.Sprintf("qual-cup:%d:%d", uid, branch)
}

// ensureQualificationCups 巡检全部分支，补发已完成但未发杯的分支奖杯，
// 返回本次实际新发的奖杯（供调用方挂结算奖励列表/推 4/22）。
// 调用点：14/4 结算后（本尾鱼可能有新达标任务）与 28/2 全量快照（覆盖
// 功能上线前的历史进度）。无变化时只读，不写库。
func (s *Server) ensureQualificationCups(ctx context.Context, uid int64) []*store.Item {
	if s == nil || s.DB == nil {
		return nil
	}
	states, err := s.DB.QualificationStates(ctx, uid)
	if err != nil {
		s.log.Warn("资格奖杯巡检读进度失败", "uid", uid, "err", err)
		return nil
	}
	branchRules := make(map[int64][]int64)
	for _, r := range qualificationRules {
		if eventGapRuleIDs[r.ID] {
			continue
		}
		branch := r.ID / 1000
		branchRules[branch] = append(branchRules[branch], r.ID)
	}
	var granted []*store.Item
	var names []string
	permanentDone := 0
	for branch := int64(1); branch <= 20; branch++ {
		cup, ok := qualificationCupByBranch[branch]
		if !ok || len(branchRules[branch]) == 0 {
			continue
		}
		complete := true
		for _, id := range branchRules[branch] {
			if states[id].CompletedTS <= 0 {
				complete = false
				break
			}
		}
		if !complete {
			continue
		}
		if branch <= qualificationBranches {
			permanentDone++
		}
		if it := s.grantQualificationCup(ctx, uid, branch, cup); it != nil {
			granted = append(granted, it)
			names = append(names, cup.NameZh)
		}
	}
	if permanentDone == qualificationBranches {
		if it := s.grantQualificationCup(ctx, uid, 0, qualificationTotalCup); it != nil {
			granted = append(granted, it)
			names = append(names, qualificationTotalCup.NameZh)
		}
	}
	if len(names) > 0 {
		s.broadcastQualificationCups(uid, names)
	}
	return granted
}

// broadcastQualificationCups 把一次巡检新发的奖杯合并成一条聊天播报。
// 老档首次登录（28/2 兜底）可能一次补发多枚，逐条播报会刷屏，故合并。
func (s *Server) broadcastQualificationCups(uid int64, names []string) {
	text := qualificationCupsBroadcastText(names)
	if text == "" {
		return
	}
	for _, sess := range s.sessionsForUID(uid) {
		sess.pushSystemChat(text)
	}
}

// qualificationCupsBroadcastText 把若干奖杯名拼成一条播报；空输入返回空串。
func qualificationCupsBroadcastText(names []string) string {
	if len(names) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("获得奖赏：")
	for _, n := range names {
		b.WriteString("「")
		b.WriteString(n)
		b.WriteString("」")
	}
	return b.String()
}

// grantQualificationCup 单杯发放：幂等键与物品同事务提交，先到先得；
// 已发过返回 nil。播报由调用方 ensureQualificationCups 合并成一条统一发送。
func (s *Server) grantQualificationCup(ctx context.Context, uid, branch int64, cup qualificationCup) *store.Item {
	it, err := s.prepareNewItem(ctx, uid, cup.ItemID, "house", 1, "")
	if err != nil {
		s.log.Warn("资格奖杯构造失败", "uid", uid, "branch", branch, "item", cup.ItemID, "err", err)
		return nil
	}
	tx, err := beginEconomyTx(ctx, s.DB)
	if err != nil {
		s.log.Warn("资格奖杯事务开启失败", "uid", uid, "branch", branch, "err", err)
		return nil
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO kv(k,v) VALUES(?,?)`,
		qualificationCupKey(uid, branch), "granted")
	if err != nil {
		s.log.Warn("资格奖杯幂等键写入失败", "uid", uid, "branch", branch, "err", err)
		return nil
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return nil // 已发放过
	}
	row, _, err := grantPreparedItemTx(ctx, tx, it, true)
	if err != nil {
		s.log.Warn("资格奖杯发放失败", "uid", uid, "branch", branch, "item", cup.ItemID, "err", err)
		return nil
	}
	if err := tx.Commit(); err != nil {
		s.log.Warn("资格奖杯提交失败", "uid", uid, "branch", branch, "err", err)
		return nil
	}
	return row
}

// awardItemTree 构造奖赏物品的 wire 树，并把容器枚举 f4 投影到奖赏专用分组
// （containerRewards=14）。ItemToTree 会按 house 投影成 10，客户端物品管理器
// 据此挂进房屋组而非奖赏面板读的 14 组，故这里覆盖回 14（见 containerRewards）。
// 找不到容器槽位时保持原样但打 warn：这种物品会以 house=10 推送，奖赏面板看不到。
func (s *Server) awardItemTree(ctx context.Context, it *store.Item) (*proto.Value, error) {
	tree, err := s.ItemToTree(ctx, it)
	if err != nil {
		return nil, err
	}
	idx := fieldIdxByCS(it.WireClass, csSlotEnum)
	if idx < 0 || idx >= len(tree.Fields) {
		s.log.Warn("奖赏物品缺少容器槽位字段，f4 未投影到奖赏分组（面板可能不显示）",
			"item", it.ItemID, "wire_class", it.WireClass, "idx", idx)
		return tree, nil
	}
	tree.Fields[idx].Num = ptrU64(containerRewards)
	return tree, nil
}

// pushAwardUpdate 推 4/22 奖赏物品更新（f4=14，进客户端奖赏分组），面板即时刷新。
func (s *Server) pushAwardUpdate(uid int64, it *store.Item) {
	if it == nil {
		return
	}
	tree, err := s.awardItemTree(s.dbCtx(), it)
	if err != nil {
		return
	}
	for _, sess := range s.sessionsForUID(uid) {
		sess.Push(4, 22, proto.Args(proto.S("61"), tree))
	}
}

// giftSourceGameEvent 是 811.f0 的「游戏活动」来源码：客户端卡片取词函数
// （GameAssembly 0x180D4BD40，跳转表 0x180D4C04C）里唯一适合资格完成的通用文案，
// 实机确认可渲染。有效来源只有 1/2/4/5/6，3 是空号（ja 越界目标，画不出卡）。
const giftSourceGameEvent = 2

// qualificationGiftLine 把本次新发的分支杯包成一条 811(bkmnmjcfbic) 礼物行，
// 供 801.f4 携带，客户端 GameResult 按来源码渲染礼物卡（升级礼物同机制）。
//
// 来源码与 f1 的硬约束来自 PR #407 的客户端反汇编 + 实机校准：
//   - 有效来源只有 1/2/4/5/6；3 的落点是 ja 越界目标，是空号，画不出卡。
//     1=新级别、2=游戏活动、4=塑封卡牌合集、5=数条稀有鱼、6=集齐水域。
//   - f1=0 客户端整枚跳过，必须给非 0（无占位符的来源随便给非 0 即可）。
//
// 资格完成没有专属来源码，固定取通用的 2（游戏活动），不做可配置项：
// 3 是空号，若把来源码做成 tunable，手工设成 3 会静默丢卡。无杯返回 nil。
func (s *Server) qualificationGiftLine(ctx context.Context, cups []*store.Item) (*proto.Value, error) {
	if len(cups) == 0 {
		return nil, nil
	}
	items := proto.Args(proto.S("61"))
	for _, cup := range cups {
		tree, err := s.awardItemTree(ctx, cup)
		if err != nil {
			return nil, err
		}
		items.Items = append(items.Items, tree)
	}
	return wiregen.MakeObj("bkmnmjcfbic", map[int]*proto.Value{
		0: proto.U32(giftSourceGameEvent),
		1: proto.U32(1), // 非 0：f1=0 客户端整枚跳过（PR #407 实证）
		2: items,
		3: proto.Args(proto.S("61")),
	})
}

// append811Row 把一条裸 811 礼物行追加到 801.f4 的 args(173) 容器里；容器为空
// （null）时新建。用于自带 wire 投影（如奖赏 f4=14）的单行来源，与 giftRows811
// 产出的 gift811Line 行共存于同一个数组。
func append811Row(container, line *proto.Value) *proto.Value {
	if container != nil && container.Kind == proto.KindArgs {
		container.Items = append(container.Items, line)
		return container
	}
	return proto.Args(proto.S("173"), line)
}

// append811Rows 合并已经包装好的 args(173) 礼物行数组。
// 阶段奖励通过 giftRows811 一次生成整个容器；若把这个容器作为单行追加，
// 801.f4 会变成 args(173)[args(173)[obj(811)]]，客户端按 811[] 解码时会
// 抛 InvalidCastException 并断线。
func append811Rows(container, rows *proto.Value) *proto.Value {
	if rows == nil || rows.Kind == proto.KindNull {
		return container
	}
	if rows.Kind != proto.KindArgs || rows.ArgCodeStr() != "173" {
		return append811Row(container, rows)
	}
	if container != nil && container.Kind == proto.KindArgs && container.ArgCodeStr() == "173" {
		container.Items = append(container.Items, rows.Items...)
		return container
	}
	return rows
}
