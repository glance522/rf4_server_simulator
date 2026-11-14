package gameserver

import (
	"context"
	"math"
	"testing"
)

// castSkillWiringServer 提供带官方 skill_def/skill_link 的测试库；
// SetDB→attachDB 已运行 ensureCastSkillEffects 种下官方投距行。
func castSkillWiringServer(t *testing.T) *Server {
	t.Helper()
	db := openSkillCraftTestDB(t)
	srv := newSkillsCraftServer(t, db)
	if _, err := db.EnsureProfile(context.Background(), 1000001); err != nil {
		t.Fatal(err)
	}
	return srv
}

func learnCastNodes(t *testing.T, srv *Server, uid int64, levels map[int64]int64) {
	t.Helper()
	for node, level := range levels {
		if err := srv.DB.SetSkillNode(context.Background(), uid, node, level); err != nil {
			t.Fatal(err)
		}
	}
}

// gate 匹配：竿行只认当前竿型，轮行只认当前轮型；官方 +5%/级。
func TestCastSkillPctMatchesEquippedGearGate(t *testing.T) {
	t.Parallel()
	srv := castSkillWiringServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	cases := []struct {
		name              string
		learn             map[int64]int64
		rodType, reelType int64
		want              float64
	}{
		{"708枪柄竿行_竿型2命中", map[int64]int64{708: 7}, 2, 3, 35},
		// 708/702 共享等级（cat2 组 1）：学 708 投影到 702，竿型 1 也命中同一
		// 共享等级——官方共享等级本来就覆盖组内全部节点 gate。
		{"708共享等级投影_竿型1命中", map[int64]int64{708: 7}, 1, 1, 35},
		{"701纺车轮行_轮型1命中", map[int64]int64{701: 5}, 2, 1, 25},
		// 701 所在 link 组 514/701/313/1002 全部 gate 轮型 1：轮型 3 不命中。
		{"701纺车轮行_轮型3不命中", map[int64]int64{701: 5}, 2, 3, 0},
		{"709鼓轮行_轮型3命中", map[int64]int64{709: 5}, 2, 3, 25},
		// 709/717 共享等级（cat2 组 2）：学 709 投影到 717，水滴轮也命中。
		{"709共享等级投影_轮型2命中", map[int64]int64{709: 5}, 2, 2, 25},
		// 官方「只需学一次」：学 cat1 的 514，跨大类在 cat2 钓法下仍生效。
		// 514/701/313/1002 同投影到轮型1 gate，槽位取最高避免×4。
		{"514跨大类_轮型1命中一次", map[int64]int64{514: 5}, 2, 1, 25},
		// 718/721/1008/1020 同 gate 轮型4：兄弟行只激活一行。
		{"兄弟传统轮行_槽位取最高", map[int64]int64{718: 5, 721: 5}, 2, 4, 25},
		// 超过静态 max_level(7) 按 max 计。
		{"708超上限按7级计", map[int64]int64{708: 9}, 2, 3, 35},
		// 未学任何节点：0。
		{"未学为0", nil, 2, 3, 0},
	}
	for _, tc := range cases {
		learnCastNodes(t, srv, uid, tc.learn)
		effects, err := srv.effectiveFishingSkills(ctx, uid)
		if err != nil {
			t.Fatal(err)
		}
		if got := srv.learnedCastSkillPct(ctx, effects, tc.rodType, tc.reelType); math.Abs(got-tc.want) > 1e-9 {
			t.Fatalf("%s: castPct=%v want=%v", tc.name, got, tc.want)
		}
		learnCastNodes(t, srv, uid, map[int64]int64{})
		for node := range tc.learn {
			if err := srv.DB.SetSkillNode(ctx, uid, node, 0); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// 竿+轮叠加：708 满 7 级（35%）+ 709 满 5 级（25%）→ 60% → 1.60 倍。
func TestCastSkillComboRodAndReelMultiplies(t *testing.T) {
	t.Parallel()
	srv := castSkillWiringServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	learnCastNodes(t, srv, uid, map[int64]int64{708: 7, 709: 5})
	_, _, castPct := srv.castSkillStats(ctx, uid, 3, 2, 3)
	if math.Abs(castPct-60) > 1e-9 {
		t.Fatalf("708L7+709L5 castPct=%v want=60", castPct)
	}
	if got := castSpecialSkillFactor(castPct); math.Abs(got-1.60) > 1e-9 {
		t.Fatalf("special_skill=%v want=1.60", got)
	}
	if got := castSpecialSkillFactor(0); got != 1 {
		t.Fatalf("未学技能 special_skill=%v want=1", got)
	}
}

// ensure 幂等：重复执行不重复计数；被删的鱼侧行在鱼口 ensure 回插后，
// 再跑抛投 ensure 必须再次删干净。
func TestEnsureCastSkillEffectsIdempotentAndPurgesFishRows(t *testing.T) {
	t.Parallel()
	srv := castSkillWiringServer(t)
	ctx := context.Background()
	if _, err := srv.ensureCastSkillEffects(ctx); err != nil {
		t.Fatal(err)
	}
	if second, err := srv.ensureCastSkillEffects(ctx); err != nil || second != 0 {
		t.Fatalf("重复 ensure 插入 %d 行 err=%v", second, err)
	}
	var seeds int
	if err := srv.DB.SQL.QueryRow(`SELECT COUNT(*) FROM skill_cast_effect_def`).Scan(&seeds); err != nil {
		t.Fatal(err)
	}
	if seeds != len(castSkillEffectSeeds) {
		t.Fatalf("种子行数=%d want=%d", seeds, len(castSkillEffectSeeds))
	}
	// 模拟旧库状态：鱼侧行存在 → 生产顺序（鱼口 ensure → 抛投 ensure）删干净。
	if _, err := srv.ensureFishSkillEffects(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ensureCastSkillEffects(ctx); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := srv.DB.SQL.QueryRow(`SELECT COUNT(*) FROM skill_fish_effect_def
		WHERE (cat_id,node_id) IN (SELECT cat_id,node_id FROM skill_cast_effect_def)`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("纯投距节点残留 %d 条自造鱼侧行", rows)
	}
}
