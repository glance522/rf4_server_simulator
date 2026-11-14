package gameserver

import (
	"hash/fnv"
	"time"
)

// fishRollContext 只传递本次判鱼的区域和钩上饵，不改动存档格式。
type fishRollContext struct {
	x, z   float64
	hasPos bool
	spot   uint64
	loc    string
	// uid 本次判鱼的玩家。窝料账本按 (uid, loc, 落点格子) 结算，
	// 没有它就查不到窝。
	uid        int64
	now        time.Time
	baits      []string
	lureBait   bool
	forRig     bool
	depthCM    int64
	lureLayer  float64
	lureAction string
	lureMask   uint32
	lureSpeed  int64
	weather    fishingWeather
	rigType    int64
	happyBite  float64
	onBoat     bool
	boatKnown  bool
	biteRoll   func() float64
	cache      *fishingRollCache
	skill      fishingSkillBonus
	spotEnv    fishSpotEnvironment
	runtime    *fishRollRuntime
	hook       gearPhysicsSpec
	query      *fishQueryCapture
	// gateBlocked 由 rollFishWithRules 挂指针：硬性鱼口门把本图名册剔空时置位，
	// 调用方据此跳过内置鱼池兜底——门拦下的杆就该空竿重排，不能从鱼池
	// 捞出不属于本图的鱼。指针随值拷贝存活。
	gateBlocked *bool
	// emptyReason 记录把候选滤空的那一层（诊断日志用，不进玩家提示）。
	// 指针随值拷贝存活，与 gateBlocked 同款用法。
	emptyReason *string
	// pityEnv 是保底杂口标记：连续多轮空杆后由咬钩计时器置位，
	// 只放宽环境类因素（分布散布归零→0.15、水层地板≥0.05），
	// 食性硬门（G1 拟饵×非掠食鱼、明确禁饵）不放宽。
	pityEnv bool
}

type fishRollRuntime struct {
	lure      lureAttributes
	lureKnown bool
	// levelChecked 保证一次判鱼只解析一次地点与海图区号。
	levelChecked bool
	// chartKnown 表示该地点有没有海图（目前只有挪威海）；zoneKnown 表示本次
	// 落点能否定位到具体区号。两者共同决定海图条件算不算有证据。
	chartKnown bool
	zone       int
	zoneKnown  bool
	spotPref   map[string]float64
	spotData   bool
	// sonarSystemID 是海图声呐档案键（如 loc12_215）；内陆为空。
	sonarSystemID string
}

func rollContext(contexts []fishRollContext) fishRollContext {
	var c fishRollContext
	if len(contexts) > 0 {
		c = contexts[0]
	}
	if c.now.IsZero() {
		c.now = time.Now()
	}
	if c.cache == nil {
		c.cache = newFishingRollCache()
	}
	return c
}

func fishVariation(key string) float64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return float64(h.Sum64()%10000) / 10000
}
