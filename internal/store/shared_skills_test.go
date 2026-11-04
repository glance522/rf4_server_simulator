package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func sharedSkillStoreTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "shared-skills.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestSharedSkillPointsKeepExistingSaveBalance(t *testing.T) {
	db := sharedSkillStoreTestDB(t)
	ctx := context.Background()
	const uid = int64(42)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET level=50,xp=123456,gold=4567 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	nodes := map[int64]int64{501: 7, 503: 0, 702: 7, 708: 0, 1010: 1}
	for node := int64(1001); node <= 1009; node++ {
		nodes[node] = 7
	}
	// 原始导入记录保持单边等级；读取共享视图不批量改写旧档。
	for node, level := range nodes {
		if _, err := db.SQL.Exec(`INSERT INTO skill_node(uid,node_id,level) VALUES(?,?,?)`, uid, node, level); err != nil {
			t.Fatal(err)
		}
	}
	total, spent, bonus, available := db.SkillPointsAvailable(ctx, uid)
	if total != 113 || spent != 78 || bonus != 0 || available != 35 {
		t.Fatalf("existing points changed: total=%d spent=%d bonus=%d available=%d", total, spent, bonus, available)
	}
	projected, err := db.ListSkillNodes(ctx, uid)
	if err != nil || projected[501] != 7 || projected[503] != 7 || projected[702] != 7 || projected[708] != 7 {
		t.Fatalf("shared projection=%v err=%v", projected, err)
	}
	for _, node := range []int64{503, 708} {
		var raw int64
		if err := db.SQL.QueryRow(`SELECT level FROM skill_node WHERE uid=? AND node_id=?`, uid, node).Scan(&raw); err != nil || raw != 0 {
			t.Fatalf("read rewrote saved node %d: %d %v", node, raw, err)
		}
	}
	if err := db.SetSkillBonus(ctx, uid, 37); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.Migrate(); err != nil {
			t.Fatal(err)
		}
	}
	total, spent, bonus, available = db.SkillPointsAvailable(ctx, uid)
	if total != 113 || spent != 78 || bonus != 37 || available != 72 {
		t.Fatalf("extra points changed: total=%d spent=%d bonus=%d available=%d", total, spent, bonus, available)
	}
	p, err := db.GetProfile(ctx, uid)
	if err != nil || p.Level != 50 || p.XP != 123456 || p.Gold != 4567 {
		t.Fatalf("unrelated saved progress changed: %+v %v", p, err)
	}
}

func TestSharedSkillImportedUnequalNodesUseMaximumWithoutWriting(t *testing.T) {
	db := sharedSkillStoreTestDB(t)
	ctx := context.Background()
	const uid = int64(43)
	for node, level := range map[int64]int64{501: 2, 503: 5, 702: 3, 708: 1, 9300: 4} {
		if _, err := db.SQL.Exec(`INSERT INTO skill_node(uid,node_id,level) VALUES(?,?,?)`, uid, node, level); err != nil {
			t.Fatal(err)
		}
	}
	for node, want := range map[int64]int64{501: 5, 503: 5, 702: 3, 708: 3, 9300: 4} {
		if got, err := db.GetSkillNode(ctx, uid, node); err != nil || got != want {
			t.Fatalf("node %d=%d want=%d err=%v", node, got, want, err)
		}
	}
	if got, err := db.SkillNodeLevelSum(ctx, uid); err != nil || got != 12 {
		t.Fatalf("shared points=%d want=12 err=%v", got, err)
	}
	var old int64
	if err := db.SQL.QueryRow(`SELECT level FROM skill_node WHERE uid=? AND node_id=501`, uid).Scan(&old); err != nil || old != 2 {
		t.Fatalf("old raw row changed: %d %v", old, err)
	}
}

func TestSharedSkillNodeWritesAreAtomicAndOtherNodesStayIndependent(t *testing.T) {
	db := sharedSkillStoreTestDB(t)
	ctx := context.Background()
	const uid = int64(44)
	if _, err := db.SQL.Exec(`INSERT INTO skill_node(uid,node_id,level) VALUES(44,501,2),(44,503,5);
		CREATE TRIGGER reject_shared_second BEFORE UPDATE ON skill_node WHEN NEW.node_id=503
		BEGIN SELECT RAISE(ABORT,'test shared write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSkillNode(ctx, uid, 501, 6); err == nil {
		t.Fatal("partial shared write succeeded")
	}
	for node, want := range map[int64]int64{501: 2, 503: 5} {
		var got int64
		if err := db.SQL.QueryRow(`SELECT level FROM skill_node WHERE uid=? AND node_id=?`, uid, node).Scan(&got); err != nil || got != want {
			t.Fatalf("failed transaction changed node %d: %d %v", node, got, err)
		}
	}
	if _, err := db.SQL.Exec(`DROP TRIGGER reject_shared_second`); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSkillNode(ctx, uid, 503, 6); err != nil {
		t.Fatal(err)
	}
	for _, node := range []int64{501, 503} {
		var got int64
		if err := db.SQL.QueryRow(`SELECT level FROM skill_node WHERE uid=? AND node_id=?`, uid, node).Scan(&got); err != nil || got != 6 {
			t.Fatalf("shared write did not update %d: %d %v", node, got, err)
		}
	}
	for node, level := range map[int64]int64{9101: 2, 9102: 3} {
		if err := db.SetSkillNode(ctx, uid, node, level); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetSkillNode(ctx, uid, 9101, 4); err != nil {
		t.Fatal(err)
	}
	if got, err := db.GetSkillNode(ctx, uid, 9102); err != nil || got != 3 {
		t.Fatalf("unrelated node synchronized: %d %v", got, err)
	}
	if got, err := db.SkillNodeLevelSum(ctx, uid); err != nil || got != 13 {
		t.Fatalf("wrong point cost after shared write: %d %v", got, err)
	}
}

func TestSharedSkillProjectionDoesNotMergeCategoriesOrMutateInput(t *testing.T) {
	input := map[int64]int64{501: 2, 503: 5, 702: 1, 9101: 4, 9102: 3}
	projected := ProjectSharedSkillNodes(input)
	if input[501] != 2 || projected[501] != 5 || projected[503] != 5 || projected[702] != 1 || projected[708] != 1 {
		t.Fatalf("projection modified input or mixed categories: input=%v projection=%v", input, projected)
	}
	if CountSkillNodePoints(input) != 13 || CountSkillNodePoints(projected) != 13 {
		t.Fatal("point cost depends on duplicate stored rows")
	}
	if !reflect.DeepEqual(SharedSkillNodeIDs(1, 501), []int64{501, 503}) || !reflect.DeepEqual(SharedSkillNodeIDs(2, 708), []int64{702, 708}) {
		t.Fatal("confirmed shared pair missing")
	}
	if !reflect.DeepEqual(SharedSkillNodeIDs(1, 702), []int64{702}) || !reflect.DeepEqual(SharedSkillNodeIDs(3, 9101), []int64{9101}) {
		t.Fatal("unconfirmed category or group was merged")
	}
	db := sharedSkillStoreTestDB(t)
	if got, err := db.SkillNodeLevelSum(context.Background(), 99); err != nil || got != 0 {
		t.Fatalf("empty character points=%d err=%v", got, err)
	}
}
