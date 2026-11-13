# 精力硬上限与自然恢复封顶分离

Status: done

## Problem Statement

玩家在官服看到精力条能到满（100%），本服永远停在 85%，而且喝咖啡、喝可可都不回精力。

根因是把两件事当成了同一件：

- **硬上限**：客户端状态条按 1.0 归一化画满。实机界面观察：服务端下发 f0=0.85 时，
  精力条只画到约 85%，说明客户端认为 1.0 才是满。
- **自然恢复封顶**：官服抓包 f0 静止时恒为 0.85，动作消耗后约 15 秒内
  回升到 0.85 就不再上涨。这是"自然恢复的上限"，不是"精力上限"。

本服把硬上限也设成 0.85（`condition_energy_max`），于是精力长期停在自然封顶，
所有回精力消耗品（咖啡 +0.15、可可 +0.05）都被钳回 0.85，恒为零效果。
玩家实际体验就是"喝咖啡没用，罐子还少了"。

## Solution

精力分成两个概念：硬上限 1.0，自然恢复只回到 0.85。空闲时精力稳定在 85%，
动作消耗后自动回升到 85%；最后 15% 由咖啡/可可这类消耗品补满。
消耗品在硬上限才失效，因此咖啡重新变得有意义。

## User Stories

1. 作为玩家，我希望精力条能像官服一样到 100%，而不是永远卡在 85%。
2. 作为玩家，我希望喝咖啡能把精力从 85% 补满到 100%，并且不扣罐子当它真的没用。
3. 边界：精力顶到 1.0 时再喝咖啡不消耗物品（沿用 consumable-no-op-guard 的口径）。
4. 边界：动作消耗后精力自然回升，仍然只回到 0.85，不会自己涨满——补满必须靠消耗品。

## Implementation Decisions

- `internal/gameserver/player_condition.go`：新增 tunable `condition_energy_regen_cap`（默认 0.85），
  与 `condition_energy_max`（改为默认 1.0）分离。`advancePlayerCondition` 的自然恢复
  按 `min(EnergyMax, EnergyRegenCap)` 封顶；扣减、消耗品恢复、读口钳制仍按 `EnergyMax`。
- 封的是"自然恢复能到多高"这个天花板，不是"当前值不得超过多少"：先算恢复目标
  `clamp(cur+regen, 0, naturalCap)`，再取 `max(cur, 恢复目标)`。已在封顶之上的值
  （消耗品补上去的）原样保留，不会被保活拍按回来。
- `store.DefaultPlayerConditionPrimary` 保持 0.85 不动：那是**新档初始值**（官服新号
  从 85% 起步），不是上限语义；改它会牵动迁移回填与抛投系数基线。
- 装备精力上限加成（`condition_gear_energy_max_cap`）继续叠加在硬上限上，钳到 1.0 不变。
- 升级：新增迁移 `00059_condition_energy_max.sql`，把历史遗留的 `condition_energy_max='0.85'`
  升到 `1.0`。tunable 读取优先取已存在的行，只改代码默认值不会覆盖旧的显式行；
  迁移只改这一个历史值，维护者标定的其它取值原样保留。
- 回滚：把 `condition_energy_max` 与 `condition_energy_regen_cap` 都设回 0.85
  即恢复旧行为（两者相等时自然恢复可达上限），无需改代码。

## Testing Decisions

- 单元（纯逻辑，手算）：`advancePlayerCondition` 长跨度恢复封在 `EnergyRegenCap` 而非 `EnergyMax`。
- 行为（真实 SQLite + `useAlcoholDrink` 接缝）：
  - 精力 1.0 时喝咖啡 → 落回通用 handler、物品保留；
  - 精力 0.85（自然封顶）时喝咖啡 → 生效并扣物、精力到 1.0（手算 0.85+0.15 钳到 1.0）。
- 期望值来自官服抓包结论（f0 静止恒 0.85）与实机界面观察（0.85 只画约 85%），不用被测代码回算。
- 回滚后变红：`TestCoffeeFillsBeyondNaturalRegenCap` 与
  `TestAdvancePlayerConditionClampsAndCaps` 会红。

## Out of Scope

- 精力自然恢复速率 `condition_energy_regen_per_hour`=100/h 是否过快（标定问题，
  但它只影响回到 0.85 的速度，不再让消耗品失效）。
- 装备精力上限加成的具体数值标定（官方数据未采集，机制默认零效果）。
- `store.DefaultPlayerConditionPrimary` 新档初始值是否要改。
