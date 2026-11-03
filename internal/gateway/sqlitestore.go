package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"rf4server/internal/store"
)

// SQLiteStore 会话凭据与重连接管的本地存储实现（共享 SQLite 文件，WAL 并发）。
// 生产与本地单机同用；MemStore 仅供测试。
type SQLiteStore struct {
	DB  *store.DB
	log *slog.Logger
}

// NewSQLiteStore 构建本地存储。
func NewSQLiteStore(db *store.DB, log *slog.Logger) *SQLiteStore {
	return &SQLiteStore{DB: db, log: log}
}

func (s *SQLiteStore) SaveToken(ctx context.Context, token string, info TokenInfo, ttl time.Duration) error {
	b, err := json.Marshal(info)
	if err != nil {
		return err
	}
	_, err = s.DB.SQL.ExecContext(ctx, `
		INSERT INTO gw_token(token, info_json, expires) VALUES(?,?,?)
		ON CONFLICT (token) DO UPDATE SET info_json=?, expires=?`,
		token, string(b), time.Now().Add(ttl).Unix(),
		string(b), time.Now().Add(ttl).Unix())
	return err
}

func (s *SQLiteStore) LookupToken(ctx context.Context, token string) (TokenInfo, bool, error) {
	var infoJSON string
	var expiresF float64 // gw_token.expires 列是 REAL，SQLite 读回 float64
	err := s.DB.SQL.QueryRowContext(ctx,
		`SELECT info_json, expires FROM gw_token WHERE token=?`, token).Scan(&infoJSON, &expiresF)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// 客户端可能回传 logintoken（登录 XML 里的 token 前 64 字符）——
			// 前缀映射回完整 token 再取
			return s.lookupByPrefix(ctx, token)
		}
		return TokenInfo{}, false, err
	}
	return decodeTokenInfo(infoJSON, expiresF)
}

// lookupByPrefix 前缀匹配（token 以 logintoken 开头）。
func (s *SQLiteStore) lookupByPrefix(ctx context.Context, prefix string) (TokenInfo, bool, error) {
	if len(prefix) < 8 {
		return TokenInfo{}, false, nil
	}
	var infoJSON string
	var expiresF float64
	err := s.DB.SQL.QueryRowContext(ctx,
		`SELECT info_json, expires FROM gw_token WHERE token LIKE ?||'%' AND length(token)>? ORDER BY expires DESC LIMIT 1`,
		prefix, len(prefix)).Scan(&infoJSON, &expiresF)
	if err != nil {
		return TokenInfo{}, false, nil // 无前缀命中
	}
	return decodeTokenInfo(infoJSON, expiresF)
}

func decodeTokenInfo(infoJSON string, expires float64) (TokenInfo, bool, error) {
	if float64(time.Now().Unix()) > expires {
		return TokenInfo{}, false, nil
	}
	var info TokenInfo
	if err := json.Unmarshal([]byte(infoJSON), &info); err != nil {
		return TokenInfo{}, false, err
	}
	return info, true, nil
}

func (s *SQLiteStore) SaveHandoff(ctx context.Context, token string, h Handoff, ttl time.Duration) error {
	h.SavedAt = time.Now().Unix()
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	_, err = s.DB.SQL.ExecContext(ctx, `
		INSERT INTO gw_handoff(token, state_json, saved_at) VALUES(?,?,?)
		ON CONFLICT (token) DO UPDATE SET state_json=?, saved_at=?`,
		token, string(b), h.SavedAt, string(b), h.SavedAt)
	return err
}

func (s *SQLiteStore) LoadHandoff(ctx context.Context, token string) (*Handoff, error) {
	var stateJSON string
	err := s.DB.SQL.QueryRowContext(ctx,
		`SELECT state_json FROM gw_handoff WHERE token=?`, token).Scan(&stateJSON)
	if err != nil {
		return nil, nil
	}
	var h Handoff
	if err := json.Unmarshal([]byte(stateJSON), &h); err != nil {
		return nil, err
	}
	return &h, nil
}

func (s *SQLiteStore) DeleteHandoff(ctx context.Context, token string) error {
	_, err := s.DB.SQL.ExecContext(ctx, `DELETE FROM gw_handoff WHERE token=?`, token)
	return err
}

func (s *SQLiteStore) NextUserid(ctx context.Context) (int64, error) {
	// 共享 KV 计数器：单机多进程安全（WAL + busy_timeout 串行写）
	const key = "userid_seq"
	var cur int64
	if v, ok, err := s.DB.KVGet(ctx, key); err == nil && ok {
		_, _ = fmtSscan(v, &cur)
	}
	next := cur + 1
	if err := s.DB.KVSet(ctx, key, int64ToStr(next)); err != nil {
		return 0, err
	}
	return next, nil
}

func (s *SQLiteStore) AccountLookup(ctx context.Context, username string) (int64, string, bool, error) {
	return s.DB.AccountLookup(ctx, username)
}

// SyncProfileName 把游戏内显示名与登录用户名同步（实现在 store 层）。
func (s *SQLiteStore) SyncProfileName(ctx context.Context, userid int64, name string) error {
	return s.DB.SyncProfileName(ctx, userid, name)
}

func fmtSscan(s string, out *int64) (int, error) {
	return fmt.Sscanf(s, "%d", out)
}

func int64ToStr(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
