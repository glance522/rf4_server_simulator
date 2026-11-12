-- 工坊"银币修理"的排队任务表（我方独占号段 9100007）。
--
-- 为什么需要这张表：官服口径（用户手册·工作坊一节 + 2026-11-11 实机截图）是
-- 「银币修理要等，金币修理即时」。等待期间物品留在工坊，修好后玩家自己取回。
-- 我们此前是收钱立刻把耐久写满、立刻回更新后的树——玩家看到的「2 小时」
-- 纯粹是界面文案（hpopgbjohpk.f3 硬编码 2），服务端根本没有任何等待。
--
-- 设计取舍：
--   * 只存"到期时间 + 修理完成后的字段快照"，不存中间态。到期时一次性
--     应用快照即可，不需要重放 21/3 的计价逻辑（价格当时已扣过）。
--   * guid 做主键：同一件物品同时只允许一个在办任务。客户端会连发两次
--     相同 21/3（2026-11-11 15:26 实测），第二次必须命中同一行，否则会
--     排两次队、扣两次钱。
--   * due_ts 用 unix 秒（REAL）：离线时间照常流逝，登录后惰性结算即可，
--     不需要为了让计时继续而常驻定时器。
CREATE TABLE IF NOT EXISTS workshop_repair_job(
    guid         TEXT PRIMARY KEY,  -- 被修理物品的实例 guid（item.guid）
    uid          INTEGER NOT NULL,  -- 玩家 uid，用于按人扫描到期任务
    due_ts       REAL NOT NULL,     -- 到期 unix 秒；到期后惰性应用快照
    fields_json  TEXT NOT NULL,     -- 修理完成后的 fields_json 快照
    durability   REAL NOT NULL,     -- 修理完成后的本体耐久
    silver_paid  INTEGER NOT NULL DEFAULT 0, -- 实付银币，用于日志与退款排查
    parts        INTEGER NOT NULL DEFAULT 0, -- 本次修理涉及零件数，仅用于日志
    created_ts   REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_workshop_repair_due ON workshop_repair_job(uid, due_ts);
