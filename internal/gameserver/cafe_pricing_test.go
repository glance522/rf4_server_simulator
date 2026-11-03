package gameserver

import (
	"context"
	"math/rand"
	"testing"
)

func TestCafeObservedRewardsStayWithTheirWholeOrder(t *testing.T) {
	cafeRewards.Do(loadCafeRewards)
	if cafeRewards.Err != nil {
		t.Fatal(cafeRewards.Err)
	}
	n := 0
	found := false
	for _, rows := range cafeRewards.Waters {
		for _, row := range rows {
			n++
			if row.Species == "" || row.MinWeightG <= 0 || row.Quantity <= 0 || row.MinRewardCents <= 0 || row.MaxRewardCents < row.MinRewardCents {
				t.Fatalf("无效模板: %+v", row)
			}
			for i := int64(0); i < 5; i++ {
				reward := cafeReward(rand.New(rand.NewSource(i)), row)
				if reward < row.MinRewardCents || reward > row.MaxRewardCents {
					t.Fatal("报价超出实测范围")
				}
				if row.ObservedRewardCents > 0 && reward != row.ObservedRewardCents {
					t.Fatal("最新整单报价被再次乘了数量或鱼市系数")
				}
			}
			if row.Species == "wfish" && row.MinWeightG == 19999 && row.Quantity == 1 && row.ObservedRewardCents == 169352 {
				found = true
			}
		}
	}
	if n != 1716 || !found {
		t.Fatalf("实测数据缺失: rows=%d whitefish=%v", n, found)
	}
}

func TestCafeRareQuoteUsesIntegerWeightRatio(t *testing.T) {
	for _, tc := range []struct{ base, g, w, want int64 }{{169352, 19999, 20000, 169360}, {100, 3, 4, 133}, {1, 2, 3, 2}, {50000, 25000, 40000, 80000}} {
		got, err := cafeScaledReward(tc.base, tc.g, tc.w)
		if err != nil || got != tc.want {
			t.Fatalf("比例报价 %+v => %d %v", tc, got, err)
		}
	}
}

func TestCafeRareVariantIsSparseAndHasItsOwnSpecies(t *testing.T) {
	srv, db := newEconomyTxTestServer(t)
	importFishStaticForProbe(t, db)
	ctx := context.Background()
	seen := map[string]bool{}
	rareN := 0
	for day := 0; day < 120; day++ {
		orders, err := srv.cafeTemplates(ctx, "level_014_carp_lake", 1700000000+int64(day)*86400)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, o := range orders {
			if _, ok := cafeVariantParents[o.Species]; ok {
				count++
				rareN++
				seen[o.Species] = true
				if o.Quantity != 1 {
					t.Fatalf("稀有变种必须只收1条: %+v", o)
				}
			}
		}
		if count > 1 {
			t.Fatal("一次生成了多张变种订单")
		}
	}
	if rareN == 0 || rareN >= 60 || len(seen) < 2 {
		t.Fatalf("变种分布异常: count=%d species=%v", rareN, seen)
	}
}
