package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"rf4server/internal/buildinfo"
)

// TestBuildInfoMetricCarriesVersion 版本号必须能从 /metrics 读出来。
//
// 只有 process 标签时，进程照常 running，而 /metrics 无法回答「现在跑的
// 到底是哪个构建」。修复后任何进程都能用一行 curl 自证版本。
func TestBuildInfoMetricCarriesVersion(t *testing.T) {
	oldVersion, oldTs, oldCommit, oldSource := buildinfo.Version, buildinfo.BuildTs, buildinfo.GitCommit, buildinfo.SourceDir
	buildinfo.Version, buildinfo.BuildTs, buildinfo.GitCommit, buildinfo.SourceDir = "20260101090000-test", "20260101010000", "abc1234", "/repo/.agents/worktrees/feature-x"
	t.Cleanup(func() {
		buildinfo.Version, buildinfo.BuildTs, buildinfo.GitCommit, buildinfo.SourceDir = oldVersion, oldTs, oldCommit, oldSource
	})

	s := NewServer(":0", "gameserver")
	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))

	if rec.Code != 200 {
		t.Fatalf("/metrics 状态码 = %d，期望 200", rec.Code)
	}
	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatalf("读取 /metrics 失败: %v", err)
	}
	text := string(body)
	for _, want := range []string{
		`process="gameserver"`,
		`version="20260101090000-test"`,
		`buildts="20260101010000"`,
		`commit="abc1234"`,
		`source="/repo/.agents/worktrees/feature-x"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("/metrics 缺少 %s，实际内容：\n%s", want, firstLines(text, 20))
		}
	}
	if strings.Contains(text, `commit="unknown"`) || strings.Contains(text, `source="unknown"`) {
		t.Fatalf("注入 ldflags 后 /metrics 仍出现 unknown 占位符：\n%s", firstLines(text, 20))
	}
}

// TestBuildInfoMetricInheritsLDFlags 未注入时也要有值，不能出现空标签。
func TestBuildInfoMetricInheritsLDFlags(t *testing.T) {
	s := NewServer(":0", "gateway")
	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	text := rec.Body.String()
	// buildinfo 的默认值必须是可读的占位符，不能是空串——空标签会让
	// 「未注入版本」和「版本为空」两种情况无法区分。
	for _, want := range []string{`version="`, `buildts="`, `commit="`} {
		if !strings.Contains(text, want) {
			t.Fatalf("/metrics 缺少标签 %s，实际内容：\n%s", want, firstLines(text, 20))
		}
	}
	if strings.Contains(text, `version=""`) {
		t.Fatal("version 标签为空串：未注入 -ldflags 时也应回落可读占位符")
	}
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
