package gameserver

import (
	"context"
	"testing"
	"time"
)

// 欢乐时光的加成行必须等于「点数经验 + ML渔具 + 酒兴 + 高级会员」的合计，
// 也就是说它把所有已有加成整体再翻一倍；总经验 = 各加成行之和（含欢乐时光自己）。
// 旧实现只按点数经验的 50% 给，导致面板那一行和实发经验都对不上。
func TestHappyHourBonusCoversEveryOtherBonus(t *testing.T) {
	s, db := newProgressSrv(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	// 高级会员生效：premiumXP = 点数经验（100%）。
	if _, err := db.SQL.Exec(`UPDATE profile SET membership=1, membership_exp=? WHERE uid=?`,
		time.Now().Add(24*time.Hour).Unix(), uid); err != nil {
		t.Fatal(err)
	}
	// 酒兴 10%，与面板「酒兴」那一行同源。
	seedDrinkEffect(t, s, "wine", 10*time.Minute, newFishGUID())

	fish := &RolledFish{
		FishGUID: newFishGUID(), Species: "perch", Grade: 2, WeightG: 642,
		HappyHour: true, HappyHourKnown: true,
	}
	r, err := s.settleCatchOnce(ctx, uid, fish, 1)
	if err != nil {
		t.Fatal(err)
	}

	var happyXP, otherXP int64
	happyRows := 0
	for _, bonus := range r.bonuses {
		if bonus.Kind == 57 { // 欢乐时光
			happyXP += bonus.XP
			happyRows++
			continue
		}
		otherXP += bonus.XP
	}
	if happyRows != 1 {
		t.Fatalf("欢乐时光加成行应有且只有一条，实际 %d 条：%+v", happyRows, r.bonuses)
	}

	// 欢乐时光 = 点数经验 + 高级会员 + 其余全部加成（ML渔具、酒兴……）。
	wantHappy := r.baseXP + r.premiumXP + otherXP
	if happyXP != wantHappy {
		t.Fatalf("欢乐时光加成 = %d，应为 点数经验 %d + 高级 %d + 其它加成 %d = %d",
			happyXP, r.baseXP, r.premiumXP, otherXP, wantHappy)
	}
	if r.premiumXP != r.baseXP {
		t.Fatalf("高级会员加成应为点数经验全额，got %d base %d", r.premiumXP, r.baseXP)
	}
	// 总经验 = 点数经验 + 高级 + 其它加成 + 欢乐时光 = 2 × 欢乐时光。
	if want := 2 * wantHappy; r.totalXP != want {
		t.Fatalf("总经验 = %d，应为 %d（欢乐时光翻倍）", r.totalXP, want)
	}
}

// 关掉欢乐时光时不应有 57 行，总经验也不翻倍。
func TestHappyHourInactiveAddsNoBonusRow(t *testing.T) {
	s, db := newProgressSrv(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	fish := &RolledFish{
		FishGUID: newFishGUID(), Species: "perch", Grade: 2, WeightG: 642,
		HappyHour: false, HappyHourKnown: true,
	}
	r, err := s.settleCatchOnce(ctx, uid, fish, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, bonus := range r.bonuses {
		if bonus.Kind == 57 {
			t.Fatalf("未激活欢乐时光却出现加成行：%+v", bonus)
		}
	}
	if r.totalXP != r.baseXP+r.premiumXP {
		t.Fatalf("未激活欢乐时光时总经验 = %d，应为 base+premium", r.totalXP)
	}
}
