package gameserver

import (
	"context"
	"math"

	"rf4server/internal/store"
)

// 工坊配件价 / 工时的后台覆盖层（迁移 9100010，规则由玩家 2026-11-12 定）。
//
// 口径只有一条：**库里有值就用库里的，库里没有就走兜底，任何一层缺失都不许
// 让报价失败**。落到实现上就是一条三级链：
//
//	workshop_part_price（该图行 → 全局行）    ← 本文件
//	  → item_price 静态价 / 属性推导 / partBasePrice / 500 兜底（partPrice 里）
//
// 为什么覆盖行命中后**不再乘部位维修倍率**：倍率（repair_part_mult.go）是用来
// 逼近官服各图价差的近似手段，运营一旦把某件的价写死，再乘一次倍率就不是他
// 填的那个数了。要按图定价就再写一行 map_key 精确行，两行并存时精确行优先。
//
// 为什么不做缓存：21/2 一次面板打开大约要算 9 个槽位 × 每个若干替代品，
// 几十次单行主键查询在本地 SQLite 上是亚毫秒级；而缓存会引入"后台改完价
// 玩家还看不到"的时序问题（species_value_rates 的进程级快照已经踩过一次）。

// workshopPartOverride 取某个配件的覆盖行（该图精确 → 全局）。查库失败也返回
// nil：覆盖表坏了只是"没有后台定价"，必须继续走标价兜底，不能让玩家修不了装备。
func (s *Server) workshopPartOverride(ctx context.Context, itemID, mapKey string) *store.WorkshopPartPrice {
	if s == nil || s.DB == nil || itemID == "" {
		return nil
	}
	rec, err := s.DB.WorkshopPartPrice(ctx, itemID, mapKey)
	if err != nil {
		return nil
	}
	return rec
}

// workshopPartPriceOverride 把覆盖行叠到标价上，返回是否命中价格覆盖。
//
// 三列的独立性：silver/gold 为 NULL 表示"这一列不覆盖"。只覆盖银币时金币按
// `银 / 110` 重算，保住"金币 = 银币/110"这条既有汇率（与 applyPartMult 一致）；
// 两列都写就原样使用——官服轮油那种"0 银 + 49 金"的写法必须能表达出来。
func workshopPartPriceOverride(rec *store.WorkshopPartPrice, silver, gold int64) (int64, int64, bool) {
	if !rec.HasPrice() {
		return silver, gold, false
	}
	if rec.Silver != nil {
		silver = *rec.Silver
		if rec.Gold == nil {
			if silver > 0 {
				gold = int64(math.Round(float64(silver) / 110))
			} else {
				gold = 0
			}
		}
	}
	if rec.Gold != nil {
		gold = *rec.Gold
	}
	return silver, gold, true
}

// workshopPartHoursFor 取"这一件"的修理工时（游戏内小时）。
//
// 覆盖行有 hours 就用它，**包括 0**——官服轮油是「49 金 / 0 小时」的即时件，
// 0 是有效值不是缺失；只有整列都没有值才回落到品类默认（workshopPartHours）。
//
// 取值跨行：该图精确行没写工时时，全局行的工时仍然有效（见 store.WorkshopPartHours）。
//
// 银币排队路径会再兜一层 `jobHours < 1 → 1`（见 hWorkshopOrder）：工时 0 的件
// 走银币排队没有意义，玩家选银币就是接受等待。金币路径本来就即时，不受影响。
func (s *Server) workshopPartHoursFor(ctx context.Context, mapKey, itemID string, cat int64) uint32 {
	if s != nil && s.DB != nil && itemID != "" {
		if h, err := s.DB.WorkshopPartHours(ctx, itemID, mapKey); err == nil && h != nil && *h >= 0 {
			return uint32(*h)
		}
	}
	return s.workshopPartHours(cat)
}
