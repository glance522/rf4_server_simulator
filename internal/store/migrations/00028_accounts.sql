-- 本地账号表：游戏登录窗口提交的用户名/密码（SHA-1）绑定 player id。
-- 客户端登录请求形如 GET /login.php?login=<用户名>&password=<40hex SHA-1>。
CREATE TABLE IF NOT EXISTS accounts (
    username   TEXT PRIMARY KEY,
    pass_sha1  TEXT NOT NULL,
    userid     INTEGER NOT NULL UNIQUE,
    created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
);

CREATE INDEX IF NOT EXISTS idx_accounts_userid ON accounts(userid);
