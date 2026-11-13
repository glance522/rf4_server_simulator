-- 精力上限口径修正：0.85 是「自然恢复封顶」而不是硬上限，硬上限应为 1.0。
--
-- 旧版本 conditionParams 的默认 EnergyMax 取的是 store.DefaultPlayerConditionPrimary
-- （0.85）。若某个部署曾通过管理后台或早期版本把 condition_energy_max 显式写成
-- '0.85'，tunable 读取会优先取已存在的行，新的代码默认值 1.0 不会覆盖它，
-- 该部署升级后精力仍然卡在 85%、咖啡/可可等回精力消耗品继续零效果。
--
-- 只改这一个历史遗留值：其它非 '0.85' 的取值视为维护者主动标定，原样保留。
-- 幂等：重复执行只是把同一个值再写一遍。
UPDATE tunables SET value='1.0', updated_by='migration', updated_ts=0
 WHERE key='condition_energy_max' AND value='0.85';
