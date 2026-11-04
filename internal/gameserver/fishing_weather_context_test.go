package gameserver

import (
	"encoding/json"
	"math"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"rf4server/internal/store"
)

func weatherIRNode(class string, values ...uint64) irNode {
	n := irNode{Class: class, Fields: make([]irField, len(values))}
	for i, value := range values {
		n.Fields[i] = irField{F: "u32", V: json.RawMessage(strconv.FormatUint(value, 10))}
	}
	return n
}

func weatherIRHour(day, hour uint64, temp, wind, fog float32) irNode {
	return weatherIRNode(hourPlanClass, day, hour,
		uint64(math.Float32bits(temp)), 0, 0, 0,
		uint64(math.Float32bits(wind)), 8, uint64(math.Float32bits(fog)), 2)
}

func weatherTimeAt(hour float64) time.Time {
	return time.Unix(worldDayEpochUnix, 0).Add(time.Duration(hour * realSecPerGameHour * float64(time.Second)))
}

func TestFishingWeatherReadsActualRainInsteadOfForecastOrWindDirection(t *testing.T) {
	root := irNode{Items: []irNode{
		weatherIRNode(forecastClass, 4, 3),
		weatherIRHour(4, 22, -5, 18, 2),
	}}
	setWeatherFloat(&root.Items[1], 4, .4)
	plan := fishingWeatherPlanFromIR(&root)
	sess := &Session{weatherPlans: map[string]fishingWeatherPlan{"lake": plan}}
	w := sess.weatherAt("lake", weatherTimeAt(4*24+22))
	if !w.known || w.airTemp != -5 || w.previousAirTemp != -5 || w.wind != 5 || w.fog != 1 || w.waterCategory != 2 {
		t.Fatalf("decoded weather = %+v", w)
	}
	if w.kind != fishingWeatherRain {
		t.Fatalf("actual rain kind = %d; wind direction must not be reused", w.kind)
	}
	setWeatherFloat(&root.Items[1], 4, 0)
	sess.weatherPlans["lake"] = fishingWeatherPlanFromIR(&root)
	if got := sess.weatherAt("lake", weatherTimeAt(4*24+22)); got.kind != fishingWeatherClear || got.fog != 1 {
		t.Fatalf("daily rain forecast or fog became actual rain: %+v", got)
	}
	root.Items[1].Fields[2].V = json.RawMessage(strconv.FormatUint(uint64(math.Float32bits(float32(math.NaN()))), 10))
	if plan := fishingWeatherPlanFromIR(&root); len(plan.hours) != 0 {
		t.Fatal("invalid temperature should not create known weather")
	}
}

func TestFishingWeatherInterpolatesAcrossMidnightAndKeepsTrend(t *testing.T) {
	root := irNode{Items: []irNode{
		weatherIRHour(5, 1, 40, 36, 1),
		weatherIRHour(4, 23, 20, 0, 0),
		weatherIRHour(4, 22, 10, 0, 0),
		weatherIRHour(5, 0, 30, 0, 0),
		weatherIRNode(forecastClass, 4, 3),
		weatherIRNode(forecastClass, 5, 0),
	}}
	setWeatherFloat(&root.Items[1], 4, .4)
	sess := &Session{weatherPlans: map[string]fishingWeatherPlan{"lake": fishingWeatherPlanFromIR(&root)}}
	beforeMidnight := sess.weatherAt("lake", weatherTimeAt(4*24+23.5))
	if !beforeMidnight.known || beforeMidnight.airTemp != 25 || beforeMidnight.kind != fishingWeatherRain {
		t.Fatalf("before midnight = %+v", beforeMidnight)
	}
	afterMidnight := sess.weatherAt("lake", weatherTimeAt(5*24+.5))
	if !afterMidnight.known || afterMidnight.airTemp != 35 || afterMidnight.previousAirTemp != 15 || afterMidnight.wind != 5 || afterMidnight.fog != .5 || afterMidnight.kind != fishingWeatherClear {
		t.Fatalf("after midnight = %+v", afterMidnight)
	}
}

func TestFishingWeatherMissingExpiredAndGappedPlansStayUnknown(t *testing.T) {
	plan := fishingWeatherPlan{hours: []fishingWeatherHour{{at: 10, airTemp: 11}, {at: 12, airTemp: 13}}}
	sess := &Session{weatherPlans: map[string]fishingWeatherPlan{"lake": plan}}
	for _, at := range []float64{9.9, 11, 11.9, 13, 35} {
		if w := sess.weatherAt("lake", weatherTimeAt(at)); w.known {
			t.Fatalf("weather at %g unexpectedly known: %+v", at, w)
		}
	}
	for _, at := range []float64{10, 10.9, 12, 12.9} {
		if w := sess.weatherAt("lake", weatherTimeAt(at)); !w.known || w.kind != fishingWeatherClear {
			t.Fatalf("weather at %g = %+v", at, w)
		}
	}
	if w := sess.weatherAt("missing", weatherTimeAt(10)); w.known || w.kind != fishingWeatherUnknown {
		t.Fatalf("missing map = %+v", w)
	}
	var absent *Session
	if w := absent.weatherAt("lake", weatherTimeAt(10)); w.known {
		t.Fatalf("missing session = %+v", w)
	}
}

func TestFishingWeatherSharedAcrossSessionsRefreshAndNextDay(t *testing.T) {
	firstTime := weatherTimeAt(20*24 + 8)
	secondTime := firstTime.Add(2 * realSecPerGameHour * time.Second)
	first, second := &Session{}, &Session{}
	first.rememberWeather(spawnLevelKey, firstTime.Unix())
	second.rememberWeather(spawnLevelKey, secondTime.Unix())
	initial := first.weatherAt(spawnLevelKey, firstTime)
	laterInitial := second.weatherAt(spawnLevelKey, secondTime)
	if !initial.known || !laterInitial.known {
		t.Fatalf("missing weather: %+v / %+v", initial, laterInitial)
	}
	advanced := first.weatherAt(spawnLevelKey, secondTime)
	if advanced != laterInitial {
		t.Fatalf("same map/time differs by enter time: %+v / %+v", advanced, laterInitial)
	}
	expiredAt := firstTime.Add(24 * realSecPerGameHour * time.Second)
	want := first.weatherAt(spawnLevelKey, expiredAt)
	if !want.known {
		t.Fatalf("weather stopped after one game day: %+v", want)
	}
	first.rememberWeather(spawnLevelKey, expiredAt.Unix())
	if w := first.weatherAt(spawnLevelKey, expiredAt); w != want {
		t.Fatalf("refresh changed the same instant: %+v / %+v", w, want)
	}
}

func TestFishingWeatherAllMapPlansCoverTheirSentWindow(t *testing.T) {
	maps, err := loadMapDefs()
	if err != nil || len(maps) == 0 {
		t.Fatalf("map definitions: %v", err)
	}
	now := weatherTimeAt(42*24 + 23)
	for loc := range maps {
		t.Run(loc, func(t *testing.T) {
			sess := &Session{}
			sess.rememberWeather(loc, now.Unix())
			for _, elapsed := range []time.Duration{0, realSecPerGameHour * time.Second, 23 * realSecPerGameHour * time.Second, 24 * realSecPerGameHour * time.Second, 6 * 24 * realSecPerGameHour * time.Second} {
				w := sess.weatherAt(loc, now.Add(elapsed))
				if !w.known || w.kind < fishingWeatherClear || w.kind > fishingWeatherThunderstorm || w.wind < 0 || w.fog < 0 || w.fog > 1 {
					t.Fatalf("at %s: %+v", elapsed, w)
				}
			}
			if w := sess.weatherAt(loc, now.Add(8*24*realSecPerGameHour*time.Second)); w.known {
				t.Fatalf("expired plan = %+v", w)
			}
		})
	}
}

func TestFishingWeatherSceneAliasesUseTheirOwnMapPlan(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "weather-locations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	for _, level := range []string{spawnLevelKey, "level_002_kirzah_river", "level_004_torfyanoe_ozero", "level_009_ahtuba"} {
		if _, err := db.SQL.Exec(`INSERT INTO loc_alias(alias,loc,note) VALUES(?,?,?)`, level, level, "weather test"); err != nil {
			t.Fatal(err)
		}
	}
	sess := &Session{Srv: &Server{DB: db}}
	now := weatherTimeAt(25*24 + 7)
	cases := map[string]string{
		"loc1_02":    spawnLevelKey,
		"lvl2_01":    "level_002_kirzah_river",
		"lvl_004_05": "level_004_torfyanoe_ozero",
		"loc_009_01": "level_009_ahtuba",
		" lvl_004 ":  "level_004_torfyanoe_ozero",
	}
	for _, level := range cases {
		sess.rememberWeather(level, now.Unix())
	}
	for scene, level := range cases {
		want := sess.weatherAt(level, now)
		if got := sess.weatherAt(scene, now); !got.known || got != want {
			t.Errorf("weatherAt(%q) = %+v; map %q = %+v", scene, got, level, want)
		}
	}
	for _, unknown := range []string{"", "garbage", "loc999_01", "level_999_unknown"} {
		if got := sess.weatherAt(unknown, now); got.known {
			t.Errorf("unknown scene %q borrowed another map's weather: %+v", unknown, got)
		}
	}
	missingPlan := &Session{Srv: sess.Srv}
	if got := missingPlan.weatherAt("loc1_02", now); got.known {
		t.Fatalf("known alias without a sent plan became known: %+v", got)
	}
}
