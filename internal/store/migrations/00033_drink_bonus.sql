CREATE TABLE IF NOT EXISTS drink_bonus_clock(
    uid INTEGER PRIMARY KEY,
    owner TEXT NOT NULL,
    last_tick INTEGER NOT NULL,
    online INTEGER NOT NULL DEFAULT 0 CHECK(online IN (0,1))
);

CREATE TABLE IF NOT EXISTS drink_bonus_effect(
    uid INTEGER NOT NULL,
    category TEXT NOT NULL CHECK(length(category)>0),
    remaining_millis INTEGER NOT NULL CHECK(remaining_millis>=0),
    duration_millis INTEGER NOT NULL CHECK(duration_millis>0),
    source_guid TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(uid,category),
    CHECK(remaining_millis<=duration_millis)
);
