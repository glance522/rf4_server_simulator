package admin

import (
	"net/http"
	"strconv"
	"strings"

	"rf4server/internal/store"
)

// 工坊配件价 / 工时后台入口（表 workshop_part_price，迁移 9100010）。
//
// 口径（玩家 2026-11-12 定）：库里有值就用库里的，库里没有走代码兜底，
// 任何缺失都不许让工坊报价报错。所以这三列**都可空**，NULL 的语义是
// "这一列不覆盖"，0 则是有效值（官服轮油就是 0 银 + 49 金 + 0 小时的即时件）。
//
// 这一层不需要通知 gameserver 重载：`partPriceForStore` 每次报价都直接查表
// （见 internal/gameserver/workshop_part_price.go 里"为什么不做缓存"），
// 后台改完下一次开面板就生效。

// workshopPartPriceRow 是列表行：覆盖字段平铺 + 配件中文名/品类，前端不必再查一次。
type workshopPartPriceRow struct {
	store.WorkshopPartPrice
	NameZh   string `json:"name_zh"`
	Category int64  `json:"category"`
}

// handleWorkshopPartPriceList 列出覆盖行，可按 item_id 过滤。
func (s *Server) handleWorkshopPartPriceList(w http.ResponseWriter, r *http.Request) {
	itemID := strings.TrimSpace(r.URL.Query().Get("item_id"))
	rows, err := s.db.WorkshopPartPricesByItem(r.Context(), itemID)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	// 名字映射先加载：SQLite 是单连接，主查询的 rows 还开着时再开第二个查询会互等。
	names, cats := s.workshopPartNames(r)
	out := make([]workshopPartPriceRow, 0, len(rows))
	for _, rec := range rows {
		row := workshopPartPriceRow{WorkshopPartPrice: rec}
		if n := names[rec.ItemID]; n != "" {
			row.NameZh = n
		} else {
			row.NameZh = rec.ItemID
		}
		row.Category = cats[rec.ItemID]
		out = append(out, row)
	}
	writeJSON(w, map[string]any{"rows": out, "count": len(out)})
}

// workshopPartNames 一次取齐配件的中文名与品类（item_def，只读）。
func (s *Server) workshopPartNames(r *http.Request) (names map[string]string, cats map[string]int64) {
	names = map[string]string{}
	cats = map[string]int64{}
	if s == nil || s.db == nil || s.db.SQL == nil {
		return
	}
	rows, err := s.db.SQL.QueryContext(r.Context(),
		`SELECT item_id, COALESCE(name_zh,''), COALESCE(category,0) FROM item_def`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		var cat int64
		if rows.Scan(&id, &name, &cat) == nil {
			names[id] = name
			cats[id] = cat
		}
	}
	return
}

// handleWorkshopPartPriceSave 整行写入（upsert）。
//
// 语义是**整行替换**：silver/gold/hours 传 null 或不传 = 该列不覆盖，
// 传 0 = 真的按 0 收（/即时）。所以要"只改工时、保留价格"，得把原来的
// 价格一起传回来；想彻底放弃覆盖就调 delete。
func (s *Server) handleWorkshopPartPriceSave(w http.ResponseWriter, r *http.Request) {
	var req store.WorkshopPartPrice
	if err := readJSON(r, &req); err != nil {
		httpError(w, 400, "参数错误")
		return
	}
	req.ItemID = strings.TrimSpace(req.ItemID)
	req.MapKey = strings.TrimSpace(req.MapKey)
	if req.ItemID == "" {
		httpError(w, 400, "参数错误：需要 item_id")
		return
	}
	for name, v := range map[string]*int64{"silver": req.Silver, "gold": req.Gold, "hours": req.Hours} {
		if v != nil && *v < 0 {
			httpError(w, 400, "参数错误："+name+" 不能为负")
			return
		}
	}
	if req.Silver == nil && req.Gold == nil && req.Hours == nil {
		httpError(w, 400, "参数错误：银币价/金币价/工时至少填一项（要撤销覆盖请调用 delete）")
		return
	}
	if err := s.db.SaveWorkshopPartPrice(r.Context(), req); err != nil {
		httpError(w, 400, err.Error())
		return
	}
	detail := "silver=" + ptrText(req.Silver) + " gold=" + ptrText(req.Gold) + " hours=" + ptrText(req.Hours)
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "workshop_part_price", req.ItemID+"/"+req.MapKey, detail)
	writeJSON(w, map[string]any{"ok": true})
}

// handleWorkshopPartPriceDelete 撤销某一行覆盖，撤销后立即回到标价/兜底。
func (s *Server) handleWorkshopPartPriceDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ItemID string `json:"item_id"`
		MapKey string `json:"map_key"`
	}
	if err := readJSON(r, &req); err != nil {
		httpError(w, 400, "参数错误")
		return
	}
	req.ItemID = strings.TrimSpace(req.ItemID)
	if req.ItemID == "" {
		httpError(w, 400, "参数错误：需要 item_id")
		return
	}
	if err := s.db.DeleteWorkshopPartPrice(r.Context(), req.ItemID, strings.TrimSpace(req.MapKey)); err != nil {
		httpError(w, 400, err.Error())
		return
	}
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "workshop_part_price_delete", req.ItemID+"/"+req.MapKey, "")
	writeJSON(w, map[string]any{"ok": true})
}

func ptrText(v *int64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatInt(*v, 10)
}
