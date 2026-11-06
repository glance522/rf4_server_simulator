package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var (
	ErrPlayerNameNotFound  = errors.New("玩家用户名不存在")
	ErrAmbiguousPlayerName = errors.New("玩家用户名不唯一")
)

// FindUniqueProfileByName resolves a case-insensitive username to exactly one player.
func (d *DB) FindUniqueProfileByName(ctx context.Context, username string) (*Profile, error) {
	rows, err := d.SQL.QueryContext(ctx,
		`SELECT uid, name FROM profile WHERE name=? COLLATE NOCASE ORDER BY uid LIMIT 2`, username)
	if err != nil {
		return nil, fmt.Errorf("查询玩家用户名: %w", err)
	}
	defer rows.Close()

	var matches []*Profile
	for rows.Next() {
		var profile Profile
		if err := rows.Scan(&profile.UID, &profile.Name); err != nil {
			return nil, fmt.Errorf("读取玩家用户名: %w", err)
		}
		matches = append(matches, &profile)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("枚举玩家用户名: %w", err)
	}
	switch len(matches) {
	case 0:
		return nil, ErrPlayerNameNotFound
	case 1:
		return matches[0], nil
	default:
		return nil, ErrAmbiguousPlayerName
	}
}

// SetPlayerAvatar associates an avatar URL with a player's stable UID and username.
func (d *DB) SetPlayerAvatar(ctx context.Context, uid int64, username, avatarURL string) error {
	result, err := d.SQL.ExecContext(ctx, `
		INSERT INTO player_avatar(uid, username, avatar_url, updated_ts)
		SELECT uid, ?, ?, ? FROM profile WHERE uid=?
		ON CONFLICT(uid) DO UPDATE SET
			username=excluded.username,
			avatar_url=excluded.avatar_url,
			updated_ts=excluded.updated_ts`,
		username, avatarURL, float64(time.Now().Unix()), uid)
	if err != nil {
		return fmt.Errorf("保存玩家头像: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("检查玩家头像保存结果: %w", err)
	}
	if affected == 0 {
		return ErrPlayerNameNotFound
	}
	return nil
}

// PlayerAvatarURL returns the player's stored avatar URL, or an empty string when unset.
func (d *DB) PlayerAvatarURL(ctx context.Context, uid int64) (string, error) {
	var avatarURL string
	err := d.SQL.QueryRowContext(ctx,
		`SELECT avatar_url FROM player_avatar WHERE uid=?`, uid).Scan(&avatarURL)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("读取玩家头像: %w", err)
	}
	return avatarURL, nil
}
