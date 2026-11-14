// Package buildinfo 由 -ldflags 注入版本信息。
package buildinfo

import "strings"

var (
	Version   = "dev"
	GitCommit = "unknown"
	BuildTime = "unknown"
	// SourceDir 是产出该二进制的检出目录（ldflags 注入）。
	// 本仓多个 worktree 共用同一个 bin/，只靠版本号无法区分
	// 「这份 exe 是哪个检出构建的」。
	SourceDir = "unknown"
	// BuildTs 为 UTC 构建时间（yyyyMMddHHmmss），由 ldflags 注入，
	// 是版本更新检测用的可比键；"0" 表示未知（dev 构建）。
	BuildTs = "0"
)

// MetricLabels 返回 rf4_build_info 会带的四个标签值。
// launcher 用它比对「磁盘上的二进制」与「内存里跑的二进制」。
func MetricLabels() (version, buildts, commit, source string) {
	return Version, BuildTs, GitCommit, SourceDir
}

// ParseMetric 从 /metrics 文本里取 rf4_build_info 的标签。
//
// 进程自报的身份比读磁盘文件可靠：bin/ 被别的构建覆盖后，磁盘上是新文件，
// 内存里跑的仍是旧代码，只有进程自己吐出的标签能回答「你现在跑的是谁」。
//
// 返回 ok=false 表示文本里没有这条指标（老构建或端口不通）。
func ParseMetric(text string) (version, buildts, commit, source string, ok bool) {
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "rf4_build_info{") {
			continue
		}
		body := line[strings.Index(line, "{")+1:]
		if end := strings.LastIndex(body, "}"); end >= 0 {
			body = body[:end]
		}
		labels := map[string]string{}
		for _, kv := range strings.Split(body, ",") {
			k, v, found := strings.Cut(kv, "=")
			if !found {
				continue
			}
			labels[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
		}
		return labels["version"], labels["buildts"], labels["commit"], labels["source"], true
	}
	return "", "", "", "", false
}
