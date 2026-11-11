package gameserver

// 搏鱼中裸部件树下发的契约回归：Push4_22Trees 发出的每个子对象都要过 4/22 的
// item 实例白名单（session.go 守门人）。2026-11-11 实测翻过车：轮树里的非部件
// 辅助对象（pcfflcdpjgk/166）混进推送，352 帧全数被"拒发推送：形状与合同不符"
// 吞掉，客户端一个字节都没收到。这条测试钉住：收集器只准发部件品类。

import (
	"context"
	"testing"
)

// TestFightWearComponentTreesPassContractWhitelist 用真实电轮+竿+前导装配验证：
// 受力落库后收集到的裸部件子树全部命中 itemTypeIDs 白名单。
func TestFightWearComponentTreesPassContractWhitelist(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	reel, err := srv.CreateItem(ctx, uid, "elcon_5968", "backpack", 1) // 电轮：带电机与非部件辅助子件
	if err != nil {
		t.Fatalf("造电轮失败: %v", err)
	}
	mountReelOnRod(t, srv, rod.GUID, reel.GUID)

	row := wearReelRow(t, db, ctx, reel.GUID)
	rated, ok := srv.reelWasherRatedFromRow(ctx, row)
	if !ok || rated <= 0 {
		t.Fatal("读不到摩擦片额定制动力")
	}
	w := fightReelWear{
		valid: true, fishGUID: "fish0000000000000000000000000001",
		rodGUID: rod.GUID, reelGUID: reel.GUID,
		setting: 25, dragKg: 5, ratedKg: rated, pullKg: 7.5,
		StressSecs: 17,
	}
	if !srv.applyFightReelWear(ctx, uid, w, "14/4", false) {
		t.Fatal("受力磨损应落库")
	}

	ledger := w
	ledger.leaderGUID = ""
	trees := srv.fightWearComponentTrees(ctx, uid, ledger)
	if len(trees) == 0 {
		t.Fatal("受力落库后应收集到部件子树")
	}
	for i, tree := range trees {
		if reason := matchPattern("item", tree); reason != "" {
			t.Fatalf("子树 %d 未过 4/22 契约白名单: %s", i, reason)
		}
	}
	// 官服帧的部件品类一个都不能少：轮侧五件 + 竿侧竿坯/导环。
	// 电轮电机（f14）在白名单内、允许下发，但存在与否取决于装配，不做强断言。
	got := map[int64]bool{}
	for _, tree := range trees {
		def := srv.reelPartDef(ctx, tree)
		if def != nil {
			got[def.Category] = true
		}
	}
	for _, category := range []int64{reelPartCatOil, reelPartCatGear, reelPartCatBearing,
		reelPartCatSpool, reelPartCatWasher, reelPartCatRodBlank, reelPartCatGuides} {
		if !got[category] {
			t.Fatalf("部件品类 %d 应出现在下发集合，实得 %v", category, got)
		}
	}
}
