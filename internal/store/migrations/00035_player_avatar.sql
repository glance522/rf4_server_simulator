CREATE TABLE IF NOT EXISTS player_avatar(
    uid          INTEGER PRIMARY KEY,
    username     TEXT NOT NULL,
    avatar_url   TEXT NOT NULL,
    updated_ts   REAL NOT NULL,
    FOREIGN KEY(uid) REFERENCES profile(uid) ON DELETE CASCADE
);
