# 调试工作流（协议类 bug）

目标：**一轮复现就能拿到证据，不再靠猜。**
所有工具都在 `tools/` 下，用 `powershell -ExecutionPolicy Bypass -File` 运行。

## 三步闭环

1. **复现**：`tools\trace.ps1` 开着，让用户在游戏里操作一次。
2. **取证**：
   - 服务端：抓到的 `cmd=xx/y` 收/回 hex；
   - 客户端：`tools\clientlog.ps1` 拿到异常栈（有栈就有方法和字段）。
3. **定位**：`tools\proto.ps1 <命令>` 查该命令的 DTO/wire 类/字段/元素类/args code，
   再对照客户端反编译文件确认强转类型。**先查表，再改代码。**

## 工具

### proto.ps1 —— 协议字典（最常用）

```powershell
.\tools\proto.ps1 21/2                # 命令档案
.\tools\proto.ps1 class hlopebmchhp   # 单个 wire 类的字段和 args code
.\tools\proto.ps1 list                # 有抓包档案的命令列表
```

输出包含：语义名、响应 DTO、wire 类、type_id、每个字段的 op/名字/中文含义、
数组字段的**元素类 + 该用的 args code**、客户端反编译文件路径和强转类型。

典型用途：要发一个对象数组时，直接看 `args code=xxx`，不用再翻三张表。

### trace.ps1 —— 抓一次交互

```powershell
.\tools\trace.ps1                      # 默认 120 秒，自动屏蔽 24/1 等高频命令
.\tools\trace.ps1 -Restart             # 先重启 gameserver 再抓
.\tools\trace.ps1 -Seconds 300 -Filter 'cmd=21/|cmd=22/'
```

输出同时写入 `logs\_trace.txt`。高频位置上报（`24/1`）默认过滤，否则环形日志
几秒就被冲掉。

### clientlog.ps1 —— 客户端异常

```powershell
.\tools\clientlog.ps1 -Last 10 -Frames 8
.\tools\clientlog.ps1 -Since '12:01'
```

`Player.log` 通常在**退出游戏时**才落盘；如果为空，先退出客户端再看。

### 其他观测点

- 控制面板 <http://127.0.0.1:9500>：进程状态、内存环形日志（最近 800 行/进程）。
- 健康与指标：`9501`(gateway) / `9502`(gameserver) 的 `/healthz`、`/metrics`；管理后台直接使用 `9500/admin/`。
- 命令跟踪开关：存在 `data/trace_cmds.on` 或环境变量 `RF4_TRACE_CMDS=1`。
- 注意：Windows 上 `logs\*.log` 的**目录元数据（大小/时间）会滞后**，
  判断日志是否在写要看文件尾部内容，不要看 `Length`。

## 已确认的协议事实（别再重新踩）

| 项 | 结论 |
|---|---|
| 21/1 根数组 | args code **144**（`ookkpknacja`/505，元素 `keialdedcnp`） |
| 21/1 f3 槽位 | 客户端强转 `oinpmjophkh[]` → 必须发**普通 u32 数组**（0x05）；发 typed_u32arr（0x07）会 InvalidCastException，整行作废 |
| 21/2 组件数组 | 元素类 `poolccanfhd` = `hlopebmchhp`/504 → args code **143** |
| poolccanfhd 字段 5 | `ojoboopbaec[]` = `hpopgbjohpk`/507 → 空数组也要发 args code **146**，发 null 会让客户端 `foreach` 崩 |
| 21/5 | args code **145**（空兑换列表） |
| 卡片/卡册实例数组 | 客户端真实类型注册表：**362**=`ipdpoaiekem`、**364**=`imdcjllgdcn`；363是`phjbhljooid`卡册定义。使用`by_elem`显式映射，不能用`by_type_id`合成值或未注册的DTO名称替代 |
| 卡片入册同步 | 800更新列表同时发送原卡及卡册，移除列表为空；原卡StorageType仍为18，父GUID指向卡册。4/1、4/4、4/5也需包含已入册原卡，客户端按父GUID填卡位；卡册f0快照不能替代库存卡片 |
| 竿的零件 | 客户端工作坊按竿 `f5` 的 `gcfcdnfmnbh` 列表渲染；出厂部件（竿坯/手柄/导环）必须同时写进 f5，只写 f0~f2 不够 |
| 装配派生表 | `rig_component` 由 `f5` 单向投影；历史数据可能为空，`reconcileRigProjection` 可回填 |
| 聊天玩家列表 24/4 | 客户端返回类型为 `int[]`，使用普通 u32 数组；24/19 资料卡为 bytes 包装的 `dgbmlekfpbg`/2002 |
| 玩家“发送消息” | 使用个人邮件 19/1，参数为目标 UID 和正文、响应 null；19/10 推送 args(524)[11062]。不是 24/10 公聊或 24/27 广播 |
| 5008.f2 阶段奖励预览键 | string[] 编码 = **0x04 数组**（u16 数量 + 每元素 u32 长度 + UTF8 目录键，与 1/5 配置版本响应同型）；发 args 容器会被客户端按 Object→String[] 强转失败并断线（4.0.25034 实机验证，2026-11-13） |

## 改协议代码前的检查清单

1. `proto.ps1 <cmd>` 确认响应类、字段 op、数组元素类和 args code；
2. 对着客户端 `.cs` 确认它强转的目标类型（`lpdfanjcalm<T>`）；
3. 改完先跑 `go build`，重启单个进程（面板 API 或 `.\tools\trace.ps1 -Restart`）；
4. 让客户端复现一次，同时看服务端 trace 和 `clientlog.ps1`：异常栈消失才算修好；
5. 失败时**先拿新异常栈**，不要再盲改字段。

## 构建（重要）

```powershell
.\tools\build.ps1            # 只编 gameserver（标准参数：-trimpath -s -w -ldflags version）
.\tools\build.ps1 all        # gateway/gameserver/seed
```

- **必须保留 -s -w**：未 strip 的 26MB 二进制会被 Windows Smart App Control 拦下
  （An Application Control policy has blocked this file），strip 后的 18MB 正常。
- 目标 exe 正在运行时无法覆盖，先停进程（面板 stop）或改编其它目标。
- 服务端单独重启：POST http://127.0.0.1:9500/api/restart?name=gameserver。
