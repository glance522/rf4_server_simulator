package gameserver

// 垂钓中自然恢复停摆：动作消耗（抛投 0.01、拉拽 0.005）只在竿在水里时发生，
// 若同一时间还按 100/h 恢复，0.36 秒就把一次抛投抹平，精力条永远画满。
// 停竿后照常快速回精力，"打完吃顿饭半分钟再战"的节奏不变。

import (
	"math"
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/store"
)

// 竿在水中时不涨精力；不在水中时按生产默认速率照常恢复。
func TestFishingPausesNaturalRegen(t *testing.T) {
	t.Parallel()
	p := testConditionParams()
	p.EnergyRegenPerHour = defEnergyRegenPerHour
	p.EnergyRegenFishingMult = defEnergyRegenFishingMult
	p.EnergyRegenFoodCost = 0

	cur := store.PlayerCondition{Primary: 0.50, Secondary: 1.0, Comfort: 1.0, UpdatedTS: 1000}

	fishing := advancePlayerCondition(cur, 1000+60, p, conditionEnv{Fishing: true})
	if math.Abs(fishing.Primary-0.50) > 1e-9 {
		t.Fatalf("垂钓中不得恢复精力，期望 0.500000，实得 %.6f", fishing.Primary)
	}

	idle := advancePlayerCondition(cur, 1000+60, p, conditionEnv{})
	// 手算：饱食 1.00-0.24*(60/3600)=0.996；因子 0.25+0.75*0.996=0.997；
	// 恢复 100*0.997*(60/3600)=1.6617，钳到自然封顶 0.85。
	if math.Abs(idle.Primary-p.EnergyRegenCap) > 1e-9 {
		t.Fatalf("停竿 60 秒应回到自然封顶 %.2f，实得 %.6f", p.EnergyRegenCap, idle.Primary)
	}
}

// 停竿后第一拍就恢复（不存在"歇一会儿才开始回"的额外冷却）。
func TestFishingPauseLiftsImmediatelyAfterReelIn(t *testing.T) {
	t.Parallel()
	p := testConditionParams()
	p.EnergyRegenPerHour = defEnergyRegenPerHour
	p.EnergyRegenFishingMult = defEnergyRegenFishingMult
	p.EnergyRegenFoodCost = 0

	cur := store.PlayerCondition{Primary: 0.20, Secondary: 1.0, Comfort: 1.0, UpdatedTS: 1000}
	// 5 秒：100*0.997*(5/3600)=0.1385，远小于封顶，用它验"确实在涨"。
	next := advancePlayerCondition(cur, 1000+5, p, conditionEnv{})
	if next.Primary <= 0.20 {
		t.Fatalf("停竿后应立刻恢复，期望 >0.20，实得 %.6f", next.Primary)
	}
	if next.Primary > 0.20+0.14+1e-9 {
		t.Fatalf("恢复量应约 0.1385，实得 +%.6f", next.Primary-0.20)
	}
}

// 乘数可调：调成 0.5 时垂钓中恢复一半，供维护者做"慢速恢复"标定。
func TestFishingRegenMultIsTunable(t *testing.T) {
	t.Parallel()
	p := testConditionParams()
	p.EnergyRegenPerHour = 1.0
	p.EnergyRegenFishingMult = 0.5
	p.EnergyRegenFoodCost = 0

	cur := store.PlayerCondition{Primary: 0.30, Secondary: 1.0, Comfort: 1.0, UpdatedTS: 1000}
	// 手算：3600 秒 → 饱食 0.76；因子 0.25+0.75*0.76=0.82；
	// 恢复 1.0*0.82*0.5 = 0.41 → 0.71，未触封顶。
	next := advancePlayerCondition(cur, 1000+3600, p, conditionEnv{Fishing: true})
	want := 0.30 + 1.0*(0.25+0.75*(1.0-0.24))*1.0*0.5
	if math.Abs(next.Primary-want) > 1e-9 {
		t.Fatalf("乘数 0.5 时恢复量应减半，期望 %.6f，实得 %.6f", want, next.Primary)
	}
}

// playerFishingActive 只认"已抛投 + 入水 + 未结算"，其余状态一律不算垂钓中。
func TestPlayerFishingActiveStates(t *testing.T) {
	t.Parallel()
	const uid = int64(1000001)
	cases := []struct {
		name string
		st   *FishState
		want bool
	}{
		{"抛投且入水未结算", &FishState{CastActive: true, InWater: true}, true},
		{"已收线未入水", &FishState{CastActive: true, InWater: false}, false},
		{"未抛投", &FishState{CastActive: false, InWater: true}, false},
		{"已结算", &FishState{CastActive: true, InWater: true, Resolved: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(config.Game{}, logging.Discard(), nil)
			srv.fishStates = map[int64]map[string]*FishState{uid: {"rod-1": tc.st}}
			if got := srv.playerFishingActive(uid); got != tc.want {
				t.Fatalf("playerFishingActive=%v，期望 %v", got, tc.want)
			}
			if got := srv.conditionEnvForUID(uid).Fishing; got != tc.want {
				t.Fatalf("conditionEnvForUID().Fishing=%v，期望 %v", got, tc.want)
			}
		})
	}
}

// 多竿时任一竿在水中就算垂钓中；未知玩家不报垂钓，也不因判据去建鱼状态。
func TestPlayerFishingActiveAnyRod(t *testing.T) {
	t.Parallel()
	const uid = int64(1000001)
	srv := NewServer(config.Game{}, logging.Discard(), nil)
	srv.fishStates = map[int64]map[string]*FishState{
		uid: {
			"rod-1": &FishState{CastActive: true, InWater: false},
			"rod-2": &FishState{CastActive: true, InWater: true},
		},
	}
	if !srv.playerFishingActive(uid) {
		t.Fatal("两根竿里有一根在水中，应判为垂钓中")
	}
	if srv.playerFishingActive(int64(999)) {
		t.Fatal("没钓过的玩家不应判为垂钓中")
	}
	if _, exists := srv.fishStates[int64(999)]; exists {
		t.Fatal("判据不应为冷玩家懒建鱼状态")
	}
}
