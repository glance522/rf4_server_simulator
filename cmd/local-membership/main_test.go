package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"rf4server/internal/store"
)

func TestLocalMembershipCLIUsesSharedTransaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gameplay.sqlite3")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnsureProfile(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET gold=1000 WHERE uid=42`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	call := func(action ...string) (int, response) {
		t.Helper()
		out := new(bytes.Buffer)
		args := append([]string{"--database", path, "--uid", "42"}, action...)
		code := run(args, out)
		var result response
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatalf("输出非JSON: %q %v", out.String(), err)
		}
		return code, result
	}
	code, status := call("status")
	if code != 0 || !status.OK || status.Balance.Gold != 1000 || status.Membership.Active || len(status.Plans) != 3 {
		t.Fatalf("读取状态错误: %d %+v", code, status)
	}
	code, bought := call("purchase", "--plan", "premium_7d", "--request-id", "cli-request-0001")
	if code != 0 || !bought.OK || bought.Purchase == nil || bought.Purchase.Replayed || bought.Balance.Gold != 500 || !bought.Membership.Active || bought.Membership.ExpiresAt != bought.Purchase.ExpiresAt {
		t.Fatalf("购买错误: %d %+v", code, bought)
	}
	code, replay := call("purchase", "--plan", "premium_7d", "--request-id", "cli-request-0001")
	if code != 0 || replay.Purchase == nil || !replay.Purchase.Replayed || replay.Balance.Gold != 500 {
		t.Fatalf("重放错误: %d %+v", code, replay)
	}
	code, failed := call("purchase", "--plan", "premium_30d", "--request-id", "cli-request-0002")
	if code != 1 || failed.OK || failed.Error == nil || failed.Error.Code != "insufficient_gold" {
		t.Fatalf("余额不足错误: %d %+v", code, failed)
	}
}

func TestLocalMembershipCLIRollsBackAndRetriesFailedPurchase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gameplay.sqlite3")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnsureProfile(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET gold=1000 WHERE uid=42;
		CREATE TRIGGER fail_membership_log BEFORE INSERT ON txn WHEN NEW.kind='membership'
		BEGIN SELECT RAISE(ABORT,'test membership failure'); END`); err != nil {
		t.Fatal(err)
	}
	args := []string{"--database", path, "--uid", "42", "purchase", "--plan", "premium_7d", "--request-id", "cli-rollback-0001"}
	call := func() (int, response) {
		t.Helper()
		out := new(bytes.Buffer)
		code := run(args, out)
		var result response
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return code, result
	}
	code, failed := call()
	if code != 1 || failed.OK || failed.Error == nil || failed.Error.Code != "database_error" {
		t.Fatalf("购买失败未返回错误: %d %+v", code, failed)
	}
	profile, err := db.GetProfile(context.Background(), 42)
	if err != nil || profile.Gold != 1000 || profile.Membership != 0 || profile.MembershipExp != 0 {
		t.Fatalf("失败购买改变角色状态: %+v %v", profile, err)
	}
	var orders int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM membership_order WHERE uid=42`).Scan(&orders); err != nil || orders != 0 {
		t.Fatalf("失败购买留下订单: %d %v", orders, err)
	}
	if _, err := db.SQL.Exec(`DROP TRIGGER fail_membership_log`); err != nil {
		t.Fatal(err)
	}
	code, bought := call()
	if code != 0 || !bought.OK || bought.Purchase == nil || bought.Purchase.Replayed || bought.Balance.Gold != 500 || !bought.Membership.Active {
		t.Fatalf("相同请求重试未成功: %d %+v", code, bought)
	}
}
