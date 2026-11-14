package admin

// 抛投状态惩罚强度的区间校验：越界值会被 gameserver 静默钳到 [0,1]，
// 后台必须在写入前拦住，否则界面显示与实际手感对不上。
// 见 specs/changes/cast-player-state-penalty-scale。

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
)

func TestCastStatePenaltyTunableRangeValidation(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.EnsureAdmin(ctx, "admin", "cast-state-range-test"); err != nil {
		t.Fatal(err)
	}
	s := NewServer(config.Admin{JWTSecret: "cast-state-range-test"}, db, logging.Discard(), WebFS())
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
	w := call("POST", "/api/login", "", `{"username":"admin","password":"cast-state-range-test"}`)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &login) != nil || login.Token == "" {
		t.Fatalf("login=%s", w.Body)
	}
	post := func(value string) int {
		return call("POST", "/api/tunables", login.Token,
			`{"key":"cast_player_state_penalty_scale","value":"`+value+`"}`).Code
	}

	for _, value := range []string{"-0.1", "1.01", "5", "NaN", "Inf", "abc", ""} {
		if got := post(value); got != 400 {
			t.Fatalf("惩罚强度 %q 应被拒绝，实得 %d", value, got)
		}
	}
	for _, value := range []string{"0", "0.35", "1"} {
		if got := post(value); got != 200 {
			t.Fatalf("惩罚强度 %q 应被接受，实得 %d", value, got)
		}
		if got, ok, err := db.TunableGet(ctx, "cast_player_state_penalty_scale"); err != nil || !ok || got != value {
			t.Fatalf("保存 %q 读到 %q err=%v", value, got, err)
		}
	}
}
