# 钓具助手：永不脱钩 / 简易手杆

Status: done

<!-- 两个开关都已实现并经单元测试覆盖；尚未在客户端实机验收（本轮不部署）。 -->

## Problem Statement

钓具助手此前只有「满红不爆」与「耐力播报」两个开关，都是防护/观测量。玩家还想要两个**直接改变钓鱼节奏**的开关：

1. **永不脱钩**：抬竿必中，搏鱼途中不再脱钩。当前链路上有三处会让鱼跑掉：`14/11` 的嘬饵否决（`BiteQuality=nibble`）、钩型保持率掷骰（`hookRetentionFails`）、以及客户端在搏鱼中上报 `14/12 f0=1/2/7/8` 时服务端按跑鱼处理。
2. **简易手杆**：中鱼指示器提示上鱼后提杆 100% 上鱼。当前 `14/11`（含海钓 `14/9`）仍会因嘬饵否决（`BiteQuality=nibble`）与钩型保持率掷骰（`hookRetentionFails`）把已提示的鱼放走；同时浮钓的 `27.f19` 是官方小值，客户端行为 105 的自动上钩会额外等 4~11 秒。

## Solution

在钓具助手一级菜单加两个功能（`3 【永不脱钩】`、`4 【简易手杆】`），复用既有的两级菜单、`kv` 开关与联系人回复链路，不新增协议字段、不改表结构。

- **永不脱钩**：`14/11`（含海钓 `14/9`）在开关开启时跳过嘬饵与保持率两项否决，直接把鱼判为中钩；`14/12` 收到「鱼跑了」帧时不收尾，改为 2 秒后重推同一尾鱼（与蓝冠天命保护同一条重推路径），不消耗鱼、不计跑鱼。
- **简易手杆**：中鱼指示器提示上鱼后提杆 100% 上鱼。开启时把 `27.f19` 固定为 1（不再按浮钓官方小值等待），并在 `14/11`/海钓 `14/9` 跳过嘬饵与钩型保持否决。不改变咬钩延迟。

## User Stories

1. 作为玩家，我在钓具助手一级菜单发 `3` 进二级，按 `y` 开启永不脱钩：此后抬竿必中（含嘬饵），搏鱼途中客户端判跑鱼也会把同一尾鱼重新挂上，不会丢鱼。
2. 作为玩家，我发 `4` 进二级按 `y` 开启简易手杆：中鱼指示器提示上鱼后提杆 100% 上鱼，不再出现嘬饵空竿与钩型保持失败。
3. 边界：两个开关默认关闭、按 uid 独立、与「满红不爆」「耐力播报」互不影响；重登后保留；二级菜单只认 `y/n/h/b`，`b` 回一级。
4. 边界：永不脱钩不覆盖断竿、断线、断轮与主动剪线——那是装备损坏，不是脱钩；也不修复已有损伤。路亚族不吃秒咬钩。

## Implementation Decisions

- 开关键：`kv` 表 `gear-assistant:no-escape:<uid>` 与 `gear-assistant:instant-bite:<uid>`，值 `"1"/"0"`，默认关。`gearFeatureKey(uid, feature)` 统一把功能名映射到键，菜单读写与游戏逻辑读同一份映射（此前是 `if feature=="stamina"` 的硬编码）。
- 菜单：一级四行 `1 【满红不爆】` / `2 【耐力播报】` / `3 【永不脱钩】` / `4 【简易手杆】`；一级别名 `脱钩`/`永不脱钩`/`noescape`、`手杆`/`简易手杆`/`simplerod`（兼容旧别名 `咬钩`/`秒咬钩`/`instantbite`）。
- **永不脱钩**（`handlers_game.go`）：
  - `hFishingHooked`（`14/11`）与 `hFishingPull`（`14/9`，海钓刺鱼入口）在进入竿锁**之前**读一次开关（SQLite 单连接，锁内查库会把整根竿堵住），开启时 `nibble` 恒为 false，嘬饵与 `hookRetentionFails` 都不再否决中钩。
  - `hFishingLeft`（`14/12`）只在 `f0∈{1,2,7,8}` 的跑鱼帧上查开关。开启且 `Hooked && Pending!=nil && CastActive` 时：`noEscapeRepush++`、`Hooked=false`、`PushSent=false`、`armPendingTimerLocked(2s)` 重推同一尾，然后直接返回，不落 `escapes` 计数、不写跑鱼提示、不走 `settleFightReelWearState` 之后的收尾。
  - `maxNoEscapeRepush = 60` 兜底：客户端与鱼实体状态不兼容时（重推也不会中）避免无限重推刷屏，到上限即落回原跑鱼路径。
  - 重推复用蓝冠天命保护的既有路径（`armPendingTimerLocked` + `repushRodPendingTicket`），该路径已在实机验证过能把已离场的鱼重新挂上。
- **简易手杆**（`fishing.go` / `handlers_game.go`）：
  - `Push14_14` 发鱼时读一次开关：开启则 `forceF19=true`，`buildFish27` 把 `27.f19` 固定为 1.0（浮钓也不再按官方小值等待）。
  - `hFishingHooked`（`14/11`）在竿锁外读开关；开启时跳过嘬饵与 `hookRetentionFails`，直接置 `Hooked=true`、`HookConfirmed=true`。
  - `hFishingPull`（`14/9`）的海钓分支同样接受该开关，跳过嘬饵否决。
  - **不修改咬钩延迟**：原先 300ms / 空口 1s / 离场冷却 4s 的实现已删除，`biteDelay` 仍按采样与落水保护走。

## Testing Decisions

- `gear_assistant_fishing_toggles_test.go`：
  - `TestGearAssistantHelpListsFishingToggles`：一级菜单列出四条命令；`3 开` 只写 no-escape 的键，`b` 回一级后 `4 开` 只写 instant-bite，两个开关互不影响。
  - `TestGearNoEscapeRepushesSameFish`：关时 `14/12 f0=1` 照旧清鱼且无重推定时器；开后同一尾鱼被保留、`Hooked=false`、`pendingTimer!=nil`、连续三次跑鱼都重推同一 GUID；到 `maxNoEscapeRepush` 后落回正常跑鱼。
  - `TestGearNoEscapeForcesHookConfirmation`：`BiteQuality=nibble` + `HookHoldP=0` 的鱼在关时被判嘬饵（不中钩），开后直接进入搏鱼态。
  - `TestGearSimpleRodKeepsBiteDelay`：在 `bite_delay_fixed=20` 的服务器上开启简易手杆，`BiteAt` 仍需 15 秒以上（不缩短咬钩延迟）。
  - `TestGearSimpleRodConfirmsHook`：`BiteQuality=nibble` + `HookHoldP=0` 的鱼在关时被判嘬饵（不中钩），开后直接进入搏鱼态。
- 既有 `TestGearAssistantCommandsAndPersistence` / `TestGearAssistantToggleKeepsWearBoundaries` 保持不变并通过。

## Out of Scope

- 不把「永不脱钩」扩展成「永不丢鱼」：断竿、断线、断轮、主动剪线照常结束这一竿。
- 不改路亚的 1.5~6 秒收线窗口；简易手杆只作用于 `f19` 与中钩确认，不缩短咬钩延迟。
- 不改 `14/8` / `14/12` / `14/14` 的响应形状、不改表结构、不加迁移、不改客户端。
- 本轮不部署、不提交。

## Further Notes

- `[坑]` 每次查开关都要开一个 SQLite 事务，而 `store/staticdb.go` 的 `MaxOpenConns(1)` 意味着**事务内不能再开事务**（曾把二级菜单查死过）。因此所有新读点都放在竿锁之外、且只在低频事件（排钩、`14/11`、跑鱼帧）上触发。
- `[坑]` `14/12` 的处理在 `st.mu` 内，`noEscape` 必须在 `st.mu.Lock()` 之前算好；`armPendingTimerLocked` 则必须持锁调用。
- `[未验证]` 重推 2 秒后客户端是否会把鱼重新画出来、以及连续重推几十次的表现，需要实机确认——蓝冠天命保护只重推 2 次，这里放到了 60 次。
- `[已知限制]` 简易手杆只解决“提示已上鱼后提杆”和 `f19`；14/14 之后客户端“接近 + 试探”的随机时长（实测 0.5~25 秒）仍由客户端决定，服务端改不了。
- `[坑]` `14/9`（抬杆/小刺）是玩家搏鱼时会连点的热路径，因此只在「海钓 + 有在途鱼」时才去查开关，淡水 14/9 零额外查询。
