package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"rf4server/internal/gameserver"
	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

func importTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "players.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	return db
}

func importTestCharacter(t *testing.T) *character {
	t.Helper()
	p := wiregen.MustMakeObj("ghfbofgdflm", map[int]*proto.Value{
		0: proto.U32(42), 1: proto.StrVal("LocalPlayer"), 2: proto.U32(7),
		3: proto.U64(12345), 7: proto.U64(1000), 8: proto.U64(20), 19: proto.U64(30),
	})
	tree := wiregen.MustMakeObj("kbnbcppjkaf", map[int]*proto.Value{})
	for _, meta := range wiregen.ClassMeta[tree.Class].Fields {
		if meta.Name == "cahhdnicmbn" {
			tree.Fields[meta.Idx].Hex = "01000000000000000000000000000000"
		}
	}
	fields, err := gameserver.FieldsToJSON(tree.Fields)
	if err != nil {
		t.Fatal(err)
	}
	return &character{uid: 42, profile: p, items: []importedItem{{row: &store.Item{
		GUID: "01000000000000000000000000000000", UID: 42, WireClass: tree.Class, TypeID: int64(tree.TypeID),
		Container: "backpack", Qty: 2, FieldsJSON: fields, Durability: 1, Workmanship: 1,
	}, tree: tree, kind: 0}}, bootstrap: map[string]*proto.Value{}, raw: map[string][]byte{"profile": {1, 2, 3}}, inventory: []byte(`{"countsByKind":{}}`), fingerprint: "test-fingerprint"}
}

func TestImportAtomicAndRepeatPreservesProgress(t *testing.T) {
	db := importTestDB(t)
	c := importTestCharacter(t)
	ctx := context.Background()
	if repeated, err := writeCharacter(ctx, db, c); err != nil || repeated {
		t.Fatalf("first import: repeated=%v err=%v", repeated, err)
	}
	if _, err := db.SQL.Exec("UPDATE profile SET xp=22222 WHERE uid=42"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec("UPDATE item SET qty=9 WHERE uid=42"); err != nil {
		t.Fatal(err)
	}
	if repeated, err := writeCharacter(ctx, db, c); err != nil || !repeated {
		t.Fatalf("repeat: repeated=%v err=%v", repeated, err)
	}
	var xp, qty int64
	_ = db.SQL.QueryRow("SELECT xp FROM profile WHERE uid=42").Scan(&xp)
	_ = db.SQL.QueryRow("SELECT qty FROM item WHERE uid=42").Scan(&qty)
	if xp != 22222 || qty != 9 {
		t.Fatal("repeat import overwrote gameplay progress")
	}
	c.fingerprint = "different-source"
	if _, err := writeCharacter(ctx, db, c); err == nil {
		t.Fatal("different source overwrote character")
	}
}

func TestImportLateFailureRollsBackEntireCharacter(t *testing.T) {
	db := importTestDB(t)
	if _, err := db.SQL.Exec(`CREATE TRIGGER fail_import BEFORE INSERT ON kv BEGIN SELECT RAISE(ABORT,'late failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := writeCharacter(context.Background(), db, importTestCharacter(t)); err == nil {
		t.Fatal("expected late failure")
	}
	for _, table := range []string{"profile", "item", "keepnet_items", "rig", "skill", "container_slot", "kv"} {
		var count int
		if err := db.SQL.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("partial import remained in %s", table)
		}
	}
}

func TestImportConflictsPreserveExistingCharacter(t *testing.T) {
	db := importTestDB(t)
	if _, err := db.SQL.Exec("INSERT INTO profile(uid,name,xp) VALUES(42,'Existing',999)"); err != nil {
		t.Fatal(err)
	}
	if _, err := writeCharacter(context.Background(), db, importTestCharacter(t)); err == nil {
		t.Fatal("existing profile was overwritten")
	}
	var name string
	var xp int64
	_ = db.SQL.QueryRow("SELECT name,xp FROM profile WHERE uid=42").Scan(&name, &xp)
	if name != "Existing" || xp != 999 {
		t.Fatal("existing character changed")
	}
}

func TestTargetRejectsPythonAccountDatabase(t *testing.T) {
	db := importTestDB(t)
	if _, err := db.SQL.Exec("CREATE TABLE accounts(username TEXT)"); err != nil {
		t.Fatal(err)
	}
	if err := checkTarget(db.Path); err == nil {
		t.Fatal("Python account database was accepted")
	}
	var count int
	if err := db.SQL.QueryRow("SELECT COUNT(*) FROM accounts").Scan(&count); err != nil || count != 0 {
		t.Fatal("foreign schema changed")
	}
}

func TestCheckedBlobRejectsCorruptionAndTraversal(t *testing.T) {
	root := t.TempDir()
	blob := []byte{0}
	if err := os.WriteFile(filepath.Join(root, "value.bin"), blob, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(blob)
	entry := blobEntry{File: "value.bin", Size: 1, SHA256: hex.EncodeToString(sum[:])}
	if _, _, err := checkedBlob(root, entry); err != nil {
		t.Fatal(err)
	}
	entry.SHA256 = "wrong"
	if _, _, err := checkedBlob(root, entry); err == nil {
		t.Fatal("corruption was accepted")
	}
	entry.File = "../value.bin"
	if _, _, err := checkedBlob(root, entry); err == nil {
		t.Fatal("path traversal was accepted")
	}
}

func TestImportOldSchemaAndRestartPreserveHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-players.sqlite3")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	initial, err := os.ReadFile(filepath.Join("..", "..", "internal", "store", "migrations", "00001_player.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.SQL.Exec(string(initial)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.SQL.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT NOT NULL,applied_at REAL NOT NULL); INSERT INTO schema_migrations VALUES(1,'migrations/00001_player.sql',1); INSERT INTO profile(uid,name,xp) VALUES(99,'Historical',77)`); err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(); err != nil {
		t.Fatal(err)
	}
	c := importTestCharacter(t)
	if _, err = writeCharacter(context.Background(), db, c); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(); err != nil {
		t.Fatal(err)
	}
	var name string
	var xp int64
	if err = db.SQL.QueryRow("SELECT name,xp FROM profile WHERE uid=99").Scan(&name, &xp); err != nil || name != "Historical" || xp != 77 {
		t.Fatal("legacy profile changed")
	}
	if repeat, err := writeCharacter(context.Background(), db, c); err != nil || !repeat {
		t.Fatalf("restart repeat=%v err=%v", repeat, err)
	}
	var check string
	if err = db.SQL.QueryRow("PRAGMA integrity_check").Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity=%s err=%v", check, err)
	}
}

func TestLocalSnapshotImportPreservesEveryRootItem(t *testing.T) {
	source := os.Getenv("RF4_TEST_CHARACTER_DIR")
	if source == "" {
		t.Skip("RF4_TEST_CHARACTER_DIR is not configured")
	}
	c, err := loadCharacter(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.items) != 1021 {
		t.Fatalf("source roots=%d, want 1021", len(c.items))
	}
	target := filepath.Join(t.TempDir(), "players.sqlite3")
	static := filepath.Join("..", "..", "server_data", "static.sqlite3")
	result, err := run(source, target, static, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.AlreadyImported || result.Items != 1021 {
		t.Fatal("incomplete first import")
	}
	db, err := store.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var roots, fish int
	_ = db.SQL.QueryRow("SELECT COUNT(*) FROM item WHERE uid=?", c.uid).Scan(&roots)
	_ = db.SQL.QueryRow("SELECT COUNT(*) FROM keepnet_items WHERE uid=?", c.uid).Scan(&fish)
	if roots != 1021 || fish != 2 {
		t.Fatalf("stored roots=%d fish=%d", roots, fish)
	}
	shortcuts, err := db.ListShortcuts(context.Background(), c.uid)
	if err != nil || len(shortcuts) != 5 {
		t.Fatalf("shortcut import count=%d err=%v", len(shortcuts), err)
	}
	if shortcuts[0].Slot != 1 || shortcuts[3].Slot != 4 || shortcuts[0].GUID != shortcuts[3].GUID {
		t.Fatal("captured shortcut aliases changed")
	}
	for _, expected := range c.items {
		row, err := db.GetItemByGUID(context.Background(), expected.row.GUID)
		if err != nil {
			t.Fatal(err)
		}
		if row.FieldsJSON != expected.row.FieldsJSON {
			t.Fatal("item fields changed during import")
		}
	}
	result, err = run(source, target, static, false)
	if err != nil || !result.AlreadyImported {
		t.Fatalf("repeat: %+v %v", result, err)
	}
}

func TestLoadCharacterRequiresConsistentLocalAccount(t *testing.T) {
	for _, mismatched := range []string{"", "inventory", "details", "bootstrap"} {
		t.Run("mismatch_"+mismatched, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "bootstrap"), 0700); err != nil {
				t.Fatal(err)
			}
			writeJSON := func(name string, value any) {
				t.Helper()
				body, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, name), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			account := func(section string) string {
				if section == mismatched {
					return "other_local_account"
				}
				return "local_account"
			}
			profile := importTestCharacter(t).profile
			body, err := proto.EncodeValue(profile, wiregen.Table())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "profile.bin"), body, 0600); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(body)
			writeJSON("profile.json", manifest{Schema: 1, ClientVersion: "4.0.25034", TargetAccount: account("profile"), NamesRedacted: true,
				blobEntry: blobEntry{File: "profile.bin", Size: len(body), SHA256: hex.EncodeToString(sum[:])}})
			writeJSON("inventory.json", inventoryIndex{Schema: 2, ClientVersion: "4.0.25034", TargetAccount: account("inventory"), SourceUserID: 42,
				IDsByKind: map[string][]string{}})
			writeJSON("details.json", manifest{Schema: 1, ClientVersion: "4.0.25034", TargetAccount: account("details"), NamesRedacted: true})
			writeJSON(filepath.Join("bootstrap", "index.json"), manifest{Schema: 1, ClientVersion: "4.0.25034", TargetAccount: account("bootstrap"), NamesRedacted: true})
			loaded, err := loadCharacter(root)
			if mismatched != "" {
				if err == nil {
					t.Fatal("accepted snapshots belonging to different local accounts")
				}
				return
			}
			if err != nil || loaded.uid != 42 {
				t.Fatalf("valid local account snapshot was rejected: %v", err)
			}
		})
	}
}

func TestImportPreservesMembershipExpiry(t *testing.T) {
	db := importTestDB(t)
	c := importTestCharacter(t)
	const expires = int64(1800000000)
	member, expiry := uint64(1), uint64(proto.UnixToFiletime(expires))
	c.profile.Fields[10].Num = &member
	c.profile.Fields[11].Num = &expiry
	if _, err := writeCharacter(context.Background(), db, c); err != nil {
		t.Fatal(err)
	}
	p, err := db.GetProfile(context.Background(), 42)
	if err != nil || p.Membership != 1 || p.MembershipExp != expires {
		t.Fatalf("导入丢失会员有效期: %+v %v", p, err)
	}
}

func TestTargetMembershipDatabaseAndRepeatPreservePurchasedProgress(t *testing.T) {
	db := importTestDB(t)
	ctx := context.Background()
	c := importTestCharacter(t)
	if _, err := writeCharacter(ctx, db, c); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET xp=22222,gold=1500 WHERE uid=42`); err != nil {
		t.Fatal(err)
	}
	purchase, err := db.PurchaseMembership(ctx, 42, "premium_7d", "import-membership-request", 1700000000)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkTarget(db.Path); err != nil {
		t.Fatalf("拒绝了含会员订单的玩家库: %v", err)
	}
	if repeated, err := writeCharacter(ctx, db, c); err != nil || !repeated {
		t.Fatalf("会员购买后不能幂等导入: repeat=%v err=%v", repeated, err)
	}
	p, err := db.GetProfile(ctx, 42)
	if err != nil || p.XP != 22222 || p.Gold != 1000 || p.Membership != 1 || p.MembershipExp != purchase.ExpiresAt {
		t.Fatalf("重复导入覆盖会员/进度: %+v %v", p, err)
	}
	var orders int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM membership_order WHERE uid=42`).Scan(&orders); err != nil || orders != 1 {
		t.Fatalf("订单丢失: %d %v", orders, err)
	}
	if _, err := db.SQL.Exec(`CREATE TABLE unrelated_state(value TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := checkTarget(db.Path); err == nil {
		t.Fatal("新增会员表兼容后放过了未登记外来表")
	}
}
