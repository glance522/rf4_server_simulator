package gameserver

// 制作域：9/1 制作、9/3 分解、9/4 询价、9/6 配方模板。
//
// 9/1 校验配方和材料，在事务内扣料、产出入包并写流水；9/3 对铲子挖饵，其它物品按分解处理；
// 9/4 返回费用行；9/6 返回配方详情。响应类型由命令合同约束。
import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

func init() {
	RegisterExtra(func(d *Dispatch) {
		d.Register(9, 1, hCraft)
		d.Register(9, 3, hCraftMine2)
		d.Register(9, 4, hCraftCost)
		d.Register(9, 6, hCraftAddTemplate)
		d.Register(9, 7, hCraftDeleteTemplate)
	})
}

// ---- 常量 ----

// dhkphmjinmj 的 wire type_id / 类名（obj8，9/1 与 9/4 的同一载荷）。
const (
	obj8TypeID = 8
	obj8Class  = "dhkphmjinmj"
)

// obj8 的 wire 字段位置；顺序为 STR、VAL、VAL、OPT、U16STR、U32。
const (
	obj8FRecipe    = 0 // cemjmlpafmc 配方 systemID
	obj8FTools     = 1 // oifjpmejkob 工具槽
	obj8FMaterials = 2 // odmioelpcpi 材料槽
	obj8FName      = 4 // phoglfnffah 自定义名
	obj8FQty       = 5 // ekjiebkgboh 数量
)

// maxCraftQty 单次制作的份数上限（防止异常 qty 把材料一次扣光）。
const maxCraftQty = 1_000_000

// craftBatchSpec 描述产物单次制作的批次规则：Limit 是材料套数上限，
// Yield 是每套材料固定产出的数量。
type craftBatchSpec struct {
	Limit int64
	Yield int64
}

// craftBatchSpecFor 返回产物的批次规则。
// 窝料一套固定出 60 个、PVA 棒按规格 60/30/12 个，受背包单格上限约束；
// 拟饵/引线/PVA 网/奖板等整件产物一次只能做 1 件；其余按 maxCraftQty。
func craftBatchSpecFor(def *store.ItemDef) craftBatchSpec {
	if def == nil {
		return craftBatchSpec{Limit: 1, Yield: 1}
	}
	switch def.Category {
	case 46:
		return craftBatchSpec{Limit: 60, Yield: 60}
	case 30:
		switch def.ItemID {
		case "pva_stick_m":
			return craftBatchSpec{Limit: 30, Yield: 30}
		case "pva_stick_l":
			return craftBatchSpec{Limit: 12, Yield: 12}
		}
		return craftBatchSpec{Limit: 60, Yield: 60}
	case 13, 15, 17, 61, 71:
		return craftBatchSpec{Limit: 1, Yield: 1}
	}
	return craftBatchSpec{Limit: maxCraftQty, Yield: 1}
}

// vendRecipeReports 判定配方是否走贩卖机语义：recipe_id 以 _vend 结尾的
// 全部是图内诱饵贩卖机商品（feed 14 + pva_stick 3 + pva_stinger 5，共 22 条），
// 官方语义是机器按货架价卖料+搅拌一步完成，玩家不需要持有原料。
func vendRecipeReports(recipeID string) bool {
	return strings.HasSuffix(recipeID, "_vend")
}

// vendCharge 是贩卖机配方每套材料要按份购买的一种配料（Qty 未乘制作套数）。
type vendCharge struct {
	ItemID string
	Qty    int64
}

// resolveVendCharges 解析贩卖机配方每套材料的购买清单。
// 静态库读取必须在经济事务开启前完成（SQLite 单连接）。
// 需求带显式 item_id 直接采用；类别/标签需求取客户端材料槽 f2 提交的
// 具体物品（craftSlot.Species 承载，实抓 2026-11-09：槽 = {需求id, 临时GUID,
// 具体item id}），槽序即需求序，已认领的槽不复用；类别可校验则校验，
// 标签沿用既有"无法反查、忽略"口径（见 consumeByDemand）。
func (s *Server) resolveVendCharges(ctx context.Context, demands []craftDemand, slots []craftSlot) ([]vendCharge, error) {
	used := make([]bool, len(slots))
	out := make([]vendCharge, 0, len(demands))
	for _, d := range demands {
		if d.ItemID != "" {
			out = append(out, vendCharge{ItemID: d.ItemID, Qty: d.need()})
			continue
		}
		picked := -1
		for i, sl := range slots {
			if used[i] || sl.Species == "" {
				continue
			}
			if len(d.Categories) > 0 {
				cat, ok := s.effectiveCategory(ctx, sl.Species)
				if !ok || !containsI64(d.Categories, cat) {
					continue
				}
			}
			picked = i
			break
		}
		if picked < 0 {
			return nil, fmt.Errorf("需求 %s 没有可定价的材料槽", d.DemandID)
		}
		used[picked] = true
		out = append(out, vendCharge{ItemID: slots[picked].Species, Qty: d.need()})
	}
	return out, nil
}

// vendTotalPrice 按配料单价（货架最低价，缺价时按货架补齐同款口径估价）
// 合计 times 套的银币总额。价格读取同样必须在经济事务之外。
func (s *Server) vendTotalPrice(ctx context.Context, charges []vendCharge, times int64) (int64, error) {
	var total int64
	for _, c := range charges {
		price, err := s.DB.CraftIngredientSilverPrice(ctx, c.ItemID)
		if err != nil {
			return 0, fmt.Errorf("配料 %s 无法定价", c.ItemID)
		}
		q := c.Qty * times
		if q > 0 && price > math.MaxInt64/q {
			return 0, fmt.Errorf("配料 %s 价格合计溢出", c.ItemID)
		}
		total += price * q
		if total < 0 {
			return 0, fmt.Errorf("配料合计溢出")
		}
	}
	return total, nil
}

// vendChargeNote 把配料清单压成流水 note（item×份 逗号分隔）。
func vendChargeNote(charges []vendCharge) string {
	parts := make([]string, 0, len(charges))
	for _, c := range charges {
		parts = append(parts, fmt.Sprintf("%sx%d", c.ItemID, c.Qty))
	}
	return strings.Join(parts, ",")
}

func containsI64(list []int64, v int64) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// craftTimesFor 把客户端提交的 qty 收敛到 [1, spec.Limit]。
func craftTimesFor(spec craftBatchSpec, qty uint64) int64 {
	if qty < 1 {
		return 1
	}
	limit := spec.Limit
	if limit < 1 {
		limit = 1
	}
	if qty > uint64(limit) {
		return limit
	}
	return int64(qty)
}

// craftOutputMerges 决定制作产物是否并入背包已有同种堆。
// 窝料与 PVA 棒每组材料独立占一格，不与其他批次合并；
// 其余计件产物（茶、咖啡、面包等）继续按数量堆叠。
func craftOutputMerges(def *store.ItemDef) bool {
	if def == nil {
		return true
	}
	return def.Category != 46 && def.Category != 30
}

// appendMaterialUses 按实际扣掉的件数记录用料。PVA 棒的 10/20/50 克规格
// 分别装 1/2/5 份干饵，只记一次会让大棒和小棒的成分比例失真。
func appendMaterialUses(dst []string, itemID string, qty int64) []string {
	for i := int64(0); i < qty; i++ {
		dst = append(dst, itemID)
	}
	return dst
}

// 制作配方对应的技能大类（捕获鱼饵 4 见 baitdig.go）。
const (
	craftCookSkillID = 6 // 烹调术
	craftFeedSkillID = 7 // 配置饵料
	craftLureSkillID = 8 // 制作鱼饵（手工饵）
)

// craftSkillCatForRecipe 将配方映射到技能大类；官方无对应技能的配方返回 0。
func craftSkillCatForRecipe(recipeID string) int64 {
	switch {
	case strings.HasPrefix(recipeID, "craft_bait_"):
		return captureBaitSkillID
	case strings.HasPrefix(recipeID, "craft_cook_"):
		return craftCookSkillID
	case strings.HasPrefix(recipeID, "craft_feed_"),
		strings.HasPrefix(recipeID, "craft_pva_"),
		recipeID == "craft_crushed_boils":
		return craftFeedSkillID
	case strings.HasPrefix(recipeID, "craft_lure_"):
		return craftLureSkillID
	}
	return 0
}

// bundleCategory 套盒类别（shop_rules.BUNDLE_CATEGORY）。
const bundleCategory = 55

// trophyMountCategory 标本/奖章类（悬挂板、落地架、吊顶，输出均此类别）。
const trophyMountCategory = 71

// couponChucheloItemID 梦鱼优惠券：标本制作商店的免单券，一次制作消耗一张。
// 客户端在标本板材料槽里用需求 id mat_coupon_chuchelo + 实例 GUID 表达，
// 静态配方的 materials_json 全为空，所以扣券只能按槽位判定，不能按需求表。
const couponChucheloItemID = "coupon_chuchelo"

// ---- 请求解析 ----

// craftSlot 表示 koelkolmmee 槽位：f0 是需求 id，f1 是物品实例 GUID。
// 部分界面使用客户端临时 GUID，服务端会再按需求 id 定位材料。
// 标本板的槽位是三字段 bhfikaplhmh {需求id, 鱼护EchoGUID, 鱼种}，f2 带鱼种。
type craftSlot struct {
	ItemID  string
	GUID    string
	Species string
}

// craftPayload 9/1 / 9/4 共用的制作载荷。
type craftPayload struct {
	Recipe    string
	Qty       uint64
	Name      string
	Tools     []craftSlot
	Materials []craftSlot
	Source    string // "obj8"、"strings" 或 "none"
}

// craftPayloadOf 从请求树读取制作载荷。优先解析 obj8，缺失时回退到顶层字符串。
func craftPayloadOf(ctx *Ctx) craftPayload {
	out := craftPayload{Source: "none"}
	if node := findObj8(ctx.Tree); node != nil && node.FieldCount() > obj8FQty {
		out.Recipe = node.Fields[obj8FRecipe].StrOr()
		out.Name = node.Fields[obj8FName].StrOr()
		if n := node.Fields[obj8FQty].Num; n != nil {
			out.Qty = u64Of(n)
		}
		out.Tools = slotList(node.Fields[obj8FTools].Val)
		out.Materials = slotList(node.Fields[obj8FMaterials].Val)
		out.Source = "obj8"
		return out
	}
	if strs := deepStrings(ctx.Tree); len(strs) > 0 {
		out.Recipe = strs[0]
		out.Source = "strings"
	}
	out.Materials = slotList(ctx.Tree)
	return out
}

// findObj8 在请求树里找最外层的 obj8（dhkphmjinmj）；没有则 nil。
// 对象沿字段值下钻，容器沿子项下钻。
func findObj8(root *proto.Value) *proto.Value {
	var found *proto.Value
	var walk func(v *proto.Value)
	walk = func(v *proto.Value) {
		if v == nil || found != nil {
			return
		}
		if v.Kind == proto.KindObj && (v.TypeID == obj8TypeID || v.Class == obj8Class) {
			found = v
			return
		}
		walkChildren(v, walk)
	}
	walk(root)
	return found
}

// slotList 收集 koelkolmmee 槽位。对象有三个字段且第二个为 GUID 时即视为槽位，
// 不依赖类名。
func slotList(tree *proto.Value) []craftSlot {
	var out []craftSlot
	var walk func(v *proto.Value)
	walk = func(v *proto.Value) {
		if v == nil {
			return
		}
		if v.Kind == proto.KindObj && v.FieldCount() == 3 {
			g := v.Fields[1]
			if g.Op == "GUID16" || (g.Op == "OPTGUID" && g.Hex != "") {
				out = append(out, craftSlot{
					ItemID:  v.Fields[0].StrOr(),
					GUID:    g.Hex,
					Species: v.Fields[2].StrOr(),
				})
			}
		}
		walkChildren(v, walk)
	}
	walk(tree)
	return out
}

// walkChildren 遍历容器、数组和对象的子值。
func walkChildren(v *proto.Value, walk func(*proto.Value)) {
	switch v.Kind {
	case proto.KindArgs, proto.KindValList, proto.KindArray,
		proto.KindU32Arr, proto.KindGuidArr, proto.KindTypedU32Arr:
		for _, it := range v.Items {
			walk(it)
		}
	case proto.KindObj:
		for i := range v.Fields {
			if v.Fields[i].Val != nil {
				walk(v.Fields[i].Val)
			}
		}
	}
}

// deepStrings 按前序遍历收集非空字符串，包括 args 中的字符串和对象的
// STR、U16STR、U32N 字段。
func deepStrings(v *proto.Value) []string {
	var out []string
	var walk func(x *proto.Value)
	walk = func(x *proto.Value) {
		if x == nil {
			return
		}
		switch x.Kind {
		case proto.KindStr:
			if x.Str != nil && *x.Str != "" {
				out = append(out, *x.Str)
			}
		case proto.KindArgs, proto.KindValList, proto.KindArray,
			proto.KindU32Arr, proto.KindGuidArr, proto.KindTypedU32Arr:
			for _, it := range x.Items {
				walk(it)
			}
		case proto.KindObj:
			for i := range x.Fields {
				switch x.Fields[i].Op {
				case "STR", "U16STR", "U32N":
					if s := x.Fields[i].StrPtr(); s != nil && *s != "" {
						out = append(out, *s)
					}
				}
			}
		}
	}
	walk(v)
	return out
}

// ---- 配方与材料需求 ----

// craftDemand 表示配方材料需求，也是 materials_json 的元素形状。
// item_id 可为空 —— 那种需求只约束类别（categories）或标签组（tags）。
type craftDemand struct {
	DemandID     string   `json:"demand_id"`
	ItemID       string   `json:"item_id"`
	Qty          *int64   `json:"qty"`
	Alternatives []string `json:"alternatives"`
	Categories   []int64  `json:"categories"`
	Tags         []int64  `json:"tags"`
}

// need 返回需求数量；未配置时为 1。
func (m craftDemand) need() int64 {
	if m.Qty == nil {
		return 1
	}
	return *m.Qty
}

// parseDemands 解析配方材料清单；materials_json 为空时返回 nil。
func parseDemands(materialsJSON string) []craftDemand {
	if materialsJSON == "" {
		return nil
	}
	var out []craftDemand
	if err := json.Unmarshal([]byte(materialsJSON), &out); err != nil {
		return nil
	}
	return out
}

// demandAccepts 返回该需求可接受的物品 id。alternatives 过滤空串后优先；
// 为空时仅使用 item_id。
func demandAccepts(m craftDemand) []string {
	alts := make([]string, 0, len(m.Alternatives))
	for _, a := range m.Alternatives {
		if a != "" {
			alts = append(alts, a)
		}
	}
	if len(alts) > 0 {
		return alts
	}
	return []string{m.ItemID}
}

// allDemandsExplicit 判断所有需求是否都指定了物品 id。存在仅按类别或标签
// 约束的需求时返回 false，此时需要读取客户端提交的材料槽。
func allDemandsExplicit(ms []craftDemand) bool {
	for _, m := range ms {
		if m.ItemID == "" {
			return false
		}
	}
	return true
}

// ---- 鱼护渔获当制作材料 ----

// slotsByDemand 把客户端材料槽按需求 id 归组。鱼类加工配方（蟹肉、鱼片、活饵、
// 死鱼饵、熏鱼等）的槽位里拖的就是鱼护条目：f0 是需求 id，f1 是鱼护 echo guid。
func slotsByDemand(slots []craftSlot) map[string][]craftSlot {
	out := map[string][]craftSlot{}
	for _, sl := range slots {
		if sl.ItemID == "" {
			continue
		}
		out[sl.ItemID] = append(out[sl.ItemID], sl)
	}
	return out
}

// keepnetAccepted 返回需求可接受的鱼种集合。鱼类配方的 alternatives 填的就是
// 鱼种 id（如 craft_bait_from_crab → ["rk_crab","ed_crab"]），这些 id 在 item_def
// 里没有定义，只能按 keepnet_items.species 匹配。
func keepnetAccepted(demand craftDemand) map[string]bool {
	accept := map[string]bool{}
	for _, id := range demandAccepts(demand) {
		if id != "" {
			accept[id] = true
		}
	}
	return accept
}

// consumeKeepnetEntryTx 事务内消费一条鱼护渔获（删除条目 + 清理镜像行/槽位，
// 与标本挂鱼同一语义，直接复用 consumeMountedFishTx 以免两处漂移）。
// 返回 false 表示该条目已不存在——并发下被别人先消费，调用方应跳过而不是报错。
func (s *Server) consumeKeepnetEntryTx(ctx context.Context, tx *sql.Tx, uid int64, guid string) (bool, error) {
	deleted, err := s.DB.DeleteKeepnetTx(ctx, tx, uid, guid)
	if err != nil || !deleted {
		return false, err
	}
	// 已经删掉了鱼护条目；镜像行清理失败同样要整体回滚，否则留下幽灵行。
	if _, err := tx.ExecContext(ctx, `DELETE FROM item WHERE uid=? AND guid=?`, uid, guid); err != nil {
		return true, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM container_slot WHERE guid=?`, guid); err != nil {
		return true, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM rig_component WHERE guid=?`, guid); err != nil {
		return true, err
	}
	return true, nil
}

// consumeKeepnetForDemandTx 事务内按鱼种从鱼护扣减需求份数，每条鱼护条目算一份。
// preferred 里的 echo guid（客户端材料槽明确选中的那一尾）优先消费，其余按鱼护
// 时间顺序补齐。返回实际消耗的条目；凑不够时返回已扣部分，由调用方决定整体失败
// ——经济事务回滚则鱼不陪葬，提交则鱼必被消费（恰好一次）。
func (s *Server) consumeKeepnetForDemandTx(ctx context.Context, tx *sql.Tx, uid int64,
	demand craftDemand, need int64, preferred map[string]bool) ([]*store.KeepnetEntry, error) {
	if need <= 0 {
		return nil, nil
	}
	accept := keepnetAccepted(demand)
	if len(accept) == 0 {
		return nil, nil
	}
	entries, err := s.DB.KeepnetListTx(ctx, tx, uid)
	if err != nil {
		return nil, err
	}
	// 先放玩家明确选中的那几条，再按鱼护时间顺序补；KeepnetListTx 已按 uid 过滤，
	// 因此别人的鱼护条目不参与匹配。
	ordered := make([]*store.KeepnetEntry, 0, len(entries))
	for _, e := range entries {
		if preferred[e.EchoGUID] && accept[e.Species] {
			ordered = append(ordered, e)
		}
	}
	for _, e := range entries {
		if preferred[e.EchoGUID] && accept[e.Species] {
			continue
		}
		ordered = append(ordered, e)
	}
	var consumed []*store.KeepnetEntry
	for _, e := range ordered {
		if int64(len(consumed)) >= need {
			break
		}
		if !accept[e.Species] {
			continue
		}
		ok, derr := s.consumeKeepnetEntryTx(ctx, tx, uid, e.EchoGUID)
		if derr != nil {
			return consumed, derr
		}
		if !ok {
			// 并发下已被别人消费：当作这条不存在，继续找下一条。
			continue
		}
		consumed = append(consumed, e)
	}
	return consumed, nil
}

// preferredKeepnet 从材料槽里挑出指向某需求的 echo guid 集合，用于让"玩家明确
// 拖进来的那一尾鱼"优先被消费，而不是按时间顺序从鱼护头开始扣。
func preferredKeepnet(slots []craftSlot, demandID string) map[string]bool {
	out := map[string]bool{}
	for _, sl := range slots {
		if sl.ItemID == demandID && sl.GUID != "" {
			out[sl.GUID] = true
		}
	}
	return out
}

// scaleDemands 按制作份数放大材料需求；times 为 1 时原样返回。
// 乘法溢出时返回 ok=false，避免异常份数绕过材料校验。
func scaleDemands(ms []craftDemand, times int64) ([]craftDemand, bool) {
	if times <= 1 {
		return ms, true
	}
	out := make([]craftDemand, len(ms))
	copy(out, ms)
	for i := range out {
		per := out[i].need()
		if per > 0 && times > math.MaxInt64/per {
			return nil, false
		}
		q := per * times
		out[i].Qty = &q
	}
	return out, true
}

// resolveRecipe 依次按配方 id、产出物 id 和 recipe_ 前缀解析配方键。
func (s *Server) resolveRecipe(ctx context.Context, key string) *store.Recipe {
	if key == "" {
		return nil
	}
	if r, err := s.DB.GetRecipe(ctx, key); err == nil && r != nil {
		return r
	}
	if r, err := s.DB.GetRecipeByOutput(ctx, key); err == nil && r != nil {
		return r
	}
	if r, err := s.DB.GetRecipe(ctx, "recipe_"+key); err == nil && r != nil {
		return r
	}
	return nil
}

// setItemName 把玩家在制作界面填写的自定义名称写入物品实例。
// 客户端该字段（phoglfnffah）为空时回落到定义名，非空时原样显示。
func setItemName(it *store.Item, name string) error {
	if it == nil {
		return errors.New("空物品行")
	}
	if name == "" {
		return nil
	}
	fields, err := FieldsFromJSON(it.FieldsJSON)
	if err != nil {
		return err
	}
	idx := fieldIdxByCS(it.WireClass, csCustomName)
	if idx < 0 || idx >= len(fields) {
		return fmt.Errorf("物品 %s 没有名称字段", it.ItemID)
	}
	fields[idx].Str = &name
	fj, err := FieldsToJSON(fields)
	if err != nil {
		return err
	}
	it.FieldsJSON = fj
	return nil
}

// ---- 9/1 制作闭环 ----

// hCraft 处理 9/1 制作：解析配方、校验材料、事务内扣料、产出入包并写流水。
// 可计件产物并入已有同种堆，非计件产物保持独立实例；803.f1 返回本次产出
// （数量为本次产出量），f6 返回背包增量（合并后为整堆），f8 返回移除项。
//
// 未知配方、材料不足和入库失败均返回 803(false)，避免客户端等待缺失响应。
func hCraft(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	cctx := ctx.Sess.pushCtx()
	req := craftPayloadOf(ctx)
	recipe := srv.resolveRecipe(cctx, req.Recipe)
	if recipe == nil {
		ctx.Log.Info("9/1 未知配方", "recipe", req.Recipe,
			"source", req.Source, "materials", len(req.Materials))
		return srv.replyCraftResult(ctx, false, nil, nil, nil)
	}
	outputID := recipe.OutputID
	if outputID == "" {
		outputID = req.Recipe
	}
	outputDef, derr := srv.DB.GetItemDefByItemID(cctx, outputID)
	if derr != nil {
		ctx.Log.Warn("9/1 产出物定义读取失败", "output", outputID, "err", derr)
		return srv.replyCraftResult(ctx, false, nil, nil, nil)
	}
	demands := parseDemands(recipe.MaterialsJSON)
	spec := craftBatchSpecFor(outputDef)
	times := craftTimesFor(spec, req.Qty)
	if len(demands) == 0 {
		// 零材料配方尚未接入鱼获/熟饵等消耗，先限制单份，避免无成本批量产出。
		times = 1
	}
	uid := ctx.Sess.Userid
	isLeaderCraft := strings.HasPrefix(recipe.RecipeID, "craft_leader_")

	// 先在事务外构造物品；构造失败时尚未扣材料。
	producedRow, err := srv.prepareNewItem(cctx, uid, outputID, "backpack",
		times*spec.Yield, "")
	if err != nil {
		ctx.Log.Warn("9/1 产出物不可构造", "output", outputID, "err", err)
		return srv.replyCraftResult(ctx, false, nil, nil, nil)
	}

	// 标本/奖章族（悬挂板 board s/m/l、落地架 stand xl、吊顶 ceil xxl，
	// 输出均为 cat 71）：客户端材料槽带鱼护 EchoGUID+鱼种（bhfikaplhmh
	// 三字段），把该渔获按鱼护同款 25 类编码嵌进板子 f1 peipahjmida，
	// 客户端据此在奖牌上渲染鱼。按输出品类判定而非配方前缀——stand/ceil
	// 族曾因只匹配 craft_fish_board_ 前缀漏嵌鱼（2026-11-06 实测）。
	// 复用上方 outputDef（静态库查询必须在经济事务开启前完成，SQLite 单连接）。
	// 嵌鱼 guid 按板子 guid 派生，避免与鱼护内原鱼同 guid。
	// 渔获定位/嵌鱼/消耗都在事务内做：无有效渔获必须 803(false)——
	// 曾实测无鱼护 GUID 的请求静默产出空白板；且消费与制作同事务，
	// 杜绝"板子已入库、鱼还在护"或并发同鱼双开标本。
	isTrophyMount := outputDef != nil && outputDef.Category == trophyMountCategory
	var mounted *mountedFish

	if isLeaderCraft {
		// 必须在经济事务开启前加载静态目录；SQLite 单连接事务内再查会阻塞。
		if _, err := srv.leaderWireCatalog(cctx); err != nil {
			ctx.Log.Warn("9/1 前导线目录加载失败", "err", err)
			return srv.replyCraftResult(ctx, false, nil, nil, nil)
		}
	}

	// 贩卖机配方（*_vend）：机器按货架价卖料+搅拌一步完成，玩家不需要持有
	// 原料。配料解析与定价读静态库/覆盖价，必须在经济事务开启前完成。
	isVendCraft := vendRecipeReports(recipe.RecipeID)
	var vendCharges []vendCharge
	var vendTotal int64
	if isVendCraft {
		var verr error
		vendCharges, verr = srv.resolveVendCharges(cctx, demands, req.Materials)
		if verr == nil {
			vendTotal, verr = srv.vendTotalPrice(cctx, vendCharges, times)
		}
		if verr != nil {
			ctx.Log.Warn("9/1 贩卖机配料定价失败", "recipe", recipe.RecipeID, "err", verr)
			return srv.replyCraftResult(ctx, false, nil, nil, nil)
		}
	}

	tx, err := beginEconomyTx(cctx, srv.DB)
	if err != nil {
		ctx.Log.Warn("9/1 开启事务失败", "err", err)
		return srv.replyCraftResult(ctx, false, nil, nil, nil)
	}
	defer func() { _ = tx.Rollback() }()

	touched := []string{}
	// usedMats 记下本次真的扣掉了哪些原料 item_id（不是槽里的需求 id）。
	// 自制窝料的产物没有独立 item_id，只能按实例把用料记下来。
	usedMats := []string{}
	// keepnetTaken 记下本次从鱼护里吃掉的渔获 echo guid。它们不在 item 表里，
	// 需要单独走 4/21 物品移除信号，客户端才会把鱼护界面里的原鱼撤下。
	keepnetTaken := []string{}
	var leaderMaterials craftedLeaderMaterials
	fail := func(reason string, args ...any) ([]byte, bool) {
		ctx.Log.Warn("9/1 制作失败："+reason, args...)
		return srv.replyCraftResult(ctx, false, nil, nil, nil)
	}

	if isLeaderCraft {
		var materialErr error
		touched, leaderMaterials, materialErr = srv.consumeLeaderCraftMaterialsTx(
			cctx, tx, uid, req.Materials, demands, times)
		if materialErr != nil {
			return fail("引线材料扣除失败", "err", materialErr)
		}
	} else if isVendCraft {
		// 同一事务内按货架价扣款：余额不足整体回滚，不产出、不扣钱。
		if vendTotal > 0 {
			if err := applyWalletTx(cctx, tx, uid, -vendTotal, 0, "buy", outputID, "",
				fmt.Sprintf("vend recipe=%s qty=%d items=%s",
					recipe.RecipeID, times, vendChargeNote(vendCharges))); err != nil {
				if errors.Is(err, errInsufficientFunds) {
					return fail("贩卖机余额不足", "total", vendTotal)
				}
				return fail("贩卖机扣款失败", "err", err)
			}
		}
		// 自制窝料成分按实例记账：配料即真实用料，投进窝里后成分比例不变。
		for _, c := range vendCharges {
			usedMats = appendMaterialUses(usedMats, c.ItemID, c.Qty*times)
		}
	} else if len(demands) > 0 && allDemandsExplicit(demands) {
		need, ok := scaleDemands(demands, times)
		if !ok {
			return fail("材料数量溢出", "times", times)
		}
		rows, lerr := listItemsTx(cctx, tx, uid, []string{"backpack", "rig"})
		if lerr != nil {
			return fail("读取材料失败", "err", lerr)
		}
		for _, demand := range need {
			remaining := demand.need()
			for _, accept := range demandAccepts(demand) {
				if remaining <= 0 {
					break
				}
				for _, it := range rows {
					if remaining <= 0 || it.ItemID != accept || it.Qty <= 0 {
						continue
					}
					take := remaining
					if have := stackCountTx(cctx, tx, it); have < take {
						take = have
					}
					got, cerr := consumeItemQtyTx(cctx, tx, it, take)
					if cerr != nil {
						return fail("扣材料失败", "item", accept, "err", cerr)
					}
					remaining -= got
					if got > 0 {
						usedMats = appendMaterialUses(usedMats, it.ItemID, got)
						if !containsGUID(touched, it.GUID) {
							touched = append(touched, it.GUID)
						}
					}
				}
			}
			// 背包/钓组里没有，回落到鱼护：鱼类加工配方的材料是渔获（蟹、鱼、蚌），
			// 它们只存在于 keepnet_items，item_def 里根本没有定义。
			if remaining > 0 {
				taken, kerr := srv.consumeKeepnetForDemandTx(cctx, tx, uid, demand, remaining,
					preferredKeepnet(req.Materials, demand.DemandID))
				if kerr != nil {
					return fail("扣渔获失败", "demand", demand.DemandID, "err", kerr)
				}
				for _, e := range taken {
					remaining--
					// 记真实鱼种（ed_crab），不是槽里的需求 id（fish_crab）。
					usedMats = appendMaterialUses(usedMats, e.Species, 1)
					keepnetTaken = append(keepnetTaken, e.EchoGUID)
					if !containsGUID(touched, e.EchoGUID) {
						touched = append(touched, e.EchoGUID)
					}
				}
			}
			if remaining > 0 {
				return fail("材料不足", "demand", demand.DemandID, "missing", remaining)
			}
		}
	} else if len(demands) > 0 {
		if len(req.Materials) == 0 {
			return fail("配方要求材料但请求没有材料槽")
		}
		byDemand := map[string]craftDemand{}
		for _, d := range demands {
			if d.DemandID != "" {
				byDemand[d.DemandID] = d
			}
		}
		rows, lerr := listItemsTx(cctx, tx, uid, []string{"backpack", "rig"})
		if lerr != nil {
			return fail("读取材料失败", "err", lerr)
		}
		byGUID := map[string]*store.Item{}
		for _, it := range rows {
			byGUID[it.GUID] = it
		}
		for _, slot := range req.Materials {
			if slot.GUID == "" {
				continue
			}
			// 材料槽按“每套需求 × 制作套数”扣减；槽的 ItemID 是需求 id。
			demand, hasDemand := byDemand[slot.ItemID]
			per := int64(1)
			if hasDemand {
				per = demand.need()
				if per < 1 {
					per = 1
				}
			}
			if per > math.MaxInt64/times {
				return fail("材料数量溢出", "demand", slot.ItemID, "times", times)
			}
			need := per * times
			consumed := false
			if row := byGUID[slot.GUID]; row != nil && row.Qty > 0 {
				got, cerr := consumeItemQtyTx(cctx, tx, row, need)
				if cerr != nil {
					return fail("扣指定材料失败", "guid", slot.GUID, "err", cerr)
				}
				if got < need {
					return fail("材料不足", "item", row.ItemID, "missing", need-got)
				}
				consumed = true
				usedMats = appendMaterialUses(usedMats, row.ItemID, got)
				if !containsGUID(touched, row.GUID) {
					touched = append(touched, row.GUID)
				}
			}
			if !consumed {
				if !hasDemand {
					return fail("材料槽既不在背包也无对应需求", "demand", slot.ItemID, "guid", slot.GUID)
				}
				alts := make(map[string]bool)
				for _, a := range demand.Alternatives {
					if a != "" {
						alts[a] = true
					}
				}
				cats := make(map[int64]bool)
				for _, c := range demand.Categories {
					cats[c] = true
				}
				remaining := need
				for _, it := range rows {
					if remaining <= 0 {
						break
					}
					if it.Qty <= 0 {
						continue
					}
					match := false
					if len(alts) > 0 {
						match = alts[it.ItemID]
					} else if len(cats) > 0 {
						realCat, ok := effectiveCategoryTx(cctx, tx, it.ItemID)
						match = ok && cats[realCat]
					}
					if !match {
						continue
					}
					got, cerr := consumeItemQtyTx(cctx, tx, it, remaining)
					if cerr != nil {
						return fail("按需求扣材料失败", "item", it.ItemID, "err", cerr)
					}
					if got > 0 && !containsGUID(touched, it.GUID) {
						touched = append(touched, it.GUID)
					}
					remaining -= got
					if got > 0 {
						usedMats = appendMaterialUses(usedMats, it.ItemID, got)
					}
					consumed = true
				}
				if remaining > 0 || !consumed {
					return fail("材料不足", "demand", demand.DemandID, "missing", remaining)
				}
			}
		}
		if len(touched) == 0 {
			return fail("配方要求材料但未扣到任何材料")
		}
	}

	if err := setItemName(producedRow, req.Name); err != nil {
		return fail("自定义名称写入失败", "err", err)
	}

	// 标本挂鱼（事务内）：定位→嵌鱼→消耗一体，任何一步失败整体回滚。
	if isTrophyMount {
		mf, note, merr := srv.mountedFishTreeTx(cctx, tx, uid, req.Materials, producedRow.GUID)
		if merr != nil {
			return fail("标本渔获读取失败", "err", merr)
		}
		if mf == nil {
			return fail("标本制作缺少有效渔获", "note", note)
		}
		if err := embedMountedFish(producedRow, mf); err != nil {
			return fail("标本嵌鱼失败", "err", err)
		}
		if err := srv.consumeMountedFishTx(cctx, tx, uid, mf.EchoGUID); err != nil {
			return fail("标本消耗渔获失败", "guid", shortGUID(mf.EchoGUID), "err", err)
		}
		couponGUID, cerr := srv.consumeCouponForTrophyTx(cctx, tx, uid, req.Materials)
		if cerr != nil {
			return fail("标本券扣除失败", "err", cerr)
		}
		if couponGUID != "" && !containsGUID(touched, couponGUID) {
			touched = append(touched, couponGUID)
			ctx.Log.Info("9/1 标本券已扣除", "guid", shortGUID(couponGUID), "recipe", recipe.RecipeID)
		}
		mounted = mf
		ctx.Log.Info("9/1 标本挂鱼", "species", mf.Species, "board", shortGUID(producedRow.GUID))
	}

	if isLeaderCraft {
		if err := srv.materializeCraftedLeaderTx(cctx, tx, producedRow, leaderMaterials); err != nil {
			return fail("引线成品物化失败", "err", err)
		}
	}

	// 本次产出量：可计件产物在 prepareNewItem 已按定义单位量×份数折算；
	// 引线物化只改子材料与强度，不动数量。
	producedQty := producedRow.Qty

	// 可计件产物（面包、茶、饲料等）并入已有同种背包堆，避免每次制作多出一个独立堆；
	// 窝料/PVA 棒每组独立占一格；拟饵、引线等非计件产物仍插入新实例。
	row, merged, err := grantPreparedItemTx(cctx, tx, producedRow, craftOutputMerges(outputDef))
	if err != nil {
		return fail("产出物入库失败", "output", outputID, "err", err)
	}
	// 自制窝料/PVA 棒：产物在客户端只有一个 item_id（feedcompleted），五个槽怎么
	// 配都叫这个名字，所以「这袋料到底用了什么」必须按实例记下来。等投进窝里
	// 再回查就晚了：那份料可能早被吃掉删掉，而窝还要活几个小时。
	if outputDef != nil && (outputDef.Category == catGroundbaitMix || outputDef.Category == catPVAStick) {
		if err := groundbaitRecordInstanceMaterialsTx(cctx, tx, row.GUID, usedMats); err != nil {
			return fail("窝料用料记录写入失败", "err", err)
		}
	}

	if err := addTxnTx(cctx, tx, uid, "craft", outputID, "",
		0, 0, fmt.Sprintf("recipe=%s qty=%d", recipe.RecipeID, times)); err != nil {
		return fail("制作流水写入失败", "err", err)
	}
	if err := tx.Commit(); err != nil {
		return fail("事务提交失败", "err", err)
	}
	skillCat := craftSkillCatForRecipe(recipe.RecipeID)
	masteryGain := srv.awardCraftProficiency(cctx, uid, skillCat, times)
	ctx.Log.Info("9/1 制作入库", "output", outputID, "qty", times,
		"took", len(touched), "recipe", recipe.RecipeID,
		"skill", skillCat, "mastery_gain", masteryGain)

	// 背包快照（803.f6 与 4/22）沿用合并后的整堆行；结果卡（803.f1）只放
	// 本次产出量，两者用各自独立的物品树，避免结果卡显示整堆数量。
	upsert := make([]*proto.Value, 0, 1+len(touched))
	var stackTree *proto.Value
	if tree, terr := srv.ItemToTree(cctx, row); terr == nil {
		stackTree = tree
		upsert = append(upsert, tree)
	} else {
		ctx.Log.Warn("9/1 背包快照构造失败", "output", outputID, "err", terr)
	}
	produced := make([]*proto.Value, 0, 1)
	if stackTree != nil {
		preview := stackTree
		if merged && producedQty > 0 && producedQty != row.Qty {
			if tree, perr := srv.craftProducedTree(cctx, row, producedQty); perr == nil {
				preview = tree
			} else {
				ctx.Log.Warn("9/1 制作结果对象构造失败", "output", outputID, "err", perr)
			}
		}
		produced = append(produced, preview)
	}
	var removed []string
	for _, g := range touched {
		r2, gerr := srv.DB.GetItemByGUID(cctx, g)
		if gerr != nil || r2 == nil {
			removed = append(removed, g)
		} else if tree, terr := srv.ItemToTree(cctx, r2); terr == nil {
			upsert = append(upsert, tree)
		}
	}
	// 渔获已在事务内消耗（鱼即成为标本，官方语义）；这里只补通知信号。
	if mounted != nil {
		removed = append(removed, mounted.EchoGUID)
		ctx.Log.Info("9/1 标本消耗渔获", "species", mounted.Species, "guid", shortGUID(mounted.EchoGUID))
	}
	var progress, skillTree *proto.Value
	if masteryGain > 0 {
		progress = masteryGainValue(skillCat, masteryGain)
		skillTree = srv.buildSingleSkill312(cctx, uid, skillCat)
	}
	ctx.AfterResponse(func() {
		if masteryGain > 0 {
			ctx.Sess.Push2_4SkillTree("制作 " + outputID)
		}
		if isVendCraft && vendTotal > 0 {
			// 扣了银币就刷新钱包 HUD（与商店购买同一信号）。
			ctx.Sess.Push2_1()
		}
		if mounted != nil {
			// 4/21 是物品移除信号：客户端据它把鱼护里的原鱼撤下
			// （与卖鱼/移动物品同一信号，见 boat.go / hItemRemove）。
			ctx.Sess.Push(4, 21, proto.Args524(proto.GuidArr([]string{mounted.EchoGUID})))
		}
		if len(keepnetTaken) > 0 {
			// 鱼类加工把渔获当材料吃掉，同样按 4/21 通知鱼护界面移除这些条目。
			ctx.Sess.Push(4, 21, proto.Args524(proto.GuidArr(keepnetTaken)))
		}
		if stackTree != nil {
			// 4/22 是背包快照，必须下发合并后的整堆数量。
			ctx.Sess.Push(4, 22, proto.Args(proto.S("61"), stackTree))
		}
	})
	// 制作成功事件：配方 ID 供 250-417 的制作类成就判断；烹饪熟练度只在
	// 本次是烹饪配方时装配，非烹饪制作保持 0，技能类成就便不会误判。
	cookPoints := float64(0)
	if skillCat == craftCookSkillID {
		cookPoints, _ = srv.DB.GetSkillPoints(cctx, uid, craftCookSkillID)
	}
	srv.evaluateAchievements(ctx, achievementEvent{
		Kind: evCraftOK, RecipeID: recipe.RecipeID, CookPoints: cookPoints,
	})
	return srv.replyCraftResultWithSkill(ctx, true, produced, upsert, removed, progress, skillTree)
}

// alignItemRowQty 将 item.qty 与可计件物品字段中的数量对齐，非计件物品保持 1。
// 扣料和材料校验都读取行数量，因此两处必须一致。
func (s *Server) alignItemRowQty(ctx context.Context, row *store.Item) {
	if row == nil || row.GUID == "" {
		return
	}
	count := int64(1)
	if def, err := s.DB.GetItemDef(ctx, row.NumericID); err == nil && def != nil &&
		countableCategories[def.Category] {
		if fields, ferr := FieldsFromJSON(row.FieldsJSON); ferr == nil {
			if q := itemQtyFieldIdx(row.WireClass); q >= 0 && q < len(fields) && fields[q].Num != nil {
				if v := int64(u64Of(fields[q].Num)); v > count {
					count = v
				}
			}
		}
	}
	if row.Qty != count {
		if err := s.DB.UpdateItemFields(ctx, row.GUID, row.FieldsJSON, count,
			row.Durability, row.Workmanship, row.RigGUID); err == nil {
			row.Qty = count
		}
	}
}

// hasMaterials 校验背包是否满足全部材料需求。
// alternatives 是可接受的 item id 列表，任一命中即可计入。
func (s *Server) hasMaterials(ctx context.Context, uid int64, ms []craftDemand) bool {
	rows, err := s.DB.ListItemsByContainers(ctx, uid, []string{"backpack", "rig"})
	if err != nil {
		return false
	}
	have := map[string]int64{}
	for _, it := range rows {
		have[it.ItemID] += s.stackCountOf(ctx, it)
	}
	for _, m := range ms {
		need := m.need()
		var got int64
		for _, a := range demandAccepts(m) {
			got += have[a]
		}
		if got < need {
			return false
		}
	}
	return true
}

// consumeMaterials 按需求扣除背包材料，支持 alternatives，并优先扣 item_id。
// 可堆叠材料按份扣除；返回被修改的 GUID 列表。
func (s *Server) consumeMaterials(ctx context.Context, uid int64, ms []craftDemand) []string {
	rows, err := s.DB.ListItemsByContainers(ctx, uid, []string{"backpack", "rig"})
	if err != nil {
		return nil
	}
	touched := []string{}
	for _, m := range ms {
		need := m.need()
		for _, accept := range demandAccepts(m) {
			if need <= 0 {
				break
			}
			for _, it := range rows {
				if need <= 0 {
					break
				}
				if it.ItemID != accept {
					continue
				}
				take := need
				if cnt := s.stackCountOf(ctx, it); cnt > 0 && cnt < take {
					take = cnt // min(need, 该堆现有份数)
				}
				got := s.consumeItemQty(ctx, it, take)
				need -= got
				if got > 0 && !containsGUID(touched, it.GUID) {
					touched = append(touched, it.GUID)
				}
			}
		}
	}
	return touched
}

// consumeDeclared 按客户端提交的材料槽扣除。槽位 f0 是需求 id，f1 是实例 GUID。
// 临时 GUID 无法在背包中定位时，再按需求 id 查找可用材料。
func (s *Server) consumeDeclared(ctx *Ctx, cctx context.Context, uid int64, slots []craftSlot, demands []craftDemand) []string {
	byDemand := map[string]craftDemand{}
	for _, d := range demands {
		if d.DemandID != "" {
			byDemand[d.DemandID] = d
		}
	}
	rows, _ := s.DB.ListItemsByContainers(cctx, uid, []string{"backpack", "rig"})
	byGUID := map[string]*store.Item{}
	for _, it := range rows {
		byGUID[it.GUID] = it
	}
	touched := []string{}
	var missing []string
	for _, sl := range slots {
		g := sl.GUID
		if g == "" {
			continue
		}
		if row, ok := byGUID[g]; ok {
			if s.consumeItemQty(cctx, row, 1) > 0 && !containsGUID(touched, g) {
				touched = append(touched, g)
			}
			continue
		}
		if demand, ok := byDemand[sl.ItemID]; ok {
			if hit := s.consumeByDemand(cctx, uid, demand); hit != "" {
				if !containsGUID(touched, hit) {
					touched = append(touched, hit)
				}
				continue
			}
		}
		missing = append(missing, fmt.Sprintf("%s/%s", sl.ItemID, shortGUID(g)))
	}
	if len(missing) > 0 {
		ctx.Log.Warn("9/1 材料槽有物品既不在背包、也无法按需求类目定位（仍放行产出）",
			"missing", missing)
	}
	return touched
}

// consumeByDemand 按一条需求的可接受类别/备选 item id 在背包里扣 1 份
// （9_1._consume_by_demand）。返回被扣物品的 guid；没扣到返回 ""。
func (s *Server) consumeByDemand(ctx context.Context, uid int64, d craftDemand) string {
	alts := make([]string, 0, len(d.Alternatives))
	for _, a := range d.Alternatives {
		if a != "" {
			alts = append(alts, a)
		}
	}
	cats := map[int64]bool{}
	for _, c := range d.Categories {
		cats[c] = true
	}
	rows, _ := s.DB.ListItemsByContainers(ctx, uid, []string{"backpack", "rig"})
	for _, it := range rows {
		iid := it.ItemID
		switch {
		case len(alts) > 0:
			if !containsStr(alts, iid) {
				continue
			}
		case len(cats) > 0:
			// effectiveCategory 与 class_for_item 同一口径（套盒解包到内含物）
			realCat, ok := s.effectiveCategory(ctx, iid)
			if !ok || !cats[realCat] {
				continue
			}
		default:
			// 只有标签组（gfhgeippgdj）：服务端无法从标签反查物品，交给下一条
			return ""
		}
		if s.consumeItemQty(ctx, it, 1) > 0 {
			ctxLog := s.log
			ctxLog.Info("9/1 材料按需求类目扣除", "item", iid, "demand", d.DemandID)
			return it.GUID
		}
	}
	return ""
}

// consumeItemQty 从实例中按份扣除 n，返回实际扣除数量。
//
// 可堆叠物品的数量同时保存在 item.qty 和 ljnnpokapig 字段中，两处必须同步。
// 数量降至 0 或不足时删除整件及其 container_slot、rig_component 关联。
// 当前数量以 stackCountOf 为准，无字段时回退到定义值。
func (s *Server) consumeItemQty(ctx context.Context, row *store.Item, n int64) int64 {
	if row == nil || row.GUID == "" {
		return 0
	}
	have := s.stackCountOf(ctx, row)
	if have < 1 {
		have = 1
	}
	take := n
	if take < 1 {
		take = 1
	}
	if have <= take {
		if err := s.DB.DeleteItemCascade(ctx, row.GUID); err != nil {
			return 0
		}
		row.Qty = 0
		return have
	}
	left := have - take
	if fields, err := FieldsFromJSON(row.FieldsJSON); err == nil && len(fields) > 0 {
		// set_item_fields 路径：改 ljnnpokapig + qty 镜像列 + 抬版本戳
		if q := itemQtyFieldIdx(row.WireClass); q >= 0 && q < len(fields) {
			fields[q].Num = ptrU64(uint64(left))
		}
		bumpCreatedTicks(fields, row.WireClass)
		if fj, jerr := FieldsToJSON(fields); jerr == nil {
			_ = s.DB.UpdateItemFields(ctx, row.GUID, fj, left, row.Durability, row.Workmanship, row.RigGUID)
			row.FieldsJSON = fj
		}
	} else {
		// set_item_qty 路径：退化行只更 qty 列（不动 fields_json）
		_ = s.DB.UpdateItemQty(ctx, row.GUID, left)
	}
	row.Qty = left
	return take
}

// effectiveCategory 物品的有效类别（shop_rules._effective 的类别半边：
// 套盒(55)解包到内含物 attrs nbkhfhenbag[0].cemjmlpafmc 的类别）。
func (s *Server) effectiveCategory(ctx context.Context, itemID string) (int64, bool) {
	def, err := s.DB.GetItemDefByItemID(ctx, itemID)
	if err != nil || def == nil {
		return 0, false
	}
	if def.Category != bundleCategory {
		return def.Category, true
	}
	if bag, ok := defAttrs(def)["nbkhfhenbag"].([]any); ok {
		for _, ent := range bag {
			m, ok := ent.(map[string]any)
			if !ok {
				continue
			}
			inner, _ := m["cemjmlpafmc"].(string)
			if inner == "" {
				continue
			}
			if di, derr := s.DB.GetItemDefByItemID(ctx, inner); derr == nil && di != nil {
				return di.Category, true
			}
			break // 内含物无定义时保留套盒自身类别
		}
	}
	return def.Category, true
}

// ---- 9/3 分解/采集 ----

// shovelBaitDrop 是铲子可挖出的自然诱饵。
// 每次挖出 1-3 种，每种 1-5 个；同类诱饵并入已有背包堆。
type shovelBaitDrop struct {
	ItemID string
	Name   string
}

type shovelBaitLoot struct {
	shovelBaitDrop
	Qty int
}

var shovelBaitDrops = []shovelBaitDrop{
	{ItemID: "worm_01", Name: "蚯蚓"},
	{ItemID: "vypolzok_01", Name: "地龙"},
	{ItemID: "maggot", Name: "蛆虫"},
	{ItemID: "maggot_caster", Name: "蛆虫蛹"},
	{ItemID: "bloodworm", Name: "红虫"},
	{ItemID: "waterbug", Name: "龙虱甲虫"},
}

func isShovelItemID(itemID string) bool {
	return itemID == "tool_shovel" || itemID == "shovel_ny25" || itemID == "tool_scoop"
}

const baitCatchSkillCat = 4

// rollShovelBaitLoot 抽取 1-3 种不重复诱饵，每种数量 1-5。
func rollShovelBaitLoot(rng *rand.Rand) []shovelBaitLoot {
	if rng == nil || len(shovelBaitDrops) == 0 {
		return nil
	}
	nTypes := 1 + rng.Intn(3)
	if nTypes > len(shovelBaitDrops) {
		nTypes = len(shovelBaitDrops)
	}
	order := rng.Perm(len(shovelBaitDrops))[:nTypes]
	out := make([]shovelBaitLoot, 0, len(order))
	for _, idx := range order {
		out = append(out, shovelBaitLoot{
			shovelBaitDrop: shovelBaitDrops[idx],
			Qty:            1 + rng.Intn(5),
		})
	}
	return out
}

// hCraftMine2 处理 9/3：铲子触发挖饵，其它物品保持原有分解行为。
func hCraftMine2(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	// 同一命令也被客户端用于铲子/红虫长柄勺挖饵。先识别工具；
	// 不是挖饵工具时才继续走作者原有的分解分支。
	if body, handled := srv.tryBaitDig(ctx); handled {
		return body, true
	}
	guids := uniqueStrings(deepGUIDs(ctx.Tree))
	if len(guids) == 0 {
		return nil, false // 没有 GUID 时交给兜底链
	}
	cctx := ctx.Sess.pushCtx()
	if len(guids) == 1 {
		if row, err := srv.DB.GetItemByGUID(cctx, guids[0]); err == nil && row != nil &&
			row.UID == ctx.Sess.Userid && isShovelItemID(row.ItemID) {
			return srv.digBaitWithShovel(ctx, row)
		}
	}
	tx, err := beginEconomyTx(cctx, srv.DB)
	if err != nil {
		ctx.Log.Warn("9/3 开启事务失败", "err", err)
		return srv.replyCraftResult(ctx, false, nil, nil, nil)
	}
	defer func() { _ = tx.Rollback() }()
	n, err := removeItemsTx(cctx, tx, ctx.Sess.Userid, guids)
	if err != nil {
		ctx.Log.Warn("9/3 删除物品失败", "err", err)
		return srv.replyCraftResult(ctx, false, nil, nil, nil)
	}
	if err := addTxnTx(cctx, tx, ctx.Sess.Userid, "mine", "", guids[0], 0, 0,
		fmt.Sprintf("decompose %d", n)); err != nil {
		ctx.Log.Warn("9/3 流水写入失败", "err", err)
		return srv.replyCraftResult(ctx, false, nil, nil, nil)
	}
	if err := tx.Commit(); err != nil {
		ctx.Log.Warn("9/3 提交失败", "err", err)
		return srv.replyCraftResult(ctx, false, nil, nil, nil)
	}
	ctx.Log.Info("9/3 分解", "count", n)
	return srv.replyCraftResult(ctx, n > 0, nil, nil, nil)
}

// digBaitWithShovel 完成一次铲子挖饵：铲子保留，诱饵入背包并随 803.f1 返回。
func (s *Server) digBaitWithShovel(ctx *Ctx, shovel *store.Item) ([]byte, bool) {
	cctx := ctx.Sess.pushCtx()
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	loot := rollShovelBaitLoot(rng)
	if len(loot) == 0 {
		return s.replyCraftResult(ctx, false, nil, nil, nil)
	}

	prepared := make([]*store.Item, 0, len(loot))
	for _, item := range loot {
		it, err := s.prepareNewItemWithCount(cctx, ctx.Sess.Userid, item.ItemID, "backpack", int64(item.Qty), "")
		if err != nil {
			ctx.Log.Warn("9/3 挖饵构造失败", "item", item.ItemID, "err", err)
			return s.replyCraftResult(ctx, false, nil, nil, nil)
		}
		prepared = append(prepared, it)
	}

	// 体力参数必须在开经济事务之前取好（tunableF 首次装载有 2 秒查询超时）。
	condParams := s.conditionParamsForUID(cctx, ctx.Sess.Userid)
	condCost := s.baitDigEnergyCost()
	condEnv := s.conditionEnvForUID(ctx.Sess.Userid)

	tx, err := beginEconomyTx(cctx, s.DB)
	if err != nil {
		ctx.Log.Warn("9/3 挖饵开启事务失败", "err", err)
		return s.replyCraftResult(ctx, false, nil, nil, nil)
	}
	defer func() { _ = tx.Rollback() }()

	// 与饵入包同事务：体力不足直接回滚，不会出现"给了饵没扣体力"。
	spent, err := spendEnergyTx(cctx, tx, ctx.Sess.Userid, condParams, condEnv, condCost)
	if err != nil {
		if errors.Is(err, errEnergyNotEnough) {
			ctx.Log.Info("9/3 铲子挖饵被拒：精力不足", "shovel", shortGUID(shovel.GUID),
				"cost", condCost, "enabled", condParams.Enabled)
		} else {
			ctx.Log.Warn("9/3 挖饵精力结算失败", "err", err)
		}
		return s.replyCraftResult(ctx, false, nil, nil, nil)
	}

	granted := make([]*store.Item, 0, len(prepared))
	parts := make([]string, 0, len(loot))
	for i, it := range prepared {
		row, _, err := grantPreparedItemTx(cctx, tx, it, true)
		if err != nil {
			ctx.Log.Warn("9/3 挖饵入包失败", "item", it.ItemID, "err", err)
			return s.replyCraftResult(ctx, false, nil, nil, nil)
		}
		granted = append(granted, row)
		parts = append(parts, fmt.Sprintf("%sx%d", loot[i].ItemID, loot[i].Qty))
	}
	if err := addTxnTx(cctx, tx, ctx.Sess.Userid, "dig_bait", shovel.ItemID, shovel.GUID, 0, 0,
		"shovel "+strings.Join(parts, ",")); err != nil {
		ctx.Log.Warn("9/3 挖饵流水写入失败", "err", err)
		return s.replyCraftResult(ctx, false, nil, nil, nil)
	}
	if err := tx.Commit(); err != nil {
		ctx.Log.Warn("9/3 挖饵提交失败", "err", err)
		return s.replyCraftResult(ctx, false, nil, nil, nil)
	}

	produced := make([]*proto.Value, 0, len(granted))
	for _, row := range granted {
		tree, err := s.ItemToTree(cctx, row)
		if err != nil {
			ctx.Log.Warn("9/3 挖饵响应构造失败", "item", row.ItemID, "err", err)
			return s.replyCraftResult(ctx, true, nil, nil, nil)
		}
		produced = append(produced, tree)
	}
	skillGain := s.awardSkillProficiency(cctx, ctx.Sess.Userid, baitCatchSkillCat)
	progress := wiregen.MustMakeObj("kegeppimjbm", map[int]*proto.Value{
		0: proto.U64(0),
		1: proto.U32(0),
		2: proto.U32(baitCatchSkillCat),
		3: proto.F32(float32(skillGain)),
		4: proto.Null(),
		5: proto.U32(0),
		6: proto.Args(proto.S("31")),
	})
	skillTree := s.buildSingleSkill312(cctx, ctx.Sess.Userid, baitCatchSkillCat)
	ctx.Log.Info("9/3 铲子挖饵", "shovel", shortGUID(shovel.GUID), "types", len(loot),
		"items", strings.Join(parts, ","), "skill_gain", skillGain, "energy_spent", spent)
	if spent > 0 {
		// 体力条不在 9/3 应答里，扣完立刻补推一条 2/2。
		ctx.AfterResponse(func() { pushKeepalive(ctx.Sess) })
	}
	return s.replyCraftResultWithSkill(ctx, true, produced, produced, nil, progress, skillTree)
}

// removeItems 删除当前玩家的若干物品并清理关联记录，返回删除件数。
func (s *Server) removeItems(ctx context.Context, uid int64, guids []string) int64 {
	n := int64(0)
	for _, g := range guids {
		deleted, err := s.DB.DeleteItemOfUID(ctx, uid, g)
		if err == nil && deleted {
			n++
		}
		_ = s.DB.DeleteItemRefs(ctx, g)
	}
	return n
}

// mountedFish 标本挂鱼结果：嵌进板子树的鱼对象 + 被消耗的鱼护条目。
type mountedFish struct {
	Tree     *proto.Value
	EchoGUID string
	Species  string
}

// mountedFishTreeTx 标本挂鱼对象（事务内）：按材料槽（bhfikaplhmh {需求id,
// 鱼护EchoGUID, 鱼种}）在鱼护里定位该渔获，按鱼护同款 25 类构造鱼对象，
// guid 以板子 guid 派生（uuid5，避免与鱼护内原鱼同 guid）。
// 返回 (nil, 原因, nil) = 没有槽位指向有效鱼护记录（槽位 GUID 是普通材料
// 或已不存在的 echo guid）；返回 (nil, "", err) = 读取异常；两者都要求
// 调用方（猫 71 制作）以 803(false) 收场——猫 71 的 52 个配方全部是标本
// 板/支架/吊顶，没有渔获就没有标本，静默放行会产出空白板。
// 鱼种不符同样拒绝：客户端槽位鱼种与鱼护实不符意味着引用已错位。
func (s *Server) mountedFishTreeTx(ctx context.Context, tx *sql.Tx, uid int64, slots []craftSlot, boardGUID string) (*mountedFish, string, error) {
	for _, sl := range slots {
		if sl.GUID == "" {
			continue
		}
		entry, err := s.DB.KeepnetGetTx(ctx, tx, uid, sl.GUID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return nil, "", err
		}
		if sl.Species != "" && entry.Species != "" && sl.Species != entry.Species {
			return nil, fmt.Sprintf("鱼种不符 slot=%s keepnet=%s", sl.Species, entry.Species), nil
		}
		s.fillKeepnetVariant(entry)
		fish := buildKeepnet25(entry, "", store.SpeciesRarityTx(ctx, tx, entry.Species), entry.ModelVariant)
		if idx := fieldIdxByCS("ecknbkpeeji", csGUID); idx >= 0 && idx < len(fish.Fields) {
			fish.Fields[idx].Hex = uuid5OIDHex(boardGUID + "/mounted_fish")
		}
		return &mountedFish{Tree: fish, EchoGUID: entry.EchoGUID, Species: entry.Species}, "", nil
	}
	return nil, "材料槽没有指向有效鱼护记录", nil
}

// embedMountedFish 把渔获对象嵌进板子字段 f1 peipahjmida 并序列化回快照。
// 任何失败都应让制作整体失败：无鱼的板子客户端渲染为空白标本。
func embedMountedFish(row *store.Item, mf *mountedFish) error {
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return fmt.Errorf("板子字段解析失败: %w", err)
	}
	idx := fieldIdxByCS(row.WireClass, "peipahjmida")
	if idx < 0 || idx >= len(fields) {
		return fmt.Errorf("wire 类 %s 无 peipahjmida 字段", row.WireClass)
	}
	fields[idx] = proto.Field{Op: "VAL", Val: mf.Tree}
	fj, err := FieldsToJSON(fields)
	if err != nil {
		return fmt.Errorf("板子字段序列化失败: %w", err)
	}
	row.FieldsJSON = fj
	return nil
}

// consumeMountedFishTx 事务内消耗渔获：护网条目 + 导入快照镜像行 +
// 槽位/钓组关联一并清理（与旧 KeepnetDelete+DeleteItemOfUID+DeleteItemRefs
// 同语义）。条目不存在返回错误——消费必须"恰好一次"，杜绝同一尾鱼双开。
func (s *Server) consumeMountedFishTx(ctx context.Context, tx *sql.Tx, uid int64, guid string) error {
	deleted, err := s.DB.DeleteKeepnetTx(ctx, tx, uid, guid)
	if err != nil {
		return err
	}
	if !deleted {
		return fmt.Errorf("鱼护条目不存在或已被消费")
	}
	// 镜像行兜底清理：type_id 非 25 的残留行也一并移除（正常不存在）。
	if _, err := tx.ExecContext(ctx, `DELETE FROM item WHERE uid=? AND guid=?`, uid, guid); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM container_slot WHERE guid=?`, guid); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM rig_component WHERE guid=?`, guid)
	return err
}

// isCouponMaterialSlot 判定材料槽是否指向梦鱼优惠券。客户端用需求 id
// mat_coupon_chuchelo 表达，f2 鱼种位回填 coupon_chuchelo；两种写法都认。
func isCouponMaterialSlot(sl craftSlot) bool {
	return sl.ItemID == couponChucheloItemID ||
		sl.ItemID == "mat_"+couponChucheloItemID ||
		sl.Species == couponChucheloItemID
}

// consumeCouponForTrophyTx 事务内按次扣一张梦鱼优惠券，返回被扣券的 GUID。
//
// 券在标本板材料里是「必要:否」的可选材料：槽位缺失、券已被扣空、或玩家
// 根本没带券，都不阻断制作（免费标本板制作本来就不依赖券）。但槽位明确
// 带了券且券还在背包时**必须**扣掉：标本配方的 materials_json 为空，hCraft
// 会走 len(demands)==0 分支跳过全部材料消费，不能指望那条路径。
//
// 返回值由调用方放进 touched：券还有余额时 803.f6 会带出新数量，扣空时
// item 行已删，803.f8 会带上该 GUID 让客户端把券从背包撤下。
func (s *Server) consumeCouponForTrophyTx(ctx context.Context, tx *sql.Tx, uid int64, slots []craftSlot) (string, error) {
	for _, sl := range slots {
		if sl.GUID == "" || !isCouponMaterialSlot(sl) {
			continue
		}
		row, err := getOwnedItemTx(ctx, tx, uid, sl.GUID)
		if errors.Is(err, sql.ErrNoRows) {
			continue // 可选材料：券已不存在不阻断制作
		}
		if err != nil {
			return "", err
		}
		if row.ItemID != couponChucheloItemID {
			continue // 同 GUID 落在别的物品上，说明槽位引用错位，忽略
		}
		n, cerr := consumeItemQtyTx(ctx, tx, row, 1)
		if cerr != nil {
			return "", cerr
		}
		if n > 0 {
			return row.GUID, nil
		}
	}
	return "", nil
}

// ---- 9/4 制作询价 ----

// hCraftCost 处理 9/4 制作询价，返回 501 价格行：
// STR 配方 systemID、U64 银价、U64 金价、U32 数量。
// 家用版制作只扣材料、不收费，价格为 0；贩卖机配方（*_vend）按货架最低价
// 报真实总价，数量按产出品类上限收敛，保证与 9/1 的实际扣款一致。
func hCraftCost(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	req := craftPayloadOf(ctx)
	qty := int64(req.Qty)
	if qty == 0 {
		qty = 1
	}
	silver := int64(0)
	cctx := ctx.Sess.pushCtx()
	if recipe := srv.resolveRecipe(cctx, req.Recipe); recipe != nil && vendRecipeReports(recipe.RecipeID) {
		if def, derr := srv.DB.GetItemDefByItemID(cctx, recipe.OutputID); derr == nil && def != nil {
			qty = craftTimesFor(craftBatchSpecFor(def), req.Qty)
		}
		if charges, err := srv.resolveVendCharges(cctx, parseDemands(recipe.MaterialsJSON), req.Materials); err == nil {
			if total, terr := srv.vendTotalPrice(cctx, charges, qty); terr == nil {
				silver = total
			} else {
				ctx.Log.Warn("9/4 贩卖机配料定价失败", "recipe", recipe.RecipeID, "err", terr)
			}
		} else {
			ctx.Log.Warn("9/4 贩卖机配料解析失败", "recipe", recipe.RecipeID, "err", err)
		}
	}
	row := wiregen.MustMakeObj("ifdefblccde", map[int]*proto.Value{
		0: proto.StrVal(req.Recipe),  // f0 systemID（必须与请求一致）
		1: proto.U64(uint64(silver)), // f1 银价（贩卖机配方为配料总价）
		2: proto.U64(0),              // f2 金币价
		3: proto.U32(uint32(qty)),    // f3 数量（贩卖机配方收敛后数量）
	})
	ctx.Log.Info("9/4 询价", "recipe", req.Recipe, "qty", qty, "silver", silver,
		"materials", len(req.Materials), "tools", len(req.Tools), "source", req.Source)
	body, err := proto.EncodeValue(row, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// ---- 9/6 添加配方模板 ----

// craftTemplateElemCode 是 171 的 f3/f4 元素容器 args code。
//
// 元素类必须是 hbokamnohjb（type_id 170，DTO bpdjphlfjdj）：客户端
// dmolhiobcbb.plphdfehkjl 把这两格读成 bpdjphlfjdj[]，发 17(kdpbhdffjaf)
// 会在 lpdfanjcalm<T> 抛 InvalidCastException，保存回调抛出后面板卡在
// busy 态（Player.log 2026-11-11 12:46:32 实栈）。
// 170 -> 96 取自 argcode_table.json 的 by_type_id；9/5 的 171 列表用 97，
// 96/97 成对，互证这组映射。
const craftTemplateElemCode = "96"

// craftTemplateListCode 是 9/5 响应里 171 列表的容器 args code。
// by_cmd["9/5"]="97" 且 by_type_id["171"]="97"，两处一致。
const craftTemplateListCode = "97"

// hCraftAddTemplate 处理 9/6 保存配方模板。
//
// 请求体就是一整个 171 对象（实抓 2026-11-11 14:29:20）：
//
//	f0 GUID（客户端未分配时为全零）
//	f1 模板名（玩家填的「奶油1」）
//	f2 配方键（craft_feed_dry）
//	f3 args(96) 工具条目、f4 args(96) 材料条目，元素 170 = {需求key, 序号, 真实物品id}
//
// 客户端已经把模板内容算好了，服务端只做两件事：补一个稳定 GUID、落库，
// 然后按 171 回包。不再按配方重新推导材料——那会把玩家选的具体物品丢掉。
func hCraftAddTemplate(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	cctx := ctx.Sess.pushCtx()

	tmpl, ok := craftTemplateFromRequest(ctx.Tree)
	if !ok {
		ctx.Log.Warn("9/6 请求体不是合法 171 模板（缺字段或 f3/f4 形状不符）")
		return []byte{0x00}, true // ctx.reply(None)
	}
	if tmpl.Fields[2].StrOr() == "" {
		ctx.Log.Warn("9/6 请求缺配方键")
		return []byte{0x00}, true
	}
	recipe := srv.resolveRecipe(cctx, tmpl.Fields[2].StrOr())
	if recipe == nil {
		ctx.Log.Warn("9/6 未知配方", "recipe", tmpl.Fields[2].StrOr())
		return []byte{0x00}, true
	}

	guid := tmpl.Fields[0].GuidHex()
	if guid == "" || guid == zeroGUIDHex {
		guid = uuid5OIDHex(fmt.Sprintf("%d:%s:%s", ctx.Sess.Userid, recipe.RecipeID, tmpl.Fields[1].StrOr()))
	}
	tmpl.Fields[0] = proto.FGuid(guid)

	// f3/f4 已由 craftTemplateFromRequest 校验为非 nil 的 args(96) 容器，
	// 这里只解析一次，落库与日志共用结果，不再二次解引用字段。
	toolsJSON, toolCount := craftTemplateEntriesJSON(tmpl.Fields[3].Val)
	materialsJSON, materialCount := craftTemplateEntriesJSON(tmpl.Fields[4].Val)

	now := float64(time.Now().Unix())
	row := &store.CraftTemplate{
		UID:           ctx.Sess.Userid,
		GUID:          guid,
		RecipeID:      recipe.RecipeID,
		Name:          tmpl.Fields[1].StrOr(),
		Kind:          tmpl.Fields[2].StrOr(),
		ToolsJSON:     toolsJSON,
		MaterialsJSON: materialsJSON,
		CreatedTs:     now,
		UpdatedTs:     now,
	}
	if err := srv.DB.SaveCraftTemplate(cctx, row); err != nil {
		ctx.Log.Error("9/6 模板落库失败", "err", err, "recipe", recipe.RecipeID)
		return []byte{0x00}, true
	}

	body, err := proto.EncodeValue(tmpl, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	ctx.Log.Info("9/6 配方模板", "recipe", recipe.RecipeID, "guid", guid,
		"name", row.Name, "tools", toolCount, "materials", materialCount)
	return body, true
}

// zeroGUIDHex 是客户端「还没分配模板 id」时下发的全零 GUID。
const zeroGUIDHex = "00000000000000000000000000000000"

// hCraftDeleteTemplate 处理 9/7 删除配方模板，响应 null。
//
// 请求里带模板 GUID（16 字节 hex）；只删本人模板，别人的 GUID 传进来删不到。
// 模板本来就不存在时同样回 null——删除是幂等的，客户端不需要区分。
func hCraftDeleteTemplate(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if srv == nil || srv.DB == nil {
		return []byte{0x00}, true
	}
	guid := templateGUIDOf(ctx.Tree)
	if guid == "" {
		ctx.Log.Warn("9/7 请求形状不认识或缺少模板 GUID，按失败关闭不执行删除")
		return []byte{0x00}, true
	}
	deleted, err := srv.DB.DeleteCraftTemplate(ctx.Sess.pushCtx(), ctx.Sess.Userid, guid)
	if err != nil {
		ctx.Log.Error("9/7 模板删除失败", "err", err, "guid", guid)
	}
	ctx.Log.Info("9/7 删除配方模板", "guid", guid, "deleted", deleted)
	return []byte{0x00}, true
}

// templateGUIDOf 取 9/7 的模板 GUID。
//
// 实抓形状（2026-11-11 15:52:57，len=24）是 args(524) 的第一个参数直接就是
// GUID：`03 03 "524" 0100 0c <16 bytes>`。因此只认顶层 args 的第 0 个元素，
// 不再整树找"第一个 32 位 hex"——那样一旦请求里先出现容器/玩家 GUID，
// 就会删错行。形状不认识时返回空串（fail closed，宁可不删也不删错）。
func templateGUIDOf(root *proto.Value) string {
	if root == nil {
		return ""
	}
	if root.Kind != proto.KindArgs && root.Kind != proto.KindValList {
		return ""
	}
	if len(root.Items) == 0 {
		return ""
	}
	return guidOfValue(root.Items[0])
}

// guidOfValue 取单个 GUID 值；非 GUID 或全零返回空串。
func guidOfValue(v *proto.Value) string {
	if v == nil || v.Kind != proto.KindGuid {
		return ""
	}
	if len(v.Hex) != 32 || v.Hex == zeroGUIDHex {
		return ""
	}
	return v.Hex
}

// craftTemplateEntryTypeID / craftTemplateEntryClass 是 171.f3/f4 的元素类：
// hbokamnohjb(170)，DTO bpdjphlfjdj。客户端按 bpdjphlfjdj[] 强转。
const (
	craftTemplateEntryTypeID = 170
	craftTemplateEntryClass  = "hbokamnohjb"
)

// craftTemplateFromRequest 从请求树里取 171 模板对象并校验 f3/f4 形状。
//
// 只找到 171 还不够：f3/f4 是 VAL 字段，畸形请求可以让它是 null、标量或
// 别的类型。不校验就往下走会在解引用 Val.Items 时 panic（handler 层没有
// recover，panic 会打死整个 gameserver 进程），或者在 panic 之前先落库一条
// 空模板脏数据。因此形状不符一律返回 false，由调用方回 null，既不落库也不崩。
func craftTemplateFromRequest(root *proto.Value) (*proto.Value, bool) {
	var found *proto.Value
	var walk func(v *proto.Value)
	walk = func(v *proto.Value) {
		if v == nil || found != nil {
			return
		}
		if v.Kind == proto.KindObj {
			if v.TypeID == 171 || v.Class == "dmolhiobcbb" {
				found = v
				return
			}
			for i := range v.Fields {
				if v.Fields[i].Val != nil {
					walk(v.Fields[i].Val)
				}
			}
			return
		}
		if v.Kind == proto.KindArgs || v.Kind == proto.KindValList ||
			v.Kind == proto.KindArray || v.Kind == proto.KindTypedArr {
			for _, it := range v.Items {
				walk(it)
			}
		}
	}
	walk(root)
	if found == nil || len(found.Fields) < 5 {
		return nil, false
	}
	if !craftTemplateEntryListValid(found.Fields[3].Val) {
		return nil, false
	}
	if !craftTemplateEntryListValid(found.Fields[4].Val) {
		return nil, false
	}
	return found, true
}

// craftTemplateEntryListValid 校验 171.f3/f4：必须是 args 容器，且每个元素
// 都是 hbokamnohjb(170) 三字段对象；空列表合法。
func craftTemplateEntryListValid(v *proto.Value) bool {
	if v == nil || v.Kind != proto.KindArgs {
		return false
	}
	for _, it := range v.Items {
		if it == nil || it.Kind != proto.KindObj || len(it.Fields) < 3 {
			return false
		}
		if it.TypeID != craftTemplateEntryTypeID && it.Class != craftTemplateEntryClass {
			return false
		}
	}
	return true
}

// craftTemplateEntriesJSON 把 170 条目列表序列化成存取用的 JSON，并返回条数。
//
// 只接受 hbokamnohjb(170) 元素：旧客户端或畸形请求可能塞进别的三字段对象，
// 不校验就会把任意对象当成模板条目存下来，9/5 再原样下发成坏模板。
func craftTemplateEntriesJSON(v *proto.Value) (string, int) {
	if v == nil || v.Kind != proto.KindArgs || len(v.Items) == 0 {
		return "[]", 0
	}
	type entry struct {
		Desc   string `json:"desc"`
		Idx    uint32 `json:"idx"`
		ItemID string `json:"item_id"`
	}
	out := make([]entry, 0, len(v.Items))
	for _, it := range v.Items {
		if it == nil || it.Kind != proto.KindObj || len(it.Fields) < 3 {
			continue
		}
		if it.TypeID != craftTemplateEntryTypeID && it.Class != craftTemplateEntryClass {
			continue
		}
		e := entry{Desc: it.Fields[0].StrOr(), ItemID: it.Fields[2].StrOr()}
		if n := it.Fields[1].Num; n != nil {
			e.Idx = uint32(*n)
		}
		out = append(out, e)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "[]", 0
	}
	return string(raw), len(out)
}

// craftTemplateEntriesFromJSON 还原 170 条目列表；坏数据返回空列表而不是报错，
// 让单条模板坏掉时不影响整个 9/5 列表。
func craftTemplateEntriesFromJSON(raw string) []*proto.Value {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	type entry struct {
		Desc   string `json:"desc"`
		Idx    uint32 `json:"idx"`
		ItemID string `json:"item_id"`
	}
	var ents []entry
	if err := json.Unmarshal([]byte(raw), &ents); err != nil {
		return nil
	}
	out := make([]*proto.Value, 0, len(ents))
	for _, e := range ents {
		out = append(out, wiregen.MustMakeObj("hbokamnohjb", map[int]*proto.Value{
			0: proto.StrVal(e.Desc),
			1: proto.U32(e.Idx),
			2: proto.StrVal(e.ItemID),
		}))
	}
	return out
}

// craftTemplate 构造 171 配方模板：f0 GUID、f1 名称、f2 分类（保存时为配方键）、
// f3 工具、f4 材料，两格都是 args(96) 的 hbokamnohjb(170) 列表。
func craftTemplate(guid, name, kind string, tools, materials []*proto.Value) *proto.Value {
	return wiregen.MustMakeObj("dmolhiobcbb", map[int]*proto.Value{
		0: proto.Guid(guid),
		1: proto.StrVal(name),
		2: proto.StrVal(kind),
		3: proto.Args(proto.S(craftTemplateElemCode), tools...),
		4: proto.Args(proto.S(craftTemplateElemCode), materials...),
	})
}

// ---- 803 构造 ----

// craftProducedTree 构造 803.f1 的制作结果对象：数量只取本次产出量。
// 可计件产物并入已有堆后 row.Qty 是整堆总量，直接下发会让结果卡显示
// "整堆数量"而不是本次做了多少；GUID 与物理字段仍沿用合并后的行，
// 保证客户端按同一实例渲染。f6 与 4/22 的背包快照必须用原行，不能复用本函数。
func (s *Server) craftProducedTree(ctx context.Context, row *store.Item, producedQty int64) (*proto.Value, error) {
	if row == nil {
		return nil, fmt.Errorf("空物品行")
	}
	if producedQty < 1 {
		return nil, fmt.Errorf("无效产出数量 %d", producedQty)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return nil, err
	}
	idx := itemQtyFieldIdx(row.WireClass)
	if idx < 0 || idx >= len(fields) {
		return nil, fmt.Errorf("物品 %s 没有数量字段", row.ItemID)
	}
	fields[idx].Num = ptrU64(uint64(producedQty))
	fj, err := FieldsToJSON(fields)
	if err != nil {
		return nil, err
	}
	preview := *row
	preview.Qty = producedQty
	preview.FieldsJSON = fj
	return s.ItemToTree(ctx, &preview)
}

// replyCraftResult 构造 803 制作/分解结果：
// f1 是制作结果预览，f6 是背包增量（产出 + 剩余材料），f8 是耗尽移除的 GUID。
func (s *Server) replyCraftResult(ctx *Ctx, ok bool, produced, upsert []*proto.Value, removed []string) ([]byte, bool) {
	return s.replyCraftResultWithSkill(ctx, ok, produced, upsert, removed, nil, nil)
}

// replyCraftResultWithSkill 额外写入 f2 310 进度块和 f3 单大类技能树。
func (s *Server) replyCraftResultWithSkill(ctx *Ctx, ok bool, produced, upsert []*proto.Value,
	removed []string, progress, skillTree *proto.Value) ([]byte, bool) {
	obj := wiregen.MustMakeObj("beckiiffkem", map[int]*proto.Value{
		0: proto.U32(boolU32(ok)),
		1: proto.Args(proto.S(listCodeForObjs(produced)), produced...),
		2: proto.Null(),
	})
	if progress != nil {
		obj.Fields[2].Val = progress
	}
	if skillTree != nil {
		obj.Fields[3].Val = skillTree
	}
	if len(upsert) > 0 {
		// f6 是客户端背包增量，必须同步产出和仍有剩余的消耗材料。
		obj.Fields[6].Val = proto.Args(proto.S("61"), upsert...)
	}
	if len(removed) > 0 {
		// f8 是耗尽材料的移除列表。
		obj.Fields[8].Val = proto.GuidArr(removed)
	}
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		ctx.Log.Warn("803 编码失败", "err", err)
		return nil, false
	}
	return body, true
}

// listCodeOverrides 个别元素 type_id 的容器 code 显式覆盖（优先于
// by_elem/by_type_id）。只放有实抓断线证据、且合成段结果已验证有毒的条目，
// 不做全局策略变更——by_type_id 段虽是按"属性 id 顺序补全"的合成表，但
// cat 46 窝料(120→49)、cat 30 干饵(145→74)、cat 19 香精(125→54)、cat 61
// (3103→193)、9/6 需求对象(17→18) 等既有制作/模板一直按它下发且客户端
// 接受，全量改回落 61 属于无回归矩阵的行为变更。
var listCodeOverrides = map[string]string{
	// cat 71 标本实例 (13001)：by_type_id 合成段给 471，客户端按 code 471
	// 走 dnepcgceamm 枚举解码器，803 解码 InvalidCastException 直接断线
	// （Player.log 2026-11-06 02:59 实证）。实抓容器 code 同通用对象列表 61
	// （cat 13/17/26/27 的 by_elem 亦为 61）。
	"13001": "61",
}

// listCodeForObjs 根据首个对象元素的 type_id 推导数组容器 code：
// 显式覆盖 → by_elem 实抓映射 → by_type_id 兜底 → 61（通用对象列表）。
func listCodeForObjs(items []*proto.Value) string {
	a := loadArgcode()
	for _, it := range items {
		if it == nil || it.Kind != proto.KindObj {
			continue
		}
		k := fmt.Sprintf("%d", it.TypeID)
		if v, ok := listCodeOverrides[k]; ok && v != "" {
			return v
		}
		if v, ok := a.ByElem[k]; ok && v != "" && v != "524" {
			return v
		}
		if v, ok := a.ByTypeID[k]; ok && v != "" && v != "524" {
			return v
		}
		break
	}
	return "61"
}

// uuid5OIDHex 按 RFC 4122 生成 NAMESPACE_OID 下的 UUIDv5，用于确定性的模板 GUID。
func uuid5OIDHex(name string) string {
	// NAMESPACE_OID = 6ba7b812-9dad-11d1-80b4-00c04fd430c8（网络序原始字节）
	ns, _ := hex.DecodeString("6ba7b8129dad11d180b400c04fd430c8")
	h := sha1.New()
	h.Write(ns)
	h.Write([]byte(name))
	sum := h.Sum(nil)[:16]
	sum[6] = (sum[6] & 0x0f) | 0x50
	sum[8] = (sum[8] & 0x3f) | 0x80
	return hex.EncodeToString(sum)
}

// ---- 小工具 ----

func containsGUID(list []string, g string) bool {
	for _, x := range list {
		if x == g {
			return true
		}
	}
	return false
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// shortGUID 返回 GUID 前 8 位，用于日志截断。
