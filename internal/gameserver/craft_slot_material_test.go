package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// craftTreeWithSlots 与 economyCraftTree 同构，但材料槽第三字段带鱼种/物品 id
// （实抓 2026-11-13：槽 = {需求id, 选中实例 guid, 鱼种/物品 id}）。公共夹具
// economyCraftTree 的 Species 恒为空串，测不出鱼种校验，故单独建树。
func craftTreeWithSlots(recipe string, slots []craftSlot, qty uint64) *proto.Value {
	arg := proto.Args(proto.S("11"))
	for _, sl := range slots {
		arg.Items = append(arg.Items, proto.Obj("bhfikaplhmh", 9,
			proto.FStr("STR", proto.S(sl.ItemID)),
			proto.FGuid(sl.GUID),
			proto.FStr("STR", proto.S(sl.Species)),
		))
	}
	return proto.Args524(proto.Obj("dhkphmjinmj", 8,
		proto.FStr("STR", proto.S(recipe)),
		proto.FVal("VAL", proto.Args(proto.S("11"))),
		proto.FVal("VAL", arg),
		proto.FOpt(0),
		proto.FStr("U16STR", nil),
		proto.FNum("U32", qty),
	))
}

// craftOKSlots 执行一次 9/1（带鱼种的槽编码）并返回 803.f0（制作成功位）。
func craftOKSlots(t *testing.T, srv *Server, uid int64, recipe string, slots []craftSlot, qty uint64) bool {
	t.Helper()
	body, ok := hCraft(economyTestCtx(srv, uid, craftTreeWithSlots(recipe, slots, qty)))
	if !ok || len(body) == 0 {
		t.Fatalf("9/1 未处理: ok=%v body=%x", ok, body)
	}
	resp := decodeTreeValue(t, srv, body)
	if len(resp.Fields) == 0 || resp.Fields[0].Num == nil {
		t.Fatalf("803 缺少 f0: %+v", resp)
	}
	return u64Of(resp.Fields[0].Num) != 0
}

// seedKeepnetFishWeightG 与 seedKeepnetFish 同构，但鱼重可指定（克）：
// 切鱼产量按鱼重阶梯（cutFishPiecesForWeightG），固定 1200g 测不出阶梯。
func seedKeepnetFishWeightG(t *testing.T, db *store.DB, uid int64, guid, species string, weightG int64) {
	t.Helper()
	if _, err := db.SQL.Exec(`
		INSERT INTO keepnet_items(echo_guid,uid,species,name_zh,weight_g,length_m,cat,
		                          keep_loc,keep_spot,keep_bait,keep_level,ts)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		guid, uid, species, "鱼", weightG, 0.40, 2, "loc1_02", 1, "worm", "level_001_lake", 1.0); err != nil {
		t.Fatalf("写入鱼护渔获 %s(%dg) 失败: %v", species, weightG, err)
	}
}

// TestCraftCutFishConsumesSelectedKeepnetFish 切鱼块必须吃掉选中的那尾鱼：
// 配方表零材料（craft_bait_cut_fish materials_json=[]），材料槽 guid 直接指向
// 鱼护 echo guid。不接线时会零成本无限产出（2026-11-13 实机白嫖实测）。
// 官服产量按鱼重阶梯（cutFishPiecesForWeightG）：1200g 的鱼整 1 块，不按
// 定义单位量 30 折算。
func TestCraftCutFishConsumesSelectedKeepnetFish(t *testing.T) {
	db := openRecipeTestDB(t)
	srv := newSkillsCraftServer(t, db)
	ctx0 := context.Background()
	const uid = int64(4111)
	if _, err := db.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}
	const fishGUID = "c1a00000000000000000000000000021"
	seedKeepnetFish(t, db, uid, fishGUID, "s.orfe", 1.0)

	slots := []craftSlot{{ItemID: "bait_fishForCut", GUID: fishGUID, Species: "s.orfe"}}
	if !craftOKSlots(t, srv, uid, "craft_bait_cut_fish", slots, 1) {
		t.Fatal("切鱼块应成功（803.f0=1）")
	}
	if keepnetGUIDs(t, db, uid)[fishGUID] {
		t.Fatal("选中的鱼应已从鱼护移除")
	}
	if got := countOfItem(t, srv, ctx0, uid, "fish_pieces"); got != 1 {
		t.Fatalf("1200g 的鱼应产出 1 块（按鱼重计件），实得 %d", got)
	}
}

// TestCraftCutFishPiecesScaleWithWeight 官服切鱼产量随鱼重阶梯：约整公斤
// 每公斤 1 块、不足 1 公斤 1 块（社区实测 512g→1、2kg→2、3.4kg→3，
// 2026-11-13 检索，见 spec 证据节）。3400g→3 块、700g→1 块。
func TestCraftCutFishPiecesScaleWithWeight(t *testing.T) {
	db := openRecipeTestDB(t)
	srv := newSkillsCraftServer(t, db)
	ctx0 := context.Background()
	const uid = int64(4118)
	if _, err := db.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		guid    string
		weightG int64
		pieces  int64
	}{
		{"c1a00000000000000000000000000031", 3400, 3},
		{"c1a00000000000000000000000000032", 700, 1},
	} {
		seedKeepnetFishWeightG(t, db, uid, c.guid, "s.orfe", c.weightG)
		slots := []craftSlot{{ItemID: "bait_fishForCut", GUID: c.guid, Species: "s.orfe"}}
		if !craftOKSlots(t, srv, uid, "craft_bait_cut_fish", slots, 1) {
			t.Fatalf("%dg 的鱼切鱼应成功", c.weightG)
		}
	}
	if got := countOfItem(t, srv, ctx0, uid, "fish_pieces"); got != 4 {
		t.Fatalf("3400g+700g 两条鱼应共产出 3+1=4 块，实得 %d", got)
	}
}

// TestCraftCutFishWithoutSlotFails 缺材料槽必须整体失败：不产物、不动鱼护。
func TestCraftCutFishWithoutSlotFails(t *testing.T) {
	db := openRecipeTestDB(t)
	srv := newSkillsCraftServer(t, db)
	ctx0 := context.Background()
	const uid = int64(4112)
	if _, err := db.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}
	const fishGUID = "c1a00000000000000000000000000022"
	seedKeepnetFish(t, db, uid, fishGUID, "s.orfe", 1.0)

	if craftOKSlots(t, srv, uid, "craft_bait_cut_fish", nil, 1) {
		t.Fatal("没有材料槽时切鱼必须失败")
	}
	if got := countOfItem(t, srv, ctx0, uid, "fish_pieces"); got != 0 {
		t.Fatalf("失败时不应产出鱼块，实得 %d", got)
	}
	if !keepnetGUIDs(t, db, uid)[fishGUID] {
		t.Fatal("失败时鱼护条目不该被删")
	}
}

// TestCraftCutFishRejectsSpeciesMismatch 槽里带的鱼种与鱼护条目不符时拒绝，
// 防止陈旧槽位删错鱼。
func TestCraftCutFishRejectsSpeciesMismatch(t *testing.T) {
	db := openRecipeTestDB(t)
	srv := newSkillsCraftServer(t, db)
	ctx0 := context.Background()
	const uid = int64(4113)
	if _, err := db.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}
	const fishGUID = "c1a00000000000000000000000000023"
	seedKeepnetFish(t, db, uid, fishGUID, "s.orfe", 1.0)

	slots := []craftSlot{{ItemID: "bait_fishForCut", GUID: fishGUID, Species: "perch"}}
	if craftOKSlots(t, srv, uid, "craft_bait_cut_fish", slots, 1) {
		t.Fatal("鱼种不符时切鱼必须失败")
	}
	if got := countOfItem(t, srv, ctx0, uid, "fish_pieces"); got != 0 {
		t.Fatalf("失败时不应产出鱼块，实得 %d", got)
	}
	if !keepnetGUIDs(t, db, uid)[fishGUID] {
		t.Fatal("鱼种不符时鱼护条目不该被删")
	}
}

// TestCraftCutFishKeepsKnife 切鱼刀是可复用工具，材料槽里的刀具不该被消耗。
func TestCraftCutFishKeepsKnife(t *testing.T) {
	db := openRecipeTestDB(t)
	srv := newSkillsCraftServer(t, db)
	ctx0 := context.Background()
	const uid = int64(4114)
	if _, err := db.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}
	const fishGUID = "c1a00000000000000000000000000024"
	seedKeepnetFish(t, db, uid, fishGUID, "s.orfe", 1.0)
	knife := economyTestItem(t, srv, uid, "tool_fish_knife", "backpack", 1)

	slots := []craftSlot{
		{ItemID: "bait_fishknife", GUID: knife.GUID, Species: "tool_fish_knife"},
		{ItemID: "bait_fishForCut", GUID: fishGUID, Species: "s.orfe"},
	}
	if !craftOKSlots(t, srv, uid, "craft_bait_cut_fish", slots, 1) {
		t.Fatal("带工具槽的切鱼应成功")
	}
	if got := countOfItem(t, srv, ctx0, uid, "tool_fish_knife"); got != 1 {
		t.Fatalf("切鱼刀不该被消耗，实剩 %d", got)
	}
	if keepnetGUIDs(t, db, uid)[fishGUID] {
		t.Fatal("选中的鱼应已从鱼护移除")
	}
}

// TestCraftCrushedBoilsConsumesBackpackBoilie 碎沸粒必须扣掉选中的沸粒：
// 材料槽 guid 指向背包沸粒实例，研磨机是工具不消耗。官服语义 25 粒磨 1 份
// （2026-11-13 实机制作界面显示，玩家口述）。实抓槽形见 slotConsumableForRecipe
// 注释（2026-11-13）。种子数量按定义单位折算（boils_hvz_van12 ljnnpokapig=30，
// economyTestItem(5) → 150 粒）。
func TestCraftCrushedBoilsConsumesBackpackBoilie(t *testing.T) {
	db := openRecipeTestDB(t)
	srv := newSkillsCraftServer(t, db)
	ctx0 := context.Background()
	const uid = int64(4115)
	if _, err := db.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}
	boil := economyTestItem(t, srv, uid, "boils_hvz_van12", "backpack", 5)
	grinder := economyTestItem(t, srv, uid, "hvz_b_grinder", "backpack", 1)

	slots := []craftSlot{
		{ItemID: "craft_tool_crusher", GUID: grinder.GUID, Species: "hvz_b_grinder"},
		{ItemID: "craft_mat_boil", GUID: boil.GUID, Species: "boils_hvz_van12"},
	}
	if !craftOKSlots(t, srv, uid, "craft_crushed_boils", slots, 1) {
		t.Fatal("碎沸粒应成功（803.f0=1）")
	}
	if got := countOfItem(t, srv, ctx0, uid, "boils_hvz_van12"); got != 125 {
		t.Fatalf("应扣 25 粒沸粒剩 125，实得 %d", got)
	}
	if got := countOfItem(t, srv, ctx0, uid, "crushed_boils"); got < 1 {
		t.Fatalf("应产出碎沸粒，实得 %d", got)
	}
	if got := countOfItem(t, srv, ctx0, uid, "hvz_b_grinder"); got != 1 {
		t.Fatalf("研磨机不该被消耗，实剩 %d", got)
	}
}

// TestCraftCrushedBoilsInsufficientBoiliesFails 不足 25 粒必须整体失败：
// 不产物、堆数量不动（一整堆 30 粒磨一次剩 5，第二次就不够了）。
func TestCraftCrushedBoilsInsufficientBoiliesFails(t *testing.T) {
	db := openRecipeTestDB(t)
	srv := newSkillsCraftServer(t, db)
	ctx0 := context.Background()
	const uid = int64(4116)
	if _, err := db.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}
	boil := economyTestItem(t, srv, uid, "boils_hvz_van12", "backpack", 1)

	slots := []craftSlot{{ItemID: "craft_mat_boil", GUID: boil.GUID, Species: "boils_hvz_van12"}}
	if !craftOKSlots(t, srv, uid, "craft_crushed_boils", slots, 1) {
		t.Fatal("第一次碎沸粒（30 粒堆）应成功")
	}
	if got := countOfItem(t, srv, ctx0, uid, "boils_hvz_van12"); got != 5 {
		t.Fatalf("应扣 25 剩 5，实得 %d", got)
	}
	if craftOKSlots(t, srv, uid, "craft_crushed_boils", slots, 1) {
		t.Fatal("只剩 5 粒时碎沸粒必须失败")
	}
	if got := countOfItem(t, srv, ctx0, uid, "boils_hvz_van12"); got != 5 {
		t.Fatalf("失败回滚后应仍剩 5，实得 %d", got)
	}
	if got := countOfItem(t, srv, ctx0, uid, "crushed_boils"); got != 1 {
		t.Fatalf("应只产出 1 份碎沸粒，实得 %d", got)
	}
}

// TestCraftCrushedBoilsWithoutBoilieFails 沸粒不存在时必须失败且不产物。
func TestCraftCrushedBoilsWithoutBoilieFails(t *testing.T) {
	db := openRecipeTestDB(t)
	srv := newSkillsCraftServer(t, db)
	ctx0 := context.Background()
	const uid = int64(4117)
	if _, err := db.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}

	slots := []craftSlot{{ItemID: "craft_mat_boil", GUID: "6d0000000000000000000000000000ff", Species: "boils_hvz_van12"}}
	if craftOKSlots(t, srv, uid, "craft_crushed_boils", slots, 1) {
		t.Fatal("沸粒不存在时碎沸粒必须失败")
	}
	if got := countOfItem(t, srv, ctx0, uid, "crushed_boils"); got != 0 {
		t.Fatalf("失败时不应产出碎沸粒，实得 %d", got)
	}
}
