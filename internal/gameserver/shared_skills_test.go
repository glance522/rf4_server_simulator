package gameserver

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"rf4server/internal/proto"
)

const sharedSkillUID = int64(1000001)

func TestSharedSkillPartialWriteAndResetRollBackTogether(t *testing.T) {
	srv := sharedSkillTestServer(t, 10)
	ctx := t.Context()
	if _, err := srv.DB.SQL.Exec(`CREATE TRIGGER fail_shared_insert BEFORE INSERT ON skill_node
		WHEN NEW.node_id=503 BEGIN SELECT RAISE(ABORT,'test shared insert'); END`); err != nil {
		t.Fatal(err)
	}
	if ok, _ := srv.skillLearn(ctx, sharedSkillUID, 1, 501); ok {
		t.Fatal("partial shared write succeeded")
	}
	var rows int
	if err := srv.DB.SQL.QueryRow(`SELECT COUNT(*) FROM skill_node WHERE uid=? AND node_id IN(501,503)`, sharedSkillUID).Scan(&rows); err != nil || rows != 0 || srv.skillAvailablePoints(ctx, sharedSkillUID) != 9 {
		t.Fatalf("failed learning left half a group or consumed points: rows=%d err=%v", rows, err)
	}
	if _, err := srv.DB.SQL.Exec(`DROP TRIGGER fail_shared_insert`); err != nil {
		t.Fatal(err)
	}
	if ok, reason := srv.skillLearn(ctx, sharedSkillUID, 1, 501); !ok {
		t.Fatal(reason)
	}
	if _, err := srv.DB.SQL.Exec(`CREATE TRIGGER fail_shared_delete BEFORE DELETE ON skill_node
		WHEN OLD.node_id=503 BEGIN SELECT RAISE(ABORT,'test shared reset'); END`); err != nil {
		t.Fatal(err)
	}
	node := int64(501)
	if err := srv.resetSkillNodes(ctx, sharedSkillUID, &node); err == nil {
		t.Fatal("partial shared reset succeeded")
	}
	var sum int64
	if err := srv.DB.SQL.QueryRow(`SELECT COUNT(*),SUM(level) FROM skill_node WHERE uid=? AND node_id IN(501,503)`, sharedSkillUID).Scan(&rows, &sum); err != nil || rows != 2 || sum != 2 || srv.skillAvailablePoints(ctx, sharedSkillUID) != 8 {
		t.Fatalf("failed reset changed ranks or refunded points: rows=%d sum=%d err=%v", rows, sum, err)
	}
}

func sharedSkillTestServer(t *testing.T, level int64) *Server {
	t.Helper()
	srv, db := newProgressSrv(t)
	importStaticForSkillsCraft(t, db, "item_def", "skill_def", "skill_cat_def")
	if _, err := db.EnsureProfile(t.Context(), sharedSkillUID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET level=?,xp=? WHERE uid=?`, level, baseXp(level), sharedSkillUID); err != nil {
		t.Fatal(err)
	}
	return srv
}

func sharedSkillRank(t *testing.T, srv *Server, node int64) int64 {
	t.Helper()
	rank, err := srv.DB.GetSkillNode(t.Context(), sharedSkillUID, node)
	if err != nil {
		t.Fatal(err)
	}
	return rank
}

func TestSharedSkillLearningChargesOnceAndKeepsEachUnlockThreshold(t *testing.T) {
	for _, pair := range []struct {
		cat, first, second int64
	}{{1, 501, 503}, {2, 702, 708}} {
		t.Run(fmt.Sprintf("category%d", pair.cat), func(t *testing.T) {
			srv := sharedSkillTestServer(t, 10)
			ctx := t.Context()
			if ok, reason := srv.skillLearn(ctx, sharedSkillUID, pair.cat, pair.first); !ok {
				t.Fatalf("first shared rank rejected: %s", reason)
			}
			if sharedSkillRank(t, srv, pair.first) != 1 || sharedSkillRank(t, srv, pair.second) != 1 || srv.skillAvailablePoints(ctx, sharedSkillUID) != 8 {
				t.Fatal("shared rank did not synchronize or charged twice")
			}
			tree, err := srv.BuildSkillTree311(ctx, sharedSkillUID, ^uint64(0))
			if err != nil {
				t.Fatal(err)
			}
			cat := sharedSkillCategoryForTest(t, tree, pair.cat)
			locked := sharedSkillNodeForTest(t, cat, pair.second)
			if u32Of(locked.Field(3).Num) != 1 || u32Of(locked.Field(6).Num) != 0 || u32Of(tree.Field(0).Num) != 8 {
				t.Fatal("protocol lost shared rank or unlocked the 30% node too early")
			}
			if ok, reason := srv.skillLearn(ctx, sharedSkillUID, pair.cat, pair.second); ok || reason != "locked" {
				t.Fatalf("shared peer bypassed its own threshold: %v %s", ok, reason)
			}
			if srv.skillAvailablePoints(ctx, sharedSkillUID) != 8 {
				t.Fatal("denied learning consumed points")
			}
			if err := srv.DB.AddSkillPoints(ctx, sharedSkillUID, pair.cat, 30); err != nil {
				t.Fatal(err)
			}
			if ok, reason := srv.skillLearn(ctx, sharedSkillUID, pair.cat, pair.second); !ok {
				t.Fatalf("unlocked peer was treated as mutually exclusive: %s", reason)
			}
			if sharedSkillRank(t, srv, pair.first) != 2 || sharedSkillRank(t, srv, pair.second) != 2 || srv.skillAvailablePoints(ctx, sharedSkillUID) != 7 {
				t.Fatal("learning the peer did not advance the same shared rank")
			}
			cat = srv.buildSingleSkill312(ctx, sharedSkillUID, pair.cat)
			peer := sharedSkillNodeForTest(t, cat, pair.second)
			if u32Of(peer.Field(3).Num) != 2 || u32Of(peer.Field(6).Num) != 1 {
				t.Fatal("single-category protocol differs from the shared tree")
			}
		})
	}
}

func TestSharedSkillResetRefundsOneAllocationAndPreservesExtraPoints(t *testing.T) {
	srv := sharedSkillTestServer(t, 10)
	ctx := t.Context()
	if err := srv.DB.SetSkillBonus(ctx, sharedSkillUID, 20); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		if ok, reason := srv.skillLearn(ctx, sharedSkillUID, 1, 501); !ok {
			t.Fatal(reason)
		}
	}
	for range 2 {
		if ok, reason := srv.skillLearn(ctx, sharedSkillUID, 2, 702); !ok {
			t.Fatal(reason)
		}
	}
	if srv.skillAvailablePoints(ctx, sharedSkillUID) != 23 || sharedSkillRank(t, srv, 503) != 4 || sharedSkillRank(t, srv, 708) != 2 {
		t.Fatal("same group number merged across fishing categories or extra points changed")
	}
	peer := int64(503)
	if err := srv.resetSkillNodes(ctx, sharedSkillUID, &peer); err != nil {
		t.Fatal(err)
	}
	if sharedSkillRank(t, srv, 501) != 0 || sharedSkillRank(t, srv, 503) != 0 || sharedSkillRank(t, srv, 702) != 2 || srv.skillAvailablePoints(ctx, sharedSkillUID) != 27 {
		t.Fatal("reset did not clear the selected shared pair and refund exactly four points")
	}
	if err := srv.resetSkillNodes(ctx, sharedSkillUID, &peer); err != nil {
		t.Fatal(err)
	}
	if srv.skillAvailablePoints(ctx, sharedSkillUID) != 27 {
		t.Fatal("repeated shared reset refunded points again")
	}
	if err := srv.resetSkillNodes(ctx, sharedSkillUID, nil); err != nil {
		t.Fatal(err)
	}
	if srv.skillAvailablePoints(ctx, sharedSkillUID) != 29 || srv.skillBonusPoints(ctx, sharedSkillUID) != 20 {
		t.Fatal("reset lost extra points or returned duplicate shared allocations")
	}
	if skillPointsTotal(58) != 153 {
		t.Fatal("shared rules changed the original level-point schedule")
	}
}

func TestSharedSkillConcurrentLearningHonorsBudgetAndMaximumRank(t *testing.T) {
	for _, tc := range []struct {
		level, want int64
	}{{2, 1}, {10, 7}} {
		t.Run(fmt.Sprintf("level%d", tc.level), func(t *testing.T) {
			srv := sharedSkillTestServer(t, tc.level)
			ctx := t.Context()
			if err := srv.DB.AddSkillPoints(ctx, sharedSkillUID, 1, 30); err != nil {
				t.Fatal(err)
			}
			var successes atomic.Int64
			var wg sync.WaitGroup
			for i := range 16 {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					node := int64(501)
					if i%2 != 0 {
						node = 503
					}
					if ok, _ := srv.skillLearn(ctx, sharedSkillUID, 1, node); ok {
						successes.Add(1)
					}
				}(i)
			}
			wg.Wait()
			if successes.Load() != tc.want || sharedSkillRank(t, srv, 501) != tc.want || sharedSkillRank(t, srv, 503) != tc.want || srv.skillAvailablePoints(ctx, sharedSkillUID) != skillPointsTotal(tc.level)-tc.want {
				t.Fatalf("shared concurrent learning lost ranks or overspent: successes=%d first=%d second=%d avail=%d", successes.Load(), sharedSkillRank(t, srv, 501), sharedSkillRank(t, srv, 503), srv.skillAvailablePoints(ctx, sharedSkillUID))
			}
		})
	}
}

func TestSharedSkillLegacyReadPreservesCurrentPointBalanceAndRawRecords(t *testing.T) {
	srv := sharedSkillTestServer(t, 50)
	ctx := t.Context()
	if _, err := srv.DB.SQL.Exec(`INSERT INTO skill_node(uid,node_id,level) VALUES
		(1000001,501,7),(1000001,503,0),(1000001,702,7),(1000001,708,0)`); err != nil {
		t.Fatal(err)
	}
	rows, err := srv.DB.SQL.Query(`SELECT node_id,max_level FROM skill_def WHERE node_id NOT IN (501,503,702,708) AND max_level>0 ORDER BY cat_id,node_id`)
	if err != nil {
		t.Fatal(err)
	}
	type entry struct{ id, max int64 }
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.max); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	remaining := int64(64)
	for _, e := range entries {
		if remaining == 0 {
			break
		}
		rank := min(remaining, e.max)
		if _, err := srv.DB.SQL.Exec(`INSERT INTO skill_node(uid,node_id,level) VALUES(?,?,?)`, sharedSkillUID, e.id, rank); err != nil {
			t.Fatal(err)
		}
		remaining -= rank
	}
	if remaining != 0 {
		t.Fatal("insufficient real skill definitions for the legacy allocation fixture")
	}
	for range 2 {
		total, spent, bonus, available := srv.DB.SkillPointsAvailable(ctx, sharedSkillUID)
		if total != 113 || spent != 78 || bonus != 0 || available != 35 || srv.skillAvailablePoints(ctx, sharedSkillUID) != 35 {
			t.Fatalf("legacy balance changed: %d/%d/%d/%d", total, spent, bonus, available)
		}
		tree, err := srv.BuildSkillTree311(ctx, sharedSkillUID, ^uint64(0))
		if err != nil || u32Of(tree.Field(0).Num) != 35 {
			t.Fatalf("tree balance changed: %v %v", tree, err)
		}
		for _, pair := range []struct{ cat, first, second int64 }{{1, 501, 503}, {2, 702, 708}} {
			cat := sharedSkillCategoryForTest(t, tree, pair.cat)
			if u32Of(sharedSkillNodeForTest(t, cat, pair.first).Field(3).Num) != 7 || u32Of(sharedSkillNodeForTest(t, cat, pair.second).Field(3).Num) != 7 {
				t.Fatal("legacy shared ranks were not projected in both protocol nodes")
			}
		}
		profile, err := srv.BuildProfile307(ctx, sharedSkillUID)
		if err != nil || u32Of(profile.Field(6).Num) != 113 {
			t.Fatalf("profile total changed: %v %v", profile, err)
		}
	}
	for _, node := range []int64{503, 708} {
		var rank int64
		if err := srv.DB.SQL.QueryRow(`SELECT level FROM skill_node WHERE uid=? AND node_id=?`, sharedSkillUID, node).Scan(&rank); err != nil || rank != 0 {
			t.Fatalf("read path rewrote legacy node %d: rank=%d err=%v", node, rank, err)
		}
	}
}

func sharedSkillCategoryForTest(t *testing.T, tree *proto.Value, cat int64) *proto.Value {
	t.Helper()
	for _, v := range tree.Field(1).Val.Items {
		if v.Kind == proto.KindObj && int64(u32Of(v.Field(0).Num)) == cat {
			return v
		}
	}
	t.Fatalf("category %d absent", cat)
	return nil
}

func sharedSkillNodeForTest(t *testing.T, cat *proto.Value, node int64) *proto.Value {
	t.Helper()
	for _, v := range cat.Field(3).Val.Items {
		if v.Kind == proto.KindObj && int64(u32Of(v.Field(0).Num)) == node {
			return v
		}
	}
	t.Fatalf("node %d absent", node)
	return nil
}

func TestSharedSkillEffectsCountOneGroup(t *testing.T) {
	srv := sharedSkillTestServer(t, 10)
	ctx := t.Context()
	if err := srv.DB.SetSkillNode(ctx, sharedSkillUID, 501, 2); err != nil {
		t.Fatal(err)
	}
	spent, err := srv.DB.SkillNodeLevelSum(ctx, sharedSkillUID)
	if err != nil || spent != 2 {
		t.Fatalf("shared group points=%d err=%v, want 2", spent, err)
	}
	// 节点效果含义尚未确认，合成技能只使用熟练度，不重复叠加节点通用效果。
	_, mastery, effect := srv.castSkillStats(ctx, sharedSkillUID, 1)
	bonus := srv.fishingBonus(ctx, sharedSkillUID, 1)
	if mastery != 0 || effect != 0 || bonus != neutralFishingSkillBonus() {
		t.Fatalf("shared skill gained an unconfirmed generic effect: mastery=%v effect=%v bonus=%+v", mastery, effect, bonus)
	}
}
