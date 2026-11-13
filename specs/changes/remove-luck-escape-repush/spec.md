# 删除蓝冠天命保护（中钩后救援重推）

Status: done

<!-- 状态取值：draft | ready | in-progress | done | cancelled
     完成时原地改这里，不要归档、不要搬家、不要另开第二份。 -->

## Problem Statement

蓝冠天命保护（c14b5bf，2026-11-08 引入）在蓝冠天命鱼（grade 4，`LuckProtected`）
中钩后收到钩型脱钩帧（14/12 f0=1/2/8）时，拒绝该脱钩并最多重推同一尾鱼 2 次
（`LuckRepush < 2`，2 秒后以新 14/14 实体重发）。

客户端自己跑搏鱼模拟（力度条、摆头、钩保持掷骰）：它已判定脱钩并播放了
脱钩动画（力度条瞬间清空），2 秒后同鱼以新实体再次咬钩。玩家视角是
「摆头 → 力度条空 → 脱钩 → 又上鱼」，进度重置，循环最多 3 轮后鱼照样跑掉——
保护最终没有保住鱼（第 3 次脱钩服务端放行），反而让玩家完全无法体验一次
完整的搏鱼。2026-11-12 gameserver.log 实证：欧洲六须鲇（w_catfish）
4 次遭遇、10 次发鱼、0 次结算。

具体事故：玩家搏鱼体验被服务端事后改判打断；救援本身必败（前 2 次重推
只是延迟同样的结局）。

现有机制为什么挡不住：救援分支在 `handlers_game.go` 的真脱钩分支之前
拦截，把客户端已终局的搏鱼强行复活；没有任何下游机制能纠正它。

## Solution

删除救援分支（A 族）及其专用状态 `LuckRepush`，让蓝冠天命鱼的中钩脱钩
与普通鱼走同一条真脱钩路径：计 escapes、记脱钩补偿（`recordHuntEscape`：
保留发窝度 + 提高蓝冠权重 + 巨物窗口 pity）、播报脱钩、结算渔轮磨损、
重新排下一口。

咬口侧保障（B 族）原样保留：蓝冠天命鱼中钩确认时仍跳过钩型保持率掷骰
（`HookHoldP = 1`，`hookRetentionFails` 对 `LuckProtected` 短路）、
嘬饵否决不适用于它（`hookNibbleEnabled && !LuckProtected`）、
该标记随待结算鱼快照持久化。即：蓝冠上钩更稳（官方「天命」语义），
但一旦客户端判脱钩就真脱钩，走补偿链。

补偿链文档（`docs/gameplay.md`：脱钩保留发窝度、蓝冠脱钩提高权重、
连续两次大奖杯/蓝冠脱钩后巨物窗口 pity）不动——它本来就是为
「脱钩会发生」设计的。

## User Stories

1. 作为玩家，我中了一条蓝冠天命鱼并脱钩，希望它像普通鱼一样真脱钩并获得
   脱钩补偿，以便继续正常钓鱼（而不是同一尾鱼反复「复活」打断我）。
2. 作为玩家，我希望蓝冠天命鱼上钩后仍然更稳（保持率掷骰跳过、嘬饵否决
   不适用），保留「天命鱼珍贵且上钩稳」的官方体感。
3. 边界：f0=7 终态帧行为不变（本来就不重推）；非蓝冠鱼行为不变；
   蓝冠嘬饵/中钩确认路径不变；旧档中持久化的 `LuckProtected` 标记继续
   正常解码并被 B 族使用（字段保留，无持久化兼容问题）。

## Implementation Decisions

- `internal/gameserver/handlers_game.go`：
  - 删除 14/12 里的救援分支（`f0 ∈ {1,2,8} && Hooked && Pending.LuckProtected &&
    LuckRepush < 2` → 重推）；其后既有的真脱钩分支（已中鱼 → 播报模板 7 +
    escapes + 补偿；未中鱼 → 官方静默）自然接管蓝冠鱼。
  - 删除鱼离场清理块里的 `st.LuckRepush = 0`。
- `internal/gameserver/fishing.go`：删除 `sessionFishing` 的 `LuckRepush`
  字段与抛竿重置块里的清零。
- `internal/gameserver/fish_hunt.go` / `fishing_persist.go`：不动
  （B 族：`LuckProtected` 判定、`HookHoldP = 1`、`hookRetentionFails`
  短路、快照持久化全保留）。
- `docs/gameplay.md`：从「始终生效」清单里去掉「蓝冠天命保护」表述，
  改为只描述保持率侧保障；补偿链段落不变。
- 命令/字段/迁移：无协议变化、无表结构变化、无新增 tunable。

## Testing Decisions

- 测试层：行为层（`newRigTestServer` 既有接缝），文件
  `internal/gameserver/fish_hunt_test.go`。
- 改写 c14b5bf 引入的两个救援断言（A 族）：
  1. `TestLuckProtectedBlueRejectsEscapeFrame` →
     `TestLuckProtectedBlueEscapesLikeNormalFish`：中钩蓝冠收到 f0=2 后
     断言真脱钩——`Pending` 清空、`Hooked` 为假、咬钩 timer 重排
     （搏鱼继续）、鱼猎补偿记账 `CompFish == 3`。
  2. `TestLuckProtectedBlueDoesNotRetryTerminalFrame` →
     `TestLuckProtectedBlueTerminalFrameStillFinal`：f0=7 终态不重推的
     断言保留（语义从「保护不碰终态」变为「保护已删除、终态仍终态」）。
- 保留 `TestLuckProtectedBlueBypassesHookQuality`（B 族：保持率掷骰跳过）
  与 `TestHookBiteStatePersistsWithPendingFish`（持久化）原样。
- 回滚后：用例 1 变红（回滚代码会拦截 f0=2 并重推，`Pending` 不清空）。
- 不重推本身无需新测试证明「不复活」：用例 1 的真脱钩断言即排他。

## Out of Scope

- 不动补偿链参数（回访补偿 3 尾、蓝冠权重提升、巨物窗口 pity 的数值）。
- 不动嘬饵机制与 `hook_nibble_enabled` 语义（B 族豁免保留）。
- 不动 f0=3/5/6 拒收/回执路径（`PendingRepush` 通用重推与蓝冠无关）。
- 不重写 `docs/analysis-pr304-pr305-fish-hunt-spawn-2026-11-09.md` 的
  历史记载（它记录当时的设计，含救援；本 spec 是后续决定）。

## Further Notes

- `[事实]` 2026-11-12 gameserver.log：w_catfish（欧洲六须鲇）多轮
  「14/12 f0=2 脱钩 → 蓝冠天命保护重推 → 14/14 重发」，10 次发鱼 0 结算。
- `[事实]` 救援必败：`LuckRepush < 2` 用尽后第 3 次脱钩照常放行，
  装备不匹配的蓝冠鱼本来就遛不上来。
- `[假设]` 删除后玩家对蓝冠脱钩的挫败由补偿链承接（保留发窝度 + 蓝冠
  权重提升 + pity），与普通奖杯脱钩同等待遇——这是补偿链设计时的原始假设。
