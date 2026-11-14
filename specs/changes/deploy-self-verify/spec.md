# 部署自证：运行中的进程必须能回答「你跑的是哪个构建」

Status: done

## Problem Statement

带修复的 `bin/gameserver.exe` 部署上线并实机验证通过；不久后另一次 `build.ps1` 构建（在另一个检出目录执行）把同一个 `bin/gameserver.exe` 覆盖成了不含该修复的版本。

从那一刻起，运维侧看到的**一切**都是正常的：

- `/api/status` 说 gameserver `running`，`restarts=0`；
- `/healthz` 返回 `ok`；
- 面板、托盘图标、日志里的启动行都正常；
- 玩家侧的现象却是「修复明明部署了却没生效」。

根因不是构建脚本有问题，而是**运行中的进程无法自证身份**：

- `/healthz` 只回 `ok\n`，任何构建都是 200，`supervisor.waitHealthy` 也只看 200；
- `rf4_build_info` 只带 `process` 标签，不带版本，`/metrics` 回答不了「现在跑的是谁」；
- `/api/status` 的 `current_version` 是 **launcher 自己**的版本，排查时极易被误读成子进程版本；
- 磁盘上的 exe 被替换后，没有任何地方对比过「磁盘文件」与「进程内存里那份」。

这份缺陷的代价是：一个已经修好、已经验证过的功能被误判成「修复无效」，排查方向从服务端一路歪到客户端渲染。

## Solution

运维在任意时刻都能用一条命令问出「现在跑的是哪个构建、从哪个检出目录构建的、磁盘上那份是不是换了」，并且面板和日志会主动告警「磁盘已替换但进程仍在跑旧代码」。

具体地：

- 子进程通过 `/metrics` 的 `rf4_build_info` 自报 `version` / `buildts` / `commit` / `source`；
- `/api/status` 的 `procs[]` 带上 `running_build`（子进程自报）与 `exe_changed`（磁盘文件已被替换）；
- launcher 每 10 秒刷新一次，发现 `exe_changed` 就写一条 WARN 日志；
- 面板每个进程卡片直接显示自报版本与来源目录，`exe_changed` 时显示醒目提示条；
- 五个构建入口（`build.ps1`、`tools/build.ps1`、`Makefile`、`.github/scripts/build-preview.sh`、`build-release.sh`）都把 `Version` / `BuildTs` / `GitCommit` / `SourceDir` 四个身份标签注入 ldflags——本仓多个 worktree 共用同一个 `bin/`，只有版本时间戳无法回答「这份 exe 是哪个检出构建的、对应哪次提交」。

## User Stories

1. 作为运维，我希望部署后能用 `curl 127.0.0.1:9502/metrics | grep rf4_build_info` 读到版本与检出目录，以便确认「跑的就是我刚部署的那份」。
2. 作为运维，我希望 `bin/` 被别的构建覆盖后面板立刻显示提示、launcher 日志出现 WARN，以便在玩家反馈前就发现。
3. 作为排查者，我希望 `/api/status` 里 launcher 自身版本与子进程自报版本**分列**，以便不再把 `current_version` 误读成子进程版本。
4. 边界：进程未启动、已停止、失败或退避中，以及观测端口不通时，`running_build` 缺省且面板显示「未知」，不能显示成「版本正常」。`running_build` 只在 `StateRunning` 时对外暴露；停止/退出时立即作废缓存身份，`RefreshBuilds` 也不再对非运行态进程访问观测端口（避免端口被复用后张冠李戴）。
5. 边界：改了 `bin/` 但没重启，`exe_changed=true` 且提示「重启后才会切到新构建」；重启后自动消除。

## Implementation Decisions

- `internal/buildinfo`：新增 `SourceDir` 变量（ldflags 注入）与 `MetricLabels()` / `ParseMetric()`。`ParseMetric` 返回 `ok=false` 表示「读不到」，与「版本是 dev」严格区分。
- `internal/metrics`：`rf4_build_info` 的 `ConstLabels` 扩到 `process/version/buildts/commit/source`。
- `internal/supervisor`：`ProcInfo` 增加 `running_build` 与 `exe_changed`；`proc` 记录启动时刻的 exe 大小与 mtime 作为指纹；`RefreshBuilds()` 并发拉取各进程 `/metrics`，缓存 10 秒（面板每 2 秒刷新，不能每刷都打端口）。`Status()` 与 `readBuild()` 都带 `StateRunning` 门禁：前者只在运行态暴露身份，后者对非运行态直接返回、且在 fetch 返回后二次确认状态再写回（fetch 最长 2 秒，期间进程可能已退出）；`supervise()` 的四处状态转换与 `StopOne()` 在 `s.mu` 内调 `clearBuild()` 作废 `build`/`buildRead`，锁顺序恒为 `s.mu → p.buildMu`，与 `Status()` 一致。
- `cmd/launcher`：`handleStatus` 增加 `launcher_build` 字段；新增 `trackBuildIdentity()` 后台循环（10 秒），发现 `exe_changed` 时 WARN 一次，恢复后允许再次告警。
- `cmd/launcher/panel.html`：进程卡片渲染自报版本（含来源目录末两段）与 `exe_changed` 提示条。
- 构建入口：五个入口统一注入四个 `-X`：`Version=<语义化版本>`、`BuildTs=<UTC 时间戳>`、`GitCommit=<提交短 SHA>`、`SourceDir=<检出目录，正斜杠>`。CI 两脚本用入参 `COMMIT_SHA` 派生的 `SHORT_SHA` 与检出根 `ROOT`；本地三脚本用 `git rev-parse --short HEAD` 与各自检出根，git 不可用时回落 `unknown`（可读占位符，不伪装成已注入）。
- 数据与迁移：不改表、不改协议、不迁移玩家库。
- 兼容：`/metrics` 多出的标签是新增 label，不影响既有抓取；老构建没有这些标签时 `ParseMetric` 返回 `ok=false`，上层显示「未知」——降级行为明确。
- 回滚：五处构建脚本各自独立，去掉任一 `-X` 注入即回到该标签报 `unknown` 的行为；`TestBuildEntrypointsInjectFullIdentity` 会把漏注入的入口直接判红。

## Testing Decisions

- 单元（`internal/metrics`）：`TestBuildInfoMetricCarriesVersion` 断言 `/metrics` 含 `version` / `buildts` / `commit` / `source` 四个标签且注入后不出现 `unknown`；`TestBuildInfoMetricIdentityNotUnknownAfterInjection` 用 `ParseMetric` 断言注入后四个标签都不是默认占位符；`TestBuildInfoMetricInheritsLDFlags` 断言默认值非空。
- 构建脚本守护（`internal/buildinfo/injection_test.go`）：`TestBuildEntrypointsInjectFullIdentity` 逐一读五个构建脚本，断言每个都含四个 `internal/buildinfo.<字段>` 的 `-X` 注入——删掉任一入口的任一注入即变红，专门防「spec 写完成、脚本没注入」。
- 单元（`internal/supervisor`，`build_identity_test.go`）：`httptest` 假观测端口验证 `fetchBuild` 正常解析 / 无指标返回 nil / 非 200 返回 nil / 端口不通返回 nil；`RefreshBuilds` 按进程各取各的身份（防止张冠李戴）；`exeChanged` 在「启动后未变」「被覆盖」「重启后恢复」「进程已停止」四种状态下正确；`TestStatusHidesRunningBuildUnlessRunning` 覆盖 stopped/starting/backoff/failed 四种非运行态下 `RunningBuild=nil`；`TestReadBuildSkipsNonRunningProcess` 断言非运行态 0 次端口访问；`TestStopOneClearsRunningBuild` 断言停止指令立即清空身份缓存。既有 `RefreshBuilds` 用例显式把进程置为 `StateRunning`，否则门禁会跳过读取、测不到原意图。
- 期望值来源：`/metrics` 文本是手写字面量，`exeChanged` 用真实临时文件 + 改写内容，不是"用同样代码再算一遍"。
- 回滚后变红：删掉任一构建脚本的 `GitCommit` / `SourceDir` / `BuildTs` 注入 → `TestBuildEntrypointsInjectFullIdentity` 红；去掉 `/metrics` 的版本或 `commit` 标签 → `TestBuildInfoMetricCarriesVersion` / `TestBuildInfoMetricIdentityNotUnknownAfterInjection` 红；去掉 `exeChanged` 指纹 → `TestExeChangedDetectsReplacedBinary` 红；`fetchBuild` 把空 version 当成功 → `TestRefreshBuildsLeavesUnreachableUnknown` 红；去掉 `Status()` 的 `StateRunning` 门禁 → `TestStatusHidesRunningBuildUnlessRunning` 红；去掉 `readBuild()` 的入口门禁 → `TestReadBuildSkipsNonRunningProcess` 红；去掉 `StopOne()` 的 `clearBuild()` → `TestStopOneClearsRunningBuild` 红。
- 行为（实机一次）：用任一构建入口重建二进制 → 部署 → `curl 127.0.0.1:9502/metrics` 断言 `rf4_build_info` 四个标签 `version`/`buildts`/`commit`/`source` 全为非 `unknown` 的真实值（CI 制品的 `commit` 应等于当次 `$GITHUB_SHA` 短码）→ `curl /api/status` 断言 `procs[].running_build.version` 一致 → 用另一个构建覆盖 `bin/gameserver.exe` → 断言 10 秒内 `exe_changed=true` 且 launcher 日志出现 WARN → 重启 → 断言恢复。

## Out of Scope

- 不做二进制签名/校验和比对（能覆盖 `bin/` 的本机进程同样能改校验文件，收益低）。
- 不改 `/healthz` 语义：健康检查回答「能不能服务」，不回答「是哪个构建」；两者混用会让探针在版本不符时把进程判死。
- 不做构建产物集中管理或锁（多个 worktree 共用 `bin/` 是团队既定工作方式，本轮只解决「察觉」）。
- 不追溯已有事故：本轮只保证之后可察觉。

## Further Notes

[事实] 覆盖来源已核实：`internal/updatecheck/updatecheck.go` 只拉 manifest 并缓存到 state 文件；`cmd/launcher/` 的写盘点只有 hosts 备份、游戏配置、`launcher_error.log`；全仓无 `archive/zip`、无解压、无写 exe 的代码。因此 `bin/` 只可能被构建脚本覆盖。

[事实] 事故现场：被覆盖前后的两个二进制大小不同（前者约 32 MB，后者约 23 MB），且覆盖后的构建里 `coupon_chuchelo` 字符串出现 0 次——扣券代码根本不在包里。

[事实] 覆盖后 `/metrics` 输出 `rf4_build_info{process="gameserver"} 1`——只有 process 标签，无法区分构建。

[事实] `ProcInfo` 的 `exe_changed` 判据是「启动时刻的 size + mtime」与当前文件比对；两个不同构建恰好同尺寸同 mtime 的窗口极小，且本仓构建时间戳精确到秒，实际不会冲突。

[推断] 面板每 2 秒刷新、`RefreshBuilds` 缓存 10 秒，意味着 `exe_changed` 的最坏察觉延迟约 10 秒；对人工排查足够，不追求实时。

[事实] 复审发现首轮只把 `SourceDir` 注入了三个本地入口：`GitCommit` 全链路未注入、CI 两脚本连 `SourceDir` 都没注入、`tools/build.ps1` 还漏了 `BuildTs`，而 spec/docs 已按「自报完整身份」口径写。本轮把五个入口的四个标签拉齐，并补 `TestBuildEntrypointsInjectFullIdentity` 静态守护，避免文档与实现再次漂移。

## 实机验收

按下面四步在任意部署环境复现，每步都有可判读的输出：

1. **进程自报身份**：`curl -s 127.0.0.1:9502/metrics | grep '^rf4_build_info'` 读到
   `process` / `version` / `buildts` / `commit` / `source` 五个标签，且 `source` 是
   期望的检出目录。gateway 同理由 9501 读到自己的 `process="gateway"`。
2. **`/api/status` 分列 launcher 与子进程**：`launcher_build` 给 launcher 自身，
   `procs[].running_build` 给子进程，两者不混读；进程正常时 `exe_changed=false`。
3. **覆盖检测**（决定性一步）：把 `bin/gameserver.exe` 换成另一份构建，
   **保持文件字节数不变**（最坏情况），约 10 秒后 `/api/status` 的
   `exe_changed` 变 `true`、`running_build.version` 仍是旧值，launcher 日志出现
   `WARN ... 已被替换，但当前运行的仍是启动时那份；重启后才会切到新构建`。
   等尺寸也能检出，是因为判据里含 mtime。
4. **重启后切换**：`POST /api/restart?name=gameserver` → `exe_changed` 恢复
   `false`、`running_build.version` 变成新值，`/metrics` 同步。「重启才生效」语义正确。

[经验] 覆盖检测不能只比文件大小：等字节数的覆盖仍能被 mtime 检出。若将来有构建流程能同时伪造 size 与 mtime，还需要引入校验和——本轮不做（见 Out of Scope）。
