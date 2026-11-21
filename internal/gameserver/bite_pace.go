package gameserver

// 饵效控速（tunable bite_pace_enabled，默认关）。
//
// 现状判鱼链里「饵好不好」只影响鱼种权重（钓到什么鱼），不影响来鱼速度：
// bite_probability 默认 1.0、路亚族 1.5~6 秒短窗必出鱼、延迟链没有饵效因子。
// 本文件给判鱼管线加一条独立的节奏通路：
//
//   - baitPaceScore：钩上饵对本水域名册的「对口分」0~1，综合 companion 档位、
//     本图饵证据、rf4records 实测占比与生态食性四类既有证据源，全部复用缓存；
//   - paceTierFor：分数 → 好/中/差三档，映射延迟乘数与单轮判定概率
//     （底层杆与路亚轮各一套）；
//   - paceStandardBiteDelay / paceStandardClamp：pace 模式下底层杆
//     （非路亚族）的延迟后处理（乘数 + 按档位区间 clamp）；
//   - groundbaitPaceFactor：打窝把底层杆延迟压向 3 秒下限；死窝不加速，
//     路亚族豁免与死窝闸门同口径。
//
// 路亚族（含海钓铁板 50-54）在 pace 模式下按「饵分 × 当前动作」的复合档位
// 取等待窗与 fire 开口率：好动作才给好档（1.5~6s 收线短窗），中/差档拉长到
// 底钓量级且不受 bite_delay_lure_max 封顶；动作改善时立即重排（只升不降），
// fire 时动态取档位开口率。原「14/14 晚于收线窗口」的担忧由 fire 时的
// st.InWater 检查、14/4 收竿停表与 biteTicket 票据失效兜住。
// 饵效只控节奏，不改判鱼候选与权重；与硬性鱼口门（bite_gate_enabled）/
// strict 规则互相独立。语义与测试口径见 specs/changes/lure-action-pace/spec.md
// 与 specs/changes/bite-pace/spec.md。

import (
	"context"
	"math"
	"time"

	"rf4server/internal/store"
)

// paceDietTTL 名册食性聚合缓存的存活期，对齐 tunables 热更节奏。
// 每张地图独立计时：访问新图只刷新自己的时间戳，不复活其他已过期条目
// （PR #531 审查 P2-4）；fish_ecology 全量重载/失效时整体清空。
const paceDietTTL = 30 * time.Second

// paceRosterDiet 一个水域名册的食性聚合：拟饵取 diet_json.lure 的最大权重，
// 天然饵按食性键（biteGateDietKeys 映射）取各键最大权重。
type paceRosterDiet struct {
	lureMax float64
	dietMax map[string]float64
}

// paceDietCacheEntry 按 statLoc 独立过期的缓存条目。
type paceDietCacheEntry struct {
	diet *paceRosterDiet
	at   time.Time
}

// fishPaceState 一杆的节奏因子。Known=false 表示 pace 模式未生效或尚未计算
// （重启恢复、开关关闭），消费方一律按中性处理。
type fishPaceState struct {
	Known    bool
	Score    float64
	Factor   float64 // 延迟乘数（好饵=1.0）
	Prob     float64 // 底层杆（非路亚族）单轮判定概率
	LureProb float64 // 路亚轮单轮判定概率
	Ground   float64 // 打窝压缩（1=无窝/路亚）
	Tier     int     // 0=差 1=中 2=好，仅日志用
}

func neutralFishPaceState() fishPaceState {
	return fishPaceState{Factor: 1, Prob: 1, LureProb: 1, Ground: 1}
}

// probForRig 按钓组族取单轮判定概率。
func (p fishPaceState) probForRig(rigType int64) float64 {
	if rigTypeLureFamily[rigType] {
		return p.LureProb
	}
	return p.Prob
}

// paceEnabled pace 总开关。
func (s *Server) paceEnabled() bool {
	return s.tunableF("bite_pace_enabled", 0) == 1
}

// paceTierFor 对口分 → 三档因子与概率。阈值/乘数/概率全部 tunable 可调，
// good 档乘数恒为 1（好饵的定义就是「不拖慢」）。路亚轮概率默认值收敛在
// paceLureProbForTier 单一来源（好 1.0 / 中 0.5 / 差 0.12，窗口即等待）。
func (s *Server) paceTierFor(score float64) (factor, prob, lureProb float64, tier int) {
	switch {
	case score >= s.tunableF("pace_score_good", 0.75):
		return 1.0, s.tunableF("pace_prob_good", 1.0), s.paceLureProbForTier(2), 2
	case score >= s.tunableF("pace_score_mid", 0.45):
		return s.tunableF("pace_factor_mid", 1.6), s.tunableF("pace_prob_mid", 0.5), s.paceLureProbForTier(1), 1
	default:
		return s.tunableF("pace_factor_bad", 3.0), s.tunableF("pace_prob_bad", 0.12), s.paceLureProbForTier(0), 0
	}
}

func paceTierName(tier int) string {
	switch tier {
	case 2:
		return "good"
	case 1:
		return "mid"
	}
	return "bad"
}

// paceStandardClamp pace 模式底层杆的 clamp，顺序与 legacy standardBiteDelay
// 一致（floor → cap → scale），保证 bite_delay_scale 管理倍率继续生效。
func paceStandardClamp(d, floorS, capS, scale float64) float64 {
	if d < floorS {
		d = floorS
	}
	if capS > 0 && d > capS {
		d = capS
	}
	if scale != 1.0 {
		d *= scale
	}
	return math.Max(0.05, d)
}

// paceFactorFor 档位延迟乘数（采样分布形状微调；好饵恒 1.0 不拖慢）。
// 等待区间的直接控制在 paceFloorCapFor 的档位区间。
func (s *Server) paceFactorFor(tier int) float64 {
	switch tier {
	case 1:
		return s.tunableF("pace_factor_mid", 1.6)
	case 0:
		return s.tunableF("pace_factor_bad", 3.0)
	}
	return 1.0
}

// paceFloorCapFor 按档位与钓组族返回 pace 模式的等待区间（下限与上限秒数）。
// 底钓/浮钓按档位各一套区间（管理后台「数值调整 → 咬钩」直接可调：好饵/差饵
// 在基础区，中饵在进阶）；海底钓族沿用海钓窗；打窝压缩时下限让位给窝料地板。
func (s *Server) paceFloorCapFor(tier int, marine bool, ground float64) (float64, float64) {
	if marine {
		return s.tunableF("pace_marine_min", 3), s.tunableF("pace_marine_max", 30)
	}
	var floorS, capS float64
	switch tier {
	case 1:
		floorS, capS = s.tunableF("pace_mid_min", 10), s.tunableF("pace_mid_max", 60)
	case 0:
		floorS, capS = s.tunableF("pace_bad_min", 20), s.tunableF("pace_bad_max", 60)
	default:
		floorS, capS = s.tunableF("pace_good_min", 6), s.tunableF("pace_good_max", 60)
	}
	if ground < 1 {
		if g := s.tunableF("pace_groundbait_floor", 3.0); g < floorS {
			floorS = g
		}
	}
	return floorS, capS
}

// paceLureRearmDebounce 动作改善重排的最小间隔：14/7 每帧都来，动作位在
// 收线中会高频切换，防抖避免 timer 反复作废重排。
const paceLureRearmDebounce = 2 * time.Second

// paceLureActionQuality 动作分 0~1：按路亚类型 × 当前动作的权重归一，口径与
// 鱼猎阶段 lureHuntActionQuality 一致（(w-0.8)/0.54，顶格动作 1.34 → 1.0，
// 中性动作 1.0 → 0.37）。无动作上报（刚落水、尚未收线）按 0.45 中性——
// 「好的动作出来了才算好饵效」，中性不给好档。
func paceLureActionQuality(lureType, action string) float64 {
	if action == "" {
		return 0.45
	}
	return clampFloat((lureActionTypeWeight(lureType, action)-0.8)/0.54, 0, 1)
}

// paceLureCombinedScore 路亚族的复合对口分 = 饵分 × (0.4 + 0.6 × 动作分)。
// 饵不对口是硬伤（差饵无论什么动作都到不了好档），动作不对也拉不满；
// 三档阈值沿用 pace_score_good/mid。口径见 specs/changes/lure-action-pace/spec.md。
func paceLureCombinedScore(baitScore float64, lureType, action string) float64 {
	return clampFloat(baitScore*(0.4+0.6*paceLureActionQuality(lureType, action)), 0, 1)
}

// paceLureWindowFor pace 模式路亚族按复合档位的等待区间（秒）。好档沿用
// pace_lure_min/max（默认 1.5~6 收线短窗，仍受 bite_delay_lure_max 封顶）；
// 中/差档默认对齐底钓中/差饵区间（10~60 / 20~60）且不再封顶——
// 「路亚最长等待」在 pace 模式下只管好档。
func (s *Server) paceLureWindowFor(tier int) (lmin, lmax float64) {
	switch tier {
	case 2:
		return s.tunableF("pace_lure_min", 1.5), s.tunableF("pace_lure_max", 6)
	case 1:
		return s.tunableF("pace_lure_mid_min", 10), s.tunableF("pace_lure_mid_max", 60)
	}
	return s.tunableF("pace_lure_bad_min", 20), s.tunableF("pace_lure_bad_max", 60)
}

// paceLureProbForTier pace 模式路亚族 fire 时的开口率。好档 1.0（窗口即等待，
// 1.5~6s 内必来）；中/差档窗口已是惩罚，低开口率再叠加出底钓同档的干等手感。
// fire 时动态取值：装排后动作变差的轮次按低概率空掷，再按差档长窗重排。
func (s *Server) paceLureProbForTier(tier int) float64 {
	switch tier {
	case 2:
		return s.tunableF("pace_lure_prob_good", 1.0)
	case 1:
		return s.tunableF("pace_lure_prob_mid", 0.5)
	}
	return s.tunableF("pace_lure_prob_bad", 0.12)
}

// paceLureStateFor 由饵分与当前动作合成路亚族的节奏因子。Score 保留纯饵分
// （fire 动态开口率要用），Tier 存复合档位。
func (s *Server) paceLureStateFor(baitScore float64, lureType, action string, ground float64) fishPaceState {
	factor, prob, lureProb, tier := s.paceTierFor(paceLureCombinedScore(baitScore, lureType, action))
	return fishPaceState{Known: true, Score: baitScore, Factor: factor, Prob: prob,
		LureProb: lureProb, Ground: ground, Tier: tier}
}

// paceStandardBiteDelay pace 模式底层杆的完整延迟：采样 ×天气×技能×档位乘数
// ×ground 后按档位区间 clamp。调用方需持有 st.mu。
func (s *Server) paceStandardBiteDelay(sample, weather, speed float64, tier int, ground float64, marine bool, scale float64) float64 {
	floorS, capS := s.paceFloorCapFor(tier, marine, ground)
	return paceStandardClamp(sample*weather*speed*s.paceFactorFor(tier)*ground, floorS, capS, scale)
}

// paceFloorShrink 打窝压缩落水保护：只缩短不拉长，pace_groundbait_floor 兜底；
// fixed 指定延迟的管理员口径原样保留（与 biteDelayInWeather 的直通一致），
// 路亚族固定 1.2 秒不动。运行中热关闭 bite_pace_enabled 时立即回到原保护
// （审查 P2-1：不得只看 st.PaceKnown 残留）。
func (s *Server) paceFloorShrink(cfg biteDelaySettings, st *FishState, lure bool, floor time.Duration) time.Duration {
	if st == nil || !s.paceEnabled() || !st.PaceKnown || cfg.fixed > 0 || lure || st.PaceGround >= 1 {
		return floor
	}
	shrunk := time.Duration(float64(floor) * st.PaceGround)
	if gfloor := time.Duration(s.tunableF("pace_groundbait_floor", 3.0) * float64(time.Second)); gfloor < floor && shrunk < gfloor {
		shrunk = gfloor
	}
	// bite-delay-floor 契约：任何倍率下落水保护不早于 800ms。
	if shrunk < 800*time.Millisecond {
		shrunk = 800 * time.Millisecond
	}
	if shrunk < floor {
		return shrunk
	}
	return floor
}

// meanPaceStandardDelay pace 模式底层杆的平均判鱼间隔（查鱼助手口径）：
// 官方样本集本身就是分布，逐条变换后取均值即精确均值。
func (s *Server) meanPaceStandardDelay(cfg biteDelaySettings, weather, speed float64, tier int, ground float64, marine bool) float64 {
	if len(officialPushDelayEligible) == 0 {
		return s.paceStandardBiteDelay(officialPushDelay(), weather, speed, tier, ground, marine, cfg.scale)
	}
	floorS, capS := s.paceFloorCapFor(tier, marine, ground)
	factor := s.paceFactorFor(tier)
	var sum float64
	for _, sample := range officialPushDelayEligible {
		sum += paceStandardClamp(sample*weather*speed*factor*ground, floorS, capS, cfg.scale)
	}
	return sum / float64(len(officialPushDelayEligible))
}

// groundbaitPaceFactor 打窝压缩：满窝把底层杆延迟压向 pace_groundbait_floor
// （默认 3 秒）；死窝（overdoseP→0）不加速；路亚族豁免与死窝闸门同口径。
func (s *Server) groundbaitPaceFactor(ctx context.Context, uid int64, loc string, x, z float64,
	hasPos bool, spot uint64, rigType int64) float64 {
	if !s.groundbaitEnabled() || uid == 0 || loc == "" {
		return 1
	}
	if s.tunableF("groundbait_overdose_lure_exempt", 1) == 1 && rigTypeLureFamily[rigType] {
		return 1
	}
	st := s.groundbaitSpotAt(ctx, uid, loc, x, z, hasPos, spot)
	if st.units <= 0 {
		return 1
	}
	S := s.groundbaitStrength(st.units)
	P := s.groundbaitOverdoseP(st.units)
	gain := s.tunableF("pace_groundbait_gain", 0.62)
	return groundbaitClamp(1-gain*S*P, s.tunableF("pace_groundbait_min_factor", 0.35), 1)
}

// paceRosterDietFor 名册食性聚合（30 秒进程内缓存，按 statLoc 独立过期，
// 对齐 tunables 热更节奏）。只在 pace 开启且挂饵判分时调用。
func (s *Server) paceRosterDietFor(ctx context.Context, c fishRollContext, statLoc string, roster []*store.FishRosterRow) *paceRosterDiet {
	s.paceDietMu.Lock()
	if hit, ok := s.paceDietByLoc[statLoc]; ok && time.Since(hit.at) < paceDietTTL {
		s.paceDietMu.Unlock()
		return hit.diet
	}
	s.paceDietMu.Unlock()
	out := &paceRosterDiet{dietMax: map[string]float64{}}
	for _, r := range roster {
		p := s.cachedFishEcology(ctx, c, r.FishKey)
		if !p.Enabled {
			continue
		}
		diet := fishEcologyJSONMap(p.DietJSON)
		if v := diet["lure"]; v > out.lureMax {
			out.lureMax = v
		}
		for k, v := range diet {
			if v > out.dietMax[k] {
				out.dietMax[k] = v
			}
		}
	}
	s.paceDietMu.Lock()
	if s.paceDietByLoc == nil {
		s.paceDietByLoc = map[string]paceDietCacheEntry{}
	}
	s.paceDietByLoc[statLoc] = paceDietCacheEntry{diet: out, at: time.Now()}
	s.paceDietMu.Unlock()
	return out
}

// invalidatePaceDietCache 清空名册食性聚合缓存。fish_ecology 全量重载
// （reload_tunables）与后台覆盖失效（invalidateFishEcologyCache）时调用，
// 否则旧聚合会活过 TTL。
func (s *Server) invalidatePaceDietCache() {
	s.paceDietMu.Lock()
	s.paceDietByLoc = nil
	s.paceDietMu.Unlock()
}

// baitPaceScore 一组饵对本水域名册的「对口分」0~1：
// 证据票（companion tier / 本图实证 / rf4 占比）与生态食性票取最大，多饵取最好。
// 天然饵与拟饵共用同一套证据源（审查 P2-3）；两者无任何证据时都给 0.30 基线
// （中性偏慢，不判死）。名册读不到、饵定义读不到、没挂饵：返回中性 0.5，
// 不因数据缺失改变节奏。
func (s *Server) baitPaceScore(ctx context.Context, c fishRollContext, loc string, baits []string, rigType int64) float64 {
	if s.DB == nil || loc == "" || len(baits) == 0 {
		return 0.5
	}
	statLoc := s.cachedResolveStatLoc(ctx, c, loc)
	if statLoc == "" {
		return 0.5
	}
	roster, err := s.cachedLocRoster(ctx, c, statLoc)
	if err != nil || len(roster) == 0 {
		return 0.5
	}
	diet := s.paceRosterDietFor(ctx, c, statLoc, roster)
	best := 0.0
	identified := false
	for _, id := range baits {
		if id == "" {
			continue
		}
		b, ok := s.cachedBaitDefinition(ctx, c, id)
		if !ok {
			continue
		}
		identified = true
		switch b.category {
		case 17:
			// 拟饵：三类证据票（companion/本图/rf4）+ 无证据基线，
			// 再与生态食性票取最大——lure 权重 2 左右即顶级掠食
			//（狗鱼 2.07），/2 归一到 0~1。biteGateDietKeys 对拟饵返回
			// nil，paceNaturalScore 的天然饵食性票不会误入。
			sc := s.paceNaturalScore(ctx, c, statLoc, roster, b, diet)
			if lureDiet := math.Min(1, diet.lureMax/2); lureDiet > sc {
				sc = lureDiet
			}
			if sc > best {
				best = sc
			}
		case 27:
			if sc := s.paceNaturalScore(ctx, c, statLoc, roster, b, diet); sc > best {
				best = sc
			}
		}
	}
	if !identified {
		return 0.5
	}
	return best
}

// paceScoreForGroups 多钩钓组的整杆分：每钩按各自挂饵独立评分，取最好一钩。
// 与 rollFishForRig 的「任意已挂饵钩位都可能出鱼」同语义——哪口饵先出鱼，
// 哪口饵决定节奏（产品决策，见 spec/能力契约「多钩」条）。
func (s *Server) paceScoreForGroups(ctx context.Context, c fishRollContext, loc string, groups [][]string, rigType int64) float64 {
	if len(groups) == 0 {
		return 0.5
	}
	best := 0.0
	for _, baits := range groups {
		if len(baits) == 0 {
			continue
		}
		if sc := s.baitPaceScore(ctx, c, loc, baits, rigType); sc > best {
			best = sc
		}
	}
	if best <= 0 {
		return 0.5
	}
	return best
}

// rodPaceScore 生产路径的整杆分：按 baitedHookSets 的钩位分组（与判鱼/扣饵
// 同一来源，坏饵与空数量饵不参与），每钩独立评分取最好。
func (s *Server) rodPaceScore(ctx context.Context, gear string, loc string, rigType int64) (float64, [][]string) {
	groups := s.baitedHookSets(ctx, gear)
	if len(groups) == 0 {
		return 0.5, nil
	}
	c := fishRollContext{}
	gs := make([][]string, 0, len(groups))
	for _, g := range groups {
		gs = append(gs, g.baits)
	}
	return s.paceScoreForGroups(ctx, c, loc, gs, rigType), gs
}

// paceNaturalScore 单个天然饵的分：逐名册鱼取三类证据票的最大，
// 再与生态食性票（该饵食性键在名册内的最大 diet 权重 ×0.8）取最大。
func (s *Server) paceNaturalScore(ctx context.Context, c fishRollContext, statLoc string,
	roster []*store.FishRosterRow, b ruleBait, diet *paceRosterDiet) float64 {
	tiers := s.cachedBaitTiers(ctx, c, b.id)
	local := s.cachedLocalBaitEvidence(ctx, c, statLoc, b.id, b.name)
	best := 0.0
	for _, r := range roster {
		sc := 0.0
		if t := tiers[r.FishKey]; t > 0 {
			switch t {
			case 3:
				sc = 1.0
			case 2:
				sc = 0.8
			default:
				sc = 0.6
			}
		}
		if local[r.FishKey] && sc < 0.5 {
			sc = 0.5
		}
		if share := s.rf4BaitShare(ctx, r.FishKey, b.id); share > 0 {
			switch {
			case share >= 5:
				if sc < 0.9 {
					sc = 0.9
				}
			case share >= 1:
				if sc < 0.7 {
					sc = 0.7
				}
			case share >= 0.2:
				if sc < 0.4 {
					sc = 0.4
				}
			}
		}
		if sc > best {
			best = sc
		}
	}
	if best <= 0 {
		// 无任何证据：中性偏慢（差档），但不判死——与饵料规则的
		// legalAlternative 长尾语义对齐。
		best = 0.30
	}
	for _, dk := range biteGateDietKeys(b) {
		if v := diet.dietMax[dk]; v > 0 {
			// 食性票封顶 0.6：家族对口只保证「不差」（mid 档），不配「好」——
			// 「好」必须由证据票（companion/本图/rf4）给出。名册内任何鱼吃这一口
			// 家族（哪怕证据为零）都能把无证据基线 0.30 抬到 mid。
			if sc := math.Min(0.6, v*0.8); sc > best {
				best = sc
			}
		}
	}
	return best
}

// computePaceState 计算并返回本杆的节奏因子（不写 st，调用方落库）。
// force=false 且已有缓存时直接复用（补排/脱钩重排，饵与落点未变）。
func (s *Server) computePaceState(sess *Session, st *FishState, force bool) fishPaceState {
	if !s.paceEnabled() || sess == nil {
		return neutralFishPaceState()
	}
	st.mu.Lock()
	known, gear, loc, rigType := st.PaceKnown, st.GearGuid, st.Loc, st.RigType
	lureType, lureAction := st.LureType, st.LureAction
	x, z, hasPos, spot := st.CastPosX, st.CastPosZ, st.HasCastPos, st.Spot
	if !hasPos {
		x, z, hasPos = st.PosX, st.PosZ, st.HasPos
	}
	uid := st.persist.uid
	st.mu.Unlock()
	if known && !force {
		return s.paceStateSnapshot(st)
	}
	if uid == 0 && sess != nil {
		uid = sess.Userid
	}
	ctx := sess.pushCtx()
	score, groups := s.rodPaceScore(ctx, gear, loc, rigType)
	factor, prob, lureProb, tier := s.paceTierFor(score)
	ground := 1.0
	if !rigTypeLureFamily[rigType] {
		ground = s.groundbaitPaceFactor(ctx, uid, loc, x, z, hasPos, spot, rigType)
	}
	pace := fishPaceState{Known: true, Score: score, Factor: factor, Prob: prob, LureProb: lureProb, Ground: ground, Tier: tier}
	logArgs := []any{"rod", shortGUID(gear), "各钩饵", groups, "score", roundTo(score, 3),
		"tier", paceTierName(pace.Tier), "factor", pace.Factor, "prob", pace.Prob,
		"lure_prob", pace.LureProb, "ground", roundTo(pace.Ground, 3), "rig_type", rigType}
	if rigTypeLureFamily[rigType] {
		// 路亚族按复合档位（饵分 × 当前动作）：好动作才给好档。
		pace = s.paceLureStateFor(score, lureType, lureAction, ground)
		logArgs = append(logArgs, "action", lureAction,
			"combined", roundTo(paceLureCombinedScore(score, lureType, lureAction), 3))
	}
	s.log.Info("咬钩节奏", logArgs...)
	return pace
}

// paceStateSnapshot 读取已落库的节奏因子（调用方不持 st.mu）。
// Known 必须带回 true：快照会经 storePaceState 写回 st，丢了 Known 会让
// 补排/嘬饵否决重排/脱钩重排把已算好的因子抹成中性，本次等待退回 legacy
// 延迟（审查 P2-2）。
func (s *Server) paceStateSnapshot(st *FishState) fishPaceState {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.PaceKnown {
		return neutralFishPaceState()
	}
	return fishPaceState{Known: true, Score: st.PaceScore, Factor: st.PaceFactor, Prob: st.PaceProb,
		LureProb: st.PaceLureProb, Ground: st.PaceGround, Tier: st.PaceTier}
}

// ensurePaceState 惰性补算：重启恢复/模式刚开启后 st.PaceKnown=false，
// timer 触发前补一次，防止恢复后的杆按中性因子（即无节奏控制）判鱼。
func (s *Server) ensurePaceState(sess *Session, st *FishState) {
	if !s.paceEnabled() || sess == nil {
		return
	}
	st.mu.Lock()
	known := st.PaceKnown
	st.mu.Unlock()
	if known {
		return
	}
	pace := s.computePaceState(sess, st, true)
	if !pace.Known {
		return
	}
	st.mu.Lock()
	if !st.PaceKnown {
		st.storePaceState(pace)
	}
	st.mu.Unlock()
}

// storePaceState 把节奏因子写进 FishState（调用方需持有 st.mu）。
// pace.Known=false（开关关闭）时不置位 st.PaceKnown，消费方按中性处理。
func (st *FishState) storePaceState(pace fishPaceState) {
	st.PaceScore, st.PaceFactor, st.PaceProb = pace.Score, pace.Factor, pace.Prob
	st.PaceLureProb, st.PaceGround, st.PaceTier = pace.LureProb, pace.Ground, pace.Tier
	st.PaceKnown = pace.Known
}

// paceProbFor 当前钓组族的单轮判定概率（调用方需持有 st.mu）。
func (st *FishState) paceProbFor() float64 {
	if !st.PaceKnown {
		return 1
	}
	if rigTypeLureFamily[st.RigType] {
		return st.PaceLureProb
	}
	return st.PaceProb
}

// rodPaceProb 判定用的整杆概率，受当前开关门控：运行中热关闭
// bite_pace_enabled 后立即回 1（审查 P2-1），残留的 st.PaceKnown 不再生效。
// 路亚族 fire 时动态取复合档位（饵分快照 × 当前动作）的开口率：装排后动作
// 变差的轮次按低概率空掷，随后空轮重排按差档长窗重排。
func (s *Server) rodPaceProb(st *FishState) float64 {
	if !s.paceEnabled() {
		return 1
	}
	if rigTypeLureFamily[st.RigType] {
		if !st.PaceKnown {
			return 1
		}
		_, _, _, tier := s.paceTierFor(paceLureCombinedScore(st.PaceScore, st.LureType, st.LureAction))
		return s.paceLureProbForTier(tier)
	}
	return st.paceProbFor()
}

// paceLureActionRearm 在动作复合档位升高时按新档重排咬钩 timer（只升不降）：
// 玩家做好动作的那一刻就进入对应档位的等待窗，好动作即 1.5~6s 短窗，而不是
// 等旧 timer 按差档长窗慢慢到点。档位不变或下降不重排——恶化由 fire 时的
// rodPaceProb 动态开口率空掷处理，随后空轮重排按差档长窗重排。
// 调用方不持 st.mu（14/7/14/9 handler 解锁后调用）；内部自行加锁并在锁内
// 复核全部守卫。票据机制（armBiteTimerLocked 递增 biteTicket）天然作废旧
// timer，与 emptyWeatherBiteLocked 的重排互斥安全。
func (s *Server) paceLureActionRearm(sess *Session, st *FishState) {
	if !s.paceEnabled() || sess == nil || st == nil {
		return
	}
	st.mu.Lock()
	baitScore, lureType, lureAction := st.PaceScore, st.LureType, st.LureAction
	_, _, _, newTier := s.paceTierFor(paceLureCombinedScore(baitScore, lureType, lureAction))
	if !st.PaceKnown || newTier <= st.PaceTier ||
		!st.CastActive || !st.InWater || st.NeedBite || st.Hooked || st.Resolved ||
		st.Pending != nil || st.PushSent || st.pushing != nil {
		st.mu.Unlock()
		return
	}
	if !st.PaceRearmAt.IsZero() && time.Since(st.PaceRearmAt) < paceLureRearmDebounce {
		st.mu.Unlock()
		return
	}
	gear, rigType := st.GearGuid, st.RigType
	ground := st.PaceGround
	st.PaceRearmAt = time.Now()
	st.mu.Unlock()
	bonus := s.fishingBonus(sess.pushCtx(), sess.Userid, rigType, gear)
	pace := s.paceLureStateFor(baitScore, lureType, lureAction, ground)
	st.mu.Lock()
	// 复核：等待期间鱼可能已 Pending/PushSent（timer 到点、客户端确认），
	// 动作也可能又变了（只升不降）。
	if !st.PaceKnown || !st.CastActive || st.Hooked || st.Resolved ||
		st.Pending != nil || st.PushSent || st.pushing != nil {
		st.mu.Unlock()
		return
	}
	if _, _, _, tierNow := s.paceTierFor(paceLureCombinedScore(st.PaceScore, st.LureType, st.LureAction)); tierNow <= st.PaceTier {
		st.mu.Unlock()
		return
	}
	st.storePaceState(pace)
	now := time.Now()
	delay := s.biteDelayInWeather(st, sess.weatherAt(st.Loc, now), now, bonus)
	delay *= s.hookBiteDelayFactorForRig(sess.pushCtx(), sess.Userid, gear)
	s.armBiteTimerLocked(sess.Userid, st, now.Add(time.Duration(delay*float64(time.Second))))
	st.mu.Unlock()
	s.log.Info("路亚动作改善，重排咬钩", "rod", shortGUID(gear), "rig_type", rigType,
		"action", lureAction, "tier", paceTierName(newTier))
}
