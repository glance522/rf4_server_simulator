package gameserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// 商店命令 22/x。

// hStoreList 处理 22/1，返回 args(140) 中的店内生效商品。
func hStoreList(ctx *Ctx) ([]byte, bool) {
	if !ctx.Sess.Srv.requireDB(ctx) {
		return nil, false
	}
	srv := ctx.Sess.Srv
	storeID := ""
	if ctx.Tree != nil {
		for _, it := range ctx.Tree.Items {
			if it != nil && it.Kind == proto.KindStr && it.Str != nil && *it.Str != "" {
				storeID = *it.Str
				break
			}
		}
	}
	cctx := ctx.Sess.pushCtx()
	out := proto.Args(proto.S("140"))
	items, err := srv.DB.ShopItems(cctx, storeID)
	if err != nil {
		ctx.Log.Warn("读取商店货架失败", "store", storeID, "err", err)
		items = nil
	}
	// 房产货架行必须保留：实测缺失时客户端点击房屋直接转圈（房屋 UI 的
	// 必要数据源），所有权改由档案 f20/f22 携带（见 profilebuild.go）。
	for _, p := range items {
		out.Items = append(out.Items, wiregen.MustMakeObj("ifdefblccde", map[int]*proto.Value{
			0: proto.StrVal(p.ItemID),
			1: proto.U64(uint64(p.Silver)),
			2: proto.U64(uint64(p.Gold)),
			3: proto.U32(uint32(p.Stock)),
		}))
	}
	body, err := proto.EncodeValue(out, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// storePrice 只接受该店生效货架上的价格，避免使用全局价格绕过店内库存。
func (s *Server) storePrice(ctx context.Context, storeID, itemID string, clientOffer uint64, goldCol bool) (uint64, error) {
	_ = clientOffer // 客户端报价只用于诊断，绝不作为服务端成交价。
	pick := func(p *store.ShopPrice) (uint64, error) {
		if p == nil {
			return 0, fmt.Errorf("无价目: %s@%s", storeID, itemID)
		}
		price := p.Silver
		cur := "silver"
		if goldCol {
			price = p.Gold
			cur = "gold"
		}
		if price <= 0 {
			return 0, fmt.Errorf("商品没有 %s 价格: %s@%s", cur, storeID, itemID)
		}
		return uint64(price), nil
	}
	p, err := s.DB.ShopItemGet(ctx, storeID, itemID)
	if err != nil {
		return 0, fmt.Errorf("商品不在该店生效货架: %s@%s: %w", storeID, itemID, err)
	}
	return pick(p)
}

// prepareStoreGoods 在事务前展开套盒并构造物品对象。
// 构造失败会在扣款前返回，避免产生已扣款但未发货的状态。
// 礼盒（ieopbeejfmc=true，官服飞德套装类）例外：只发放礼盒本体，玩家
// 4/8 使用时才拆开；饵料盒（ieopbeejfmc 为空）维持购买即展开，与官服
// 真实报文行为一致（饵料盒购买回执 804 的 f2 直接携带内容物）。
func (s *Server) prepareStoreGoods(ctx context.Context, uid int64, itemID string) (items []*store.Item, isBundle bool, err error) {
	def, err := s.DB.GetItemDefByItemID(ctx, itemID)
	if err != nil || def == nil {
		return nil, false, fmt.Errorf("无定义 %s", itemID)
	}
	if def.Category == bundleCategory {
		if bundleOpenable(def) {
			it, err := s.prepareNewItem(ctx, uid, itemID, "backpack", 1, "")
			return []*store.Item{it}, true, err
		}
		items, err = s.prepareBundleItems(ctx, uid, itemID, "backpack", 1)
		return items, true, err
	}
	it, err := s.prepareNewItem(ctx, uid, itemID, "backpack", 1, "")
	if err != nil {
		return nil, false, err
	}
	return []*store.Item{it}, false, nil
}

// storeGoodsReturnable 在事务前按客户端退货品类白名单判断整批产出。
// 购买即展开的盒子以实际内容物为准；只要有一件可退，整批都必须保持独立，
// 让退货时能够精确撤销本次购买而不碰购买前的库存。
func (s *Server) storeGoodsReturnable(ctx context.Context, items []*store.Item) (bool, error) {
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		if it == nil || it.ItemID == "" {
			return false, errors.New("商店产出物品不完整")
		}
		if seen[it.ItemID] {
			continue
		}
		seen[it.ItemID] = true
		def, err := s.DB.GetItemDefByItemID(ctx, it.ItemID)
		if err != nil || def == nil {
			return false, fmt.Errorf("读取商店产出定义 %s: %w", it.ItemID, err)
		}
		if storeReturnCategoryAllowed(def.Category) {
			return true, nil
		}
	}
	return false, nil
}

// hStoreBuy 处理 22/2 银币购买和 22/3 金币购买：
// 校验价格、构造物品，并在同一事务内扣款、记流水和入库。
func hStoreBuy(goldCol bool) Handler {
	return func(ctx *Ctx) ([]byte, bool) {
		srv := ctx.Sess.Srv
		if !srv.requireDB(ctx) {
			return nil, false
		}
		fail := func(message string) ([]byte, bool) {
			ctx.ReplyFailure("System.InvalidOperationException", message)
			return nil, true
		}
		uid := ctx.Sess.Userid
		strs := treeStrs(ctx.Tree)
		ints := treeInts(ctx.Tree)
		if len(strs) < 2 {
			return fail("购买参数无效")
		}
		storeID, itemID := strs[0], strs[1]
		var offer uint64
		if len(ints) > 0 {
			offer = ints[0]
		}
		cctx := ctx.Sess.pushCtx()
		price, err := srv.storePrice(cctx, storeID, itemID, offer, goldCol)
		if err != nil {
			ctx.Log.Warn("商店拒绝购买", "store", storeID, "item", itemID, "gold", goldCol, "err", err)
			return fail("购买未完成，请刷新商店后重试")
		}
		prepared, isBundle, err := srv.prepareStoreGoods(cctx, uid, itemID)
		if err != nil {
			ctx.Log.Warn("商店商品不可构造", "item", itemID, "err", err)
			return fail("购买未完成，请刷新商店后重试")
		}
		returnable, err := srv.storeGoodsReturnable(cctx, prepared)
		if err != nil {
			ctx.Log.Warn("商店商品退货属性不可判定", "item", itemID, "err", err)
			return fail("购买未完成，请刷新商店后重试")
		}

		tx, err := beginEconomyTx(cctx, srv.DB)
		if err != nil {
			ctx.Log.Warn("商店开启事务失败", "err", err)
			return fail("购买未完成，请刷新商店后重试")
		}
		defer func() { _ = tx.Rollback() }()

		currency := "silver"
		silverDelta, goldDelta := -int64(price), int64(0)
		if goldCol {
			currency = "gold"
			silverDelta, goldDelta = 0, -int64(price)
		}
		note := fmt.Sprintf("store=%s currency=%s", storeID, currency)
		if err := applyWalletTx(cctx, tx, uid, silverDelta, goldDelta,
			"buy", itemID, "", note); err != nil {
			ctx.Log.Warn("商店扣款失败", "item", itemID, "err", err)
			if errors.Is(err, errInsufficientFunds) {
				return fail("余额不足，未购买商品")
			}
			return fail("购买未完成，请刷新商店后重试")
		}

		granted := make([]*store.Item, 0, len(prepared))
		for _, it := range prepared {
			// 可退批次独立存放；不可退计件产出按精确 item_id 并入同容器已有堆。
			row, _, gerr := grantPreparedItemTx(cctx, tx, it, !returnable)
			if gerr != nil {
				ctx.Log.Warn("商店入库失败", "item", it.ItemID, "err", gerr)
				return fail("购买未完成，请刷新商店后重试")
			}
			granted = append(granted, row)
		}
		if returnable {
			if err := srv.saveStorePurchaseTx(cctx, tx, uid, storeID, itemID, price, goldCol, granted); err != nil {
				ctx.Log.Warn("商店购买凭据保存失败", "err", err)
				return fail("购买未完成，请刷新商店后重试")
			}
		}
		// 纸质地图（category 40）只是可阅读的地图道具，购买它不等于买断地点：
		// 旧实现会在这里写 place_access(kind='buyout')，导致"买张地图就变成无限访问"。
		// 地点访问只走 3/37 金币买断这一条路，商店购买不再授予任何访问权。
		if err := tx.Commit(); err != nil {
			ctx.Log.Warn("商店事务提交失败", "item", itemID, "err", err)
			return fail("购买未完成，请刷新商店后重试")
		}

		trees := make([]*proto.Value, 0, len(granted))
		for _, row := range granted {
			tree, terr := srv.ItemToTree(cctx, row)
			if terr != nil {
				ctx.Log.Warn("商店物品树构造失败", "item", itemID, "err", terr)
				continue
			}
			trees = append(trees, tree)
		}
		f1 := proto.Null()
		f2 := proto.Null()
		if isBundle {
			if len(trees) > 0 {
				f2 = proto.Args(proto.S("61"), trees...)
			}
		} else if len(trees) > 0 {
			f1 = proto.Args(proto.S("61"), trees...)
		}
		obj804, err := wiregen.MakeObj("jfafgmcfjkc", map[int]*proto.Value{
			0: proto.StrVal(itemID),
			1: f1,
			2: f2,
			3: proto.Null(),
		})
		if err != nil {
			return nil, false
		}
		body, err := proto.EncodeValue(obj804, ctx.Sess.Table())
		if err != nil {
			return nil, false
		}
		ctx.Sess.Push2_1()
		return body, true
	}
}
func boolToI(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// hStoreReturn 处理 22/4，原子扣物退款并返回 800 移除快照。
func hStoreReturn(ctx *Ctx) ([]byte, bool) {
	return ctx.Sess.Srv.returnStorePurchase(ctx)
}

// hStoreATMQuery 处理 22/6。实抓证据（2026-11-04 trace）：客户端在房产
// 购买流程中每次 3/33 之后都会发送 [store_id, filetime]，据此判断支付/
// 结算是否完成；回 false 会让客户端停在购买界面循环。房产商店回 true
// 放行结算；其余商店维持既有 false 行为，不影响 ATM 钱包商店。
func hStoreATMQuery(ctx *Ctx) ([]byte, bool) {
	if strs := treeStrs(ctx.Tree); len(strs) > 0 && strings.HasPrefix(strs[0], "housestore_") {
		return []byte{0x0E}, true
	}
	return []byte{0x0F}, true
}

// treeStrs、treeInts 分别读取顶层 args 中的字符串和整数序列。
func treeStrs(v *proto.Value) []string {
	var out []string
	if v == nil {
		return out
	}
	for _, it := range v.Items {
		if it != nil && it.Kind == proto.KindStr && it.Str != nil {
			out = append(out, *it.Str)
		}
	}
	return out
}

func treeInts(v *proto.Value) []uint64 {
	var out []uint64
	if v == nil {
		return out
	}
	for _, it := range v.Items {
		if it != nil && (it.Kind == proto.KindU32 || it.Kind == proto.KindU64) {
			out = append(out, it.Num)
		}
	}
	return out
}

func registerShopHandlers(d *Dispatch) {
	d.Register(22, 1, hStoreList)
	d.Register(22, 2, hStoreBuy(false))
	d.Register(22, 3, hStoreBuy(true))
	for _, sub := range []uint8{2, 3} {
		d.RegisterResponseContract(22, sub, ResponseContractRule{Kind: proto.KindObj, TypeIDs: []uint32{804}, Classes: []string{"jfafgmcfjkc"}})
	}
	d.Register(22, 4, hStoreReturn)
	d.RegisterResponseContract(22, 4, ResponseContractRule{Kind: proto.KindObj, TypeIDs: []uint32{800}, Classes: []string{"dlbahppbdad"}})
	d.Register(22, 6, hStoreATMQuery)
}

// attachReceipt 将购买收据 166 写入物品树的对应字段；字段缺失时补 null。
func attachReceipt(tree *proto.Value, receipt *proto.Value) {
	if tree == nil || tree.Kind != proto.KindObj {
		return
	}
	idx := fieldIdxByCS(tree.Class, csOrigin)
	if idx < 0 || idx >= len(tree.Fields) {
		return
	}
	tree.Fields[idx].Op = "VAL"
	tree.Fields[idx].Val = receipt
}
