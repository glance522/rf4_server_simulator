package gameserver

import (
	"context"
	"math"
	"sync"

	"rf4server/internal/proto"
)

// 无轮手竿和缺少装配数据时保留旧协议基线；带轮竿使用综合物理模型。
const (
	castBaseFloatM   float32 = 7.5461488
	castBaseLureM    float32 = 7.1598907
	castBaseDefaultM float32 = castBaseFloatM

	// 基准距离（= 所有修正系数恰为 1.0 时的参考距离），2026-11-13 由实钓资料反推。
	//
	// 锚点（远投实测，用户提供的实钓记录）：
	//   10 ft（3.05 m）远投竿 + 65 g 铁板/雷德 + 15 lb 编织，技术到位 → 约 100 m
	//   10 ft 轻远投竿 + 40 g 拟饵 + 15 lb 编织，「轻松一甩」→ 约 100 m
	// 把 3.05 m 竿 / 50 g 载荷 / 0.15~0.20 mm 编织代入本模型：
	//   竿长 (3.05/2.7)^1.1 = 1.1435、匹配 ≈0.95、载荷 (50/20)^0.25 = 1.257、
	//   线径 ×1.12、伸展 ×1.03、编织 ×1.035 → 系数乘积 ≈ 1.63
	//   基准 = 100 / 1.63 ≈ 61 → 取 60（略保守）。
	//
	// 交叉校验（底钓/远投）：4.2 m 竿 + 100 g 坠 + 细编织
	//   → 60 × 1.626 × 0.890 × 1.495 ≈ 129 m，落在实钓 90~150 m、低于赛事 236 m，合理。
	//
	// 两族取同一基准：路亚与底钓的差别由「竿长」与「绝对载荷」两个因子承担。
	// 旧值 45/60 无测量来源（由一条测试反推）；本次曾误用美式鲈钓估算表（7 ft 竿），
	// 其「最远 44 m」与 10 ft 竿实测 100 m 明显冲突，已弃用。
	castNominalDefaultM = 60.0
	castNominalLureM    = 60.0
	castMinPoweredM     = 8.0
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
	SkillNodeEffect      float64
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
		// 竿尖线速度与竿长近似同阶，距离按 (L/2.7m)^1.1 归一：
		// 1.8m≈0.64，2.7m=1.00，3.9m≈1.50，4.2m≈1.63。
		// 旧式 (0.82+0.07L, 0.85..1.18) 把 2.4~3.6 m 的差距压到 8%，长竿抛不远。
		return clampFloat(math.Pow(c.RodLengthM/2.7, 1.1), 0.45, 1.9)
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
	castDistanceModifierFunc{name: "payload_mass", fn: func(c CastDistanceContext) float64 {
		return castPayloadMassFactor(c.PayloadWeightG)
	}},
	castDistanceModifierFunc{name: "fishing_skill", fn: func(c CastDistanceContext) float64 {
		return castMasteryFactor(c.SkillMastery)
	}},
	castDistanceModifierFunc{name: "special_skill", fn: func(c CastDistanceContext) float64 {
		return castSpecialSkillFactor(c.SkillNodeEffect)
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
	// 抛投力度随体力（精力）下降：满体力 = 满力度，体力 50% 时力度掉到 90~100
	// （实测口径：满体力 130 力度，半体力约 90~100）。饱食度不参与——饥饿只影响
	// 体力恢复速度与健康，不直接削减力度（上游 #462 同一口径）。
	castDistanceModifierFunc{name: "player_state", fn: func(c CastDistanceContext) float64 {
		return castEnergyStateFactor(c.PlayerPrimary)
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
	SkillNodeEffect    float64
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
	// 线杯规格对抛投的影响：小线杯出线更顺（杯口小、出线角度更集中、出线阻力低、
	// 转动惯量小），大线杯储线多但出线阻力与转动惯量更大，同等条件应略近。
	// 旧实现注释写的是「小杯转动惯量低，同竿同线同饵时抛得更远」，但数值表却是
	// 小杯折减（1→0.90）、大杯折减更少（7→0.65），注释与实现自相矛盾。
	// SpoolSize 是线杯档位枚举（1 最小 … 7 最大），不是容量毫升数。
	// 2026-11-13 重标定：以 4~5 档（5000~6000，路亚/远投常用杯）为中性 1.0，
	// 小杯 1 档 +3%、2 档 +2%，大杯 6 档 −1%、7 档 −3%，整体落差 ≈6%。
	// 容量本身由 spool_capacity / spool_fill 单独承担，此处不重复计。
	if size <= 0 {
		return 1
	}
	switch {
	case size <= 1:
		return 1.03
	case size == 2:
		return 1.02
	case size == 3:
		return 1.01
	case size <= 5:
		return 1.00
	case size == 6:
		return 0.99
	default:
		return 0.97
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

// castPlayerStateFactor 玩家状态修正器：精力/饱食度越低抛得越近。
// 0 值是真实空状态，按 0 参与加权、落到 0.70 下限；
// 不再把 <=0 重置为默认值（旧边界会把真·空状态当成满状态）。
func castPlayerStateFactor(primary, secondary float64) float64 {
	state := 0.55*(primary/defaultPlayerConditionPrimary) +
		0.45*(secondary/defaultPlayerConditionSecondary)
	return clampFloat(state, 0.70, 1.03)
}

// castEnergyStateFactor 是官方口径的抛投力度系数：只认体力（精力）。
// 实测口径——满体力 130 力度；体力降到 50% 时力度掉到约 90~100，即系数 ≈0.70~0.77。
// clamp(体力/0.85, 0.70, 1.03) 恰好复现：满体力 1.00（130）、半体力 0.70（91）。
// 上限 1.03 对应"体力被消耗品顶到 1.0 时比自然满值再远 3%"。
func castEnergyStateFactor(primary float64) float64 {
	if defaultPlayerConditionPrimary <= 0 {
		return 1
	}
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
		// Test 缺失（静态库 1043 根竿里有 218 根没登记 Test）：既不白给满分，
		// 也不能按超载重罚，取中性偏保守。
		return 0.85
	}
	if testMinG < 0 {
		testMinG = 0
	}
	if testMinG > testMaxG {
		testMinG, testMaxG = testMaxG, testMinG
	}
	if payloadG <= 0 {
		return 0.35
	}
	ratio := payloadG / testMaxG
	bandLo := 0.10
	if testMinG > 0 {
		bandLo = testMinG / testMaxG
	}
	if bandLo <= 0.01 || bandLo >= 1 {
		bandLo = 0.10
	}
	if ratio > 1 {
		// 超载：竿梢被压死，沿用 0.70 衰减（2× Test ≈ 峰值 58.8%）。
		return clampFloat(1/(1+rodTestOverloadDecay*(ratio-1)), 0.22, 1)
	}
	if ratio >= bandLo {
		// 区间内：Test 下限处 0.78 → Test 上限处 1.00（上限比下限远 28%），
		// 与实钓资料「额定区间上限比下限远 20~30%」一致。逐竿用自己的
		// testMin/testMax 归一，所以 1043 根竿各自自动对齐，无需逐竿标定。
		return clampFloat(0.78+0.22*(ratio-bandLo)/(1-bandLo), 0.78, 1)
	}
	// 低于额定下限：欠载、竿身蓄不起来，按平方根继续掉。
	return clampFloat(0.78*math.Sqrt(ratio/bandLo), 0.35, 0.78)
}

// castPayloadMassFactor 是「绝对载荷」对距离的贡献，与竿的额定区间无关。
// 同竿同线时载荷越重，空气阻力相对动能越小、越能保持速度，因而抛得越远；
// 这也是 UL 竿只能在 10~17 m、XH 竿能到 40~50 m 的根本原因之一。
// 以 20 g = 1.0 归一，(g/20)^0.25，钳在 [0.35, 1.8]：
//
//	1 g→0.47  3 g→0.62  10 g→0.84  20 g→1.00
//	50 g→1.26 100 g→1.50 200 g→1.78
func castPayloadMassFactor(payloadG float64) float64 {
	if payloadG <= 0 {
		return 0.35
	}
	return clampFloat(math.Pow(payloadG/20, 0.25), 0.35, 1.8)
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
func castSpecialSkillFactor(nodeEffect float64) float64 {
	return 1 + clampFloat(nodeEffect*0.002, 0, 0.08)
}

func castSkillFactor(mastery, nodeEffect float64) float64 {
	return castMasteryFactor(mastery) * castSpecialSkillFactor(nodeEffect)
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

	if rod, rerr := s.DB.GetItemByGUID(ctx, rodGUID); rerr == nil && rod != nil {
		if def, derr := s.itemDefForRow(ctx, rod); derr == nil {
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
	p.SkillCategory, p.SkillMastery, p.SkillNodeEffect = s.castSkillStats(ctx, uid, rigType, rodGUID)
	p.PlayerPrimary, p.PlayerSecondary = s.castPlayerCondition(ctx, uid)
	p.ReelType, p.SpoolSize, p.SpoolCapacityM = s.castReelSpoolStats(ctx, reel, ids.Line)
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
		SkillCategory: p.SkillCategory, SkillMastery: p.SkillMastery, SkillNodeEffect: p.SkillNodeEffect,
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
