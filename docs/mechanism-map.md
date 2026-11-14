# 机制代码索引

本文只回答**"这个机制从哪个文件哪个函数进去"**，是路由表不是说明书：

- 玩法数值与机制说明 → `docs/gameplay.md` 与各机制专题
- 改这里会踩的坑 → `specs/capabilities/`
- 为什么当初这么决定 → `docs/adr/` 与 `specs/changes/<slug>/spec.md`

只写文件与入口函数，**不写行号、不复述规则**（行号会过期，规则有唯一归属）。
机制对应的一切行为以代码与测试为准；本表过期时按代码改本表。

## 钓鱼核心（`internal/gameserver`）

| 机制 | 入口文件 | 唯一入口 / 关键状态 | 测试 |
|---|---|---|---|
| 咬钩与鱼口 | `fishing.go`、`fish_hunt.go`、`handlers_game.go` | 状态根 `FishState`；定时链 `scheduleBiteState → onRodBiteTimerTicket → pushPendingBite`；咬口质量 `applyHookBiteQuality`、保持判定 `hookRetentionFails`、嘬饵否决 `finishNibbleHook` | `fish_bite_environment_test.go`、`bite_floor_test.go`、`fish_hunt_test.go` |
| 派鱼与档位 | `fishing.go`、`fish_hunt.go`、`fish_distribution.go`、`fish_roll_context.go` | 总装 `rollFishForRig → rollFishWithRules`；四档 `gradeRatioModel`、`sampleWeightByGradeRatio`、`gradeRatioGradeWeights`；鱼猎阶段 `sampleHuntWeight`；重量上限 `fishWeightCapKg` | `fishing_grade_ratio_test.go`、`fish_distribution_test.go`、`fish_hunt_test.go`、`fishing_weight_cap_test.go` |
| 饵料与食性 | `fishing_bait_rules.go`、`fishing_bait_override.go`、`hook_profiles.go` | 无单一入口：食性过滤 `filterFishBaitSet`（内嵌 `fishing_bait_rules.txt`）／覆盖与轮换 `RotationState`、`activeBaitGroup`／钩型 `hookBiteProfileFor` | `fishing_bait_rules_test.go`、`fishing_bait_override_test.go`、`hook_profiles_test.go` |
| 硬门与区域名册 | `fish_bite_gates.go`、`fish_zone.go`、`fish_roll_context.go` | 资格门 `applyBiteGates`（G1 拟饵食性硬剔 / G2 水层地板 / G-diet 双票，开关 `bite_gate_enabled`）；区域名册 `applyFishZoneRoster`（`fish_zone` 表，缓存 30s + `reload_fish_zones` 队列失效）；空候选经 `gateBlocked` 压制鱼池兜底 | `fish_bite_gates_test.go`、`fish_zone_test.go` |
| 钓组装配 | `rigops.go`、`lure_hooks.go`、`fishing_rods.go` | 唯一写入口 `rigSetComponent`（13/15/16/17 命令族）；拟饵钩位 `lureHookMount`；多竿状态 `fishingStateForRod`；投影重建 `reconcileRigProjection` | `rigops_test.go`、`rigfishing_test.go`、`rig_dup_index_test.go` |
| 载荷与断损 | `gear_damage.go`、`gear_physics.go`、`reel_fight_wear.go` | 无单一入口：断件 `breakLineOrRodWithLineOut`、`resolveGearBreak`／扣线 `lineLossTx`／承力上限 `gearWeightCapKg`、`gearWeightCapForRig`／搏鱼轮耗 `fightReelCache → settleFightReelWear` | `gear_damage_test.go`、`line_break_loss_test.go`、`reel_fight_wear_test.go` |
| 搏鱼与结算 | `handlers_game.go`、`catch_settlement.go`、`progress_ops.go` | 结算唯一入口 `settleCatchOnceWithQualification`（回执 `catchReceipt`）；入护 `fishingKeep`、放生 `fishingRelease`、收竿 `fishingEndSettle` 都收敛到它 | `catch_settlement_test.go`、`fishing_contract_test.go`、`fishing_atomic_test.go` |
| 持久化与恢复 | `fishing_persist.go`、`catch_recovery.go`、`../store/repos_fishing_state.go` | 恢复唯一入口 `resumeFishingState`；未结算鱼 `recoverCompletedCatchOnLogin` | `fishing_persist_test.go`、`catch_recovery_test.go`、`fish_bite_race_test.go` |

## 系统层

| 系统 | 入口文件 | 唯一入口 / 关键类型 | 测试 |
|---|---|---|---|
| 值编解码 | `../proto/decode.go`、`../proto/encode.go`、`../proto/value.go` | `proto.DecodeValue` / `EncodeValue`（值模型 `proto.Value`） | `../proto/golden_test.go`、`../proto/failure_test.go` |
| 分帧 / RC4 / 信封 | `../proto/frame.go`、`../proto/rc4.go`、`../proto/envelope.go` | `proto.TakeCompleteFrames`、`proto.NewRC4`（可用 `Restore` 接管状态）、`proto.ParseEnvelope` | `../proto/frame_test.go` |
| 类型表 | `../wiregen/tables.go`、`../wiregen/make.go` | `wiregen.Table()`、`wiregen.MakeObj` | `../wiregen/corpus_test.go` |
| 命令分发 | `dispatch.go`、`handlers.go` | `NewDispatch` + `Register` / `RegisterFamily`；族注册统一在 `registerHandlers`（内建覆盖 `RegisterExtra`） | `contract_guard_test.go` |
| 请求执行与回执 | `request_receipts.go`、`../store/repos_request_receipts.go` | 唯一入站口 `(*Server).dispatchRequest`；幂等键 `ClaimRequestReceipt` | `request_receipts_test.go` |
| 会话与推送 | `session.go`、`server.go` | `Session.SendEnvelope` / `Push` / `PushRaw`、`Server.openSession` | `session_sequence_test.go` |
| 网关 | `../gateway/login.go`、`../gateway/realtime.go`、`../gateway/store.go` | `NewLoginServer`、`NewRealtimeServer`、`gateway.Store` | `../gateway/integration_test.go` |
| 隧道 | `../tunnel/tunnel.go`、`../gateway/tunnelclient.go` | `tunnel.NewConn`（线路格式以文件头注释为准） | `../tunnel/tunnel_test.go` |
| 存储与迁移 | `../store/store.go`、`migrations/*.sql` | `store.Open` / `OpenWithStatic`、`(*DB).Migrate` | `../store/migration_stale_branch_test.go`、`data_integrity_test.go` |
| 静态库只读 | `../store/staticdb.go` | `OpenWithStatic` + `staticConnector`（每连接 ATTACH + 写保护） | `../store/staticdb_test.go`、`../seed/static_runtime_test.go` |
| 档案构建 | `profilebuild.go` | `BuildProfile307`、`BuildSkillTree311` | `statsbuild_test.go` |
| 等级与经验 | `progress.go`、`progress_ops.go` | 纯函数 `levelForXp` / `baseXp`；写入 `grantXpMonotonic` | `progress_test.go`、`progress_notifications_test.go` |
| 状态条（精力/饱食/健康） | `player_condition.go`、`player_condition_energy.go` | 纯函数 `advancePlayerCondition` + `currentPlayerCondition`；持久化 `UpsertPlayerCondition` | `player_condition_energy_test.go`、`player_condition_health_test.go` |
| 欢乐时光 | `happy_hour.go`、`../store/repos_happy_hour.go` | `AdvanceHappyHour` / `GetHappyHourStatus`（开关走 `tunables.happy_hour_enabled`） | `happy_hour_test.go` |
| 成就 | `achievements_rules.go`、`achievements.go` | `evaluateAchievements`（规则表 `achievementRule`） | `achievements_rules_test.go`、`achievements_push_test.go` |
| 商店与定价 | `../store/shop_catalog.go`、`../store/shop_augment.go`、`shop.go` | `ShopItems → effectiveShopShelf`（货架解析链）；协议侧 `hStoreBuy` | `../store/shop_catalog_test.go` |
| 鱼市与工坊 | `econ_market.go`、`../store/repos_market_offer.go` | `registerEconMarket`（20/x、21/x）+ 纯函数 `marketPriceCents` | `econ_market_test.go`、`../store/repos_market_offer_test.go` |
| 邮件 / 制作 / 咖啡厅 / 战利品箱 | `mailbox.go`、`craft_ops.go`、`cafe_orders.go`、`loot_box.go` | `registerMailHandlers`、`hCraft`、`hCafeSubmit`、`rollLootBox` | `mailbox_snapshot_test.go`、`craft_limits_test.go`、`cafe_orders_test.go`、`loot_box_test.go` |
| 管理端 | `../admin/api.go`、`../admin/bait_rules.go`、`../admin/hook_profiles.go` | 路由总装配 `admin.NewServer`；`handleBaitRuleSave`、`handleHookProfileSave`；tunables 读写走 `(*DB).TunableSet` | `../admin/api_test.go`、`../admin/bait_rules_test.go`、`../admin/hook_profiles_test.go` |
| 启动与监管 | `../../cmd/{launcher,gateway,gameserver,seed}/main.go`、`../supervisor/supervisor.go` | 各 `main()` 是唯一装配点；`seed.Ensure` | `../supervisor/supervisor_test.go`、`../seed/seed_test.go` |
| 游戏内查鱼 | `fish_query.go`、`fish_query_water.go`、`fish_query_regions.go` | `(*Server).QueryFish` | `fish_query_test.go`、`fish_query_water_test.go` |
| 纸地图标定与贴图名 | `../fishmaps/fishmaps.go` | `fishmaps.Scenes`（gameserver 经 `fishQueryRegionScenes` 别名引用）、`fishmaps.TextureFor` | `../fishmaps/fishmaps_test.go` |

## 结构观察（这些是查代码时最容易误判的点）

1. **`FishState` 是钓鱼会话状态根**：咬钩 timer、`Pending`/`Hooked`、持久化元数据、搏鱼轮磨损账本都挂在这一个结构上。改任一项都要按"整根状态"回归，而不是只看本机制。
2. **`RolledFish` 是跨模块契约**：派鱼写入、咬钩读取、结算消费同一尾鱼。所以"咬钩"与"结算"不是两个独立机制，而是同一鱼实体的两端。
3. **事实上的两个核心文件**：`fishing.go`（定时链 + 派鱼家族 + `FishState`/`RolledFish`）与 `rigops.go`（整个 13/15/16/17 装配族 + handler 注册）。其余多是策略插件。
4. **三处刻意没有单一入口**：饵料（规则/覆盖轮换/钩型/拟饵钩位四套）、载荷与断损（断件/承力上限/搏鱼磨损/静态磨损/修理五套）、派鱼子策略（分布/四档/鱼猎/上限四处）。硬凑一个"总入口"会失真，按子策略查。
5. **派鱼的策略装配点是 `fishRollContext`**：`rollFishWithRules` 把分布、食性、生态、天气、钩型依次乘到同一组权重上。这就是"派鱼看起来散落在四个文件"的原因。
6. **唯一入站接缝是 `(*Server).dispatchRequest`**：幂等在分发层统一实现，不在各 handler 里。新增机制要假设自己可能被回执短路或重放保护挡住。
7. **共享状态集中在 `Server` 单结构体**（会话表 + 各类 TTL 缓存 + 多个互斥锁），`NewServer` 是唯一构造点，测试也走它；这是测试最大的隐式耦合面。
8. **`tunables` 是运维与玩法之间唯一的热更通道**：管理端写、store 存、gameserver 带 TTL 读。要验证热更不要起整套进程，直接打 `(*DB).TunableSet`。
9. **管理的两条边界**：`admin` 只持 `*store.DB`，与 gameserver 无直接调用（踢人/封禁经 DB 指令队列，gameserver 轮询执行）。
10. **不存在名为"装配指纹"的实现**：一致性靠 f5 投影单向重建（`reconcileRigProjection` / `BackfillRigProjections`）+ `rigCompKey(category, ordinal)` 二维键。搜索 `fingerprint` 找不到东西是正常的。

## 维护

- 机制入口改名或拆分时，同一次改动里改本表对应行（`docs/agents/domain.md` 对账表已登记）。
- 新增机制：先在本表加一行，再决定它的行为说明落在 `docs/gameplay.md` 还是 `docs/<mechanism>.md`。
- 本表**不带行号**、**不复制规则**；如果某行需要解释"为什么"，那是能力契约或 ADR 的事。
