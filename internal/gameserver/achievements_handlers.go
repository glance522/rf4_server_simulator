package gameserver

import (
	"context"
	"fmt"
	"strings"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

func registerAchievementHandlers(d *Dispatch) {
	d.Register(3, 3, hAchievements305)
	d.Register(3, 16, hPlayerRewards2015)
	d.Register(3, 17, hFishTrophies323)
}

// playerRewardItems 返回玩家占有的全部奖赏物品（house/backpack/keepnet 中
// wire 类 enikeammfen、type_id 2015 的行）。3/16 面板与 4/1/4/4/4/5 的
// 奖赏容器（客户端分组 14）共用这一口径，避免两处筛选条件漂移。
func (s *Server) playerRewardItems(ctx context.Context, uid int64) ([]*store.Item, error) {
	if s == nil || s.DB == nil {
		return nil, nil
	}
	rows, err := s.DB.ListItemsByContainers(ctx, uid, []string{"house", "backpack", "keepnet"})
	if err != nil {
		return nil, err
	}
	out := make([]*store.Item, 0, len(rows))
	for _, it := range rows {
		// wire 类与 type_id 必须同时匹配，任一不符的异常行都不得混入奖赏列表。
		if it.WireClass != "enikeammfen" || it.TypeID != 2015 {
			continue
		}
		out = append(out, it)
	}
	return out, nil
}

// hPlayerRewards2015 处理 3/16 奖赏列表。客户端 RPC 签名
// public iidloljandl[] cfbomeockod() 返回 2015 数组，容器名 "185"
// （argcode_table by_type_id[2015]=185 = by_cmd[3/16]）。
//
// 数据源：玩家 house/backpack/keepnet 里 wire 类 enikeammfen（物品类别 72 奖赏）
// 的物品——与官方把嘉奖当物品存储的方式一致。纯查询：不写库、不推送。
func hPlayerRewards2015(ctx *Ctx) ([]byte, bool) {
	if ctx.Sess == nil || ctx.Sess.Srv == nil || ctx.Sess.Srv.DB == nil {
		return encodeArgsCode(ctx, "185")
	}
	srv := ctx.Sess.Srv
	cctx := ctx.Sess.pushCtx()
	rows, err := srv.playerRewardItems(cctx, ctx.Sess.Userid)
	if err != nil {
		ctx.Log.Error("3/16 奖赏查询失败", "err", err)
		return encodeArgsCode(ctx, "185")
	}
	items := make([]*proto.Value, 0, len(rows))
	for _, it := range rows {
		obj, merr := makeRewardEntry(it)
		if merr != nil {
			ctx.Log.Error("3/16 奖赏条目编码失败", "err", merr, "guid", it.GUID)
			continue
		}
		items = append(items, obj)
	}
	body, err := proto.EncodeValue(proto.Args(proto.S("185"), items...), ctx.Sess.Table())
	if err != nil {
		ctx.Log.Error("3/16 奖赏响应编码失败", "err", err)
		return encodeArgsCode(ctx, "185")
	}
	return body, true
}

// makeRewardEntry 由物品行统一构造 enikeammfen(2015) 对象，3/16 列表与
// 3/40 详情共用，避免两处字段口径漂移。
//
// 注意 f6/f7：wire op 虽为 U32，但客户端 onobdmohahp 把 hjilekjlgbk(工艺) 与
// nmidlgejlcp(耐久) 声明为 float，即 4 字节承载 IEEE754 位模式；必须走
// proto.F32（内部写 F32Bits），用 proto.U32 送整数会被客户端读成接近 0。
func makeRewardEntry(it *store.Item) (*proto.Value, error) {
	kind := uint32(0) // 0=背包/其它
	if it.Container == "house" {
		kind = 10 // 房/家（奖杯陈列位）
	}
	created := netTicksNow()
	updatedFT := proto.WindowsFiletimeNow()
	if it.CreatedTS > 0 {
		created = netTicksFromUnix(it.CreatedTS)
		updatedFT = proto.UnixToFiletime(int64(it.CreatedTS))
	}
	visibility := uint32(0)
	if fields, err := FieldsFromJSON(it.FieldsJSON); err == nil {
		visibility = rewardVisibilityFromFields(fields)
	}
	wm := it.Workmanship
	if wm <= 0 {
		wm = 1
	}
	du := it.Durability
	if du <= 0 {
		du = 1
	}
	return wiregen.MakeObj("enikeammfen", map[int]*proto.Value{
		0:  proto.U32(placeOfItem(it.ItemID)),
		1:  proto.U32(visibility), // 0=对其他玩家可见、1=隐藏
		2:  proto.Guid(it.GUID),
		3:  proto.Guid(""),
		4:  proto.U32(kind),
		5:  proto.U32(uint32(it.NumericID)),
		6:  proto.F32(float32(wm)),
		7:  proto.F32(float32(du)),
		8:  proto.U64(uint64(created)),
		9:  proto.U64(uint64(updatedFT)),
		10: proto.U32(0),
		11: proto.Null(),
		12: proto.Null(),
		13: proto.U32(uint32(it.Qty)),
		14: proto.U32(0),
	})
}

// placeOfItem 从奖牌 item_id 推「名次」（enikeammfen.f0 = ijahnpdddfc，
// 客户端本地化 kDlg_Rewards_Position = 第{0}名）。认竞赛类 _1~_5 家族
// （ft_*、weekly_medal_*、ann_rec_*），刻意排除 statuette_fisherman_10 /
// trophy_level_* 这类「计数/等级」后缀，避免把 10 当成第10名。非名次类返回 0。
func placeOfItem(itemID string) uint32 {
	if !strings.HasPrefix(itemID, "ft_") &&
		!strings.HasPrefix(itemID, "weekly_medal_") &&
		!strings.HasPrefix(itemID, "ann_rec_") {
		return 0
	}
	switch {
	case strings.HasSuffix(itemID, "_1"):
		return 1
	case strings.HasSuffix(itemID, "_2"):
		return 2
	case strings.HasSuffix(itemID, "_3"):
		return 3
	case strings.HasSuffix(itemID, "_4"):
		return 4
	case strings.HasSuffix(itemID, "_5"):
		return 5
	}
	return 0
}

// rewardVisibilityFromFields 读取 enikeammfen.f1；缺失时按可见处理。
func rewardVisibilityFromFields(fields []proto.Field) uint32 {
	if len(fields) <= 1 || fields[1].Num == nil {
		return 0
	}
	return uint32(*fields[1].Num)
}

// setRewardVisibility 把 3/40 的可见性参数写入奖赏物品的 enikeammfen.f1。
func setRewardVisibility(ctx context.Context, db *store.DB, it *store.Item, visibility bool) error {
	if db == nil || it == nil || it.WireClass != "enikeammfen" || it.TypeID != 2015 {
		return fmt.Errorf("不是奖赏物品")
	}
	fields, err := FieldsFromJSON(it.FieldsJSON)
	if err != nil || len(fields) <= 1 {
		// 历史/测试行可能只有基础列、没有完整 fields_json；补成标准
		// enikeammfen 模板后再写 f1，避免可见性按钮永久无效。
		obj, merr := wiregen.MakeObj("enikeammfen", nil)
		if merr != nil {
			return merr
		}
		fields = obj.Fields
	}
	value := uint64(0)
	if visibility {
		value = 1
	}
	fields[1].Num = ptrU64(value)
	fj, err := FieldsToJSON(fields)
	if err != nil {
		return err
	}
	if err := db.UpdateItemFields(ctx, it.GUID, fj, it.Qty, it.Durability, it.Workmanship, it.RigGUID); err != nil {
		return err
	}
	it.FieldsJSON = fj
	return nil
}

func hAchievements305(ctx *Ctx) ([]byte, bool) {
	if ctx.Sess == nil || ctx.Sess.Srv == nil || ctx.Sess.Srv.DB == nil {
		return encodeArgsCode(ctx, "114")
	}
	v, err := ctx.Sess.Srv.buildAchievements305(ctx.Sess.Srv.dbCtx(), ctx.Sess.Userid)
	if err != nil {
		ctx.Log.Error("3/3 成就查询失败", "err", err)
		return encodeArgsCode(ctx, "114")
	}
	body, err := proto.EncodeValue(v, ctx.Sess.Table())
	if err != nil {
		ctx.Log.Error("3/3 成就编码失败", "err", err)
		return encodeArgsCode(ctx, "114")
	}
	// 纯查询：不判定、不写库、不推送。成就解锁与弹窗只发生在玩法事件后。
	return body, true
}

func hFishTrophies323(ctx *Ctx) ([]byte, bool) {
	if ctx.Sess == nil || ctx.Sess.Srv == nil || ctx.Sess.Srv.DB == nil {
		return encodeArgsCode(ctx, "132")
	}
	v, err := ctx.Sess.Srv.buildFishTrophies323(ctx.Sess.Srv.dbCtx(), ctx.Sess.Userid)
	if err != nil {
		ctx.Log.Error("3/17 鱼种奖杯查询失败", "err", err)
		return encodeArgsCode(ctx, "132")
	}
	// 面板刚被填满的水域在这里兜底补发奖杯：正常情况下入护时已经发过，
	// 这条只是防止「发奖杯那一步失败/进程中断」导致面板满了却没有奖杯。
	ctx.Sess.pushWaterwayTrophies(ctx.Sess.Srv.ensureWaterwayTrophies(ctx.Sess.Srv.dbCtx(), ctx.Sess.Userid))
	// 稀有鱼 / 超级稀有鱼的鱼种数里程碑在这里兜底：面板就是玩家看「我有多少种
	// 稀有鱼」的地方，翻面板时顺手把该给的奖座补齐并推 4/22。
	pushRarityMilestoneItems(ctx.Sess, ctx.Sess.Srv.ensureRarityMilestones(ctx.Sess.Srv.dbCtx(), ctx.Sess.Userid))
	body, err := proto.EncodeValue(v, ctx.Sess.Table())
	if err != nil {
		ctx.Log.Error("3/17 鱼种奖杯编码失败", "err", err)
		return encodeArgsCode(ctx, "132")
	}
	return body, true
}

func encodeArgsCode(ctx *Ctx, code string) ([]byte, bool) {
	body, err := proto.EncodeValue(proto.Args(proto.S(code)), ctx.Sess.Table())
	if err != nil {
		ctx.Log.Error("空参数列表编码失败", "code", code, "err", err)
		return emptyArgsBody(), true
	}
	return body, true
}
