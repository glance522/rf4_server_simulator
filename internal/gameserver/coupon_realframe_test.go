package gameserver

import (
	"context"
	"encoding/hex"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// 实机 9/1 请求体（len=153）：在标本制作商店选中「小稀有鱼奖章」，材料槽两件
// ——梅花鲈 + 梦鱼优惠券。券槽的真实 id 是 mat_coupon_chuchelo（不是
// mat_coupon_ + item_id 拼出来的），本题以此帧作为「一券一实例 + 按次消耗」
// 的验收基准，禁止再用自造树替代。
const realCraftFrameHex = "0303353234010001080000001563726166745f666973685f626f6172645f735f303103023131000003023131020001090000000f626169745f66697368466f72437574e666d2a3cab796ea2bca98dee780e3290572756666650109000000136d61745f636f75706f6e5f6368756368656c6ff838ddbc528c562ffbb7301ee9f8c7390f636f75706f6e5f6368756368656c6f00ffff00000000"

// TestRealCraftFrameCarriesCouponSlot 先证明解析层能把真实帧里的券槽读出来。
// 这一步绿了，后面「扣没扣」才有意义——否则可能是解析层根本没认出券槽。
func TestRealCraftFrameCarriesCouponSlot(t *testing.T) {
	t.Parallel()
	body, err := hex.DecodeString(realCraftFrameHex)
	if err != nil {
		t.Fatalf("基准帧 hex 损坏: %v", err)
	}
	srv := newSkillsCraftServer(t, openRecipeTestDB(t))
	tree, _, derr := proto.DecodeValue(body, 0, srv.table)
	if derr != nil {
		t.Fatalf("真实帧解码失败: %v", derr)
	}
	ctx := &Ctx{Main: 9, Sub: 1, CallID: 1, Tree: tree, DecodeOK: true, Log: logging.Discard()}
	payload := craftPayloadOf(ctx)
	if payload.Recipe != "craft_fish_board_s_01" {
		t.Fatalf("配方 = %q，期望 craft_fish_board_s_01", payload.Recipe)
	}
	var coupon *craftSlot
	for i := range payload.Materials {
		if isCouponMaterialSlot(payload.Materials[i]) {
			coupon = &payload.Materials[i]
		}
	}
	if coupon == nil {
		t.Fatalf("真实帧里没解析出券槽，materials=%+v", payload.Materials)
	}
	if coupon.ItemID != "mat_coupon_chuchelo" {
		t.Fatalf("券槽 ItemID = %q，期望 mat_coupon_chuchelo", coupon.ItemID)
	}
	if coupon.GUID != "f838ddbc528c562ffbb7301ee9f8c739" {
		t.Fatalf("券槽 GUID = %q，期望 f838ddbc528c562ffbb7301ee9f8c739", coupon.GUID)
	}
}

// TestRealCraftFrameConsumesCoupon 用实机 9/1 原始帧驱动 handler：
// 券必须被扣掉一行，且 803.f8 带上被删的券 GUID。
// 这条用例不依赖自造报文，是部署错二进制时唯一能兜住的回归。
func TestRealCraftFrameConsumesCoupon(t *testing.T) {
	t.Parallel()
	body, err := hex.DecodeString(realCraftFrameHex)
	if err != nil {
		t.Fatalf("基准帧 hex 损坏: %v", err)
	}
	db := openRecipeTestDB(t)
	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)
	srv.randFloat = fixedRolls(0.9)
	uid := int64(2100001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: 1, Userid: uid, Srv: srv, log: logging.Discard()}

	// 帧里的券 GUID 必须逐字节对上库里那一行，否则扣券分支会按「可选材料」静默跳过。
	// 这里用显式 GUID 建券，复刻实机背包里那两行的真实身份。
	const couponGUID = "f838ddbc528c562ffbb7301ee9f8c739"
	prepared, perr := srv.prepareNewItem(ctx, uid, couponChucheloItemID, "backpack", 1, couponGUID)
	if perr != nil {
		t.Fatal(perr)
	}
	if err := srv.insertPreparedRows(ctx, []*store.Item{prepared}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.CreateItem(ctx, uid, couponChucheloItemID, "backpack", 1); err != nil {
		t.Fatal(err)
	}
	if r, q := couponRowCount(t, srv, uid); r != 2 || q != 2 {
		t.Fatalf("券初始应为 2 行 2 张，实得 %d 行 %d 张", r, q)
	}
	// 帧里的鱼 GUID 必须逐字节对上真实鱼护条目，否则槽查不到鱼、制作直接失败。
	const fishGUID = "e666d2a3cab796ea2bca98dee780e329"
	entry := &store.KeepnetEntry{UID: uid, EchoGUID: fishGUID, Species: "ruffe", WeightG: 109, LengthM: .17, Cat: 2,
		KeepLevel: "level_001_lake", KeepBait: "spinner_2713", TS: 1}
	if err := srv.DB.KeepnetInsert(ctx, entry); err != nil {
		t.Fatal(err)
	}
	ktree := buildKeepnet25(entry, entry.KeepLevel, 0, "")
	ktree.Fields[9].Num = ptrU64(0)
	ktree.Fields[18].Num = ptrU64(0)
	ktree.Fields[22].Num = ptrU64(639292851529062897)
	ktree.Fields[24].Num = ptrU64(16794116)
	kfields, kerr := FieldsToJSON(ktree.Fields)
	if kerr != nil {
		t.Fatal(kerr)
	}
	if err := srv.DB.InsertItem(ctx, &store.Item{GUID: fishGUID, UID: uid, WireClass: ktree.Class, TypeID: 25,
		Container: "keepnet", Qty: 1, FieldsJSON: kfields, Durability: 1, Workmanship: 1}); err != nil {
		t.Fatal(err)
	}

	tree, _, derr := proto.DecodeValue(body, 0, srv.table)
	if derr != nil {
		t.Fatalf("真实帧解码失败: %v", derr)
	}
	box := &handlerBox{srv: srv, sess: sess, main: 9, sub: 1, tree: tree}
	out, ok := box.invoke(t)
	if !ok {
		t.Fatal("9/1 未处理")
	}
	v := decodeTreeValue(t, srv, out)
	if got := u64Of(v.Fields[0].Num); got != 1 {
		t.Fatalf("803.f0 应 1（制作成功），实得 %d", got)
	}
	if r, q := couponRowCount(t, srv, uid); r != 1 || q != 1 {
		t.Fatalf("真实帧制作一次后券应剩 1 行 1 张，实得 %d 行 %d 张", r, q)
	}
}
