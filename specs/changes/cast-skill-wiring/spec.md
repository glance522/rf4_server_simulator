# cast-skill-wiring：投距技能真实生效 + 抛投状态系数回归体力

Status: done

## Problem

1. **技能点了个寂寞**：玩家学满 cat2/701（纺车轮投距 +5%/级）配鼓轮 SG80 + 枪柄竿
   CST-M88MH，实测抛距无变化。三层原因叠加：
   - 官方语义上 701 只认**纺车轮**（`Stationärrolle`）；鼓轮的正确节点是 709（30% 解锁），
     枪柄竿是 708（30% 解锁）——玩家一个都没学（证据见
     `docs/cast-skill-official-effects.md`）。
   - 服务端 `castSkillStats` 把节点效果硬编码为 0（5c4db56 有意清零，决策记录在
     `docs/equipped-fishing-skills.md`「尚未确认含义的节点不统一增加鱼口、鱼重或投距」）。
   - `skill_fish_effect_def`（00038）全是 `local-sim` 自造鱼侧行，把 701 映射成
     ecology_interest 之类的鱼口效果，与官方文案矛盾。
2. **饥饿扣抛距**：`castPlayerStateFactor` 用 `0.55*(精力/0.85)+0.45*(饱食/0.9946)`
   加权（本地提交 fb1d97f 裸标定，无官服抓包依据）。用户饿着（0.384）白丢 28% 射程。
   用户裁决：抛距应由**体力（精力）**决定。

## Solution

### 官方效果注册表（迁移 00060 + ensureCastSkillEffects）

- 新表 `skill_cast_effect_def(cat_id, node_id, effect_key, per_level_pct, gate_kind, gate_type,
  source, confidence)`。种子走运行时 ensure（与 00038 同范式：建表在迁移、种子在运行时），
  25 行全部为**客户端本地化逐字提取**的官方数据（`source='client-loc-4.0.25034'`,
  confidence 0.9），不再按 ordinal 取模编造。
- 竿侧行 15 条（gate rod，+5%/级）、轮侧行 10 条（gate reel，+5%/级）。
  完整对照表见 `docs/cast-skill-official-effects.md`。
- 305/317（诱饵笼 +10%/级）不种（独立装备通道）。

### 接线（cast_distance_inputs.go / cast_distance.go）

- `castSkillStats(ctx, uid, rigType, rodType, reelType, gears...)` 第三返回值改为
  `castPct`（官方加成百分比和）。`learnedCastSkillPct`：
  - 行 gate 匹配：rod 行要求 `rodType==gate_type`，reel 行要求 `reelType==gate_type`；
  - **轮侧跨钓法大类生效**（官方「只需学一次」）：不按当前钓法大类过滤，等级共享由
    `SkillIdentity.Project`（skill_link + group_id）投影；
  - 同一 gate 槽位（kind×type）只取最高一行贡献：兄弟行（传统轮轻型/重型 718/721/
    1008/1020 同为 fopbhlmkcpe 4）在装备侧不可区分，官服同时只激活当前轮对应那行；
  - 等级按 `skill_def.max_level` 封顶。
- `castSpecialSkillFactor(castPct) = 1 + castPct/100`（删除 0.002/8% 旧标定壳子）；
  `SkillNodeEffect` 更名 `SkillCastPct`，日志键 `skill_node_effect`→`skill_cast_pct`。
- `castDistanceForRig` 调整顺序：`castReelSpoolStats`（产出 reelType）先于
  `castSkillStats`。

### 共享等级跨 gate 的语义（实现中发现，官方数据模型自证）

708/702 同 cat2 组 1、709/717 同 cat2 组 2（官方文案明文「此能力会随着水滴轮使用的
能力一起提高」）。等级投影后**共享等级覆盖组内全部节点 gate**：学 708 → 竿型 1/2 都
命中（各只算一次，分属不同槽位）。这是官方共享等级的本来语义，不是缺陷。

### 抛投状态系数：体力唯一

```go
func castPlayerStateFactor(primary float64) float64 {
    return clampFloat(primary/defaultPlayerConditionPrimary, 0.70, 1.03)
}
```

- 删除 0.55/0.45 双状态加权；饱食度保留在 profile/日志（仍影响恢复与健康）。
- clamp [0.70, 1.03] 维持（标定值，见 player-condition-core spec）。

### 鱼侧行清理

`ensureCastSkillEffects` 在 attachDB 中紧随 `ensureFishSkillEffects` 调用（顺序约束：
先种后删，DELETE 才能站稳），删除上表 25 节点在 `skill_fish_effect_def` 的自造鱼侧行。
官方文案含鱼侧语义的节点（301/502/504/506/508/510 等）不动。

## Testing

- `TestCastPlayerStateFactorZeroStateUsesFloor`：精力 0→0.70、满→1.0（饱食不参与）、
  1.0→1.03 钳、0.60→0.7059 比例。
- `TestCastSkillPctMatchesEquippedGearGate`：gate 命中/不命中、708/702 与 709/717 共享
  等级跨 gate、514 跨大类一次生效、兄弟传统轮行槽位取最高、超 max_level 封顶。
- `TestCastSkillComboRodAndReelMultiplies`：708L7+709L5 → castPct=60 → special_skill=1.60。
- `TestEnsureCastSkillEffectsIdempotentAndPurgesFishRows`：幂等、种子行数、鱼侧行
  回插后再删干净。
- `TestSkillFishEffectsAreTableDrivenAndSharedGroupsCountOnce`：重写为生产顺序
  （鱼口 ensure → 抛投 ensure → 鱼口回插 → 抛投再删），共享组夹具改用 515/516
  （官方鱼侧语义节点）。
- `TestEffectiveFishingSkillsUseOnlyCurrentOwnedRod` / `TestSharedSkillEffectsCountOneGroup`
  同步新签名与夹具。

## Out of Scope

- 「鱼的可控性 +3%/级」「搏鱼能量消耗 -5%/级」：搏鱼相位机制，另立 spec。
- 诱饵笼/弹弓/眼镜蛇抛投通道（305/317/530/330/531/331）。
- `skill_fish_effect_def` 其余自造行的全面对齐（官方含鱼侧语义节点需逐节点设计，二期）。
- 服务端强制落点：14/2 仍采信客户端 `requestedM`，服务端账本只做诊断与审计。
  客户端模型是否本地应用技能由用户实机实验判定（学 708+709 后对比 requested_m）；
  若客户端不加成，二期再找客户端距离输入通道做服务端权威化。
