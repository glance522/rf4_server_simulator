# 配方模板持久化：9/6 保存、9/5 列出、9/7 删除

Status: done

## Problem Statement

玩家在制作页填好材料、点【保存】、命名并点【确定】后，界面弹出「创建了 xxx 模板」，
但配方列表里始终没有这条模板，无法点进去直接复现配方。切页面/重登后彻底消失。

现在 9/5 `crafting/tmplts` 恒返回 `args(97)` 空列表（`TRACE 回 cmd=9/5 hex=030239370000`），
9/6 只把模板回给客户端一次、不落库，9/7 `crafting/deltmplt` 没有实现。
模板在服务端不存在，因此「保存成功」只是客户端内存里的一次性结果。

## Solution

模板按玩家落库。保存时写一行（同名覆盖），配方列表按玩家读出全部模板并作为 171 数组下发，
删除时按 GUID 删除本人模板。重登后模板仍在，点模板能带着原配方与材料进入制作页。

## User Stories

1. 作为玩家，我保存模板后立刻能在配方列表看到它，并点进去直接制作。
2. 作为玩家，重启游戏/重登后我的模板还在。
3. 作为玩家，用同名再保存一次时覆盖旧模板，不产生两条同名记录。
4. 作为玩家，我在客户端删除模板后列表不再出现它。
5. 边界：别的玩家的模板永远不会出现在我的列表里。
6. 边界：模板引用的配方被静态库移除时，列表跳过该条而不是崩掉整个列表。

## Implementation Decisions

- 存储：新增迁移 `00056_craft_templates.sql`，表 `craft_template`：
  - 主键 `(uid, guid)`；`guid` 是模板实例 id（客户端保存时下发的 171.f0）。
  - 列：`uid`、`guid`、`recipe_id`、`name`、`kind`、`tools_json`、`materials_json`、`created_ts`、`updated_ts`。
  - `(uid, recipe_id, name)` 唯一索引支撑「同名覆盖」；`uid` 索引支撑列表查询。
  - 幂等：`ON CONFLICT(uid, guid) DO UPDATE` + 同名先删后写在同一事务内。
- 仓储：`internal/store/repos_craft_templates.go`，入口 `SaveCraftTemplate` /
  `ListCraftTemplates` / `DeleteCraftTemplate`（全部带 uid 约束，天然隔离）。
- 命令：
  - 9/6 `hCraftAddTemplate`：构造 171 后落库再回包；GUID 沿用请求下发的模板 id，
    没有则按 uuid5(uid+recipe+name) 派生，保证同一次保存可重放。
  - 9/5 `hCraftTemplates`：从「硬编码空 args(97)」改为读库，返回 `args(97)` 的 171 列表。
  - 9/7 `hCraftDeleteTemplate`：按请求里的 GUID 删除本人模板，响应 null。
- 形状不变：171 的字段与 args code 沿用 `craft-template-9-6-shape` 的结论
  （f3/f4 元素 170、容器 96；列表容器 97）。
- 兼容：客户端基线 `4.0.25034`；旧库升级只建表，不改任何既有表。回滚为删除该表并恢复
  9/5 空列表实现。

## Testing Decisions

- 存储层（真实 SQLite，迁移后）：同名覆盖只留一行、按 uid 隔离、删除只影响本人。
- 协议层（`internal/gameserver`）：9/6 保存后 9/5 能读出 1 条 171、容器 code 为 97；
  9/7 删除后 9/5 变空；重登（新 Session，同 uid）后 9/5 仍有该模板。
- 期望值来源：9/5 的容器 code 97 来自 `argcode_table.json` 的 `by_cmd["9/5"]`；
  171 形状来自 `craft-template-9-6-shape` 的实机报文。
- 回滚后变红：模板持久化三组的全部断言。

## Out of Scope

- 不做模板跨账号共享/导出。
- 不改客户端命名弹框与列表 UI。
- 不实现模板重命名命令。

## Further Notes

- `[事实]` 2026-11-11 实机 9/5 请求/响应：收 `03033532340000`，回 `030239370000`（空 args(97)）。
- `[事实]` 2026-11-11 实机 9/6 响应 `01ab000000…0302393606…`：171 + f3/f4 均 args(96)、元素 170。
- `[事实]` `internal/gameserver/assets/argcode_table.json` 的 `by_cmd["9/5"]="97"`、
  `by_type_id["171"]="97"`，9/5 的 171 列表容器就是 97。
- `[事实]` `internal/gameserver/timeline.go` 的 `hCraftTemplates` 注释即写明「返回空的
  args(97) 配方模板」，`items_probe_test.go` 把空列表钉成了期望值。
