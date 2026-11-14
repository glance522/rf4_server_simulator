package metrics

import (
	"net/http/httptest"
	"testing"

	"rf4server/internal/buildinfo"
)

// TestBuildInfoMetricIdentityNotUnknownAfterInjection 模拟构建脚本用 -ldflags
// 注入四个身份标签后的场景：进程 /metrics 自报的 commit / source 必须是真实值，
// 不能回落到 unknown 占位。构建脚本漏注入时，跑起来的进程只会报 unknown——
// 这条用例与 internal/buildinfo 的脚本注入守护把「脚本」与「指标」两端都锁住。
func TestBuildInfoMetricIdentityNotUnknownAfterInjection(t *testing.T) {
	oldVersion, oldTs, oldCommit, oldSource := buildinfo.Version, buildinfo.BuildTs, buildinfo.GitCommit, buildinfo.SourceDir
	buildinfo.Version = "20260101090000-test"
	buildinfo.BuildTs = "20260101010000"
	buildinfo.GitCommit = "abc1234"
	buildinfo.SourceDir = "/repo/.agents/worktrees/feature-x"
	t.Cleanup(func() {
		buildinfo.Version, buildinfo.BuildTs, buildinfo.GitCommit, buildinfo.SourceDir = oldVersion, oldTs, oldCommit, oldSource
	})

	s := NewServer(":0", "gameserver")
	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	version, buildts, commit, source, ok := buildinfo.ParseMetric(body)
	if !ok {
		t.Fatalf("/metrics 里没有 rf4_build_info：\n%s", firstLines(body, 20))
	}
	if version == "" || version == "unknown" || buildts == "" || buildts == "0" {
		t.Fatalf("注入后 version/buildts 仍是占位符：version=%q buildts=%q", version, buildts)
	}
	if commit == "" || commit == "unknown" {
		t.Fatalf("注入后 commit 仍是 %q，期望非 unknown 的提交短码", commit)
	}
	if source == "" || source == "unknown" {
		t.Fatalf("注入后 source 仍是 %q，期望真实检出目录", source)
	}
}
