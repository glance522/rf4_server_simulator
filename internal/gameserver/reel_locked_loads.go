package gameserver

import (
	_ "embed"
	"encoding/json"

	"rf4server/internal/store"
)

//go:embed assets/reel_locked_loads.json
var reelLockedLoadsJSON []byte

var reelLockedLoads = func() map[string]float64 {
	var loads map[string]float64
	if err := json.Unmarshal(reelLockedLoadsJSON, &loads); err != nil {
		panic(err)
	}
	return loads
}()

// 锁轮承力属于轮体，不随刹车片的泄力值一起增加。
// 未收录型号保留旧估算，耐久和轴承改装沿用现有规则。
func reelLockedLoadKg(def *store.ItemDef, drag, durability, bearingFactor float64) float64 {
	if def != nil {
		if load := reelLockedLoads[def.ItemID]; load > 0 {
			factoryQuality := nestedNumber(itemDefNested(def, "fkmdfljgann"), reelPartQualityAttr, 100)
			return load * reelLockMultiplier(durability) / reelLockMultiplier(1) * bearingFactor / reelBearingFactor(factoryQuality)
		}
	}
	return drag * reelLockMultiplier(durability) * bearingFactor
}

func nestedNumber(attrs map[string]any, key string, fallback float64) float64 {
	if n, ok := attrs[key].(float64); ok {
		return n
	}
	return fallback
}
