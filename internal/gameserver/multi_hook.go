package gameserver

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"

	"rf4server/internal/store"
)

// 多钩钓组一次上多条鱼。相关规格见
// specs/changes/multi-hook-multi-catch/spec.md。
//
// 设计要点：客户端本地跑搏鱼物理，服务端只能通过每条鱼的 f8 拉力与 f5 体力池
// 影响手感，断线由客户端判定后经 17/9 上报。所以"一次多条"的安全边界必须放在
// 派鱼之前（数量/总重/逐条体量/总拉力预算），不能指望事后拦截。

// multiHookConfig 是多钩闸门参数。Enabled=false 时所有调用方都应退化为单鱼。
type multiHookConfig struct {
	Enabled   bool
	MaxTotal  int     // 本杆最多鱼数（含主鱼）
	BiteProb  float64 // 每个额外钩位的独立咬口概率
	BudgetK   float64 // 总重预算系数：预算 = K × 钓组最弱环
	SizeDecay float64 // 逐条降档：第 i 条单条上限 × α^(i-1)
	PullK     float64 // 总拉力预算系数：κ × 最弱环
}

// multiHookConfig 读取运行参数；参数缺省即旧行为（关闭）。
func (s *Server) multiHookConfig() multiHookConfig {
	cfg := multiHookConfig{
		Enabled:   s.tunableF("multi_hook_bite", 0) > 0,
		MaxTotal:  int(math.Round(s.tunableF("multi_hook_max", 2))),
		BiteProb:  clampFloat(s.tunableF("multi_hook_bite_chance", 0.5), 0, 1),
		BudgetK:   clampFloat(s.tunableF("multi_hook_weight_budget", 0.55), 0, 1),
		SizeDecay: clampFloat(s.tunableF("multi_hook_size_decay", 0.6), 0, 1),
		PullK:     clampFloat(s.tunableF("multi_hook_pull_budget", 0.8), 0.1, 1),
	}
	if cfg.MaxTotal < 2 {
		cfg.MaxTotal = 2
	}
	if cfg.MaxTotal > 6 {
		cfg.MaxTotal = 6
	}
	if !cfg.Enabled {
		cfg.MaxTotal = 1
	}
	return cfg
}

// hookMountedForExtraHooks 返回额外鱼的钩是否也补挂竿位（tunables，默认开）。
//
// 官方口径：钩 f09 的挂竿位（1024）是客户端钩实体进入搏鱼态的运行位，缺位时
// 整条鱼"能上岸但全程无演出"。主鱼在 14/11 时补过，额外鱼没人补，所以它们的
// 钩一直是静止件，鱼模型不会挂上去——玩家只能看到主鱼一条。
func (s *Server) hookMountedForExtraHooks() bool {
	if s == nil {
		return false
	}
	return s.tunableF("multi_hook_hook_mount", 1) > 0
}

// extraEntityPushEnabled 返回是否给额外鱼派 `14/14` 实体（tunables 键
// multi_hook_extra_entity，**默认 0 = 不派**）。
//
// 2026-11-13 17:26 实测（tools/fish_probe.py 给 Fish 类 82 个方法下钩）：
// 服务端连发两条 `14/14`（主鱼 513g + 额外 3618g，逐条都拿到 `14/12 f0=6`），
// 客户端整杆只走了一次 `Fish..ctor`，`Update`/`FixedUpdate` 也只命中同一个
// 实例——它的 FishingScene 就是单鱼模型，多派没有模型可挂。
//
// 更糟的是补派会把正在搏的主鱼挤出槽位：额外鱼 ack 之后 12ms，客户端就对
// 主鱼回了 `14/12 f0=1`，服务端按"真脱钩"处理，那杆 513g 主鱼根本没进结算。
// 所以默认不派实体：渔获全部留给 `14/4` 的 Batch 一并结算，鱼种靠聊天播报。
func (s *Server) extraEntityPushEnabled() bool {
	if s == nil {
		return false
	}
	return s.tunableF("multi_hook_extra_entity", 0) > 0
}

// extraPushDeferredOnStrike 返回额外鱼的鱼实体是否推迟到主鱼刺鱼确认后再派
// （tunables 键 multi_hook_push_on_strike，默认 1）。仅在 multi_hook_extra_entity
// 打开（确实要派实体）时才有意义。
//
// 客户端 Fish 状态机是 1(咬口) → 2(搏鱼) → 4(离场)。`14/14` 只把鱼实体放进
// 钓组、停在"等待玩家刺鱼"的咬口态，模型不挂到钩上；只有玩家起竿（`14/11`）
// 之后客户端才把这条鱼转进搏鱼态并把它挂到 f23 指定的钩上。
//
// 实机 2026-11-13 16:38：三条鱼同时派 `14/14`，客户端只对主鱼回了 `14/11`，
// 另外两条一直停在 `14/12 f0=6`，于是钩上永远只有一条鱼。既然咬口态不演，
// 就别在咬口时派——等主鱼刺中确认、客户端已经进入搏鱼态，再把额外鱼补进去。
func (s *Server) extraPushDeferredOnStrike() bool {
	if s == nil {
		return false
	}
	return s.tunableF("multi_hook_push_on_strike", 1) > 0
}

// deferExtrasUntilStrike 记录"本杆额外鱼先留在队列里等刺鱼"，只做诊断日志。
func (s *Server) deferExtrasUntilStrike(st *FishState, gear string) {
	if s == nil || st == nil {
		return
	}
	st.mu.Lock()
	n := len(st.Extra)
	st.mu.Unlock()
	if n > 0 {
		s.log.Info("多钩额外鱼入队等待刺鱼", "rod", shortGUID(gear), "条数", n,
			"派实体", s.extraEntityPushEnabled())
	}
}

// pushExtrasAfterStrike 在主鱼刺鱼确认后处理队列里的额外鱼。
//
// 派实体时留一个短延迟：`14/11` 到达的瞬间客户端还在做刺鱼切态，立刻灌
// `14/14` 容易被打回拒收（f0=3/5）。延迟可配 multi_hook_push_delay_ms，默认 350ms。
//
// 不派实体时（默认）这里什么都不做：渔获留给 14/4 一并结算。
func (s *Server) pushExtrasAfterStrike(sess *Session, st *FishState, gear string, rigType int64) {
	if sess == nil || st == nil {
		return
	}
	if s.extraEntityPushEnabled() && !s.extraPushDeferredOnStrike() {
		return
	}
	st.mu.Lock()
	n := len(st.Extra)
	gen := st.castGeneration
	st.mu.Unlock()
	if n == 0 {
		return
	}
	delay := time.Duration(s.tunableF("multi_hook_push_delay_ms", 350)) * time.Millisecond
	if delay <= 0 {
		s.pushExtrasAtStrike(sess, st, gear, rigType, gen)
		return
	}
	time.AfterFunc(delay, func() { s.pushExtrasAtStrike(sess, st, gear, rigType, gen) })
}

// pushExtrasAtStrike 是 pushExtrasAfterStrike 的到期执行体：状态已变（换杆、
// 收竿、本杆作废）就放弃，避免把上一杆的鱼派进新一杆。
func (s *Server) pushExtrasAtStrike(sess *Session, st *FishState, gear string, rigType int64, gen uint64) {
	st.mu.Lock()
	ok := st.castGeneration == gen && len(st.Extra) > 0 && st.CastActive && !st.Resolved
	st.mu.Unlock()
	if !ok {
		return
	}
	if !s.extraEntityPushEnabled() {
		return
	}
	s.log.Info("多钩刺鱼后补派额外鱼", "rod", shortGUID(gear))
	s.pushExtraFish(sess, st, gear, rigType)
}

// multiHookStrengthKg 返回本钓组承力上限（最弱环 × 过载系数）；0 表示未知。
func (s *Server) multiHookStrengthKg(ctx context.Context, uid int64, gearGUID string) float64 {
	if s == nil || s.DB == nil || gearGUID == "" {
		return 0
	}
	ids := s.rigGearIDs(ctx, uid, gearGUID)
	spec := s.loadGearSpecForRig(ctx, ids, gearGUID)
	return gearStrengthCapKg(spec)
}

// rollExtraFishForRig 在主鱼之后，为其余已挂饵的钩位各掷一次骰。
//
// 三道闸门依次生效：
//  1. 钩位互斥：主鱼占掉的钩、已出鱼的钩不再参与；
//  2. 逐条降档：第 i 条额外鱼的单条上限 = α^(i-1) × 0.35 × 预算；
//  3. 总重预算：ΣW 不得越过 K × 最弱环，越界从最重的开始丢（直接不咬）。
//
// 返回的鱼已绑定各自钩位与饵快照，除重量/种类外沿用主鱼的咬口条件。
func (s *Server) rollExtraFishForRig(ctx context.Context, uid int64, gearGUID, loc, bait string, bonus fishingSkillBonus, c fishRollContext, primary *RolledFish, cfg multiHookConfig) []*RolledFish {
	if primary == nil || !cfg.Enabled || cfg.MaxTotal <= 1 || gearGUID == "" {
		return nil
	}
	groups := s.baitedHookSets(ctx, gearGUID)
	if len(groups) < 2 {
		// 多钩钓组已装好但只有一个钩位挂了饵：给一条诊断日志，
		// 免得玩家以为"开了多钩却没反应"。顺带把钩位/饵位的 extra 与归属
		// 打出来，否则只能看到"1 组"却不知道是哪一步掉的
		// （实机 2026-11-13 07:40 重装钓组后就是这么退化的）。
		rigType := s.rigTypeForWrite(ctx, gearGUID)
		comps := s.storedRigComponents(ctx, gearGUID)
		if slots := hookPositionsIn(rigType, comps); slots >= 2 {
			s.log.Info("多钩未生效：只有一个钩位挂了饵", "rod", shortGUID(gearGUID),
				"钩位", slots, "已挂饵钩位", len(groups),
				"钩位明细", hookExtraDetail(comps), "饵位明细", baitExtraDetail(ctx, s, gearGUID, comps))
		}
		return nil
	}
	ids := s.rigGearIDs(ctx, uid, gearGUID)
	spec := s.loadGearSpecForRig(ctx, ids, gearGUID)
	budget := 0.0
	if strength := gearStrengthCapKg(spec); strength > 0 {
		budget = cfg.BudgetK * strength
	}
	// 每次多钩判定都留一条明细，方便对照"钩位几个、几个真的挂了饵"。
	if slots := hookPositionsIn(s.rigTypeForWrite(ctx, gearGUID), s.storedRigComponents(ctx, gearGUID)); slots >= 2 {
		s.log.Info("多钩判定", "rod", shortGUID(gearGUID), "钩位", slots,
			"已挂饵钩位", len(groups), "总重预算kg", math.Round(budget*10)/10)
	}

	used := map[string]bool{}
	if primary.HookGUID != "" {
		used[strings.ToLower(primary.HookGUID)] = true
	}
	usedKg := float64(primary.WeightG) / 1000

	rand.Shuffle(len(groups), func(i, j int) { groups[i], groups[j] = groups[j], groups[i] })
	out := make([]*RolledFish, 0, cfg.MaxTotal-1)
	var outKg float64
	for _, group := range groups {
		if len(out) >= cfg.MaxTotal-1 {
			break
		}
		if group.hookGUID == "" || len(group.baits) == 0 || used[strings.ToLower(group.hookGUID)] {
			continue
		}
		if rand.Float64() > cfg.BiteProb {
			continue
		}
		selected := spec
		if !s.applyBaitedHookSpec(ctx, group, &selected) {
			continue
		}
		capKg := 0.0
		if budget > 0 {
			capKg = math.Pow(cfg.SizeDecay, float64(len(out)+1)) * 0.35 * budget
			if remain := budget - usedKg - outKg; remain < capKg {
				capKg = remain
			}
			if capKg <= 0.2 {
				break
			}
		}
		if own := s.fishWeightCapKg(selected); own > 0 && (capKg <= 0 || own < capKg) {
			capKg = own
		}
		cc := c
		cc.baits = group.baits
		cc.hook = selected
		f := s.rollFishWithRules(loc, group.baits[0], capKg, bonus, cc)
		if f == nil {
			continue
		}
		f.HookGUID = group.hookGUID
		f.BaitGUIDs = strings.Join(group.baitGUIDs, " ")
		f.BaitsKnown = true
		// 同一杆、同一水层：咬口条件沿用主鱼快照，避免多算一次天气/船位。
		f.HappyHour, f.HappyHourKnown = primary.HappyHour, primary.HappyHourKnown
		f.LocLevel = primary.LocLevel
		f.DepthCM = primary.DepthCM
		f.OnBoat = primary.OnBoat
		f.Drift = primary.Drift
		f.CastM = primary.CastM
		f.IsNight = primary.IsNight
		f.QualBait = s.qualificationMountedBait(ctx, f.BaitGUIDs)
		used[strings.ToLower(group.hookGUID)] = true
		out = append(out, f)
		outKg += float64(f.WeightG) / 1000
	}
	return out
}

// applyMultiHookPullBudget 把一组鱼的 f8 拉力按体重占比压进总拉力预算。
//
// 客户端可能把同时挂住的鱼的张力相加，服务端无法直接设定合成曲线，
// 只能保证 Σf8 不超过 κ × 最弱环，让"总重力度条"落在钓组承力之内。
func (s *Server) applyMultiHookPullBudget(fish []*RolledFish, strengthKg, pullK float64) {
	if len(fish) < 2 || strengthKg <= 0 || pullK <= 0 {
		return
	}
	budgetN := pullK * strengthKg * newtonPerKgf
	total := 0.0
	for _, f := range fish {
		if f != nil {
			total += s.fishFightPullFor(f)
		}
	}
	if total <= budgetN || total <= 0 {
		return
	}
	ratio := budgetN / total
	for _, f := range fish {
		if f == nil {
			continue
		}
		base := s.fishFightPullFor(f)
		f.Force = math.Max(0.5, math.Round(base*ratio*10000)/10000)
	}
}

// pushExtraFish 逐条推送额外鱼的 14/14；客户端未接受的不留在队列里。
// 返回本次真正推出去的鱼，供调用方拼"逐钩清单"提示。
func (s *Server) pushExtraFish(sess *Session, st *FishState, gear string, rigType int64) []*RolledFish {
	if sess == nil || st == nil {
		return nil
	}
	st.mu.Lock()
	extras := append([]*RolledFish(nil), st.Extra...)
	generation := st.castGeneration
	st.mu.Unlock()
	if len(extras) == 0 {
		return nil
	}
	var pushed []*RolledFish
	sent := map[string]bool{}
	mountExtra := s.hookMountedForExtraHooks()
	for _, f := range extras {
		if f == nil {
			continue
		}
		// 客户端明确拒收过这条鱼的实体：不再重派，但渔获留在队列里，
		// 14/4 照常结算——鱼已经咬了钩，只是客户端画不出来。
		if f.NoEntity {
			continue
		}
		// 先让这枚钩进搏鱼态，再派鱼实体：钩还没挂竿位时客户端会把鱼当成
		// 静止件上的死物，模型不挂。派完再补一次，防实体创建把运行位重置。
		if mountExtra {
			sess.Push4_22HookGUID(f.HookGUID, true)
		}
		if sess.Push14_14(f, f.HookGUID, gear, rigType) {
			sent[f.FishGUID] = true
			pushed = append(pushed, f)
			s.log.Info("14/14 发额外鱼", "rod", shortGUID(gear), "fish", shortGUID(f.FishGUID),
				"hook", shortGUID(f.HookGUID), "鱼种", f.Species, "重量_g", f.WeightG)
			if mountExtra && sess.Push4_22HookGUID(f.HookGUID, true) {
				s.log.Info("14/14 额外鱼钩挂竿位", "rod", shortGUID(gear),
					"fish", shortGUID(f.FishGUID), "hook", shortGUID(f.HookGUID))
			}
		}
	}
	st.mu.Lock()
	if generation != st.castGeneration {
		st.mu.Unlock()
		return pushed
	}
	kept := st.Extra[:0]
	for _, f := range st.Extra {
		if f != nil && (sent[f.FishGUID] || f.NoEntity) {
			kept = append(kept, f)
		}
	}
	st.Extra = kept
	if len(st.Extra) == 0 {
		st.HoldMulti = false
	}
	st.mu.Unlock()
	return pushed
}

// promoteExtraLocked 把队首额外鱼提升为主战斗对象。
// PushSent 置真：这尾鱼已经推过 14/14，不应再触发一次派鱼。
func promoteExtraLocked(st *FishState) *RolledFish {
	if st == nil {
		return nil
	}
	for len(st.Extra) > 0 {
		f := st.Extra[0]
		st.Extra = st.Extra[1:]
		if f == nil || f.FishGUID == "" {
			continue
		}
		st.Pending = f
		st.Hooked = f.HookConfirmed
		st.Resolved = false
		st.PushSent = true
		st.PendingRepush = 0
		st.HoldMulti = len(st.Extra) > 0
		return f
	}
	return nil
}

// promoteAfterActiveGone 在当前鱼离场后提升队首，并保持钓组在水中。
func (s *Server) promoteAfterActiveGone(st *FishState) *RolledFish {
	if st == nil {
		return nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	next := promoteExtraLocked(st)
	if next != nil {
		st.CastActive = true
		st.InWater = true
	}
	return next
}

// repushPromotedAfterTakeover 在队首接管成主战斗对象后，把它的实体重新推给客户端。
//
// 客户端在 14/12 的脱钩播报后会把钩上的鱼模型和力度条一起清掉，而队列里的
// 下一条在服务端仍是"已经推过 14/14"（promoteExtraLocked 里 PushSent=true）。
// 实机 2026-11-13 15:20:53：主鱼 d_zubatka 报 f0=2，服务端立刻接管 sebastes_n，
// 但再没发过任何派鱼包——玩家看到的就是"脱钩后力度条不显示鱼、钩上看不见
// 模型，收完线却又钓上来一条"。所以接管必须重走一次派鱼，PushSent 交回
// 推送结果决定，而不是无条件当已推送。
func (s *Server) repushPromotedAfterTakeover(sess *Session, st *FishState) {
	if sess == nil || st == nil || s == nil {
		return
	}
	st.mu.Lock()
	f := st.Pending
	gear := st.GearGuid
	generation := st.castGeneration
	ready := f != nil && st.CastActive && !st.Resolved && st.pushing == nil
	if ready {
		st.PushSent = false
	}
	st.mu.Unlock()
	if !ready {
		return
	}
	s.log.Info("多钩接管重派鱼", "rod", shortGUID(gear), "fish", shortGUID(f.FishGUID),
		"hook", shortGUID(f.HookGUID), "鱼种", f.Species, "重量_g", f.WeightG)
	s.pushPendingBite(sess, st, f, gear, generation)
}

// settleExtraOnReelIn 在 14/4 收竿时把本杆其余已咬钩的鱼一并结算。
//
// 为什么不能在收竿之后再"接管下一条"：客户端把 14/4 当成"本杆结束"，收竿后
// 立刻回到手持状态并开始下一次抛投（实机 2026-11-13 15:40:29 收竿、15:40:31
// 就发出 14/1 抛投距离计算）。此时服务端再派 14/14，客户端只会回一条 14/12
// 却不给力度条也不挂模型——玩家看到的就是"拉上来只有一条鱼"。
//
// 所以本杆的其余鱼必须在这一次 14/4 里结算完；它们挂在 Batch 上，等玩家对
// 主鱼做出 14/5 入护或 14/6 放生的选择时一起处理。
func (s *Server) settleExtraOnReelIn(ctx context.Context, uid int64, st *FishState, rigType int64) []*RolledFish {
	if s == nil || st == nil {
		return nil
	}
	st.mu.Lock()
	extras := st.Extra
	gear := st.GearGuid
	st.Extra = nil
	st.HoldMulti = false
	st.mu.Unlock()
	if len(extras) == 0 {
		return nil
	}
	settled := make([]*RolledFish, 0, len(extras))
	for _, f := range extras {
		if f == nil || f.FishGUID == "" {
			continue
		}
		if res, _ := s.settleCatchXPWithQualifications(ctx, uid, f, rigType, nil); res == nil {
			s.log.Warn("14/4 多钩额外鱼结算失败，按脱落处理",
				"fish", shortGUID(f.FishGUID), "鱼种", f.Species, "重量_g", f.WeightG)
			continue
		}
		settled = append(settled, f)
	}
	if len(settled) == 0 {
		return nil
	}
	st.mu.Lock()
	st.Batch = settled
	st.mu.Unlock()
	parts := make([]string, 0, len(settled))
	var totalG int64
	for _, f := range settled {
		parts = append(parts, fmt.Sprintf("%s/%dg", f.Species, f.WeightG))
		totalG += f.WeightG
	}
	s.log.Info("14/4 多钩一并结算", "rod", shortGUID(gear), "条数", len(settled),
		"合计_g", totalG, "明细", strings.Join(parts, ","))
	return settled
}

// dropExtraFish 清空本杆未结算的额外鱼（主鱼咬钩未刺中就收竿时使用）。
func dropExtraFish(st *FishState) int {
	if st == nil {
		return 0
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	n := len(st.Extra)
	st.Extra = nil
	st.HoldMulti = false
	return n
}

// pendingExtraCount 返回队列里还有多少条待处理的额外鱼（内部持锁）。
func (s *Server) pendingExtraCount(st *FishState) int {
	if st == nil {
		return 0
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return len(st.Extra)
}

// extraIndexByGUID 返回队列中匹配鱼 GUID 的下标；-1 表示不是额外鱼。
func extraIndexByGUID(st *FishState, guid string) int {
	if st == nil || guid == "" {
		return -1
	}
	for i, f := range st.Extra {
		if f != nil && strings.EqualFold(f.FishGUID, guid) {
			return i
		}
	}
	return -1
}

// extraIndexByAnyGUID 在请求里找队列中的鱼：不假设 14/11 的参数布局。
func extraIndexByAnyGUID(st *FishState, guids []string) int {
	for _, g := range guids {
		if i := extraIndexByGUID(st, g); i >= 0 {
			return i
		}
	}
	return -1
}

// removeExtraAt 移除队列中的第 i 条（脱钩 / 客户端拒收）。
func removeExtraAt(st *FishState, i int) *RolledFish {
	if st == nil || i < 0 || i >= len(st.Extra) {
		return nil
	}
	f := st.Extra[i]
	st.Extra = append(st.Extra[:i], st.Extra[i+1:]...)
	if len(st.Extra) == 0 {
		st.HoldMulti = false
	}
	return f
}

// hookExtraDetail 按 extra 升序拼钩位明细（extra:短GUID），用于对照子槽分组。
func hookExtraDetail(comps map[int64]rigComp) string {
	keys := rigCompKeysOfCategory(comps, catHook)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%d:%s", comps[key].Extra, shortGUID(comps[key].GUID)))
	}
	return strings.Join(parts, ",")
}

// baitExtraDetail 拼饵/拟饵明细（品类/extra>库里记下的钩位，- 表示没记录）。
//
// 多钩压成一组时，光看"已挂饵钩位=1"分不清是记录写坏了、extra 没读出来，
// 还是钩组件根本没装齐；这三样拼在一起就能一眼定位。
func baitExtraDetail(ctx context.Context, s *Server, rod string, comps map[int64]rigComp) string {
	saved := map[string]string{}
	if rows, err := s.DB.SQL.QueryContext(ctx,
		`SELECT key,value FROM rod_setting WHERE guid=? AND key LIKE 'bait_hook:%'`, rod); err == nil {
		for rows.Next() {
			var key, hook string
			if rows.Scan(&key, &hook) == nil {
				saved[strings.ToLower(strings.TrimPrefix(key, "bait_hook:"))] = hook
			}
		}
		rows.Close()
	}
	var parts []string
	for _, cat := range []int64{catBait, catLure} {
		for _, key := range rigCompKeysOfCategory(comps, cat) {
			rec := saved[strings.ToLower(comps[key].GUID)]
			if rec != "" {
				rec = shortGUID(rec)
			} else {
				rec = "-"
			}
			parts = append(parts, fmt.Sprintf("%d/%d>%s", cat, comps[key].Extra, rec))
		}
	}
	return strings.Join(parts, ",")
}

// multiHookKeepChat 生成多钩的入护汇总播报。
//
// 客户端结算卡（801）只有一尾的位置，杆上也只有一个鱼模型，所以"另外几钩
// 还上了鱼"这件事玩家在界面上完全看不到。这里只报条数与合计重量，不给逐钩
// 明细——玩家要看的是"这杆一共几尾、多重"。
func multiHookKeepChat(kept []*store.KeepnetEntry) string {
	var totalG int64
	for _, e := range kept {
		if e == nil {
			continue
		}
		totalG += e.WeightG
	}
	return fmt.Sprintf("多钩：另入护 %d 尾，合计 %.2f kg", len(kept), float64(totalG)/1000)
}
