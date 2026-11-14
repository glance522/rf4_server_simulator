# 部署

本指南介绍 `rf4server` 的本地部署方式。协议格式面向 RF4 客户端 **4.0.25034**。

## 组件与端口

| 进程 | 默认端口 | 用途 |
| --- | ---: | --- |
| `gateway` | 443/TCP | HTTPS 登录端点。 |
| `gateway` | 9341/TCP | 游戏客户端实时连接。 |
| `gameserver` | 9401/TCP | 网关到游戏服务器的内部隧道。仅绑定到可信网络接口。 |
| `launcher` | 9500/TCP | 本地控制中心，同时提供内嵌管理后台 `/admin/`。 |
| `gateway` | 9501/TCP | 健康检查、就绪检查、指标和性能分析。 |
| `gameserver` | 9502/TCP | 健康检查、就绪检查、指标和性能分析。 |

观测端点提供 `/healthz`、`/readyz`、`/metrics` 和 `/debug/pprof`。

## 前置条件

- 从源码构建时，需要 Go 1.27.3 或更高版本。
- `launcher` 需要 Windows；其他服务器命令可在 Go 支持的其他平台上构建。
- 具备绑定 443 端口的权限。
- 配置的端口没有被其他进程占用。

## 构建与配置

```powershell
powershell -ExecutionPolicy Bypass -File .\build.ps1
Copy-Item config.example.yaml config.yaml
```

启动前编辑 `config.yaml`：

- 将 `gateway.advertise_host` 设置为客户端可用于实时连接的地址。
- 将 `gateway.login_domain` 设置为主登录主机名。生成的证书会覆盖该名称、`localhost` 和 `127.0.0.1`。
- 为 `gateway.tunnel.psk` 和 `game.tunnel.psk` 设置相同的非空值。
- 在将服务开放到任何网络之前，替换 `admin.jwt_secret`、`admin.password` 和隧道 PSK。
- 保持 `sqlite.static_path` 指向随附的 `server_data/static.sqlite3`。

`config.yaml` 已被 `.gitignore` 排除。配置加载器支持 `config.example.yaml` 中说明的 `RF4_*` 覆盖项。

YAML 文件中的管理员用户名和密码会在启动时应用。账户不存在时会创建账户；已有账户的密码不同时会重置为配置中的密码。

## 初始化数据

在启动服务器进程前，先运行一次 `seed`。重复运行是安全的。

```powershell
bin\seed.exe -config config.yaml
```

初始化会执行数据库迁移、导入静态目录、创建配置的默认玩家档案，并创建或对齐管理员账户。重复运行不会删除玩家数据。

## 启动

### 启动器

```text
bin\launcher.exe
```

首次使用时，launcher 会初始化数据库，启动 `gameserver` 和 `gateway`，执行健康检查，并重启已退出的进程。管理后台 handler 直接内嵌在 launcher 中，统一入口为 `http://127.0.0.1:9500`，不再监听独立的 8080 管理端口。

### 从控制中心启动游戏

在统一控制中心的“运行控制”页填写 `rf4_x64.exe` 的绝对路径并保存。路径会写入当前 `config.yaml` 的：

```yaml
launcher:
  game_path: 'D:\Games\RF4EN\Game\rf4_x64.exe'
  game_region: "CN"
```

点击“启动游戏”后，launcher 直接执行：

```text
D:\Games\RF4EN\Game\rf4_x64.exe -region CN
```

只接受本机同源页面调用，路径必须是存在的绝对路径，且文件名为 `rf4_x64.exe`。

### 手动启动

在独立终端中分别启动以下命令：

```powershell
bin\gameserver.exe -config config.yaml
bin\gateway.exe -config config.yaml
```

手动运行这两个进程不会启动管理控制台；管理控制台随 `launcher` 的 9500 统一入口提供。

默认本地开发凭据为 `admin` / `admin123`。对于任何未与本地计算机隔离的环境，请在 `config.yaml` 中替换这些凭据。

## 部署后自证版本

`bin/` 下的可执行文件会被任何一次 `build.ps1` / `tools/build.ps1` / `make build` 覆盖，而这几个脚本都按**执行时的检出目录**编译。本仓常同时存在多个 worktree，覆盖之后进程还在跑旧代码，从 `/healthz`、托盘、启动日志都看不出来，于是「明明部署了却没生效」。

部署完必须核对「运行中的进程」而不是「磁盘上的文件」：

```powershell
# 直接问进程：这是哪个构建、从哪个目录构建的
curl.exe -s http://127.0.0.1:9502/metrics | Select-String rf4_build_info

# 经 launcher 看：running_build 是子进程自报，exe_changed 表示磁盘已被替换
curl.exe -s http://127.0.0.1:9500/api/status
```

正常输出形如：

```text
rf4_build_info{process="gameserver",version="20260101090000",buildts="20260101010000",commit="abc1234",source="<检出目录>"} 1
```

要点：

- `source` 是构建时的**检出目录**（正斜杠），`commit` 是对应提交的短 SHA，`buildts` 是 UTC 构建时间。`bin/gameserver.exe` 的 `source` 必须是你期望的那个 worktree——不一致说明被别的检出覆盖过。五个构建入口（`build.ps1` / `tools/build.ps1` / `make build` / `.github/scripts/build-preview.sh` / `build-release.sh`）都会自动注入这四个标签，不需要手填；CI 制品的 `commit` 取触发构建的 `$GITHUB_SHA` 短码。
- `procs[].running_build` 缺失或面板显示「未知」表示观测端口不通或跑的是不带这些标签的老构建，不能当成「版本正常」。
- `procs[].exe_changed` 为真表示磁盘上的 exe 已换但进程没重启（面板同时显示提示条，launcher 日志出现 `WARN ... 已被替换`）。此时新构建**尚未生效**，重启对应进程才会切换。
- `/api/status` 的 `current_version` 是 **launcher 自己**的版本；子进程版本看 `procs[].running_build.version`，两者混读会得出错误结论。

手工做验证性部署时请显式带语义化版本号，便于事后追溯：

```powershell
go build -trimpath -ldflags "-s -w -X 'rf4server/internal/buildinfo.Version=<语义化标签>' -X 'rf4server/internal/buildinfo.BuildTs=<UTC 时间戳>' -X 'rf4server/internal/buildinfo.GitCommit=<提交短 SHA>' -X 'rf4server/internal/buildinfo.SourceDir=<检出目录>'".Replace('\','/') -o .deploy/gameserver.exe ./cmd/gameserver
```

设计与取舍见 [deploy-self-verify](../specs/changes/deploy-self-verify/spec.md)。

## 客户端接入

客户端必须将登录主机名解析到网关，并信任生成的 HTTPS 证书。对于使用默认登录主机的客户端，请在 hosts 文件中添加以下条目：

```text
127.0.0.1 api.rf4game.ru
```

如果客户端还会请求其他登录主机名，也需要将其映射到网关，并提供覆盖该主机名的证书。

先启动一次网关以生成 `certs/server.crt` 和 `certs/server.key`，然后仅在本地测试环境中信任 `certs/server.crt`。该证书为自签名证书；不得重新分发私钥。

`gateway.advertise_host` 和 `gateway.advertise_port` 会包含在登录响应中。客户端使用这两个值建立实时连接。

## 数据

主数据库是一个 SQLite 文件，通常为 `data/rf4server.db`，由 gameserver、gateway 和内嵌管理后台共享使用，并启用 WAL 模式。

静态目录存储在 `server_data/static.sqlite3` 中，运行时以只读方式挂载。隔离模型详见 [static_runtime_isolation.md](static_runtime_isolation.md)。

`seed` 会将静态表导入主数据库，作为迁移和兼容副本。运行时会将对存储层所列静态表的读取重定向到只读的 `static` schema。

## 安全与协议约束

- 网关登录流程不会认证游戏账户。它会为配置的用户 ID 签发 token，而实时连接会接受结构有效、但未在当前 token 存储中注册的 token。请将网络访问边界视为信任边界，不要将服务暴露到公网。
- 完整的登录 token 就是 RC4 密钥。输入和输出 RC4 流会跨帧连续。帧分配、加密和套接字写入必须通过会话写入器保持有序。
- 在处理客户端帧之前先发送 ACK。改变这一顺序会破坏客户端协议。
- 服务器到客户端的推送必须使用预期的顶层 value 结构，并通过响应契约检查。无效推送会被拒绝，而不会发送。
- 世界时钟 keepalive 间隔必须小于客户端连接超时时间。默认为 5 秒；客户端在没有下行数据约 10 秒后断开连接。世界时钟使用 UTC。
- 将 launcher、隧道和观测端口绑定到可信网络接口。替换所有默认凭据和密钥。

## 智能体辅助部署 prompt

以下 prompt 可按本机实际路径调整后，直接交给 Codex、Dsh、豆包、WorkBuddy 等智能体：

```text
请帮我在这台 Windows 电脑上部署并启动 <项目目录> 里的 rf4_server_simulator。

请先检查：
- Windows 版本，以及当前是否以管理员权限运行；
- Go 是否已安装，版本是否不低于 1.27.3；
- 443、9341、9401、9500-9502 端口是否可用。

然后完成以下工作：
1. 阅读 README.md、config.yaml 和 docs/deploy.md，说明你将执行的操作。
2. 构建项目，确认 bin 下生成 gateway.exe、gameserver.exe、seed.exe 和 launcher.exe。
3. 检查 config.yaml 中 gateway.tunnel.psk 与 game.tunnel.psk 一致，sqlite.static_path 指向 server_data/static.sqlite3。
4. 备份 C:\Windows\System32\drivers\etc\hosts，然后把 api.rf4game.ru 和 api.rf4game.com 指向 127.0.0.1。
5. 启动一次 gateway 生成 certs/server.crt 和 certs/server.key，并指导我把 server.crt 安装到“受信任的根证书颁发机构”。不要上传或分享私钥。
6. 启动 bin\launcher.exe，检查 http://127.0.0.1:9500、http://127.0.0.1:9500/#admin 以及各服务健康检查。
7. 只做构建和运行配置修复，不改变业务逻辑。每一步告诉我执行内容、结果和失败原因；需要管理员权限时先停下让我确认。
```
