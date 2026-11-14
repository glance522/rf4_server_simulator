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

// 鱼汛模式二选一：读取（legacy/realistic/mixed）→ 原子切换 → 队列刷新。
func TestBiteModeEndpoint(t *testing.T) {
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
	_, login := do("POST", "/api/login", `{"username":"admin","password":"admin123"}`)
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

	// 空库 = 全关 = legacy。
	if code, out := call("GET", "/api/fish/bite_mode", ""); code != 200 || out["mode"] != "legacy" {
		t.Fatalf("initial mode: %d %v", code, out)
	}
	// 切到真实鱼汛：五个开关全部为 1。
	if code, out := call("POST", "/api/fish/bite_mode", `{"mode":"realistic"}`); code != 200 || out["mode"] != "realistic" {
		t.Fatalf("set realistic: %d %v", code, out)
	}
	for _, key := range []string{"bite_gate_enabled", "fish_ecology_enabled", "fish_weather_enabled", "fish_bait_rules_enabled", "fish_distribution_enabled"} {
		var v string
		if err := db.SQL.QueryRow(`SELECT value FROM tunables WHERE key=?`, key).Scan(&v); err != nil || v != "1" {
			t.Fatalf("%s=%v err=%v", key, v, err)
		}
	}
	if code, out := call("GET", "/api/fish/bite_mode", ""); code != 200 || out["mode"] != "realistic" {
		t.Fatalf("realistic mode: %d %v", code, out)
	}
	// 手动关一个 → mixed。
	if _, err := db.SQL.Exec(`UPDATE tunables SET value='0' WHERE key='fish_weather_enabled'`); err != nil {
		t.Fatal(err)
	}
	if code, out := call("GET", "/api/fish/bite_mode", ""); code != 200 || out["mode"] != "mixed" {
		t.Fatalf("mixed mode: %d %v", code, out)
	}
	// 切回遗留：五个开关全部归零。
	if code, out := call("POST", "/api/fish/bite_mode", `{"mode":"legacy"}`); code != 200 {
		t.Fatalf("set legacy: %d %v", code, out)
	}
	var n int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM tunables WHERE key IN
		('bite_gate_enabled','fish_ecology_enabled','fish_weather_enabled','fish_bait_rules_enabled','fish_distribution_enabled')
		AND value='1'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("legacy leaves %d switches on", n)
	}
	// 非法模式拒绝。
	if code, _ := call("POST", "/api/fish/bite_mode", `{"mode":"weird"}`); code == 200 {
		t.Fatal("invalid mode accepted")
	}
}
