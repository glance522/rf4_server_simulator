package gameserver

import (
	"math"
	"path/filepath"
	"strings"
	"testing"

	"rf4server/internal/store"
)

func weatherActivitySample(kind int, temperature float64) fishingWeather {
	return fishingWeather{known: true, kind: kind, airTemp: temperature, previousAirTemp: temperature, wind: 3}
}

func TestFishWeatherUsesStaticSpeciesNames(t *testing.T) {
	db, err := store.OpenWithStatic(":memory:", filepath.Join("..", "..", "server_data", "static.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.SQL.Query(`SELECT fish_key,COALESCE(fish_zh,'') FROM loc_wiki_fish UNION SELECT fish_key,COALESCE(name_zh,'') FROM loc_fish`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	names := map[string]map[string]bool{}
	for rows.Next() {
		var key, name string
		if err := rows.Scan(&key, &name); err != nil {
			t.Fatal(err)
		}
		if names[key] == nil {
			names[key] = map[string]bool{}
		}
		names[key][strings.Trim(name, "\ufeff \t\r\n")] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		key, name string
		low       float64
	}{
		{"carp", "普通鲤鱼", 18}, {"c.carp", "金鲫", 18}, {"crucian", "银鲫", 18},
		{"g_carp", "草鱼", 18}, {"b_carp", "黑鲩", 18}, {"din_m_carp", "丁克斯镜鲤", 18}, {"kohaku", "红白锦鲤", 18},
		{"a_salmon", "大西洋鲑", 4}, {"riv_rb_trout", "虹鳟", 4}, {"k_salmon", "里海鲑鱼", 4},
		{"a_char", "北极红点鲑", 4}, {"salvel_l", "美洲红点鲑", 4}, {"lenok_o", "细鳞鲑", 4}, {"wfish", "白鲑鱼", 4},
		{"n.pike", "常见狗鱼", 10}, {"perch", "鲈鱼", 10}, {"e.pikeperch", "梭鲈鱼", 10}, {"walleye", "玻璃梭鲈", 10},
	} {
		if !names[sample.key][sample.name] {
			t.Fatalf("static species %s is not %s", sample.key, sample.name)
		}
		low, _, known := fishWeatherTemperatureBand(sample.key)
		if !known || low != sample.low {
			t.Fatalf("wrong temperature group for %s (%s)", sample.key, sample.name)
		}
	}
}

func TestFishWeatherTemperaturePreferencesAndUnknowns(t *testing.T) {
	cold := weatherActivitySample(fishingWeatherUnknown, 7)
	warm := weatherActivitySample(fishingWeatherUnknown, 24)
	if fishWeatherActivity("c.carp", warm, 12, 0) <= fishWeatherActivity("c.carp", cold, 12, 0) {
		t.Fatal("warm-water fish did not prefer suitable warmth")
	}
	if fishWeatherActivity("riv_rb_trout", cold, 12, 0) <= fishWeatherActivity("riv_rb_trout", warm, 12, 0) {
		t.Fatal("cold-water fish did not prefer suitable cool conditions")
	}
	if fishWeatherActivity("perch", cold, 12, 0) >= fishWeatherActivity("perch", weatherActivitySample(fishingWeatherUnknown, 18), 12, 0) {
		t.Fatal("predator temperature band did not affect feeding")
	}
	cold.previousAirTemp = 30
	if fishWeatherActivity("unknown", cold, 12, 0) != fishWeatherActivity("unknown", warm, 12, 0) {
		t.Fatal("unknown fish received an invented temperature preference")
	}
	if fishWeatherActivity("carp_albino", warm, 12, 0) != fishWeatherActivity("carp", warm, 12, 0) {
		t.Fatal("colour variant changed its temperature preference")
	}
	if got := fishWeatherActivity("c.carp", fishingWeather{}, 12, 0); got != 1 {
		t.Fatalf("missing weather must be neutral, got %v", got)
	}
	warm.kind = 900
	if got := fishWeatherActivity("c.carp", warm, 12, 0); got != 1 {
		t.Fatalf("unknown category must leave valid suitable conditions neutral, got %v", got)
	}
	warm.airTemp = -10
	if fishWeatherActivity("c.carp", warm, 12, 0) >= 1 {
		t.Fatal("unknown category discarded reliable temperature data")
	}
}

func TestFishWeatherRainWindFogAndDepth(t *testing.T) {
	activity := func(kind int) float64 {
		return fishWeatherActivity("c.carp", weatherActivitySample(kind, 22), 12, 0)
	}
	if !(activity(fishingWeatherClear) < activity(fishingWeatherFewClouds) && activity(fishingWeatherFewClouds) < activity(fishingWeatherCloudy) && activity(fishingWeatherCloudy) <= activity(fishingWeatherRain)) {
		t.Fatal("cloud and light-rain progression is inconsistent")
	}
	if !(activity(fishingWeatherThunderstorm) < activity(fishingWeatherHeavyRain) && activity(fishingWeatherHeavyRain) < activity(fishingWeatherRain)) {
		t.Fatal("heavy rain and thunderstorms should reduce feeding")
	}
	clear := weatherActivitySample(fishingWeatherClear, 22)
	if fishWeatherActivity("c.carp", clear, 0, 0) <= fishWeatherActivity("c.carp", clear, 12, 0) {
		t.Fatal("bright-sky effect must follow game daylight")
	}
	if fishWeatherActivity("c.carp", clear, 12, 2000) <= fishWeatherActivity("c.carp", clear, 12, 0) {
		t.Fatal("depth should soften surface-light effects")
	}
	breeze := weatherActivitySample(fishingWeatherUnknown, 22)
	calm, windy, foggy := breeze, breeze, breeze
	calm.wind, windy.wind, foggy.fog = 0, 25, 0.9
	if fishWeatherActivity("c.carp", calm, 12, 0) >= fishWeatherActivity("c.carp", breeze, 12, 0) || fishWeatherActivity("c.carp", windy, 12, 0) >= fishWeatherActivity("c.carp", breeze, 12, 0) {
		t.Fatal("light wind should differ from calm and strong wind")
	}
	if fishWeatherActivity("c.carp", foggy, 12, 0) >= fishWeatherActivity("c.carp", breeze, 12, 0) {
		t.Fatal("fog did not participate in feeding activity")
	}
	if fishWeatherActivity("c.carp", windy, 12, 2000) <= fishWeatherActivity("c.carp", windy, 12, 0) {
		t.Fatal("depth should soften surface-wind effects")
	}
}

func TestFishWeatherChangesAndSmoothTemperatureEdges(t *testing.T) {
	stable := weatherActivitySample(fishingWeatherRain, 22)
	for _, previous := range []float64{10, 34} {
		changed := stable
		changed.previousAirTemp = previous
		if fishWeatherActivity("c.carp", changed, 12, 0) >= fishWeatherActivity("c.carp", stable, 12, 0) {
			t.Fatal("abrupt warming and cooling must differ from stable weather")
		}
	}
	for _, temperature := range []float64{18, 27} {
		left := fishWeatherActivity("c.carp", weatherActivitySample(fishingWeatherRain, temperature-0.0001), 12, 0)
		right := fishWeatherActivity("c.carp", weatherActivitySample(fishingWeatherRain, temperature+0.0001), 12, 0)
		if math.Abs(left-right) > 0.0001 {
			t.Fatalf("temperature boundary at %v is abrupt: %v/%v", temperature, left, right)
		}
	}
}

func TestFishWeatherActivityBoundsAndInvalidValues(t *testing.T) {
	for _, species := range []string{"c.carp", "riv_rb_trout", "perch", "unknown"} {
		for kind := fishingWeatherUnknown; kind <= fishingWeatherThunderstorm; kind++ {
			for _, temperature := range []float64{-60, 0, 15, 25, 60, math.NaN(), math.Inf(1)} {
				for _, depth := range []int64{-100, 0, 100, 2000, 1000000000} {
					weather := weatherActivitySample(kind, temperature)
					weather.previousAirTemp, weather.wind, weather.fog = -40, 50, 1
					got := fishWeatherActivity(species, weather, 12, depth)
					if math.IsNaN(got) || math.IsInf(got, 0) || got < 0.1 || got > 1 {
						t.Fatalf("invalid activity for %s/%d/%v/%d: %v", species, kind, temperature, depth, got)
					}
				}
			}
		}
	}
	weather := weatherActivitySample(fishingWeatherRain, 22)
	weather.wind, weather.fog, weather.previousAirTemp = math.Inf(1), math.NaN(), math.Inf(-1)
	if got := fishWeatherActivity("c.carp", weather, 12, 0); got != 1 {
		t.Fatalf("invalid optional fields should be neutral, got %v", got)
	}
}
