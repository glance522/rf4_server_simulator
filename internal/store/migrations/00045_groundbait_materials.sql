-- 窝料按实际用料判定（specs/changes/groundbait-system/spec.md）。
--
-- 自制窝料的产物在客户端只有一个 item_id（feedcompleted），5~6 个槽随便怎么配都叫这个名，
-- 所以「这袋料到底是用什么做的」必须按实例记，不能记在 item_id 上。
CREATE TABLE IF NOT EXISTS groundbait_recipe(
    instance_guid TEXT PRIMARY KEY,
    mats_json     TEXT NOT NULL DEFAULT '',  -- 制作时实际扣掉的原料 item_id 列表
    created_ts    REAL NOT NULL DEFAULT 0
) WITHOUT ROWID;

-- 投放时把当时那份料的用料一起冻进账本行：实例之后会被吃掉、删掉，
-- 窝还得好几个小时，判鱼时不能回头去查一个已经消失的实例。
ALTER TABLE groundbait_deposit ADD COLUMN materials_json TEXT NOT NULL DEFAULT '';
