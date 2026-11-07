package gameserver

import (
	"testing"
)

// 规则表：89 条实装、ID 不重复、Check 非空、留空的成就不进表。
func TestAchievementRegistryShape(t *testing.T) {
	seen := map[int64]bool{}
	for _, r := range achievementRules {
		if seen[r.ID] {
			t.Errorf("duplicate rule id %d", r.ID)
		}
		seen[r.ID] = true
		if r.Check == nil {
			t.Errorf("rule %d has nil Check", r.ID)
		}
	}
	if len(achievementRules) != 89 {
		t.Errorf("implemented rules = %d, want 89", len(achievementRules))
	}
	// 这些成就依赖服务端尚未实现的玩法，必须仍留在文件末尾的注释清单里。
	for _, id := range []int64{102, 104, 106, 108, 156, 157, 204, 209, 210,
		251, 265, 266, 267, 269, 270, 271, 275,
		300, 305, 311, 409, 411, 412, 413, 414, 415, 416,
		901, 902, 903, 904, 911} {
		if seen[id] {
			t.Errorf("achievement %d should stay unimplemented", id)
		}
	}
}

// 每条规则对应的 id 都必须在 achievement_def 里，否则解锁写入会失败。
func TestAchievementRuleIDsExistInDefinitions(t *testing.T) {
	env := newDLineTestEnv(t, 1000001)
	registerAchievementHandlers(env.srv.dispatch)
	got := achievementsByID(t, env.invoke(t, 3, 3, nil))
	for _, r := range achievementRules {
		if _, ok := got[r.ID]; !ok {
			t.Errorf("rule id %d missing from achievement_def", r.ID)
		}
	}
}

// 上鱼事件：钓组类型与拟饵前缀各自只点亮对应成就，不串台。
func TestAchievementRigAndBaitRules(t *testing.T) {
	const uid = int64(1000004)
	env := newDLineTestEnv(t, uid)
	registerAchievementHandlers(env.srv.dispatch)

	c := env.ctx(14, 4, nil)
	env.srv.evaluateAchievements(c, achievementEvent{
		Kind: evFishLanded, WeightG: 300, RigType: 7, Bait: "spoon_lures_1",
	})
	got := achievementsByID(t, env.invoke(t, 3, 3, nil))
	for _, id := range []int64{900, 151, 159} {
		if !got[id].unlocked || !got[id].claimed {
			t.Errorf("achievement %d should unlock: %+v", id, got[id])
		}
	}
	for _, id := range []int64{152, 153, 154, 155, 160, 200, 905, 1000, 350} {
		if got[id].unlocked {
			t.Errorf("achievement %d should stay locked: %+v", id, got[id])
		}
	}
	if len(c.afterResponse) != 1 {
		t.Errorf("one event should register 1 push callback, got %d", len(c.afterResponse))
	}
}

// 制作事件：配方前缀点亮对应自制路亚；烹饪熟练度单独判技能成就。
func TestAchievementCraftAndCookRules(t *testing.T) {
	const uid = int64(1000005)
	env := newDLineTestEnv(t, uid)
	registerAchievementHandlers(env.srv.dispatch)

	c := env.ctx(9, 1, nil)
	env.srv.evaluateAchievements(c, achievementEvent{
		Kind: evCraftOK, RecipeID: "craft_lure_novg_001", CookPoints: 40,
	})
	got := achievementsByID(t, env.invoke(t, 3, 3, nil))
	if !got[350].unlocked || !got[350].claimed {
		t.Errorf("350 should unlock on novgorod lure recipe: %+v", got[350])
	}
	if !got[420].unlocked {
		t.Errorf("420 should unlock at cook points 40: %+v", got[420])
	}
	if got[421].unlocked || got[422].unlocked || got[351].unlocked {
		t.Error("higher cook tiers and other lure series must stay locked")
	}

	c2 := env.ctx(9, 1, nil)
	env.srv.evaluateAchievements(c2, achievementEvent{
		Kind: evCraftOK, RecipeID: "craft_cook_blini", CookPoints: 100,
	})
	got = achievementsByID(t, env.invoke(t, 3, 3, nil))
	for _, id := range []int64{410, 421, 422} {
		if !got[id].unlocked || !got[id].claimed {
			t.Errorf("achievement %d should unlock: %+v", id, got[id])
		}
	}
}

// 采集、入护统计、放流三个事件各自只判定自己订阅的规则。
func TestAchievementDigKeptReleasedRules(t *testing.T) {
	const uid = int64(1000006)
	env := newDLineTestEnv(t, uid)
	registerAchievementHandlers(env.srv.dispatch)

	env.srv.evaluateAchievements(env.ctx(9, 3, nil), achievementEvent{
		Kind: evBaitDug, BaitKey: "bloodworm",
	})
	env.srv.evaluateAchievements(env.ctx(14, 5, nil), achievementEvent{
		Kind: evFishKept, DistinctBaits: 30,
	})
	env.srv.evaluateAchievements(env.ctx(14, 6, nil), achievementEvent{
		Kind: evFishReleased, Grade: superTrophyGrade,
	})
	got := achievementsByID(t, env.invoke(t, 3, 3, nil))
	for _, id := range []int64{258, 906, 908, 909, 912} {
		if !got[id].unlocked || !got[id].claimed {
			t.Errorf("achievement %d should unlock: %+v", id, got[id])
		}
	}
	for _, id := range []int64{250, 259, 261, 272, 907, 900, 905, 1000} {
		if got[id].unlocked {
			t.Errorf("achievement %d should stay locked: %+v", id, got[id])
		}
	}
}

// 912 只认蓝冠：超级稀有 == 蓝冠，非蓝冠档位放流不解锁。
func TestAchievementReleaseOnlyBlueCrown(t *testing.T) {
	const uid = int64(1000007)
	env := newDLineTestEnv(t, uid)
	registerAchievementHandlers(env.srv.dispatch)

	env.srv.evaluateAchievements(env.ctx(14, 6, nil), achievementEvent{
		Kind: evFishReleased, Grade: 3,
	})
	got := achievementsByID(t, env.invoke(t, 3, 3, nil))
	if got[912].unlocked {
		t.Errorf("grade 3 release must not unlock 912: %+v", got[912])
	}

	env.srv.evaluateAchievements(env.ctx(14, 6, nil), achievementEvent{
		Kind: evFishReleased, Grade: superTrophyGrade,
	})
	got = achievementsByID(t, env.invoke(t, 3, 3, nil))
	if !got[912].unlocked || !got[912].claimed {
		t.Errorf("blue crown release should unlock 912: %+v", got[912])
	}
}
