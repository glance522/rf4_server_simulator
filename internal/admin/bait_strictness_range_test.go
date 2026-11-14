package admin

// 严格吃饵档位与杂口强度的入口区间校验：越界值会被 gameserver 静默钳位，
// 后台必须在写入前拦住，否则界面显示与实际鱼口对不上。
// 见 specs/changes/bait-strict-preference。

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
)

func TestBaitStrictnessTunableRangeValidation(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.EnsureAdmin(ctx, "admin", "bait-strictness-range-test"); err != nil {
		t.Fatal(err)
	}
	s := NewServer(config.Admin{JWTSecret: "bait-strictness-range-test"}, db, logging.Discard(), WebFS())
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
	w := call("POST", "/api/login", "", `{"username":"admin","password":"bait-strictness-range-test"}`)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &login) != nil || login.Token == "" {
		t.Fatalf("login=%s", w.Body)
	}
	post := func(key, value string) int {
		return call("POST", "/api/tunables", login.Token,
			`{"key":"`+key+`","value":"`+value+`"}`).Code
	}

	for _, tc := range []struct {
		key      string
		rejected []string
		accepted []string
	}{
		{"fish_bait_strictness", []string{"-1", "0.5", "3", "NaN", "Inf", "abc", ""}, []string{"0", "1", "2"}},
		{"fish_bait_alt_bite", []string{"-0.1", "0.51", "5", "NaN", "Inf", "abc", ""}, []string{"0", "0.05", "0.5"}},
	} {
		for _, value := range tc.rejected {
			if got := post(tc.key, value); got != 400 {
				t.Fatalf("%s %q 应被拒绝，实得 %d", tc.key, value, got)
			}
		}
		for _, value := range tc.accepted {
			if got := post(tc.key, value); got != 200 {
				t.Fatalf("%s %q 应被接受，实得 %d", tc.key, value, got)
			}
			if got, ok, err := db.TunableGet(ctx, tc.key); err != nil || !ok || got != value {
				t.Fatalf("保存 %s=%q 读到 %q err=%v", tc.key, value, got, err)
			}
		}
	}
}
