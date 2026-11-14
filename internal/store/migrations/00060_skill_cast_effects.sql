-- 抛投技能效果定义：种子数据在 gameserver 启动路径由 ensureCastSkillEffects
-- 显式写入（与 skill_fish_effect_def 的 00038 同范式：建表在迁移、种子在运行时）。
-- 数据来源是客户端 4.0.25034 本地化文案逐字提取（ability_<node_id>），不是自造标定：
-- 投距类技能官方阶梯统一 +5%/级（见 docs/cast-skill-official-effects.md）。
CREATE TABLE IF NOT EXISTS skill_cast_effect_def(
    cat_id        INTEGER NOT NULL,
    node_id       INTEGER NOT NULL,
    effect_key    TEXT NOT NULL,
    per_level_pct REAL NOT NULL,
    gate_kind     TEXT NOT NULL,   -- 'rod' | 'reel'：按当前竿/轮的 fopbhlmkcpe 匹配
    gate_type     INTEGER NOT NULL,
    source        TEXT NOT NULL DEFAULT 'client-loc',
    confidence    REAL NOT NULL DEFAULT 0.9,
    PRIMARY KEY(cat_id, node_id)
);
