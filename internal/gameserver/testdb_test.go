package gameserver

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"rf4server/internal/store"
)

var gameserverTestDBRoot string

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "rf4-gameserver-test-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	gameserverTestDBRoot = root
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}

type testDBTemplateKind int

const (
	testDBMigrated testDBTemplateKind = iota
	testDBItems
	testDBSkills
	testDBRecipes
)

var testDBTemplates = [...]struct {
	once sync.Once
	path string
	err  error
}{}

func testDBTemplatePath(kind testDBTemplateKind) (string, error) {
	if kind < 0 || int(kind) >= len(testDBTemplates) {
		return "", fmt.Errorf("未知测试数据库模板: %d", kind)
	}
	slot := &testDBTemplates[kind]
	slot.once.Do(func() {
		names := map[testDBTemplateKind]string{
			testDBMigrated: "migrated.db",
			testDBItems:    "item-def.db",
			testDBSkills:   "skills.db",
			testDBRecipes:  "recipes.db",
		}
		slot.path = filepath.Join(gameserverTestDBRoot, names[kind])
		slot.err = buildTestDBTemplate(slot.path, testDBTemplateTables(kind))
	})
	return slot.path, slot.err
}

func testDBTemplateTables(kind testDBTemplateKind) []string {
	switch kind {
	case testDBItems:
		return []string{"item_def"}
	case testDBSkills:
		return []string{"item_def", "skill_def", "skill_cat_def"}
	case testDBRecipes:
		return []string{"item_def", "recipe"}
	default:
		return nil
	}
}

func buildTestDBTemplate(path string, staticTables []string) error {
	db, err := store.Open(path)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		return err
	}
	if len(staticTables) > 0 {
		staticPath := filepath.Join("..", "..", "server_data", "static.sqlite3")
		if _, err := db.SQL.Exec(`ATTACH DATABASE ? AS src`, staticPath); err != nil {
			return fmt.Errorf("附加静态库: %w", err)
		}
		for _, table := range staticTables {
			if _, err := db.SQL.Exec(`INSERT OR IGNORE INTO main.` + table + ` SELECT * FROM src.` + table); err != nil {
				_, _ = db.SQL.Exec(`DETACH DATABASE src`)
				return fmt.Errorf("导入静态表 %s: %w", table, err)
			}
		}
		if _, err := db.SQL.Exec(`DETACH DATABASE src`); err != nil {
			return err
		}
	}
	_, _ = db.SQL.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return nil
}

func seedTestDBFile(t *testing.T, path string, kind testDBTemplateKind) {
	t.Helper()
	template, err := testDBTemplatePath(kind)
	if err != nil {
		t.Skipf("准备测试数据库模板: %v", err)
	}
	copyTestDBFile(t, template, path)
}

func openMigratedTestDB(t *testing.T) *store.DB {
	t.Helper()
	return openTestDBFromTemplate(t, testDBMigrated)
}

func openItemTestDB(t *testing.T) *store.DB {
	t.Helper()
	return openTestDBFromTemplate(t, testDBItems)
}

func openSkillCraftTestDB(t *testing.T) *store.DB {
	t.Helper()
	return openTestDBFromTemplate(t, testDBSkills)
}

func openRecipeTestDB(t *testing.T) *store.DB {
	t.Helper()
	return openTestDBFromTemplate(t, testDBRecipes)
}

func openTestDBFromTemplate(t *testing.T, kind testDBTemplateKind) *store.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	seedTestDBFile(t, path, kind)
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("打开测试 SQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func copyTestDBFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("打开数据库模板: %v", err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatalf("创建测试数据库: %v", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatalf("复制数据库模板: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("关闭测试数据库: %v", err)
	}
}

const fastBiteDelaySeconds = 0.5

func enableFastBites(t *testing.T, db *store.DB, srv *Server) {
	t.Helper()
	if err := db.TunableSet(t.Context(), "bite_delay_fixed", "0.5", "test"); err != nil {
		t.Fatalf("设置快速咬钩: %v", err)
	}
	srv.tunMu.Lock()
	srv.tunCache = map[string]float64{"bite_delay_fixed": fastBiteDelaySeconds}
	srv.tunAt = time.Now()
	srv.tunMu.Unlock()
}

func backdateCastForFastBite(t *testing.T, srv *Server, uid int64) {
	t.Helper()
	st := srv.stateFor(uid)
	st.mu.Lock()
	defer st.mu.Unlock()
	st.CastTs = time.Now().Add(-13 * time.Second)
	if st.FallSeen && !st.FallTs.IsZero() {
		st.FallTs = st.CastTs
	}
	st.RigSettled = true
}
