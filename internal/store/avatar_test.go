package store

import (
	"context"
	"testing"
)

// 头像列：迁移后 profile 表应有 avatar 列，且 GetProfile 能读回写入的值。
func TestProfileAvatarColumnRoundTrip(t *testing.T) {
	db := openIntegrityTestDB(t)
	ctx := context.Background()

	if !db.HasAvatarColumn() {
		t.Fatal("迁移后 profile 表缺少 avatar 列")
	}
	const uid = int64(4242)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	const url = "http://127.0.0.1:9500/avatars/4242.png"
	if _, err := db.SQL.ExecContext(ctx,
		`UPDATE profile SET avatar=? WHERE uid=?`, url, uid); err != nil {
		t.Fatal(err)
	}
	p, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if p.Avatar != url {
		t.Fatalf("avatar=%q，期望 %q", p.Avatar, url)
	}
}
