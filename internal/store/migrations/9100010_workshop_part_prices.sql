-- 工坊配件价 / 修理工时的后台覆盖层（我方独占号段 9100010）。
--
-- 为什么必须独立一张表，而不是直接改 item_price：
--   item_price 登记在 store.StaticTables 里，属于**运行时只读的静态资料**，
--   seed 刷新会整表替换。后台手改的价格下次刷新资料就没了。species_value_rates
--   （9100001）、shop_item_override（00014）都是同一个理由独立出来的。
--
-- 取值优先级（实现见 internal/gameserver/workshop_part_price.go）：
--   本表 map_key 精确命中
--     > 本表 map_key='' 全局行
--       > item_price 静态价
--         > 属性推导（partPrice：摩擦片 26×kg²、齿轮 480×传动比、轴承 品质×10、
--                     线杯 容量×12、轮油 50 银）
--           > partBasePrice[品类]
--             > 500 兜底
-- 每一层缺失都继续往下掉，**任何一层都不会让报价失败**——这是玩家 2026-11-12
-- 定的口径：库里有的优先用库里的，库里没有就走兜底防报错。
--
-- 列语义：NULL = 这一列不覆盖（继续走下面的兜底层），不是"0"。
-- 需要"0 银币 / 0 金币 / 即时"就必须显式写 0，所以三列都用可空列。
CREATE TABLE IF NOT EXISTS workshop_part_price (
    item_id    TEXT    NOT NULL,            -- 配件 ItemID，对应 item_def.item_id
    map_key    TEXT    NOT NULL DEFAULT '', -- '' = 全局；否则只对这张图生效
    silver     INTEGER,                     -- NULL = 不覆盖银币价
    gold       INTEGER,                     -- NULL = 不覆盖金币价
    hours      INTEGER,                     -- NULL = 用品类默认工时；0 = 即时
    source     TEXT    NOT NULL DEFAULT 'manual',
    note       TEXT    NOT NULL DEFAULT '',
    updated_at REAL    NOT NULL DEFAULT 0,
    PRIMARY KEY (item_id, map_key)
);

CREATE INDEX IF NOT EXISTS idx_workshop_part_price_item
    ON workshop_part_price(item_id);
