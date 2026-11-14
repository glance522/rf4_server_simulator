-- rf4records.com 周纪录捕获表：外部证据源（官方周纪录聚合站，robots 允许、免费公开）。
-- 每行 = 官方周纪录榜上的一条纪录（玩家用某饵钓到某鱼）。用途：食性判定的外部证据，
-- 与 companion_bait_fish（同口径的预置快照）互补；本表由 tools/fetch_rf4records.py 增量抓取。
-- 不存玩家名：统计用途不需要，减少个人数据留存。
-- 迁移器按整文件执行，不解析 Down 段；不要添加 -- +goose Down。
CREATE TABLE IF NOT EXISTS rf4records_catch(
    fish_name_en TEXT NOT NULL,           -- 源站英文名（原始事实，未映射也保留）
    fish_key   TEXT NOT NULL DEFAULT '',  -- 映射后的本地 fish_key，未映射为空
    bait_name  TEXT NOT NULL,             -- 源站 bait_display（原始事实）
    bait_id    TEXT NOT NULL DEFAULT '',  -- 映射到 item_def.item_id，未匹配为空
    weight_g   INTEGER,
    waterbody  TEXT NOT NULL DEFAULT '',
    region     TEXT NOT NULL DEFAULT '',
    categories TEXT NOT NULL DEFAULT '',  -- 钓法分类，分号连接
    trophy_class TEXT NOT NULL DEFAULT '',
    catch_date TEXT NOT NULL DEFAULT '',  -- 源站 dd.mm.yy 原样
    fetched_at REAL NOT NULL,
    PRIMARY KEY(fish_name_en, bait_name, weight_g, catch_date, waterbody));
