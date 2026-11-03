-- 咖啡厅 NPC 订单与玩家交付回执；不改玩家交易挂单。
CREATE TABLE cafe_order(
    order_id TEXT PRIMARY KEY,
    scene TEXT NOT NULL,
    level_key TEXT NOT NULL,
    batch_start INTEGER NOT NULL CHECK(batch_start >= 0),
    period_seconds INTEGER NOT NULL CHECK(period_seconds > 0),
    ordinal INTEGER NOT NULL CHECK(ordinal >= 0),
    species TEXT NOT NULL CHECK(length(species) > 0),
    min_weight_g INTEGER NOT NULL CHECK(min_weight_g > 0),
    quantity INTEGER NOT NULL CHECK(quantity > 0),
    reward_cents INTEGER NOT NULL CHECK(reward_cents > 0),
    offer_size INTEGER NOT NULL DEFAULT 1 CHECK(offer_size BETWEEN 1 AND 4),
    expires_at INTEGER NOT NULL CHECK(expires_at > batch_start),
    UNIQUE(scene,batch_start,period_seconds,ordinal)
);
CREATE INDEX cafe_order_batch ON cafe_order(scene,batch_start,period_seconds);
CREATE TABLE cafe_order_completion(
    uid INTEGER NOT NULL REFERENCES profile(uid),
    order_id TEXT NOT NULL REFERENCES cafe_order(order_id),
    reward_cents INTEGER NOT NULL CHECK(reward_cents > 0),
    fish_guids_json TEXT NOT NULL,
    completed_at INTEGER NOT NULL CHECK(completed_at >= 0),
    PRIMARY KEY(uid,order_id)
);
