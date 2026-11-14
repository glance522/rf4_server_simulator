-- 钩实证记录：结算钓获时把选中钩的 item_id 写入 catch_log.hook，
-- 为「此鱼常用钩型（实证）」积累数据；旧行默认空串。
ALTER TABLE catch_log ADD COLUMN hook TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_catch_log_fish_hook ON catch_log(fish_key, hook);
