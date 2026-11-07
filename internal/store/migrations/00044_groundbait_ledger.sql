-- 窝料系统 v1.1：逐批投放账本 + 玩家游戏钟（specs/changes/groundbait-system/spec.md）。
-- 份数就是时效：1 份 = 0.6 游戏小时，10 份 = 6 游戏小时；净窝份数超过 15 份开始压开口率。
-- 窝点不另存聚合行：某个格子的状态由本表未过期的批次即时汇总，
-- 免得聚合与明细两处写数据对不上；重启后也从这里恢复。
CREATE TABLE IF NOT EXISTS groundbait_deposit(
    id           INTEGER PRIMARY KEY,
    uid          INTEGER NOT NULL,
    loc          TEXT    NOT NULL,
    bx           INTEGER NOT NULL,            -- 落点 5 米网格桶
    bz           INTEGER NOT NULL,
    has_pos      INTEGER NOT NULL DEFAULT 1,  -- 0=拿不到落点坐标，退化成整点一格
    channel      TEXT    NOT NULL,            -- hand/spod/feeder/pva/catapult/tube
    item_id      TEXT    NOT NULL,
    portions     REAL    NOT NULL,            -- 投放份数（初始）
    units_left   REAL    NOT NULL,            -- 咬口/脱钩扣减后的剩余份数
    hours0       REAL    NOT NULL,            -- 该批初始窝时（游戏小时）
    deposit_hour REAL    NOT NULL,            -- 投放时该玩家的在线游戏钟读数
    txn_guid     TEXT    NOT NULL UNIQUE,     -- 幂等键：重复上报不会重复投放
    created_ts   REAL    NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS groundbait_deposit_cell ON groundbait_deposit(uid, loc, bx, bz);

-- 玩家游戏钟：real_ts 是上次结算的真实时刻，game_hours 是累计在线游戏小时。
-- 只有服务端被请求碰到时才推进，且单次跨度封顶 30 秒，所以离线时间不扣窝时。
CREATE TABLE IF NOT EXISTS groundbait_clock(
    uid        INTEGER PRIMARY KEY,
    real_ts    REAL NOT NULL,
    game_hours REAL NOT NULL,
    updated_ts REAL NOT NULL
) WITHOUT ROWID;
