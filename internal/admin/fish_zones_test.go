package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/logging"
)

// 区域固定鱼种名册 API：保存 → 列表 → 修改 → 删除，含基本校验。
func TestFishZoneAPI(t *testing.T) {
	db := testDB(t)
	if err := db.EnsureAdmin(context.Background(), "admin", "admin123"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Admin{HTTPAddr: "127.0.0.1:0", JWTSecret: "test"}
	srv := NewServer(cfg, db, logging.Discard(), WebFS())
	if _, err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	base := "http://" + srv.Addr()
	client := &http.Client{Timeout: 5 * time.Second}

	do := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		r, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	code, login := do("POST", "/api/login", `{"username":"admin","password":"admin123"}`)
	if code != 200 || login["token"] == nil {
		t.Fatalf("登录失败: %d %v", code, login)
	}
	auth := login["token"].(string)
	call := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		r, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+auth)
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// 保存（新增）。
	code, out := call("POST", "/api/fish/zones",
		`{"loc":"旧奥斯特罗格湖","name":"湾口鲤区","kind":"grid_rect","gx0":2,"gz0":2,"gx1":4,"gz1":4,
		 "mode":"exclusive","boost":3,"keep_others":0.35,"enabled":true,
		 "fish":[{"fish_key":"carp","weight":2,"enabled":true},{"fish_key":"crucian","weight":1,"enabled":true}]}`)
	if code != 200 || out["ok"] != true {
		t.Fatalf("save zone: %d %v", code, out)
	}
	zoneID, _ := out["id"].(float64)
	if zoneID <= 0 {
		t.Fatalf("zone id missing: %v", out)
	}

	// 列表带名册。
	code, out = call("GET", "/api/fish/zones", "")
	if code != 200 {
		t.Fatalf("list zones: %d", code)
	}
	raw, _ := json.Marshal(out["zones"])
	if !strings.Contains(string(raw), `"fish_key":"carp"`) || !strings.Contains(string(raw), "湾口鲤区") {
		t.Fatalf("zone list missing roster: %s", raw)
	}

	// 校验：海图区号缺失、非法模式都拒绝；端点乱序会自动翻正（合法输入）。
	for _, bad := range []string{
		`{"loc":"x","kind":"sea_zone","mode":"exclusive"}`,
		`{"loc":"x","kind":"grid_rect","gx0":0,"gx1":1,"gz0":0,"gz1":1,"mode":"weird"}`,
	} {
		if code, out := call("POST", "/api/fish/zones", bad); code == 200 {
			t.Fatalf("invalid zone accepted: %s -> %v", bad, out)
		}
	}
	if code, out := call("POST", "/api/fish/zones",
		`{"loc":"x","name":"翻正区","kind":"grid_rect","gx0":5,"gx1":1,"gz0":0,"gz1":1,"mode":"exclusive","fish":[]}`); code != 200 {
		t.Fatalf("reversed rect should be normalized: %d %v", code, out)
	}

	// 改成增益区。
	code, out = call("POST", "/api/fish/zones", fmt.Sprintf(
		`{"id":%d,"loc":"旧奥斯特罗格湖","name":"湾口鲤区","kind":"grid_rect","gx0":2,"gz0":2,"gx1":4,"gz1":4,
		 "mode":"boost","boost":2.5,"keep_others":0.4,"enabled":true,
		 "fish":[{"fish_key":"carp","weight":1,"enabled":true}]}`, int64(zoneID)))
	if code != 200 {
		t.Fatalf("update zone: %d %v", code, out)
	}
	code, out = call("GET", "/api/fish/zones", "")
	raw, _ = json.Marshal(out["zones"])
	if !strings.Contains(string(raw), `"mode":"boost"`) || !strings.Contains(string(raw), `"boost":2.5`) {
		t.Fatalf("update not visible: %s", raw)
	}

	// 删除。
	if code, out = call("POST", "/api/fish/zones/delete", fmt.Sprintf(`{"id":%d}`, int64(zoneID))); code != 200 {
		t.Fatalf("delete zone: %d %v", code, out)
	}
	code, out = call("GET", "/api/fish/zones", "")
	raw, _ = json.Marshal(out["zones"])
	if strings.Contains(string(raw), "湾口鲤区") {
		t.Fatalf("zone not deleted: %s", raw)
	}
}
