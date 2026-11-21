package gameserver

import (
	"context"
	"math"
	"sort"
	"time"
)

// meanBiteDelay averages the production delay AFTER applying its clamps.
// It does not sample RNG or include client wait, occupied rods or landing time.
// 路亚窗（lmin/lmax/lureCap）由调用方按 pace 复合档位传入，与生产
// biteDelayInWeather 路亚分支同源：好档保留 bite_delay_lure_max 封顶，
// 中/差档 lureCap 传 0 不封顶；pace 关闭传 1.5/6/cfg.lureMax（legacy 窗）。
func meanBiteDelay(cfg biteDelaySettings, lure bool, weather, speed, lmin, lmax, lureCap float64) float64 {
	if cfg.fixed > 0 {
		return math.Max(.05, cfg.fixed)
	}
	if !lure {
		var sum float64
		for _, sample := range officialPushDelayEligible {
			sum += standardBiteDelay(sample*weather, speed, cfg.maxDelay, cfg.scale)
		}
		if len(officialPushDelayEligible) > 0 {
			return sum / float64(len(officialPushDelayEligible))
		}
		// Same rounded uniform bands as the production fallback.
		for _, band := range pushDelayBands {
			for cent := int(math.Floor(band.lo * 100)); cent <= int(math.Ceil(band.hi*100)); cent++ {
				x := float64(cent) / 100
				width := math.Max(0, math.Min(band.hi, x+.005)-math.Max(band.lo, x-.005))
				sum += band.p * width / (band.hi - band.lo) * standardBiteDelay(x*weather, speed, cfg.maxDelay, cfg.scale)
			}
		}
		return sum
	}
	// Uniform [lmin,lmax], transformed by piecewise-linear clamps. Integration at
	// each segment midpoint is exact; averaging the raw window is not.
	points := []float64{lmin, lmax}
	k := weather * speed
	for _, x := range []float64{.8 / k, lureCap / k, .8 / (k * cfg.scale)} {
		if x > lmin && x < lmax {
			points = append(points, x)
		}
	}
	sort.Float64s(points)
	var sum float64
	for i := 1; i < len(points); i++ {
		a, b := points[i-1], points[i]
		sum += (b - a) * lureBiteDelay((a+b)/2*weather, speed, lureCap, cfg.scale)
	}
	return sum / (lmax - lmin)
}

func (s *Server) fishQueryMeanDelay(st *FishState, weather fishingWeather, at time.Time, pace fishPaceState) float64 {
	cfg := s.effectiveBiteDelaySettings(st)
	if cfg.fixed > 0 {
		return meanBiteDelay(cfg, false, 1, 1, 0, 0, 0)
	}
	if rigTypeLureFamily[st.RigType] {
		// 与生产同源：路亚等待窗按 pace 复合档位取（pace.Known 才生效），
		// 好档保留 bite_delay_lure_max 封顶，中/差档不封顶；pace 关闭回
		// legacy [1.5,6] 窗。
		lmin, lmax, capS := 1.5, 6.0, cfg.lureMax
		if pace.Known {
			lmin, lmax = s.paceLureWindowFor(pace.Tier)
			if lmax < lmin {
				lmax = lmin
			}
			if pace.Tier < 2 {
				capS = 0
			}
		}
		return meanBiteDelay(cfg, true, s.weatherBiteDelayFactor(st, weather, at), st.Bonus.BiteSpeed, lmin, lmax, capS)
	}
	return meanBiteDelay(cfg, false, s.weatherBiteDelayFactor(st, weather, at), st.Bonus.BiteSpeed, 0, 0, 0)
}

// fishQueryPaceState 查鱼助手的节奏因子：按候选饵算对口分（查询假设无窝料，
// Ground=1；打窝的实际手感以实钓为准）。c 携带请求级缓存，同一次查询内
// 名册与饵证据只查一次。
func (s *Server) fishQueryPaceState(ctx context.Context, c fishRollContext, st *FishState) fishPaceState {
	if !s.paceEnabled() || st == nil {
		return neutralFishPaceState()
	}
	score := s.baitPaceScore(ctx, c, st.Loc, []string{st.Bait}, st.RigType)
	factor, prob, lureProb, tier := s.paceTierFor(score)
	pace := fishPaceState{Known: true, Score: score, Factor: factor, Prob: prob, LureProb: lureProb, Ground: 1, Tier: tier}
	if rigTypeLureFamily[st.RigType] {
		// 查询与生产同源：路亚用复合分（饵分 × 假设动作分）。假设动作沿用
		// fishQueryMotion 的 drift/jig 口径（查询不知道玩家实际手法，st.LureAction
		// 即该假设）；路亚类型按候选饵实时加载，读不到按 ""（动作分中性）。
		lureType := ""
		if lure, ok := s.loadLureAttributes(ctx, st.Bait); ok {
			lureType = lure.Type
		}
		pace = s.paceLureStateFor(score, lureType, st.LureAction, 1)
	}
	return pace
}

func (p *FishQueryPlan) setTheoreticalRate(delay float64) {
	if !finiteSonarNumber(delay) || delay <= 0 || !finiteSonarNumber(p.Chance) {
		return
	}
	p.MeanDelaySeconds = delay
	p.TheoreticalPerMinute = 60 * clampFloat(p.Chance, 0, 1) / delay
	p.TheoreticalPerHour = 60 * p.TheoreticalPerMinute
}

func fishQueryPlanBetter(a, b FishQueryPlan) bool {
	if math.Abs(a.TheoreticalPerHour-b.TheoreticalPerHour) < 1e-12 {
		return a.WaitSeconds < b.WaitSeconds
	}
	return a.TheoreticalPerHour > b.TheoreticalPerHour
}
