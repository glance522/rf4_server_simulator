package gameserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// 活动阶段礼物：新年/周年面板每个阶段的奖励物品与数量，管理员经 tunable
// `event_stage_gifts` 配置（Control Deck 热更）。预览（5008.f2）随下一次
// 28/1（重启游戏客户端后登录）生效；发放判定在结算与 28/2 巡检时按最新配置执行。
//
// 5008.f2 是 string[]：wire 编码为 0x04 数组（u16 数量 + 每元素 u32 长度 +
// 原始 UTF8 目录键），与 1/5 配置版本响应同型。实机验证（4.0.25034）：
// args 容器会被客户端按 Object 强转 String[] 失败并断线；0x04 数组在资格
// 面板正确渲染奖励行，重复键客户端去重，数组本身不表达数量——实际发放
// 数量以配置为准，预览与配置的一致性由管理员保证（spec：event-stage-gifts）。

const eventStageGiftsKey = "event_stage_gifts"

// eventStageGiftMaxQty 单件奖励数量上限：防御手工配置笔误刷爆背包。
const eventStageGiftMaxQty = 99

type eventStageGift struct {
	ItemID string
	Qty    int64
}

// eventStageGiftPlan 一个已通过校验的阶段奖励计划。
type eventStageGiftPlan struct {
	Branch  int64
	Stage   int64
	Gifts   []eventStageGift
	Preview []string // 去重后的目录键，保持首次出现顺序
}

// eventStageGiftPlans 解析并校验 tunable 配置，按分支、阶段升序返回。
// JSON：{"<branch>": {"<stage>": [{"item":"fcb_coffee_can2","qty":1}]}}。
// 非法条目跳过并 warn（单个坏条目不拖垮整份配置）。
func (s *Server) eventStageGiftPlans() []eventStageGiftPlan {
	raw := s.tunableS(eventStageGiftsKey, "")
	if raw == "" {
		return nil
	}
	var parsed map[string]map[string][]struct {
		Item string `json:"item"`
		Qty  int64  `json:"qty"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		s.log.Warn("阶段礼物配置解析失败", "err", err)
		return nil
	}
	var plans []eventStageGiftPlan
	for branchStr, stages := range parsed {
		branch, err := strconv.ParseInt(branchStr, 10, 32)
		if err != nil {
			s.log.Warn("阶段礼物配置分支号非法，跳过", "branch", branchStr)
			continue
		}
		counts, ok := eventTaskCounts[uint64(branch)]
		if !ok {
			s.log.Warn("阶段礼物配置分支不是活动分支，跳过", "branch", branch)
			continue
		}
		for stageStr, gifts := range stages {
			stage, err := strconv.ParseInt(stageStr, 10, 32)
			if err != nil || stage < 1 || stage > int64(len(counts)) {
				s.log.Warn("阶段礼物配置阶段号非法，跳过", "branch", branch, "stage", stageStr)
				continue
			}
			plan := eventStageGiftPlan{Branch: branch, Stage: stage}
			seen := map[string]bool{}
			for _, g := range gifts {
				if g.Item == "" {
					s.log.Warn("阶段礼物配置物品键为空，跳过", "branch", branch, "stage", stage)
					continue
				}
				qty := g.Qty
				if qty < 1 {
					qty = 1
				}
				if qty > eventStageGiftMaxQty {
					qty = eventStageGiftMaxQty
				}
				plan.Gifts = append(plan.Gifts, eventStageGift{ItemID: g.Item, Qty: qty})
				if !seen[g.Item] {
					seen[g.Item] = true
					plan.Preview = append(plan.Preview, g.Item)
				}
			}
			if len(plan.Gifts) == 0 {
				continue
			}
			plans = append(plans, plan)
		}
	}
	sort.Slice(plans, func(i, j int) bool {
		if plans[i].Branch != plans[j].Branch {
			return plans[i].Branch < plans[j].Branch
		}
		return plans[i].Stage < plans[j].Stage
	})
	return plans
}

// eventStageGiftPlanFor 取某分支某阶段的计划；未配置返回 nil。
func eventStageGiftPlanFor(plans []eventStageGiftPlan, branch, stage uint64) *eventStageGiftPlan {
	for i := range plans {
		if plans[i].Branch == int64(branch) && plans[i].Stage == int64(stage) {
			return &plans[i]
		}
	}
	return nil
}

// eventStageGiftPreview 构造 5008.f2 奖励预览键数组。未配置发 null：
// null 是功能上线前的现状且长期实机稳定，空 0x04 数组的客户端行为未验证，
// 不冒进（spec: event-stage-gifts）。
func eventStageGiftPreview(plans []eventStageGiftPlan, branch, stage uint64) *proto.Value {
	plan := eventStageGiftPlanFor(plans, branch, stage)
	if plan == nil {
		return nullVal()
	}
	arr := &proto.Value{Kind: proto.KindArray}
	for _, key := range plan.Preview {
		arr.Items = append(arr.Items, proto.BytesOf([]byte(key)))
	}
	return arr
}

// eventStageGiftKey 阶段礼物幂等键。
func eventStageGiftKey(uid, branch, stage int64) string {
	return fmt.Sprintf("event-stage-gift:%d:%d:%d", uid, branch, stage)
}

// ensureEventStageGifts 巡检活动分支的阶段礼物：阶段内已下发（非缺口）任务
// 全部完成且配置了奖励时发放，返回本次新发物品。调用点与分支奖杯一致：
// 14/4 结算后与 28/2 全量快照；无变化时只读。
// 与分支奖杯相同的兜底口径：首次巡检（28/2 或结算后）会对已配置且已达标、
// 但尚无 kv 标记的阶段补发，覆盖发放功能上线前的历史进度。
func (s *Server) ensureEventStageGifts(ctx context.Context, uid int64) []*store.Item {
	if s == nil || s.DB == nil {
		return nil
	}
	plans := s.eventStageGiftPlans()
	if len(plans) == 0 {
		return nil
	}
	states, err := s.DB.QualificationStates(ctx, uid)
	if err != nil {
		s.log.Warn("阶段礼物巡检读进度失败", "uid", uid, "err", err)
		return nil
	}
	stageRules := map[int64][]int64{} // key = branch*100+stage，仅活动分支非缺口规则
	for _, r := range qualificationRules {
		if eventGapRuleIDs[r.ID] {
			continue
		}
		branch := r.ID / 1000
		if _, ok := eventTaskCounts[uint64(branch)]; !ok {
			continue
		}
		key := branch*100 + (r.ID%1000)/100
		stageRules[key] = append(stageRules[key], r.ID)
	}
	var granted []*store.Item
	var names []string
	for i := range plans {
		plan := &plans[i]
		rules := stageRules[plan.Branch*100+plan.Stage-1]
		if len(rules) == 0 {
			continue // 阶段没有可判定任务，不能凭空发放
		}
		complete := true
		for _, id := range rules {
			if states[id].CompletedTS <= 0 {
				complete = false
				break
			}
		}
		if !complete {
			continue
		}
		items, name := s.grantEventStageGift(ctx, uid, plan)
		granted = append(granted, items...)
		if len(items) > 0 {
			names = append(names, name)
		}
	}
	if len(names) > 0 {
		s.broadcastQualificationCups(uid, names)
	}
	return granted
}

// grantEventStageGift 单阶段发放：物品先按配置数量构造到正常背包，幂等键与全部入账
// 同一经济事务提交；已发过返回 nil。播报文案取首个物品的静态中文名，
// 由调用方合并成一条发送。活动奖励是普通商品，不能复用资格奖杯的 house/奖赏分组。
func (s *Server) grantEventStageGift(ctx context.Context, uid int64, plan *eventStageGiftPlan) ([]*store.Item, string) {
	prepared := make([]*store.Item, 0, len(plan.Gifts))
	for _, g := range plan.Gifts {
		it, err := s.prepareNewItemWithCount(ctx, uid, g.ItemID, "backpack", g.Qty, "")
		if err != nil {
			s.log.Warn("阶段礼物物品构造失败", "uid", uid, "branch", plan.Branch,
				"stage", plan.Stage, "item", g.ItemID, "err", err)
			return nil, ""
		}
		prepared = append(prepared, it)
	}
	tx, err := beginEconomyTx(ctx, s.DB)
	if err != nil {
		s.log.Warn("阶段礼物事务开启失败", "uid", uid, "err", err)
		return nil, ""
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO kv(k,v) VALUES(?,?)`,
		eventStageGiftKey(uid, plan.Branch, plan.Stage), "granted")
	if err != nil {
		s.log.Warn("阶段礼物幂等键写入失败", "uid", uid, "err", err)
		return nil, ""
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return nil, "" // 已发放过
	}
	var out []*store.Item
	for _, it := range prepared {
		row, _, err := grantPreparedItemTx(ctx, tx, it, true)
		if err != nil {
			s.log.Warn("阶段礼物发放失败", "uid", uid, "branch", plan.Branch,
				"stage", plan.Stage, "item", it.ItemID, "err", err)
			return nil, ""
		}
		out = append(out, row)
	}
	if err := tx.Commit(); err != nil {
		s.log.Warn("阶段礼物提交失败", "uid", uid, "err", err)
		return nil, ""
	}
	return out, plan.Gifts[0].ItemID
}
