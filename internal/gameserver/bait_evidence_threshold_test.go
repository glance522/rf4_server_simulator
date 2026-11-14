package gameserver

import (
	"fmt"
	"testing"
	"time"
)

// 食性证据分级（specs/changes/bait-evidence-threshold）：
// rf4records 占比分档（满分/中档/长尾）+ companion 兜底 + 开关回滚。
func TestBaitEvidenceThresholdGrading(t *testing.T) {
	s, db := seedBaitRuleTest(t)
	sqls := []string{
		// 三个自定义饵（颗粒类，c.roach 的 allow 与 legalAlternative 都不命中，
		// 使权重完全由证据分档决定）。
		`INSERT INTO item_def(numeric_id,item_id,category,attrs_json) VALUES
			(999811,'rule_grain_a',27,'{"fopbhlmkcpe":9}'),
			(999812,'rule_grain_b',27,'{"fopbhlmkcpe":9}'),
			(999813,'rule_grain_c',27,'{"fopbhlmkcpe":9}')`,
		`INSERT INTO item_catalog(item_id,name_zh) VALUES
			('rule_grain_a','测试颗粒甲'),('rule_grain_b','测试颗粒乙'),('rule_grain_c','测试颗粒丙')`,
		// companion 兜底：frog 无 rf4 纪录，tier 3/2 驱动分档
		`INSERT INTO companion_bait_fish(bait_id,fish_key,tier,evidence) VALUES
			('rule_worm','frog',3,500),('rule_other','frog',2,40)`,
	}
	for _, q := range sqls {
		if _, err := db.SQL.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	// rf4records 占比（count/total）：甲 150、乙 6、丙 1 → 95.6% / 3.8% / 0.6%
	seq := 0
	ins := func(bait string) {
		t.Helper()
		seq++
		if _, err := db.SQL.Exec(`INSERT INTO rf4records_catch(fish_name_en,fish_key,bait_name,bait_id,weight_g,waterbody,region,categories,trophy_class,catch_date,fetched_at) VALUES('x','c.roach','n','`+bait+`',`+fmt.Sprintf("%d", seq)+`,'','','','','',1)`); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 150; i++ {
		ins("rule_grain_a")
	}
	for i := 0; i < 6; i++ {
		ins("rule_grain_b")
	}
	ins("rule_grain_c")
	// 显式开启分级（seedBaitRuleTest 默认关闭，见其注释）
	s.tunMu.Lock()
	delete(s.tunCache, "bait_share_strong")
	s.tunMu.Unlock()
	s.loadRf4Share() // 同步预热，避免异步时序

	cache := newFishingRollCache()
	now := time.Unix(worldDayEpochUnix+1500, 0)
	weight := func(fish, bait string) float64 {
		t.Helper()
		keys, w := s.filterFishBaitSet("rules-test", []string{bait}, []string{fish}, []float64{1}, now, fishRollContext{cache: cache})
		if len(keys) == 0 {
			return 0
		}
		return w[0]
	}

	// 占比分档：甲 95.6% 满分、乙 3.8% 中档 0.6、丙 0.6% 长尾 0.31（颗粒类无合法替代）
	a := weight("c.roach", "rule_grain_a")
	b := weight("c.roach", "rule_grain_b")
	c := weight("c.roach", "rule_grain_c")
	if a < 1 {
		t.Fatalf("占比 95.6%% 应满分: %f", a)
	}
	if b < maxBaitPreference*0.34 || b > maxBaitPreference*0.36 {
		t.Fatalf("占比 3.8%% 应中档 0.35 倍率: %f (期望 ~%f)", b, maxBaitPreference*0.35)
	}
	if c < maxBaitPreference*0.17 || c > maxBaitPreference*0.19 {
		t.Fatalf("占比 0.6%% 应长尾 0.18 倍率: %f (期望 ~%f)", c, maxBaitPreference*0.18)
	}
	if !(a > b && b > c) {
		t.Fatalf("档位应递减: %f %f %f", a, b, c)
	}

	// companion 兜底：frog 无 rf4 纪录（share=-1），tier 3 满分 / tier 2 中档
	fw := weight("frog", "rule_worm")
	fo := weight("frog", "rule_other")
	if fw < 1 {
		t.Fatalf("tier3 应满分: %f", fw)
	}
	if fo < maxBaitPreference*0.34 || fo > maxBaitPreference*0.36 {
		t.Fatalf("tier2 应中档: %f", fo)
	}
	// rule_grain_a 对 frog 无任何证据：排除
	if weight("frog", "rule_grain_a") != 0 {
		t.Fatal("无证据饵应排除")
	}

	// 开关回滚：任一阈值 <=0 回旧行为（companion 有行即满分，无证据排除）
	s.tunMu.Lock()
	s.tunCache["bait_share_strong"] = 0
	s.tunMu.Unlock()
	if weight("c.roach", "rule_grain_b") != 0 || weight("frog", "rule_other") <= 0 {
		t.Fatalf("开关关闭应回旧行为: b=%f other=%f",
			weight("c.roach", "rule_grain_b"), weight("frog", "rule_other"))
	}
}
