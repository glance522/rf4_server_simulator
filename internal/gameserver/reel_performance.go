package gameserver

import (
	"context"
	"math"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

const reelDurabilityStep = 0.20

// reelSlipWear 是旧直接磨损入口的基准值，等价于 29 档；生产搏鱼链路按
// 当前 17/14 摩擦档位调用 reelSlipWearForSetting。正常低档位几乎不磨，
// 只有玩家把泄力推到 25~29 才明显消耗摩擦片。
//
// 生产链路的逐档磨损率改为 tunable（后台可配，specs/changes/reel-wear-tunables），
// 这些常量只作为缺省回落值，同时保留给旧直接入口使用。
const (
	reelSlipWear    = 0.0001   // 29 档，0.01%/次
	reelSlipWearLow = 0.000001 // 24 档及以下，0.0001%/次
	reelSlipWearMax = 0.0001   // 29 档，0.01%/次
)

// 逐档磨损率的缺省值（与上面的常量同值，分开命名以便 tunable 回落时语义清晰）。
const (
	defaultReelSlipWearLow = reelSlipWearLow
	defaultReelSlipWear25  = 0.000025
	defaultReelSlipWear26  = 0.00005
	defaultReelSlipWear27  = 0.000075
	defaultReelSlipWear28  = 0.000095
	defaultReelSlipWearMax = reelSlipWearMax
)

// reelSlipTunable 读取单个磨损率 tunable：缺失、非数值、≤0 或非有限值都回落到
// 代码默认值，保证未配置时的行为与硬编码逐字节一致。
func (s *Server) reelSlipTunable(key string, def float64) float64 {
	v := s.tunableF(key, def)
	if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return def
	}
	return v
}

// reelSlipWearForSetting 把线上摩擦档位换算成单次打滑的摩擦片寿命损耗。
// 线上 0~28 是 UI 1~29，29 是锁轮；锁轮另走发热曲线，这里的 29 值只作为
// 曲线边界和诊断口径保留。逐档倍率走 tunable，后台可调。
//
// 这是生产入口：读取 tunable，缺失时回落到代码默认。
func (s *Server) reelSlipWearForSetting(setting int64) float64 {
	switch setting {
	case 25:
		return s.reelSlipTunable("reel_slip_wear_s25", defaultReelSlipWear25)
	case 26:
		return s.reelSlipTunable("reel_slip_wear_s26", defaultReelSlipWear26)
	case 27:
		return s.reelSlipTunable("reel_slip_wear_s27", defaultReelSlipWear27)
	case 28:
		return s.reelSlipTunable("reel_slip_wear_s28", defaultReelSlipWear28)
	case 29:
		return s.reelSlipTunable("reel_slip_wear_max", defaultReelSlipWearMax)
	default:
		if setting < 25 {
			return s.reelSlipTunable("reel_slip_wear_low", defaultReelSlipWearLow)
		}
		return s.reelSlipTunable("reel_slip_wear_max", defaultReelSlipWearMax)
	}
}

// reelSlipWearForSettingDefault 是包级默认口径：不读 tunable，直接给代码默认值。
// 测试与需要「标称曲线」的场合用它，避免被后台配置影响断言。
func reelSlipWearForSettingDefault(setting int64) float64 {
	switch setting {
	case 25:
		return defaultReelSlipWear25
	case 26:
		return defaultReelSlipWear26
	case 27:
		return defaultReelSlipWear27
	case 28:
		return defaultReelSlipWear28
	case 29:
		return defaultReelSlipWearMax
	default:
		if setting < 25 {
			return defaultReelSlipWearLow
		}
		return defaultReelSlipWearMax
	}
}

const reelPartQualityAttr = "obifdihmcfn"

// 渔轮内部六件的品类编号：磨损、承力与工坊都按这套编号寻件。
// 官方抓包核实（specs/changes/wear-fight-time/spec.md）：34 是旋钮（knobs），
// 摇柄握把是竿侧 category 4——两者官服实测都不磨损，34 因此不进磨损矩阵。
const (
	reelPartCatBearing = int64(7)  // 轴承
	reelPartCatSpool   = int64(8)  // 线杯
	reelPartCatWasher  = int64(9)  // 摩擦片
	reelPartCatOil     = int64(10) // 轮油
	reelPartCatHandle  = int64(34) // 旋钮（knobs，官服实测不磨损；历史注释误作摇柄）
	reelPartCatGear    = int64(52) // 齿轮
	reelPartCatMotor   = int64(81) // 电动轮电机（磨损停扣，待官服电轮数据）
)

// 竿侧零件品类编号：官服搏鱼快照里与轮件并列受力磨损（specs/changes/wear-fight-time/spec.md §1）。
const (
	reelPartCatRodBlank  = int64(36) // 竿坯
	reelPartCatRodHandle = int64(4)  // 竿侧手柄（官服不磨损，仅随帧下发）
	reelPartCatGuides    = int64(5)  // 导环
)

const (
	defaultElectricLockedMultiplier = 3.0
	defaultElectricMotorKgPerPower  = 1.0
	electricMotorPowerAttr          = "afmboidbbaj"
)

// clampUnit 把耐久这类 0~1 的剩余比例收进合法区间。
func clampUnit(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// reelLockMultiplier 按耐久阶段给出锁轮承力倍率：
// 100%=1.4、80%=1.3、60%=1.2、40%=1.1、20%=1.0。
// 耐久为 0 表示轮子已损坏：承力必须为 0，不能再按最后一档 1.0 继续使用，
// 否则客户端会看到"损坏"却仍显示正常锁轮承力，服务端也仍把它当可用件比较。
func reelLockMultiplier(durability float64) float64 {
	if durability <= 1e-9 {
		return 0
	}
	if durability < reelDurabilityStep {
		durability = reelDurabilityStep
	}
	if durability > 1 {
		durability = 1
	}
	stage := math.Ceil((durability - 1e-9) / reelDurabilityStep)
	if stage < 1 {
		stage = 1
	}
	if stage > 5 {
		stage = 5
	}
	return 0.9 + stage*0.1
}

// reelBearingFactor 把轴承品质与剩余耐久映射为传力效率。低档轴承会让制动力产生
// 少量损失，高档轴承接近完整传递摩擦片的制动力；轴承磨旧后再掉一截（最多 −15%），
// 所以换轴承是真的能把泄力与锁轮承力找回来。
func reelBearingFactor(quality, durability float64) float64 {
	quality = math.Max(0, math.Min(100, quality))
	return (0.90 + quality/1000) * (0.85 + 0.15*clampUnit(durability))
}

// reelGearFactor 把齿轮品质与剩余耐久映射为传动效率。齿轮磨旧会让传动链打滑，
// 锁轮承力最多再掉 10%：齿轮是长期账，但确实参与承力。
func reelGearFactor(quality, durability float64) float64 {
	quality = math.Max(0, math.Min(100, quality))
	return (0.95 + quality/2000) * (0.90 + 0.10*clampUnit(durability))
}

// reelLockedDragKg 是锁轮承力的唯一算法：剩余制动力 x 本体耐久档位倍率 x 轴承效率
// x 齿轮效率。性能表落库与搏鱼裁决都必须走这一条，否则磨了摩擦片而力度上限不动，
// "满红不爆"只修好一半。
func reelLockedDragKg(base, durability, bearingFactor, gearFactor float64) float64 {
	if base <= 0 {
		return 0
	}
	return base * reelLockMultiplier(durability) * bearingFactor * gearFactor
}

// electricLockedMultiplier 是电轮锁轮有效承力的倍率。默认固定为 3 倍，
// 但保留数值 tunable，方便后台紧急调整。
func (s *Server) electricLockedMultiplier() float64 {
	v := s.tunableF("electric_locked_multiplier", defaultElectricLockedMultiplier)
	if v < 1 || v > 10 {
		return defaultElectricLockedMultiplier
	}
	return v
}

func reelMotorQualityFactor(quality float64) float64 {
	quality = math.Max(0, math.Min(100, quality))
	return 0.85 + 0.15*quality/100
}

// reelMotorCapacityInFields 计算当前实装电机的有效承力。电机功率字段本身就是
// F32 位型，当前数据里 rengine_1/rengine_2 分别约 33kg/47kg，因此默认 1:1
// 映射到承力公斤，同时保留 tunable 供后续按实机表现校准。
func (s *Server) reelMotorCapacityInFields(ctx context.Context, row *store.Item, fields []proto.Field,
	bearingFactor, gearFactor float64) (capacity, durability float64, found bool) {
	if s == nil || row == nil {
		return 0, 0, false
	}
	child, def, _, ok := s.selectedReelPart(ctx, fields, reelPartCatMotor)
	if !ok || child == nil || def == nil {
		return 0, 0, false
	}
	powerKg := itemDefF32(def, electricMotorPowerAttr)
	if powerKg <= 0 {
		return 0, 0, true
	}
	scale := s.tunableF("electric_motor_kg_per_power", defaultElectricMotorKgPerPower)
	if scale <= 0 || scale > 100 {
		scale = defaultElectricMotorKgPerPower
	}
	workmanship, ok := wireFloatAt(child, csWorkmanship)
	if !ok || workmanship <= 0 {
		workmanship = 1
	}
	durability = reelChildDurability(child)
	if durability <= 0 {
		return 0, 0, true
	}
	oilDurability := 1.0
	if oil, _, _, ok := s.selectedReelPart(ctx, fields, reelPartCatOil); ok && oil != nil {
		oilDurability = reelChildDurability(oil)
	}
	oilDrive := 0.90 + 0.10*clampUnit(oilDurability)
	capacity = powerKg * scale *
		clampUnit(workmanship) *
		reelMotorQualityFactor(reelPartQuality(def)) *
		math.Sqrt(durability) *
		bearingFactor * gearFactor * oilDrive
	return capacity, durability, true
}

// reelEffectiveLockedDragInFields 是电轮锁轮承力的唯一派生入口：
// 机械承力与电机有效承力取小，再乘实时倍率。普通轮、缺失电机或电机已损坏时
// 自动退回普通机械锁轮，避免电机归零把手动摇轮也判成 0。
//
// 机械承力口径：规格表（reel_strength.json）命中的轮子 = 出厂轮根规格 × 轴承
// 子件耐久——与客户端轮根字段同源（官方 4/22 实抓：Turion SW 30000 满耐久
// 127.94，磨损后 126.35）；本体耐久归 0（轮子损坏）承力归零。未收录轮子回退
// 旧公式（基础制动力 × 耐久倍率 × 轴承 × 齿轮）。
func (s *Server) reelEffectiveLockedDragInFields(ctx context.Context, row *store.Item, fields []proto.Field,
	base, durability, bearingFactor, gearFactor float64) float64 {
	mechanical := 0.0
	if row != nil {
		if spec, ok := reelRootSpec(row.ItemID); ok {
			if durability > 0 {
				_, _, bearingDur := s.reelPartStateFromRow(ctx, row, reelPartCatBearing)
				mechanical = spec * clampUnit(bearingDur)
			}
		} else {
			mechanical = reelLockedDragKg(base, durability, bearingFactor, gearFactor)
		}
	} else {
		mechanical = reelLockedDragKg(base, durability, bearingFactor, gearFactor)
	}
	if mechanical <= 0 {
		return 0
	}
	motorCapacity, motorDurability, found := s.reelMotorCapacityInFields(
		ctx, row, fields, bearingFactor, gearFactor)
	if !found || motorDurability <= 0 || motorCapacity <= 0 {
		return mechanical
	}
	core := math.Min(mechanical, motorCapacity)
	if core <= 0 {
		return mechanical
	}
	return s.electricLockedMultiplier() * core
}

// reelOilWearFactor 返回摩擦片磨损倍率。轮油品质越高、剩余油量越充足，
// 保护越强；油干涸后有效品质下降，磨损倍率会逐步回到普通水平。
func reelOilWearFactor(quality, durability float64) float64 {
	quality = math.Max(0, math.Min(100, quality))
	effective := quality * (0.5 + 0.5*clampUnit(durability))
	return 1.25 - effective/200
}

// reelCastFriction 把轴承品质与耐久、轮油品质与干涸程度、轮子本体耐久换算成放线摩擦
// 系数。油越好越润、轴承越新越顺、轮子越健康，摩擦越小；返回值越大，客户端能用的
// 全力抛投距离越短。轮油自身快干涸时润滑效果对半打折，轴承磨旧再单独加一档。
func reelCastFriction(bearingQuality, bearingDurability, oilQuality, oilDurability, durability float64) float64 {
	bearingQuality = math.Max(0, math.Min(100, bearingQuality))
	oilQuality = math.Max(0, math.Min(100, oilQuality))
	oilEffective := oilQuality * (0.5 + 0.5*clampUnit(oilDurability))
	friction := 0.05 +
		0.18*(1-bearingQuality/100) +
		0.16*(1-oilEffective/100) +
		0.12*clampUnit(1-durability) +
		0.06*(1-clampUnit(bearingDurability))
	return math.Max(0.05, math.Min(0.55, friction))
}

// reelPartDef 返回内嵌轮子零件对应的静态定义。
func (s *Server) reelPartDef(ctx context.Context, child *proto.Value) *store.ItemDef {
	if s == nil || s.DB == nil || child == nil || child.Kind != proto.KindObj {
		return nil
	}
	idx := fieldIdxByCS(child.Class, csNumericID)
	if idx < 0 || idx >= child.FieldCount() || child.Fields[idx].Num == nil {
		return nil
	}
	def, err := s.DB.GetItemDef(ctx, int64(child.Fields[idx].U32()))
	if err != nil {
		return nil
	}
	return def
}

// selectedReelPart 返回轮子树中当前安装的指定品类零件及其静态定义。
// 工坊改装后的传动比、摩擦力和轴承油品都必须优先从这里读取。
func (s *Server) selectedReelPart(ctx context.Context, fields []proto.Field, category int64) (*proto.Value, *store.ItemDef, int, bool) {
	for i := range fields {
		child := fields[i].Val
		if child == nil || child.Kind != proto.KindObj {
			continue
		}
		def := s.reelPartDef(ctx, child)
		if def != nil && def.Category == category {
			return child, def, i, true
		}
	}
	return nil, nil, -1, false
}

// frictionWasherDrag 读取摩擦片子实例里已持久化的制动力。
// present=false 表示字段尚未初始化（历史存档/新物品），此时才允许按零件定义
// 补齐；present=true 时返回字段原值，0 也必须原样保留——0 是"摩擦片已磨穿、
// 泄力失效"，不是"数据缺失"。registry 里 csDragForce 是 U32 承载的 F32 位型。
func frictionWasherDrag(child *proto.Value) (value float64, present bool, idx int) {
	if child == nil {
		return 0, false, -1
	}
	idx = fieldIdxByCS(child.Class, csDragForce)
	if idx < 0 || idx >= child.FieldCount() {
		return 0, false, idx
	}
	switch fieldOpAt(child.Class, idx) {
	case "FLOAT", "DOUBLE":
		v := child.Fields[idx].Float
		return v, v != 0, idx
	default:
		raw := child.Fields[idx].Num
		if raw == nil {
			return 0, false, idx
		}
		return float64(proto.F32FromBits(uint32(*raw))), true, idx
	}
}

// reelInstalledDragFieldFromRow reads only the stored force field of the
// installed washer. present means that washer resolves to category 9 and its
// force scalar can be read, including zero; it does not validate the physical
// range and never denotes a value obtained from a definition fallback.
func (s *Server) reelInstalledDragFieldFromRow(ctx context.Context, row *store.Item) (value float64, present bool) {
	if row == nil {
		return 0, false
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return 0, false
	}
	child, _, _, found := s.selectedReelPart(ctx, fields, 9)
	if !found {
		return 0, false
	}
	value, present, _ = frictionWasherDrag(child)
	return value, present
}

// reelBaseDragFromRow 从当前已安装的摩擦片实例读取基础制动力。
// 老数据的子实例若缺物理字段，会回退到所选零件定义，而不是母轮出厂定义。
// 零件字段记录的是零件自身成色；母轮顶层受力值还要乘母轮成色。
func (s *Server) reelBaseDragFromRow(ctx context.Context, row *store.Item) float64 {
	return s.reelInstalledDragFromRow(ctx, row)
}

// reelInstalledDragFromRow 读取当前摩擦片实例自身的物理制动力（未乘母轮成色）。
func (s *Server) reelInstalledDragFromRow(ctx context.Context, row *store.Item) float64 {
	if row == nil {
		return 0
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err == nil {
		for i := range fields {
			child := fields[i].Val
			def := s.reelPartDef(ctx, child)
			if def == nil || def.Category != reelPartCatWasher {
				continue
			}
			if cur, present, _ := frictionWasherDrag(child); present {
				// 制动力是持久化状态：字段存在就以其为准，包括 0（已磨穿、泄力失效）。
				return cur
			}
			if v := itemDefF32(def, gearAttrDragMax) * row.Workmanship; v > 0 {
				return v
			}
		}
	}
	if def, derr := s.itemDefForRow(ctx, row); derr == nil {
		return reelDragMaxKg(def) * row.Workmanship
	}
	return 0
}

// degradeFrictionWasher 按单次基准磨损磨摩擦片（旧口径入口，等价于 wear=reelSlipWear）。
func (s *Server) degradeFrictionWasher(ctx context.Context, reel *store.Item) (wornOut bool, found bool, err error) {
	return s.degradeFrictionWasherBy(ctx, reel, reelSlipWear)
}

// degradeFrictionWasherFields 在内存中的零件树上磨摩擦片：制动力 csDragForce 按 wear
// （寿命比例）线性衰减，并把耐久字段写成等比剩余值——裁决与承力只认制动力，但背包与工坊要能看到损耗百分比。
// 返回 wornOut（制动力归零、泄力失效）与 found（是否找到摩擦片子件）；找不到时调用方回退本体耐久通道。
func (s *Server) degradeFrictionWasherFields(ctx context.Context, reel *store.Item,
	fields []proto.Field, wear float64) (wornOut bool, found bool) {
	if reel == nil {
		return false, false
	}
	child, def, _, ok := s.selectedReelPart(ctx, fields, reelPartCatWasher)
	if !ok || child == nil {
		return false, false
	}
	cur, present, idx := frictionWasherDrag(child)
	if idx < 0 || idx >= len(child.Fields) {
		return false, true
	}
	maxDrag := itemDefF32(def, gearAttrDragMax) * reel.Workmanship
	if maxDrag <= 0 {
		return false, true
	}
	if !present {
		// 只有"字段缺失"才用零件定义值初始化；存量零值由一次性迁移处理。
		cur = maxDrag
	} else if cur <= 0 {
		// 字段存在且为 0 = 已磨穿。泄力失效必须保持，下一次打滑不能复活制动力。
		return true, true
	}
	if wear > 0 {
		// 线性磨损：按寿命比例烧掉额制制动力的一小份。
		next := cur - maxDrag*wear
		if next < 0 {
			next = 0
		}
		setWireFloat(child.Fields, child.Class, idx, next)
		cur = next
	}
	// 摩擦片耐久字段是制动力剩余比例的镜像：工坊与背包靠它显示损耗%。
	setReelChildDurability(child, reelWasherLife(cur, maxDrag))
	return cur <= 0, true
}

// degradeFrictionWasherBy 磨一次摩擦片并落库（单独改物品树的入口）。
// 拼接型结算（零件耐久与本体聚合要同一次写入）走 degradeFrictionWasherFields。
func (s *Server) degradeFrictionWasherBy(ctx context.Context, reel *store.Item, wear float64) (wornOut bool, found bool, err error) {
	if reel == nil {
		return false, false, nil
	}
	fields, err := FieldsFromJSON(reel.FieldsJSON)
	if err != nil {
		return false, false, err
	}
	wornOut, found = s.degradeFrictionWasherFields(ctx, reel, fields, wear)
	if !found {
		return false, false, nil
	}
	bumpCreatedTicks(fields, reel.WireClass)
	fj, err := FieldsToJSON(fields)
	if err != nil {
		return false, true, err
	}
	// 摩擦片与母轮受力上限要一起写，不能只写子件。
	snapshot := *reel
	snapshot.FieldsJSON = fj
	if s.canonicalizeReelStrength(ctx, &snapshot, fields) {
		fj, err = FieldsToJSON(fields)
		if err != nil {
			return false, true, err
		}
	}
	if err := s.DB.UpdateItemFields(ctx, reel.GUID, fj, reel.Qty, reel.Durability, reel.Workmanship, reel.RigGUID); err != nil {
		return false, true, err
	}
	reel.FieldsJSON = fj
	return wornOut, true, nil
}

// reelPartQualityFromRow 读取轮子当前内嵌零件及静态品质。没有该零件时返回
// 100 品质，以兼容缺少旧版内嵌配置的历史物品。
func (s *Server) reelPartQualityFromRow(ctx context.Context, row *store.Item, category int64) (string, float64) {
	if row == nil {
		return "", 100
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return "", 100
	}
	for i := range fields {
		def := s.reelPartDef(ctx, fields[i].Val)
		if def == nil || def.Category != category {
			continue
		}
		quality := float64(itemDefInt(def, reelPartQualityAttr))
		quality = math.Max(0, math.Min(100, quality))
		return def.ItemID, quality
	}
	return "", 100
}

// buildReelPerformance 按当前物品树计算轮子物理参数，但不写库。
// 经济事务需要先完成所有依赖 item_def 的读取，再在事务内统一落盘。
func (s *Server) buildReelPerformance(ctx context.Context, row *store.Item) (*store.ReelPerformance, error) {
	base := s.reelBaseDragFromRow(ctx, row)
	bearingItemID, bearingQuality, bearingDurability := s.reelPartStateFromRow(ctx, row, reelPartCatBearing)
	_, gearQuality, gearDurability := s.reelPartStateFromRow(ctx, row, reelPartCatGear)
	oilItemID, oilQuality, oilDurability := s.reelPartStateFromRow(ctx, row, reelPartCatOil)
	bearingFactor := reelBearingFactor(bearingQuality, bearingDurability)
	gearFactor := reelGearFactor(gearQuality, gearDurability)
	durability := row.Durability
	// 轮子耐久没有 20% 下限：归零即损坏（物品树带 128 损坏位），承力与抛投
	// 摩擦都必须按 0 结算，修理后由耐久 1.0 恢复正常。
	if durability < 0 {
		durability = 0
	}
	if durability > 1 {
		durability = 1
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return nil, err
	}
	v := &store.ReelPerformance{
		ReelGUID:   row.GUID,
		UID:        row.UID,
		BaseDragKg: base,
		LockedDragKg: s.reelEffectiveLockedDragInFields(
			ctx, row, fields, base, durability, bearingFactor, gearFactor),
		Durability:     durability,
		BearingItemID:  bearingItemID,
		BearingQuality: bearingQuality,
		BearingFactor:  bearingFactor,
		OilItemID:      oilItemID,
		OilQuality:     oilQuality,
		OilWearFactor:  reelOilWearFactor(oilQuality, oilDurability),
		CastFriction:   reelCastFriction(bearingQuality, bearingDurability, oilQuality, oilDurability, durability),
	}
	v.CastEfficiency = 1 - v.CastFriction
	// 重算改装参数时不能清掉尚未跨档的润滑磨损进度。
	if old, err := s.DB.GetReelPerformance(ctx, row.GUID); err == nil && old != nil {
		v.WearProgress = old.WearProgress
	}
	return v, nil
}

// syncReelPerformance 重新计算并持久化一只轮子的基础/锁轮承力。
func (s *Server) syncReelPerformance(ctx context.Context, row *store.Item) (*store.ReelPerformance, error) {
	v, err := s.buildReelPerformance(ctx, row)
	if err != nil {
		return nil, err
	}
	if err := s.DB.UpsertReelPerformance(ctx, *v); err != nil {
		return nil, err
	}
	return v, nil
}

// RebuildReelPerformance 为现有轮子补齐/刷新物理参数表。
func (s *Server) RebuildReelPerformance(ctx context.Context) (int, error) {
	rows, err := s.DB.SQL.QueryContext(ctx, `SELECT guid FROM item WHERE wire_class=?`, "dgjinoebdop")
	if err != nil {
		return 0, err
	}
	var guids []string
	for rows.Next() {
		var guid string
		if err := rows.Scan(&guid); err != nil {
			rows.Close()
			return 0, err
		}
		guids = append(guids, guid)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	count := 0
	for _, guid := range guids {
		row, rerr := s.DB.GetItemByGUID(ctx, guid)
		if rerr != nil || row == nil {
			continue
		}
		if _, rerr = s.syncReelPerformance(ctx, row); rerr != nil {
			return count, rerr
		}
		count++
	}
	return count, nil
}

// reelWasherRatedFromRow 返回当前安装摩擦片的额定制动力（零件额定 x 母轮工艺）。
// 磨损是按额定值的比例烧的，所以结算必须先拿到额定值；没有独立摩擦片子件时返回
// false，调用方回退到本体耐久通道。
func (s *Server) reelWasherRatedFromRow(ctx context.Context, row *store.Item) (float64, bool) {
	if row == nil {
		return 0, false
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return 0, false
	}
	_, def, _, found := s.selectedReelPart(ctx, fields, reelPartCatWasher)
	if !found || def == nil {
		return 0, false
	}
	rated := itemDefF32(def, gearAttrDragMax) * row.Workmanship
	if rated <= 0 {
		return 0, false
	}
	return rated, true
}

// reelPartStateFromRow 读取轮子当前内嵌零件的静态品质与自身耐久。没有该零件时按 100
// 品质、满耐久返回，兼容缺少旧版内嵌配置的历史物品；零件耐久字段缺失同样按满耐久
// （历史存档没写过该字段），值为 0 才是真的磨完/干涸。
func (s *Server) reelPartStateFromRow(ctx context.Context, row *store.Item, category int64) (itemID string, quality, durability float64) {
	itemID, quality = s.reelPartQualityFromRow(ctx, row, category)
	durability = 1
	if row == nil {
		return itemID, quality, durability
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return itemID, quality, durability
	}
	child, _, _, found := s.selectedReelPart(ctx, fields, category)
	if !found || child == nil {
		return itemID, quality, durability
	}
	// 字段缺失与 FLOAT 的 0 都按满耐久（历史存档没写过该字段），
	// 只有 U32 承载的 0 才是真的磨完/干涸。
	durability = reelChildDurability(child)
	return itemID, quality, durability
}

// reelOilStateFromRow 读取当前安装轮油的品质与自身耐久，供锁轮发热曲线折算油的保护力。
func (s *Server) reelOilStateFromRow(ctx context.Context, row *store.Item) (quality, durability float64) {
	_, quality, durability = s.reelPartStateFromRow(ctx, row, reelPartCatOil)
	return quality, durability
}

// reelLockedDragFromRow 按锁轮承力唯一算法算出这只轮子此刻能承受的公斤数：读的是已磨损
// 的摩擦片实例与当前零件耐久。搏鱼裁决、抛投裁决与性能表都必须走它，否则会出现在
// "摩擦片磨了但力度上限不动"的两套数字（满红不爆只修好一半）。
func (s *Server) reelLockedDragFromRow(ctx context.Context, row *store.Item) float64 {
	if row == nil {
		return 0
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return 0
	}
	_, bearingQuality, bearingDurability := s.reelPartStateFromRow(ctx, row, reelPartCatBearing)
	_, gearQuality, gearDurability := s.reelPartStateFromRow(ctx, row, reelPartCatGear)
	return s.reelEffectiveLockedDragInFields(ctx, row, fields, s.reelBaseDragFromRow(ctx, row), row.Durability,
		reelBearingFactor(bearingQuality, bearingDurability),
		reelGearFactor(gearQuality, gearDurability))
}
