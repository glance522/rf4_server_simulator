## 本地离线分支

当前工作区基于 `codex/selected-gameplay-fixes`，已合入上游和 PR 修复；离线功能及其他任务尚未发布的改动保留在本地。`upstream` 指向原 GitHub 仓库。

- 双击根目录 `启动游戏.exe`，会先启动本地后台，再打开游戏副本。
- 游戏账号和密码均为 `123123`。键盘和鼠标由玩家操作。
- `local-service` 保存本地认证、协议桥接和自动化测试；Go 游戏逻辑沿用下方原项目。
- `Game` 是测试游戏副本，`profile` 是它的独立用户设置；不使用原版 `E:\RF4` 的文件。
- `local-service/data/gameplay.sqlite3` 保存角色和游戏进度；`local.sqlite3` 保存本地账号。
- `data/characters` 是导入种子；重复启动不会覆盖已导入角色的进度。
- 后台仅监听本机，HTTP `19214`、游戏连接 `19213`。关闭网页不会停止后台，可在本地页面停止后台。
- 游戏、存档、配置和程序产物均已加入 Git 忽略；请勿强制加入版本库。

本机需要 Python 3.12；修改 Go 源码后用 Go 1.27.3 构建：

```powershell
go build -o bin/gameserver.exe ./cmd/gameserver
go build -o bin/import-character.exe ./cmd/import-character
go build -o bin/local-membership.exe ./cmd/local-membership
go build -ldflags '-H=windowsgui' -o 启动游戏.exe ./cmd/local-launcher
go test ./...
go vet ./...
python -m unittest discover -s local-service/tests -v
```

已实测进入地图、取竿、抛竿、收鱼并入护；后续互动仍在逐项验证和修复。自动化测试通过不代表全部游戏操作已验收。

个人中心使用提供的20261103源码包页面与素材，访问 `/portal/`；账号、金币单位和会员交易沿用本地版接口。调试后台入口为 `/admin/`，管理员账号 `admin`、初始密码 `123123`；可通过 `RF4_LOCAL_ADMIN_PASSWORD` 在启动前覆盖密码。原控制页保留在 `/local-control/`。

本地页面提供会员与调试设置：

- 当前本地套餐：1天1金币、7天5金币、30天15金币；使用角色游戏金币，有效会员的鱼获经验额外增加100%。
- 购买会保存订单、扣款和真实到期时间；重试同一订单不会重复扣款，在线角色会刷新会员状态。
- “调试模式”首次启用采用3–5秒鱼讯、鱼体重4倍预设，保留落水/就位保护及装备上限。只影响新鱼，大鱼拉力仍随体重增加；关闭后恢复先前参数。
- 重启保留角色进度和玩家选择的调试开关；不覆盖已钓获的鱼。
- 会员实际购买后游戏面板显示、护网旧鱼显示仍需当前版本手工验收。

---

# rf4_server_simulator

对一款钓鱼游戏的**本地服务端进行模拟与技术研究**的 Go 实现。项目采用三进程架构（接入网关 / 游戏逻辑服务 / Web 管理后台），使用本地单文件 SQLite，尽量做到无外部中间件即可运行。

> 本项目由玩家兴趣驱动，不以盈利为目的，不是完整复刻，也不是可直接长期运营的私服方案。
## 本项目目前处于持续开发阶段，制作组未收取任何费用，网络上的一切收费行为都是假冒，请勿相信

## 下载即玩（推荐玩家）

如果只是想游玩，**不需要安装 Go，也不需要自行编译**。请前往 [Releases](https://github.com/glance522/rf4_server_simulator/releases/latest) 下载最新的 Windows 已编译版本，解压后阅读包内的“快速开始.txt”，双击“启动服务器.cmd”或 `bin\launcher.exe` 即可运行。

如果正式 Release 更新不及时，可以尝试 [Releases](https://github.com/glance522/rf4_server_simulator/releases) 页面中最新标记为 **Pre-release** 的 **Preview** 版本。Preview 由 `main` 分支最新提交自动构建，包名和发布说明中会标明对应的 Commit ID。

游玩过程中如果遇到 Bug、崩溃、协议异常或功能不符合预期，欢迎在 [Issues](https://github.com/glance522/rf4_server_simulator/issues) 中反馈。为便于定位问题，请尽量提供：

- 使用的 Release 版本和客户端版本；
- 问题发生前做了什么，以及能否稳定复现；
- 相关日志或截图（请先移除 token、账号、路径等个人信息）；
- 如果与特定钓法、装备或地图有关，请一并注明。

我们希望通过玩家的实际反馈持续修正问题，让项目变得更好。

## 项目定位与使用边界

- 本项目是个人爱好驱动的本地研究项目，主要用于学习和实践协议编解码、会话保序、状态机、数据持久化与服务端工程。
- 受时间、精力和对完整游戏机制的理解限制，当前进度**无法与成熟私服相比**，也不以追赶或替代私服为目标。
- 钓鱼系统目前只保证**能够完成上鱼和基础结算**，并没有完整复刻官服的鱼群、概率、生态、活动节奏与长期经济机制。
- 很多系统仍不完善。部分命令目前只保证协议形状合法、客户端不崩溃，实际业务语义仍在补齐；请不要把本项目视为完整或生产可用的服务端。
- 本项目**不会开放任何捐款、充值、赞助或付费功能**，维护者不以盈利为目的。
- **请勿向任何第三方私服充值。** 私服的资金与稳定性没有保障，可能随时关停或跑路，请自行谨慎判断风险。
- 请遵守当地法律法规、游戏服务条款及相关权利人的要求。本仓库只包含服务端模拟与学习代码，不提供游戏本体。

## 交流与协作

欢迎正在推进同类本地化、协议研究或服务端实现的同仁，通过 Issue、Pull Request 或其他公开渠道交流进展、协议结论和实现思路。

我们更希望把这里作为一个开放的技术交流与协作入口，而不是收费产品或私服运营项目。发现错误、补齐机制或分享兼容性结论，都是非常有价值的贡献。

提交代码前请阅读 [贡献指南](CONTRIBUTING.md)。所有作者和 AI Agent 都应保持提交职责单一，并遵守项目的开源与使用边界。
## 个人中心与游戏金币会员

启动 gateway 后，可在网关 HTTPS 地址的 `/portal/` 打开个人中心，先从同一设备进入游戏创建角色，再进入页面。页面沿用上游按来源 IP 区分角色的规则，不提供独立账号注册；共用出口或代理时也受这一识别规则限制。

会员仅消耗角色存档中的游戏金币：1 天 1 金币、7 天 5 金币、30 天 15 金币。有效会员钓获经验额外增加 100%，永久会员无需购买；同一订单重试不会重复扣款。管理后台可设置普通、限时或永久会员。

升级前备份玩家库。升级统一使用 `membership_exp`，并兼容导入旧本地 `membership_expires_at` 的到期时间；历史钓获记录保留，新的钓获按鱼 GUID 去重。迁移自动执行，重复启动不重复续期、结算或覆盖进度。

UI 回归测试需安装 Playwright，并通过 `RF4_TEST_BROWSER` 指定 Chromium 或 Edge；执行 `node --test internal/portal/ui_test.cjs`。测试只使用隔离的角色和接口数据。

## 当前实现概览

以下是当前代码已经打通的**主链路和基础机制**，不是完整功能清单：

- **接入与协议**：HTTPS 登录与 token 签发、realtime TCP 握手、帧编解码、
  RC4 连续密码流、ACK、传输保活、断线重连与 RC4 状态接管；同时具备
  响应契约校验、推送守卫和协议兜底，避免未知命令直接导致客户端崩溃。
- **会话与玩家数据**：多会话管理、同 token 重连接管、玩家档案、等级与经验、
  银币/金币/银行、地图与出生点、旅行、世界时钟、技能树、成就与渔获纪录。
- **钓鱼主链路**：抛竿、落水、咬钩、刺鱼、搏鱼、收线、结算、入护网与放生；
  支持竿型、饵、拟饵、钓深、分水域鱼口、路亚短窗节奏，以及钓鱼状态持久化
  和重连恢复。
- **物品与钓组**：背包与护网、物品实例与属性、钓组装配/拆卸、子部件物化、
  强度与耐久、工坊修理、快捷栏绑定。
- **经济与内容系统**：商店目录、服务端定价、库存覆盖、购买与退货事务、
  鱼市交易、邮件与附件、制作、竞赛、基础社交资料与纪录。
- **管理与运维**：Control Deck 管理后台（JWT + 内嵌 SPA）、玩家/库存/技能/
  商店/鱼口/邮件管理、踢人与封禁、操作审计；launcher 负责进程监督、
  健康检查和日志面板。
- **存储与可靠性机制**：SQLite WAL、数据库迁移、静态配置库只读 ATTACH、
  请求回执与幂等控制、事务化经济操作、唯一约束与数据完整性检查。
- **隐私与脱敏**：仓库不内嵌可关联到玩家的回放响应、实例 GUID、token 或账号数据；
  协议兜底只使用结构清单，新手物品在运行时按静态定义生成。
- **测试与回归**：单元测试、集成测试、协议语料回环测试，以及钓鱼、经济、
  装配、重连等关键链路的专项回归。

### 已知边界

- 钓鱼玩法以“能稳定完成上鱼与结算”为首要目标，不追求当前阶段完全复刻官服手感、数值和生态。
- 非核心系统、活动系统和大量边界分支仍在持续补充；协议能够响应不等于业务已经完整实现。
- 目前仅针对下方标注的客户端版本做适配，其它版本、不同服务端配置和长期运行场景均未验证。

## 架构

```text
游戏客户端（hosts 指向本机）
   │ HTTPS :443 登录              TCP :9341 实时
   ▼                              ▼
┌─────────────── gateway ────────────────┐
│ 登录应答（token 签发）/ TCP 接入 / 握手 │
│ 帧编解码 / RC4 双向加解密 / ACK / 保活  │
│ 重连接管（RC4 状态落库）                │
└──────────────┬─────────────────────────┘
                │ 内部隧道（明文 RPC 信封，PSK 握手）
                ▼
┌────────────── gameserver ──────────────┐        ┌──── admin :8080 ────┐
│ 信封编解码 / 命令分发 / 推送守卫        │◄───────│ Web 管理后台        │
│ 钓鱼闭环 / 物品 / 商店 / 邮箱           │ 共享   │ 玩家/库存/鱼口/邮件  │
└──────────────┬─────────────────────────┘ SQLite └─────────────────────┘
                ▼
        data/rf4server.db（单文件，WAL）
              + server_data/static.sqlite3（运行期只读 ATTACH）
```

- **gateway**：客户端唯一入口。持有 RC4 会话加密（密码流跨帧连续）、先 ACK
  后转发、传输保活、断线重连接管。
- **gameserver**：游戏逻辑。命令分发、推送守卫（形状不符拒发）、钓鱼闭环、
  物品/商店/邮箱/技能；管理指令经共享 SQLite 队列消费。
- **admin**：Control Deck 管理后台（JWT + 内嵌 SPA，无外部依赖）。玩家档案/
  货币/等级/技能熟练度、背包与护网增删、商店库存覆盖、分水域鱼口控制、
  多附件邮件、踢人/封禁与全操作审计。

## 适用客户端与接入准备

- **适用客户端版本：4.0.25034**；其他版本尚未验证。
- **修改 hosts 把登录域名指向本机**（需管理员权限）：
  编辑 `%SystemRoot%\System32\drivers\etc\hosts`，追加以下内容后保存：
  ```text
  127.0.0.1 api.rf4game.ru
  127.0.0.1 api.rf4game.com
  ```
- **信任本地证书**：首次运行 gateway 会自动生成自签名证书 `certs/server.crt`，
  双击安装到“受信任的根证书颁发机构”（客户端登录走 HTTPS，需要信任它）。
- 测完想回官服：把 hosts 里添加的行删掉即可。

## 快速开始（一键）

双击或运行 `bin/launcher.exe`（无需任何命令行）：

- 首次运行自动创建 `data/` 并初始化本地数据库（迁移 + 静态配置导入 + 默认管理员 `admin/admin123`）
- 按序启动 gameserver → gateway → admin 并做健康检查；**崩溃自动重启**（指数退避）
- **系统托盘常驻**：绿=全部运行 / 黄=部分 / 红=停止；菜单可打开面板/管理后台、启停全部、退出
- **控制面板** `http://127.0.0.1:9500`：状态卡片、单进程启停/重启、三路实时日志
- **管理后台** `http://127.0.0.1:8080`

进阶（可选命令行）：

```powershell
bin\seed.exe       -config config.yaml                                   # 仅初始化数据库
bin\gameserver.exe -config config.yaml                                   # 单独运行某进程
bin\admin.exe      -config config.yaml -init-admin admin:自定义密码       # 自定义管理员
```

## 从源码运行或自行编译（可选）

如果你下载的是源码版本，或者希望自行编译，可以先安装 Go 1.27.3 或更高版本，然后让 Dsh、豆包、WorkBuddy、Codex 等智能体根据你的系统协助完成。只玩 Release 版本时不需要安装 Go。操作前建议备份 hosts，并且不要上传或分享 `config.yaml`、`certs/`、`data/`。

下面的 prompt 可以按需修改路径后直接使用：

```text
请帮我在这台 Windows 电脑上部署并启动 D:\Code\rf4_go_public 里的 rf4_server_simulator。

请先检查：
- Windows 版本，以及当前是否以管理员权限运行；
- Go 是否已安装，版本是否不低于 1.27.3；
- 443、9341、8080、9401、9500-9503 端口是否可用。

然后完成以下工作：
1. 阅读 README.md、config.yaml 和 docs/deploy.md，说明你将执行的操作。
2. 构建项目，确认 bin 下生成 gateway.exe、gameserver.exe、admin.exe、seed.exe 和 launcher.exe。
3. 检查 config.yaml 中 gateway.tunnel.psk 与 game.tunnel.psk 一致，sqlite.static_path 指向 server_data/static.sqlite3。
4. 备份 C:\Windows\System32\drivers\etc\hosts，然后把 api.rf4game.ru 和 api.rf4game.com 指向 127.0.0.1。
5. 启动一次 gateway 生成 certs/server.crt 和 certs/server.key，并指导我把 server.crt 安装到“受信任的根证书颁发机构”。不要上传或分享私钥。
6. 启动 bin\launcher.exe，检查 http://127.0.0.1:9500、http://127.0.0.1:8080 以及各服务健康检查。
7. 只做构建和运行配置修复，不改变业务逻辑。每一步告诉我执行内容、结果和失败原因；需要管理员权限时先停下让我确认。
```

## 构建

```powershell
# Windows（PowerShell）
powershell -File build.ps1          # 产物在 bin/

# 或 make（Git Bash / Linux）
make build
```

依赖 Go 1.27.3+，纯 Go 依赖（SQLite 驱动为 modernc.org/sqlite，无 CGO）。

## 配置

- `config.example.yaml`：模板（入库）
- `config.yaml`：实际配置（**不入库**，从模板复制后修改）
- 静态库：`sqlite.static_path`（默认 `server_data/static.sqlite3`），三进程运行期只读 ATTACH
- 环境变量覆盖：`RF4_SQLITE_PATH`、`RF4_SQLITE_STATIC_PATH`、`RF4_LOG_LEVEL` 等（见 `internal/config`）

## 运行

```powershell
bin\seed.exe       -config config.yaml   # 初始化本地数据库（幂等，仓库自带静态配置库）
bin\gameserver.exe -config config.yaml
bin\gateway.exe    -config config.yaml
bin\admin.exe      -config config.yaml -init-admin admin:你的密码
```

详细步骤、管理后台说明、协议约束与故障排查见 [docs/deploy.md](docs/deploy.md)。

## 开发

```bash
make test      # go vet + 全部单测（含端到端钓鱼闭环，无需外部服务）
make corpus    # 语料回环测试（语料文件不在仓库中时自动跳过）
```

## 目录速览

```text
cmd/launcher                        图形化常驻监督器（托盘+面板）
cmd/gateway|gameserver|admin|seed   服务进程与初始化工具
internal/proto                      协议字节层（帧/RC4/值标记/信封）
internal/wiregen                    生成的类型表（364 类）+ 语料回环门
internal/tunnel                     网关↔主服内部隧道
internal/gateway                    登录 HTTPS / realtime TCP / 会话
internal/gameserver                 会话核 / 处理器 / 钓鱼 / 物品 / 商店
internal/store                      SQLite 存储层 + 迁移
internal/admin                      管理 API + 内嵌 SPA
docs/                               部署手册 / 回归清单
```

## 客户端兼容与实现范围

- 当前支持的客户端版本为 **4.0.25034**；其他版本尚未验证。
- 目前已测试的钓法为：**手杆、简易沉底、简易路亚**。
- 所有实现均为较简实现，以客户端能够顺畅运行和完成基础玩法闭环为主要目标；没有复刻官服的手感、概率、生态或完整机制。

## 许可

项目采用 **MIT 协议**（见 [LICENSE](LICENSE)）。本 README 中的项目定位与使用边界用于说明维护者立场，不改变许可证本身授予的权利与义务。

## 可选本地离线工具

构建同时生成 `import-character`、`local-membership` 和 `启动游戏.exe`，本地 HTTP 服务使用 Python 3.12 标准库。`local-service` 提供本机登录、管理与 Go 游戏逻辑的桥接；游戏客户端及角色快照须由使用者自行提供，不随源码或预发布包分发。

本地服务数据位于 `local-service/data/`，游戏副本位于 `Game/`；两者不入库。没有本地角色快照时，使用合成数据运行公共回归，真实角色导入测试会明确跳过。管理与角色账户由本机配置和数据决定，公开提交不包含任何真实存档或登录凭据。

咖啡厅订单使用已核对的公开报价组合，稀有订单按同水域同鱼种最高档订单的重量比例估价，这是本地规则，并非官方公式。目前覆盖 17 个有报价资料的水域；熊湖暂无报价模板。刷新使用现实时间，同一订单重复交付不会重复付款。

公开门户以 CSS 绘制头像和标记，不分发第三方图片。运行界面检查前安装 Playwright，并用 `RF4_TEST_BROWSER` 指定浏览器；Python 回归执行 `python -m unittest discover -s local-service/tests`。
