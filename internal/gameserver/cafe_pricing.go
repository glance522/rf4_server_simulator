package gameserver

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"sync"

	"rf4server/internal/store"
)

//go:embed assets/cafe_reward_templates.json
var cafeRewardJSON []byte

type cafeRewardTemplate struct {
	Species             string `json:"species"`
	MinWeightG          int64  `json:"minWeightG"`
	Quantity            int64  `json:"quantity"`
	MinRewardCents      int64  `json:"minRewardCents"`
	MaxRewardCents      int64  `json:"maxRewardCents"`
	ObservedRewardCents int64  `json:"observedRewardCents,omitempty"`
}

var cafeRewards struct {
	sync.Once
	Version int64
	Waters  map[string][]cafeRewardTemplate
	Err     error
}

func loadCafeRewards() {
	var data struct {
		PricingVersion int64                           `json:"pricingVersion"`
		Waters         map[string][]cafeRewardTemplate `json:"waters"`
	}
	cafeRewards.Err = json.Unmarshal(cafeRewardJSON, &data)
	cafeRewards.Version, cafeRewards.Waters = data.PricingVersion, data.Waters
}

// 父鱼种按游戏本体的变种名称核对；不改变实际发鱼概率。
var cafeVariantParents = map[string]string{
	"alb_barbel": "c_barbel", "carp_albino": "carp", "carp_ghost": "carp", "carp_red": "carp",
	"g_carp_albino": "g_carp", "l_carp_albino": "l_carp", "l_carp_ghost": "l_carp",
	"m_carp_albino": "m_carp", "m_carp_ghost": "m_carp", "m_carp_red": "m_carp",
	"n_carp_albino": "n_carp", "n_carp_ghost": "n_carp", "r_carp_albino": "r_carp", "r_carp_ghost": "r_carp",
	"w_catfish_a": "w_catfish",
}

func cafeReward(r *rand.Rand, row cafeRewardTemplate) int64 {
	if row.ObservedRewardCents > 0 {
		return row.ObservedRewardCents
	}
	return row.MinRewardCents + r.Int63n(row.MaxRewardCents-row.MinRewardCents+1)
}

// cafeScaledReward 按重量比例折算整单奖励，以整数分四舍五入。
func cafeScaledReward(baseCents, baseGrams, grams int64) (int64, error) {
	if baseCents <= 0 || baseGrams <= 0 || grams <= 0 || baseCents > math.MaxInt64/grams {
		return 0, store.ErrCafeOrderInvalid
	}
	n := baseCents * grams
	q, rem := n/baseGrams, n%baseGrams
	if rem >= baseGrams-rem {
		q++
	}
	return q, nil
}

func (s *Server) calibratedCafeTemplates(ctx context.Context, level string, now int64) ([]store.CafeOrderTemplate, error) {
	cafeRewards.Do(loadCafeRewards)
	if cafeRewards.Err != nil {
		return nil, cafeRewards.Err
	}
	catalog := cafeRewards.Waters[level]
	if len(catalog) == 0 {
		return nil, nil
	}
	ordinary, large := []cafeRewardTemplate{}, []cafeRewardTemplate{}
	highest := map[string]cafeRewardTemplate{}
	for _, row := range catalog {
		v, t, b := s.DB.GradeLines(ctx, row.Species)
		if gradeFromLines(v, t, b, row.MinWeightG) >= 3 {
			large = append(large, row)
		} else {
			ordinary = append(ordinary, row)
		}
		if row.Quantity == 1 && row.MinWeightG > highest[row.Species].MinWeightG {
			highest[row.Species] = row
		}
	}
	if len(ordinary) < 9 || len(large) == 0 {
		return nil, fmt.Errorf("咖啡厅模板不足: %s", level)
	}
	// 固定分组使一个槽位到期不会改变其他槽位的条件，也避免重复单。
	groupRand := cafeRandom("cafe-groups/" + level)
	groupRand.Shuffle(len(ordinary), func(i, j int) { ordinary[i], ordinary[j] = ordinary[j], ordinary[i] })
	buckets := make([][]cafeRewardTemplate, 9)
	for i, row := range ordinary {
		buckets[i%9] = append(buckets[i%9], row)
	}
	type rareDemand struct {
		species string
		base    cafeRewardTemplate
		lo, hi  int64
	}
	var variants, extremes []rareDemand
	roster, err := s.DB.LocRoster(ctx, s.DB.ResolveStatLoc(ctx, level))
	if err != nil {
		return nil, err
	}
	for _, fish := range roster {
		parent, isVariant := cafeVariantParents[fish.FishKey]
		if !isVariant {
			parent = fish.FishKey
		}
		base, ok := highest[parent]
		if !ok {
			continue
		}
		_, t, b := s.DB.GradeLines(ctx, fish.FishKey)
		if t == nil || b == nil || *t <= 0 || *b <= *t {
			continue
		}
		lo := *b
		if isVariant {
			lo = max(*t, base.MinWeightG)
		}
		hi := *b + *b/50
		var naturalMax int64
		if err := s.DB.SQL.QueryRowContext(ctx, `SELECT COALESCE(max_g,0) FROM species_weight WHERE fish_key=?`, fish.FishKey).Scan(&naturalMax); err != nil {
			return nil, err
		}
		if naturalMax > 0 {
			hi = min(hi, naturalMax)
		}
		if hi < lo {
			continue
		}
		d := rareDemand{fish.FishKey, base, lo, hi}
		if isVariant {
			variants = append(variants, d)
		} else {
			extremes = append(extremes, d)
		}
	}
	sort.Slice(variants, func(i, j int) bool { return variants[i].species < variants[j].species })
	sort.Slice(extremes, func(i, j int) bool { return extremes[i].species < extremes[j].species })
	timeScale := s.tunableF("cafe_order_time_scale", 1)
	if math.IsNaN(timeScale) || math.IsInf(timeScale, 0) || timeScale < 0.01 || timeScale > 100 {
		timeScale = 1
	}
	out := make([]store.CafeOrderTemplate, 0, 10)
	for slot := 0; slot < 10; slot++ {
		clock := cafeRandom(fmt.Sprintf("cafe-clock/%s/%d", level, slot))
		minutes := 60 + clock.Intn(301)
		if slot == 8 {
			minutes = 360 + clock.Intn(361)
		}
		if slot == 9 {
			minutes = 720 + clock.Intn(2161)
		}
		period := int64(math.Round(float64(minutes*60) * timeScale))
		phase := clock.Int63n(period)
		cycle := int64(0)
		if now >= phase {
			cycle = (now - phase) / period
		}
		r := cafeRandom(fmt.Sprintf("cafe-demand/v%d/%s/%d/%d", cafeRewards.Version, level, slot, cycle))
		pool := large
		if slot < 8 {
			pool = buckets[slot]
		} else if slot == 9 {
			pool = buckets[8]
		}
		row := pool[r.Intn(len(pool))]
		weight, qty, reward := row.MinWeightG, row.Quantity, cafeReward(r, row)
		if slot == 9 && r.Intn(4) == 0 {
			rarePool := variants
			if len(rarePool) == 0 {
				rarePool = extremes
			}
			if len(rarePool) > 0 {
				d := rarePool[r.Intn(len(rarePool))]
				row = d.base
				row.Species = d.species
				weight = d.lo + r.Int63n(d.hi-d.lo+1)
				qty = 1
				reward, err = cafeScaledReward(cafeReward(r, d.base), d.base.MinWeightG, weight)
				if err != nil {
					return nil, err
				}
			}
		}
		v, t, b := s.DB.GradeLines(ctx, row.Species)
		out = append(out, store.CafeOrderTemplate{Species: row.Species, MinWeightG: weight, Quantity: qty, RewardCents: reward, OfferSize: gradeFromLines(v, t, b, weight), PeriodSeconds: period, PhaseSeconds: phase, PricingVersion: cafeRewards.Version})
	}
	return out, nil
}
