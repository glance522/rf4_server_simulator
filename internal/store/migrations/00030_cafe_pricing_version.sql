-- 报价规则更新生成新单，保留旧报价及已结算回执。
CREATE TABLE cafe_order_v2(
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
    pricing_version INTEGER NOT NULL DEFAULT 1 CHECK(pricing_version > 0),
    UNIQUE(scene,batch_start,period_seconds,ordinal,pricing_version)
);
INSERT INTO cafe_order_v2 SELECT order_id,scene,level_key,batch_start,period_seconds,ordinal,species,min_weight_g,quantity,reward_cents,offer_size,expires_at,1 FROM cafe_order;
CREATE TABLE cafe_order_completion_v2(
    uid INTEGER NOT NULL REFERENCES profile(uid),
    order_id TEXT NOT NULL REFERENCES cafe_order_v2(order_id),
    reward_cents INTEGER NOT NULL CHECK(reward_cents > 0),
    fish_guids_json TEXT NOT NULL,
    completed_at INTEGER NOT NULL CHECK(completed_at >= 0),
    PRIMARY KEY(uid,order_id)
);
INSERT INTO cafe_order_completion_v2 SELECT * FROM cafe_order_completion;
DROP TABLE cafe_order_completion;
DROP TABLE cafe_order;
ALTER TABLE cafe_order_v2 RENAME TO cafe_order;
ALTER TABLE cafe_order_completion_v2 RENAME TO cafe_order_completion;
CREATE INDEX cafe_order_batch ON cafe_order(scene,batch_start,period_seconds,pricing_version);
