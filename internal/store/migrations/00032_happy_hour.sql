CREATE TABLE IF NOT EXISTS happy_hour_state(
    uid INTEGER PRIMARY KEY,
    local_day TEXT NOT NULL,
    used_millis INTEGER NOT NULL DEFAULT 0 CHECK(used_millis BETWEEN 0 AND 3600000),
    last_tick INTEGER NOT NULL,
    session_key TEXT NOT NULL DEFAULT '',
    active INTEGER NOT NULL DEFAULT 0 CHECK(active IN (0,1))
);
INSERT OR IGNORE INTO tunables(key,value) VALUES('happy_hour_enabled','1');
