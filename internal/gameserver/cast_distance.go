package gameserver

import (
	"context"
	"math"
	"sync"

	"rf4server/internal/proto"
)

// 无轮手竿和缺少装配数据时保留旧协议基线；带轮竿使用综合物理模型。
// 标称距离是修正器链的 100% 力度乘法基准：淡水路亚按官服手感锚点校准
// （满级投距技能的 UL 配置满功率 ≈25m，见
// specs/changes/cast-distance-calibration/spec.md）；海钓路亚（50-54，
// 远投场景）暂留旧值 45 待补锚点；其余默认族同样待锚点。
const (
	castBaseFloatM   float32 = 7.5461488
	castBaseLureM    float32 = 7.1598907
	castBaseDefaultM float32 = castBaseFloatM

	castNominalDefaultM    = 60.0
	castNominalLureM       = 27.5
	castNominalMarineLureM = 45.0
	castMinPoweredM        = 8.0
)

// CastDistanceContext 是抛投模型的一次只读输入。后续加入风力、抛投姿势、
// 轮型或特殊技能时，可在不改主计算流程的情况下扩展字段和修正器。
type CastDistanceContext struct {
	RigType              int64
	CastAction           uint32
	CastPower            float64
	RodLengthM           float64
	RodAction            int64
	RodStiffness         int64
	RodSensitivity       float64
	RodTestMinG          float64
	RodTestMaxG          float64
	RigWeightG           float64
	BaitWeightG          float64
	PayloadWeightG       float64
	ReelEfficiency       float64
	ReelType             int64
	SpoolSize            int64
	SpoolCapacityM       float64
	SpoolFill            float64
	LineDiameterMM       float64
	LineLengthM          float64
	LineStretch          float64
	LineMaterial         int64
	LineLinearDensityGPM float64
	LineRunningWeightG   float64
	PlayerPrimary        float64
	PlayerSecondary      float64
	SkillCategory        int64
	SkillMastery         float64
	SkillCastPct         float64
}

// CastDistanceModifier 是可扩展的抛投距离变量接口。Factor 返回乘数；
// 1 为中性，小于 1 缩短距离，大于 1 增加距离。
type CastDistanceModifier interface {
	Name() string
	Factor(CastDistanceContext) float64
}

type castDistanceModifierFunc struct {
	name string
	fn   func(CastDistanceContext) float64
}

func (m castDistanceModifierFunc) Name() string { return m.name }
func (m castDistanceModifierFunc) Factor(c CastDistanceContext) float64 {
	if m.fn == nil {
		return 1
	}
	return m.fn(c)
}

var (
	extraCastModifiersMu sync.RWMutex
	extraCastModifiers   []CastDistanceModifier
)

// RegisterCastDistanceModifier 注册后续新增变量。注册应在服务启动阶段完成；
// 计算时会读取一份副本，不会与在线抛投互相阻塞。
func RegisterCastDistanceModifier(m CastDistanceModifier) {
	if m == nil || m.Name() == "" {
		return
	}
	extraCastModifiersMu.Lock()
	extraCastModifiers = append(extraCastModifiers, m)
	extraCastModifiersMu.Unlock()
}

var builtInCastModifiers = []CastDistanceModifier{
	castDistanceModifierFunc{name: "cast_action", fn: func(c CastDistanceContext) float64 {
		return castActionFactor(c.CastAction)
	}},
	castDistanceModifierFunc{name: "cast_power", fn: func(c CastDistanceContext) float64 {
		return castPowerFactor(c.CastPower)
	}},
	castDistanceModifierFunc{name: "rod_length", fn: func(c CastDistanceContext) float64 {
		if c.RodLengthM <= 0 {
			return 1
		}
		// 2.4m≈0.99，3.6m≈1.07；长竿有更大的有效加速行程。
		return clampFloat(0.82+0.07*c.RodLengthM, 0.85, 1.18)
	}},
	castDistanceModifierFunc{name: "rod_action", fn: func(c CastDistanceContext) float64 {
		return castRodActionFactor(c.RodAction)
	}},
	castDistanceModifierFunc{name: "rod_stiffness", fn: func(c CastDistanceContext) float64 {
		return castRodStiffnessFactor(c.RodStiffness)
	}},
	castDistanceModifierFunc{name: "rod_test_payload", fn: func(c CastDistanceContext) float64 {
		return rodTestWeightFactor(c.PayloadWeightG, c.RodTestMinG, c.RodTestMaxG)
	}},
	castDistanceModifierFunc{name: "fishing_skill", fn: func(c CastDistanceContext) float64 {
		return castMasteryFactor(c.SkillMastery)
	}},
	castDistanceModifierFunc{name: "special_skill", fn: func(c CastDistanceContext) float64 {
		return castSpecialSkillFactor(c.SkillCastPct)
	}},
	castDistanceModifierFunc{name: "line_diameter", fn: func(c CastDistanceContext) float64 {
		return castLineDiameterFactor(c.LineDiameterMM)
	}},
	castDistanceModifierFunc{name: "line_stretch", fn: func(c CastDistanceContext) float64 {
		return castLineStretchFactor(c.LineStretch)
	}},
	castDistanceModifierFunc{name: "line_material", fn: func(c CastDistanceContext) float64 {
		return castLineMaterialFactor(c.LineMaterial)
	}},
	castDistanceModifierFunc{name: "line_mass", fn: func(c CastDistanceContext) float64 {
		return castLineMassFactor(c.LineRunningWeightG)
	}},
	castDistanceModifierFunc{name: "spool_size", fn: func(c CastDistanceContext) float64 {
		return castSpoolSizeFactor(c.SpoolSize)
	}},
	castDistanceModifierFunc{name: "spool_fill", fn: func(c CastDistanceContext) float64 {
		return castSpoolFillFactor(c.SpoolFill)
	}},
	castDistanceModifierFunc{name: "reel_type", fn: func(c CastDistanceContext) float64 {
		return castReelTypeFactor(c.ReelType)
	}},
	castDistanceModifierFunc{name: "player_state", fn: func(c CastDistanceContext) float64 {
		return castPlayerStateFactor(c.PlayerPrimary)
	}},
	castDistanceModifierFunc{name: "reel_friction", fn: func(c CastDistanceContext) float64 {
		return clampFloat(c.ReelEfficiency, 0.25, 1)
	}},
}

type castDistanceProfile struct {
	DistanceM            float64
	BaseM                float64
	Friction             float64
	Efficiency           float64
	BearingQuality       float64
	OilQuality           float64
	ReelGUID             string
	LineLimitM           float64
	ClipLimitM           float64
	CastAction           uint32
	CastPower            float64
	ReelType             int64
	SpoolSize            int64
	SpoolCapacityM       float64
	SpoolFill            float64
	LineDiameterMM       float64
	LineLengthM          float64
	LineStretch          float64
	LineMaterial         int64
	LineLinearDensityGPM float64
	LineRunningWeightG   float64
	PlayerPrimary        float64
	PlayerSecondary      float64

	RodLengthM         float64
	RodAction          int64
	RodStiffness       int64
	RodSensitivity     float64
	RodTestMinG        float64
	RodTestMaxG        float64
	RigWeightG         float64
	BaitItemID         string
	BaitWeightG        float64
	PayloadWeightG     float64
	PayloadTestRatio   float64 // 载荷 / 竿 Test 上限；0 表示竿未登记 Test
	RodOverTest        bool    // 载荷超过竿 Test 上限，抛投距离开始衰减
	PayloadParts       string  // 组合重量的分项摘要，供日志审计
	WeightFactor       float64
	SkillCategory      int64
	SkillMastery       float64
	SkillCastPct       float64
	SkillFactor        float64
	SpecialSkillFactor float64
	LengthFactor       float64
	Factors            map[string]float64
}

// 抛投动作来自 olieodeoaof.f8。1 是常规全挥杆；0 是短促动作；2 是
// 精准动作。动作只校验本次实际抛投，不会污染下一杆的最大距离。
func castActionFactor(action uint32) float64 {
	switch action {
	case 0:
		return 0.92
	case 2:
		return 0.86
	default:
		return 1
	}
}

func castPowerFactor(power float64) float64 {
	if power <= 0 {
		return 0.05
	}
	// 挥杆能量与距离不是线性关系，低蓄力仍能抛出一段距离。
	return clampFloat(0.18+0.82*math.Sqrt(clampFloat(power, 0, 1)), 0.05, 1)
}

func castLineDiameterFactor(diameterMM float64) float64 {
	if diameterMM <= 0 {
		return 1
	}
	return clampFloat(math.Sqrt(0.25/diameterMM), 0.84, 1.12)
}

// castRodActionFactor 按竿的“调性”修正能量传递：慢调竿身整体弯曲、蓄力
// 行程长但回弹慢，快调竿只有竿梢段参与、回弹干脆，同样挥杆速度下能把更
// 多能量送给线组。这是客户端调性枚举（1=Slow…4=ExtraFast）在服务端唯一
// 的物理落点；其余参数（硬度档）见 castRodStiffnessFactor。
func castRodActionFactor(action int64) float64 {
	switch action {
	case 1: // Slow
		return 0.975
	case 2: // Moderate
		return 0.99
	case 3: // Fast
		return 1.005
	case 4: // ExtraFast
		return 1.02
	default:
		return 1
	}
}

// castRodStiffnessFactor 用硬度档做极小幅修正：越硬的竿梢越不容易“吃掉”
// 挥杆行程，但过硬会牺牲容错。幅度压在 -1.4%..+1.4% 内，不和竿长、Test
// 克重抢主导权，避免同一根竿只换硬度标号就出现距离跳变。
//
// 客户端硬度档实际是 0..10（0=未登记/任意，1..7 常见档，8..10 多为船竿），
// 不是旧注释假设的 1..7。8 档以上按公式已超过 clamp 上限，统一取 1.014；
// 0 保持中性 1.0，异常大值按最硬档封顶，不再静默降级成中性。
func castRodStiffnessFactor(stiffness int64) float64 {
	if stiffness <= 0 {
		return 1
	}
	if stiffness > 10 {
		stiffness = 10
	}
	return clampFloat(0.986+0.004*float64(stiffness), 0.986, 1.014)
}

// 拉伸率越高，蓄力时的能量损失越明显。数值来自主线静态定义。
func castLineStretchFactor(stretch float64) float64 {
	if stretch < 0 {
		return 1
	}
	return clampFloat(1.03-0.05*stretch, 0.97, 1.03)
}

// 主线材质来自静态枚举：1=尼龙，2=碳线，3=编织线。
func castLineMaterialFactor(material int64) float64 {
	switch material {
	case 2:
		return 0.99
	case 3:
		return 1.035
	default:
		return 1
	}
}

// 参与飞行的线段质量会让启动和制动更吃能量；按 g 做温和的非线性衰减。
func castLineMassFactor(weightG float64) float64 {
	if weightG <= 0 {
		return 1
	}
	return clampFloat(1/math.Sqrt(1+weightG/60), 0.80, 1)
}

func castSpoolSizeFactor(size int64) float64 {
	// 线杯规格 1～7 对应常见纺车轮 1000～7000。小杯转动惯量低，
	// 同竿同线同饵时抛得更远；规格 5（6000）作为当前大杯基准。
	switch size {
	case 1:
		return 0.90
	case 2:
		return 0.84
	case 3:
		return 0.79
	case 4:
		return 0.75
	case 5:
		return 0.72
	case 6:
		return 0.68
	case 7:
		return 0.65
	case 8:
		return 0.84 // 低轮廓线杯
	case 9, 10, 11, 12, 13, 14:
		return 0.90 - 0.020*float64(size-9)
	case 15, 16, 17, 18, 19, 20:
		return 0.72 - 0.012*float64(size-15)
	case 21, 22:
		return 0.82
	case 23:
		return 0.56
	case 24:
		return 0.52
	default:
		return 1
	}
}

func castSpoolFillFactor(fill float64) float64 {
	if fill <= 0 {
		return 0.72
	}
	if fill <= 0.80 {
		return clampFloat(0.72+0.28*math.Sqrt(fill/0.80), 0.72, 1)
	}
	if fill <= 1 {
		return 1
	}
	return clampFloat(1-0.35*(fill-1), 0.80, 1)
}

func castReelTypeFactor(reelType int64) float64 {
	switch reelType {
	case 1: // 纺车轮
		return 1
	case 2: // 低轮廓水滴轮
		return 0.98
	case 3: // 鼓轮/水滴轮
		return 0.96
	case 4: // 船轮
		return 0.90
	case 5: // 飞钓轮
		return 0.92
	default:
		return 1
	}
}

// castPlayerStateFactor 玩家状态修正器：只按主状态（精力）比例缩放抛距。
// 饱食度影响恢复与健康，不进抛投（2026-11 用户裁决：抛距由体力决定；
// 0.55/0.45 加权是本地自造标定，无官服依据）。0 值是真实空状态，
// 按 0 计、落到 0.70 下限；不再把 <=0 重置为默认值。
func castPlayerStateFactor(primary float64) float64 {
	return clampFloat(primary/defaultPlayerConditionPrimary, 0.70, 1.03)
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func castBaseDistanceM(rigType int64) float32 {
	if rigTypeLureFamily[rigType] {
		return castBaseLureM
	}
	return castBaseDefaultM
}

func castBaseDistanceBits(rigType int64) uint32 {
	return proto.F32Bits(castBaseDistanceM(rigType))
}

func castNominalDistanceM(rigType int64) float64 {
	if rigTypeLureFamily[rigType] {
		if marineRigType(rigType) {
			// 海钓路亚 50-54 远投场景，锚点未取到前保留旧标称。
			return castNominalMarineLureM
		}
		return castNominalLureM
	}
	return castNominalDefaultM
}

// rodTestWeightFactor 把线组载荷换算为抛投重量系数。
//
// 竿的 Test 区间是“推荐抛投重量”：区间内载荷越重、竿身储能越充分，距离
// 随对数缓慢上升；超过 Test 上限后竿梢已经无法把全部挥杆能量传给线组，
// 距离转为衰减。旧实现全程单调递增，导致 100g 铁板配 2-10g 竿反而抛得
// 比合规载荷更远，与竿梢超重的实际手感相反。
//
// 衰减只在 payload > testMax 时生效，Test 区间内的标定保持原曲线，
// 因此既有距离区间不受影响。该系数只影响抛投，不参与断线或炸杆；
// 炸杆仍由搏鱼张力决定（见 gear_damage.go）。
func rodTestWeightFactor(payloadG, testMinG, testMaxG float64) float64 {
	if testMaxG <= 0 {
		return 1
	}
	if testMinG < 0 {
		testMinG = 0
	}
	if testMinG > testMaxG {
		testMinG, testMaxG = testMaxG, testMinG
	}
	if payloadG <= 0 {
		return 0.80
	}
	effectiveMin := testMinG
	if effectiveMin <= 0 {
		effectiveMin = math.Max(0.1, testMaxG*0.10)
	}
	referenceG := (effectiveMin + testMaxG) * 0.5
	if referenceG <= 0 {
		return 1
	}
	inBand := func(g float64) float64 {
		return 0.80 + 0.12*math.Log1p(g/referenceG)
	}
	if payloadG <= testMaxG {
		return inBand(payloadG)
	}
	// 峰值取 Test 上限处的储能，超出后按超重倍率 r=payload/testMax 衰减。
	// 相对峰值的比例恒为 1/(1+rodTestOverloadDecay*(r-1))，只与超重倍率
	// 有关、与竿型无关：1.5×→74.1%，2×→58.8%，3×→41.7%。
	// r 很大时会撞到 clampFloat 的绝对下限 0.22（那是系数绝对值，不是
	// "剩峰值的 22%"）；下限用于避免"抛在脚边"的极端值。
	peak := inBand(testMaxG)
	overRatio := payloadG/testMaxG - 1
	return clampFloat(peak/(1+rodTestOverloadDecay*overRatio), 0.22, peak)
}

// rodTestOverloadDecay 控制超重衰减陡度。2 倍 Test 上限时 r=2，系数为
// 1/(1+0.70)=58.8% 峰值，接近路亚实钓中"超重后抛投距离明显下降"的量级。
// 该数值是常数取值的标定锚点，由 TestCastOverTestPayloadShortensDistance
// 的数值断言锁定，调整常数必须同步调整断言。
const rodTestOverloadDecay = 0.70

// rodOverTestRatio 返回载荷相对竿 Test 上限的倍率，1 表示刚好在上限。
// 竿未登记 Test 时返回 0（数据缺失，不做超重判断）。
func rodOverTestRatio(payloadG, testMaxG float64) float64 {
	if testMaxG <= 0 || payloadG <= 0 {
		return 0
	}
	return payloadG / testMaxG
}

func castMasteryFactor(mastery float64) float64 {
	mastery = clampFloat(mastery, 0, profCap)
	return 0.82 + 0.18*(mastery/profCap)
}

// 已学习技能节点作为独立的特殊技能变量，避免与大类熟练度重复计算。
// castPct 是官方加成百分比的和（投距类 +5%/级，见
// docs/cast-skill-official-effects.md），直接换算成乘数；
// 极端值由 calculateCastDistance 的因子钳位兜底。
func castSpecialSkillFactor(castPct float64) float64 {
	return 1 + castPct/100
}

func castSkillFactor(mastery, castPct float64) float64 {
	return castMasteryFactor(mastery) * castSpecialSkillFactor(castPct)
}

func castModifiersSnapshot() []CastDistanceModifier {
	extraCastModifiersMu.RLock()
	extras := append([]CastDistanceModifier(nil), extraCastModifiers...)
	extraCastModifiersMu.RUnlock()
	mods := make([]CastDistanceModifier, 0, len(builtInCastModifiers)+len(extras))
	mods = append(mods, builtInCastModifiers...)
	return append(mods, extras...)
}

func calculateCastDistance(c CastDistanceContext) (float64, map[string]float64) {
	distance := castNominalDistanceM(c.RigType)
	factors := make(map[string]float64)
	for _, modifier := range castModifiersSnapshot() {
		if modifier == nil {
			continue
		}
		factor := modifier.Factor(c)
		if math.IsNaN(factor) || math.IsInf(factor, 0) || factor <= 0 {
			factor = 1
		}
		factor = clampFloat(factor, 0.05, 2)
		factors[modifier.Name()] = factor
		distance *= factor
	}
	if distance < castMinPoweredM {
		distance = castMinPoweredM
	}
	return distance, factors
}

// castDistanceForRig 从真实装配、主线实例、轮子性能和技能表构造抛投上下文，
// 再经修正器管线计算。没有安装轮子的手竿继续使用旧协议距离。
func (s *Server) castDistanceForRig(ctx context.Context, uid int64, rodGUID string, rigType int64) castDistanceProfile {
	legacy := float64(castBaseDistanceM(rigType))
	p := castDistanceProfile{DistanceM: legacy, BaseM: legacy, Efficiency: 1,
		CastAction: 1, CastPower: 1,
		PlayerPrimary:   defaultPlayerConditionPrimary,
		PlayerSecondary: defaultPlayerConditionSecondary}
	if s == nil || s.DB == nil || rodGUID == "" {
		return p
	}

	ids := s.rigGearIDs(ctx, uid, rodGUID)
	if ids.ReelGUID == "" {
		return p
	}
	reel, err := s.DB.GetItemByGUID(ctx, ids.ReelGUID)
	if err != nil || reel == nil {
		return p
	}
	perf, err := s.syncReelPerformance(ctx, reel)
	if err != nil || perf == nil {
		return p
	}

	rodType := int64(0)
	if rod, rerr := s.DB.GetItemByGUID(ctx, rodGUID); rerr == nil && rod != nil {
		if def, derr := s.itemDefForRow(ctx, rod); derr == nil {
			// 竿型（fopbhlmkcpe）是投距技能的竿侧 gate 输入。
			if v, ok := defAttrF64(def, "fopbhlmkcpe"); ok {
				rodType = int64(v)
			}
			p.RodLengthM = rodLengthM(def)
			p.RodAction = rodAction(def)
			p.RodStiffness = rodStiffness(def)
			p.RodSensitivity = rodSensitivity(def)
			p.RodTestMinG, p.RodTestMaxG = rodTestG(def)
		}
	}

	line := s.castLineStats(ctx, rodGUID, ids.Line)
	p.LineDiameterMM = line.DiameterMM
	p.LineLengthM = line.LengthM
	p.LineStretch = line.Stretch
	p.LineMaterial = line.Material
	p.LineLinearDensityGPM = line.LinearDensityGPM

	terminalRigWeight, baitWeight, parts := s.castPayloadWeights(ctx, rodGUID)
	// 真正随挥杆启动的只有竿梢到终端之间那段线；线杯上的存线不全部算作
	// 飞行载荷。这里以实际剩余线和竿长中的较小值估算运行线段。
	lineRunningLength := math.Min(p.LineLengthM, math.Max(p.RodLengthM, 0))
	p.LineRunningWeightG = p.LineLinearDensityGPM * lineRunningLength
	p.RigWeightG = terminalRigWeight + p.LineRunningWeightG
	p.BaitWeightG = baitWeight
	p.PayloadWeightG = p.RigWeightG + p.BaitWeightG
	p.PayloadParts = formatCastWeightParts(parts, p.LineRunningWeightG)
	p.PayloadTestRatio = rodOverTestRatio(p.PayloadWeightG, p.RodTestMaxG)
	p.RodOverTest = p.PayloadTestRatio > 1
	// 同一次抛投同时记录咬钩逻辑使用的当前饵 ID，便于审计“饵效饵”和
	// “重量饵”是否来自同一个 f5 装配。饵 ID 本身不做距离乘数，真正
	// 进入物理计算的是上方汇总出的真实重量。
	p.BaitItemID = s.currentBait(ctx, rodGUID)
	// 轮型先于技能统计加载：投距技能按竿/轮的 fopbhlmkcpe 匹配 gate。
	p.ReelType, p.SpoolSize, p.SpoolCapacityM = s.castReelSpoolStats(ctx, reel, ids.Line)
	p.SkillCategory, p.SkillMastery, p.SkillCastPct = s.castSkillStats(ctx, uid, rigType, rodType, p.ReelType, rodGUID)
	p.PlayerPrimary, p.PlayerSecondary = s.castPlayerCondition(ctx, uid)
	if p.SpoolCapacityM > 0 && p.LineLengthM > 0 {
		// 线长是整盘标称长度；超过杯容量时按容量计，避免 600m 线盘被当成
		// 25 倍超装。
		p.SpoolFill = spoolFillRatio(p.LineLengthM, p.SpoolCapacityM)
	}

	c := CastDistanceContext{
		RigType: rigType, CastAction: p.CastAction, CastPower: p.CastPower,
		RodLengthM: p.RodLengthM, RodAction: p.RodAction, RodStiffness: p.RodStiffness,
		RodSensitivity: p.RodSensitivity,
		RodTestMinG:    p.RodTestMinG, RodTestMaxG: p.RodTestMaxG,
		RigWeightG: p.RigWeightG, BaitWeightG: p.BaitWeightG, PayloadWeightG: p.PayloadWeightG,
		ReelEfficiency: perf.CastEfficiency,
		ReelType:       p.ReelType, SpoolSize: p.SpoolSize, SpoolCapacityM: p.SpoolCapacityM,
		SpoolFill: p.SpoolFill, LineDiameterMM: p.LineDiameterMM, LineLengthM: p.LineLengthM,
		LineStretch: p.LineStretch, LineMaterial: p.LineMaterial,
		LineLinearDensityGPM: p.LineLinearDensityGPM, LineRunningWeightG: p.LineRunningWeightG,
		PlayerPrimary: p.PlayerPrimary, PlayerSecondary: p.PlayerSecondary,
		SkillCategory: p.SkillCategory, SkillMastery: p.SkillMastery, SkillCastPct: p.SkillCastPct,
	}
	p.BaseM = castNominalDistanceM(rigType)
	p.Friction = perf.CastFriction
	p.Efficiency = perf.CastEfficiency
	p.BearingQuality = perf.BearingQuality
	p.OilQuality = perf.OilQuality
	p.ReelGUID = ids.ReelGUID
	p.DistanceM, p.Factors = calculateCastDistance(c)
	p.LengthFactor = p.Factors["rod_length"]
	p.WeightFactor = p.Factors["rod_test_payload"]
	p.SkillFactor = p.Factors["fishing_skill"]
	p.SpecialSkillFactor = p.Factors["special_skill"]

	if p.LineLengthM > 0 {
		// 线杯只能容纳 SpoolCapacityM；整盘线长可能只是商品标称长度。
		p.LineLimitM = p.LineLengthM
		if p.SpoolCapacityM > 0 && p.SpoolCapacityM < p.LineLimitM {
			p.LineLimitM = p.SpoolCapacityM
		}
		p.DistanceM = math.Min(p.DistanceM, p.LineLimitM)
	}
	if clip, ok := s.savedReelClipM(ctx, rodGUID); ok && clip > 0 {
		p.ClipLimitM = float64(clip)
		p.DistanceM = math.Min(p.DistanceM, p.ClipLimitM)
	}
	if p.DistanceM < 0.5 {
		p.DistanceM = 0.5
	}
	return p
}

func (s *Server) castDistanceBitsForRig(ctx context.Context, uid int64, rodGUID string, rigType int64) uint32 {
	return proto.F32Bits(float32(s.castDistanceForRig(ctx, uid, rodGUID, rigType).DistanceM))
}
