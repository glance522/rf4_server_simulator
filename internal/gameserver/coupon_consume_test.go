package gameserver

import (
	"context"
	"fmt"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// coupon_consume_test.go 覆盖梦鱼优惠券（coupon_chuchelo）的三条行为：
//   1. 一券一实例——券的实例类 ahgeldclmdj 没有客户端 UI 数量槽（ljnnpokapig），
//      背包只按行渲染，所以张数必须用行数表达，发放几件就落几行、每行 1 张。
//   2. 标本制作按次扣券——客户端 9/1 材料槽带 mat_coupon_chuchelo + 券 GUID，
//      服务端必须扣一张（静态配方 materials_json 全为空，会走 len(demands)==0
//      分支直接跳过，不能指望那条路径扣券）。
//   3. 存量展开迁移——邮件发的 999 张可能被压成 1 行，迁移按发放审计与制作流水
//      把总张数收敛后铺成多行，重复执行保持幂等。

// couponRowCount 返回玩家券行数与总张数（一行即一张，故两者应相等）。
func couponRowCount(t *testing.T, srv *Server, uid int64) (rows int, qty int64) {
	t.Helper()
	items, err := srv.DB.ListItemsByContainers(context.Background(), uid, []string{"backpack", "rig"})
	if err != nil {
		t.Fatalf("列出背包失败: %v", err)
	}
	for _, it := range items {
		if it.ItemID == couponChucheloItemID {
			rows++
			qty += srv.stackCountOf(context.Background(), it)
		}
	}
	return rows, qty
}

// grantCoupons 按「一张一个实例」发放 count 张券，返回新建的行。
func grantCoupons(t *testing.T, srv *Server, uid, count int64) []*store.Item {
	t.Helper()
	rows, err := srv.grantItemUnits(context.Background(), uid, couponChucheloItemID, "backpack", count)
	if err != nil {
		t.Fatalf("发放券失败: %v", err)
	}
	if int64(len(rows)) != count {
		t.Fatalf("发放 %d 张券应建 %d 行，实得 %d 行", count, count, len(rows))
	}
	return rows
}

// craftBoardWithCoupon 用一段鱼护渔获 + 券槽发起标本板制作，返回 803 响应树。
func craftBoardWithCoupon(t *testing.T, box *handlerBox, srv *Server, uid int64,
	recipe string, couponGUID, fishGUID, species string) *proto.Value {
	t.Helper()
	slots := []craftSlot{}
	if couponGUID != "" {
		// 真实 9/1 帧里券槽是 mat_coupon_chuchelo（不是 mat_coupon_ + coupon_chuchelo），
		// 这里必须逐字节复刻，否则测的是自己编的 id，实机坏了测试照样绿。
		slots = append(slots, craftSlot{ItemID: "mat_coupon_chuchelo", GUID: couponGUID, Species: couponChucheloItemID})
	}
	if fishGUID != "" {
		slots = append(slots, craftSlot{ItemID: "bait_fishForCut", GUID: fishGUID, Species: species})
	}
	box.main, box.sub = 9, 1
	box.tree = proto.Args524(craftObj8(recipe, slots, 1))
	body, ok := box.invoke(t)
	if !ok {
		t.Fatal("9/1 未处理")
	}
	return decodeTreeValue(t, srv, body)
}

// removedGUIDs 解出 803.f8 的移除列表。
func removedGUIDs(v *proto.Value) []string {
	out := []string{}
	f8 := v.Fields[8].Val
	if f8 == nil || f8.Kind != proto.KindGuidArr {
		return out
	}
	for _, g := range f8.Items {
		out = append(out, g.Hex)
	}
	return out
}

// TestGrantCouponCreatesOneRowPerVoucher 发放 N 张券落 N 行，每行 1 张。
func TestGrantCouponCreatesOneRowPerVoucher(t *testing.T) {
	t.Parallel()
	db := openRecipeTestDB(t)
	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)
	uid := int64(2100005)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}

	rows := grantCoupons(t, srv, uid, 5)
	gotRows, gotQty := couponRowCount(t, srv, uid)
	if gotRows != 5 || gotQty != 5 {
		t.Fatalf("5 张券应为 5 行共 5 张，实得 %d 行 %d 张", gotRows, gotQty)
	}
	seen := map[string]bool{}
	for _, it := range rows {
		if seen[it.GUID] {
			t.Fatalf("券行 GUID 重复: %s", it.GUID)
		}
		seen[it.GUID] = true
		if it.WireClass != "ahgeldclmdj" || it.TypeID != 13018 {
			t.Fatalf("券行应为 ahgeldclmdj/13018，实得 %s/%d", it.WireClass, it.TypeID)
		}
		if it.Qty != 1 {
			t.Fatalf("一券一实例每行应为 1 张，实得 %d", it.Qty)
		}
	}
}

// TestCreateItemRejectsMultiUnitCoupon CreateItem 只造单个实例，券多张必须走
// grantItemUnits——否则会被压成一行，张数丢失。
func TestCreateItemRejectsMultiUnitCoupon(t *testing.T) {
	t.Parallel()
	db := openRecipeTestDB(t)
	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)
	uid := int64(2100007)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}

	if _, err := srv.CreateItem(ctx, uid, couponChucheloItemID, "backpack", 999); err == nil {
		t.Fatal("CreateItem 发 999 张券应报错，实际成功了（张数会被压掉）")
	}
	if r, q := couponRowCount(t, srv, uid); r != 0 || q != 0 {
		t.Fatalf("报错后不应落任何券行，实得 %d 行 %d 张", r, q)
	}
	// 单张仍可用，保证既有调用点不受影响。
	if _, err := srv.CreateItem(ctx, uid, couponChucheloItemID, "backpack", 1); err != nil {
		t.Fatalf("CreateItem 发 1 张券不应报错: %v", err)
	}
	if r, q := couponRowCount(t, srv, uid); r != 1 || q != 1 {
		t.Fatalf("应落 1 行 1 张，实得 %d 行 %d 张", r, q)
	}
}

// TestCraftFishBoardConsumesOneCoupon 一次标本制作扣掉一张券（删掉那一行），
// 剩余张数仍等于剩余行数，被消耗的 GUID 出现在 803.f8。
func TestCraftFishBoardConsumesOneCoupon(t *testing.T) {
	t.Parallel()
	db := openRecipeTestDB(t)
	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)
	srv.randFloat = fixedRolls(0.9)
	uid := int64(2100001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: 1, Userid: uid, Srv: srv, log: logging.Discard()}
	box := &handlerBox{srv: srv, sess: sess}

	entry, _ := seedImportedKeepnetFish(t, srv, uid, 31, false) // asp
	rows := grantCoupons(t, srv, uid, 3)
	if r, q := couponRowCount(t, srv, uid); r != 3 || q != 3 {
		t.Fatalf("券初始应为 3 行 3 张，实得 %d 行 %d 张", r, q)
	}

	consumed := rows[0].GUID
	v := craftBoardWithCoupon(t, box, srv, uid, "craft_fish_board_s_01", consumed, entry.EchoGUID, "asp")
	if got := u64Of(v.Fields[0].Num); got != 1 {
		t.Fatalf("803.f0 应 1，实得 %d", got)
	}
	if r, q := couponRowCount(t, srv, uid); r != 2 || q != 2 {
		t.Fatalf("制作一次后券应剩 2 行 2 张，实得 %d 行 %d 张", r, q)
	}
	if row, gerr := db.GetItemByGUID(ctx, consumed); gerr == nil && row != nil {
		t.Fatalf("被消耗的券行应已删除: %+v", row)
	}
	if got := removedGUIDs(v); !containsString(got, consumed) {
		t.Fatalf("803.f8 应含被消耗券 guid %s，实得 %v", consumed, got)
	}
}

// TestCraftFishBoardConsumesLastCoupon 扣掉最后一张时券行删除，803.f8 携带其 GUID。
func TestCraftFishBoardConsumesLastCoupon(t *testing.T) {
	t.Parallel()
	db := openRecipeTestDB(t)
	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)
	srv.randFloat = fixedRolls(0.9)
	uid := int64(2100002)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: 1, Userid: uid, Srv: srv, log: logging.Discard()}
	box := &handlerBox{srv: srv, sess: sess}

	entry, _ := seedImportedKeepnetFish(t, srv, uid, 31, false)
	rows := grantCoupons(t, srv, uid, 1)
	couponGUID := rows[0].GUID

	v := craftBoardWithCoupon(t, box, srv, uid, "craft_fish_board_s_01", couponGUID, entry.EchoGUID, "asp")
	if got := u64Of(v.Fields[0].Num); got != 1 {
		t.Fatalf("803.f0 应 1，实得 %d", got)
	}
	if r, q := couponRowCount(t, srv, uid); r != 0 || q != 0 {
		t.Fatalf("券应被扣空，实得 %d 行 %d 张", r, q)
	}
	if row, gerr := db.GetItemByGUID(ctx, couponGUID); gerr == nil && row != nil {
		t.Fatalf("券行应已删除，仍存在: %+v", row)
	}
	if got := removedGUIDs(v); !containsString(got, couponGUID) {
		t.Fatalf("803.f8 应含券 guid %s，实得 %v", couponGUID, got)
	}
}

// TestCraftFishBoardConsumesOnePerCraft 连续做两次标本，每次各扣一张（4→3→2）。
// 直接对齐用户的实机验收路径：背包 4 张券，做一次少一张。券槽每次带的是
// 客户端从材料选择器里点名的那一张的有效 GUID。
func TestCraftFishBoardConsumesOnePerCraft(t *testing.T) {
	t.Parallel()
	db := openRecipeTestDB(t)
	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)
	srv.randFloat = fixedRolls(0.9)
	uid := int64(2100006)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: 1, Userid: uid, Srv: srv, log: logging.Discard()}
	box := &handlerBox{srv: srv, sess: sess}

	rows := grantCoupons(t, srv, uid, 4)
	if r, q := couponRowCount(t, srv, uid); r != 4 || q != 4 {
		t.Fatalf("券初始应为 4 行 4 张，实得 %d 行 %d 张", r, q)
	}
	for i, want := range []int64{3, 2} {
		// 每次用一条独立的渔获（制作会消耗鱼护条目），点名一张仍存在的券。
		entry, _ := seedImportedKeepnetFish(t, srv, uid, 31, false)
		guid := rows[i].GUID
		v := craftBoardWithCoupon(t, box, srv, uid, "craft_fish_board_s_01", guid, entry.EchoGUID, "asp")
		if got := u64Of(v.Fields[0].Num); got != 1 {
			t.Fatalf("第 %d 次制作 803.f0 应 1，实得 %d", i+1, got)
		}
		if r, q := couponRowCount(t, srv, uid); r != int(want) || q != want {
			t.Fatalf("第 %d 次制作后应剩 %d 行 %d 张，实得 %d 行 %d 张", i+1, want, want, r, q)
		}
		if got := removedGUIDs(v); !containsString(got, guid) {
			t.Fatalf("第 %d 次 803.f8 应含券 guid %s，实得 %v", i+1, guid, got)
		}
	}
}

// TestCraftFishBoardWithoutCouponStillSucceeds 券是可选材料：不带券也能做标本。
func TestCraftFishBoardWithoutCouponStillSucceeds(t *testing.T) {
	t.Parallel()
	db := openRecipeTestDB(t)
	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)
	srv.randFloat = fixedRolls(0.9)
	uid := int64(2100003)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: 1, Userid: uid, Srv: srv, log: logging.Discard()}
	box := &handlerBox{srv: srv, sess: sess}

	entry, _ := seedImportedKeepnetFish(t, srv, uid, 31, false)
	v := craftBoardWithCoupon(t, box, srv, uid, "craft_fish_board_s_01", "", entry.EchoGUID, "asp")
	if got := u64Of(v.Fields[0].Num); got != 1 {
		t.Fatalf("无券也应制作成功，803.f0 实得 %d", got)
	}
}

// TestCraftFishBoardCouponSlotWithStaleGUIDIsIgnored 槽里券 GUID 已不存在时不阻断制作。
// 客户端缓存过期、或另一个会话先扣空了券，都会出现这种请求。
func TestCraftFishBoardCouponSlotWithStaleGUIDIsIgnored(t *testing.T) {
	t.Parallel()
	db := openRecipeTestDB(t)
	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)
	srv.randFloat = fixedRolls(0.9)
	uid := int64(2100004)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: 1, Userid: uid, Srv: srv, log: logging.Discard()}
	box := &handlerBox{srv: srv, sess: sess}

	entry, _ := seedImportedKeepnetFish(t, srv, uid, 31, false)
	v := craftBoardWithCoupon(t, box, srv, uid, "craft_fish_board_s_01",
		fmt.Sprintf("%032x", 424242), entry.EchoGUID, "asp")
	if got := u64Of(v.Fields[0].Num); got != 1 {
		t.Fatalf("券 GUID 失效不应阻断制作，803.f0 实得 %d", got)
	}
}

// seedCouponGrantAudit 写入一条发券审计（金额与 uid 由调用方给）。
func seedCouponGrantAudit(t *testing.T, db *store.DB, uid, qty int64, ts float64) {
	t.Helper()
	detail := fmt.Sprintf(
		`{"uid":%d,"text":"test","items":[{"item_id":%q,"qty":%d}]}`,
		uid, couponChucheloItemID, qty)
	if _, err := db.SQL.Exec(
		`INSERT INTO audit_log(admin,action,target,detail,ts) VALUES('admin','mail_send',?,?,?)`,
		fmt.Sprint(uid), detail, ts); err != nil {
		t.Fatalf("写入发券审计失败: %v", err)
	}
}

// seedTrophyCraftTxn 写入一条标本制作流水（ts 用于区分是否在发券之后）。
func seedTrophyCraftTxn(t *testing.T, db *store.DB, uid int64, output string, ts float64) {
	t.Helper()
	if _, err := db.SQL.Exec(
		`INSERT INTO txn(uid,ts,kind,item_id,silver_delta,gold_delta,note) VALUES(?,?,?,?,0,0,?)`,
		uid, ts, "craft", output, "recipe=craft_"+output+" qty=1"); err != nil {
		t.Fatalf("写入制作流水失败: %v", err)
	}
}

// TestCouponGrantRepairRestoresRemainder 999 张被压成 1 张后，按「发放 − 制作」
// 补回余量（v1 数量补偿）；重复执行保持幂等。
func TestCouponGrantRepairRestoresRemainder(t *testing.T) {
	t.Parallel()
	db := openRecipeTestDB(t)
	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)
	uid := int64(2100010)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}

	// 现场复刻：发 999，领取后被压成 1 张；之后做了 3 次标本板（一次都没扣券）。
	seedCouponGrantAudit(t, db, uid, 999, 1000)
	coupon, err := srv.CreateItem(ctx, uid, couponChucheloItemID, "backpack", 1)
	if err != nil {
		t.Fatalf("创建券失败: %v", err)
	}
	for _, out := range []string{"fish_board_l_16", "fish_board_l_15", "fish_board_m_14"} {
		seedTrophyCraftTxn(t, db, uid, out, 1100)
	}

	fixed, err := srv.repairCouponGrantQuantities(ctx)
	if err != nil {
		t.Fatalf("补偿失败: %v", err)
	}
	if fixed != 1 {
		t.Fatalf("应补偿 1 行，实得 %d", fixed)
	}
	if _, got := couponRowCount(t, srv, uid); got != 996 {
		t.Fatalf("券应补偿到 996 张（999−3），实得 %d", got)
	}
	row, err := db.GetItemByGUID(ctx, coupon.GUID)
	if err != nil || row == nil {
		t.Fatalf("读取券行失败: %v", err)
	}
	fields, ferr := FieldsFromJSON(row.FieldsJSON)
	if ferr != nil {
		t.Fatalf("解析券字段失败: %v", ferr)
	}
	if n, ok := itemQtyValue(row.WireClass, fields); !ok || int64(n) != 996 {
		t.Fatalf("券字段数量槽应 996，实得 %d ok=%v", n, ok)
	}

	if again, err := srv.repairCouponGrantQuantities(ctx); err != nil || again != 0 {
		t.Fatalf("重复执行应跳过，fixed=%d err=%v", again, err)
	}
}

// TestCouponRowRepairExpandsSingleRow 单行 996 张 → 996 行各 1 张（v2 展开）。
func TestCouponRowRepairExpandsSingleRow(t *testing.T) {
	t.Parallel()
	db := openRecipeTestDB(t)
	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)
	uid := int64(2100020)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}

	seedCouponGrantAudit(t, db, uid, 999, 1000)
	for _, out := range []string{"fish_board_l_16", "fish_board_l_15", "fish_board_m_14"} {
		seedTrophyCraftTxn(t, db, uid, out, 1100)
	}
	coupon, err := srv.CreateItem(ctx, uid, couponChucheloItemID, "backpack", 1)
	if err != nil {
		t.Fatalf("创建券失败: %v", err)
	}
	// 存量现场：一行 996 张（v1 已补过数量，但客户端显示不出张数）。
	if err := srv.setCouponRowQuantity(ctx, coupon.GUID, 996); err != nil {
		t.Fatalf("写入 996 张失败: %v", err)
	}

	fixed, err := srv.repairCouponRowLayout(ctx)
	if err != nil {
		t.Fatalf("展开失败: %v", err)
	}
	if fixed != 1 {
		t.Fatalf("应展开 1 个玩家，实得 %d", fixed)
	}
	rows, qty := couponRowCount(t, srv, uid)
	if rows != 996 || qty != 996 {
		t.Fatalf("应展开为 996 行 996 张，实得 %d 行 %d 张", rows, qty)
	}
	// 原 GUID 必须保留（可追溯），且数量已归一为 1。
	row, gerr := db.GetItemByGUID(ctx, coupon.GUID)
	if gerr != nil || row == nil {
		t.Fatalf("原券行应保留: err=%v row=%v", gerr, row)
	}
	if cur, cerr := srv.couponRowQuantity(ctx, coupon.GUID); cerr != nil || cur != 1 {
		t.Fatalf("原券行数量应归一为 1，实得 %d err=%v", cur, cerr)
	}

	// 幂等：再跑一次不再改动。
	if again, err := srv.repairCouponRowLayout(ctx); err != nil || again != 0 {
		t.Fatalf("重复执行应跳过，fixed=%d err=%v", again, err)
	}
	if r2, q2 := couponRowCount(t, srv, uid); r2 != 996 || q2 != 996 {
		t.Fatalf("重复执行后仍应 996 行 996 张，实得 %d 行 %d 张", r2, q2)
	}
}

// TestCouponRowRepairExpandsQuantityConserving v2 只做布局转换：一行 N 张
// 展开成 N 行各 1 张，总张数守恒，不查审计账目、不增不删。
func TestCouponRowRepairExpandsQuantityConserving(t *testing.T) {
	t.Parallel()
	db := openRecipeTestDB(t)
	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)
	uid := int64(2100021)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}

	// 现场：一行 4 张（没有任何发放审计记录，模拟运营直接设的数量）。
	coupon, err := srv.CreateItem(ctx, uid, couponChucheloItemID, "backpack", 1)
	if err != nil {
		t.Fatalf("创建券失败: %v", err)
	}
	if err := srv.setCouponRowQuantity(ctx, coupon.GUID, 4); err != nil {
		t.Fatalf("写入 4 张失败: %v", err)
	}
	if r, q := couponRowCount(t, srv, uid); r != 1 || q != 4 {
		t.Fatalf("前置应为 1 行 4 张，实得 %d 行 %d 张", r, q)
	}

	if fixed, err := srv.repairCouponRowLayout(ctx); err != nil || fixed != 1 {
		t.Fatalf("应展开 1 行，fixed=%d err=%v", fixed, err)
	}
	rows, qty := couponRowCount(t, srv, uid)
	if rows != 4 || qty != 4 {
		t.Fatalf("应守恒展开为 4 行 4 张，实得 %d 行 %d 张", rows, qty)
	}
	// 原 GUID 保留且归一为 1 张。
	if cur, cerr := srv.couponRowQuantity(ctx, coupon.GUID); cerr != nil || cur != 1 {
		t.Fatalf("原券行应归一为 1 张，实得 %d err=%v", cur, cerr)
	}
	// 幂等：再跑一次不重复展开。
	if again, err := srv.repairCouponRowLayout(ctx); err != nil || again != 0 {
		t.Fatalf("重复执行应跳过，fixed=%d err=%v", again, err)
	}
	if r2, q2 := couponRowCount(t, srv, uid); r2 != 4 || q2 != 4 {
		t.Fatalf("重复执行后仍应 4 行 4 张，实得 %d 行 %d 张", r2, q2)
	}
}

// TestCouponRowRepairKeepsOperatorSetQuantity 关键验收：运营把券从 996 张改成
// 4 张后重启，v2 不能按历史发放审计（999−3）把它拉回 996 张。v1 负责账目，
// v2 只负责「一行多张 → 多行各一张」，两者职责分开。
func TestCouponRowRepairKeepsOperatorSetQuantity(t *testing.T) {
	t.Parallel()
	db := openRecipeTestDB(t)
	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)
	uid := int64(2100022)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}

	// 历史账目：发 999、做 3 次，按旧 v2 会算成 996 张。
	seedCouponGrantAudit(t, db, uid, 999, 1000)
	for _, out := range []string{"fish_board_l_16", "fish_board_l_15", "fish_board_m_14"} {
		seedTrophyCraftTxn(t, db, uid, out, 1100)
	}
	// v1 标记已写（现场就是这样），所以 v1 不会重跑；只跑 v2。
	if err := srv.DB.KVSet(ctx, couponGrantRepairKey, "done;fixed=1"); err != nil {
		t.Fatal(err)
	}
	// 运营为验收把背包改成 4 张，且已经是一券一行。
	grantCoupons(t, srv, uid, 4)
	if r, q := couponRowCount(t, srv, uid); r != 4 || q != 4 {
		t.Fatalf("前置应为 4 行 4 张，实得 %d 行 %d 张", r, q)
	}

	if fixed, err := srv.repairCouponRowLayout(ctx); err != nil || fixed != 0 {
		t.Fatalf("已是一券一行时不应改动，fixed=%d err=%v", fixed, err)
	}
	if r, q := couponRowCount(t, srv, uid); r != 4 || q != 4 {
		t.Fatalf("重启后必须保持运营设置的 4 张，实得 %d 行 %d 张（被账目拉回即为回归）", r, q)
	}
}

// TestCouponRowRepairSkipsWhenAlreadyOnePerRow 已是「一券一行」时不动数据。
func TestCouponRowRepairSkipsWhenAlreadyOnePerRow(t *testing.T) {
	t.Parallel()
	db := openRecipeTestDB(t)
	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)
	uid := int64(2100023)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}

	seedCouponGrantAudit(t, db, uid, 5, 1000)
	grantCoupons(t, srv, uid, 5)

	if fixed, err := srv.repairCouponRowLayout(ctx); err != nil || fixed != 0 {
		t.Fatalf("已是目标布局不应改动，fixed=%d err=%v", fixed, err)
	}
	if r, q := couponRowCount(t, srv, uid); r != 5 || q != 5 {
		t.Fatalf("应保持 5 行 5 张，实得 %d 行 %d 张", r, q)
	}
}
