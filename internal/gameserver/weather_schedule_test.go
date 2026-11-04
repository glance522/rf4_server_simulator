package gameserver

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func scheduledMapWeather(t *testing.T, loc string, at int64) irNode {
	t.Helper()
	maps, err := loadMapDefs()
	if err != nil {
		t.Fatal(err)
	}
	var root irNode
	if err := json.Unmarshal(maps[loc], &root); err != nil {
		t.Fatal(err)
	}
	if err := materializeIR(&root); err != nil {
		t.Fatal(err)
	}
	reanchorWeather(&root, at)
	return root
}

func TestWeatherScheduleEveryMapsForecastActuallyOccurs(t *testing.T) {
	maps, err := loadMapDefs()
	if err != nil {
		t.Fatal(err)
	}
	if len(mapWeatherProfiles()) != len(maps) {
		t.Fatal("some maps have no weather profile")
	}
	seen := map[int]bool{}
	for loc := range maps {
		t.Run(loc, func(t *testing.T) {
			now := weatherTimeAt(100*24 + 23)
			root := scheduledMapWeather(t, loc, now.Unix())
			plan := fishingWeatherPlanFromIR(&root)
			if len(plan.forecasts) != 8 || len(plan.hours) != 193 {
				t.Fatalf("window = %d forecasts, %d hours", len(plan.forecasts), len(plan.hours))
			}
			irWalkObj(&root, func(n *irNode) {
				if n.Class != forecastClass {
					return
				}
				day, _ := irFieldInt(n, 0)
				kind, _ := irFieldInt(n, 1)
				seen[int(kind)] = true
				maxTemp, maxWind, occurred := math.Inf(-1), 0.0, false
				for hour := int64(0); hour < 24; hour++ {
					w, ok := plan.at(float64(day*24 + hour))
					if !ok {
						t.Fatalf("missing day %d hour %d", day, hour)
					}
					occurred = occurred || w.kind() == int(kind)
					maxTemp, maxWind = math.Max(maxTemp, w.airTemp), math.Max(maxWind, w.wind*3.6)
					if w.rain > 0 && w.fog != 0 && loc != "level_011_ladoga_02" {
						t.Fatal("rain was reused as fog")
					}
				}
				if !occurred {
					t.Fatalf("day %d forecast %d never happened", day, kind)
				}
				temp, _ := fishingWeatherFloat(n, 2)
				wind, _ := fishingWeatherFloat(n, 3)
				if math.Abs(temp-maxTemp) > 1e-5 || math.Abs(wind-maxWind) > 1e-5 {
					t.Fatalf("forecast temp/wind %g/%g differs from actual %g/%g", temp, wind, maxTemp, maxWind)
				}
			})
			// 原预报发布六天后，预报和小时计划仍须逐值一致。
			later := scheduledMapWeather(t, loc, now.Add(6*time.Hour).Unix())
			laterPlan := fishingWeatherPlanFromIR(&later)
			for _, w := range plan.hours {
				if other, ok := laterPlan.at(float64(w.at)); ok && other != w {
					t.Fatalf("future weather changed at hour %d", w.at)
				}
			}
			oldForecasts := map[int64][]byte{}
			irWalkObj(&root, func(n *irNode) {
				if n.Class == forecastClass {
					d, _ := irFieldInt(n, 0)
					oldForecasts[d], _ = json.Marshal(n)
				}
			})
			irWalkObj(&later, func(n *irNode) {
				if n.Class == forecastClass {
					d, _ := irFieldInt(n, 0)
					if old, ok := oldForecasts[d]; ok {
						current, _ := json.Marshal(n)
						if !bytes.Equal(old, current) {
							t.Fatalf("published forecast changed for day %d", d)
						}
					}
				}
			})
		})
	}
	if len(seen) != 6 {
		t.Fatalf("forecast coverage = %v", seen)
	}
}

func TestWeatherScheduleMapClimateDifferencesAndBitePreferences(t *testing.T) {
	profiles := mapWeatherProfiles()
	mean := func(loc string) (temp, wind float64) {
		p := profiles[loc]
		for d := int64(0); d < 8; d++ {
			for h := int64(0); h < 24; h++ {
				n := p.hour(d, h)
				v, _ := fishingWeatherFloat(&n, 2)
				temp += v
				v, _ = fishingWeatherFloat(&n, 6)
				wind += v
			}
		}
		return temp / 192, wind / 192
	}
	cold, seaWind := mean("level_012_north_sea")
	warm, inlandWind := mean("level_009_ahtuba")
	if warm-cold < 15 || seaWind <= inlandWind {
		t.Fatalf("map climates lost: cold=%g warm=%g wind=%g/%g", cold, warm, seaWind, inlandWind)
	}
	now := weatherTimeAt(100*24 + 12)
	sess := &Session{}
	sess.rememberWeather("level_012_north_sea", now.Unix())
	sess.rememberWeather("level_009_ahtuba", now.Unix())
	a, b := sess.weatherAt("level_012_north_sea", now), sess.weatherAt("level_009_ahtuba", now)
	if fishWeatherActivity("c.carp", a, 12, 100) >= fishWeatherActivity("c.carp", b, 12, 100) {
		t.Fatal("warm-water fish ignored actual map temperatures")
	}
	if fishWeatherActivity("riv_rb_trout", a, 12, 100) <= fishWeatherActivity("riv_rb_trout", b, 12, 100) {
		t.Fatal("cold-water fish ignored actual map temperatures")
	}
}

func TestWeatherScheduleMapAndSpawnWireMatchFishingPlan(t *testing.T) {
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "weather-test"}}, logging.Discard(), nil)
	maps, _ := loadMapDefs()
	now := weatherTimeAt(100*24 + 23)
	for loc := range maps {
		body, err := srv.buildMapDobBody(loc, now.Unix())
		if err != nil {
			t.Fatal(err)
		}
		mapValue, end, err := proto.DecodeValue(body, 0, srv.Table())
		if err != nil || end != len(body) {
			t.Fatalf("map %s decode: %v, %d/%d", loc, err, end, len(body))
		}
		spawn, err := srv.buildSpawnLocatorBody(loc, "", now.Unix())
		if err != nil {
			t.Fatal(err)
		}
		spawnValue, _, err := proto.DecodeValue(spawn, 0, srv.Table())
		if err != nil {
			t.Fatal(err)
		}
		spawnMap, err := proto.EncodeValue(spawnValue.Field(0).Val, srv.Table())
		if err != nil || !bytes.Equal(spawnMap, body) {
			t.Fatalf("spawn/map weather differs for %s: %v", loc, err)
		}
		// 对照实际上线的小时数据，而不是只验证生成器自己的中间结构。
		sess := &Session{}
		sess.rememberWeather(loc, now.Unix())
		var walk func(*proto.Value)
		count := 0
		walk = func(v *proto.Value) {
			if v == nil {
				return
			}
			if v.Class == hourPlanClass {
				d, h := int64(v.Fields[0].U64()), int64(v.Fields[1].U64())
				if d == 100 && h == 23 {
					w := sess.weatherAt(loc, now)
					if !w.known || w.airTemp != float64(math.Float32frombits(uint32(v.Fields[2].U64()))) || w.wind != float64(math.Float32frombits(uint32(v.Fields[6].U64())))/3.6 {
						t.Fatalf("wire/fishing mismatch %s: %+v", loc, w)
					}
					count++
				}
			}
			for _, f := range v.Fields {
				walk(f.Val)
			}
			for _, item := range v.Items {
				walk(item)
			}
		}
		walk(mapValue)
		if count != 1 {
			t.Fatalf("current hour count %s: %d", loc, count)
		}
	}
}
