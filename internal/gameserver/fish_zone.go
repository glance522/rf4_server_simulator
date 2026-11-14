package gameserver

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"rf4server/internal/store"
)

// 区域固定鱼种名册（fish_zone / fish_zone_fish，管理后台可编辑）。
//
// 判鱼时在硬性鱼口门之后、饵料规则之前生效：
//   - 落点（抛竿落点优先，缺省人物位置）落在 kind=grid_rect 的 40m 网格矩形内，
//     或海图的声呐区号等于 kind=sea_zone 的区号，即命中该区域；
//   - exclusive：候选 ∩ 名单（名单内 enabled 且 weight>0），权重乘名单 weight；
//     名单外剔除。命中但交集为空 = 本轮无鱼（压制内置鱼池兜底）；
//   - boost：名单内 ×(boost×weight)，名单外 ×keep_others（海图观测语义）；
//   - 叠加语义（2026-11-14 起）：同时命中多个区域时全部生效——
//     独占名单取交集（每个硬承诺都兑现，权重连乘），增益区连乘，
//     独占先过滤、增益在幸存者上继续乘。不再按 id 择一，没有静默死区。
//
// 未配置任何区域的水域完全不受影响。与 fish_distribution 散布乘法复合：
// 名册定「这片有什么鱼」，散布定「这 4 游戏小时它们开口到什么程度」。

const fishZoneCacheTTL = 30 * time.Second

func (s *Server) fishZonesFor(ctx context.Context, loc string) []store.FishZone {
	if s.DB == nil || loc == "" {
		return nil
	}
	s.fishZoneMu.RLock()
	if s.fishZoneCache != nil && time.Since(s.fishZoneAt) < fishZoneCacheTTL {
		zones := s.fishZoneCache[loc]
		s.fishZoneMu.RUnlock()
		return zones
	}
	s.fishZoneMu.RUnlock()

	s.fishZoneMu.Lock()
	defer s.fishZoneMu.Unlock()
	if s.fishZoneCache != nil && time.Since(s.fishZoneAt) < fishZoneCacheTTL {
		return s.fishZoneCache[loc]
	}
	all, err := s.DB.ListFishZones(ctx)
	if err != nil {
		s.log.Warn("读取区域鱼种名册失败", "err", err)
		all = nil
	}
	m := map[string][]store.FishZone{}
	for _, z := range all {
		if !z.Enabled {
			continue
		}
		m[z.Loc] = append(m[z.Loc], z)
	}
	s.fishZoneCache = m
	s.fishZoneAt = time.Now()
	return m[loc]
}

// fishZoneContains 判定落点是否落在区域内。
// grid_rect 用与 fishArea 相同的 40m 世界格推导（gx=floor(x/40)，含端点）；
// grid_cells 是地图刷选的任意格子组合，落点格在集合内即命中；
// sea_zone 用判鱼上下文里已解析的声呐区号。拿不到定位证据时一律不命中。
func fishZoneContains(z store.FishZone, c fishRollContext) bool {
	switch z.Kind {
	case "grid_rect":
		if !c.hasPos || math.IsNaN(c.x) || math.IsNaN(c.z) {
			return false
		}
		gx, gz := math.Floor(c.x/40), math.Floor(c.z/40)
		return gx >= float64(z.GX0) && gx <= float64(z.GX1) &&
			gz >= float64(z.GZ0) && gz <= float64(z.GZ1)
	case "grid_cells":
		if !c.hasPos || math.IsNaN(c.x) || math.IsNaN(c.z) || len(z.Cells) == 0 {
			return false
		}
		gx, gz := int(math.Floor(c.x/40)), int(math.Floor(c.z/40))
		for _, cell := range z.Cells {
			if int(cell[0]) == gx && int(cell[1]) == gz {
				return true
			}
		}
		return false
	case "circle":
		// 圆形钓点区：中心世界坐标 + 半径（米级精度，坑/窄口等小面积）。
		if !c.hasPos || math.IsNaN(c.x) || math.IsNaN(c.z) || z.Radius <= 0 {
			return false
		}
		dx, dz := c.x-z.Cx, c.z-z.Cz
		return dx*dx+dz*dz <= z.Radius*z.Radius
	case "sea_zone":
		if c.runtime == nil || !c.runtime.zoneKnown {
			return false
		}
		if len(z.SeaZones) > 0 {
			for _, n := range z.SeaZones {
				if int(n) == c.runtime.zone {
					return true
				}
			}
			return false
		}
		return c.runtime.zone == int(z.SeaZone)
	}
	return false
}

// resolveFishZones 返回落点命中的全部启用区域。区域叠加语义（2026-11-14 起）：
// 每块画出来的区都生效，不再按 id 择一——独占名单取交集、增益连乘，
// 「画了没反应」的死区从语义上消除。
func resolveFishZones(zones []store.FishZone, c fishRollContext) []store.FishZone {
	var hits []store.FishZone
	for i := range zones {
		if fishZoneContains(zones[i], c) {
			hits = append(hits, zones[i])
		}
	}
	return hits
}

// zoneHitNote 审计日志用：命中区域的名目（名字+模式）。
func zoneHitNote(zones []store.FishZone, c fishRollContext) string {
	hits := resolveFishZones(zones, c)
	if len(hits) == 0 {
		return "无"
	}
	parts := make([]string, 0, len(hits))
	for _, z := range hits {
		name := z.Name
		if name == "" {
			name = "#" + fmt.Sprint(z.ID)
		}
		parts = append(parts, name+"（"+z.Mode+"）")
	}
	return strings.Join(parts, "+")
}

// fishZoneFactorForPoint 返回落点命中区域叠加后对指定鱼种的系数：
// 任一独占区名单外（或停用条目）=0；独占名单权重连乘；
// 增益区名单内 ×boost×weight、名单外 ×keep_others，多区连乘；
// 未命中任何区域 =1。查鱼助手的区域评分复用同一语义。
func fishZoneFactorForPoint(zones []store.FishZone, x, z float64, key string) float64 {
	hits := resolveFishZones(zones, fishRollContext{x: x, z: z, hasPos: true})
	if len(hits) == 0 {
		return 1
	}
	factor := 1.0
	for _, zone := range hits {
		w, listed := 0.0, false
		for _, f := range zone.Fish {
			if f.FishKey == key {
				listed = true
				if f.Enabled && f.Weight > 0 {
					w = f.Weight
				}
				break
			}
		}
		switch zone.Mode {
		case "exclusive":
			if !listed || w <= 0 {
				return 0
			}
			factor *= w
		default: // boost
			if listed && w > 0 {
				factor *= zone.Boost * w
			} else {
				factor *= zone.KeepOthers
			}
		}
	}
	return factor
}

// applyFishZoneRoster 把命中的全部区域（叠加语义）作用到候选权重上：
// 独占区先过滤——候选必须同时出现在每个独占区名单里（硬承诺全兑现），
// 权重为各独占区该鱼种权重的连乘；增益区随后连乘
// （在名单 ×boost×weight，不在 ×keep_others）。
// 返回 hit=false 表示没有命中区域（调用方什么都不用做）；
// 命中独占区且过滤后为空时返回空切片，调用方按「本轮无鱼」处理。
func (s *Server) applyFishZoneRoster(ctx context.Context, statLoc string, keys []string, wts []float64, c fishRollContext) ([]string, []float64, bool) {
	zones := s.fishZonesFor(ctx, statLoc)
	if len(zones) == 0 {
		return keys, wts, false
	}
	hits := resolveFishZones(zones, c)
	if len(hits) == 0 {
		return keys, wts, false
	}
	var excl, boost []store.FishZone
	for _, z := range hits {
		if z.Mode == "exclusive" {
			excl = append(excl, z)
		} else {
			boost = append(boost, z)
		}
	}
	// 独占权重表：鱼种 → 各独占区名单权重连乘；不在任一名单的鱼种缺项。
	var exclWeight map[string]float64
	if len(excl) > 0 {
		exclWeight = make(map[string]float64, 16)
		for zi, z := range excl {
			roster := zoneRosterWeights(z)
			if zi == 0 {
				for k, w := range roster {
					exclWeight[k] = w
				}
				continue
			}
			for k := range exclWeight {
				if _, ok := roster[k]; !ok {
					delete(exclWeight, k)
				}
			}
			for k, w := range roster {
				if prev, ok := exclWeight[k]; ok {
					exclWeight[k] = prev * w
				}
			}
		}
	}
	// 增益权重表预构建，避免逐鱼种重算。
	boostRosters := make([]map[string]float64, len(boost))
	for i, z := range boost {
		boostRosters[i] = zoneRosterWeights(z)
	}
	outKeys := make([]string, 0, len(keys))
	outWts := make([]float64, 0, len(wts))
	for i, key := range keys {
		if wts[i] <= 0 {
			continue
		}
		w := wts[i]
		if len(excl) > 0 {
			ew, ok := exclWeight[key]
			if !ok {
				continue
			}
			w *= ew
		}
		for bi, z := range boost {
			if bw, ok := boostRosters[bi][key]; ok {
				w *= z.Boost * bw
			} else {
				w *= z.KeepOthers
			}
		}
		outKeys = append(outKeys, key)
		outWts = append(outWts, w)
	}
	return outKeys, outWts, true
}

// zoneRosterWeights 该区名单里启用且权重大于 0 的鱼种权重表。
func zoneRosterWeights(z store.FishZone) map[string]float64 {
	m := make(map[string]float64, len(z.Fish))
	for _, f := range z.Fish {
		if f.Enabled && f.Weight > 0 {
			m[f.FishKey] = f.Weight
		}
	}
	return m
}
