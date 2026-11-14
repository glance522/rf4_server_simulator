package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"rf4server/internal/fishmaps"
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

// fishAreaPaper 标定的纸地图数值，字段与 fishmaps.PaperMap 一致（float32 精度原样下发）。
type fishAreaPaper struct {
	OffsetX float32 `json:"offset_x"`
	OffsetZ float32 `json:"offset_z"`
	Scale   float32 `json:"scale"`
	Width   float64 `json:"width"`
	Height  float64 `json:"height"`
}

// fishAreaCalibration 内陆网格图的场景标定（世界↔图像像素换算见 docs/fish-query-coordinates.md）。
type fishAreaCalibration struct {
	OriginX float64        `json:"origin_x"`
	OriginZ float64        `json:"origin_z"`
	Size    float64        `json:"size"`
	Paper   *fishAreaPaper `json:"paper"`
	// DisplayOriginX/Z 游戏内 HUD 显示坐标原点（显示=(世界−原点)/5）。内陆与 origin 相同；
	// 挪威海游戏内显示 Z=世界Z/5+420（原点 z=-2100），与像素标定的世界原点分离
	// （2026-11-14 实测：玩家在 198 区，游戏显示 469:349 ↔ 世界 (2345,-355)）。
	DisplayOriginX float64 `json:"display_origin_x,omitempty"`
	DisplayOriginZ float64 `json:"display_origin_z,omitempty"`
}

// fishAreaRow 在 store.FishAreaView 基础上追加地图编辑器所需的定位信息：
// map_kind: grid=可拖拽画矩形（有纸地图标定）；sea=海图区号（有图无标定）；
// none=无图（保留手填兜底）。calibration 为 nil 表示无网格标定。
type fishAreaRow struct {
	Loc         string               `json:"loc"`
	FishCount   int64                `json:"fish_count"`
	Level       string               `json:"level"`
	MapKind     string               `json:"map_kind"`
	Texture     string               `json:"texture"`
	Calibration *fishAreaCalibration `json:"calibration"`
}

// handleFishSeaNow GET：玩家当前所在海图区号。优先读 fishing_state 实时钓鱼状态
// （玩家正在海上抛竿时的 loc12_N），兜底最近一条海钓记录。这是与判鱼完全同源的
// 权威信号——管理员不用在图上找格子，直接高亮"我在哪"。
func (s *Server) handleFishSeaNow(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	var loc string
	var ts float64
	var live bool
	err := s.db.SQL.QueryRowContext(ctx,
		`SELECT loc, updated_ts FROM fishing_state
		 WHERE loc LIKE 'loc12_%' AND cast_active=1 ORDER BY updated_ts DESC LIMIT 1`).Scan(&loc, &ts)
	if err == nil {
		live = true
	} else {
		err = s.db.SQL.QueryRowContext(ctx,
			`SELECT loc, ts FROM catch_log WHERE loc LIKE 'loc12_%' ORDER BY ts DESC LIMIT 1`).Scan(&loc, &ts)
	}
	if err != nil {
		httpError(w, 404, "没有进行中的海钓，也没有海钓记录")
		return
	}
	zone := 0
	if i := strings.LastIndexByte(loc, '_'); i >= 0 {
		if n, e := strconv.Atoi(loc[i+1:]); e == nil {
			zone = n
		}
	}
	if zone <= 0 {
		httpError(w, 500, "区号解析失败: "+loc)
		return
	}
	writeJSON(w, map[string]any{"zone": zone, "loc": loc, "ts": ts, "live": live})
}

// handleFishSeaZoneLatest GET：最近一次海钓的客户端上报区号（catch_log.loc=loc12_N）。
// 海图区域命中按客户端上报区号判定，用钓获记录定位就是零误差——不需要在图上找格子。
func (s *Server) handleFishSeaZoneLatest(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	var loc, fish string
	var ts float64
	err := s.db.SQL.QueryRowContext(ctx,
		`SELECT loc, COALESCE(name_zh,fish_key), ts FROM catch_log
		 WHERE loc LIKE 'loc12_%' ORDER BY ts DESC LIMIT 1`).Scan(&loc, &fish, &ts)
	if err != nil {
		httpError(w, 404, "还没有海钓记录——去钓一条鱼再回来点")
		return
	}
	zone := 0
	if i := strings.LastIndexByte(loc, '_'); i >= 0 {
		if n, err := strconv.Atoi(loc[i+1:]); err == nil {
			zone = n
		}
	}
	if zone <= 0 {
		httpError(w, 500, "区号解析失败: "+loc)
		return
	}
	writeJSON(w, map[string]any{"zone": zone, "loc": loc, "fish": fish, "ts": ts})
}

// handleFishSeaZones GET：海图（挪威海）声呐区号的世界坐标矩形，
// 供地图编辑器渲染可点选的海图网格。
func (s *Server) handleFishSeaZones(w http.ResponseWriter, r *http.Request) {
	grids, err := fishmaps.SeaZoneGrids()
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"zones": grids})
}

func (s *Server) handleFishAreas(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.ListFishAreasAdmin(r.Context())
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	out := make([]*fishAreaRow, 0, len(rows))
	for _, row := range rows {
		item := &fishAreaRow{Loc: row.Loc, FishCount: row.FishCount, MapKind: "none"}
		if level := s.db.ResolveLevelKeyByName(r.Context(), row.Loc); level != "" {
			item.Level = level
			if name, known := fishmaps.TextureFor(level); known {
				item.Texture = "maps/" + name + ".webp"
				if sc, ok := fishmaps.Scenes[level]; ok && sc.Paper != nil {
					item.MapKind = "grid"
					item.Calibration = &fishAreaCalibration{
						OriginX: sc.OriginX, OriginZ: sc.OriginZ, Size: sc.Size,
						Paper: &fishAreaPaper{
							OffsetX: sc.Paper.OffsetX, OffsetZ: sc.Paper.OffsetZ,
							Scale: sc.Paper.Scale, Width: sc.Paper.Width, Height: sc.Paper.Height,
						},
					}
				} else {
					// 海图：区域用声呐区号语义，但几何工具（矩形/格子/圆）同样可用；
					// 挪威海带客户端标定，其他海图（当前没有）降级矢量参考图。
					item.MapKind = "sea"
					if sp := fishmaps.SeaPaper; sp != nil && level == "level_012_north_sea" {
						item.Calibration = &fishAreaCalibration{
							OriginX: 0, OriginZ: 0, Size: 0,
							DisplayOriginX: 0, DisplayOriginZ: -2100,
							Paper: &fishAreaPaper{
								OffsetX: sp.OffsetX, OffsetZ: sp.OffsetZ,
								Scale: sp.Scale, Width: sp.Width, Height: sp.Height,
							},
						}
					}
				}
			}
		}
		out = append(out, item)
	}
	writeJSON(w, out)
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
