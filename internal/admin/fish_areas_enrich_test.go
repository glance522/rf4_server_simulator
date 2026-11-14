package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/logging"
)

// /api/fish/areas 富化字段：中文名→level key→网格标定（grid）/海图（sea）/无图（none）。
// 期望值按 docs/fish-query-coordinates.md 公式手算，不用被测代码复算。
func TestFishAreasEnrichment(t *testing.T) {
	db := testDB(t)
	for _, row := range []string{
		`INSERT INTO loc_fish(loc, fish_key, catches) VALUES ('旧奥斯特罗格湖', 'bream', 1)`,
		`INSERT INTO loc_fish(loc, fish_key, catches) VALUES ('挪威海', 'cod', 1)`,
		`INSERT INTO loc_fish(loc, fish_key, catches) VALUES ('未登记水域', 'roach', 1)`,
		`INSERT INTO loc_alias(alias, loc) VALUES ('level_004_torfyanoe_ozero', '旧奥斯特罗格湖')`,
		`INSERT INTO loc_alias(alias, loc) VALUES ('level_012_north_sea', '挪威海')`,
	} {
		if _, err := db.SQL.Exec(row); err != nil {
			t.Fatal(err, row)
		}
	}
	if err := db.EnsureAdmin(context.Background(), "admin", "admin123"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Admin{HTTPAddr: "127.0.0.1:0", JWTSecret: "test"}
	srv := NewServer(cfg, db, logging.Discard(), WebFS())
	if _, err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	login := struct{ Token string }{}
	r, _ := http.NewRequest("POST", "http://"+srv.Addr()+"/api/login", strings.NewReader(`{"username":"admin","password":"admin123"}`))
	r.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(resp.Body).Decode(&login)
	resp.Body.Close()
	if login.Token == "" {
		t.Fatal("登录失败")
	}

	req, _ := http.NewRequest("GET", "http://"+srv.Addr()+"/api/fish/areas", nil)
	req.Header.Set("Authorization", "Bearer "+login.Token)
	resp, err = (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var rows []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	byLoc := map[string]map[string]any{}
	for _, row := range rows {
		byLoc[row["loc"].(string)] = row
	}
	if len(rows) != 3 {
		t.Fatalf("返回 %d 行, 期望 3: %v", len(rows), rows)
	}

	grid := byLoc["旧奥斯特罗格湖"]
	if grid["level"] != "level_004_torfyanoe_ozero" || grid["map_kind"] != "grid" {
		t.Fatalf("grid 行定位字段错误: %v", grid)
	}
	if grid["texture"] != "maps/map_level_004_torfyanoe.webp" {
		t.Fatalf("grid 行贴图名错误（特例映射）: %v", grid["texture"])
	}
	cal, ok := grid["calibration"].(map[string]any)
	if !ok {
		t.Fatalf("grid 行缺 calibration: %v", grid)
	}
	if cal["origin_x"] != float64(0) || cal["origin_z"] != float64(0) || cal["size"] != float64(400) {
		t.Fatalf("grid 行场景标定错误: %v", cal)
	}
	paper, ok := cal["paper"].(map[string]any)
	if !ok {
		t.Fatalf("grid 行缺 paper 标定: %v", cal)
	}
	// 客户端 float32 标定经 JSON 最短表示往返：56/37 精确，4.824 按 float32 最短串比对。
	if paper["offset_x"] != float64(56) || paper["offset_z"] != float64(37) || paper["scale"] != float64(4.824) {
		t.Fatalf("grid 行纸图标定错误: %v", paper)
	}
	if paper["width"] != float64(2048) || paper["height"] != float64(2048) {
		t.Fatalf("grid 行贴图尺寸错误: %v", paper)
	}

	sea := byLoc["挪威海"]
	if sea["level"] != "level_012_north_sea" || sea["map_kind"] != "sea" {
		t.Fatalf("sea 行定位字段错误: %v", sea)
	}
	if sea["texture"] != "maps/map_level_012_north_sea.webp" {
		t.Fatalf("sea 行贴图名错误: %v", sea["texture"])
	}
	// 海图带客户端纸地图标定（SeaPaper）+ 显示原点偏移（游戏内显示 Z=世界Z/5+420）
	seaCal, ok := sea["calibration"].(map[string]any)
	if !ok {
		t.Fatalf("sea 行应带纸地图标定: %v", sea["calibration"])
	}
	if seaCal["display_origin_z"] != float64(-2100) {
		t.Fatalf("sea 行显示原点 z 应为 -2100: %v", seaCal["display_origin_z"])
	}
	if p, ok := seaCal["paper"].(map[string]any); !ok || p["scale"] != float64(0.38) {
		t.Fatalf("sea 行纸图标定错误: %v", seaCal["paper"])
	}

	none := byLoc["未登记水域"]
	if none["level"] != "" || none["map_kind"] != "none" || none["texture"] != "" || none["calibration"] != nil {
		t.Fatalf("未登记别名的水域应静默降级为 none: %v", none)
	}
}
