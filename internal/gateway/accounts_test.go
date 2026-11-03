package gateway

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"rf4server/internal/config"
	"rf4server/internal/metrics"
)

// newAccountTestServer 构建账号登录测试环境（fallback 关闭，账号预置）。
func newAccountTestServer(t *testing.T) *LoginServer {
	t.Helper()
	st := NewMemStore(1000001)
	if err := st.AccountCreate(context.Background(), "alice", "Passw0rd", 1000001); err != nil {
		t.Fatalf("预置账号: %v", err)
	}
	ls := &LoginServer{
		cfg:   config.Gateway{TokenTTL: 1},
		store: st,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	registerTestLoginMetrics()
	return ls
}

// registerTestLoginMetrics 初始化登录指标（直接构造 LoginServer 的测试需要）。
func registerTestLoginMetrics() {
	reg := prometheus.NewRegistry()
	loginRequests = metrics.NewCounter(reg, "gateway_login_requests_total", "登录请求数")
	loginIssued = metrics.NewCounter(reg, "gateway_tokens_issued_total", "签发 token 数")
	loginErrors = metrics.NewCounter(reg, "gateway_login_errors_total", "登录处理错误数")
}

func sha1Hex(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func doLogin(ls *LoginServer, query string) (int, string) {
	req := httptest.NewRequest("GET", "https://api.rf4game.ru/login.php?"+query, nil)
	req.Header.Set("User-Agent", "RF4")
	w := httptest.NewRecorder()
	ls.handleLogin(w, req)
	b, _ := io.ReadAll(w.Result().Body)
	return w.Code, string(b)
}

func TestAccountLoginSuccess(t *testing.T) {
	ls := newAccountTestServer(t)
	code, body := doLogin(ls, "login=alice&password="+sha1Hex("Passw0rd")+"&version=4.0.25034")
	if code != 200 || !strings.Contains(body, `success="true"`) || !strings.Contains(body, "1000001") {
		t.Fatalf("正确凭据应成功并绑定 1000001: code=%d body=%s", code, body)
	}
}

func TestAccountLoginWrongPassword(t *testing.T) {
	ls := newAccountTestServer(t)
	code, body := doLogin(ls, "login=alice&password="+sha1Hex("WrongPass1"))
	if code != 200 || strings.Contains(body, `success="true"`) {
		t.Fatalf("密码错误必须拒绝: code=%d body=%s", code, body)
	}
}

func TestAccountLoginUnregistered(t *testing.T) {
	ls := newAccountTestServer(t)
	code, body := doLogin(ls, "login=bob&password="+sha1Hex("Passw0rd"))
	if code != 200 || strings.Contains(body, `success="true"`) {
		t.Fatalf("未注册用户名必须拒绝（不自动建号）: code=%d body=%s", code, body)
	}
}

func TestAccountLoginBadHash(t *testing.T) {
	ls := newAccountTestServer(t)
	code, body := doLogin(ls, "login=alice&password=deadbeef")
	if code != 200 || strings.Contains(body, `success="true"`) {
		t.Fatalf("非 40 位 hex 哈希必须拒绝: code=%d body=%s", code, body)
	}
}

func TestAccountLoginNoCredentialsFallbackOff(t *testing.T) {
	ls := newAccountTestServer(t)
	code, body := doLogin(ls, "")
	if code != 200 || strings.Contains(body, `success="true"`) {
		t.Fatalf("fallback 关闭时无凭据请求必须拒绝: code=%d body=%s", code, body)
	}
}

func TestAccountLoginNoCredentialsFallbackOn(t *testing.T) {
	st := NewMemStore(1000001)
	ls := &LoginServer{
		cfg:   config.Gateway{TokenTTL: 1, AccountsFallbackIP: true},
		store: st,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	registerTestLoginMetrics()
	code, body := doLogin(ls, "")
	if code != 200 || !strings.Contains(body, `success="true"`) {
		t.Fatalf("fallback 开启时无凭据回退 IP 分配: code=%d body=%s", code, body)
	}
}
