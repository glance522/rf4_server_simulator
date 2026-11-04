package gameserver

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func effectiveSkillTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	db := connectTestDB(t)
	importStaticForSkillsCraft(t, db, "item_def", "skill_def", "skill_cat_def")
	srv := NewServerForGearTest(t, db)
	if _, err := db.EnsureProfile(context.Background(), 1000001); err != nil {
		t.Fatal(err)
	}
	rod, err := srv.CreateItem(context.Background(), 1000001, "boatr_10222_70_100", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	return srv, rod.GUID
}

func TestEffectiveFishingSkillsUseOnlyCurrentOwnedRod(t *testing.T) {
	srv, gear := effectiveSkillTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if err := srv.DB.AddSkillPoints(ctx, uid, 11, 60); err != nil {
		t.Fatal(err)
	}
	if err := srv.DB.SetSkillNode(ctx, uid, 1004, 2); err != nil {
		t.Fatal(err)
	}
	base := srv.fishingBonus(ctx, uid, 55, "")
	withRod := srv.fishingBonus(ctx, uid, 55, gear)
	effects, err := srv.effectiveFishingSkills(ctx, uid, gear)
	if err != nil {
		t.Fatal(err)
	}
	if effects.Mastery[11] != 60 || effects.GearMastery[11] != 5 || effects.mastery(11) != 65 || effects.GearNodes[1004] != 1 {
		t.Fatalf("real rod bonuses were not separated: %+v", effects)
	}
	if effects.nodeLevel(skillNodeDef{NodeID: 1004, CatID: 11, MaxLevel: 3, UnlockPct: 25}) != 3 {
		t.Fatal("temporary node rank did not combine with the learned rank")
	}
	equippedCat := srv.skillCategory312(11, effects)
	if got := u32Of(skillNodeForTest(t, equippedCat, 1004).Field(3).Num); got != 3 {
		t.Fatalf("equipped node rank sent to client=%d, want 3", got)
	}
	if withRod.BiteSpeed >= base.BiteSpeed || srv.fishingBonus(ctx, uid, 5, gear) != neutralFishingSkillBonus() {
		t.Fatal("rod mastery did not affect only its actual fishing category")
	}
	if got := srv.fishingBonus(ctx, uid, 55, ""); got != base {
		t.Fatal("removing rod retained a temporary bonus")
	}
	foreign, err := srv.effectiveFishingSkills(ctx, uid+1, gear)
	if err != nil || len(foreign.GearNodes) != 0 || len(foreign.GearMastery) != 0 {
		t.Fatalf("foreign rod gave bonuses: %+v %v", foreign, err)
	}
	if err := srv.DB.SetSkillNode(ctx, uid, 1004, 0); err != nil {
		t.Fatal(err)
	}
	if got := srv.fishingBonus(ctx, uid, 55, gear); got != withRod {
		t.Fatal("unknown node meaning added a generic fishing bonus")
	}
	_, mastery, nodes := srv.castSkillStats(ctx, uid, 55, gear)
	if mastery != 65 || nodes != 0 {
		t.Fatalf("casting ignored gear mastery or invented node effects: %v %v", mastery, nodes)
	}
	if pts, _ := srv.DB.GetSkillPoints(ctx, uid, 11); pts != 60 {
		t.Fatal("temporary mastery was written to permanent proficiency")
	}
	if _, err := srv.DB.SQL.Exec(`UPDATE item SET durability=0 WHERE guid=?`, gear); err != nil {
		t.Fatal(err)
	}
	broken, err := srv.effectiveFishingSkills(ctx, uid, gear)
	if err != nil || len(broken.GearNodes) != 0 || len(broken.GearMastery) != 0 || srv.fishingBonus(ctx, uid, 55, gear) != base {
		t.Fatal("broken rod retained temporary skill bonuses")
	}
	brokenCat := srv.skillCategory312(11, broken)
	if got := u32Of(skillNodeForTest(t, brokenCat, 1004).Field(3).Num); got != 2 {
		t.Fatalf("broken rod node rank sent to client=%d, want permanent rank 2", got)
	}
}

func skillCategoryForTest(t *testing.T, tree *proto.Value, cat int64) *proto.Value {
	t.Helper()
	for _, v := range tree.Field(1).Val.Items {
		if v.Kind == proto.KindObj && int64(u32Of(v.Field(0).Num)) == cat {
			return v
		}
	}
	t.Fatalf("category %d absent", cat)
	return nil
}

func skillNodeForTest(t *testing.T, cat *proto.Value, node int64) *proto.Value {
	t.Helper()
	for _, v := range cat.Field(3).Val.Items {
		if v.Kind == proto.KindObj && int64(u32Of(v.Field(0).Num)) == node {
			return v
		}
	}
	t.Fatalf("node %d absent", node)
	return nil
}

func TestGearUnlocks110WithoutCreatingPermanentProficiencyOrPoints(t *testing.T) {
	srv, gear := effectiveSkillTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	def, err := srv.DB.GetItemDefByItemID(ctx, "boatr_10222_70_100")
	if err != nil {
		t.Fatal(err)
	}
	attrs := defAttrs(def)
	entries, _ := attrs["dmcgapifbdm"].([]any)
	attrs["dmcgapifbdm"] = append(entries, map[string]any{"fopbhlmkcpe": 3, "pihmbmniond": 5, "oalocpjcokm": proto.F32Bits(10)})
	raw, _ := json.Marshal(attrs)
	if _, err := srv.DB.SQL.Exec(`UPDATE item_def SET attrs_json=? WHERE item_id=?`, string(raw), def.ItemID); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.DB.SQL.Exec(`UPDATE profile SET level=10 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	if err := srv.DB.AddSkillPoints(ctx, uid, 5, 100); err != nil {
		t.Fatal(err)
	}
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.GearGuid = gear
	st.mu.Unlock()
	tree, err := srv.BuildSkillTree311(ctx, uid, ^uint64(0))
	if err != nil {
		t.Fatal(err)
	}
	cat := skillCategoryForTest(t, tree, 5)
	if proto.F32FromBits(u32Of(cat.Field(1).Num)) != 100 || proto.F32FromBits(u32Of(cat.Field(2).Num)) != 10 || u32Of(skillNodeForTest(t, cat, 814).Field(6).Num) != 1 {
		t.Fatal("110 threshold, permanent mastery and equipment mastery differ in the tree")
	}
	if ok, reason := srv.skillLearn(ctx, uid, 5, 814); !ok {
		t.Fatalf("effective 110 should permit learning: %s", reason)
	}
	st.mu.Lock()
	st.GearGuid = ""
	st.mu.Unlock()
	if ok, reason := srv.skillLearn(ctx, uid, 5, 814); ok || reason != "locked" {
		t.Fatalf("unequipped gear continued to satisfy 110: %v %s", ok, reason)
	}
	if pts, _ := srv.DB.GetSkillPoints(ctx, uid, 5); pts != 100 {
		t.Fatal("gear raised permanent mastery above 100")
	}
	if level, _ := srv.DB.GetSkillNode(ctx, uid, 814); level != 1 || srv.skillAvailablePoints(ctx, uid) != 8 {
		t.Fatal("unequipping lost learned rank or refunded allocated points")
	}
	cat = srv.buildSingleSkill312(ctx, uid, 5)
	node := skillNodeForTest(t, cat, 814)
	if u32Of(cat.Field(2).Num) != 0 || u32Of(node.Field(3).Num) != 1 || u32Of(node.Field(6).Num) != 0 {
		t.Fatal("single-category update differs from permanent ranks or equipment unlocks")
	}
	if !nodeUnlocked(skillNodeDef{UnlockPct: 110}, 100, 50) || nodeUnlocked(skillNodeDef{UnlockPct: 111}, 100, 50) {
		t.Fatal("effective proficiency is not capped at 110")
	}
}

func TestGearSkillResetPreservesTemporaryRanksAndAdminPoints(t *testing.T) {
	srv, gear := effectiveSkillTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if _, err := srv.DB.SQL.Exec(`UPDATE profile SET level=10 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	if err := srv.DB.SetSkillBonus(ctx, uid, 3); err != nil {
		t.Fatal(err)
	}
	if err := srv.DB.SetSkillNode(ctx, uid, 1004, 2); err != nil {
		t.Fatal(err)
	}
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.GearGuid = gear
	st.mu.Unlock()
	tree, err := srv.BuildSkillTree311(ctx, uid, ^uint64(0))
	if err != nil || u32Of(tree.Field(0).Num) != 10 {
		t.Fatalf("tree points include equipment ranks or exclude administered points: %v %v", tree, err)
	}
	sess := &Session{ID: 1, Userid: uid, Srv: srv, log: logging.Discard()}
	if _, ok := (&handlerBox{srv: srv, sess: sess, main: 5, sub: 3, tree: proto.Args524(proto.U32(11), proto.U32(1004))}).invoke(t); !ok {
		t.Fatal("node reset was not handled")
	}
	if srv.skillAvailablePoints(ctx, uid) != 12 {
		t.Fatal("reset refunded equipment ranks or lost administrative points")
	}
	effects, err := srv.effectiveFishingSkills(ctx, uid, gear)
	if err != nil || effects.GearNodes[1004] != 1 || effects.Learned[1004] != 0 {
		t.Fatal("reset modified equipment bonuses")
	}
	if math.Abs(float64(proto.F32FromBits(u32Of(srv.buildSingleSkill312(ctx, uid, 11).Field(2).Num)))-5) > 1e-6 {
		t.Fatal("reset cleared temporary proficiency display")
	}
}
