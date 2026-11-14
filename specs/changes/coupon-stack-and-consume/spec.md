# 梦鱼优惠券：一券一实例 + 标本制作按次消耗

Status: done

## Problem Statement

玩家收到补偿邮件 `coupon_chuchelo × 999`（`audit_log id=224` 实锤），但领取后背包里
只有 1 张、且不显示数量；用这张券做了三次标本（22:39:59 / 22:40:35 / 22:45:45 三条 9/1
入库日志，对应 txn id=2804/2805/2806），券仍是 1 张，没有扣减。实机截图确认券详情页无数量行、
tooltip「参数/没有参数」。

根因（RCA 5-Why）：

1. `countableCategories` 缺 cat 74 → 券被当「非计件」，邮件领取 999 被 `CreateItem`
   截成 `qty=1`，数量槽不写。
2. 券实例类 `ahgeldclmdj/13018` 的权威数量槽是 meta 基类的 `dnmfciolgep`（idx 11），
   **没有** `ljnnpokapig`；`itemQtyFieldIdx` 对非 `metaQtyFamily` 类只找 `ljnnpokapig`
   → 返回 -1，数量永远写不进 wire。
3. 关键转折：即使把 996 正确写进 `dnmfciolgep`，实机背包**仍不显示数量**。
   逐字段核对 `internal/wiregen/tables.go:1225-1239`（`ahgeldclmdj` 13 字段）与
   `:2803-2819`（定义类 `fpcfjngdcho` 15 字段）：两者都没有 UI 数量槽
   `ljnnpokapig`；票券族 cat39/55/74/76 全体不带该槽；对照能显示数量的可堆叠类
   （`nacllfpgomk`/142、`aafdnnmleob`/128、`bpmmimcmpof`/721）都两槽齐全。
   `internal/gameserver/csnames.go:15` 把 `ljnnpokapig` 定义为「数量（可堆叠件）」，
   `specs/changes/bundle-absolute-qty/spec.md:37` 的客户端 RVA 事实也是读定义对象的
   `ljnnpokapig`。→ **靠服务端写字段永远显示不出张数，必须换承载方式。**
4. 静态库 52 条标本配方 `materials_json=[]` → `hCraft` 不进任何扣料分支；客户端
   9/1 请求帧实证带 `mat_coupon_chuchelo` 槽 + 券 GUID + `coupon_chuchelo` 鱼种标记，
   服务端视而不见。券面文案「一次订单免费券」+ 材料区「必要:否」说明**有券则每次
   消耗 1 张**，非阻断性材料。
5. 存量补偿迁移若按「发放量 − 制作次数」精确收敛，会把运营手动调整的数量拉回
   996，和用户验收（4 张）直接冲突——迁移职责必须与账目解耦。

## Solution

用户在「A 保持现状 + 如实标注」与「B 一张券一个实例」之间**选择 B**（m01799）。
客户端按 GUID 逐件渲染、背包容量按行计数（`hItemCount` 用 `CountItemsByContainers`）；
实测插入第二行券后重启登录，背包显示两张独立券卡、容量 436→437（用户截图确认）。

- 券从 `countableCategories` 移除；新增 `const couponCategory = 74`。
  `CreateItem` / `prepareNewItem` 对券固定写数量槽 1；`CreateItem` 对 `units>1`
  直接报错，杜绝再次把 999 压成 1 张。
- 发放入口统一走 `grantItemUnits`：券用 `prepareCouponRows` 按张数循环构造
  `qty` 个独立实例，`insertPreparedRows` 在一个经济事务里落库（失败整体回滚）。
  覆盖：管理指令 `grant_item`、邮件 19/3 领取（含快照/非快照两条路径，统一前置处理）、
  `AddToStack`、组合包内含物（`mail_validate.go` / `bundle_inventory.go` 天然按件建行）。
- `hCraft` 在标本制作事务内识别客户端提交的券槽，按次扣 1 张（qty=1 走
  `consumeItemQtyTx` 的删行分支），把券 GUID 加入 `touched`：券耗尽时 803.f8 带上
  GUID 让客户端撤下卡片；券是可选材料，没券/券 GUID 失效都不阻断制作。
- 数量读取统一：`rigops.go`/`craft_ops.go`/`item_repair.go`/`baitdig.go`/
  `drink_use.go`/`gear_damage.go`/`rigmeta.go` 的 `fieldIdxByCS(class, csQty)`
  → `itemQtyFieldIdx(class)`（券读 `dnmfciolgep`，其它类行为不变）。
- 存量迁移拆成两层、职责分离：
  - v1 `repairCouponGrantQuantities`（marker `migration:coupon_chuchelo_grant:v1`）
    管账目：按发放审计 + 制作流水把塌陷的余量补回（历史现场补到 996）。
  - v2 `repairCouponRowLayout`（marker `migration:coupon_chuchelo_rows:v2`）
    管布局：只把「一行 N 张」守恒展开成 N 行各 1 张，**总张数不变、不查审计账目、
    不增不删**。运营改成 4 张后重启不会被拉回 996。
  - 两者都在 `RepairItemPhysics` 启动时调用，v1 先、v2 后；已是一券一行则快速跳过。

## User Stories

1. 作为玩家，背包里有多少张梦鱼优惠券就能看到多少张独立券卡（不再依赖客户端
   不存在的数量角标）。
2. 作为玩家，每次用券做标本消耗 1 张实体券（删掉一行），做一次少一张。
3. 边界：券耗尽后再制作仍可继续（券「必要:否」），不误删其他物品、不破坏鱼护消耗。
4. 边界：运营手动调整券数量后重启，不会被历史发放审计拉回旧账目。
5. 边界：邮件领取 999 张券落 999 行，单事务提交，任一行失败整体回滚。

## Implementation Decisions

- `internal/gameserver/items.go`：`countableCategories` 删 74；新增 `couponCategory`；
  `CreateItem` 券分支 + `units>1` 护栏；新增 `prepareCouponRows` / `insertPreparedRows` /
  `grantItemUnits`；`AddToStack` 券分支走按件建行。
- `internal/gameserver/economy_tx.go`：`prepareNewItem` 券分支与 `CreateItem` 对称
  （数量槽固定 1）；`grantPreparedItemTx` 对券（非计件）永远走 `insertItemTx`，天然多行。
- `internal/gameserver/mailbox.go`：`execAdminCmd` 的 `grant_item` 改走
  `grantItemUnits` 并逐行推送 4/22；`acceptMails` 构造段统一前置处理券，按
  `att.Qty` 循环建行（不走快照，避免多行复用同一 GUID 撞主键）。
- `internal/gameserver/craft_ops.go`：新增 `couponChucheloItemID`、
  `isCouponMaterialSlot`、`consumeCouponForTrophyTx`；`isTrophyMount` 分支在
  `consumeMountedFishTx` 之后调用，失败 `fail`，成功且未 `touched` 时 append 并记日志。
  803 的 `f6`/`f8` 组装逻辑既有即可（券耗尽时 `GetItemByGUID` 返回 nil → `removed`）。
- `internal/gameserver/coupon_repair.go`（新文件）：v1 账目补偿 + v2 守恒展开，
  两个独立 marker；v2 失败不写完成标记、下次启动重试，已对齐玩家快速跳过。
- `internal/gameserver/item_repair.go`：`RepairItemPhysics` 中 v1 之后调用 v2。
- 静态库、表结构、协议响应形状一律不动；不引入新依赖。
- 兼容：客户端 4.0.25034；回滚 = revert 提交 + 恢复备份 db。
- 迁移：v2 首次启动时把「一行多张」守恒展开成「一券一行」，写 marker
  `migration:coupon_chuchelo_rows:v2` 后不再重复执行。

## Testing Decisions

全部落在 `internal/gameserver/coupon_consume_test.go`（真实 SQLite，
`newSkillsCraftServer` / `openRecipeTestDB`）：

- `TestGrantCouponCreatesOneRowPerVoucher`：发 5 张 → 5 行各 1 张，GUID 不重复。
- `TestCraftFishBoardConsumesOneCoupon`：3 行做一次 → 2 行，被扣 GUID 进 803.f8。
- `TestCraftFishBoardConsumesOnePerCraft`：连续两次制作 4→3→2（对齐实机验收路径）。
- `TestCraftFishBoardConsumesLastCoupon`：最后 1 张被扣 → 0 行、f8 含 GUID。
- `TestCraftFishBoardWithoutCouponStillSucceeds`：无券仍成功。
- `TestCraftFishBoardCouponSlotWithStaleGUIDIsIgnored`：失效 GUID 不阻断、不误扣。
- `TestCouponGrantRepairRestoresRemainder`：v1 999 压成 1 + 3 次制作 → 996，幂等。
- `TestCouponRowRepairExpandsSingleRow`：v2 单行 996 → 996 行各 1 张，原 GUID 保留、幂等。
- `TestCouponRowRepairExpandsQuantityConserving`：无审计记录时 1 行 4 张 → 4 行，守恒。
- `TestCouponRowRepairKeepsOperatorSetQuantity`：**关键回归**——有 999/3 的历史账目 +
  v1 marker 已写 + 运营设成 4 行，重启 v2 不改动、保持 4 张。
- `TestCouponRowRepairSkipsWhenAlreadyOnePerRow`：已是目标布局 fixed=0。
- `internal/gameserver/items_coupon_test.go`：锁 cat74→`ahgeldclmdj`/13018、
  `reclassifyCategories`、fallback 存量重分类。
- 复审回归用例（#441 审查结论）：
  - `TestCouponGrantRepairKeepsMarkerWhenMultiRow`：多行券跳过补偿时不写全局
    marker，收敛成单行后下次启动能重试补齐。
  - `TestCouponRowRepairDeletesZeroQtyRow`：`qty<1` 的脏行直接删除、不补成 1 张，
    总张数守恒且幂等。
  - `TestCreateItemRejectsZeroUnitCoupon`：`units=0` 不能白送一张券。
  - `TestRepairItemPhysicsEndToEndFromLegacyCoupon`：旧钓线兜底类存量券行走完
    v1 补量 → v2 展开 → reclassify，最终 996 行全部 `ahgeldclmdj`、数量槽为 1。
  - `TestLootBoxPushesEveryCouponRow`：礼包开出 3 张券时推 3 条 4/22 背包增量。
  - `items_coupon_test.go`：`TestReclassifyCouponFromFallback` 增补数量槽断言，
    锁死「reclassify 后 `dnmfciolgep` 不得归 0」。
- 门禁：`go build ./...` / `go vet ./...` / `go test ./... -count=1` 全绿。
  改动基线已 rebase 到 origin/main；旧基线上曾出现的
  `TestFishEcologySeedsSpeciesAndMatchesWaterLayers` 与
  `TestGroundbaitWeatherSofteningLiftsWithinBaseline` 两条失败由上游提交修复，
  不是本改动引入，也不是长期既存项。
- 回滚变红：去掉 `couponCategory` 分支、恢复 `74: true`、去掉 P1 的
  `skippedMulti` 早退、或把 `units != 1` 改回 `units > 1`，对应新用例必红。

## Out of Scope

- 标本制作的定价语义（券抵扣价格 / 无券付费）——`hCraftCost` 恒 0 是既有行为，
  本次不动，另立任务需官服实抓。
- 静态库 52 条标本配方补 `materials_json`（会改变 9/6 模板下发，风险外溢）。
- 其他票券类（cat 39/73/75/76）的计件语义，本次只修 cat 74 实证问题。
- 客户端背包角标逻辑本身：无法反编译验证（`CONTRIBUTING.md:92` 禁止），
  本方案从服务端数据布局上绕开该限制。

## Further Notes

- [事实] `audit_log id=224`：`{"item_id":"coupon_chuchelo","qty":999}`。
- [事实] 9/1 请求帧 hex 含 `mat_coupon_chuchelo` + 券 GUID + `coupon_chuchelo`。
- [事实] 实机截图：券详情无数量行；材料区梦鱼优惠券「必要:否」。
- [事实] 券面俄文 «Бесплатный купон на один заказ»（一次订单免费券）。
- [事实] `ahgeldclmdj` 的 `dnmfciolgep` 是「剩余数量」槽，与 cat76 房契实例
  `fdnngkgnmon/13041` 同 13 字段布局。
- [事实] 券实例类/定义类都无 `ljnnpokapig`；票券族 cat39/55/74/76 全体无该槽；
  对照可显示数量的类两槽齐全。→ 一行多张无法显示张数。
- [事实] 客户端按 GUID 逐件渲染：插入第二行券 + 重启登录后背包显示两张独立券卡，
  容量 436→437（用户截图确认，m01927）。
- [推断] 背包角标读定义对象的 `ljnnpokapig`：依据
  `specs/changes/bundle-absolute-qty/spec.md:37` 的 RVA 事实
  （`ahoacflhhno.ekfefhbgegl` → `def.ljnnpokapig × 条目数`），但其语义是礼包内含物
  数量，不能直接当背包堆叠角标的权威依据。本方案不依赖此推断成立：即使角标另有
  来源，「一行一张」也天然正确。
- [已验证] 部署后启动日志 `09:10:55 梦鱼券展开为一券一实例 uid=1000001
  expanded_rows=1`；玩家库券行由 1 行 4 张 → 4 行各 1 张，总张数守恒；
  `/healthz=ok`，无 `拒发响应`/`顶层合同不符`/ERROR。

## 复审修复与已知口径（#441）

按审查结论修掉的可修代码项：多行券玩家被跳过时 v1 不再写全局完成标记（保留
可见偏差、下次启动重试）；`reclassifyStoredItem` 对券类显式回写数量槽；v2 展开
时 `qty<1` 的脏行直接删除而不是补成 1 张；`CreateItem` 的券分支要求 `units==1`；
礼包与捆绑包发放改走 `grantItemUnits` 并逐行推送 4/22 增量。

已知口径与限制（**只做记录，不改代码**）：

- [事实] `trophyCraftCountSince` 统计首发后所有 `kind='craft'` 的 cat71 产物。
  券在制作里是可选材料（缺失不阻断），不带券的制作同样计入，补偿因此偏保守
  （系统性少补）。`txn` 不保存材料槽，无法从流水反推某次制作是否用了券。
- [事实] `couponGrants` 只统计 `action='mail_send'` 的审计记录；管理后台
  `grant_item` 与组合包渠道发放的券不在 v1 补偿范围内。
- [事实] v2 单次展开总张数超过 `maxBundlePreparedItems`（10000）时报错且不写
  marker，每次启动重试并告警；这是防止一次性构造过多实例的保护，不是静默吞掉。
- [事实] 每行券的读取走 `couponLayoutRows` 的逐行查询（N+1），券行数较大时
  启动期开销偏高，属低风险性能项，本轮最小 diff 未改动。

## 计件物品的数量槽口径（实机回归发现）

[事实] `RepairItemPhysics` 校正 `item.qty` 时必须读 `fields_json` 的 `ljnnpokapig`
（`csQty`），不能用 `itemQtyFieldIdx`。后者对 `metaQtyFamily` 类（`bpmmimcmpof`、
`deckldefiap`、`dpghngjmnap`、`pcngolccead`、`ahgeldclmdj` 等）返回的是
`dnmfciolgep` 槽——那是物品属性里的常量语义，不是「还剩多少件」。

实证：本轮改动的早期版本把该处换成了 `itemQtyFieldIdx`，实机启动后 13 行
`dip_op_c`（cat 58，实例类 `bpmmimcmpof`）的 `item.qty` 被统一写成 100，真实值
`[1, 95, 98, 100]` 全部丢失，`items_changed=12`。用未改动的 `origin/main` 构建跑
同一份被污染的副本，数量会收敛回真实值，据此定位到这一行。

守护用例：`TestRepairCountableQtyUsesStackSlot`。用例构造两行 `dip_op_c`
（`ljnnpokapig` 分别为 95 与 1，`dnmfciolgep` 置 100，`item.qty` 预置错误值 100），
跑 `RepairItemPhysics` 后要求 `item.qty` 收敛为 95 与 1。把该行换回
`itemQtyFieldIdx` 后用例必红（实测报「item.qty 被写成 100」）。

注意这与券的 `dnmfciolgep` 用法不冲突：券类（cat 74）的实例类 `ahgeldclmdj`
确实以 `dnmfciolgep` 为权威数量槽（它没有 `ljnnpokapig` 槽），所以券的读写走
`itemQtyFieldIdx` 是对的；差别只在「计件物品改 `item.qty`」这条路径上必须用
`csQty`。

## 部署前提：运行中的二进制必须与构建一致

本改动曾出现「代码已修、实机却仍不扣券」，根因不是扣券逻辑，而是 `bin/gameserver.exe`
被另一次构建覆盖后没有重启——跑着的进程里根本没有这段代码（该二进制内
`coupon_chuchelo` 字符串出现 0 次）。因此部署后必须复核**运行中**二进制的自报身份，
而不是只看启动日志：`/api/status` 的 `procs[].running_build` 是子进程自报的版本，
`procs[].exe_changed` 为真表示磁盘上的文件已被替换、需重启才生效。

[经验] 测试不能用自造报文。券槽的真实 id 是 `mat_coupon_chuchelo`，不是
`mat_coupon_` + item_id 拼出来的 `mat_coupon_coupon_chuchelo`——后者只因
`isCouponMaterialSlot` 认 `Species` 兜底才侥幸通过，实机坏了照样绿。因此新增
`internal/gameserver/coupon_realframe_test.go`，把一段实机 9/1 请求体硬编码为常量
直接驱动 handler，不依赖自造树。
