package gameserver

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// world 命令族（25/29）和背包变动命令（4/6-4/8）。
// world 族通常返回 800 容器快照。

// rigSnapshot800 构造背包和装备容器的 800 快照。
func (s *Server) rigSnapshot800(ctx *Ctx) []byte {
	cctx := ctx.Sess.pushCtx()
	bp := proto.Args(proto.S("61"))
	rg := proto.Args(proto.S("61"))
	guids, _ := s.DB.ItemGUIDsByContainers(cctx, ctx.Sess.Userid, []string{"backpack"})
	for _, g := range guids {
		if row, err := s.DB.GetItemByGUID(cctx, g); err == nil {
			if tree, terr := s.ItemToTree(cctx, row); terr == nil {
				bp.Items = append(bp.Items, tree)
			}
		}
	}
	rguids, _ := s.DB.ItemGUIDsByContainers(cctx, ctx.Sess.Userid, []string{"rig"})
	for _, g := range rguids {
		if row, err := s.DB.GetItemByGUID(cctx, g); err == nil {
			if tree, terr := s.ItemToTree(cctx, row); terr == nil {
				rg.Items = append(rg.Items, tree)
			}
		}
	}
	body, err := proto.EncodeValue(build800(bp, rg, proto.Null()), ctx.Sess.Table())
	if err != nil {
		return emptyArgsBody()
	}
	return body
}

func registerWorldHandlers(d *Dispatch) {
	// 25/x 船只与燃料：返回容器快照。
	d.Register(25, 1, hWorldSnapshot)
	d.Register(25, 2, hWorldSnapshot)
	d.Register(25, 4, hWorldSnapshot)
	d.Register(25, 5, hWorldSnapshot)
	d.Register(25, 6, hU64Zero) // 船只/加油配置 → long 0（回 800 快照是自造形状错误）
	// 29/x 赛事结果：返回 null。
	for _, sub := range []uint8{1, 2, 3, 4, 5} {
		d.Register(29, sub, hWorldNull)
	}
	// 背包变动
	d.Register(4, 6, hItemMove)
	d.Register(4, 7, hItemRemove)
	d.Register(4, 8, hItemUse)
	d.Register(3, 37, hPlaceBuyoutTx)
	d.Register(3, 38, hPlaceTripTx)
	d.Register(3, 33, hBuyHouseTx)
}

func hWorldSnapshot(ctx *Ctx) ([]byte, bool) {
	if !ctx.Sess.Srv.requireDB(ctx) {
		return nil, false
	}
	return ctx.Sess.Srv.rigSnapshot800(ctx), true
}

func hWorldNull(ctx *Ctx) ([]byte, bool) {
	return []byte{0x00}, true
}

// hItemMove 4/6：guids + [源容器, 目标容器] → 移动 → 回快照。
//
// 移动完成后按 4/21(删旧)+4/22(推新树) 增量同步客户端：物品树的容器
// 枚举由 canonicalizeItemPhysicalFields 按新容器投影（房屋=10），客户端
// 物品管理器据此把物品挂进目标容器字典。仅回 800 快照时客户端房屋
// 仓库不刷新，存放交互表现为"没反应"。
func hItemMove(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	ints := treeIntsDeep(ctx.Tree)
	dstKind := uint64(containerBackpack)
	if len(ints) > 1 {
		dstKind = ints[1]
	}
	dst := containerName(dstKind)
	cctx := ctx.Sess.pushCtx()
	houseCap := int64(houseContainerCapacity)
	if dst == "house" {
		houseCap, _ = srv.importedContainerCapacity(cctx, ctx.Sess.Userid, dstKind, houseCap, 0)
	}
	tx, err := beginEconomyTx(cctx, srv.DB)
	if err != nil {
		ctx.Log.Warn("4/6 开启移动事务失败", "err", err)
		ctx.ReplyFailure("System.IO.IOException", "物品未能移动，请重试")
		return nil, true
	}
	defer tx.Rollback()

	seen := map[string]bool{}
	pending := make([]*store.Item, 0)
	var houseUsed, houseAdded int64
	if dst == "house" {
		if err := tx.QueryRowContext(cctx,
			`SELECT COUNT(*) FROM item WHERE uid=? AND container='house'`, ctx.Sess.Userid).Scan(&houseUsed); err != nil {
			ctx.Log.Warn("4/6 读取房屋容量失败", "err", err)
			ctx.ReplyFailure("System.IO.IOException", "房屋仓库容量读取失败，请重试")
			return nil, true
		}
	}
	for _, g := range deepGUIDs(ctx.Tree) {
		if g == "" || seen[g] {
			continue
		}
		seen[g] = true
		row, err := srv.DB.GetItemByGUIDTx(cctx, tx, g)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			ctx.Log.Warn("4/6 读取待移动物品失败", "guid", shortGUID(g), "err", err)
			ctx.ReplyFailure("System.IO.IOException", "物品未能移动，请重试")
			return nil, true
		}
		if row == nil || row.UID != ctx.Sess.Userid {
			continue
		}
		if dst == "house" && row.Container != "house" {
			houseAdded++
		}
		row.Container = dst
		pending = append(pending, row)
	}
	if dst == "house" && houseUsed+houseAdded > houseCap {
		ctx.Log.Info("4/6 房屋仓库已满", "used", houseUsed, "add", houseAdded, "cap", houseCap)
		ctx.ReplyFailure("System.IO.IOException", "房屋仓库已满，无法继续存放")
		return nil, true
	}
	for _, row := range pending {
		if _, err := tx.ExecContext(cctx,
			`UPDATE item SET container=? WHERE guid=? AND uid=?`, dst, row.GUID, ctx.Sess.Userid); err != nil {
			ctx.Log.Warn("4/6 更新物品容器失败", "guid", shortGUID(row.GUID), "err", err)
			ctx.ReplyFailure("System.IO.IOException", "物品未能移动，请重试")
			return nil, true
		}
	}
	if err := tx.Commit(); err != nil {
		ctx.Log.Warn("4/6 提交移动事务失败", "err", err)
		ctx.ReplyFailure("System.IO.IOException", "物品未能移动，请重试")
		return nil, true
	}

	for _, row := range pending {
		if tree, terr := srv.ItemToTree(cctx, row); terr == nil {
			ctx.Sess.Push(4, 21, proto.Args524(proto.GuidArr([]string{row.GUID})))
			ctx.Sess.Push(4, 22, proto.Args(proto.S("61"), tree))
		}
	}
	return srv.rigSnapshot800(ctx), true
}

// hItemRemove 4/7：删除物品 → 回快照。
// 载荷为 args("524")[容器枚举, GuidArr]。容器=5（鱼护）时目标是 keepnet
// 表的回显 GUID（扔回水里），不在 item 表——按原路径静默无操作会让鱼护
// 里的鱼扔不出去，需要分流到 KeepnetDelete。
func hItemRemove(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	guids := deepGUIDs(ctx.Tree)
	removed, fish, err := srv.removeItemsAndRepairRigs(ctx.Sess.pushCtx(), ctx.Sess.Userid, guids)
	if err != nil {
		ctx.Log.Warn("物品移除失败", "err", err)
		ctx.ReplyFailure("System.IO.IOException", "物品未能移除，请重试")
		return nil, true
	}
	if len(fish) > 0 {
		st := srv.stateFor(ctx.Sess.Userid)
		st.mu.Lock()
		kept := st.Keepnet[:0]
		for _, entry := range st.Keepnet {
			if !containsGUID(fish, entry.FishGUID) {
				kept = append(kept, entry)
			}
		}
		st.Keepnet = kept
		st.mu.Unlock()
	}
	if len(removed) > 0 {
		ctx.Sess.Push4_21(removed)
	}
	if len(fish) > 0 {
		srv.retryCompletedCatchRecovery(ctx.Sess)
	}
	return srv.rigSnapshot800(ctx), true
}

// hItemUse 处理 4/8，返回被寻址物品的单件 800 快照。
// 礼盒（ieopbeejfmc=true 的 cat 55）由 useGiftBox 拦截拆开。
func hItemUse(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	if body, handled := srv.useAlcoholDrink(ctx); handled {
		return body, true
	}
	if body, handled := srv.useSkillBook(ctx); handled {
		return body, true
	}
	if body, handled := srv.useGiftBox(ctx); handled {
		return body, true
	}
	if body, handled := srv.useFoodConsumable(ctx); handled {
		return body, true
	}
	guids := deepGUIDs(ctx.Tree)
	out := proto.Args(proto.S("61"))
	if len(guids) > 0 {
		if row, err := srv.DB.GetItemByGUID(ctx.Sess.pushCtx(), guids[0]); err == nil {
			if tree, terr := srv.ItemToTree(ctx.Sess.pushCtx(), row); terr == nil {
				out.Items = append(out.Items, tree)
			}
		}
	}
	body, err := proto.EncodeValue(build800(out, proto.Null(), proto.Null()), ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// containerName 将容器枚举转换为存储 key。
// 已知的 0 到 11 容器按各自类型处理；未知枚举回退到背包。
func containerName(v uint64) string {
	return containerEnumName(v)
}

// treeIntsDeep 深扫整数。
func treeIntsDeep(v *proto.Value) []uint64 {
	var out []uint64
	var walk func(x *proto.Value)
	walk = func(x *proto.Value) {
		if x == nil {
			return
		}
		switch x.Kind {
		case proto.KindU32, proto.KindU64:
			out = append(out, x.Num)
		case proto.KindArgs, proto.KindValList, proto.KindArray, proto.KindGuidArr, proto.KindU32Arr:
			for _, it := range x.Items {
				walk(it)
			}
		case proto.KindObj:
			for i := range x.Fields {
				if x.Fields[i].Num != nil {
					out = append(out, u64Of(x.Fields[i].Num))
				}
			}
		}
	}
	walk(v)
	return out
}

// hPlaceBuyoutTx 处理 3/37 地点买断：
// 已拥有检查、余额条件扣减、授权和流水在同一事务内完成。
func hPlaceBuyoutTx(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	uid := ctx.Sess.Userid
	// bundleGrant 标记本次买断是否落在捆绑包价位，提交后再发附赠物品。
	bundleGrant := false
	cctx := ctx.Sess.pushCtx()
	place, packet, price := parsePlaceBuyoutArgs(ctx.Tree)
	tier := placeBuyoutPrice(place, packet)

	tx, err := beginEconomyTx(cctx, srv.DB)
	if err != nil {
		ctx.Log.Warn("3/37 开启事务失败", "err", err)
		return nil, false
	}
	defer func() { _ = tx.Rollback() }()
	var owned int
	if err := tx.QueryRowContext(cctx, `SELECT COUNT(*) FROM place_access WHERE uid=? AND place=? AND kind='buyout'`,
		uid, place).Scan(&owned); err != nil {
		ctx.Log.Warn("3/37 查询已拥有地点失败", "place", place, "err", err)
		return nil, false
	}
	validPrice := tier > 0 && price == tier
	if owned == 0 && validPrice {
		walletErr := applyWalletTx(cctx, tx, uid, 0, -tier, "buy", place, "", "place_buyout")
		if walletErr != nil {
			if !errors.Is(walletErr, errInsufficientFunds) {
				ctx.Log.Warn("3/37 金币扣款失败", "place", place, "tier", packet, "err", walletErr)
				return nil, false
			}
			ctx.Log.Info("3/37 金币余额不足，不扣款", "place", place, "tier", packet)
		} else {
			if _, err := tx.ExecContext(cctx, `
				INSERT INTO place_access(uid, place, kind, ts) VALUES(?,?, 'buyout', ?)
				ON CONFLICT(uid, place, kind) DO NOTHING`, uid, place, float64(time.Now().Unix())); err != nil {
				ctx.Log.Warn("3/37 授予地点失败", "place", place, "err", err)
				return nil, false
			}
			ctx.Log.Info("3/37 金币买断地图", "place", place, "tier", packet,
				"gold", -tier, "gold_yuan", float64(tier)/100.0)
		}
		bundleGrant = true
	} else if owned == 0 {
		tiers, _ := placeBuyoutTiers[place]
		ctx.Log.Info("3/37 价格与静态表不符或未授权，不扣款", "place", place,
			"packet", packet, "price", price, "tiers", tiers)
	}
	if err := tx.Commit(); err != nil {
		ctx.Log.Warn("3/37 事务提交失败", "err", err)
		return nil, false
	}
	if bundleGrant {
		srv.grantPremiumBundle(cctx, ctx, uid, place, tier)
	}
	p, err := srv.BuildProfile307W(cctx, uid, worldTotalGameMin(time.Now()))
	if err != nil {
		ctx.Log.Warn("3/37 档案构造失败", "err", err)
		return nil, false
	}
	body, err := proto.EncodeValue(p, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// hBuyHouseTx 处理 3/33 房屋购买。
//
// 实抓证据（2026-11-04 trace）：请求 args 为 [房屋 item_id, 价格, bool]，
// 如 ["level_012_north_sea_house", 4700, true]。价格由客户端取自 22/5 面板
// 数据（gameserver 的 shop_boats.json），服务端按同一数据校验，避免面板价
// 与结算价漂移。 bool 参数含义未确认，接受两种取值。
//
// 成功路径：同一事务内已拥有检查、银币扣款、房屋物品发放与流水。房屋物品
// 发放到背包（物品定义带 dhaenhabecc 地点键，客户端据其把该地点的房屋
// 标记为已拥有，从而显示"进入"而不是继续购买）；响应沿用 3/37 先例返回
// 档案 307，客户端据其刷新余额。
func hBuyHouseTx(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	uid := ctx.Sess.Userid
	strs := treeStrs(ctx.Tree)
	ints := treeInts(ctx.Tree)
	if len(strs) < 1 || len(ints) < 1 {
		ctx.Log.Warn("3/33 参数无效", "strs", strs, "ints", ints)
		ctx.ReplyFailure("System.InvalidOperationException", "房屋购买参数无效")
		return nil, true
	}
	itemID, price := strs[0], ints[0]
	if !strings.HasSuffix(itemID, "_house") {
		ctx.Log.Warn("3/33 非房屋商品，拒绝", "item", itemID)
		ctx.ReplyFailure("System.InvalidOperationException", "商品不是房屋")
		return nil, true
	}
	// 价格与 22/5 面板一致才结算；面板是客户端唯一的价格来源。
	var tier uint64
	for _, row := range loadShopBoats() {
		if row.Name == itemID {
			tier = row.Price
			break
		}
	}
	if tier == 0 || price != tier {
		ctx.Log.Info("3/33 价格与面板不符或未上架，不扣款", "item", itemID,
			"price", price, "tier", tier)
		ctx.ReplyFailure("System.InvalidOperationException", "房屋价格已变化，请重新打开购买界面")
		return nil, true
	}

	cctx := ctx.Sess.pushCtx()
	// 房屋不产生背包物品：所有权由 place_access(kind='house') 承载并经
	// 档案 307 的 f20 下发（见 profilebuild.go）。
	tx, err := beginEconomyTx(cctx, srv.DB)
	if err != nil {
		ctx.Log.Warn("3/33 开启事务失败", "err", err)
		return nil, false
	}
	defer func() { _ = tx.Rollback() }()
	var owned int
	if err := tx.QueryRowContext(cctx,
		`SELECT COUNT(*) FROM place_access WHERE uid=? AND place=? AND kind='house'`,
		uid, itemID).Scan(&owned); err != nil {
		ctx.Log.Warn("3/33 查询已拥有房屋失败", "item", itemID, "err", err)
		return nil, false
	}
	if owned > 0 {
		ctx.Log.Info("3/33 已拥有该房屋，不重复扣款", "item", itemID)
		ctx.ReplyFailure("System.InvalidOperationException", "已拥有该房屋")
		return nil, true
	}
	if walletErr := applyWalletTx(cctx, tx, uid, -int64(price), 0,
		"buy", itemID, "", "house_purchase"); walletErr != nil {
		if !errors.Is(walletErr, errInsufficientFunds) {
			ctx.Log.Warn("3/33 银币扣款失败", "item", itemID, "err", walletErr)
			return nil, false
		}
		ctx.Log.Info("3/33 银币余额不足，不扣款", "item", itemID, "price", price)
		ctx.ReplyFailure("System.InvalidOperationException", "余额不足，未购买房屋")
		return nil, true
	}
	// 所有权进档案 f20 拥有列表（与 3/37 地点买断写 place_access 同模式），
	// 客户端重启后也能从 3/1 档案里读到已购房屋。
	if _, err := tx.ExecContext(cctx, `
		INSERT INTO place_access(uid, place, kind, ts) VALUES(?,?, 'house', ?)
		ON CONFLICT (uid, place, kind) DO NOTHING`,
		uid, itemID, float64(time.Now().Unix())); err != nil {
		ctx.Log.Warn("3/33 记录房屋所有权失败", "item", itemID, "err", err)
		return nil, false
	}
	if err := tx.Commit(); err != nil {
		ctx.Log.Warn("3/33 事务提交失败", "item", itemID, "err", err)
		return nil, false
	}
	ctx.Log.Info("3/33 购买房屋", "item", itemID, "silver", -int64(price))

	p, err := srv.BuildProfile307W(cctx, uid, worldTotalGameMin(time.Now()))
	if err != nil {
		ctx.Log.Warn("3/33 档案构造失败", "err", err)
		return nil, false
	}
	body, err := proto.EncodeValue(p, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	ctx.Sess.Push2_1()
	return body, true
}

// hU64Zero 返回 u64 零值，供需要 long 语义的命令使用。
func hU64Zero(*Ctx) ([]byte, bool) { return append([]byte{0x11}, proto.PackU64(0)...), true }

// ---- 3/38 钓鱼游 ----
//
// 钓鱼游是一个完整状态（用户提供的官方说明）：
//   - 购买后无视级别进入对应地点；
//   - 每次购买 5 个游戏日 = 5 现实小时，只在在线时消耗（离线暂停，在线
//     不论身处哪张地图都计时），计时消费见 session.go 的 consumeTripTimes；
//   - 每个地点最多购买 3 次；
//   - 进入地图发放配套装备、离开删除（装备清单待实抓，暂未实现）。

// tripDurationSecDefault 每次购买的默认时长：5 游戏日 = 5 现实小时。
const tripDurationSecDefault = int64(5 * 3600)

// tripDurationKVKey kv 覆盖键：钓鱼游时长（秒）。改 kv 即生效，不必重启
// （currentTripDurationSec 每次现读）。客户端徽章时长完全由 f23 下发
// （反编译证实），配短时长即可快速验证倒数与到期回收。
const tripDurationKVKey = "trip_duration_sec"

// currentTripDurationSec 读 kv 覆盖的钓鱼游时长。未配置/非法值回默认；
// 下限 30 秒，防止误配 0 导致购票即到期。
func (s *Server) currentTripDurationSec(ctx context.Context) int64 {
	if s.DB != nil {
		if raw, ok, err := s.DB.KVGet(ctx, tripDurationKVKey); err == nil && ok {
			if v, perr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); perr == nil && v >= 30 {
				return v
			}
		}
	}
	return tripDurationSecDefault
}

// tripGameMin 现实秒 → 游戏分钟（24 倍速）。
func tripGameMin(sec int64) int64 { return sec * 24 / 60 }

// tripMaxPurchases 每个地点的限购次数（客户端面板"最多可购买 3 次"）。
const tripMaxPurchases = 3

// placeTripPrice 一个地点钓鱼游的双币种价格。
type placeTripPrice struct {
	Silver int64 // 单位：分（29800 = 298.00 银）
	Gold   int64 // 单位：金币百分位（45 = 0.45 金）
}

// placeTripPrices 钓鱼游票价表。票价由客户端本地静态数据决定（地图 dob
// 不含票价），Silver/Gold 为 0 表示该币种价格未实抓，按客户端报价结算并记
// 日志，避免缺表导致购票永远失败。
var placeTripPrices = map[string]placeTripPrice{
	"level_018_moskitnoe": {Silver: 29800, Gold: 45}, // 铜湖 298 银 / 0.45 金（2026-11-05 实抓）
	"level_012_north_sea": {Silver: 139500},          // 挪威海 1395 银（2026-11-05 实抓）；金币价待实抓
}

// parsePlaceTripArgs 解析 3/38 请求 [地图 key, bool, 价格]。
// bool 含义未确认，按购买界面"银币/金币"两个按钮理解为金币支付标记。
func parsePlaceTripArgs(tree *proto.Value) (place string, gold bool, price int64) {
	if tree == nil || tree.Kind != proto.KindArgs {
		return "", false, 0
	}
	for _, it := range tree.Items {
		if it == nil {
			continue
		}
		switch it.Kind {
		case proto.KindStr:
			if it.Str != nil && *it.Str != "" && place == "" {
				place = *it.Str
			}
		case proto.KindBool:
			gold = it.Bool
		case proto.KindU32, proto.KindU64:
			if price == 0 {
				price = int64(it.Num)
			}
		}
	}
	return place, gold, price
}

// hPlaceTripTx 处理 3/38 购买钓鱼游。
//
// 实抓证据（2026-11-05 trace）：请求 args 为 ["level_018_moskitnoe", false,
// 29800]，与购买面板"298 银 / 0.45 金"的银币价（分）一致。
//
// 响应沿用 3/37/3/33 先例返回档案 307（fallback_kinds.json 给 3/38 标注的
// dto 类 ghfbofgdflm 即档案 307），客户端据其刷新余额并继续切图流程；
// 此前未实现时兜底链回全零 307（UID=0），客户端解析后停在购买界面。
// 访问记录 kind='trip' 只存 place_access 供服务端判资格与到期回收，
// 不进档案 f22——客户端把 f22 一律渲染成"永久买断"并隐藏 3/37 买断按钮
// （2026-11-05 实测）。
func hPlaceTripTx(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	uid := ctx.Sess.Userid
	place, gold, price := parsePlaceTripArgs(ctx.Tree)
	durSec := srv.currentTripDurationSec(ctx.Sess.pushCtx())
	if !validLevelKey(place) || price <= 0 {
		ctx.Log.Warn("3/38 参数无效", "place", place, "gold", gold, "price", price)
		ctx.ReplyFailure("System.InvalidOperationException", "钓鱼游购买参数无效")
		return nil, true
	}
	// 票价必须整段走固定票价表，不能回落客户端报价：购票面板价格由客户端
	// 本地静态数据渲染，是固定值——服务端按报价结算等于把定价权交给请求方，
	// gold=true price=1 曾被按 1 金币成交（审查 P1 实测）。未收录地点与
	// 未实抓币种（金币价缺失）一律拒绝购买。
	tier, ok := placeTripPrices[place]
	if !ok {
		ctx.Log.Info("3/38 未收录的钓鱼游地点，拒绝购买", "place", place,
			"gold", gold, "price", price)
		ctx.ReplyFailure("System.InvalidOperationException", "该地点暂不支持购买钓鱼游")
		return nil, true
	}
	want := tier.Silver
	if gold {
		want = tier.Gold
	}
	if want <= 0 {
		ctx.Log.Info("3/38 该币种票价未收录，拒绝购买", "place", place,
			"gold", gold, "price", price)
		ctx.ReplyFailure("System.InvalidOperationException", "该支付方式暂未开放")
		return nil, true
	}
	if price != want {
		ctx.Log.Info("3/38 票价与实抓价格不符，不扣款", "place", place,
			"gold", gold, "price", price, "want", want)
		ctx.ReplyFailure("System.InvalidOperationException", "钓鱼游价格已变化，请重新打开购买界面")
		return nil, true
	}

	cctx := ctx.Sess.pushCtx()
	tx, err := beginEconomyTx(cctx, srv.DB)
	if err != nil {
		ctx.Log.Warn("3/38 开启事务失败", "err", err)
		return nil, false
	}
	defer func() { _ = tx.Rollback() }()
	var purchases int64
	var remaining float64
	err = tx.QueryRowContext(cctx,
		`SELECT purchases, remaining_sec FROM trip_state WHERE uid=? AND place=?`,
		uid, place).Scan(&purchases, &remaining)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		ctx.Log.Warn("3/38 读取钓鱼游状态失败", "place", place, "err", err)
		return nil, false
	}
	if purchases >= tripMaxPurchases {
		ctx.Log.Info("3/38 购买次数已达上限", "place", place, "purchases", purchases)
		ctx.ReplyFailure("System.InvalidOperationException", "该钓鱼游购买次数已达上限")
		return nil, true
	}
	if gold {
		if walletErr := applyWalletTx(cctx, tx, uid, 0, -price,
			"buy", place, "", "place_trip"); walletErr != nil {
			return tripWalletFail(ctx, place, price, walletErr)
		}
	} else {
		if walletErr := applyWalletTx(cctx, tx, uid, -price, 0,
			"buy", place, "", "place_trip"); walletErr != nil {
			return tripWalletFail(ctx, place, price, walletErr)
		}
	}
	// 时长累加并**购买即启表**（last_consume_ts=now）：il2csharp 全量反编译
	// 证实客户端只在 3/1 登录与 3/38 购票两个时机捕获档案（1/14 响应与
	// 2/1 推送均不更新），进图没有捕获通道；FishingSceneVoucherWathDog
	// 对 !knpoijanngk 直接判"钓鱼旅结束"禁抛竿，故官方机制即购票起算。
	// 到期的行重购时锚点从新时刻起。
	nowF := float64(time.Now().Unix())
	if _, err := tx.ExecContext(cctx, `
		INSERT INTO trip_state(uid, place, remaining_sec, last_consume_ts, purchases, updated_ts)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(uid, place) DO UPDATE SET
			remaining_sec=remaining_sec+excluded.remaining_sec,
			last_consume_ts=excluded.last_consume_ts,
			purchases=purchases+1,
			updated_ts=excluded.updated_ts`,
		uid, place, float64(durSec), nowF, 1, nowF); err != nil {
		ctx.Log.Warn("3/38 写入钓鱼游状态失败", "place", place, "err", err)
		return nil, false
	}
	// 访问记录进 place_access(kind='trip')：服务端判资格（tripLevelLocked）、
	// 到期回收（consumeTripTimes）都查它；不进档案 f22（客户端把 f22 一律
	// 渲染成永久买断）。到期被回收后再次购买要重新插入。
	if _, err := tx.ExecContext(cctx, `
		INSERT INTO place_access(uid, place, kind, ts) VALUES(?,?, 'trip', ?)
		ON CONFLICT (uid, place, kind) DO NOTHING`,
		uid, place, nowF); err != nil {
		ctx.Log.Warn("3/38 记录钓鱼游访问失败", "place", place, "err", err)
		return nil, false
	}
	if err := tx.Commit(); err != nil {
		ctx.Log.Warn("3/38 事务提交失败", "place", place, "err", err)
		return nil, false
	}
	ctx.Log.Info("3/38 购买钓鱼游", "place", place, "gold", gold, "price", price,
		"purchases", purchases+1, "remaining_sec", remaining+float64(durSec))

	// 装备包不在购票时发放：官方生命周期（用户 2026-11-05 口头修正）为
	// "进图发放/离图回收"（含下线），见 trip_equip.go syncTripEquipment。

	p, err := srv.BuildProfile307W(cctx, uid, worldTotalGameMin(time.Now()))
	if err != nil {
		ctx.Log.Warn("3/38 档案构造失败", "err", err)
		return nil, false
	}
	body, err := proto.EncodeValue(p, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	ctx.Sess.Push2_1()
	return body, true
}

// tripWalletFail 钓鱼游扣款的失败分流：余额不足回业务失败，其余交兜底链。
func tripWalletFail(ctx *Ctx, place string, price int64, walletErr error) ([]byte, bool) {
	if !errors.Is(walletErr, errInsufficientFunds) {
		ctx.Log.Warn("3/38 扣款失败", "place", place, "price", price, "err", walletErr)
		return nil, false
	}
	ctx.Log.Info("3/38 余额不足，不扣款", "place", place, "price", price)
	ctx.ReplyFailure("System.InvalidOperationException", "余额不足，未购买钓鱼游")
	return nil, true
}
