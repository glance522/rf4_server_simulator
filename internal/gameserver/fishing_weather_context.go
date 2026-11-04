package gameserver

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"
)

type fishingWeather struct {
	known           bool
	airTemp         float64
	previousAirTemp float64
	wind            float64 // 米/秒。
	fog             float64
	kind            int // 按即时云量、降雨和雷暴强度分类。
	waterCategory   int
}

type fishingWeatherHour struct {
	at            int64
	airTemp       float64
	wind          float64
	fog           float64
	cloud         float64
	rain          float64
	thunder       float64
	waterCategory int
}

type fishingWeatherPlan struct {
	hours     []fishingWeatherHour
	forecasts map[int64]int
}

// rememberWeather 保留本次下发地图的统一天气，判鱼沿用同一小时序列。
func (sess *Session) rememberWeather(loc string, nowUnix int64) {
	if sess == nil {
		return
	}
	maps, err := loadMapDefs()
	if err != nil {
		return
	}
	var root irNode
	if err := json.Unmarshal(maps[loc], &root); err != nil {
		return
	}
	if err := materializeIR(&root); err != nil {
		return
	}
	reanchorWeather(&root, nowUnix)
	plan := fishingWeatherPlanFromIR(&root)
	sess.weatherMu.Lock()
	if sess.weatherPlans == nil {
		sess.weatherPlans = make(map[string]fishingWeatherPlan)
	}
	sess.weatherPlans[loc] = plan
	sess.weatherMu.Unlock()
}

func fishingWeatherPlanFromIR(root *irNode) fishingWeatherPlan {
	plan := fishingWeatherPlan{forecasts: make(map[int64]int)}
	irWalkObj(root, func(n *irNode) {
		switch n.Class {
		case forecastClass:
			day, dayOK := irFieldInt(n, 0)
			kind, kindOK := irFieldInt(n, 1)
			if dayOK && kindOK && kind >= 0 && kind <= 5 {
				plan.forecasts[day] = int(kind)
			}
		case hourPlanClass:
			day, dayOK := irFieldInt(n, 0)
			hour, hourOK := irFieldInt(n, 1)
			temp, tempOK := fishingWeatherFloat(n, 2)
			cloud, cloudOK := fishingWeatherFloat(n, 3)
			rain, rainOK := fishingWeatherFloat(n, 4)
			thunder, thunderOK := fishingWeatherFloat(n, 5)
			fog, fogOK := fishingWeatherFloat(n, 8)
			wind, windOK := fishingWeatherFloat(n, 6)
			water, waterOK := irFieldInt(n, 9)
			if !dayOK || !hourOK || hour < 0 || hour >= 24 || !tempOK || !cloudOK || !rainOK || !thunderOK || !fogOK || !windOK || wind < 0 {
				return
			}
			if !waterOK {
				water = 0
			}
			plan.hours = append(plan.hours, fishingWeatherHour{
				at: day*24 + hour, airTemp: temp, wind: wind / 3.6,
				fog: math.Max(0, math.Min(1, fog)), waterCategory: int(water),
				cloud: cloud, rain: rain, thunder: thunder,
			})
		}
	})
	sort.Slice(plan.hours, func(i, j int) bool { return plan.hours[i].at < plan.hours[j].at })
	return plan
}

func fishingWeatherFloat(n *irNode, idx int) (float64, bool) {
	bits, ok := irFieldInt(n, idx)
	if !ok || bits < 0 || bits > math.MaxUint32 {
		return 0, false
	}
	v := float64(math.Float32frombits(uint32(bits)))
	return v, !math.IsNaN(v) && !math.IsInf(v, 0)
}

func (sess *Session) weatherAt(loc string, now time.Time) fishingWeather {
	if sess == nil {
		return fishingWeather{kind: -1}
	}
	loc = strings.TrimSpace(loc)
	sess.weatherMu.RLock()
	plan, ok := sess.weatherPlans[loc]
	sess.weatherMu.RUnlock()
	if !ok && sess.Srv != nil && sess.Srv.DB != nil {
		// 钓鱼上报场景名，沿用公共地点解析；未知场景不回退出生地图。
		if level := sess.Srv.DB.ResolveLevelKey(sess.Srv.dbCtx(), loc); level != "" {
			sess.weatherMu.RLock()
			plan, ok = sess.weatherPlans[level]
			sess.weatherMu.RUnlock()
		}
	}
	if !ok {
		return fishingWeather{kind: -1}
	}
	at := float64(now.Unix()-worldDayEpochUnix)/realSecPerGameHour + float64(now.Nanosecond())/float64(time.Second)/realSecPerGameHour
	current, ok := plan.at(at)
	if !ok {
		return fishingWeather{kind: -1}
	}
	weather := fishingWeather{
		known: true, airTemp: current.airTemp, previousAirTemp: current.airTemp,
		wind: current.wind, fog: current.fog, kind: current.kind(), waterCategory: current.waterCategory,
	}
	if previous, ok := plan.at(at - 2); ok {
		weather.previousAirTemp = previous.airTemp
	}
	return weather
}

func (plan fishingWeatherPlan) at(at float64) (fishingWeatherHour, bool) {
	idx := sort.Search(len(plan.hours), func(i int) bool { return float64(plan.hours[i].at) > at }) - 1
	if idx < 0 || at >= float64(plan.hours[idx].at+1) {
		return fishingWeatherHour{}, false
	}
	current := plan.hours[idx]
	if idx+1 < len(plan.hours) && plan.hours[idx+1].at == current.at+1 {
		next := plan.hours[idx+1]
		fraction := at - float64(current.at)
		current.airTemp += (next.airTemp - current.airTemp) * fraction
		current.wind += (next.wind - current.wind) * fraction
		current.fog += (next.fog - current.fog) * fraction
		current.cloud += (next.cloud - current.cloud) * fraction
		current.rain += (next.rain - current.rain) * fraction
		current.thunder += (next.thunder - current.thunder) * fraction
	}
	return current, true
}

// 字段来自客户端 WeatherOrchestrator：f4 降雨、f5 雷暴、f7 风向、f8 雾。
func (hour fishingWeatherHour) kind() int {
	if hour.thunder > .01 {
		return fishingWeatherThunderstorm
	}
	if hour.rain > .5 {
		return fishingWeatherHeavyRain
	}
	if hour.rain > .01 {
		return fishingWeatherRain
	}
	if hour.cloud > .3 {
		return fishingWeatherCloudy
	}
	if hour.cloud > .1 {
		return fishingWeatherFewClouds
	}
	return fishingWeatherClear
}
