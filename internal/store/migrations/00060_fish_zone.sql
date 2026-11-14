-- 区域固定鱼种名册：管理后台在地图上划区域（内陆按 40m 世界网格矩形，
-- 挪威海按声呐海图区号），指定区内固定出什么鱼。判鱼时在硬性鱼口门之后、
-- 饵料规则之前生效：独占区只出名单内鱼种；增益区名单内 ×boost、名单外
-- ×keep_others（沿用挪威海钓点观测语义）。没有区域记录时不限制，
-- 回落地图名册——不改变未配置地图的现有手感。
CREATE TABLE IF NOT EXISTS fish_zone(
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    loc         TEXT    NOT NULL,           -- 统计名册 loc（loc_alias.loc）
    name        TEXT    NOT NULL DEFAULT '',
    kind        TEXT    NOT NULL,           -- 'grid_rect' | 'sea_zone'
    sea_zone    INTEGER,                    -- kind='sea_zone' 时的海图区号
    gx0         INTEGER NOT NULL DEFAULT 0, -- kind='grid_rect' 时的 40m 网格
    gz0         INTEGER NOT NULL DEFAULT 0, -- 矩形（含端点），gx=floor(x/40)
    gx1         INTEGER NOT NULL DEFAULT 0,
    gz1         INTEGER NOT NULL DEFAULT 0,
    mode        TEXT    NOT NULL DEFAULT 'exclusive', -- 'exclusive' | 'boost'
    boost       REAL    NOT NULL DEFAULT 3.0,  -- boost 模式名单内乘数
    keep_others REAL    NOT NULL DEFAULT 0.35, -- boost 模式名单外保留系数
    enabled     INTEGER NOT NULL DEFAULT 1,
    updated_by  TEXT    NOT NULL DEFAULT '',
    updated_ts  REAL    NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_fish_zone_loc ON fish_zone(loc);

CREATE TABLE IF NOT EXISTS fish_zone_fish(
    zone_id  INTEGER NOT NULL REFERENCES fish_zone(id) ON DELETE CASCADE,
    fish_key TEXT    NOT NULL,
    weight   REAL    NOT NULL DEFAULT 1,
    enabled  INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY(zone_id, fish_key)
);
