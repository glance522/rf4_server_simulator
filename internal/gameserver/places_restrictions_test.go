package gameserver

import (
	"context"
	"testing"
)

// 水域装备限制（服务端镜像客户端 ebjhdhighdj 注册表）。
// 规则锚点：铜壶 level_018_moskitnoe —— carp 鲤竿全禁、match 竞技竿
// 档位≥4 禁、spin 纺车轮 size 档≥6 禁。

func TestPlaceGearRuleViolated(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	rules := restrictionsForLevel("level_018_moskitnoe")
	if len(rules) != 3 {
		t.Fatalf("铜壶规则数=%d, want 3", len(rules))
	}
	for _, tc := range []struct {
		item    string
		wantBan bool
	}{
		{"carp_10046_360H", true},    // 鲤竿：全禁
		{"match_10042_EM18", true},   // 竞技竿档4：禁
		{"match_10025_M130H", false}, // 竞技竿档3：放行
		{"spin_7016", true},          // 纺车轮 size6：禁
		{"spin_7015", false},         // 纺车轮 size5：放行
		{"spin_10158_M100HH", false}, // 纺车竿：不在限制范围
	} {
		row, err := srv.CreateItem(ctx, 1000001, tc.item, "backpack", 1)
		if err != nil {
			t.Fatalf("%s: %v", tc.item, err)
		}
		def, err := srv.itemDefForRow(ctx, row)
		if err != nil || def == nil {
			t.Fatalf("%s 定义读取失败: %v", tc.item, err)
		}
		if got := placeGearViolationReason(def, rules) != ""; got != tc.wantBan {
			t.Fatalf("%s 违规=%v, want %v", tc.item, got, tc.wantBan)
		}
	}
	_ = db
}

func TestPlaceRestrictionOnlyOnRegisteredLevel(t *testing.T) {
	if rules := restrictionsForLevel("level_000_home"); len(rules) != 0 {
		t.Fatalf("未登记地图不应有规则: %v", rules)
	}
	if rules := restrictionsForLevel(""); len(rules) != 0 {
		t.Fatalf("空 key 不应有规则: %v", rules)
	}
}

func TestPlaceRodAndRigViolation(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	// 玩家切到铜壶。
	if err := db.SetPlayerLocation(ctx, uid, "level_018_moskitnoe", "", 0); err != nil {
		t.Fatal(err)
	}

	carp, err := srv.CreateItem(ctx, uid, "carp_10046_360H", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	spinRod, err := srv.CreateItem(ctx, uid, "spin_10158_M100HH", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	bigReel, err := srv.CreateItem(ctx, uid, "spin_7016", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	okReel, err := srv.CreateItem(ctx, uid, "spin_7015", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}

	if reason := srv.placeRodViolation(ctx, uid, carp.GUID); reason == "" {
		t.Fatal("铜壶取鲤竿应被拒")
	}
	if reason := srv.placeRodViolation(ctx, uid, spinRod.GUID); reason != "" {
		t.Fatalf("铜壶取纺车竿不应被拒: %s", reason)
	}

	// 纺车竿 + 大纺车轮：装配和抛竿都应被拒。
	if res := srv.rigSetComponent(ctx, uid, spinRod.GUID, 0, bigReel.GUID, 0,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true, SplitStack: true}); !res.hasMaterials() && res.ReplacedGUID == "" {
		// 装配结果不作硬性断言（不同起始状态路径不同），下方按钓组整体校验。
		_ = res
	}
	if reason := srv.placeRigViolation(ctx, uid, spinRod.GUID); reason == "" {
		t.Fatal("铜壶抛投大纺车轮钓组应被拒")
	}

	// 换成允许的轮子后放行。先把大轮拆下。
	srv.rigRemoveComponent(ctx, uid, spinRod.GUID, gearCatReel)
	if reason := srv.placeRigViolation(ctx, uid, spinRod.GUID); reason != "" {
		t.Fatalf("拆轮后不应再违规: %s", reason)
	}
	srv.rigSetComponent(ctx, uid, spinRod.GUID, 0, okReel.GUID, 0,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true, SplitStack: true})
	if reason := srv.placeRigViolation(ctx, uid, spinRod.GUID); reason != "" {
		t.Fatalf("铜壶抛投 size5 纺车轮钓组不应被拒: %s", reason)
	}
}
