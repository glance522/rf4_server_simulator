package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"rf4server/internal/store"
)

type catalogItem struct {
	ItemID    string `json:"item_id"`
	NumericID int64  `json:"numeric_id"`
	Name      string `json:"name"`
	Category  int64  `json:"category"`
	Kind      string `json:"kind"`
	Brand     string `json:"brand"`
	Group     string `json:"group"`
	GroupKey  string `json:"group_key"`
}

type catalogGroup struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Count int64  `json:"count"`
	Order int    `json:"-"`
}

const catalogKindExpr = `COALESCE(NULLIF(c.kind,''),NULLIF(d.kind,''),'')`

func catalogGroupFor(kind string, category int64) (key, name string, order int) {
	switch {
	case category == 6 || kind == "reel":
		return "reel", "完整渔轮", 10
	case category == 27 || kind == "bait" || kind == "bait_boil":
		return "bait", "饵料", 20
	case category == 26:
		return "food", "食物与饮料", 30
	case kind == "rod" || kind == "rod_casting" || kind == "rod_match" ||
		kind == "rod_spinning" || kind == "rod_telescopic" || kind == "feeder":
		return "rod", "鱼竿", 40
	case strings.HasPrefix(kind, "lure_"):
		return "lure", "拟饵", 50
	case kind == "float":
		return "float", "浮漂", 60
	case kind == "container":
		return "container", "套装与容器", 70
	default:
		return "other", "其他商品", 100
	}
}

func completeCatalogCondition() string {
	return catalogKindExpr + ` <> 'rod_blank'
		AND COALESCE(d.category,0) NOT IN (4,5,7,8,9,10,34,36,48,49,50,51,52,53,54,57)`
}

func catalogGroupCondition(key string) (string, bool) {
	switch key {
	case "reel":
		return `(COALESCE(d.category,0)=6 OR ` + catalogKindExpr + `='reel')`, true
	case "bait":
		return `(COALESCE(d.category,0)=27 OR ` + catalogKindExpr + ` IN ('bait','bait_boil'))`, true
	case "food":
		return `COALESCE(d.category,0)=26`, true
	case "rod":
		return catalogKindExpr + ` IN ('rod','rod_casting','rod_match','rod_spinning','rod_telescopic','feeder')`, true
	case "lure":
		return catalogKindExpr + ` LIKE 'lure_%'`, true
	case "float":
		return catalogKindExpr + `='float'`, true
	case "container":
		return catalogKindExpr + `='container'`, true
	case "other":
		return `NOT (` + strings.Join([]string{
			`COALESCE(d.category,0)=6`,
			`COALESCE(d.category,0)=27`,
			`COALESCE(d.category,0)=26`,
			catalogKindExpr + ` IN ('reel','rod','rod_casting','rod_match','rod_spinning','rod_telescopic','feeder','float','container')`,
			catalogKindExpr + ` LIKE 'lure_%'`,
		}, " OR ") + `)`, true
	default:
		return "", false
	}
}

func catalogJoins(r *http.Request) string {
	if r.URL.Query().Get("shop_only") == "1" {
		return ` JOIN shop_item s ON s.item_id=d.item_id`
	}
	return ""
}

func (s *Server) handleCatalogGroups(w http.ResponseWriter, r *http.Request) {
	conditions := []string{completeCatalogCondition()}
	args := make([]any, 0)
	if group := strings.TrimSpace(r.URL.Query().Get("group")); group != "" {
		where, ok := catalogGroupCondition(group)
		if !ok {
			httpError(w, 400, "未知商品分类")
			return
		}
		conditions = append(conditions, where)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := s.db.SQL.QueryContext(ctx, `
		SELECT COALESCE(d.category,0), `+catalogKindExpr+`, COUNT(DISTINCT d.item_id)
		FROM item_def d
		LEFT JOIN item_catalog c ON c.item_id=d.item_id`+catalogJoins(r)+`
		WHERE `+strings.Join(conditions, " AND ")+`
		GROUP BY d.category,c.kind,d.kind`, args...)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	defer rows.Close()
	groups := make(map[string]*catalogGroup)
	for rows.Next() {
		var category, count int64
		var kind string
		if err := rows.Scan(&category, &kind, &count); err != nil {
			httpError(w, 500, err.Error())
			return
		}
		key, name, order := catalogGroupFor(kind, category)
		g := groups[key]
		if g == nil {
			g = &catalogGroup{Key: key, Name: name, Order: order}
			groups[key] = g
		}
		g.Count += count
	}
	out := make([]catalogGroup, 0, len(groups))
	for _, group := range groups {
		out = append(out, *group)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	writeJSON(w, out)
}

func (s *Server) handleCatalogItems(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 80
	}
	like := "%" + q + "%"
	conditions := []string{"(d.item_id LIKE ? OR COALESCE(c.name_zh,c.name,'') LIKE ?)"}
	args := []any{like, like}
	if r.URL.Query().Get("complete_only") == "1" {
		conditions = append(conditions, completeCatalogCondition())
	}
	if group := strings.TrimSpace(r.URL.Query().Get("group")); group != "" {
		where, ok := catalogGroupCondition(group)
		if !ok {
			httpError(w, 400, "未知商品分类")
			return
		}
		conditions = append(conditions, where)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	query := `
		SELECT d.item_id, d.numeric_id, COALESCE(c.name_zh,c.name,''), COALESCE(d.category,0),
		       COALESCE(d.kind,''), COALESCE(d.brand,''), COALESCE(c.kind,'')
		FROM item_def d
		LEFT JOIN item_catalog c ON c.item_id=d.item_id` + catalogJoins(r) + `
		WHERE ` + strings.Join(conditions, " AND ") + `
		GROUP BY d.item_id,d.numeric_id
		ORDER BY CASE WHEN d.item_id=? THEN 0 ELSE 1 END, d.item_id LIMIT ?`
	args = append(args, q, limit)
	rows, err := s.db.SQL.QueryContext(ctx, query, args...)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := make([]catalogItem, 0)
	for rows.Next() {
		var v catalogItem
		var catalogKind string
		if err := rows.Scan(&v.ItemID, &v.NumericID, &v.Name, &v.Category, &v.Kind, &v.Brand, &catalogKind); err != nil {
			httpError(w, 500, err.Error())
			return
		}
		if catalogKind == "" {
			catalogKind = v.Kind
		}
		v.GroupKey, v.Group, _ = catalogGroupFor(catalogKind, v.Category)
		out = append(out, v)
	}
	writeJSON(w, out)
}

func (s *Server) handleStores(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.ListAdminStores(r.Context())
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	writeJSON(w, rows)
}

func (s *Server) handleStoreItems(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := s.db.ListAdminStoreItems(r.Context(), r.URL.Query().Get("store_id"),
		r.URL.Query().Get("q"), limit)
	if err != nil {
		httpError(w, 400, err.Error())
		return
	}
	writeJSON(w, rows)
}

func (s *Server) handleStoreItemUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StoreID string `json:"store_id"`
		ItemID  string `json:"item_id"`
		Silver  int64  `json:"silver"`
		Gold    int64  `json:"gold"`
		Stock   int64  `json:"stock"`
	}
	if err := readJSON(r, &req); err != nil || req.StoreID == "" || req.ItemID == "" {
		httpError(w, 400, "参数错误")
		return
	}
	if err := s.db.SetAdminShopOverride(r.Context(), req.StoreID, req.ItemID,
		req.Silver, req.Gold, req.Stock, adminOf(r)); err != nil {
		httpError(w, 400, err.Error())
		return
	}
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "shop_override", req.StoreID+"/"+req.ItemID,
		fmt.Sprintf("silver=%d gold=%d stock=%d", req.Silver, req.Gold, req.Stock))
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleStoreItemReset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StoreID string `json:"store_id"`
		ItemID  string `json:"item_id"`
	}
	if err := readJSON(r, &req); err != nil || req.StoreID == "" || req.ItemID == "" {
		httpError(w, 400, "参数错误")
		return
	}
	if err := s.db.ResetAdminShopOverride(r.Context(), req.StoreID, req.ItemID); err != nil {
		httpError(w, 500, err.Error())
		return
	}
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "shop_override_reset", req.StoreID+"/"+req.ItemID, "")
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleFishAreas(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.ListFishAreasAdmin(r.Context())
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	writeJSON(w, rows)
}

func (s *Server) handleFishConfig(w http.ResponseWriter, r *http.Request) {
	loc := strings.TrimSpace(r.URL.Query().Get("loc"))
	if loc == "" {
		httpError(w, 400, "缺少 loc")
		return
	}
	cfg, err := s.db.GetAdminFishConfig(r.Context(), loc)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	writeJSON(w, cfg)
}

func (s *Server) handleFishConfigSave(w http.ResponseWriter, r *http.Request) {
	var req store.FishConfigSave
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Area) == "" {
		httpError(w, 400, "参数错误")
		return
	}
	if err := s.db.SaveAdminFishConfig(r.Context(), req, adminOf(r)); err != nil {
		httpError(w, 400, err.Error())
		return
	}
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "fish_config", req.Area,
		fmt.Sprintf("probability=%.3f fixed=%.2f scale=%.2f fish=%d",
			req.Setting.BiteProbability, req.Setting.BiteDelayFixed,
			req.Setting.BiteDelayScale, len(req.Fish)))
	_ = s.db.QueuePush(r.Context(), mustJSON(map[string]any{"op": "reload_tunables"}))
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleFishConfigReset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Area string `json:"area"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Area) == "" {
		httpError(w, 400, "参数错误")
		return
	}
	if err := s.db.ResetAdminFishConfig(r.Context(), req.Area); err != nil {
		httpError(w, 500, err.Error())
		return
	}
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "fish_config_reset", req.Area, "")
	_ = s.db.QueuePush(r.Context(), mustJSON(map[string]any{"op": "reload_tunables"}))
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleFishPool(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	like := "%" + q + "%"
	rows, err := s.db.SQL.QueryContext(r.Context(), `
		SELECT w.fish_key, COALESCE(MAX(NULLIF(l.name_zh,'')),w.fish_key),
		       COALESCE(w.min_g,0), COALESCE(w.max_g,0)
		FROM species_weight w LEFT JOIN loc_fish l ON l.fish_key=w.fish_key
		WHERE w.fish_key LIKE ? OR l.name_zh LIKE ?
		GROUP BY w.fish_key ORDER BY w.fish_key LIMIT ?`, like, like, limit)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var key, name string
		var minG, maxG int64
		if err := rows.Scan(&key, &name, &minG, &maxG); err != nil {
			httpError(w, 500, err.Error())
			return
		}
		out = append(out, map[string]any{"fish_key": key, "name_zh": name, "min_g": minG, "max_g": maxG})
	}
	writeJSON(w, out)
}

func (s *Server) handlePlayerSkillUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UID     int64   `json:"uid"`
		SkillID int64   `json:"skill_id"`
		Points  float64 `json:"points"`
	}
	if err := readJSON(r, &req); err != nil || req.UID == 0 || req.SkillID == 0 {
		httpError(w, 400, "参数错误")
		return
	}
	if err := s.db.SetPlayerSkillPointsAdmin(r.Context(), req.UID, req.SkillID, req.Points); err != nil {
		httpError(w, 400, err.Error())
		return
	}
	_ = s.db.QueuePush(r.Context(), mustJSON(map[string]any{
		"op": "refresh_skills", "uid": req.UID,
	}))
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "skill_proficiency",
		fmt.Sprintf("%d/%d", req.UID, req.SkillID), fmt.Sprintf("%.3f", req.Points))
	writeJSON(w, map[string]any{"ok": true})
}

// handlePlayerSkillPoints 直接设置玩家的"未分配技能点"。
// 未分配点数 = 等级累计点数 - 已投入节点等级 + 管理员偏移，
// 因此这里把目标值换算成偏移量后写入 kv，不修改等级或节点数据。
func (s *Server) handlePlayerSkillPoints(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UID    int64 `json:"uid"`
		Points int64 `json:"points"`
	}
	if err := readJSON(r, &req); err != nil || req.UID == 0 || req.Points < 0 {
		httpError(w, 400, "参数错误")
		return
	}
	if req.Points > 1000000 {
		req.Points = 1000000
	}
	total, spent, _, _ := s.db.SkillPointsAvailable(r.Context(), req.UID)
	bonus := req.Points - (total - spent)
	if err := s.db.SetSkillBonus(r.Context(), req.UID, bonus); err != nil {
		httpError(w, 500, err.Error())
		return
	}
	_ = s.db.QueuePush(r.Context(), mustJSON(map[string]any{
		"op": "refresh_skills", "uid": req.UID,
	}))
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "skill_points",
		fmt.Sprintf("%d", req.UID), fmt.Sprintf("available=%d bonus=%d", req.Points, bonus))
	_, _, _, avail := s.db.SkillPointsAvailable(r.Context(), req.UID)
	writeJSON(w, map[string]any{"ok": true, "available_points": avail})
}

func (s *Server) handlePlayerItemDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UID   int64    `json:"uid"`
		GUIDs []string `json:"guids"`
	}
	if err := readJSON(r, &req); err != nil || req.UID == 0 || len(req.GUIDs) == 0 {
		httpError(w, 400, "参数错误")
		return
	}
	_ = s.db.QueuePush(r.Context(), mustJSON(map[string]any{
		"op": "delete_item", "uid": req.UID, "guids": req.GUIDs,
	}))
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "item_delete",
		strconv.FormatInt(req.UID, 10), strings.Join(req.GUIDs, ","))
	writeJSON(w, map[string]any{"ok": true, "queued": true})
}

// handlePlayerMailDelete 删除指定玩家的若干封邮件。
//
// 用于清理"领不出来"的历史邮件（例如附件数量超限的坏包）：
// 领取侧现在会回业务错误，但玩家自己没法处理，需要服主删掉或重发。
func (s *Server) handlePlayerMailDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UID int64   `json:"uid"`
		IDs []int64 `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil || req.UID == 0 || len(req.IDs) == 0 {
		httpError(w, 400, "参数错误")
		return
	}
	ids := make([]string, 0, len(req.IDs))
	for _, id := range req.IDs {
		if id > 0 {
			// 必须以十进制入队：消费端 delete_mail 会先按十进制解析
			// （见 gameserver 的 parseMailIDArg），不能用十六进制 GUID。
			ids = append(ids, strconv.FormatInt(id, 10))
		}
	}
	if len(ids) == 0 {
		httpError(w, 400, "参数错误")
		return
	}
	_ = s.db.QueuePush(r.Context(), mustJSON(map[string]any{
		"op": "delete_mail", "uid": req.UID, "mail_ids": ids,
	}))
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "mail_delete",
		strconv.FormatInt(req.UID, 10), strings.Join(ids, ","))
	writeJSON(w, map[string]any{"ok": true, "queued": true})
}

func (s *Server) handlePlayerKeepnetAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UID       int64   `json:"uid"`
		Species   string  `json:"species"`
		WeightG   int64   `json:"weight_g"`
		LengthM   float64 `json:"length_m"`
		Cat       int64   `json:"cat"`
		KeepLoc   string  `json:"keep_loc"`
		KeepSpot  int64   `json:"keep_spot"`
		KeepBait  string  `json:"keep_bait"`
		KeepLevel string  `json:"keep_level"`
	}
	if err := readJSON(r, &req); err != nil || req.UID == 0 || strings.TrimSpace(req.Species) == "" || req.WeightG <= 0 {
		httpError(w, 400, "参数错误：需要 uid、鱼种和正数重量")
		return
	}
	if req.Cat < 1 || req.Cat > 4 {
		req.Cat = 1
	}
	entry := store.KeepnetEntry{
		UID: req.UID, Species: strings.TrimSpace(req.Species), WeightG: req.WeightG,
		LengthM: req.LengthM, Cat: req.Cat, KeepLoc: req.KeepLoc,
		KeepSpot: req.KeepSpot, KeepBait: req.KeepBait, KeepLevel: req.KeepLevel,
	}
	_ = s.db.QueuePush(r.Context(), mustJSON(map[string]any{
		"op": "add_keepnet", "uid": req.UID, "keepnet": entry,
	}))
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "keepnet_add",
		strconv.FormatInt(req.UID, 10), fmt.Sprintf("%s %dg", req.Species, req.WeightG))
	writeJSON(w, map[string]any{"ok": true, "queued": true})
}

func (s *Server) handlePlayerKeepnetDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UID   int64    `json:"uid"`
		GUIDs []string `json:"guids"`
	}
	if err := readJSON(r, &req); err != nil || req.UID == 0 || len(req.GUIDs) == 0 {
		httpError(w, 400, "参数错误")
		return
	}
	_ = s.db.QueuePush(r.Context(), mustJSON(map[string]any{
		"op": "delete_keepnet", "uid": req.UID, "keepnet_guids": req.GUIDs,
	}))
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "keepnet_delete",
		strconv.FormatInt(req.UID, 10), strings.Join(req.GUIDs, ","))
	writeJSON(w, map[string]any{"ok": true, "queued": true})
}

func (s *Server) handleMailSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UID   int64            `json:"uid"`
		Text  string           `json:"text"`
		Items []store.MailItem `json:"items"`
	}
	if err := readJSON(r, &req); err != nil || req.UID == 0 || (strings.TrimSpace(req.Text) == "" && len(req.Items) == 0) {
		httpError(w, 400, "参数错误：需要 uid、正文或附件")
		return
	}
	for i := range req.Items {
		if req.Items[i].Qty <= 0 {
			req.Items[i].Qty = 1
		}
	}
	// 发不出来的邮件不要发出去：附件按领取路径同口径先干跑一遍。
	// 否则玩家会收到一封永远领不出来的信，而后台当场显示"已下发"。
	vctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	err := s.validateMailAttachments(vctx, req.Items)
	cancel()
	if err != nil {
		httpError(w, 400, err.Error())
		return
	}
	if err := s.db.QueuePush(r.Context(), mustJSON(map[string]any{
		"op": "send_mail", "uid": req.UID, "text": req.Text, "items": req.Items,
	})); err != nil {
		httpError(w, 500, "写入发送队列失败: "+err.Error())
		return
	}
	detail, _ := json.Marshal(req)
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "mail_send", strconv.FormatInt(req.UID, 10), string(detail))
	writeJSON(w, map[string]any{"ok": true, "queued": true})
}
