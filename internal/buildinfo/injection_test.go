package buildinfo

import (
	"os"
	"strings"
	"testing"
)

// buildEntrypoints 是仓库里所有会产出二进制的构建入口。
// 四个身份标签的 -ldflags 注入必须覆盖每一个，否则那份 exe 起来后对应的
// /metrics 标签就是 unknown，而 spec/docs 却写着「自报完整身份」。
var buildEntrypoints = []string{
	"../../Makefile",
	"../../build.ps1",
	"../../tools/build.ps1",
	"../../.github/scripts/build-preview.sh",
	"../../.github/scripts/build-release.sh",
}

// identityFields 是 rf4_build_info 对外承诺的四个身份标签，缺一不可。
var identityFields = []string{"Version", "BuildTs", "GitCommit", "SourceDir"}

// TestBuildEntrypointsInjectFullIdentity 逐一读五个构建脚本，断言每个都含四个
// internal/buildinfo.<字段> 的 -X 注入。删掉任一入口的任一注入即变红。
// 测试 cwd 是 internal/buildinfo，../../ 即仓库根。
func TestBuildEntrypointsInjectFullIdentity(t *testing.T) {
	for _, rel := range buildEntrypoints {
		data, err := os.ReadFile(rel)
		if err != nil {
			t.Fatalf("读不到构建脚本 %s: %v（测试 cwd 应为 internal/buildinfo）", rel, err)
		}
		text := strings.ReplaceAll(string(data), "\r\n", "\n")
		for _, field := range identityFields {
			want := "-X 'rf4server/internal/buildinfo." + field + "="
			if !strings.Contains(text, want) {
				t.Errorf("%s 缺少 %s 的 ldflags 注入（应为 %q）；构建出的二进制会把该标签报成 unknown", rel, field, want)
			}
		}
	}
}
