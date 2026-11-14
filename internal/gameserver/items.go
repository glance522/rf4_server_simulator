package gameserver

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// ---- 物品类别到 wire 类的映射 ----

type wireClassEntry struct {
	Class  string
	TypeID int64
}

// categoryWireClass 按客户端类别枚举选择 wire 类。
// 13/14/15 为前导、31 为食笼、44 为 Sbirolino、46 为成品窝料，
// 48..54 为渔轮子部件，59/60 为打窝器和弹弓。
var categoryWireClass = map[int64]wireClassEntry{
	3:  {"cnolibojkao", 157},
	4:  {"hgabifpnhmk", 156},
	5:  {"knocoeggfdk", 159}, // 导环（spin 竿实例使用 rcs_basic_spin）
	6:  {"dgjinoebdop", 149},
	7:  {"oiebifopill", 146},
	8:  {"lcajgpjolbo", 151},
	9:  {"loedbndnill", 147},
	10: {"plefjjahdgh", 150},
	11: {"ohciikgjghe", 140},
	12: {"agedjipcnpe", 131},
	13: {"oldfpclaabo", 137}, // 前导（f1/f2 钢丝/配件子实例物化）
	14: {"donkbnnmgjm", 139}, // 前导锁/配件
	15: {"cjigljdikhe", 138}, // 钢丝/编织前导
	17: {"ioombppcnlh", 141}, // 拟饵（f0 gpoaffnjbdo=args(60) 出厂钩数组）
	18: {"bigdipenecj", 124},
	22: {"hcahlnccaek", 114},
	23: {"blpddffbgjg", 115},
	25: {"nffjdjfkjem", 118},
	26: {"aafdnnmleob", 128},
	27: {"kbnbcppjkaf", 100},
	28: {"gjdmdcpoifh", 101},
	29: {"igjimikeadh", 161},
	31: {"cdgdbeoejeo", 119}, // 食笼
	34: {"bndpebpblfk", 148}, // 握丸（13 字段 reader；渔轮 f5 必需）
	35: {"dbmbnkpkafb", 164},
	36: {"kjggkpflhjp", 155},
	37: {"gfkbfjgkjoh", 116},
	39: {"bplepbfgpmk", 162},
	40: {"dbojlbebaca", 143},
	41: {"jceogpbfpjh", 163}, // 工坊工具；定义类型 12056 夹在票据实例 162 与露营灯 164 之间
	42: {"nacllfpgomk", 142}, // 工坊材料，可堆叠；定义类型 12026 夹在拟饵 141 与地图 143 之间
	44: {"fcdgfdomnjm", 160}, // Sbirolino
	46: {"cilmgoaoofg", 120}, // 成品窝料
	47: {"eebdgfednim", 700}, // 铃铛/咬钩信号器 dadgbbiijki
	57: {"fmaajjlcdmc", 167}, // 铅芯线（客户端 emfindpkead；f0=强度）
	48: {"npnkhkicfkg", 613}, // 渔轮导线环
	49: {"bnfpgceodij", 616}, // 制动钮
	50: {"jjikombcnnj", 614}, // 轮手柄
	51: {"gaoapgffamm", 615}, // 星形制动
	52: {"ibinolcnogf", 610},
	53: {"phdkhaocdai", 611},   // 齿轮 LED
	54: {"gpldfemlfch", 612},   // 蜗杆轴
	59: {"iknkgeomkip", 3200},  // Cobra 打窝器
	60: {"ifnaghdaolg", 3201},  // Slingshot 弹弓
	79: {"abmokghdbhf", 11500}, // 竿架（rod pod），客户端模型 pphmpibfgea
	// 提取物/蘸饵（dip）与竿架同属 UI 家族 dfignppokoh，但走 nledogicnfe 变体：
	// 鱼饵类条目在背包列表排序比较器里被强转成 nledogicnfe（hook 栈实证
	// InvalidCastException 'pphmpibfgea'->'nledogicnfe'，鱼饵筛选必现、
	// 背包里有两份小药时普通 I 必现），小药 UI 是家族的
	// iaphgclhemo<nledogicnfe,...> 实例化（挂 lillemolakn 的 dip 探针两轮零命中）。
	// 早期落成钓线类会卡死整个背包（Player.log 456 次 cast to pphmpibfgea 实证）。
	// nledogicnfe 比 pphmpibfgea 多 f0 hmfmpbemjkg（父实例 guid，发空），
	// wire 类 bpmmimcmpof/721，其余 14 个字段与 abmokghdbhf 同名同 op。
	58: {"bpmmimcmpof", 721},
	// 香精（fplt_*、feedadds_*，cat 19）：实例类 deckldefiap/125（DTO
	// ifgjaajoofo : aklijhgdgbh<bmalnngjeho>，饵料 UI 家族 dfignppokoh 的
	// camnheeojef 变体），扁平 16 字段（名称 f0、颜色 f1、总数 f2
	// ljnnpokapig、guid f3、父容器 f4、容器枚举 f5、数字 id f6、工艺 f7、
	// 耐久 f8、创建 f9、更新 f10、状态位 f11、时效 f12、促销 f13、
	// 剩余数量 f14 dnmfciolgep、掩码 f15）。2026-11-05 逐一隔离实证：cat 19
	// 落窝料类 cilmgoaoofg 毒死鱼饵页，落 deckldefiap 不转圈。def 的
	// ninlajclahd（香味强度）/hkgndoipdlc（剂量）不上 wire，客户端从 def
	// 模板读。注意 46 成品窝料仍走 cilmgoaoofg（未实测，买时需验证）。
	19: {"deckldefiap", 125},
	// 添加剂（aroma_*/cb_*/op_*，cat 20）与香精是两个 wire 类：客户端
	// danefpkhooj.mcjpaeakaep 按品类枚举 oinpmjophkh 分发（case=cat-2），
	// case 18 对 cat 20 整袋 Cast<kgjoemjjadb>，cat 19 的 ifgjaajoofo 一进
	// 袋就炸（2026-11-05 扫货 aroma/cb_attr/op_attr 后鱼饵页复现转圈；
	// 同日 Player.log 同源异常 nledogicnfe->bebejlknhka 实证该分发表）。
	// 126=dpghngjmnap，DTO kgjoemjjadb : aklijhgdgbh<aljlmaepbih>，14 字段
	// 无名称串，权威数量槽 f12 dnmfciolgep、UI 镜像 f0 ljnnpokapig。
	20: {"dpghngjmnap", 126},
	// PVA 材料/线（cat 45）：同表 case 43 期望 bebejlknhka
	// （aklijhgdgbh<nmebhablank>），wire 类 pcngolccead/144，字段布局与 126
	// 相同。此前按 def 画像推断落 bpmmimcmpof/721（nledogicnfe 实为 cat 58
	// 提取物的类），商店渲染 box_hvz_pva_* 礼盒内容袋时 Cast<bebejlknhka>
	// 必炸（Player.log 22:51–23:01 共 14 次 InvalidCastException 实证）。
	45: {"pcngolccead", 144},
	// 以下映射取自 mcjpaeakaep 分发表的 cat→DTO 配对（每类 DTO 在 tables.go
	// 唯一对应一个 wire 类），取代兜底类；均未实测，购买后按鱼饵页/背包渲染验证。
	2:  {"ecknbkpeeji", 25},    // 鱼护（case 0 lmeocpochih）
	30: {"fgbnhjmpdmd", 145},   // 干饵/重量饵（case 28 fadkdmoigli）
	33: {"oplokpidmdi", 129},   // 水晶珠（case 31 aiecmhcnioo）
	38: {"nikamphcfoe", 127},   // spod 炸弹（case 36 chffnbpekig）
	55: {"coacllgmnml", 3100},  // 礼包（case 53 niodmhfbkag）
	61: {"cddbbllbjim", 3103},  // case 59 mdagojpmfba
	62: {"omkmkofjhcf", 12600}, // case 60 kmlgiiefbmh
	63: {"dbjjakbbbfc", 12601}, // case 61 bepeekbhdck
	69: {"hmojlejoofg", 12702}, // case 67 hfjfcadnmjh
	70: {"lligmmmgllg", 12703}, // case 68 ddhcineacka
	71: {"focdogajjcc", 13001}, // case 69 lcadlijehco
	72: {"enikeammfen", 2015},  // case 70 iidloljandl
	// 梦鱼优惠券（cat 74）此前没有映射，prepareNewItem 回落到钓线兜底类
	// ohciikgjghe/140，客户端按钓线 DTO 强转，标本制作商店的材料列表里
	// 找不到这张券，制作无法进行。定义类 fpcfjngdcho/13015 的域字段
	// fopbhlmkcpe/ppiooalfgii/eiofdcjlaai 与 coupon_chuchelo.attrs_json
	// 完全吻合，其配对实例类为 ahgeldclmdj/13018（同族锚点：cat76→13041）。
	74: {"ahgeldclmdj", 13018}, // case 72 mpjcnoimibo：梦鱼优惠券
	76: {"fdnngkgnmon", 13041}, // case 74 ghikepgmojg
	78: {"dapkdcghfmg", 11301}, // case 76 ipdpoaiekem
	82: {"ckfmndabaei", 11304}, // imdcjllgdcn 卡册
	80: {"ipdakkbfoja", 13301}, // 技能书（客户端按 wrapper 承载书目配置）
	81: {"kbpmmclfipg", 1470},  // 渔轮电机（装在轮 f14 扩展槽，DTO odlknjlbooh）
}

const fallbackWireClass = "ohciikgjghe"
const fallbackTypeID = 140

// resolveWireClass 实例类解析的统一入口（当前=categoryWireClass 直查；
// 曾在此按 def 剂量 hkgndoipdlc 给香精分流到窝料类，已被隔离实验证伪——
// 剂量与所属实例族无关，保留函数作为将来按 def 细分时的扩展点）。
func resolveWireClass(def *store.ItemDef) (wireClassEntry, bool) {
	cls, ok := categoryWireClass[def.Category]
	return cls, ok
}

// couponCategory 梦鱼优惠券（标本制作商店的免单券，一次制作消耗一张）。
//
// 券**不入** countableCategories：它的实例类 ahgeldclmdj 只有 dnmfciolgep
// （剩余数量），没有客户端 UI 数量槽 ljnnpokapig，整个票券族（cat 39/55/74/76）
// 都没有该槽，所以一张券堆成一行永远显示不出「还剩几张」。改为「一张券一个
// 实例」：每行 dnmfciolgep 恒为 1，发放 N 张就建 N 行，制作一次删一行——客户端
// 按 GUID 逐件渲染，玩家看到的就是 N 张卡（实机两行=两张卡已验证）。
const couponCategory = 74

// 客户端计件类别（可堆叠合并）；钓线(11)/前导(15) 同名字段是长度 m；浮漂(28) 显式非计件。
var countableCategories = map[int64]bool{
	14: true, 18: true, 19: true, 20: true, 26: true, 27: true,
	29: true, 30: true, 42: true, 45: true, 46: true, 58: true,
}

// fieldIdxByCS 按 cs 名（描述符属性名）定位字段 idx。
func fieldIdxByCS(class, csName string) int {
	meta := wiregen.ClassMeta[class]
	for _, fi := range meta.Fields {
		if fi.Name == csName {
			return fi.Idx
		}
	}
	return -1
}

// fieldOpAt 类的第 idx 个 op。
func fieldOpAt(class string, idx int) string {
	ops := wiregen.ClassOps[class]
	if idx < 0 || idx >= len(ops) {
		return ""
	}
	return ops[idx]
}

// metaQtyFamily 数量语义相同的实例类：权威槽都是 meta 基类的剩余数量槽
// dnmfciolgep，ljnnpokapig 只是总数/UI 计数镜像。dfignppokoh 家族的竿架
// abmokghdbhf/pphmpibfgea、提取物 bpmmimcmpof/nledogicnfe；aklijhgdgbh
// 家族的香精 deckldefiap/ifgjaajoofo、添加剂 dpghngjmnap/kgjoemjjadb、
// PVA pcngolccead/bebejlknhka。
func metaQtyFamily(class string) bool {
	return class == "abmokghdbhf" || class == "bpmmimcmpof" || class == "deckldefiap" ||
		class == "dpghngjmnap" || class == "pcngolccead" ||
		class == "ahgeldclmdj" // 梦鱼优惠券：13 字段 meta 基类，权威数量槽 dnmfciolgep
}

// itemQtyFieldIdx 可堆叠数量槽 idx。多数实例类的数量字段就叫 ljnnpokapig；
// metaQtyFamily 各类（竿架/提取物/香精/添加剂/PVA）的权威槽是 meta 基类
// dnmfciolgep —— bpmmimcmpof/deckldefiap/dpghngjmnap/pcngolccead 虽然也有
// ljnnpokapig，但那只是总数/UI 计数镜像。
func itemQtyFieldIdx(class string) int {
	if metaQtyFamily(class) {
		return fieldIdxByCS(class, "dnmfciolgep")
	}
	return fieldIdxByCS(class, csQty)
}

// qtyMirrorIdx 家族类的 UI 计数镜像槽：abmokghdbhf=f0 miecmmbpolk
// （lillemolakn.medpgjlibog 读），bpmmimcmpof=f1 ljnnpokapig（总数槽），
// deckldefiap=f2 ljnnpokapig（总数槽），dpghngjmnap/pcngolccead=f0
// ljnnpokapig（14 字段布局，无名称串）。
func qtyMirrorIdx(class string) int {
	switch class {
	case "abmokghdbhf":
		return fieldIdxByCS(class, "miecmmbpolk")
	case "bpmmimcmpof", "deckldefiap", "dpghngjmnap", "pcngolccead":
		return fieldIdxByCS(class, csQty)
	}
	return -1
}

// itemQtyValue 读取实例数量槽；没有数量字段时返回 false。
func itemQtyValue(class string, fields []proto.Field) (uint64, bool) {
	idx := itemQtyFieldIdx(class)
	if idx < 0 || idx >= len(fields) || fields[idx].Num == nil {
		return 0, false
	}
	return u64Of(fields[idx].Num), true
}

// setItemQty 写入实例数量槽；metaQtyFamily 家族的 UI 计数镜像槽
// （qtyMirrorIdx：abmokghdbhf=f0 miecmmbpolk——lillemolakn.medpgjlibog 读，
// bpmmimcmpof/deckldefiap/dpghngjmnap/pcngolccead=f ljnnpokapig）两处一起写。
func setItemQty(class string, fields []proto.Field, qty uint64) bool {
	idx := itemQtyFieldIdx(class)
	if idx < 0 || idx >= len(fields) {
		return false
	}
	fields[idx].Num = ptrU64(qty)
	if m := qtyMirrorIdx(class); m >= 0 && m < len(fields) {
		fields[m].Num = ptrU64(qty)
	}
	return true
}

// writeItemQty 写入新构造实例的数量字段。
func writeItemQty(class string, obj *proto.Value, qty uint64) {
	_ = setItemQty(class, obj.Fields, qty)
}

// fieldIsFloat 该位置的 op 是否浮点。
func fieldIsFloat(class string, idx int) bool {
	op := fieldOpAt(class, idx)
	return op == "FLOAT" || op == "DOUBLE"
}

// ---- 物品树重建 ----

// alignFieldsToOps 将字段快照对齐到类的 op 数量。
// 数量一致时原样返回，缺失字段补零；数量不符时按身份字段重建，
// 避免截断后把值写入错误的 op 类型。
func alignFieldsToOps(it *store.Item, fields []proto.Field) []proto.Field {
	ops := wiregen.ClassOps[it.WireClass]
	if len(ops) == 0 {
		return fields
	}
	if len(fields) == len(ops) {
		return fields
	}
	if len(fields) < len(ops) {
		out := make([]proto.Field, len(ops))
		copy(out, fields)
		for i := len(fields); i < len(ops); i++ {
			zf, err := proto.ZeroField(ops[i])
			if err != nil {
				return fields
			}
			out[i] = zf
		}
		return out
	}
	// 字段数量不符时按身份字段重建。
	out := make([]proto.Field, len(ops))
	for i, op := range ops {
		zf, err := proto.ZeroField(op)
		if err != nil {
			return fields
		}
		out[i] = zf
	}
	for i, op := range ops {
		switch op {
		case "GUID16":
			if out[i].Hex == "00000000000000000000000000000000" && it.GUID != "" {
				out[i].Hex = it.GUID
			}
		case "FLOAT", "DOUBLE":
			if cs := fieldCSName(it.WireClass, i); cs == "hjilekjlgbk" {
				out[i].Float = it.Workmanship
			} else if cs == "nmidlgejlcp" {
				out[i].Float = it.Durability
			}
		}
	}
	if idx := fieldIdxByCS(it.WireClass, csNumericID); idx >= 0 && idx < len(out) && out[idx].Num != nil {
		out[idx].Num = ptrU64(uint64(it.NumericID))
	}
	return out
}

// fieldCSName 类的第 idx 个字段 cs 名（无则空串）。
func fieldCSName(class string, idx int) string {
	meta := wiregen.ClassMeta[class]
	for _, fi := range meta.Fields {
		if fi.Idx == idx {
			return fi.Name
		}
	}
	return ""
}

// ItemToTree 由 PG item 行重建完整物品树（4/5、14/2、4/22 共用）。
func (s *Server) ItemToTree(ctx context.Context, it *store.Item) (*proto.Value, error) {
	fields, err := FieldsFromJSON(it.FieldsJSON)
	if err != nil {
		return nil, err
	}
	fields = alignFieldsToOps(it, fields)
	// 竿架竿位设置兜底：存量竿架没有 f1 时客户端建不出竿位设置，
	// 电子报警器不会亮也不会响（编码格式见 rodpod.go）。
	if rodPodInjectSlots && it.WireClass == rodPodWireClass {
		ensureRodPodSlots(it, fields)
	}
	obj := proto.Obj(it.WireClass, uint32(it.TypeID), fields...)

	class := it.WireClass
	if class == deckClass {
		cards, err := cardsInDeck(it)
		if err != nil {
			return nil, err
		}
		obj.Fields[0].Val = tradingCardList(cards...)
	}

	// 状态位零值兜底（存量行为 0 → 客户端当未初始化）
	def, derr := s.DB.GetItemDef(ctx, it.NumericID)
	if derr != nil {
		def = nil
	}
	if def != nil {
		stateFlagsZeroFill(class, obj, def)
		// 先补齐嵌套子配置，再按定义+条件统一投影物理字段。这里不是“零值兜底”：
		// 强度、曳力、成色、容器枚举都由权威数据推导，保证发给客户端的树一致。
		s.materializeInto(ctx, class, def, obj, it.GUID, it.Workmanship, it.Durability)
		s.canonicalizeItemPhysicalFields(ctx, it, obj.Fields)
		if err := s.initializeTradingItem(ctx, def, obj); err != nil {
			return nil, err
		}
	}
	// 鱼竿动态接管（竿上组件/钓组/挂线挂轮必须现算，快照过期会让插槽列表失真）
	if class == "cnolibojkao" {
		if err := s.rebuildRod(ctx, it, obj); err != nil {
			return nil, err
		}
	}
	// 损坏态投影：耐久耗尽的装备点亮客户端红框/折断渲染（只动 wire，不落库）。
	// 必须放在 rebuildRod 之后——竿的 hgaijieklek 会按定义基值重建，之前写入会被抹掉。
	applyBrokenItemState(class, obj, it.Durability)
	return obj, nil
}

// materializeInto 物化嵌套子配置：渔轮 f2..f14、前导 f1..f3、拟饵 f0 出厂钩。
// 只填充空槽，已有子对象的槽保持不变。
func (s *Server) materializeInto(ctx context.Context, class string, def *store.ItemDef, obj *proto.Value, guid string, workmanship, durability float64) {
	attrs := defAttrs(def)
	if class == "dgjinoebdop" {
		// 部分旧轮型只有顶层定义，没有嵌套六件；用同系列完整轮型做只读零件模板，
		// 否则这些轮子会绕过摩擦片、轴承、齿轮和轮油磨损。
		attrs = s.reelMaterializationAttrs(ctx, def)
	}
	if attrs == nil {
		return
	}
	fill := func(keys []string, isArgs bool) {
		subs := s.materializeSubObjects(ctx, attrs, keys, guid, workmanship, durability)
		for key, sub := range subs {
			idx := fieldIdxByCS(class, key)
			if idx < 0 || idx >= len(obj.Fields) {
				continue
			}
			if obj.Fields[idx].Val != nil && obj.Fields[idx].Val.Kind != proto.KindNull {
				continue
			}
			obj.Fields[idx].Val = sub
		}
	}
	switch class {
	case "dgjinoebdop": // 渔轮 f2..f14
		fill(reelPartKeys, false)
	case "oldfpclaabo": // 前导 f1 前导线、f2 连接件、f3 普通主线
		fill(leaderPartKeys, false)
		if idx := fieldIdxByCS(class, gearAttrLeaderIn); idx >= 0 && idx < len(obj.Fields) {
			if normalized, changed := normalizeLeaderInnerWire(obj.Fields[idx].Val); changed {
				obj.Fields[idx].Val = normalized
			}
		}
		// f0 强度取线材与连接件中的最弱值，再乘以成品工艺。
		if idx := fieldIdxByCS(class, csStrength); idx >= 0 && fieldIsFloat(class, idx) && obj.Fields[idx].Float == 0 {
			strength := 0.0
			for _, key := range []string{"mbnambppbom", "lbadjgllglo"} {
				cfg, _ := attrs[key].(map[string]any)
				if value := nestedF32(cfg, gearAttrMaxLoad); value > 0 && (strength == 0 || value < strength) {
					strength = value
				}
			}
			if strength > 0 {
				obj.Fields[idx].Float = strength * workmanship
			}
		}
	case "ioombppcnlh": // 拟饵 f0 = args(60) 出厂钩
		idx := fieldIdxByCS(class, "gpoaffnjbdo")
		if idx >= 0 && idx < len(obj.Fields) {
			if obj.Fields[idx].Val == nil || obj.Fields[idx].Val.Kind == proto.KindNull {
				if hooks := s.lureHooks(ctx, attrs, guid, workmanship, durability); hooks != nil {
					obj.Fields[idx].Val = hooks
				}
			}
		}
	}
}

// normalizeLeaderInnerWire 修复旧版本把主线对象类直接塞进前导 f1 的记录。
// cat11 与 cat15 的线材字段名基本一致，按字段名搬到 cat15 对象即可保留
// 真实外观 numeric_id、强度、线径、长度、耐久和实例 GUID。
func normalizeLeaderInnerWire(old *proto.Value) (*proto.Value, bool) {
	if old == nil || old.Kind != proto.KindObj {
		return old, false
	}
	target := categoryWireClass[gearCatLeaderWire]
	if old.Class == target.Class {
		return old, false
	}
	if old.Class != categoryWireClass[gearCatLine].Class {
		return old, false
	}
	obj, err := wiregen.MakeObj(target.Class, nil)
	if err != nil {
		return old, false
	}
	for _, targetField := range wiregen.ClassMeta[target.Class].Fields {
		sourceIdx := fieldIdxByCS(old.Class, targetField.Name)
		if sourceIdx >= 0 && sourceIdx < len(old.Fields) {
			obj.Fields[targetField.Idx] = old.Fields[sourceIdx]
		}
	}
	return obj, true
}

// itemStrengthForPush 返回要下发给客户端的受力上限。
//
// 自制引线（leader_crafted_*）必须走实例材料：定义里的内嵌线材与连接件是
// null，strengthBitsFor 只会得到 0，客户端拿到的就一直是制作时写进快照的旧值，
// 磨损不体现、服务端最弱环也看不见这根引线。这里按材料定义重算并乘当前耐久，
// 让两端用同一个数。耐久已耗尽时不改写，保留"损坏件不强写 0"的既有行为。
func (s *Server) itemStrengthForPush(
	ctx context.Context,
	def *store.ItemDef,
	it *store.Item,
	fields []proto.Field,
) (float64, bool) {
	if strength, ok := strengthBitsFor(def, it.Durability); ok {
		return strength, true
	}
	if def != nil && def.Category == gearCatLeader && it.Durability > 0 {
		base := s.craftedLeaderBaseStrengthFromFields(ctx, it.WireClass, fields)
		if base > 0 {
			durability := it.Durability
			if durability > 1 {
				durability = 1
			}
			return base * durability, true
		}
	}
	return 0, false
}

// canonicalizeItemPhysicalFields 是物品物理字段的统一投影入口。
//
// fields_json 保存物品身份和状态，但工艺、耐久、强度、刹车片曳力和容器枚举
// 由数据模型权威派生，不能被快照中的空值覆盖。新建和读取物品共用此函数。
func (s *Server) canonicalizeItemPhysicalFields(ctx context.Context, it *store.Item, fields []proto.Field) bool {
	if s.DB == nil || it == nil || len(fields) == 0 {
		return false
	}
	changed := false
	class := it.WireClass
	for _, pair := range []struct {
		cs string
		v  float64
	}{{csWorkmanship, it.Workmanship}, {csDurability, it.Durability}} {
		idx := fieldIdxByCS(class, pair.cs)
		if setWireFloat(fields, class, idx, pair.v) {
			changed = true
		}
	}
	if idx := fieldIdxByCS(class, csSlotEnum); idx >= 0 && fieldOpAt(class, idx) == "BYTE" {
		// 卡片属于专用收藏库存；背包/装备中的根物品 wire 容器枚举为 0，
		// 存入房屋的物品为 10——客户端物品管理器（eegjkjenhoe）按该枚举
		// 把物品挂进对应容器字典，恒 0 会让房屋仓库永远收不到物品。
		// container='rig' 是服务端归属，不是物品对象的 lmmhdlhogdc；
		// 只有母物品内部的嵌套子实例使用 rig(4)。
		kind := rootItemStorageKind(class)
		if it.Container == "house" {
			kind = 10
		}
		if u64Of(fields[idx].Num) != kind {
			fields[idx].Num = ptrU64(kind)
			changed = true
		}
	}

	def, err := s.DB.GetItemDef(ctx, it.NumericID)
	if err != nil || def == nil {
		return changed
	}
	// 渔轮顶部 f0 是客户端的受力/力度上限，不是传动比。优先使用当前
	// 安装的摩擦片制动力，缺失时才回退到出厂摩擦片；传动比只在齿轮
	// 子件自己的强度字段里出现，不能污染母轮上限。
	if class == "dgjinoebdop" {
		if s.canonicalizeReelStrength(ctx, it, fields) {
			changed = true
		}
	} else if strength, ok := s.itemStrengthForPush(ctx, def, it, fields); ok {
		idx := fieldIdxByCS(class, csStrength)
		if setWireFloat(fields, class, idx, strength) {
			changed = true
		}
	}
	// 渔轮刹车片子是嵌套实例，客户端使用它自己的 f0。其值必须来自当前
	// 安装摩擦片的定义，而不是母轮出厂摩擦片，否则重登后摩擦力会归位。
	if class == "dgjinoebdop" {
		sub, dragDef, _, ok := s.selectedReelPart(ctx, fields, 9)
		if ok && sub.Class == "loedbndnill" && dragDef != nil {
			for _, pair := range []struct {
				cs string
				v  float64
			}{{csWorkmanship, it.Workmanship}, {csDurability, it.Durability}} {
				si := fieldIdxByCS(sub.Class, pair.cs)
				// 刹车片耐久是零件自己的磨损数据（工坊修复/换件依赖它），
				// 只在缺失（nil/0）时用母轮耐久初始化，已有值必须保留。
				if pair.cs == csDurability && si >= 0 && si < len(sub.Fields) {
					if f := sub.Fields[si]; f.Num != nil && proto.F32FromBits(uint32(*f.Num)) > 0 {
						continue
					}
				}
				if setWireFloat(sub.Fields, sub.Class, si, pair.v) {
					changed = true
				}
			}
			// csDragForce 是持久化的磨损状态：字段已存在（含 0=已磨穿）必须原样
			// 下发，只在字段缺失（新物品/历史档）时用零件定义值初始化。旧实现
			// 每次下发都重算，会把磨损悄悄刷回满值并随启动修复写回数据库。
			if _, present, si := frictionWasherDrag(sub); !present && si >= 0 && si < len(sub.Fields) {
				force := itemDefF32(dragDef, gearAttrDragMax) * it.Workmanship
				if force > 0 && setWireFloat(sub.Fields, sub.Class, si, force) {
					changed = true
				}
			}
		}
	}
	if canonicalizeNestedItemContainers(class, fields) {
		changed = true
	}
	if s.canonicalizeNestedItemPhysicalFields(ctx, class, fields, it.Workmanship, it.Durability) {
		changed = true
	}
	return changed
}

// canonicalizeNestedItemPhysicalFields 将母物品内嵌子实例的强度、工艺和耐久
// 投影到客户端树。静态配置产生的子实例也必须具有非零强度。
func (s *Server) canonicalizeNestedItemPhysicalFields(ctx context.Context, class string, fields []proto.Field, workmanship, durability float64) bool {
	var objs []*proto.Value
	appendRange := func(start, end int) {
		for i := start; i <= end && i < len(fields); i++ {
			if v := fields[i].Val; v != nil && v.Kind == proto.KindObj {
				objs = append(objs, v)
			}
		}
	}
	switch class {
	case "cnolibojkao":
		appendRange(0, 2)
	case "dgjinoebdop":
		appendRange(2, 14)
	case "oldfpclaabo":
		appendRange(1, 2)
	case "ioombppcnlh":
		if len(fields) > 0 && fields[0].Val != nil {
			objs = append(objs, fields[0].Val.Items...)
		}
	}
	changed := false
	for _, obj := range objs {
		if obj == nil || obj.Kind != proto.KindObj {
			continue
		}
		if setWireFloat(obj.Fields, obj.Class, fieldIdxByCS(obj.Class, csWorkmanship), workmanship) {
			changed = true
		}
		// 子件耐久是零件自己的磨损数据（工坊修复/换件依赖它），
		// 只在缺失（nil/0）时用母物品耐久初始化，已有值必须保留。
		if si := fieldIdxByCS(obj.Class, csDurability); si >= 0 && si < len(obj.Fields) {
			cur := 0.0
			if f := obj.Fields[si]; f.Num != nil {
				cur = float64(proto.F32FromBits(uint32(*f.Num)))
			}
			if cur <= 0 && setWireFloat(obj.Fields, obj.Class, si, durability) {
				changed = true
			}
		}
		ni := fieldIdxByCS(obj.Class, csNumericID)
		if ni < 0 || ni >= len(obj.Fields) || obj.Fields[ni].Num == nil {
			continue
		}
		def, err := s.DB.GetItemDef(ctx, int64(u64Of(obj.Fields[ni].Num)))
		if err != nil || def == nil {
			continue
		}
		strength := 0.0
		strengthOK := false
		if def.Category == 52 {
			strength = itemDefF32(def, gearAttrRatio) * workmanship
			strengthOK = strength > 0
		} else {
			strength, strengthOK = strengthBitsFor(def, durability)
		}
		if strengthOK && strength > 0 {
			if setWireFloat(obj.Fields, obj.Class, fieldIdxByCS(obj.Class, csStrength), strength) {
				changed = true
			}
		}
	}
	return changed
}

// canonicalizeNestedItemContainers 把真实装配子实例的 base 容器统一为 rig(4)。
// 只处理明确由物品定义物化出来的嵌套对象，避免误改钓组/曲线等非物品树。
func canonicalizeNestedItemContainers(class string, fields []proto.Field) bool {
	changed := false
	setObj := func(obj *proto.Value) {
		if obj == nil || obj.Kind != proto.KindObj {
			return
		}
		si := fieldIdxByCS(obj.Class, csSlotEnum)
		if si >= 0 && fieldOpAt(obj.Class, si) == "BYTE" && u64Of(obj.Field(si).Num) != 4 {
			obj.Fields[si].Num = ptrU64(4)
			changed = true
		}
	}
	switch class {
	case "cnolibojkao":
		for i := 0; i < 3 && i < len(fields); i++ {
			setObj(fields[i].Val)
		}
	case "dgjinoebdop":
		for i := 2; i <= 14 && i < len(fields); i++ {
			setObj(fields[i].Val)
		}
	case "oldfpclaabo":
		for i := 1; i <= 2 && i < len(fields); i++ {
			setObj(fields[i].Val)
		}
	case "ioombppcnlh":
		if len(fields) > 0 && fields[0].Val != nil {
			for _, it := range fields[0].Val.Items {
				setObj(it)
			}
		}
	}
	return changed
}

// canonicalizeReelStrength updates only the reel root scale, leaving installed
// parts untouched. FieldsJSON must describe the currently selected parts.
//
// 轮根（nlmnpekipjc/csStrength）官方语义是"出厂满耐久轮身强度"规格，与摩擦片
// 拖力是两个独立字段（官方 4/22 实抓：轮根 126.35 = 规格表 127.94 × 轴承耐久
// 0.98779，比值 2.2~3.6× 摩擦片）。轮根规格表命中的轮子按
// root_spec × 轴承子件耐久 下发；摩擦片磨损只写摩擦片字段（147 子件 f0），
// 不再污染轮根。未收录的 ItemID 回退旧口径（摩擦片值）。
func (s *Server) canonicalizeReelStrength(ctx context.Context, it *store.Item, fields []proto.Field) bool {
	if it == nil || it.WireClass != "dgjinoebdop" {
		return false
	}
	if spec, ok := reelRootSpec(it.ItemID); ok {
		_, _, bearingDur := s.reelPartStateFromRow(ctx, it, reelPartCatBearing)
		return setWireFloat(fields, it.WireClass, fieldIdxByCS(it.WireClass, csStrength), spec*clampUnit(bearingDur))
	}
	reelRootMissOnce(func(msg string, keys ...any) { s.log.Warn(msg, keys...) }, it.ItemID)
	if strength, present := s.reelInstalledDragFieldFromRow(ctx, it); present {
		return setWireFloat(fields, it.WireClass, fieldIdxByCS(it.WireClass, csStrength), strength)
	}
	strength := s.reelBaseDragFromRow(ctx, it)
	if strength > 0 {
		return setWireFloat(fields, it.WireClass, fieldIdxByCS(it.WireClass, csStrength), strength)
	}
	def, err := s.itemDefForRow(ctx, it)
	if err != nil || def == nil {
		return false
	}
	if strength, ok := strengthBitsFor(def, it.Durability); ok {
		return setWireFloat(fields, it.WireClass, fieldIdxByCS(it.WireClass, csStrength), strength)
	}
	return false
}

// setWireFloat 写入 wire 浮点字段；兼容 FLOAT/DOUBLE 与 U32 承载 IEEE754 的
// registry 布局。返回是否真的改变，供一次性数据修复决定是否落库。
func setWireFloat(fields []proto.Field, class string, idx int, v float64) bool {
	if idx < 0 || idx >= len(fields) {
		return false
	}
	switch fieldOpAt(class, idx) {
	case "FLOAT", "DOUBLE":
		if fields[idx].Float == v {
			return false
		}
		fields[idx].Float = v
		return true
	case "U32":
		bits := math.Float32bits(float32(v))
		if uint32(u64Of(fields[idx].Num)) == bits {
			return false
		}
		fields[idx].Num = ptrU64(uint64(bits))
		return true
	default:
		return false
	}
}

// rebuildRod 鱼竿 idx {0,1,2,3,4,5,16,18} 动态重建。
func (s *Server) rebuildRod(ctx context.Context, it *store.Item, obj *proto.Value) error {
	def, err := s.DB.GetItemDef(ctx, it.NumericID)
	if err != nil {
		return nil // 无定义：保留快照
	}
	// 出厂部件 f0/f1/f2（attrs: ajkflmejnih/ofpkgghjmmf/ddbadcmdkcl，值是内嵌
	// 部件定义对象，systemID 在 nhjofmmmngl —— 不能当字符串取）
	slots := []string{"ajkflmejnih", "ofpkgghjmmf", "ddbadcmdkcl"}
	for i, attr := range slots {
		if i >= 3 {
			break
		}
		sysID, ok := rodPartSystemID(def, attr)
		if !ok || sysID == "" {
			continue
		}
		// 部件以独立子对象重建，GUID 由竿 GUID 和槽名稳定派生。
		// 状态位必须取竿配置中的基值，不能使用部件独立定义中的装配位，
		// 否则客户端会拒绝渔轮收线输入。
		child, cerr := s.buildFactoryChild(ctx, it, sysID, i)
		if cerr != nil || child == nil {
			continue
		}
		// 竿侧受力磨损把子件耐久持久化在 DB 字段树里（applyRodStressWear）：
		// 现建子件前先把已持久化的耐久合并回来，否则每次快照都会把磨损抹回满值。
		// 状态位/其余字段仍以现建为准，只合并耐久一项。
		if old := obj.Fields[i].Val; old != nil && old.Kind == proto.KindObj {
			if dur := reelChildDurability(old); dur < 1 {
				setReelChildDurability(child, dur)
			}
		}
		obj.Fields[i].Val = child
	}

	// f3 挂线 / f4 挂轮（DB rig_guid 反查优先，否则沿用实例已有值）
	if g, err := s.DB.GetItemGUIDByRigSlot(ctx, it.GUID, 11); err == nil && g != "" {
		obj.Fields[3].Hex = g
	}
	if g, err := s.DB.GetItemGUIDByRigSlot(ctx, it.GUID, 6); err == nil && g != "" {
		obj.Fields[4].Hex = g
	}

	// f5 钓组对象（153）——绝不能 null
	if obj.Fields[5].Val == nil || obj.Fields[5].Val.Kind == proto.KindNull {
		obj.Fields[5].Val = s.defaultRig153(ctx, it)
	} else {
		// 渲染级联动：存量竿的快照里可能还存着旧版下发的全开掩码 f9，
		// 原样回放会让登录后的安装弹窗仍列出全部挂法（弹窗由 153 直接
		// 驱动，14/2、4/22 走 liveRig153 已重算，这里兜住 4/5 存量回放）。
		applyRigBaitMountRender(obj.Fields[5].Val)
	}
	// 出厂部件必须同时出现在 f5 的零件列表里：客户端的工作坊/装配界面
	// 都按 f5 的 gcfcdnfmnbh 逐行渲染，缺了它们零件行就是空的。
	s.appendFactoryPartsToRig(ctx, obj)

	// 版本戳由实际装配/耐久修改推进。读取时变更版本会使客户端把未改动的
	// 水中鱼竿也当作新快照重建，切竿或同步背包不应改变搏鱼实体。
	// f18 状态位 = 定义 | 2048
	if idx := fieldIdxByCS("cnolibojkao", "hgaijieklek"); idx >= 0 {
		st := u64Of(obj.Fields[idx].Num)
		if defBits, ok := defAttrU64(def, "hgaijieklek"); ok {
			st = defBits
		}
		obj.Fields[idx].Num = ptrU64(st | 2048)
	}
	return nil
}

// appendFactoryPartsToRig 把出厂部件（f0..f2 内嵌子对象）补进 f5 的零件列表。
//
// f5 的 field[2] 是 gcfcdnfmnbh 列表（品类、guid、附加值），客户端工作坊
// 与装配界面都按它逐行渲染零件。只写 f0..f2 而不登记进 f5 时，零件行为空。
// 已存在的 guid 不重复添加；非竿或没有 f5 时直接返回。
func (s *Server) appendFactoryPartsToRig(ctx context.Context, obj *proto.Value) {
	if obj == nil || obj.Kind != proto.KindObj || obj.FieldCount() <= 5 {
		return
	}
	rig := obj.Fields[5].Val
	if rig == nil || rig.Kind != proto.KindObj || rig.FieldCount() <= 2 {
		return
	}
	arr := rig.Fields[2].Val
	if arr == nil {
		return
	}
	seen := map[string]bool{}
	for _, e := range arr.Items {
		if e == nil || e.Kind != proto.KindObj || e.FieldCount() < 2 {
			continue
		}
		if g := e.Fields[1].Hex; g != "" {
			seen[g] = true
		}
	}
	for i := 0; i < 3 && i < obj.FieldCount(); i++ {
		child := obj.Fields[i].Val
		if child == nil || child.Kind != proto.KindObj {
			continue
		}
		gidx := fieldIdxByCS(child.Class, csGUID)
		if gidx < 0 || gidx >= child.FieldCount() {
			continue
		}
		g := child.Fields[gidx].Hex
		if g == "" || seen[g] {
			continue
		}
		var cat uint64
		if nidx := fieldIdxByCS(child.Class, csNumericID); nidx >= 0 && nidx < child.FieldCount() {
			if def, err := s.DB.GetItemDef(ctx, int64(u64Of(child.Fields[nidx].Num))); err == nil && def != nil {
				cat = uint64(def.Category)
			}
		}
		arr.Items = append(arr.Items, wiregen.MustMakeObj("gcfcdnfmnbh", map[int]*proto.Value{
			0: proto.U32(uint32(cat)),
			1: proto.Guid(g),
			2: proto.U32(0),
		}))
		seen[g] = true
	}
}

// buildFactoryChild 出厂部件子实例（guid = md5(竿guid|槽名) 稳定派生）。
// 状态位使用竿定义中该槽配置的 hgaijieklek 基值。
// 必须在 applyDefAttrs 之后写入，否则会被定义属性覆盖。
func (s *Server) buildFactoryChild(ctx context.Context, rod *store.Item, sysID string, partIdx int) (*proto.Value, error) {
	def, err := s.DB.GetItemDefByItemID(ctx, sysID)
	if err != nil || def == nil {
		return nil, err
	}
	cls, ok := categoryWireClass[def.Category]
	if !ok {
		cls = struct {
			Class  string
			TypeID int64
		}{fallbackWireClass, fallbackTypeID}
	}
	slotNames := []string{"rod", "handle", "third"}
	sum := md5.Sum([]byte(rod.GUID + "|" + slotNames[partIdx]))
	childGUID := fmt.Sprintf("%x", sum)
	obj, err := wiregen.MakeObj(cls.Class, nil)
	if err != nil {
		return nil, err
	}
	setState := func(v uint64) {
		if idx := fieldIdxByCS(cls.Class, csStateEnum); idx >= 0 {
			obj.Fields[idx].Num = ptrU64(v)
		}
	}
	setGUID := func() {
		if idx := fieldIdxByCS(cls.Class, csGUID); idx >= 0 {
			obj.Fields[idx].Hex = childGUID
		} else if fieldOpAt(cls.Class, 0) == "GUID16" {
			obj.Fields[0].Hex = childGUID
		}
	}
	setNum := func(cs string, v uint64) {
		if idx := fieldIdxByCS(cls.Class, cs); idx >= 0 {
			obj.Fields[idx].Num = ptrU64(v)
		}
	}
	setFloat := func(cs string, v float64) {
		idx := fieldIdxByCS(cls.Class, cs)
		if idx < 0 {
			return
		}
		if fieldIsFloat(cls.Class, idx) {
			obj.Fields[idx].Float = v
			return
		}
		if fieldOpAt(cls.Class, idx) == "U32" {
			obj.Fields[idx].Num = ptrU64(uint64(math.Float32bits(float32(v))))
		}
	}
	setGUID()
	setNum(csNumericID, uint64(def.NumericID)) // numeric_id（基类 cs）
	setNum(csSlotEnum, 4)                      // 装配子件：容器为 rig(4)
	// 类前缀字段按 attrs 回填；状态位来源是**部件独立定义行**。
	s.applyDefAttrs(def, cls.Class, obj)
	base := u64Of(firstNonNil(obj.Fields, fieldIdxByCS(cls.Class, csStateEnum)).Num)
	if base == 0 {
		if defBits, ok := defAttrU64(def, "hgaijieklek"); ok {
			base = defBits
		}
	}
	// 只有竿坯增加 2048 出厂装配位；手柄和第三件保持部件定义态。
	if partIdx == 0 {
		base |= 2048
	}
	// 子部件必须继承母竿的工艺和耐久，否则客户端会显示“损耗 100%”，
	// 并将部件物理强度按 0 处理。
	setFloat(csWorkmanship, rod.Workmanship)
	setFloat(csDurability, rod.Durability)
	setState(base)
	// parent 指向竿
	if idx := fieldIdxByCS(cls.Class, csParent); idx >= 0 {
		obj.Fields[idx].Hex = rod.GUID
	} else if fieldOpAt(cls.Class, 1) == "GUID16" || fieldOpAt(cls.Class, 1) == "OPTGUID" {
		obj.Fields[1].Hex = rod.GUID
	}
	_ = setNum
	return obj, nil
}

func firstNonNil(fields []proto.Field, idx int) proto.Field {
	if idx >= 0 && idx < len(fields) {
		return fields[idx]
	}
	return proto.Field{}
}

// defaultRig153 在 f5 缺失时按竿型构造默认钓组。
// 钓线和渔轮放在 f3/f4 专用字段中，不进入 f5；沉底族不包含浮漂组件。
func (s *Server) defaultRig153(ctx context.Context, it *store.Item) *proto.Value {
	components := proto.Args(proto.S("81"))
	rigType := s.rigTypeOfItem(ctx, it)
	name := rigNameForType(rigType)
	depth := defaultDepthFor(name, rigType)
	if comps, err := s.DB.ListRigComponents(ctx, it.GUID); err == nil {
		for _, rc := range comps {
			if rc.GUID == "" {
				continue
			}
			if _, special := categorySlotField[rc.Slot]; special {
				continue // 钓线/渔轮：写竿的 f3/f4 专用字段，不进 f5
			}
			if bottomRigTypes[rigType] && rc.Slot == 28 {
				continue // 沉底族无浮漂槽
			}
			components.Items = append(components.Items,
				wiregen.MustMakeObj("gcfcdnfmnbh", map[int]*proto.Value{
					0: proto.U32(uint32(rc.Slot)),
					1: proto.Guid(rc.GUID),
					// 读取时再次规范化槽位索引。
					2: proto.U32(uint32(canonicalIndex(rc.Slot, rc.Extra, rigType))),
				}))
		}
	}
	clipM, _ := s.savedReelClipM(ctx, it.GUID)
	drag := defaultReelDragSetting
	if saved, ok := s.rodReelDragSetting(ctx, it.GUID); ok {
		drag = saved
	}
	speed := defaultReelSpeed
	if saved, ok := s.rodReelSpeedSetting(ctx, it.GUID); ok {
		speed = saved
	}
	return wiregen.MustMakeObj("gbbhdkefnkn", map[int]*proto.Value{
		0: proto.StrVal(name),
		1: proto.U32(uint32(rigType)),
		2: components,
		4: proto.U32(uint32(depth)),
		5: proto.U32(uint32(clipM)),
		6: proto.U32(uint32(drag)),
		7: proto.U32(uint32(speed)),
	})
}

// fillFeedUnitWeight 把单件克重写进窝料实例的重量字段（U32 槽承载 IEEE754 位模式，
// FLOAT 槽直接写浮点）。已有非零值不覆盖，返回是否发生变化。
// 用途：老库存实例是在「照抄定义」的年代生成的，重量恒为 0。
func fillFeedUnitWeight(wireClass string, fields []proto.Field, itemID string) bool {
	idx := fieldIdxByCS(wireClass, castAttrWaterWeight)
	if idx < 0 || idx >= len(fields) {
		return false
	}
	grams := groundbaitUnitGrams(itemID)
	if fieldIsFloat(wireClass, idx) {
		if fields[idx].Float != 0 {
			return false
		}
		fields[idx].Float = grams
		return true
	}
	if u64Of(fields[idx].Num) != 0 {
		return false
	}
	fields[idx].Num = ptrU64(uint64(math.Float32bits(float32(grams))))
	return true
}

// applyDefAttrs 将 item_def.attrs_json 回填到对象；浮点值使用 IEEE754 位模式。
func (s *Server) applyDefAttrs(def *store.ItemDef, class string, obj *proto.Value) {
	if def.AttrsJSON == "" {
		return
	}
	var attrs map[string]any
	if err := json.Unmarshal([]byte(def.AttrsJSON), &attrs); err != nil {
		return
	}
	// 渔轮电机（kbpmmclfipg/1470）的功率与最高速度在定义里叫
	// afmboidbbaj / fgkfkgimfgo，wire 类字段叫 npnlhobgfen / mbbdpnidheg，
	// 名字对不上，直接按别名映射；这两个值是 f32 位模式，走下面的 float 分支。
	if class == "kbpmmclfipg" {
		if v, ok := attrs["afmboidbbaj"]; ok {
			if _, exists := attrs["npnlhobgfen"]; !exists {
				attrs["npnlhobgfen"] = v
			}
		}
		if v, ok := attrs["fgkfkgimfgo"]; ok {
			if _, exists := attrs["mbbdpnidheg"]; !exists {
				attrs["mbbdpnidheg"] = v
			}
		}
	}
	// metaQtyFamily 族（竿架/提取物/香精/添加剂/PVA）的数量在定义里叫
	// ljnnpokapig，实例字段是 meta 基类的剩余数量槽 dnmfciolgep，按别名映射；
	// 家族 UI 计数槽（abmokghdbhf=f0 miecmmbpolk，bpmmimcmpof=f1、
	// deckldefiap=f2、dpghngjmnap/pcngolccead=f0 ljnnpokapig）由 writeItemQty 镜像。
	if metaQtyFamily(class) {
		if v, ok := attrs[csQty]; ok {
			if _, exists := attrs["dnmfciolgep"]; !exists {
				attrs["dnmfciolgep"] = v
			}
		}
	}
	// 品类 46（成品窝料）的原生定义不带克重，实例照抄定义就永远是「质量 0 克」。
	// 客户端「质量」那一行读的是实例重量字段（cilmgoaoofg f7 ijbjficbhog）的
	// IEEE754 位模式、单位克；真实服务端按实例下发，这里按窝料档案补上。
	if def.Category == 46 {
		if w, ok := attrs[castAttrWaterWeight].(float64); !ok || w == 0 {
			attrs[castAttrWaterWeight] = float64(math.Float32bits(float32(groundbaitUnitGrams(def.ItemID))))
		}
	}
	for cs, val := range attrs {
		idx := fieldIdxByCS(class, cs)
		if idx < 0 || idx >= len(obj.Fields) {
			continue
		}
		switch tv := val.(type) {
		case float64:
			if fieldIsFloat(class, idx) {
				// 配置中的浮点是 IEEE754 位模式整数，入站时必须按 f32 解释。
				if fieldOpAt(class, idx) == "DOUBLE" {
					obj.Fields[idx].Float = proto.F64FromBits(uint64(tv))
				} else {
					obj.Fields[idx].Float = float64(proto.F32FromBits(uint32(tv)))
				}
			} else {
				obj.Fields[idx].Num = ptrU64(uint64(tv))
			}
		case string:
			sv := tv
			obj.Fields[idx].Str = &sv
		case bool:
			if tv {
				obj.Fields[idx].Num = ptrU64(1)
			} else {
				obj.Fields[idx].Num = ptrU64(0)
			}
		}
	}
}

// ---- attrs 读取辅助 ----

func defAttrs(def *store.ItemDef) map[string]any {
	if def == nil || def.AttrsJSON == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(def.AttrsJSON), &m); err != nil {
		return nil
	}
	return m
}

func defAttrF32(def *store.ItemDef, key string) (uint32, bool) {
	m := defAttrs(def)
	if m == nil {
		return 0, false
	}
	switch v := m[key].(type) {
	case float64:
		return uint32(v), true
	}
	return 0, false
}

func defAttrU64(def *store.ItemDef, key string) (uint64, bool) {
	m := defAttrs(def)
	if m == nil {
		return 0, false
	}
	switch v := m[key].(type) {
	case float64:
		return uint64(v), true
	}
	return 0, false
}

func defAttrStr(def *store.ItemDef, key string) (string, bool) {
	m := defAttrs(def)
	if m == nil {
		return "", false
	}
	if s, ok := m[key].(string); ok {
		return s, true
	}
	return "", false
}

// rodPartSystemID 读取鱼竿出厂部件槽的 systemID。
// 三个槽位（竿坯、手柄、第三件）保存的是内嵌部件定义对象，
// systemID 位于对象的 nhjofmmmngl 字段。
func rodPartSystemID(def *store.ItemDef, key string) (string, bool) {
	m := defAttrs(def)
	if m == nil {
		return "", false
	}
	switch v := m[key].(type) {
	case string:
		if v != "" {
			return v, true
		}
	case map[string]any:
		if s, ok := v["nhjofmmmngl"].(string); ok && s != "" {
			return s, true
		}
	}
	return "", false
}

func ptrU64(v uint64) *uint64 { return &v }

// rodPartConfigBits 读取竿定义中某部件槽的 hgaijieklek 基值。
// 取不到时返回 (0,false)，调用方回退到部件定义行。
func rodPartConfigBits(def *store.ItemDef, key string) (uint64, bool) {
	m := defAttrs(def)
	if m == nil {
		return 0, false
	}
	cfg, ok := m[key].(map[string]any)
	if !ok || cfg == nil {
		return 0, false
	}
	switch v := cfg["hgaijieklek"].(type) {
	case float64:
		return uint64(v), true
	case int64:
		return uint64(v), true
	case int:
		return uint64(v), true
	}
	return 0, false
}

// ---- 新实例创建 ----

// NewItemGUID 生成 32hex 随机 guid。
func NewItemGUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%x", b[:])
}

// netTicksNow .NET Ticks（物品 f7 创建时间用）。
func netTicksNow() int64 {
	// 100ns ticks；必须保留亚秒，f22 依赖严格单调的版本戳。
	return time.Now().UnixNano()/100 + 621_355_968_000_000_000
}

// reclassifyStoredItem 把误落成钓线类的物品改写成该类别自己的实例类。
// 已购工具、材料、铃铛和铅芯线靠这一步恢复实例类型，不必重买。
// guid、容器、数量、耐久和工艺保持不变。类别已经对上，或不是钓线回退类时不改。
//
// reclassifyCategories 限定允许改写的类别：这些类别的实例在客户端是独立 DTO，
// 早期没有映射时被创建成钓线类 ohciikgjghe，必须换类才能正常渲染。
// 新增映射时可把类别加进来，启动修复会对存量物品重写一次。
var reclassifyCategories = map[int64]bool{
	2: true, 19: true, 20: true, 30: true, 33: true, 38: true, 41: true,
	42: true, 45: true, 47: true, 55: true, 57: true, 58: true, 61: true,
	62: true, 63: true, 69: true, 70: true, 71: true, 72: true, 74: true,
	76: true, 78: true, 79: true, 82: true,
}

func (s *Server) reclassifyStoredItem(ctx context.Context, it *store.Item, def *store.ItemDef) (bool, error) {
	if it == nil || def == nil || !reclassifyCategories[def.Category] {
		return false, nil
	}
	cls, ok := resolveWireClass(def)
	if !ok || (it.WireClass == cls.Class && it.TypeID == cls.TypeID) {
		return false, nil
	}
	// 只改写「已知来源」的行：钓线兜底类、窝料类（香精误落 cilmgoaoofg，
	// 2026-11-05 北黑莓/黑加仑毒行，按 I 必挂），dfignppokoh 家族另一变体
	// （58 的映射从 abmokghdbhf/11500 修正为 bpmmimcmpof/721，存量需迁移），
	// 以及 aklijhgdgbh 家族错类（cat 20 添加剂误落 deckldefiap/125、cat 45
	// PVA 误落 bpmmimcmpof/721——2026-11-05 客户端分发表实证后修正，存量迁移）。
	legacyClassOK := it.WireClass == fallbackWireClass ||
		it.WireClass == "cilmgoaoofg" ||
		(metaQtyFamily(it.WireClass) &&
			(def.Category == 58 || def.Category == 20 || def.Category == 45))
	if it.WireClass != "" && !legacyClassOK {
		return false, nil
	}
	obj, err := wiregen.MakeObj(cls.Class, nil)
	if err != nil {
		return false, err
	}
	s.applyDefAttrs(def, cls.Class, obj)
	if def.Category == 47 {
		old, err := FieldsFromJSON(it.FieldsJSON)
		if err != nil {
			return false, err
		}
		// 换实例类型时保留已挂载铃铛的父引用和共同状态字段。
		for _, field := range wiregen.ClassMeta[cls.Class].Fields {
			idx := fieldIdxByCS(it.WireClass, field.Name)
			if idx >= 0 && idx < len(old) && old[idx].Op == obj.Fields[field.Idx].Op {
				obj.Fields[field.Idx] = old[idx]
			}
		}
	}
	setHex := func(cs, v string) {
		if idx := fieldIdxByCS(cls.Class, cs); idx >= 0 {
			obj.Fields[idx].Hex = v
		}
	}
	setNum := func(cs string, v uint64) {
		if idx := fieldIdxByCS(cls.Class, cs); idx >= 0 {
			obj.Fields[idx].Num = ptrU64(v)
		}
	}
	setHex(csGUID, it.GUID)
	setNum(csNumericID, uint64(def.NumericID))
	rowQty := it.Qty
	if countableCategories[def.Category] {
		writeItemQty(cls.Class, obj, uint64(rowQty))
	} else if def.Category == couponCategory {
		// 券已退出 countableCategories，但实例类 ahgeldclmdj 的权威数量槽
		// dnmfciolgep 仍须写回，否则改写后数量槽保持 0，客户端读不出这一张。
		writeItemQty(cls.Class, obj, uint64(rowQty))
	}
	workmanship := it.Workmanship
	durability := it.Durability
	s.materializeInto(ctx, cls.Class, def, obj, it.GUID, workmanship, durability)
	if idx := fieldIdxByCS(cls.Class, csCreated); idx >= 0 && u64Of(obj.Fields[idx].Num) == 0 {
		obj.Fields[idx].Num = ptrU64(uint64(netTicksNow()))
	}
	if idx := fieldIdxByCS(cls.Class, csUpdated); idx >= 0 && u64Of(obj.Fields[idx].Num) == 0 {
		obj.Fields[idx].Num = ptrU64(uint64(proto.WindowsFiletimeNow()))
	}
	if idx := fieldIdxByCS(cls.Class, csStateEnum); idx >= 0 && (def.Category != 47 || u64Of(obj.Fields[idx].Num) == 0) {
		st := u64Of(obj.Fields[idx].Num)
		if defBits, ok := defAttrU64(def, "hgaijieklek"); ok {
			st = defBits
		}
		obj.Fields[idx].Num = ptrU64(st)
	}
	it.WireClass = cls.Class
	it.TypeID = cls.TypeID
	it.Qty = rowQty
	it.Workmanship = workmanship
	it.Durability = durability
	s.canonicalizeItemPhysicalFields(ctx, it, obj.Fields)
	if err := s.initializeTradingItem(ctx, def, obj); err != nil {
		return false, err
	}
	fieldsJSON, err := FieldsToJSON(obj.Fields)
	if err != nil {
		return false, err
	}
	it.FieldsJSON = fieldsJSON
	if err := s.DB.UpdateItemWire(ctx, it.GUID, cls.Class, cls.TypeID, fieldsJSON, rowQty); err != nil {
		return false, err
	}
	return true, nil
}

// CreateItem 按 itemID 创建新实例并入包，返回数据库行。
// 查不到定义时返回错误，由调用方处理退款。
func (s *Server) CreateItem(ctx context.Context, uid int64, itemID, container string, units int64) (*store.Item, error) {
	def, err := s.DB.GetItemDefByItemID(ctx, itemID)
	if err != nil || def == nil {
		return nil, fmt.Errorf("物品定义不存在: %s", itemID)
	}
	cls, ok := resolveWireClass(def)
	if !ok {
		cls = wireClassEntry{fallbackWireClass, fallbackTypeID}
	}
	obj, err := wiregen.MakeObj(cls.Class, nil)
	if err != nil {
		return nil, err
	}
	s.applyDefAttrs(def, cls.Class, obj)

	// 基类 13 字段按 cs 名写入
	guid := NewItemGUID()
	setHex := func(cs, v string) {
		if idx := fieldIdxByCS(cls.Class, cs); idx >= 0 {
			obj.Fields[idx].Hex = v
		}
	}
	setNum := func(cs string, v uint64) {
		if idx := fieldIdxByCS(cls.Class, cs); idx >= 0 {
			obj.Fields[idx].Num = ptrU64(v)
		}
	}
	setHex(csGUID, guid) // guid
	setNum(csNumericID, uint64(def.NumericID))
	rowQty := int64(1)
	// 数量槽：计件 = 定义单位量×units，item.qty 必须与字段同值；
	// metaQtyFamily（竿架/提取物/香精）写权威槽 dnmfciolgep 并镜像总数槽，
	// 线(11)/前导(15) 是长度，不在此分支。
	if countableCategories[def.Category] {
		if idx := itemQtyFieldIdx(cls.Class); idx >= 0 {
			defQty := u64Of(obj.Fields[idx].Num)
			if defQty == 0 {
				defQty = 1
			}
			fieldQty := defQty * uint64(units)
			writeItemQty(cls.Class, obj, fieldQty)
			rowQty = int64(fieldQty)
		}
	} else if def.Category == couponCategory {
		// 一券一实例：数量槽固定 1，行数即张数。units 必须严格等于 1：
		// 多张请走 grantItemUnits/prepareCouponRows；units<1 也不能白送一张。
		if units != 1 {
			return nil, fmt.Errorf("梦鱼券需按张建行，CreateItem 不支持 units=%d", units)
		}
		writeItemQty(cls.Class, obj, 1)
	}
	// 嵌套子配置物化（渔轮/前导/拟饵）
	s.materializeInto(ctx, cls.Class, def, obj, guid, 1.0, 1.0)
	// 时间：f7 Ticks / f8 FILETIME
	if idx := fieldIdxByCS(cls.Class, csCreated); idx >= 0 {
		obj.Fields[idx].Num = ptrU64(uint64(netTicksNow()))
	}
	if idx := fieldIdxByCS(cls.Class, csUpdated); idx >= 0 {
		obj.Fields[idx].Num = ptrU64(uint64(proto.WindowsFiletimeNow()))
	}
	// 状态位 = 定义 hgaijieklek（装备类 |2048）
	if idx := fieldIdxByCS(cls.Class, csStateEnum); idx >= 0 {
		st := u64Of(obj.Fields[idx].Num)
		if defBits, ok := defAttrU64(def, "hgaijieklek"); ok {
			st = defBits
		}
		if cls.Class == "cnolibojkao" || cls.Class == "dgjinoebdop" || cls.Class == "hgabifpnhmk" ||
			cls.Class == "knocoeggfdk" || cls.Class == "dbmbnkpkafb" {
			st |= 2048
		}
		obj.Fields[idx].Num = ptrU64(st)
	}

	it := &store.Item{
		GUID: guid, UID: uid, ItemID: def.ItemID, NumericID: def.NumericID,
		WireClass: cls.Class, TypeID: cls.TypeID, Container: tradingItemContainer(cls.Class, container), Slot: 0,
		Qty: rowQty, Durability: 1.0, Workmanship: 1.0,
		CreatedTS: float64(time.Now().Unix()),
	}
	// 竿的厂件 f0/f1/f2 与默认钓组 f5 必须在创建时落库，不能只靠 ItemToTree。
	if cls.Class == "cnolibojkao" {
		if err := s.rebuildRod(ctx, it, obj); err != nil {
			return nil, err
		}
	}
	s.canonicalizeItemPhysicalFields(ctx, it, obj.Fields)
	if err := s.initializeTradingItem(ctx, def, obj); err != nil {
		return nil, err
	}
	fieldsJSON, err := FieldsToJSON(obj.Fields)
	if err != nil {
		return nil, err
	}
	it.FieldsJSON = fieldsJSON
	if err := s.DB.InsertItem(ctx, it); err != nil {
		return nil, err
	}
	return it, nil
}

// prepareNewItemWithCount 按“实际件数”构造可堆叠物品，绕开定义单位量换算。
// 铲子挖饵需要精确发放 1-5 个，而不是 1 个购买单位（自然饵通常为 30 个/单位）。
func (s *Server) prepareNewItemWithCount(ctx context.Context, uid int64, itemID, container string,
	count int64, explicitGUID string) (*store.Item, error) {
	if count < 1 {
		count = 1
	}
	it, err := s.prepareNewItem(ctx, uid, itemID, container, 1, explicitGUID)
	if err != nil {
		return nil, err
	}
	fields, err := FieldsFromJSON(it.FieldsJSON)
	if err != nil {
		return nil, err
	}
	if setItemQty(it.WireClass, fields, uint64(count)) {
		fj, ferr := FieldsToJSON(fields)
		if ferr != nil {
			return nil, ferr
		}
		it.FieldsJSON = fj
	}
	it.Qty = count
	return it, nil
}

// prepareCouponRows 按「一张券一个实例」构造 count 张独立券（不落库）。
//
// 券的数量槽本身可用（dnmfciolgep），但客户端背包不读它、只按行渲染，
// 所以张数必须用行数表达。上限沿用组合包的实例数上限，避免一次构造过多。
func (s *Server) prepareCouponRows(ctx context.Context, uid int64, itemID, container string, count int64) ([]*store.Item, error) {
	if count < 1 {
		count = 1
	}
	if count > maxBundlePreparedItems {
		return nil, fmt.Errorf("梦鱼券张数 %d 超过上限 %d", count, maxBundlePreparedItems)
	}
	rows := make([]*store.Item, 0, count)
	for i := int64(0); i < count; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		it, err := s.prepareNewItem(ctx, uid, itemID, container, 1, "")
		if err != nil {
			return nil, err
		}
		rows = append(rows, it)
	}
	return rows, nil
}

// insertPreparedRows 在一个经济事务里插入多行，任一行失败整体回滚。
func (s *Server) insertPreparedRows(ctx context.Context, rows []*store.Item) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := beginEconomyTx(ctx, s.DB)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, it := range rows {
		if _, _, gerr := grantPreparedItemTx(ctx, tx, it, false); gerr != nil {
			return gerr
		}
	}
	return tx.Commit()
}

// grantItemUnits 发放 units 件物品并返回全部新行：可堆叠类别合并成一行，
// 梦鱼券按件建行。管理指令、礼包等按「件数」发东西的入口都走这里。
func (s *Server) grantItemUnits(ctx context.Context, uid int64, itemID, container string, units int64) ([]*store.Item, error) {
	def, err := s.DB.GetItemDefByItemID(ctx, itemID)
	if err != nil || def == nil {
		return nil, fmt.Errorf("物品定义不存在: %s", itemID)
	}
	if def.Category != couponCategory {
		it, _, aerr := s.AddToStack(ctx, uid, itemID, container, units)
		if aerr != nil {
			return nil, aerr
		}
		return []*store.Item{it}, nil
	}
	rows, err := s.prepareCouponRows(ctx, uid, itemID, container, units)
	if err != nil {
		return nil, err
	}
	if err := s.insertPreparedRows(ctx, rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// AddToStack 为可堆叠物品合并数量，返回现有物品或新行。
func (s *Server) AddToStack(ctx context.Context, uid int64, itemID, container string, units int64) (*store.Item, bool, error) {
	def, err := s.DB.GetItemDefByItemID(ctx, itemID)
	if err != nil || def == nil {
		return nil, false, fmt.Errorf("物品定义不存在: %s", itemID)
	}
	if def.Category == couponCategory {
		// 梦鱼券：一张一个实例。调用方给的是张数，就逐张建行。
		rows, err := s.prepareCouponRows(ctx, uid, itemID, container, units)
		if err != nil {
			return nil, false, err
		}
		if len(rows) == 0 {
			return nil, false, fmt.Errorf("梦鱼券张数无效: %d", units)
		}
		if err := s.insertPreparedRows(ctx, rows); err != nil {
			return nil, false, err
		}
		return rows[len(rows)-1], false, nil
	}
	if !countableCategories[def.Category] {
		it, err := s.CreateItem(ctx, uid, itemID, container, units)
		return it, false, err
	}
	// 合并写入走 row.WireClass 驱动的 setItemQty（家族镜像在函数内处理），
	// 无需在此预先解析实例类。

	defQty := int64(1)
	if v, ok := defAttrU64(def, csQty); ok && v > 0 {
		defQty = int64(v)
	}
	addQty := defQty * units
	// 找同类同 ID 堆
	guids, _ := s.DB.ItemGUIDsByContainers(ctx, uid, []string{container})
	for _, g := range guids {
		row, err := s.DB.GetItemByGUID(ctx, g)
		if err != nil || row.ItemID != itemID {
			continue
		}
		fields, _ := FieldsFromJSON(row.FieldsJSON)
		if cur, ok := itemQtyValue(row.WireClass, fields); ok {
			// 若 row.qty 与字段值不一致，以字段值为基准修正后再累加。
			if cur > 0 {
				row.Qty = int64(cur)
			}
			setItemQty(row.WireClass, fields, cur+uint64(addQty))
		}
		fj, _ := FieldsToJSON(fields)
		newQty := row.Qty + addQty
		_ = s.DB.UpdateItemFields(ctx, row.GUID, fj, newQty, row.Durability, row.Workmanship, row.RigGUID)
		row.Qty = newQty
		row.FieldsJSON = fj
		return row, true, nil
	}
	it, err := s.CreateItem(ctx, uid, itemID, container, units)
	return it, false, err
}

func wireClassOf(cat int64) string {
	if c, ok := categoryWireClass[cat]; ok {
		return c.Class
	}
	return fallbackWireClass
}
