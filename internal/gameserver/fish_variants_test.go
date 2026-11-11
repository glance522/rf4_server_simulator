package gameserver

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/store"
)

// newVariantTestServer 造一个只带 tunables 缓存的 Server（不连库、不启网络）。
// 变体族名推导只读 tunables + 档位线缓存，用不到完整服务端。
func newVariantTestServer(t *testing.T, tun map[string]float64) *Server {
	t.Helper()
	srv := NewServer(config.Game{}, logging.Discard(), nil)
	srv.tunMu.Lock()
	srv.tunCache = tun
	srv.tunAt = time.Now()
	srv.tunMu.Unlock()
	return srv
}

// 大西洋鲑的档位线：4000 入档 / 30000 奖杯 / 38000 蓝冠（与线上一致）。
func seedSalmonLines(t *testing.T) *store.DB {
	t.Helper()
	db := openItemTestDB(t)
	if _, err := db.SQL.Exec(`INSERT OR REPLACE INTO fish_grade_lines
		(fish_key, qualified_g, trophy_g, blue_trophy_g, source, checked_at)
		VALUES ('a_salmon', 4000, 30000, 38000, 'test', '2026-01-01')`); err != nil {
		t.Fatalf("写入档位线: %v", err)
	}
	return db
}

// 幼鱼两族仍按体重 + 体长卡（鱼苗模型塞进成鱼尺寸槽会让渔护缩略图空白）；
// 成鱼不按体重分期，见 TestFishModelVariantIgnoresWeightBands。
func TestPickFishModelVariantJuvenileFamilies(t *testing.T) {
	if got := pickFishModelVariant("a_salmon", 800, true, "guid-1", 0.18,
		defaultFishVariantParrMaxG, defaultFishVariantSmoltMaxG, defaultFishVariantJuvenileMaxM); got != "a_salmon_parr" {
		t.Fatalf("800g/0.18m 幼鱼应走 parr，实得 %q", got)
	}
	if got := pickFishModelVariant("a_salmon", 3000, false, "guid-2", 0.30,
		defaultFishVariantParrMaxG, defaultFishVariantSmoltMaxG, defaultFishVariantJuvenileMaxM); got != "a_salmon_smolt" {
		t.Fatalf("3000g/0.30m 应走 smolt，实得 %q", got)
	}
	// 关键回归：7.7kg / 0.95m 已经过了鱼苗尺度，必须走成鱼族。
	got := pickFishModelVariant("a_salmon", 7724, false, "guid-3", 0.95,
		defaultFishVariantParrMaxG, defaultFishVariantSmoltMaxG, defaultFishVariantJuvenileMaxM)
	if !strings.HasPrefix(got, "a_salmon_f") {
		t.Fatalf("7.7kg 成鱼体型应走成鱼族，实得 %q", got)
	}
	// 无变体族的鱼种留空。
	if got := pickFishModelVariant("perch", 500, true, "guid-4", 0.20,
		defaultFishVariantParrMaxG, defaultFishVariantSmoltMaxG, defaultFishVariantJuvenileMaxM); got != "" {
		t.Fatalf("鲈鱼不该有族名，实得 %q", got)
	}
}

// 玩家实测的问题就在这里：旧实现把「入档线 → 蓝冠线」等分给各期，于是同一个体重挡位永远
// 是同一套模型（上星、蓝冠各固定一两种）。官服是同一鱼种的多套外观随机出，各个挡位都能
// 钓到任意一套。这里钉住：同一体重、不同个体 → 雌雄六套外观都能出现。
func TestFishModelVariantIgnoresWeightBands(t *testing.T) {
	weights := []struct {
		name string
		g    int64
	}{
		{"入档以上", 12000},
		{"上星", 30000},
		{"蓝冠", 38000},
	}
	all := []string{"a_salmon_f1", "a_salmon_f2", "a_salmon_f3",
		"a_salmon_m1", "a_salmon_m2", "a_salmon_m3"}
	for _, w := range weights {
		seen := map[string]bool{}
		for i := 0; i < 400; i++ {
			guid := fmt.Sprintf("%032x", i*2654435761)
			v := pickFishModelVariant("a_salmon", w.g, fishVariantMale(guid), guid, 1.30,
				defaultFishVariantParrMaxG, defaultFishVariantSmoltMaxG, defaultFishVariantJuvenileMaxM)
			seen[v] = true
		}
		for _, want := range all {
			if !seen[want] {
				t.Fatalf("%s档（%dg）从未出过 %s，模型仍与体重绑定：%v", w.name, w.g, want, seen)
			}
		}
	}
}

// 同一尾鱼换任何体重挡位都必须还是同一套外观——证明选族只由身份决定，与体重无关。
func TestFishModelVariantSameSeedAnyWeight(t *testing.T) {
	const guid = "9f2c1d4e6a8b0c1d2e3f405162738495"
	male := fishVariantMale(guid)
	first := pickFishModelVariant("a_salmon", 12000, male, guid, 1.30,
		defaultFishVariantParrMaxG, defaultFishVariantSmoltMaxG, defaultFishVariantJuvenileMaxM)
	if first == "" {
		t.Fatal("大西洋鲑应当有族名")
	}
	for _, w := range []int64{5000, 12000, 30000, 38000, 200000} {
		if got := pickFishModelVariant("a_salmon", w, male, guid, 1.30,
			defaultFishVariantParrMaxG, defaultFishVariantSmoltMaxG, defaultFishVariantJuvenileMaxM); got != first {
			t.Fatalf("同一尾鱼在 %dg 得到 %q，12000g 时是 %q", w, got, first)
		}
	}
}

// 五套外观（大鳞麻哈鱼）、两套（大马哈鱼）也要铺满，不能只出第一套。
func TestFishModelVariantCoversMultiFamilySpecies(t *testing.T) {
	cases := map[string][]string{
		"chavycha": {"chavycha_f_1", "chavycha_f_2", "chavycha_f_3", "chavycha_f_4", "chavycha_f_5",
			"chavycha_m_1", "chavycha_m_2", "chavycha_m_3", "chavycha_m_4", "chavycha_m_5"},
		"keta":   {"keta_f_1", "keta_f_2", "keta_m_1", "keta_m_2"},
		"kijuch": {"kijuch_f_1", "kijuch_f_2", "kijuch_m_1", "kijuch_m_2"},
	}
	for species, want := range cases {
		seen := map[string]bool{}
		for i := 0; i < 400; i++ {
			guid := fmt.Sprintf("%032x", i*2654435761+7)
			v := pickFishModelVariant(species, 20000, fishVariantMale(guid), guid, 1.20,
				defaultFishVariantParrMaxG, defaultFishVariantSmoltMaxG, defaultFishVariantJuvenileMaxM)
			seen[v] = true
		}
		for _, f := range want {
			if !seen[f] {
				t.Fatalf("%s 的族 %s 从未被选中：%v", species, f, seen)
			}
		}
	}
}

func TestFishModelVariantTunableOff(t *testing.T) {
	db := seedSalmonLines(t)
	srv := newVariantTestServer(t, map[string]float64{"fish_model_variant": 0})
	srv.SetDB(db)
	if got := srv.fishModelVariantForFish("a_salmon", 20000, 1.30, 2, "abc"); got != "" {
		t.Fatalf("开关关闭时应留空，得到 %q", got)
	}
}

// 溜鱼（27.f22）、渔护/结算（25.f11）必须下发同一个族名；
// 命名鱼的名字要让位到 25.f12，不能被族名挤掉。
func TestFishModelKeySharedByEntityAndKeepnet(t *testing.T) {
	db := seedSalmonLines(t)
	srv := newVariantTestServer(t, map[string]float64{})
	srv.SetDB(db)

	f := &RolledFish{FishGUID: "0e1d2c3b4a5968778695a4b3c2d1e0f1", Species: "a_salmon",
		Grade: 2, WeightG: 20000, LengthM: 1.05}
	want := srv.fishModelVariantFor(f)
	if want == "" {
		t.Fatal("大西洋鲑应当有族名")
	}
	tree27 := srv.buildFish27(f, "", "", 24)
	if tree27.Fields[22].Str == nil || *tree27.Fields[22].Str != want {
		t.Fatalf("27.f22 = %v，want %q", tree27.Fields[22].Str, want)
	}

	entry := &store.KeepnetEntry{EchoGUID: f.FishGUID, Species: f.Species,
		WeightG: f.WeightG, LengthM: f.LengthM, Cat: f.Grade}
	srv.fillKeepnetVariant(entry)
	if entry.ModelVariant != want {
		t.Fatalf("渔护条目族名=%q，溜鱼时=%q", entry.ModelVariant, want)
	}
	tree25 := buildKeepnet25(entry, "", 0, entry.ModelVariant)
	if tree25.Fields[11].Str == nil || *tree25.Fields[11].Str != want {
		t.Fatalf("25.f11 = %v，want %q", tree25.Fields[11].Str, want)
	}
	if tree25.Fields[12].Str != nil {
		t.Fatalf("未命名鱼不应写 25.f12，得到 %q", *tree25.Fields[12].Str)
	}

	named := *entry
	named.NameZh = "鲍里斯"
	tree25 = buildKeepnet25(&named, "", 0, named.ModelVariant)
	if tree25.Fields[11].Str == nil || *tree25.Fields[11].Str != want {
		t.Fatalf("命名鱼 25.f11 应仍是族名，得到 %v", tree25.Fields[11].Str)
	}
	if tree25.Fields[12].Str == nil || *tree25.Fields[12].Str != "鲍里斯" {
		t.Fatalf("命名鱼的名字应写进 25.f12，得到 %v", tree25.Fields[12].Str)
	}

	// 没有变体族的鱼种：f11 仍然写名字，行为不变。
	plain := &store.KeepnetEntry{EchoGUID: "aa", Species: "perch", WeightG: 300, Cat: 1, NameZh: "小鲈"}
	srv.fillKeepnetVariant(plain)
	tree25 = buildKeepnet25(plain, "", 0, plain.ModelVariant)
	if plain.ModelVariant != "" {
		t.Fatalf("鲈鱼不应有族名，得到 %q", plain.ModelVariant)
	}
	if tree25.Fields[11].Str == nil || *tree25.Fields[11].Str != "小鲈" || tree25.Fields[12].Str != nil {
		t.Fatal("无变体族鱼的 25.f11 应保持写名字")
	}
}

// 关掉开关后 27.f22 回到命名鱼名字/null，不会残留族名。
func TestFishModelVariantOffKeepsOldWire(t *testing.T) {
	db := seedSalmonLines(t)
	srv := newVariantTestServer(t, map[string]float64{"fish_model_variant": 0})
	srv.SetDB(db)
	f := &RolledFish{FishGUID: "11223344556677889900112233445566", Species: "a_salmon",
		Grade: 2, WeightG: 20000, LengthM: 1.05, Name: "老鲑"}
	tree27 := srv.buildFish27(f, "", "", 24)
	if tree27.Fields[22].Str == nil || *tree27.Fields[22].Str != "老鲑" {
		t.Fatalf("关闭开关后 27.f22 应为命名鱼名字，得到 %v", tree27.Fields[22].Str)
	}
	srv.tunMu.Lock()
	srv.tunCache = map[string]float64{"fish_model_variant": 1}
	srv.tunAt = time.Now()
	srv.tunMu.Unlock()
	f.ModelVariant = ""
	if got := srv.fishModelVariantFor(f); got == "" {
		t.Fatal("重新打开开关后应当有族名")
	}
}
