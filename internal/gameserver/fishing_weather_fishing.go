package gameserver

import (
	"context"
	"math"
	"time"
)

func (s *Server) weatherFishActivity(species string, c fishRollContext) float64 {
	if s.tunableF("fish_weather_enabled", 0) != 1 {
		return 1
	}
	_, hour := worldDayAndHour(c.now.Unix())
	return fishWeatherActivity(species, c.weather, hour, c.depthCM)
}

// 等待节奏参考本水域鱼种；实际吃口仍逐鱼判定，不把低活跃归一化掉。
func (s *Server) weatherBiteDelayFactor(st *FishState, weather fishingWeather, now time.Time) float64 {
	if st == nil || !weather.known || s.tunableF("fish_weather_enabled", 0) != 1 {
		return 1
	}
	weights := map[string]float64{}
	baitLoc := st.Loc
	if s.DB != nil {
		ctx := s.dbCtx()
		loc := s.DB.ResolveStatLoc(ctx, st.Loc)
		if loc != "" {
			baitLoc = loc
			if roster, err := s.DB.LocRoster(ctx, loc); err == nil {
				for _, fish := range roster {
					weights[fish.FishKey] = fish.Catches
				}
			}
			if overrides, err := s.DB.FishWeightOverrides(ctx, loc); err == nil {
				for key, setting := range overrides {
					if setting.Enabled && setting.Weight > 0 {
						weights[key] = setting.Weight
					} else {
						weights[key] = 0
					}
				}
			}
		}
	}
	if len(weights) == 0 {
		pool := fishPools[st.Loc]
		if len(pool) == 0 {
			pool = defaultFishPool
		}
		for _, fish := range pool {
			weights[fish.species]++
		}
	}
	_, hour := worldDayAndHour(now.Unix())
	keys, values := make([]string, 0, len(weights)), make([]float64, 0, len(weights))
	for species, weight := range weights {
		keys, values = append(keys, species), append(values, weight)
	}
	if s.tunableF("fish_bait_rules_enabled", 0) == 1 {
		baits := []string{st.Bait}
		if s.DB != nil && st.GearGuid != "" {
			baits = s.currentBaits(s.dbCtx(), st.GearGuid)
		}
		keys, values = s.filterFishBaitSet(baitLoc, baits, keys, values, now, fishRollContext{rigType: st.RigType})
	}
	var total, active float64
	for i, species := range keys {
		weight := values[i]
		if weight > 0 {
			total += weight
			active += weight * fishWeatherActivity(species, weather, hour, st.DepthCM)
		}
	}
	if total <= 0 || active <= 0 {
		return 1
	}
	return 1 / math.Sqrt(active/total)
}

func (s *Server) emptyWeatherBiteLocked(sess *Session, st *FishState, now time.Time) {
	st.PushSent = false
	st.Pending = nil
	st.Resolved = true
	if s.tunableF("fish_weather_enabled", 0) != 1 || rigTypeLureFamily[st.RigType] || st.Hooked || !st.CastActive || s.weatherRigBroken(s.dbCtx(), sess.Userid, st.GearGuid) {
		return
	}
	st.Resolved = false
	delay := s.biteDelayInWeather(st, sess.weatherAt(st.Loc, now), now, st.Bonus)
	s.armBiteTimerLocked(sess.Userid, st, now.Add(time.Duration(delay*float64(time.Second))))
}

// 共同部件损坏或已知钩（含拟饵内置活性钩）全部损坏时停口；缺少资料保持原行为。
func (s *Server) weatherRigBroken(ctx context.Context, uid int64, gear string) bool {
	if s.DB == nil || gear == "" {
		return false
	}
	type hookProbe struct {
		hook       string
		baits      []string
		hostBroken bool
	}
	guids := []string{gear}
	var probes []hookProbe
	seenHooks := map[string]bool{}
	if rows, err := s.DB.ListRigComponents(ctx, gear); err == nil {
		for _, row := range rows {
			switch row.Slot {
			case gearCatReel, gearCatLine, gearCatLeader, gearCatLeaderWire:
				guids = append(guids, row.GUID)
			case catHook:
				hookRow, err := s.DB.GetItemByGUID(ctx, row.GUID)
				if err == nil && hookRow != nil && hookRow.UID == uid && !seenHooks[row.GUID] {
					probes = append(probes, hookProbe{hook: row.GUID})
					seenHooks[row.GUID] = true
				}
			case catLure:
				lure, err := s.DB.GetItemByGUID(ctx, row.GUID)
				if err != nil || lure == nil || lure.UID != uid {
					continue
				}
				hook := s.lureActiveHookGUID(ctx, row.GUID)
				if hook != "" && !seenHooks[hook] {
					probes = append(probes, hookProbe{hook: hook, baits: []string{row.GUID}, hostBroken: lure.Durability <= 0})
					seenHooks[hook] = true
				}
			}
		}
	}
	for _, g := range guids {
		if row, err := s.DB.GetItemByGUID(ctx, g); err == nil && row != nil && row.UID == uid && row.Durability <= 0 {
			return true
		}
	}
	knownHooks, brokenHooks := 0, 0
	for _, probe := range probes {
		def, durability := s.baitedHookDef(ctx, baitedHookSet{hookGUID: probe.hook, baitGUIDs: probe.baits})
		if def == nil {
			continue
		}
		knownHooks++
		if durability <= 0 || probe.hostBroken {
			brokenHooks++
		}
	}
	return knownHooks > 0 && brokenHooks == knownHooks
}
