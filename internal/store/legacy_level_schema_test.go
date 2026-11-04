package store

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
)

func TestLegacyLevelSchemaCreatesOnlyEmptyMetadata(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "legacy-schema.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTableOwnership(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"level_reward_state", "level_reward_receipt", "profile", "item", "mail", "txn"} {
		assertCount(t, db, "SELECT COUNT(*) FROM "+table, 0)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM tunables WHERE key LIKE 'level_reward%'`, 0)
	assertCount(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=31 AND name='migrations/00031_level_rewards.sql'`, 1)
	if _, err := db.SQL.Exec(`INSERT INTO level_reward_state VALUES(99,20,20,1,2);
		INSERT INTO level_reward_state VALUES(101,1,58,1,2);
		INSERT INTO level_reward_state VALUES(102,58,58,1,2);
		INSERT INTO level_reward_receipt VALUES(99,21,0,NULL,3);
		INSERT INTO level_reward_receipt VALUES(101,2,0,NULL,3);
		INSERT INTO level_reward_receipt VALUES(101,58,0,NULL,3)`); err != nil {
		t.Fatal(err)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM level_reward_receipt WHERE uid=99 AND mail_id IS NULL`, 1)
	for _, query := range []string{
		`INSERT INTO level_reward_state VALUES(100,0,1,1,2)`,
		`INSERT INTO level_reward_state VALUES(100,59,59,1,2)`,
		`INSERT INTO level_reward_state VALUES(100,20,19,1,2)`,
		`INSERT INTO level_reward_state VALUES(100,20,20,NULL,2)`,
		`INSERT INTO level_reward_receipt VALUES(100,1,0,NULL,3)`,
		`INSERT INTO level_reward_receipt VALUES(100,59,0,NULL,3)`,
		`INSERT INTO level_reward_receipt VALUES(100,21,-1,NULL,3)`,
		`INSERT INTO level_reward_receipt VALUES(100,21,0,NULL,NULL)`,
	} {
		if _, err := db.SQL.Exec(query); err == nil {
			t.Fatal("accepted invalid legacy metadata")
		}
	}
}

func TestLegacyLevelSchemaUpgradePreservesSavesWithoutAwards(t *testing.T) {
	for _, version := range []int64{8, 17, 30} {
		t.Run(strconv.FormatInt(version, 10), func(t *testing.T) {
			db := openCatchHistoryDBAtVersion(t, version)
			if _, err := db.SQL.Exec(`INSERT INTO profile(uid,name,level,xp,silver,gold) VALUES(81,'Legacy',25,123,456,789);
				INSERT INTO item(guid,uid,item_id) VALUES('legacy-item',81,'legacy-rod');
				INSERT INTO mail(uid,text,read) VALUES(81,'Legacy mail',0);
				INSERT INTO kv(k,v) VALUES('legacy-note','keep')`); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := db.Migrate(); err != nil {
					t.Fatal(err)
				}
			}
			if err := ValidateTableOwnership(context.Background(), db); err != nil {
				t.Fatal(err)
			}
			assertCount(t, db, `SELECT COUNT(*) FROM profile WHERE uid=81 AND level=25 AND xp=123 AND silver=456 AND gold=789`, 1)
			assertCount(t, db, `SELECT COUNT(*) FROM item WHERE guid='legacy-item' AND uid=81`, 1)
			assertCount(t, db, `SELECT COUNT(*) FROM mail WHERE uid=81 AND text='Legacy mail' AND read=0`, 1)
			assertCount(t, db, `SELECT COUNT(*) FROM kv WHERE k='legacy-note' AND v='keep'`, 1)
			assertCount(t, db, `SELECT COUNT(*) FROM level_reward_state WHERE uid=81 AND baseline_level=25 AND high_water_level=25`, 1)
			assertCount(t, db, `SELECT COUNT(*) FROM level_reward_receipt`, 0)
			assertCount(t, db, `SELECT COUNT(*) FROM txn`, 0)
			assertCount(t, db, `SELECT COUNT(*) FROM tunables WHERE key LIKE 'level_reward%'`, 0)
		})
	}
}

func TestLegacyLevelSchemaKeepsExistingVersion31ReceiptsAfterRestart(t *testing.T) {
	db := openCatchHistoryDBAtVersion(t, 31)
	if _, err := db.SQL.Exec(`INSERT INTO profile(uid,name,level,xp,silver,gold) VALUES(84,'Legacy',26,123,456,900);
		INSERT INTO item(guid,uid,item_id) VALUES('legacy-equipment',84,'legacy-rod');
		INSERT INTO mail(id,uid,text,read) VALUES(91,84,'Saved mail',1);
		INSERT INTO level_reward_state VALUES(84,20,25,11,12);
		INSERT INTO level_reward_receipt VALUES(84,24,7,91,13);
		INSERT INTO level_reward_receipt VALUES(84,25,0,NULL,14)`); err != nil {
		t.Fatal(err)
	}
	path := db.Path
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for range 2 {
		if err := reopened.Migrate(); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateTableOwnership(context.Background(), reopened); err != nil {
		t.Fatal(err)
	}
	assertCount(t, reopened, `SELECT COUNT(*) FROM level_reward_state WHERE uid=84 AND baseline_level=20 AND high_water_level=25 AND created_ts=11 AND updated_ts=12`, 1)
	assertCount(t, reopened, `SELECT COUNT(*) FROM level_reward_receipt WHERE uid=84 AND level=24 AND gold=7 AND mail_id=91 AND created_ts=13`, 1)
	assertCount(t, reopened, `SELECT COUNT(*) FROM level_reward_receipt WHERE uid=84 AND level=25 AND gold=0 AND mail_id IS NULL AND created_ts=14`, 1)
	assertCount(t, reopened, `SELECT COUNT(*) FROM profile WHERE uid=84 AND level=26 AND xp=123 AND silver=456 AND gold=900`, 1)
	assertCount(t, reopened, `SELECT COUNT(*) FROM item WHERE guid='legacy-equipment' AND uid=84`, 1)
	assertCount(t, reopened, `SELECT COUNT(*) FROM mail WHERE id=91 AND text='Saved mail' AND read=1`, 1)
	assertCount(t, reopened, `SELECT COUNT(*) FROM txn`, 0)
}

func TestLegacyLevelSchemaFailureRollsBackAndRetries(t *testing.T) {
	db := openCatchHistoryDBAtVersion(t, 30)
	if _, err := db.SQL.Exec(`INSERT INTO profile(uid,level,gold) VALUES(82,30,1200);
		CREATE TRIGGER reject_legacy_level_schema BEFORE INSERT ON schema_migrations
		WHEN NEW.name='migrations/00031_level_rewards.sql'
		BEGIN SELECT RAISE(ABORT,'migration unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err == nil {
		t.Fatal("migration failure was ignored")
	}
	assertCount(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE name IN ('level_reward_state','level_reward_receipt')`, 0)
	assertCount(t, db, `SELECT COUNT(*) FROM profile WHERE uid=82 AND level=30 AND gold=1200`, 1)
	if _, err := db.SQL.Exec(`DROP TRIGGER reject_legacy_level_schema`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM profile WHERE uid=82 AND level=30 AND gold=1200`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM level_reward_receipt`, 0)
}
