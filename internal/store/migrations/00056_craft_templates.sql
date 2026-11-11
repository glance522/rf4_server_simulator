-- 配方模板持久化：玩家在制作页保存的自定义模板。
--
-- 此前 9/5（crafting/tmplts）恒返回空的 args(97)，9/6（crafting/addtmplt）只回包
-- 不落库，9/7（crafting/deltmplt）未实现，所以「创建了 xxx 模板」提示出现后
-- 配方列表里看不到，切页面/重登即消失。
--
-- guid 是模板实例 id：客户端保存时下发的 171.f0，也是 9/7 删除的键。
-- (uid, guid) 为主键让同一次保存可重放；(uid, recipe_id, name) 唯一索引支撑
-- 「同名覆盖」，避免同名模板在列表里堆成两条。
CREATE TABLE IF NOT EXISTS craft_template(
    uid            INTEGER NOT NULL,
    guid           TEXT    NOT NULL,              -- 模板实例 id（16 字节 hex）
    recipe_id      TEXT    NOT NULL,              -- 来源配方（craft_feed_dry 等）
    name           TEXT    NOT NULL,              -- 玩家填写的模板名
    kind           TEXT    NOT NULL DEFAULT '',   -- 配方分类（171.f2）
    tools_json     TEXT    NOT NULL DEFAULT '[]', -- 170 条目投影：工具槽
    materials_json TEXT    NOT NULL DEFAULT '[]', -- 170 条目投影：材料槽
    created_ts     REAL    NOT NULL,
    updated_ts     REAL    NOT NULL,
    PRIMARY KEY (uid, guid)
);
CREATE UNIQUE INDEX IF NOT EXISTS craft_template_uid_recipe_name
    ON craft_template(uid, recipe_id, name);
CREATE INDEX IF NOT EXISTS craft_template_uid ON craft_template(uid);
