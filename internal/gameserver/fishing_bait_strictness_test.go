package gameserver

// 严格吃饵档位的判鱼回归测试。档位 0 必须逐点等价于改动前：
// fishing_bait_rules_test.go 的「实钓证据可补充未列出的搭配」与
// fishing_bait_alternatives_test.go 的 5% 杂口断言在默认档下继续成立。
// 见 specs/changes/bait-strict-preference。

import (
	"context"
	"math"
	"testing"
	"time"
)

func setBaitStrictTunable(t *testing.T, s *Server, key, value string) {
	t.Helper()
	if err := s.DB.TunableSet(context.Background(), key, value, "test"); err != nil {
		t.Fatal(err)
	}
	s.tunMu.Lock()
	s.tunAt = time.Time{}
	s.tunMu.Unlock()
}

func seedBaitStrictnessTest(t *testing.T) (*Server, time.Time) {
	t.Helper()
	s, db := seedBaitRuleTest(t)
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS fish_bait_pref(fish_key TEXT NOT NULL, bait_zh TEXT NOT NULL, PRIMARY KEY(fish_key,bait_zh))`,
		`CREATE TABLE IF NOT EXISTS companion_bait_fish(bait_id TEXT, fish_key TEXT, tier INTEGER, evidence INTEGER)`,
		`INSERT OR IGNORE INTO loc_wiki_fish(loc,fish_key,fish_zh) VALUES('rules-test','n.pike','常见狗鱼')`,
		`INSERT INTO item_def(numeric_id,item_id,category,attrs_json) VALUES
			(999901,'strict_fly',27,'{"fopbhlmkcpe":7}'),
			(999902,'strict_lure',17,'{"mbemakijdeh":4}'),
			(999903,'strict_other',27,'{"fopbhlmkcpe":0}')`,
		`INSERT INTO item_catalog(item_id,name_zh) VALUES
			('strict_fly','苍蝇'),('strict_lure','X-Crank 001'),('strict_other','补充饵')`,
		// 「各类饵」在档位 >0 时按这 5 条偏好 + 拟饵族收窄。
		`INSERT OR IGNORE INTO fish_bait_pref(fish_key,bait_zh) VALUES
			('n.pike','Balsa Minnow 系列'),('n.pike','Ripper Jerk 系列'),('n.pike','Spiker 系列'),
			('n.pike','Wooden Jerk-Fat 系列'),('n.pike','X-Crank  系列')`,
		// 规则表没写「吃」但有实证的搭配：档 0 满权重，档 1 杂口，档 2 不咬。
		`INSERT OR IGNORE INTO companion_bait_fish(bait_id,fish_key,tier,evidence) VALUES('strict_other','c.roach',1,3)`,
	} {
		if _, err := db.SQL.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	// 固定时间让 fishVariation 在同一测试内可复现。
	return s, time.Unix(1763000000, 0)
}

func strictBite(t *testing.T, s *Server, now time.Time, bait, fish string) (bool, float64) {
	t.Helper()
	keys, weights := s.filterFishBaitSet("rules-test", []string{bait}, []string{fish}, []float64{1}, now)
	if len(keys) == 0 {
		return false, 0
	}
	return true, weights[0]
}

func TestBaitStrictnessZeroKeepsLegacyUnion(t *testing.T) {
	s, now := seedBaitStrictnessTest(t)
	// 档位 0：并集放行，实证命中拿满权重（1.0~1.5）；狗鱼吃虫饵也照旧。
	setBaitStrictTunable(t, s, "fish_bait_strictness", "0")
	ok, w := strictBite(t, s, now, "strict_other", "c.roach")
	if !ok || w < 1 || w > 1.5 {
		t.Fatalf("档位 0 应保持实证满权重: ok=%v w=%v", ok, w)
	}
	if ok, _ := strictBite(t, s, now, "strict_fly", "n.pike"); !ok {
		t.Fatal("档位 0 的狗鱼虫饵旧行为被改变")
	}
}

func TestBaitStrictnessOnePrefersRules(t *testing.T) {
	s, now := seedBaitStrictnessTest(t)
	setBaitStrictTunable(t, s, "fish_bait_strictness", "1")

	if ok, _ := strictBite(t, s, now, "strict_fly", "n.pike"); ok {
		t.Fatal("档位 1：狗鱼不该吃苍蝇（『各类饵』必须按偏好收窄）")
	}
	ok, w := strictBite(t, s, now, "strict_lure", "n.pike")
	if !ok || w < 1 || w > 3 {
		t.Fatalf("档位 1：对口拟饵仍应咬钩 ok=%v w=%v", ok, w)
	}
	// 手算：maxBaitPreference=1.5*1.15=1.725，默认 altBite=0.05 → 0.08625。
	ok, w = strictBite(t, s, now, "strict_other", "c.roach")
	want := (1.5 * 1.15) * 0.05
	if !ok || math.Abs(w-want) > 1e-12 {
		t.Fatalf("档位 1：规则外实证应只剩杂口，want=%v got=%v ok=%v", want, w, ok)
	}
}

func TestBaitStrictnessTwoIsAllowlist(t *testing.T) {
	s, now := seedBaitStrictnessTest(t)
	setBaitStrictTunable(t, s, "fish_bait_strictness", "2")
	if ok, _ := strictBite(t, s, now, "strict_other", "c.roach"); ok {
		t.Fatal("档位 2：规则外实证不该咬钩")
	}
	if ok, _ := strictBite(t, s, now, "strict_fly", "n.pike"); ok {
		t.Fatal("档位 2：狗鱼不该吃苍蝇")
	}
	if ok, _ := strictBite(t, s, now, "strict_lure", "n.pike"); !ok {
		t.Fatal("档位 2：对口拟饵仍应咬钩，避免整片空竿")
	}
}

func TestBaitStrictnessKnobs(t *testing.T) {
	s, now := seedBaitStrictnessTest(t)
	setBaitStrictTunable(t, s, "fish_bait_strictness", "1")

	// 杂口强度 0 = 完全关掉；0.2 = 手算 1.725*0.2 = 0.345。
	setBaitStrictTunable(t, s, "fish_bait_alt_bite", "0")
	if ok, _ := strictBite(t, s, now, "strict_other", "c.roach"); ok {
		t.Fatal("杂口强度 0 时规则外搭配应完全不咬")
	}
	setBaitStrictTunable(t, s, "fish_bait_alt_bite", "0.2")
	ok, w := strictBite(t, s, now, "strict_other", "c.roach")
	if !ok || math.Abs(w-(1.5*1.15)*0.2) > 1e-12 {
		t.Fatalf("杂口强度 0.2 应得 0.345: ok=%v w=%v", ok, w)
	}

	// bait_pref_scale 在严格通道作偏好强度：tier3 时 0=中性、1=×1.5。
	if _, err := s.DB.SQL.Exec(`INSERT OR IGNORE INTO companion_bait_fish(bait_id,fish_key,tier,evidence) VALUES('strict_lure','n.pike',3,10)`); err != nil {
		t.Fatal(err)
	}
	setBaitStrictTunable(t, s, "bait_pref_scale", "0")
	_, neutral := strictBite(t, s, now, "strict_lure", "n.pike")
	setBaitStrictTunable(t, s, "bait_pref_scale", "1")
	_, boosted := strictBite(t, s, now, "strict_lure", "n.pike")
	if neutral <= 0 || math.Abs(boosted/neutral-1.5) > 1e-12 {
		t.Fatalf("tier3 偏好系数应为 1.5 倍: neutral=%v boosted=%v", neutral, boosted)
	}
}

func TestBaitStrictnessMissingPrefsKeepsWildcard(t *testing.T) {
	s, now := seedBaitStrictnessTest(t)
	if _, err := s.DB.SQL.Exec(`DELETE FROM fish_bait_pref WHERE fish_key='n.pike'`); err != nil {
		t.Fatal(err)
	}
	setBaitStrictTunable(t, s, "fish_bait_strictness", "2")
	// 偏好数据缺失时不得把鱼整条封死，退回原「各类饵」规则。
	if ok, _ := strictBite(t, s, now, "strict_fly", "n.pike"); !ok {
		t.Fatal("偏好表无记录时应保留内置规则，而不是封鱼")
	}
}
