package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
}

func (s *Server) handleCatalogItems(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 80
	}
	like := "%" + q + "%"
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := s.db.SQL.QueryContext(ctx, `
		SELECT d.item_id, d.numeric_id, COALESCE(c.name_zh,c.name,''), COALESCE(d.category,0),
		       COALESCE(d.kind,''), COALESCE(d.brand,'')
		FROM item_def d
		LEFT JOIN item_catalog c ON c.item_id=d.item_id
		WHERE d.item_id LIKE ? OR COALESCE(c.name_zh,c.name,'') LIKE ?
		GROUP BY d.item_id,d.numeric_id
		ORDER BY CASE WHEN d.item_id=? THEN 0 ELSE 1 END, d.item_id LIMIT ?`,
		like, like, q, limit)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := make([]catalogItem, 0)
	for rows.Next() {
		var v catalogItem
		if err := rows.Scan(&v.ItemID, &v.NumericID, &v.Name, &v.Category, &v.Kind, &v.Brand); err != nil {
			httpError(w, 500, err.Error())
			return
		}
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
	_ = s.db.QueuePush(r.Context(), mustJSON(map[string]any{
		"op": "send_mail", "uid": req.UID, "text": req.Text, "items": req.Items,
	}))
	detail, _ := json.Marshal(req)
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "mail_send", strconv.FormatInt(req.UID, 10), string(detail))
	writeJSON(w, map[string]any{"ok": true, "queued": true})
}
