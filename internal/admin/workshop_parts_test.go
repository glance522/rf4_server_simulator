package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/logging"
)

// TestWorkshopPartPriceAPI 覆盖工坊配件价覆盖层的后台入口：
// 写入 → 列表可查回（带配件名/品类）→ 删除后回到无覆盖。
//
// 口径（玩家 2026-11-12 定）：库里有值优先用库里的，库里没有走兜底。
// 三列都可空，NULL = 不覆盖，0 = 真的按 0 收（官服轮油就是 0 银 + 49 金 + 0 小时）。
func TestWorkshopPartPriceAPI(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.EnsureAdmin(ctx, "admin", "admin123"); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(config.Admin{HTTPAddr: "127.0.0.1:0", JWTSecret: "test"},
		db, logging.Discard(), WebFS())
	if _, err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	base := "http://" + srv.Addr()
	client := &http.Client{Timeout: 5 * time.Second}
	call := func(method, path, token, body string) (int, string) {
		t.Helper()
		r, _ := http.NewRequest(method, base+path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out bytes.Buffer
		_, _ = out.ReadFrom(resp.Body)
		return resp.StatusCode, out.String()
	}

	code, body := call("POST", "/api/login", "", `{"username":"admin","password":"admin123"}`)
	if code != 200 {
		t.Fatalf("login: %d %s", code, body)
	}
	var login struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(body), &login)

	// 1) 三列全空要拒绝：那是"没有覆盖"，应该走 delete。
	if code, body = call("POST", "/api/workshop/part_prices", login.Token,
		`{"item_id":"oil_fan_001"}`); code != 400 {
		t.Fatalf("三列全空应 400，实得 %d %s", code, body)
	}

	// 2) 只写银币价
	if code, body = call("POST", "/api/workshop/part_prices", login.Token,
		`{"item_id":"oil_fan_001","silver":999}`); code != 200 {
		t.Fatalf("写入失败: %d %s", code, body)
	}

	// 3) 列表应能查回，且 count=1
	code, body = call("GET", "/api/workshop/part_prices?item_id=oil_fan_001", login.Token, "")
	if code != 200 {
		t.Fatalf("列表失败: %d %s", code, body)
	}
	var list struct {
		Count int `json:"count"`
		Rows  []struct {
			ItemID string `json:"item_id"`
			MapKey string `json:"map_key"`
			Silver *int64 `json:"silver"`
			Gold   *int64 `json:"gold"`
			Hours  *int64 `json:"hours"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("解析列表: %v %s", err, body)
	}
	if list.Count != 1 || list.Rows[0].ItemID != "oil_fan_001" || list.Rows[0].Silver == nil || *list.Rows[0].Silver != 999 {
		t.Fatalf("列表内容不对: %s", body)
	}
	if list.Rows[0].Gold != nil || list.Rows[0].Hours != nil {
		t.Fatalf("未填的列必须是 null（表示不覆盖）: %s", body)
	}

	// 4) 补一条"0 银 + 49 金 + 0 小时"的官服轮油写法：0 必须原样落库
	if code, body = call("POST", "/api/workshop/part_prices", login.Token,
		`{"item_id":"oil_fan_002","silver":0,"gold":49,"hours":0}`); code != 200 {
		t.Fatalf("写入 0 值失败: %d %s", code, body)
	}
	code, body = call("GET", "/api/workshop/part_prices?item_id=oil_fan_002", login.Token, "")
	if code != 200 {
		t.Fatalf("列表失败: %d %s", code, body)
	}
	if !strings.Contains(body, `"silver":0`) || !strings.Contains(body, `"gold":49`) || !strings.Contains(body, `"hours":0`) {
		t.Fatalf("0 值应原样返回（不能用 null 吃掉）: %s", body)
	}

	// 5) 负数要拒绝
	if code, body = call("POST", "/api/workshop/part_prices", login.Token,
		`{"item_id":"oil_fan_002","hours":-1}`); code != 400 {
		t.Fatalf("负工时应 400，实得 %d %s", code, body)
	}

	// 6) 删除后回到无覆盖
	if code, body = call("POST", "/api/workshop/part_prices/delete", login.Token,
		`{"item_id":"oil_fan_001"}`); code != 200 {
		t.Fatalf("删除失败: %d %s", code, body)
	}
	code, body = call("GET", "/api/workshop/part_prices?item_id=oil_fan_001", login.Token, "")
	if code != 200 || !strings.Contains(body, `"count":0`) {
		t.Fatalf("删除后列表应为空: %d %s", code, body)
	}
}
