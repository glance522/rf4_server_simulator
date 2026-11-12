package gameserver

import (
	"math"
	"testing"

	"rf4server/internal/store"
)

// TestWorkshopPartResidualRates 把残值率这张表钉在官服抓包上。
//
// 数据来源：logs/ncdecoded.txt 帧 #331（21/2 回包）同一单的五件零件，
// 每个槽位的 hlopebmchhp.f3 就是那件旧件的残值，除以 hpopgbjohpk.f2 的新件价
// 得到残值率。轮油（第五槽，f0=0edd4e21…）的 f3 是 (0,0)，所以是 0。
func TestWorkshopPartResidualRates(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	defer db.Close()
	cases := []struct {
		cat  int64
		want float64
		note string
	}{
		{7, 0.383, "轴承 324/845"},
		{8, 0.383, "线轴 239/624"},
		{9, 0.383, "摩擦片 289/754"},
		{52, 0.9, "齿轮 266/296"},
		{10, 0, "轮油 f3=(0,0)，旧油不折抵"},
	}
	for _, c := range cases {
		if got := srv.workshopPartResidualRate(c.cat); math.Abs(got-c.want) > 1e-9 {
			t.Fatalf("品类 %d（%s）残值率应为 %v，实得 %v", c.cat, c.note, c.want, got)
		}
	}
	// 轮油按全价收：新件 50 银 → 残值 (0,0)，客户端报价与实扣都是 50。
	if rs, rg := srv.workshopPartResidual(10, 50, 0); rs != 0 || rg != 0 {
		t.Fatalf("轮油残值应为 (0,0)，实得 (%d,%d)", rs, rg)
	}
}

// workshopPageSilver 返回"客户端工坊界面会显示、并会作为 21/4 报价提交"的金额。
//
// 官服口径（logs/nc.jsonl 同一单反推）：界面上的价是 Σ(新件标价 − 旧件残值)，
// 21/2 发下去的 hpopgbjohpk.f2 与 hlopebmchhp.f3 就是这两项。服务端必须按同一个
// 公式收钱，否则就会出现"界面报 2197、实扣 3"这种事。
//
// 用服务端自己的定价函数算，是为了把契约钉成"下单收的钱 == 页面写的钱"，
// 而不是钉死某个数字——价格表一改这条断言照样成立。
func workshopPageSilver(t *testing.T, srv *Server, item *store.Item, cats ...int64) int64 {
	t.Helper()
	ctx := t.Context()
	want := map[int64]bool{}
	for _, c := range cats {
		want[c] = true
	}
	var total int64
	for _, p := range srv.repairPartsOf(ctx, item) {
		if !want[int64(p.Cat)] {
			continue
		}
		// 空 storeID / 空地图：不乘部位维修倍率，验的是"界面价 == 实扣"这个契约本身。
		baseSilver, baseGold := srv.partRepairBasePrice(ctx, "", "", p)
		residual, _ := srv.workshopPartResidual(int64(p.Cat), baseSilver, baseGold)
		// 银币支付时只扣银币列（官服那单新件含 5 金，实扣只有 30 银）。
		total += baseSilver - residual
	}
	return total
}
