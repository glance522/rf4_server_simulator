package gameserver

import (
	"fmt"
	"hash/fnv"
	"math"
	"time"
)

// fishRollContext 只传递本次判鱼的区域和钩上饵，不改动存档格式。
type fishRollContext struct {
	x, z   float64
	hasPos bool
	spot   uint64
	now    time.Time
	baits  []string
}

func rollContext(contexts []fishRollContext) fishRollContext {
	var c fishRollContext
	if len(contexts) > 0 {
		c = contexts[0]
	}
	if c.now.IsZero() {
		c.now = time.Now()
	}
	return c
}

func fishVariation(key string) float64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return float64(h.Sum64()%10000) / 10000
}

// 区域以玩家世界位置划分；缺位置时使用客户端场景与钓点编号。
func fishArea(loc string, c fishRollContext) string {
	if c.hasPos && !math.IsNaN(c.x) && !math.IsNaN(c.z) && !math.IsInf(c.x, 0) && !math.IsInf(c.z, 0) {
		return fmt.Sprintf("%s/%.0f/%.0f", loc, math.Floor(c.x/40), math.Floor(c.z/40))
	}
	return fmt.Sprintf("%s/%d", loc, c.spot)
}

// activityMultiplier 保留非活跃时段的少量杂口，未知鱼种使用中性昼夜权重。
func activityMultiplier(species string, hour int64) float64 {
	night := hour >= 19 || hour < 6
	switch species {
	case "ruffe", "ruffe_n", "eel", "burbot", "w_catfish", "w_catfish_a", "bullhead_syb", "ch_catfish", "fhcatfish":
		if night {
			return 1.8
		}
		return 0.35
	case "n.pike", "perch", "c_bleak", "rudd", "barb_toni":
		if night {
			return 0.45
		}
		return 1.4
	}
	return 1
}

func distributionMultiplier(area, species string, now time.Time) float64 {
	_, hour := worldDayAndHour(now.Unix())
	// 每四个游戏小时重新分布；同区域同鱼种共享结果，不因玩家或换饵重抽。
	period := (now.Unix() - worldDayEpochUnix) / (4 * realSecPerGameHour)
	v := fishVariation(fmt.Sprintf("%s/%s/%d", area, species, period))
	if v < 0.3 {
		return 0
	}
	return (0.15 + 3.85*v*v) * activityMultiplier(species, hour)
}

func applyFishDistribution(loc string, keys []string, weights []float64, c fishRollContext) {
	area := fishArea(loc, c)
	for i, key := range keys {
		weights[i] *= distributionMultiplier(area, key, c.now)
	}
}
