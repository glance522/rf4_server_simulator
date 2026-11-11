# 修复 9/6 配方模板响应形状与配方解析

Status: done

## Problem Statement

玩家在「制作 → 干混合」页填好右侧全部材料后点【保存】，弹出模板命名框；点【确定】后
整个制作面板卡死，保存/制作按钮变灰、点不动。客户端 `Player.log` 在点击瞬间抛出：

```
System.InvalidCastException: Unable to cast object of type 'efglhjegaph' to type 'bpdjphlfjdj[]'.
  at bdgpabcfpjl.lpdfanjcalm[T]
  at dmolhiobcbb.plphdfehkjl
```

`dmolhiobcbb` 是 9/6 的响应 wire 类（type_id 171）。客户端把 171 的 f3/f4 当
`bpdjphlfjdj[]` 读，而服务端发的是 `efglhjegaph`（`kdpbhdffjaf`，type_id 17）对象，
容器 code 也用了 `18`/`61`。强转失败使保存回调抛出，面板停在 busy 态。

同时日志暴露第二个缺陷：两次 9/6 请求（玩家在配 `craft_feed_dry` 干混合）都返回了
`recipe=craft_bait_bread`。配方键靠"请求里第一个字符串"猜测，猜不中就回退到配方表
第一行，于是模板名/材料/产物全是另一条配方。

## Solution

9/6 按客户端期望的元素类返回配方模板：f3/f4 的元素是 `hbokamnohjb`（type_id 170），
容器 args code 取该元素的映射值 **96**。配方键从请求载荷显式解析，解析不到时返回
null 而不是乱发别的配方。

## User Stories

1. 作为玩家，我在制作页保存自定义模板，点【确定】后面板正常关闭/刷新，不卡死。
2. 作为玩家，我保存的模板名字、材料、产物与我当前配置的配方一致（干混合不会被写成面包）。
3. 边界：请求里没有可识别配方键时，服务端返回 null，客户端不拿到别的配方模板。
4. 边界：配方没有任何材料需求时，f3 发空数组（code 96），不发 null。

## Implementation Decisions

- 模块：`internal/gameserver` 制作域（9/6 handler 与模板构造）。
- 命令/字段：9/6 响应 171，f0 GUID、f1 名称、f2 分类、f3 输入材料、f4 产物，字段 op 不变。
  变更点是 **f3/f4 的元素类与 args code**：元素 `kdpbhdffjaf`(17)/code 18、61
  → 元素 `hbokamnohjb`(170)/code **96**（`argcode_table.json` 的 `by_type_id["170"]`）。
- 元素字段（`hbokamnohjb` ops `STR, U32, STR`，DTO `bpdjphlfjdj`）：
  f0 配方描述、f1 配方索引、f2 物品 itemID。按 JSON 键语义，f0 取需求 key，
  f2 取需求接受的物品 id。
- 配方解析：复用 `craftPayloadOf` 的 obj8 路径（f0 就是 `cemjmlpafmc` 配方 key，
  与 9/1/9/4 同源），不再用 `deepStrings` 的首串猜。解析不到 → 返回 null。
- 数据与迁移：不改表、不迁移；模板不落库（与原行为一致）。
- 兼容：客户端基线 `4.0.25034`；回滚方式为恢复 9/6 原形状（会重新卡死，仅用于对比）。

## Testing Decisions

- 协议层（`internal/gameserver`，真实 SQLite 模板库 `openRecipeTestDB`）：
  - 9/6 响应为 171，f3/f4 的 `KindArgs` code 必须等于 `listCodeForObjs` 对 type 170
    的映射值 96；
  - f3 每个元素 type_id 必是 170，且 f0 = demand_key、f2 = 接受的物品 id；
  - 干混合（`craft_feed_dry`）请求必须返回 `feed_dry` 相关模板，不能再回面包；
  - 无材料需求时 f3 为空数组（code 96、0 项），不是 null。
- 期望值来源：`argcode_table.json` 的既有映射 + 客户端强转目标类名（Player.log 栈），
  不是"用同样代码再算一遍"。
- 回滚后变红：模板元素类/args code 断言、干混合配方归属断言。

## Out of Scope

- 不实现模板持久化（9/5 列表、9/7 删除仍维持现有空列表/null 行为）。
- 不改 9/1、9/3、9/4 的既有形状。
- 不改客户端命名弹框的 UI 行为。

## Further Notes

- `[事实]` Player.log 2026-11-11 12:46:32 强转异常栈（类名 `dmolhiobcbb`、目标 `bpdjphlfjdj[]`）。
- `[事实]` `internal/wiregen/tables.go`：`dmolhiobcbb`=171/DTO `hkjgdnepdoh`，
  `hbokamnohjb`=170/DTO `bpdjphlfjdj`，`kdpbhdffjaf`=17/DTO `efglhjegaph`。
- `[事实]` `internal/gameserver/assets/argcode_table.json`：`by_type_id["170"]="96"`；
  未出现在 `by_elem`，因此走 `listCodeForObjs` 的 by_type_id 段即可得到 96。
- `[事实]` `logs/gameserver.log` 2026-11-11 10:55:19 与 12:46:32 两次 9/6 都解析成
  `craft_bait_bread`，而玩家操作的是 `craft_feed_dry`。
- `[未验证]` 官方服务端的 9/6 样本未取得；本次形状由客户端解码器强转目标反推。
