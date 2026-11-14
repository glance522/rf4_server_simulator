package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/store"
)

func testDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开测试 SQLite: %v", err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestAdminAPI 全 API 流程：登录 → 仪表盘 → 调参 → 玩家列表/改数据/发物品/发邮件 → 审计。
func TestAdminAPI(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	_ = db.EnsureAdmin(context.Background(), "admin", "admin123")
	uid := int64(1000001)
	_, _ = db.EnsureProfile(context.Background(), uid)
	// 发邮件现在会在入队前校验附件物品是否存在（见 mail_validate.go），
	// 因此这里要像真实库一样先有物品定义。
	if _, err := db.SQL.Exec(`INSERT OR REPLACE INTO item_def(numeric_id,item_id,category,kind,brand,attrs_json)
		VALUES(9001,'worm_01',27,'bait','','{}')`); err != nil {
		t.Fatalf("seed item_def: %v", err)
	}
	if _, err := db.SQL.Exec(`INSERT OR REPLACE INTO item_catalog(item_id,name,name_zh,kind,numeric_id)
		VALUES('worm_01','Worm','红虫','bait',9001)`); err != nil {
		t.Fatalf("seed item_catalog: %v", err)
	}
	if _, err := db.SQL.Exec(`INSERT OR REPLACE INTO item_def(numeric_id,item_id,category,kind,brand,attrs_json) VALUES
		(9002,'gift_full_bait',27,'bait','','{}'),
		(9003,'gift_not_sold',27,'bait','','{}'),
		(9004,'gift_rod_blank',36,'rod_blank','','{}'),
		(9005,'gift_reel_spool',8,'reel_part','','{}'),
		(9006,'gift_reel_gear',52,'reel_part','','{}')`); err != nil {
		t.Fatalf("seed catalog item_def: %v", err)
	}
	if _, err := db.SQL.Exec(`INSERT OR REPLACE INTO item_catalog(item_id,name,name_zh,kind,numeric_id) VALUES
		('gift_full_bait','Gift Bait','完整奖励饵料','bait',9002),
		('gift_not_sold','Not Sold','未上架物品','bait',9003),
		('gift_rod_blank','Rod Blank','竿坯组件','rod_blank',9004),
		('gift_reel_spool','Reel Spool','线杯组件','reel_part',9005),
		('gift_reel_gear','Reel Gear','主齿轮组件','reel_part',9006)`); err != nil {
		t.Fatalf("seed catalog item_catalog: %v", err)
	}
	if _, err := db.SQL.Exec(`INSERT OR REPLACE INTO shop_item(store_id,item_id,silver,gold,stock) VALUES
		('store_test','gift_full_bait',100,0,50),
		('store_test','gift_rod_blank',100,0,50)`); err != nil {
		t.Fatalf("seed catalog shop_item: %v", err)
	}

	cfg := config.Admin{HTTPAddr: "127.0.0.1:0", JWTSecret: "test"}
	srv := NewServer(cfg, db, logging.Discard(), WebFS())
	errCh, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + srv.Addr()
	client := &http.Client{Timeout: 5 * time.Second}

	call := func(method, path, token, body string) (int, string) {
		var rd *bytes.Reader
		if body == "" {
			rd = bytes.NewReader(nil)
		} else {
			rd = bytes.NewReader([]byte(body))
		}
		r, _ := http.NewRequest(method, base+path, rd)
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return resp.StatusCode, buf.String()
	}

	// 错误密码
	code, _ := call("POST", "/api/login", "", `{"username":"admin","password":"wrong"}`)
	if code != 401 {
		t.Fatalf("错误密码应 401, 实得 %d", code)
	}
	// 登录
	code, body := call("POST", "/api/login", "", `{"username":"admin","password":"admin123"}`)
	if code != 200 {
		t.Fatalf("登录应 200: %d %s", code, body)
	}
	var lr struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(body), &lr)
	token := lr.Token

	code, body = call("GET", "/api/catalog/items?q=gift_&shop_only=1&complete_only=1", token, "")
	if code != 200 || !strings.Contains(body, `"item_id":"gift_full_bait"`) || !strings.Contains(body, `"group":"饵料"`) || strings.Contains(body, "gift_not_sold") || strings.Contains(body, "gift_rod_blank") {
		t.Fatalf("完整商店商品目录异常: %d %s", code, body)
	}
	// 阶段奖励目录按完整静态目录分层读取：未上架的活动专属商品也可选，竿坯仍排除。
	code, body = call("GET", "/api/catalog/groups?complete_only=1", token, "")
	if code != 200 || !strings.Contains(body, `"key":"bait"`) {
		t.Fatalf("完整商品分类异常: %d %s", code, body)
	}
	code, body = call("GET", "/api/catalog/items?q=gift_&group=bait&complete_only=1", token, "")
	if code != 200 || !strings.Contains(body, `"item_id":"gift_full_bait"`) || !strings.Contains(body, `"item_id":"gift_not_sold"`) || strings.Contains(body, "gift_rod_blank") || strings.Contains(body, "gift_reel_spool") || strings.Contains(body, "gift_reel_gear") {
		t.Fatalf("完整静态目录商品异常: %d %s", code, body)
	}

	// 内嵌前端可达
	code, _ = call("GET", "/", token, "")
	if code != 200 {
		t.Fatalf("内嵌前端应 200, 实得 %d", code)
	}

	// 未认证拒绝
	code, _ = call("GET", "/api/dashboard", "", "")
	if code != 401 {
		t.Fatalf("未认证应 401, 实得 %d", code)
	}

	// 仪表盘
	code, body = call("GET", "/api/dashboard", token, "")
	if code != 200 || !strings.Contains(body, "players") {
		t.Fatalf("仪表盘异常: %d %s", code, body)
	}

	// 关于页：README 由二进制内嵌并提供给管理后台
	code, body = call("GET", "/api/about", token, "")
	if code != 200 {
		t.Fatalf("关于页应 200, 实得 %d: %s", code, body)
	}
	var about struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(body), &about); err != nil || !strings.Contains(about.Content, "# rf4_server_simulator") {
		t.Fatalf("README 内容异常: %v %s", err, body)
	}

	// 调参
	if code, _ = call("POST", "/api/tunables", token, `{"key":"bite_probability","value":"0.5"}`); code != 200 {
		t.Fatalf("调参失败 %d", code)
	}
	v, ok, _ := db.TunableGet(context.Background(), "bite_probability")
	if !ok || v != "0.5" {
		t.Fatalf("参数未落库: %q", v)
	}
	_ = db.TunableSet(context.Background(), "bite_probability", "1.0", "test")

	// 玩家列表
	code, body = call("GET", "/api/players?q=1000001", token, "")
	if code != 200 || !strings.Contains(body, `"uid":1000001`) {
		t.Fatalf("玩家列表异常: %d %s", code, body)
	}

	// 改数据
	if code, _ = call("POST", "/api/players/update", token, `{"uid":1000001,"silver":77777}`); code != 200 {
		t.Fatal("改数据失败")
	}
	p, _ := db.GetProfile(context.Background(), uid)
	if p.Silver != 77777 {
		t.Fatalf("银币未生效: %d", p.Silver)
	}

	// 清空队列（前面调参步骤投递过 reload_tunables）
	for {
		_, ok, _ := db.QueuePop(context.Background())
		if !ok {
			break
		}
	}

	// 发物品（指令入共享队列）
	if code, _ = call("POST", "/api/players/grant", token, `{"uid":1000001,"item_id":"worm_01","qty":5}`); code != 200 {
		t.Fatal("发物品请求失败")
	}
	if raw, ok, _ := db.QueuePop(context.Background()); !ok || !strings.Contains(raw, "grant_item") {
		t.Fatalf("发物品指令未入队: %q", raw)
	}

	// 发邮件
	if code, _ = call("POST", "/api/mail/send", token, `{"uid":1000001,"text":"测试","items":[{"item_id":"worm_01","qty":3}]}`); code != 200 {
		t.Fatal("发邮件请求失败")
	}
	if raw, ok, _ := db.QueuePop(context.Background()); !ok || !strings.Contains(raw, "send_mail") {
		t.Fatalf("发邮件指令未入队: %q", raw)
	}

	// 封禁/解封
	if code, _ = call("POST", "/api/players/ban", token, `{"uid":1000001,"banned":true}`); code != 200 {
		t.Fatal("封禁失败")
	}
	banned, _ := db.IsBanned(context.Background(), uid)
	if !banned {
		t.Fatal("封禁未落库")
	}
	_ = db.SetBanned(context.Background(), uid, false)

	// 审计
	code, body = call("GET", "/api/audit?limit=10", token, "")
	if code != 200 || !strings.Contains(body, "grant_item") {
		t.Fatalf("审计缺失: %s", body)
	}

	select {
	case err := <-errCh:
		t.Fatalf("服务异常退出: %v", err)
	default:
	}
}

// 两个限时活动开关互斥：开启任一个会自动关闭另一个，普通参数与 0/1 校验不受影响。
func TestEventSwitchesMutuallyExclusive(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	_ = db.EnsureAdmin(context.Background(), "admin", "admin123")
	srv := NewServer(config.Admin{HTTPAddr: "127.0.0.1:0", JWTSecret: "test"}, db, logging.Discard(), WebFS())
	if _, err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	base := "http://" + srv.Addr()
	client := &http.Client{Timeout: 5 * time.Second}
	post := func(path, token, body string) int {
		r, _ := http.NewRequest("POST", base+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	r, _ := http.NewRequest("POST", base+"/api/login", strings.NewReader(`{"username":"admin","password":"admin123"}`))
	r.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	var lr struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&lr)
	_ = resp.Body.Close()
	token := lr.Token

	set := func(key, value string) {
		body := `{"key":"` + key + `","value":"` + value + `"}`
		if code := post("/api/tunables", token, body); code != 200 {
			t.Fatalf("设置 %s=%s 返回 %d", key, value, code)
		}
	}
	get := func(key string) string {
		v, _, _ := db.TunableGet(context.Background(), key)
		return v
	}

	set("event_anniversary_enabled", "1")
	set("event_newyear_enabled", "1")
	if got := get("event_newyear_enabled"); got != "1" {
		t.Fatalf("新年开关应为 1，实得 %q", got)
	}
	if got := get("event_anniversary_enabled"); got != "0" {
		t.Fatalf("开启新年后周年应自动关闭，实得 %q", got)
	}

	set("event_anniversary_enabled", "1")
	if got := get("event_anniversary_enabled"); got != "1" {
		t.Fatalf("周年开关应为 1，实得 %q", got)
	}
	if got := get("event_newyear_enabled"); got != "0" {
		t.Fatalf("开启周年后新年应自动关闭，实得 %q", got)
	}

	// 关闭自己不应影响另一个开关
	set("event_anniversary_enabled", "0")
	set("event_newyear_enabled", "1")
	set("event_anniversary_enabled", "0")
	if got := get("event_newyear_enabled"); got != "1" {
		t.Fatalf("关闭周年不应动新年开关，实得 %q", got)
	}

	// 普通玩法开关不受互斥影响，0/1 校验仍生效
	set("happy_hour_enabled", "1")
	if got := get("happy_hour_enabled"); got != "1" {
		t.Fatalf("普通开关应为 1，实得 %q", got)
	}
	if code := post("/api/tunables", token, `{"key":"event_newyear_enabled","value":"7"}`); code != 400 {
		t.Fatalf("活动开关非法值应 400，实得 %d", code)
	}
	if code := post("/api/tunables", token, `{"key":"event_branches","value":"8,21"}`); code != 400 {
		t.Fatalf("非法分支配置应 400，实得 %d", code)
	}
	if code := post("/api/tunables", token, `{"key":"event_branches","value":"17,18"}`); code != 200 {
		t.Fatalf("合法分支配置应 200，实得 %d", code)
	}
}

// 活动分支配置只接受 9~20 的逗号分隔整数，空串表示不下发。
func TestValidateEventBranches(t *testing.T) {
	for _, v := range []string{"", "  ", "9", "17,18", " 19 , 20 ",
		"9,10,11,12,13,14,15,16,17,18,19,20"} {
		if err := validateEventBranches(v); err != nil {
			t.Fatalf("%q 应合法: %v", v, err)
		}
	}
	for _, v := range []string{"8", "21", "abc", "17,abc", "17,,18", "9;10", "-1"} {
		if err := validateEventBranches(v); err == nil {
			t.Fatalf("%q 应被拒绝", v)
		}
	}
}

func TestValidateEventStageGifts(t *testing.T) {
	for _, value := range []string{
		"",
		`{"13":{"1":[{"item":"fcb_coffee_can2","qty":1}]}}`,
		`{"20":{"10":[{"item":"ab_tea_can2","qty":99}]}}`,
	} {
		if err := validateEventStageGifts(value); err != nil {
			t.Fatalf("%q 应合法: %v", value, err)
		}
	}
	for _, value := range []string{
		"not-json",
		`{"8":{"1":[{"item":"fcb_coffee_can2","qty":1}]}}`,
		`{"13":{"11":[{"item":"fcb_coffee_can2","qty":1}]}}`,
		`{"13":{"1":[]}}`,
		`{"13":{"1":[{"item":"","qty":1}]}}`,
		`{"13":{"1":[{"item":"fcb_coffee_can2","qty":0}]}}`,
		`{"13":{"1":[{"item":"fcb_coffee_can2","qty":100}]}}`,
	} {
		if err := validateEventStageGifts(value); err == nil {
			t.Fatalf("%q 应被拒绝", value)
		}
	}
}

// 发鱼模式三选一：POST 原子写三个开关，GET 按优先级推导；非法模式 400，0/1 校验覆盖新键。
func TestSpawnModeEndpoint(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	_ = db.EnsureAdmin(context.Background(), "admin", "admin123")
	srv := NewServer(config.Admin{HTTPAddr: "127.0.0.1:0", JWTSecret: "test"}, db, logging.Discard(), WebFS())
	if _, err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	base := "http://" + srv.Addr()
	client := &http.Client{Timeout: 5 * time.Second}
	call := func(method, path, token, body string) (int, string) {
		r, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return resp.StatusCode, buf.String()
	}

	code, body := call("POST", "/api/login", "", `{"username":"admin","password":"admin123"}`)
	if code != 200 {
		t.Fatalf("登录应 200: %d %s", code, body)
	}
	var lr struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(body), &lr)
	token := lr.Token

	get := func(key string) string {
		v, _, _ := db.TunableGet(context.Background(), key)
		return v
	}
	mode := func() string {
		code, body := call("GET", "/api/fish/spawn_mode", token, "")
		if code != 200 {
			t.Fatalf("读取发鱼模式应 200: %d %s", code, body)
		}
		var out struct {
			Mode string `json:"mode"`
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatalf("解析模式响应: %v %s", err, body)
		}
		return out.Mode
	}

	if got := mode(); got != "legacy" {
		t.Fatalf("全新库三个开关全空应报 legacy，实得 %q", got)
	}
	if code, _ := call("POST", "/api/fish/spawn_mode", token, `{"mode":"nope"}`); code != 400 {
		t.Fatalf("非法模式应 400，实得 %d", code)
	}
	if code, _ := call("POST", "/api/fish/spawn_mode", token, `{"mode":"two_stage"}`); code != 200 {
		t.Fatalf("切换两档制应 200")
	}
	if got := get("grade_ratio_enabled"); got != "0" {
		t.Fatalf("切两档制后档位比例应为 0，实得 %q", got)
	}
	if got := get("fish_hunt_enabled"); got != "0" {
		t.Fatalf("切两档制后鱼猎应为 0，实得 %q", got)
	}
	if got := get("two_stage_hunt_enabled"); got != "1" {
		t.Fatalf("切两档制后两档开关应为 1，实得 %q", got)
	}
	if got := mode(); got != "two_stage" {
		t.Fatalf("GET 应报 two_stage，实得 %q", got)
	}
	if code, _ := call("POST", "/api/fish/spawn_mode", token, `{"mode":"grade_ratio"}`); code != 200 {
		t.Fatalf("切档位比例应 200")
	}
	if got := get("grade_ratio_enabled"); got != "1" {
		t.Fatalf("切档位比例后应为 1，实得 %q", got)
	}
	if got := get("two_stage_hunt_enabled"); got != "0" {
		t.Fatalf("切档位比例后两档开关应为 0，实得 %q", got)
	}
	if got := mode(); got != "grade_ratio" {
		t.Fatalf("GET 应报 grade_ratio，实得 %q", got)
	}

	// 0/1 校验覆盖两个新开关键；两档制权重校验非负数字。
	if code, _ := call("POST", "/api/tunables", token, `{"key":"fish_hunt_enabled","value":"7"}`); code != 400 {
		t.Fatalf("fish_hunt_enabled 非法值应 400，实得 %d", code)
	}
	if code, _ := call("POST", "/api/tunables", token, `{"key":"two_stage_hunt_enabled","value":"x"}`); code != 400 {
		t.Fatalf("two_stage_hunt_enabled 非法值应 400，实得 %d", code)
	}
	if code, _ := call("POST", "/api/tunables", token, `{"key":"two_stage_daily_below","value":"-1"}`); code != 400 {
		t.Fatalf("两档制负权重应 400，实得 %d", code)
	}
	if code, _ := call("POST", "/api/tunables", token, `{"key":"two_stage_baited_trophy","value":"6"}`); code != 200 {
		t.Fatalf("两档制合法权重应 200，实得 %d", code)
	}
}
