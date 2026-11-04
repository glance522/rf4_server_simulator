-- 为旧存档保留等级基线与历史回执结构。
CREATE TABLE IF NOT EXISTS level_reward_state(
    uid              INTEGER PRIMARY KEY,
    baseline_level   INTEGER NOT NULL CHECK(baseline_level BETWEEN 1 AND 58),
    high_water_level INTEGER NOT NULL CHECK(high_water_level BETWEEN baseline_level AND 58),
    created_ts       REAL NOT NULL,
    updated_ts       REAL NOT NULL
);
INSERT OR IGNORE INTO level_reward_state(uid,baseline_level,high_water_level,created_ts,updated_ts)
SELECT uid,MAX(1,MIN(level,58)),MAX(1,MIN(level,58)),
       CAST(strftime('%s','now') AS REAL),CAST(strftime('%s','now') AS REAL)
FROM profile;

CREATE TABLE IF NOT EXISTS level_reward_receipt(
    uid        INTEGER NOT NULL,
    level      INTEGER NOT NULL CHECK(level BETWEEN 2 AND 58),
    gold       INTEGER NOT NULL CHECK(gold>=0),
    mail_id    INTEGER,
    created_ts REAL NOT NULL,
    PRIMARY KEY(uid,level)
);
