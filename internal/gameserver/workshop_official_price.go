package gameserver

import (
	"context"
	"math"

	"rf4server/internal/proto"
)

// 官服工坊的计价口径（2026-11-12 定案，证据见 docs/official-protocol-workshop.md）。
//
// 把 logs/nc.jsonl 里同一单的完整链路（21/2 报价 → 21/4 下单 → 2/1 余额 →
// 4/22 刷新）里五件零件全部解出来后，官服模型是这样：
//
//	hlopebmchhp.f3  = 手头旧件的**残值**（折抵额），不是修理费
//	hpopgbjohpk.f2  = 新件售价
//	hpopgbjohpk.f3  = 该品类的**固定工时**（小时）
//	21/4 请求 f3    = Σ(新件价 − 旧件残值)   ← 客户端按这个报价，官服按这个收
//
// 那一单（齿轮 rgm_ef_spark10021，完好度 0.999487，新件 296 银 5 金）：
//
//	21/2 f3 = (266, 0)                      → 296×0.9=266（金币列官方就是 0）
//	21/4 客户端报 30                        → 296−266=30
//	2/1 银币 5000→4970                      → 实扣 30，与报价一分不差
//	21/4 回包锁 {workshop_01,1,597,120}     → 120 分钟 = 该件 hpopgbjohpk.f3=2 小时
//	下单后该件 GUID 变了、完好度 1.0        → 官服的"修理"就是换一个同型号新件
//
// 他四件的残值率：轴承 324/845、线轴 239/624、摩擦片 289/754 都是 0.383，
// 而它们的完好度都在 0.9999 附近——说明残值率与磨损无关，是品类属性。
//
// 旧实现把 f3 当"修理费"、按 round(磨损×500) 发，同型号修理也只收这个数，
// 于是客户端按"新件价 − 0"算全价、服务端只收几银：实测界面报 2197 / 实扣 3、
// 界面报 3756 / 实扣 62。两边改成同源之后，界面与实扣必然一致。
const (
	// tunableWorkshopWearResidualRate 是易损件（轴承/线轴/摩擦片）的
	// 旧件残值率：残值 = 新件标价 × 该系数。官服实测 0.383。
	tunableWorkshopWearResidualRate = "workshop_wear_part_residual_rate"
	// tunableWorkshopOilResidualRate 是润滑油（cat 10）的旧件残值率。
	// 官服 21/2 里轮油那一槽的 f3 就是 **(0, 0)** —— 旧油不折抵，客户端
	// 按新件全价报价，我们也必须按全价收（详见 workshopPartResidual 注释）。
	tunableWorkshopOilResidualRate = "workshop_oil_residual_rate"
	// tunableWorkshopGearResidualRate 是主动齿轮的残值率。官服实测 266/296 = 0.9。
	tunableWorkshopGearResidualRate = "workshop_gear_residual_rate"
	// tunableWorkshopDurableResidualRate 是上表没列出的品类（竿件/渔轮本体）的
	// 残值率。它们与齿轮同性质（结构件/整机），默认也取 0.9。
	tunableWorkshopDurableResidualRate = "workshop_durable_residual_rate"
	// tunableWorkshopDefaultHours 是工时表里没列出的品类的默认工时（小时）。
	tunableWorkshopDefaultHours = "workshop_default_repair_hours"
)

const (
	workshopWearResidualRateDefault    = 0.383
	workshopGearResidualRateDefault    = 0.9
	workshopDurableResidualRateDefault = 0.9
	// 旧油不折抵：官服 21/2 第五槽（轮油）f3 = (0,0)。
	workshopOilResidualRateDefault    = 0.0
	workshopDefaultRepairHoursDefault = 24.0
)

// residualRateSpec 把"哪个 tunable 管这个品类、缺省值多少"放在一张表里。
type residualRateSpec struct {
	key string
	def float64
}

// workshopResidualRateTunables 是**官服实测到**的残值率，只收有抓包依据的
// 品类，不猜：
//
//	轴承 bearings ef-sp_001_bb  324/845 = 0.383
//	线轴 ef_spool_spark200011   239/624 = 0.383
//	摩擦片 ef-fb-sp.orig-5803   289/754 = 0.383
//	齿轮 rgm_ef_spark10021      266/296 = 0.9
//	轮油（第五槽 0edd4e21…）    0/2706  = 0   ← 旧油不折抵
//
// 竿件（竿身 3 / 竿柄 4 / 导环 5 / 竿坯 36）、渔轮本体 6 官服那单没出现，
// 走 tunableWorkshopDurableResidualRate 这个"耐用件"默认档：它们和齿轮一样
// 是结构件/整机，不是轴承油封那类易耗品。拿到新抓包直接往这张表里加。
var workshopResidualRateTunables = map[int64]residualRateSpec{
	7:  {tunableWorkshopWearResidualRate, workshopWearResidualRateDefault}, // 轴承
	8:  {tunableWorkshopWearResidualRate, workshopWearResidualRateDefault}, // 线轴/线杯
	9:  {tunableWorkshopWearResidualRate, workshopWearResidualRateDefault}, // 摩擦制动器
	10: {tunableWorkshopOilResidualRate, workshopOilResidualRateDefault},   // 润滑油：旧油不折抵
	52: {tunableWorkshopGearResidualRate, workshopGearResidualRateDefault}, // 主动齿轮
}

// workshopPartHoursByCat 是官服 21/2 的 hpopgbjohpk.f3：每个品类固定的
// 银币修理工时（小时）。数值直接来自 nc.jsonl 那一单五件的原值，且回包的
// 锁 RemainMin=120 与齿轮那 2 小时严格吻合。
//
// 竿件（4/5/36）官服抓包里没有样本，落到 tunableWorkshopDefaultHours。
//
// 注意这一格**是逐件**的，不是逐品类的：官服 21/2 的轮油槽里 21 种油，
// 有银币价的（syb 2706 银 / sat 6039 银 …）f3=18，纯金币价的
// （blg 49 金 / m_au 165 金 …）f3=0——因为 f3 只约束银币路径，金币即时。
// 我们这边轮油统一按银币计价，所以统一走 18 这一档；哪天把油价改成金币
// 计价，就得跟着把这一格改成按件取值。
var workshopPartHoursByCat = map[int64]uint32{
	52: 2,  // 主动齿轮
	7:  36, // 轴承
	8:  36, // 线轴/线杯
	9:  24, // 摩擦制动器
	10: 18, // 润滑油
}

// workshopPartResidualRate 返回该品类旧件的残值率（残值 / 新件标价）。
func (s *Server) workshopPartResidualRate(cat int64) float64 {
	spec, ok := workshopResidualRateTunables[cat]
	if !ok {
		spec = residualRateSpec{tunableWorkshopDurableResidualRate, workshopDurableResidualRateDefault}
	}
	rate := s.tunableF(spec.key, spec.def)
	if rate < 0 {
		rate = 0
	}
	if rate > 1 {
		rate = 1
	}
	return rate
}

// workshopPartResidual 把新件标价折算成旧件残值（银, 金）。
//
// **只折银币列，金币列固定 0**。官服样本里三件的金币残值是 0 / 2 / 2（对应新件
// 金币价 5 / 8 / 7），既不等于 `round(金币价 × 残值率)`（5→5、8→3、7→3），
// 也不等于截断或任何单一比例 —— 规则还没解出来，样本只有三个小数字。
//
// 为什么先发 0 而不是套同一系数：客户端拿 `hpopgbjohpk.f2 − 这一格` 算报价。
// 齿轮新件含 5 金，套 0.9 折出来 4.5→5，客户端算出的金币报价就是 **0 金**，
// 而我们的零金额分支（`costGold <= 0`）会拒掉它 —— 玩家点金币修理只会看到
// 「发生内部错误」。发 0 则金币报价 = 新件金币价，跟官方那一单的实收一致
// （官服这单 5 金一分没扣、只扣了 30 银）。易损件的金币列会因此比官服贵 2 金，
// 但金币修理本来就是次要路径，宁可贵一点也不能变成报错或白送。
//
// 银币列是主路径且已逐单核对过（界面 30 / 实扣 30），必须严格按残值率折。
//
// 轮油是个特例，残值率 0：官服那一单 21/2 的第五槽（f0=0edd4e21…，轮油）
// f3 = **(0, 0)**，21/4 里客户端也只按新件价报。我们原来把轮油并进轴承那档
// 0.383，结果 21/2 发 f3=19、客户端仍报全价 50 → 换件报价校验不通过 →
// 玩家点「更换」当场弹「发生内部错误」（2026-11-12 03:28~03:29 实测三单）。
// 旧油本来也没有折抵价值，口径与官服一致才是对的。
func (s *Server) workshopPartResidual(cat, silver, gold int64) (int64, int64) {
	rate := s.workshopPartResidualRate(cat)
	rs := int64(math.Round(float64(silver) * rate))
	if rs < 0 {
		rs = 0
	}
	return rs, 0
}

// workshopPartHours 返回该品类的固定修理工时（小时）。
//
// 21/2 的 hpopgbjohpk.f3 与银币排队任务的锁时长都必须走这一个函数：客户端
// 界面上标的「N 小时」读的就是它，两处不同源会让玩家看到的小时数和实际
// 等待时间对不上。
func (s *Server) workshopPartHours(cat int64) uint32 {
	if h, ok := workshopPartHoursByCat[cat]; ok {
		return h
	}
	h := s.tunableF(tunableWorkshopDefaultHours, workshopDefaultRepairHoursDefault)
	if h < 1 {
		h = 1
	}
	return uint32(math.Round(h))
}

// partRepairBasePrice 返回"换一个同型号新件"的标价（银, 金），也就是客户端
// 在 21/2 里看到、并被它拿来减残值的那一格 f2。
//
// 零件本身通常没有 item_price 行，partPrice 会继续回退到 attrs 推导和
// partBasePrice 表，与 21/2 里 hpopgbjohpk.f2 用的是同一条链路，所以两边
// 算出来的新件价一定相同。
//
// storeID / mapKey 用来乘该店铺该**部位**的维修倍率（见 repair_part_mult.go）。
// 21/2 与 21/4 必须传同一组值，否则客户端拿到的 f2 和服务端结算用的价不是
// 一个数，报价校验会拒单。传空串等于不乘倍率（测试和拿不到工坊 id 的兜底路径）。
func (s *Server) partRepairBasePrice(ctx context.Context, storeID, mapKey string, p repairPart) (int64, int64) {
	if p.NID != 0 {
		if def, err := s.DB.GetItemDef(ctx, int64(p.NID)); err == nil && def != nil && def.ItemID != "" {
			return s.partPriceForStore(ctx, storeID, mapKey, def.ItemID, def.Category)
		}
	}
	return s.partPriceForStore(ctx, storeID, mapKey, "", int64(p.Cat))
}

// childNumericID 取子对象的 numeric_id。同型号"修理"要按该件自己的新件标价
// 和残值计费，不能用手头其它件的价格。
func childNumericID(v *proto.Value) uint32 {
	if v == nil || v.Kind != proto.KindObj {
		return 0
	}
	if idx := fieldIdxByCS(v.Class, csNumericID); idx >= 0 && idx < v.FieldCount() {
		return uint32(u64Of(v.Fields[idx].Num))
	}
	return 0
}
