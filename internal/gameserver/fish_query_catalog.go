package gameserver

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func (s *Server) fishQueryMaps(ctx context.Context, req FishQueryRequest, c fishRollContext) ([]fishQueryMap, error) {
	rows, err := s.DB.SQL.QueryContext(ctx, `SELECT alias,loc FROM loc_alias ORDER BY alias`)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for rows.Next() {
		var key, name string
		if err := rows.Scan(&key, &name); err != nil {
			rows.Close()
			return nil, err
		}
		names[key] = name
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if req.Map != "" {
		if _, ok := names[req.Map]; !ok {
			return nil, fmt.Errorf("不支持的地图")
		}
		_, supported := fishQueryRegionScenes[req.Map]
		if req.X == nil && !sonarChartLevel(req.Map) && !supported {
			return nil, fmt.Errorf("%w：%s", errFishQueryRegionMapUnsupported, names[req.Map])
		}
	}
	current, err := s.DB.GetPlayerLocation(ctx, req.UID)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(names))
	for key := range names {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var result []fishQueryMap
	for _, key := range keys {
		if req.Map != "" && req.Map != key {
			continue
		}
		statLoc := s.cachedResolveStatLoc(ctx, c, key)
		roster, err := s.cachedLocRoster(ctx, c, statLoc)
		if err != nil {
			return nil, err
		}
		eligible := false
		coarseWeights := make(map[string]float64, len(roster))
		for _, fish := range roster {
			coarseWeights[fish.FishKey] = fish.Catches
			if fish.FishKey == req.Fish {
				eligible = true
			}
		}
		if ovs, e := s.DB.FishWeightOverrides(ctx, statLoc); e != nil {
			return nil, e
		} else {
			for fish, ov := range ovs {
				coarseWeights[fish] = 0
				if ov.Enabled && ov.Weight > 0 {
					coarseWeights[fish] = ov.Weight
					if fish == req.Fish {
						eligible = true
					}
				}
			}
		}
		if len(roster) == 0 {
			pool := fishPools[key]
			if len(pool) == 0 {
				pool = defaultFishPool
			}
			for _, fish := range pool {
				if fish.species == req.Fish {
					eligible = true
					break
				}
			}
		}
		if !eligible {
			continue
		}
		loc := fishQueryMap{id: key, name: names[key], scene: key}
		if current != nil && current.LevelKey == key && current.Scene != "" {
			loc.scene = current.Scene
		}
		refs, err := s.DB.SQL.QueryContext(ctx, `SELECT spot,SUM(catches) FROM spot_fish WHERE loc=? AND fish_key=? GROUP BY spot ORDER BY SUM(catches) DESC,spot LIMIT 6`, statLoc, req.Fish)
		if err != nil {
			return nil, err
		}
		for refs.Next() {
			var r FishQueryReference
			if err := refs.Scan(&r.Coordinate, &r.Catches); err != nil {
				refs.Close()
				return nil, err
			}
			loc.references = append(loc.references, r)
		}
		if err := refs.Err(); err != nil {
			refs.Close()
			return nil, err
		}
		refs.Close()
		if req.X != nil {
			zone, _ := sonarZoneForPoint(key, *req.X, *req.Z)
			loc.points = []fishQueryPoint{{x: req.X, z: req.Z, zone: zone, kind: "manual"}}
		} else if sonarChartLevel(key) {
			grids, err := loadSonarGrids()
			if err != nil {
				return nil, err
			}
			byZone := map[int]fishQueryPoint{}
			gridKeys := make([]string, 0, len(grids))
			for id := range grids {
				gridKeys = append(gridKeys, id)
			}
			sort.Strings(gridKeys)
			for _, id := range gridKeys {
				g := grids[id]
				if g.level != key {
					continue
				}
				x, z := g.topLeftX+g.width/2, g.topLeftZ-g.length/2
				zone, ok := sonarZoneForPoint(key, x, z)
				if !ok {
					continue
				}
				if _, ok := byZone[zone]; !ok {
					byZone[zone] = fishQueryPoint{x: &x, z: &z, zone: zone, kind: "sea_grid"}
				}
			}
			priority := map[int]int{}
			for i, r := range loc.references {
				if n, e := strconv.Atoi(r.Coordinate); e == nil {
					priority[n] = 100 - i
				}
			}
			nameMap, _ := s.cachedFishNames(ctx, c)
			rule := fishBaitRules[ruleName(nameMap[req.Fish])]
			for _, zone := range rule.chartZones {
				priority[zone] += 1000
			}
			for _, p := range byZone {
				loc.points = append(loc.points, p)
			}
			sort.Slice(loc.points, func(i, j int) bool {
				if priority[loc.points[i].zone] != priority[loc.points[j].zone] {
					return priority[loc.points[i].zone] > priority[loc.points[j].zone]
				}
				return loc.points[i].zone < loc.points[j].zone
			})
			if len(loc.points) > 6 {
				loc.points = loc.points[:6]
			}
		} else {
			loc.regions, err = newFishQueryRegions(statLoc, fishQueryRegionScenes[key], c.now, s.tunableF("fish_distribution_enabled", 0) == 1, s.fishZonesFor(ctx, statLoc))
			if err != nil {
				return nil, err
			}
			if loc.regions.enabled {
				water, err := loadFishQueryWater()
				if err != nil {
					return nil, err
				}
				if water != nil {
					if err := loc.regions.filterWater(water.Maps[key]); err != nil {
						return nil, err
					}
				}
			}
			if err := loc.regions.shortlist(ctx, req.Fish, coarseWeights); err != nil {
				return nil, err
			}
			kind := "region_uniform"
			if loc.regions.enabled {
				kind = "inland_region"
			}
			loc.references = nil
			loc.points = []fishQueryPoint{{kind: kind}}
		}
		if len(loc.points) > 0 {
			result = append(result, loc)
		}
	}
	return result, nil
}

func (s *Server) fishQueryBaits(ctx context.Context, req FishQueryRequest, c fishRollContext, flags map[string]float64) ([]fishQueryBait, int, error) {
	// Shortlist by target-specific evidence before loading the other species'
	// tiers. Exact production probabilities are calculated for this bounded set.
	rows, err := s.DB.SQL.QueryContext(ctx, `SELECT d.item_id,COALESCE(NULLIF(c.name_zh,''),NULLIF(c.name,''),d.item_id),d.category
		FROM item_def d LEFT JOIN item_catalog c ON c.item_id=d.item_id
		LEFT JOIN companion_bait_fish b ON b.bait_id=d.item_id AND b.fish_key=?
		WHERE d.category IN (17,27) AND (?='' OR d.item_id=?)
		ORDER BY COALESCE(b.tier,0) DESC,COALESCE(b.evidence,0) DESC,d.item_id`, req.Fish, req.Bait, req.Bait)
	if err != nil {
		return nil, 0, err
	}
	var all []fishQueryBait
	for rows.Next() {
		var bait fishQueryBait
		if err := rows.Scan(&bait.id, &bait.name, &bait.category); err != nil {
			rows.Close()
			return nil, 0, err
		}
		bait.equivalents = 1
		if req.Bait == "" || req.Bait == bait.id {
			all = append(all, bait)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, 0, err
	}
	rows.Close()
	if req.Bait != "" && len(all) == 0 {
		return nil, 0, fmt.Errorf("鱼饵不存在")
	}
	exactGroups := flags["fish_bait_rules_enabled"] != 1 && flags["fish_ecology_enabled"] != 1
	// With strict bait rules/ecology disabled, only category (skill family) and
	// companion tiers affect this single-bait scenario. Collapse EXACT equivalents.
	groups := map[string]int{}
	perCategory := map[int64]int{}
	considered := 0
	var result []fishQueryBait
	for _, bait := range all {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		if req.Bait == "" && perCategory[bait.category] >= 24 {
			continue
		}
		considered++
		if !exactGroups {
			result = append(result, bait)
			perCategory[bait.category]++
			continue
		}
		tiers := s.cachedBaitTiers(ctx, c, bait.id)
		keys := make([]string, 0, len(tiers))
		for key := range tiers {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var signature strings.Builder
		fmt.Fprintf(&signature, "%d/%t", bait.category, tiers != nil)
		for _, key := range keys {
			fmt.Fprintf(&signature, "/%s:%d", key, tiers[key])
		}
		id := signature.String()
		if index, ok := groups[id]; ok {
			result[index].equivalents++
			continue
		}
		groups[id] = len(result)
		result = append(result, bait)
		perCategory[bait.category]++
	}
	return result, considered, nil
}
