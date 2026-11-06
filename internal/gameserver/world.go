package gameserver

import (
	"errors"
	"strings"

	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
	"time"
)

// world 命令族（25/27/29）和背包变动命令（4/6-4/8）。
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
	// 27/x RemoteSettings：返回容器快照，27/4 返回空集合。
	d.Register(27, 1, hWorldSnapshot)
	d.Register(27, 2, hWorldSnapshot)
	d.Register(27, 3, hWorldSnapshot)
	d.Register(27, 4, hWorldRemoteSettingsEmpty)
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
	d.Register(3, 33, hBuyHouseTx)
}

func hWorldSnapshot(ctx *Ctx) ([]byte, bool) {
	if !ctx.Sess.Srv.requireDB(ctx) {
		return nil, false
	}
	return ctx.Sess.Srv.rigSnapshot800(ctx), true
}

func hWorldRemoteSettingsEmpty(ctx *Ctx) ([]byte, bool) {
	obj := wiregen.MustMakeObj("gglppphenoi", map[int]*proto.Value{
		0: proto.Null(),
		1: proto.Null(),
		2: proto.Null(),
		3: proto.Null(),
	})
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

func hWorldNull(ctx *Ctx) ([]byte, bool) {
	return []byte{0x00}, true
}

// hItemMove 4/6：guids + [源容器, 目标容器] → 移动 → 回快照。
func hItemMove(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	guids := deepGUIDs(ctx.Tree)
	ints := treeIntsDeep(ctx.Tree)
	if len(guids) > 0 {
		dst := "backpack"
		if len(ints) > 1 {
			dst = containerName(ints[1])
		}
		for _, g := range guids {
			if row, err := srv.DB.GetItemByGUID(ctx.Sess.pushCtx(), g); err == nil && row.UID == ctx.Sess.Userid {
				row.Container = dst
				_, _ = srv.DB.SQL.ExecContext(ctx.Sess.pushCtx(),
					`UPDATE item SET container=? WHERE guid=?`, dst, g)
			}
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
	removed, fish, err := srv.DB.RemoveOwnedItems(ctx.Sess.pushCtx(), ctx.Sess.Userid, guids)
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
	p, err := srv.BuildProfile307(cctx, uid)
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

	p, err := srv.BuildProfile307(cctx, uid)
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
