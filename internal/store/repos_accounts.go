package store

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// PasswordSHA1 把明文密码哈希为客户端登录使用的 SHA-1（40 位小写 hex）。
// 注意：客户端登录只上传 SHA-1，服务端拿不到明文，因此密码复杂度策略
// 只能在注册/改密入口（管理后台）强制执行。
func PasswordSHA1(password string) string {
	sum := sha1.Sum([]byte(password))
	return hex.EncodeToString(sum[:])
}

// ValidatePassword 密码策略：至少 8 位，必须同时包含大写字母、小写字母和数字。
// 仅在拿到明文的入口（管理后台建号/改密）调用。
func ValidatePassword(password string) error {
	if len(password) < 8 {
		return errors.New("密码至少 8 位")
	}
	var hasUpper, hasLower, hasDigit bool
	for _, r := range password {
		switch {
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= 'a' && r <= 'z':
			hasLower = true
		case r >= '0' && r <= '9':
			hasDigit = true
		}
	}
	if !hasUpper || !hasLower || !hasDigit {
		return errors.New("密码必须同时包含大写字母、小写字母和数字")
	}
	return nil
}

// Account 本地登录账号（用户名/密码绑定 player id）。
type Account struct {
	Username  string `json:"username"`
	PassSHA1  string `json:"-"` // 不下发给前端
	Userid    int64  `json:"userid"`
	CreatedAt int64  `json:"created_at"`
}

// UnboundProfile 是有存档但没有登录账号的玩家（用户管理页灰显展示）。
type UnboundProfile struct {
	Userid int64  `json:"userid"`
	Name   string `json:"name"`
}

// ErrAccountExists 用户名已被注册。
var ErrAccountExists = errors.New("账号已存在")

// ErrUseridTaken userid 已被其他账号绑定。
var ErrUseridTaken = errors.New("userid 已被其他账号绑定")

// NormalizeUsername 统一用户名形态（去空白、转小写比对在调用方做，这里只裁剪）。
func NormalizeUsername(s string) string {
	return strings.TrimSpace(s)
}

// AccountLookup 按用户名查账号；不存在返回 ok=false。
func (s *DB) AccountLookup(ctx context.Context, username string) (userid int64, passSHA1 string, ok bool, err error) {
	err = s.SQL.QueryRowContext(ctx,
		`SELECT userid, pass_sha1 FROM accounts WHERE username=?`, NormalizeUsername(username)).
		Scan(&userid, &passSHA1)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	return userid, strings.ToLower(passSHA1), true, nil
}

// AccountCreate 建号。username 唯一、userid 唯一。
func (s *DB) AccountCreate(ctx context.Context, username, passSHA1 string, userid int64) error {
	if NormalizeUsername(username) == "" || len(strings.TrimSpace(passSHA1)) != 40 {
		return errors.New("用户名或密码哈希非法")
	}
	_, err := s.SQL.ExecContext(ctx,
		`INSERT INTO accounts(username, pass_sha1, userid) VALUES(?,?,?)`,
		NormalizeUsername(username), strings.ToLower(passSHA1), userid)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			if taken, _ := s.AccountUseridTaken(ctx, userid); taken {
				return ErrUseridTaken
			}
			return ErrAccountExists
		}
		return err
	}
	return nil
}

// AccountList 全量账号（创建时间倒序）。
func (s *DB) AccountList(ctx context.Context) ([]Account, error) {
	rows, err := s.SQL.QueryContext(ctx,
		`SELECT username, userid, created_at FROM accounts ORDER BY created_at DESC, username ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		var a Account
		var created sql.NullInt64
		if err := rows.Scan(&a.Username, &a.Userid, &created); err != nil {
			return nil, err
		}
		a.CreatedAt = created.Int64
		out = append(out, a)
	}
	return out, rows.Err()
}

// AccountSetPassword 重置密码（服务端把明文哈希为 SHA-1 后传入）。
func (s *DB) AccountSetPassword(ctx context.Context, username, passSHA1 string) error {
	res, err := s.SQL.ExecContext(ctx,
		`UPDATE accounts SET pass_sha1=? WHERE username=?`,
		strings.ToLower(passSHA1), NormalizeUsername(username))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// AccountDelete 删号。
func (s *DB) AccountDelete(ctx context.Context, username string) error {
	res, err := s.SQL.ExecContext(ctx, `DELETE FROM accounts WHERE username=?`, NormalizeUsername(username))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// AccountUseridTaken userid 是否已被账号占用。
func (s *DB) AccountUseridTaken(ctx context.Context, userid int64) (bool, error) {
	var one int
	err := s.SQL.QueryRowContext(ctx,
		`SELECT 1 FROM accounts WHERE userid=? LIMIT 1`, userid).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// NextUseridSeq 自动递增分配 userid：取现有玩家档案最大 uid 继续编号，
// 不存在档案时从 1000001（userid_start 默认值）之后开始。
// 不再使用 KV 计数器，避免与 profile 表状态不同步。
func (s *DB) NextUseridSeq(ctx context.Context) (int64, error) {
	var maxUID int64
	if err := s.SQL.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(uid), 0) FROM profile`).Scan(&maxUID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if maxUID < 1000001 {
		maxUID = 1000001
	}
	return maxUID + 1, nil
}

// UnboundProfiles 列出已有存档但尚未绑定登录账号的玩家（如历史 1000001），
// 用于用户管理页展示。
func (s *DB) UnboundProfiles(ctx context.Context) ([]UnboundProfile, error) {
	rows, err := s.SQL.QueryContext(ctx, `
		SELECT p.uid, p.name FROM profile p
		LEFT JOIN accounts a ON a.userid = p.uid
		WHERE a.username IS NULL
		ORDER BY p.uid LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []UnboundProfile
	for rows.Next() {
		var u UnboundProfile
		if err := rows.Scan(&u.Userid, &u.Name); err != nil {
			return nil, err
		}
		list = append(list, u)
	}
	return list, rows.Err()
}

// SyncProfileName 把游戏内显示名（profile.name）与账号用户名同步：
// profile 不存在则先建（与主服 ensureProfileTx 相同形态），再更新 name。
// 注册（管理后台建号）与账号登录成功时都会调用，游戏内即自动显示该名字。
func (s *DB) SyncProfileName(ctx context.Context, userid int64, name string) error {
	now := float64(time.Now().Unix())
	if _, err := s.SQL.ExecContext(ctx, `
		INSERT INTO profile(uid, name, created_ts, updated_ts)
		VALUES(?, ?, ?, ?) ON CONFLICT (uid) DO NOTHING`,
		userid, name, now, now); err != nil {
		return err
	}
	_, err := s.SQL.ExecContext(ctx,
		`UPDATE profile SET name=?, updated_ts=? WHERE uid=?`, name, now, userid)
	return err
}

// AccountAt 账号创建时间的可读展示（前端兜底）。
func AccountAt(ts int64) time.Time { return time.Unix(ts, 0) }

