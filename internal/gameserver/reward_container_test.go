package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

// 房屋里的奖赏展示位按 E 后，客户端走 4/1(items/info)、4/4(items/list)、
// 4/5(items/get) 拉「奖赏」容器（客户端容器枚举 14），不是 3/16 面板。
// 缺这一组时列表恒为空，客户端显示「未找到一件合适的物件」。
func TestRewardContainerInventory(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	defer db.Close()
	c := context.Background()
	if err := srv.EnsurePlayer(c, 1000001); err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)

	cup, err := srv.CreateItem(c, 1000001, "ny_2024_cup", "backpack", 1)
	if err != nil {
		t.Fatalf("创建奖杯失败: %v", err)
	}
	medal, err := srv.CreateItem(c, 1000001, "qual_spin_cup", "house", 1)
	if err != nil {
		t.Fatalf("创建奖牌失败: %v", err)
	}
	// 非奖赏物品不得混进容器 14。
	if _, err := srv.CreateItem(c, 1000001, "worm_01", "backpack", 1); err != nil {
		t.Fatalf("创建普通物品失败: %v", err)
	}

	// 4/1：数量与容量都等于已拥有的奖赏数（奖赏不是可扩容仓库）。
	b, ok := hItemCount(&Ctx{Sess: sess, Main: 4, Sub: 1, Log: logging.Discard(),
		Tree: proto.Args524(proto.U32(containerRewards)), DecodeOK: true})
	if !ok {
		t.Fatal("4/1 未处理")
	}
	dec, _, err := proto.DecodeValue(b, 0, srv.Table())
	if err != nil {
		t.Fatalf("4/1 响应解码失败: %v", err)
	}
	if used := u64Of(dec.Fields[0].Num); used != 2 {
		t.Errorf("4/1 奖赏容器 used=%d，期望 2", used)
	}
	if capacity := u64Of(dec.Fields[1].Num); capacity != 2 {
		t.Errorf("4/1 奖赏容器容量=%d，期望 2", capacity)
	}

	// 4/4：只回奖赏 GUID，顺序来自 house/backpack/keepnet 列表。
	b44, ok := hItemGUIDs(&Ctx{Sess: sess, Main: 4, Sub: 4, Log: logging.Discard(),
		Tree: proto.Args524(proto.U32(containerRewards)), DecodeOK: true})
	if !ok {
		t.Fatal("4/4 未处理")
	}
	dec44, _, err := proto.DecodeValue(b44, 0, srv.Table())
	if err != nil {
		t.Fatalf("4/4 响应解码失败: %v", err)
	}
	got := make(map[string]bool, len(dec44.Items))
	for _, it := range dec44.Items {
		got[it.Hex] = true
	}
	for _, want := range []string{cup.GUID, medal.GUID} {
		if !got[want] {
			t.Errorf("4/4 奖赏容器缺少 %s", want)
		}
	}
	if len(dec44.Items) != 2 {
		t.Errorf("4/4 奖赏容器返回 %d 件，期望 2（不得混入普通物品）", len(dec44.Items))
	}

	// 4/5：奖赏物品树必须把 f4 投影成 14，否则客户端不进奖赏分组。
	b45, ok := hItemDetails(&Ctx{Sess: sess, Main: 4, Sub: 5, Log: logging.Discard(),
		Tree: proto.Args524(proto.U32(containerRewards), proto.GuidArr([]string{cup.GUID, medal.GUID})), DecodeOK: true})
	if !ok {
		t.Fatal("4/5 未处理")
	}
	dec45, _, err := proto.DecodeValue(b45, 0, srv.Table())
	if err != nil {
		t.Fatalf("4/5 响应解码失败: %v", err)
	}
	if dec45.ArgCount() != 2 {
		t.Fatalf("4/5 奖赏容器应回 2 件，实得 %d", dec45.ArgCount())
	}
	seen := map[string]bool{}
	for _, item := range dec45.Items {
		if item.Class != "enikeammfen" || item.TypeID != 2015 {
			t.Fatalf("4/5 返回了非奖赏对象 %s/%d", item.Class, item.TypeID)
		}
		idx := fieldIdxByCS(item.Class, csSlotEnum)
		if idx < 0 {
			t.Fatalf("enikeammfen 缺容器槽位字段")
		}
		if got := u64Of(item.Fields[idx].Num); got != containerRewards {
			t.Errorf("4/5 奖赏 f4=%d，期望 %d", got, containerRewards)
		}
		guidField := item.Field(2)
		seen[guidField.GuidHex()] = true
	}
	for _, want := range []string{cup.GUID, medal.GUID} {
		if !seen[want] {
			t.Errorf("4/5 奖赏容器缺少 %s", want)
		}
	}
}

// 背包里的奖赏必须在客户端拉库存前搬进房屋，否则展示位列表（容器 10）
// 永远看不到它们。
func TestAwardsMoveToHouseBeforeInventoryList(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	defer db.Close()
	c := context.Background()
	if err := srv.EnsurePlayer(c, 1000001); err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	cup, err := srv.CreateItem(c, 1000001, "ny_2024_cup", "backpack", 1)
	if err != nil {
		t.Fatalf("创建奖杯失败: %v", err)
	}

	// 第一次背包 4/1 就触发搬迁。
	if _, ok := hItemCount(&Ctx{Sess: sess, Main: 4, Sub: 1, Log: logging.Discard(),
		Tree: proto.Args524(proto.U32(containerBackpack)), DecodeOK: true}); !ok {
		t.Fatal("4/1 背包未处理")
	}
	row, err := srv.DB.GetItemByGUID(c, cup.GUID)
	if err != nil || row == nil {
		t.Fatalf("读回奖杯失败: %v", err)
	}
	if row.Container != "house" {
		t.Fatalf("奖赏容器=%q，期望 house", row.Container)
	}

	// 房屋列表（展示位取物路径）现在能看到它。
	b, ok := hItemGUIDs(&Ctx{Sess: sess, Main: 4, Sub: 4, Log: logging.Discard(),
		Tree: proto.Args524(proto.U32(10)), DecodeOK: true})
	if !ok {
		t.Fatal("4/4 房屋未处理")
	}
	dec, _, err := proto.DecodeValue(b, 0, srv.Table())
	if err != nil {
		t.Fatalf("4/4 解码失败: %v", err)
	}
	found := false
	for _, it := range dec.Items {
		if it.Hex == cup.GUID {
			found = true
		}
	}
	if !found {
		t.Fatalf("搬迁后房屋列表仍缺少奖杯 %s", cup.GUID)
	}

	// 房屋容器取详情时，奖赏树也必须投影到 f4=14；house=10 会让
	// 客户端「奖赏」摆放标签把它过滤掉。
	b45, ok := hItemDetails(&Ctx{Sess: sess, Main: 4, Sub: 5, Log: logging.Discard(),
		Tree: proto.Args524(proto.U32(10), proto.GuidArr([]string{cup.GUID})), DecodeOK: true})
	if !ok {
		t.Fatal("4/5 房屋未处理")
	}
	dec45, _, err := proto.DecodeValue(b45, 0, srv.Table())
	if err != nil {
		t.Fatalf("4/5 解码失败: %v", err)
	}
	if dec45.ArgCount() != 1 {
		t.Fatalf("4/5 房屋应回 1 件，实得 %d", dec45.ArgCount())
	}
	item := dec45.Items[0]
	idx := fieldIdxByCS(item.Class, csSlotEnum)
	if idx < 0 || u64Of(item.Fields[idx].Num) != containerRewards {
		t.Fatalf("4/5 房屋奖赏 f4=%d，期望 %d", u64Of(item.Fields[idx].Num), containerRewards)
	}

	// 3/16 面板仍能展示（它读 house/backpack/keepnet）。
	b316, ok := hPlayerRewards2015(&Ctx{Sess: sess, Main: 3, Sub: 16, Log: logging.Discard(), DecodeOK: true})
	if !ok {
		t.Fatal("3/16 未处理")
	}
	dec316, _, err := proto.DecodeValue(b316, 0, srv.Table())
	if err != nil {
		t.Fatalf("3/16 解码失败: %v", err)
	}
	if dec316.ArgCount() != 1 {
		t.Fatalf("3/16 返回 %d 件，期望 1", dec316.ArgCount())
	}
}

// 放置奖赏时客户端可能把目标容器写成 14；物品必须落 house，不能被
// containerName(14) 的兜底值丢进背包。
func TestRewardMoveToRewardsContainerStaysInHouse(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	defer db.Close()
	c := context.Background()
	if err := srv.EnsurePlayer(c, 1000001); err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	cup, err := srv.CreateItem(c, 1000001, "ny_2024_cup", "backpack", 1)
	if err != nil {
		t.Fatalf("创建奖杯失败: %v", err)
	}
	body, ok := hItemMove(&Ctx{Sess: sess, Main: 4, Sub: 6, ExpectResponse: true, Log: logging.Discard(),
		Tree: proto.Args524(proto.U32(containerBackpack), proto.U32(containerRewards),
			proto.GuidArr([]string{cup.GUID})), DecodeOK: true})
	if !ok || len(body) == 0 {
		t.Fatal("4/6 奖赏放置未处理")
	}
	row, err := srv.DB.GetItemByGUID(c, cup.GUID)
	if err != nil || row == nil {
		t.Fatalf("读回奖杯失败: %v", err)
	}
	if row.Container != "house" {
		t.Fatalf("奖赏放置后容器=%q，期望 house", row.Container)
	}
}
