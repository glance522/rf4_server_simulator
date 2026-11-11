package admin

// 摩擦片磨损参数的区间校验：下限低于 float64 精度门槛的值必须被拒绝，
// 否则参数会静默失效（看着生效、实际一分不磨）。见 specs/changes/reel-wear-tunables。

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
)

func TestReelWearTunableRangeValidation(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.EnsureAdmin(ctx, "admin", "reel-range-test"); err != nil {
		t.Fatal(err)
	}
	s := NewServer(config.Admin{JWTSecret: "reel-range-test"}, db, logging.Discard(), WebFS())
	call := func(method, path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, r)
		return w
	}

	var login struct {
		Token string `json:"token"`
	}
	w := call("POST", "/api/login", "", `{"username":"admin","password":"reel-range-test"}`)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &login) != nil || login.Token == "" {
		t.Fatalf("login=%s", w.Body)
	}

	// 小于精度门槛的值必须被拒。1e-21 是第一轮实测能写进去、但实际不磨的值；
	// 1e-13 仍低于 1e-12 下限，一并挡掉。
	for _, value := range []string{"0", "-1", "0.000000000000000000001", "1e-21", "1e-13", "2", "NaN", ""} {
		got := call("POST", "/api/tunables", login.Token, `{"key":"reel_slip_wear_low","value":"`+value+`"}`).Code
		if got != 400 {
			t.Fatalf("磨损率 %q 应被拒绝，实得 %d", value, got)
		}
	}

	// 区间内的值（含默认值和下限本身）必须接受。
	for _, value := range []string{"0.000001", "1e-12", "1"} {
		w = call("POST", "/api/tunables", login.Token, `{"key":"reel_slip_wear_low","value":"`+value+`"}`)
		if w.Code != 200 {
			t.Fatalf("磨损率 %q 应被接受，实得 %d %s", value, w.Code, w.Body)
		}
		if got, ok, err := db.TunableGet(ctx, "reel_slip_wear_low"); err != nil || !ok || got != value {
			t.Fatalf("保存 %q 读到 %q err=%v", value, got, err)
		}
	}

	// 其它磨损参数的边界：校准秒数下限 0.01、上限 3600。
	for _, value := range []string{"0.001", "3601"} {
		if got := call("POST", "/api/tunables", login.Token, `{"key":"reel_slip_calibration_seconds","value":"`+value+`"}`).Code; got != 400 {
			t.Fatalf("校准秒数 %q 应被拒绝，实得 %d", value, got)
		}
	}
	if got := call("POST", "/api/tunables", login.Token, `{"key":"reel_slip_calibration_seconds","value":"0.01"}`).Code; got != 200 {
		t.Fatalf("校准秒数下限 0.01 应被接受，实得 %d", got)
	}

	// 受力磨损速率上限 0.01%/s（官服量级 0.0004，0.01 已快 25 倍）：超出应被拒；
	// 烧穿曲线键已随锁轮曲线删除，不再受区间约束。
	for _, value := range []string{"0.02", "-0.1"} {
		if got := call("POST", "/api/tunables", login.Token, `{"key":"wear_stress_gear_per_s","value":"`+value+`"}`).Code; got != 400 {
			t.Fatalf("受力速率 %q 应被拒绝，实得 %d", value, got)
		}
	}
	if got := call("POST", "/api/tunables", login.Token, `{"key":"wear_stress_gear_per_s","value":"0.0004"}`).Code; got != 200 {
		t.Fatalf("受力速率 0.0004 应被接受，实得 %d", got)
	}
	// 打滑门槛倍率：下限 1，上限 10。
	if got := call("POST", "/api/tunables", login.Token, `{"key":"reel_slip_deadband_ratio","value":"0.9"}`).Code; got != 400 {
		t.Fatalf("打滑门槛 0.9 应被拒绝，实得 %d", got)
	}
	if got := call("POST", "/api/tunables", login.Token, `{"key":"reel_slip_deadband_ratio","value":"1.05"}`).Code; got != 200 {
		t.Fatalf("打滑门槛 1.05 应被接受，实得 %d", got)
	}

	// 未受区间约束的键不受影响（回归保护：别把校验扩散到别的参数）。
	if got := call("POST", "/api/tunables", login.Token, `{"key":"xp_gain","value":"0.0001"}`).Code; got != 200 {
		t.Fatalf("xp_gain 不在磨损区间表内，应正常保存，实得 %d", got)
	}
}
