# 限制船票只能在其购买地图使用

Status: done

## Problem Statement

玩家在 A 地图码头购买的船票，切到 B 地图后仍能在 B 地图码头验票上船。

票定义里的站点枚举在各张地图之间重复，而验票只比较站点号，所以同一张票
在任意地图都能通过。结果是"在哪个码头买票"失去意义，玩家也不必在目标
地图的码头购票。

## Solution

船票只在其购买地图的码头可用。持有 A 地图的票走到 B 地图码头时，验票按
"没有可用票"处理，客户端弹出选票框；玩家需要在 B 地图码头重新购票。已经
激活但属于其它地图的票不再被复用。

## User Stories

1. 作为玩家，我在阿赫图巴买的票只能在阿赫图巴的码头使用，以便上船资格和购买地点一致。
2. 边界：在 B 地图码头验票、背包里只有 A 地图的票时，看到"没有可用票"的选票框，而不是直接上船。
3. 边界：有效期内的跨地图票不被复用，也不会因为被拒而写入租用状态，回到原地图后仍可正常使用。
4. 边界：票定义缺少地图字段时保持放行，避免旧数据或异常定义被误拒。

## Implementation Decisions

- `internal/gameserver/boat.go`：新增 `boatTicketLocationAttr`（`dhaenhabecc`，票定义中的
  购买地图 key），以及 `ticketLevelOf` / `ticketLevelOK` 两个辅助函数。
- `internal/gameserver/boat.go`：`3/7` 的三条判定路径都加入地图校验——带 GUID 的验票、
  按站点复用有效票、按站点选票。
- `internal/gameserver/boat.go`：带 GUID 的验票在站点或地图不符时改为拒绝并返回 null，
  此前站点不符只记录告警后继续放行。
- 响应形状、`args code`、字段语义、数据库表与迁移均未修改；客户端基线仍为 `4.0.25034`。
- 地图比对使用 `currentLevel(uid)`，其返回值与票定义中的 `level_*` key 同域。
- 回滚方式：revert 对应提交，验票恢复为只按站点判定。

## Testing Decisions

- 协议/行为单元测试：新增 `TestCheckTicketRejectsCrossLevelTicket`。用阿赫图巴票
  （站点 2、`level_009_ahtuba`）在出生地图发起验票，断言返回 null 且票的激活字段仍为 0；
  再把玩家所在地图切到 `level_009_ahtuba`，断言同一张票验票通过并返回 `type_id=162`。
- 期望值来源：站点与地图字段取自静态库中的票定义；地图 key 是否有效由
  `internal/gameserver/assets/map_defs.json` 决定，不依赖被测代码再算一遍。
- 既有回归：`TestCheckTicket`、`TestActiveRentalTicketRepairsLegacyWorldAnchor`、
  `TestStartTicketRentIgnoresRentMinutesTunable` 保持通过。
- 实机回归：跨地图验票时日志出现「票不属于当前地图，拒绝上船」，客户端弹出选票框。
- 回滚后跨地图用例重新通过，即旧行为复现。

## Out of Scope

- 船票到期弹窗，以及背包内没有新票时送回出生点。
- 船票价格、站点配置、租期定义与商店上架规则。
- 船票剩余时间递减推送（`pushTicketRemaining` 目前没有调用点）。

## Further Notes

- `[事实]` 静态库中 77 张船票全部带 `dhaenhabecc` 字段，覆盖 11 张地图。
- `[事实]` 这 11 个地图 key 均存在于 `internal/gameserver/assets/map_defs.json`，
  不会出现票指向不可达地图而永久无法使用的情况。
- `[事实]` 站点枚举在 11 张地图上重复：站点 1 出现在全部 11 张地图，站点 2 出现在 7 张。
- `[未验证]` 客户端收到 null 后除选票框之外还有哪些提示，尚未实机确认。
