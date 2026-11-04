package gameserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// tackleAidItem 备用渔具里的一件物品。
type tackleAidItem struct {
	ItemID string `json:"item_id"`
	Qty    int64  `json:"qty"`
}

// tackleAidDef 一张地图的备用渔具配置；Place 为空表示所有地图的默认值。
type tackleAidDef struct {
	Place string          `json:"place"`
	Kind  string          `json:"kind"` // float / spin
	Items []tackleAidItem `json:"items"`
}

// tackleAidCooldown 两次领取之间的冷却：客户端世界时钟按 24 倍速推进，
// 1 游戏日 = 60 分钟现实时间。
const tackleAidCooldown = 60 * time.Minute

// tackleAidKVKey 保存备用渔具配置的 kv 键；改这个 JSON 即可更新，不必重启进程。
const tackleAidKVKey = "tackle_aid_defs"

// defaultTackleAidDefs 内置兜底：未配置的地图都用这两套基础装备。
var defaultTackleAidDefs = []tackleAidDef{
	{
		Kind: "float",
		Items: []tackleAidItem{
			{ItemID: "tele_10012_tl16"},         // 浮子竿
			{ItemID: "spin_5820"},               // 渔轮
			{ItemID: "mono_15065", Qty: 120},    // 主线
			{ItemID: "bob_25164"},               // 浮漂
			{ItemID: "orig_20008_16"},           // 鱼钩
			{ItemID: "msinkset_30015", Qty: 42}, // 铅坠组
		},
	},
	{
		Kind: "spin",
		Items: []tackleAidItem{
			{ItemID: "spin_10001_S60L"},      // 纺车竿
			{ItemID: "spin_5820"},            // 渔轮
			{ItemID: "mono_15065", Qty: 120}, // 主线
			{ItemID: "spinner_2712"},         // 亮片拟饵
			{ItemID: "orig_20008_16"},        // 鱼钩
		},
	},
}

// spareFloatTackle / spareSpinTackle 保留给测试与兜底引用。
var (
	spareFloatTackle = defaultTackleAidDefs[0].Items
	spareSpinTackle  = defaultTackleAidDefs[1].Items
)

// tackleAidDefs 读当前生效的备用渔具表：优先 kv.tackle_aid_defs，缺失或解析失败用内置兜底。
func (s *Server) tackleAidDefs(ctx context.Context) []tackleAidDef {
	if s.DB != nil {
		var raw string
		if err := s.DB.SQL.QueryRowContext(ctx,
			`SELECT v FROM kv WHERE k=?`, tackleAidKVKey).Scan(&raw); err == nil && raw != "" {
			var defs []tackleAidDef
			if json.Unmarshal([]byte(raw), &defs) == nil && len(defs) > 0 {
				return defs
			}
		}
	}
	return defaultTackleAidDefs
}

// tackleAidItemsFor 取某地图某类型的备用渔具：先精确匹配地图，再退回 Place 为空的默认项。
func (s *Server) tackleAidItemsFor(ctx context.Context, place, kind string) ([]tackleAidItem, bool) {
	var fallback []tackleAidItem
	for _, def := range s.tackleAidDefs(ctx) {
		if def.Kind != kind {
			continue
		}
		if def.Place == place {
			return def.Items, true
		}
		if def.Place == "" && fallback == nil {
			fallback = def.Items
		}
	}
	return fallback, fallback != nil
}

// grantTackleAid 发放一套备用渔具：逐件落库、分配背包空槽，并推给客户端。
func (s *Server) grantTackleAid(ctx context.Context, c *Ctx, uid int64, items []tackleAidItem) int {
	var slot int64
	if err := s.DB.SQL.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(slot),-1)+1 FROM item WHERE uid=? AND container='backpack'`,
		uid).Scan(&slot); err != nil {
		c.Log.Warn("备用渔具取空槽失败", "err", err)
		return 0
	}
	granted := 0
	for _, it := range items {
		qty := it.Qty
		if qty <= 0 {
			qty = 1
		}
		row, gerr := s.CreateItem(ctx, uid, it.ItemID, "backpack", qty)
		if gerr != nil || row == nil {
			c.Log.Warn("备用渔具发放失败", "item", it.ItemID, "err", gerr)
			continue
		}
		if _, serr := s.DB.SQL.ExecContext(ctx,
			`UPDATE item SET slot=? WHERE guid=?`, slot, row.GUID); serr != nil {
			c.Log.Warn("备用渔具槽位设置失败", "item", it.ItemID, "slot", slot, "err", serr)
		}
		slot++
		granted++
		if c.Sess != nil {
			s.pushItemUpdate(c.Sess.Userid, row)
		}
	}
	return granted
}

// hTackleAid 处理 3/14（备用浮子渔具）与 3/15（备用纺车竿渔具）。
//
// 客户端在管理机构按 G/J 时发出，弹窗由客户端本地给出；服务端此前只回 null，
// 导致"提示领取成功但背包里没有东西"。这里按（地图 + 类型）查配置发放，
// 配置存 kv.tackle_aid_defs，各图可以给不同的装备。
func hTackleAid(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if srv == nil || srv.DB == nil {
		return []byte{0x00}, true
	}
	kind := "float"
	name := "浮子渔具"
	if ctx.Sub == 15 {
		kind = "spin"
		name = "纺车竿渔具"
	}
	cctx := ctx.Sess.pushCtx()
	place := srv.currentLevel(ctx.Sess.Userid)
	items, ok := srv.tackleAidItemsFor(cctx, place, kind)
	if !ok || len(items) == 0 {
		ctx.Log.Info("备用渔具未配置", "place", place, "kind", kind)
		return []byte{0x00}, true
	}
	// 游戏内每天只能领一次（浮子/纺车各一次）。
	// 客户端世界时钟按 24 倍速推进，1 游戏日 = 60 分钟现实时间，因此用真实时间做冷却；
	// 按真实时间记录，重登不会刷新（否则重登就能再领一次）。
	dayKey := fmt.Sprintf("tackle_aid_last:%d:%s", ctx.Sess.Userid, kind)
	var lastUnix int64
	if err := srv.DB.SQL.QueryRowContext(cctx,
		`SELECT v FROM kv WHERE k=?`, dayKey).Scan(&lastUnix); err == nil && lastUnix > 0 {
		if elapsed := time.Since(time.Unix(lastUnix, 0)); elapsed < tackleAidCooldown {
			ctx.Log.Info("备用渔具本游戏日已领取", "place", place, "kind", kind,
				"elapsed_min", int(elapsed.Minutes()), "cooldown_min", int(tackleAidCooldown.Minutes()))
			// 回 false：客户端据此提示"已领取"而不是"领取成功"。
			return []byte{0x0F}, true
		}
	}
	granted := srv.grantTackleAid(cctx, ctx, ctx.Sess.Userid, items)
	if granted > 0 {
		_, _ = srv.DB.SQL.ExecContext(cctx,
			`INSERT INTO kv(k, v) VALUES(?, ?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`,
			dayKey, strconv.FormatInt(time.Now().Unix(), 10))
	}
	ctx.Log.Info("备用渔具已发放", "place", place, "kind", name, "granted", granted,
		"total", len(items))
	return []byte{0x00}, true
}
