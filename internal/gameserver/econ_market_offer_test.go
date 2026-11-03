package gameserver

import (
	"context"
	"testing"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func seedMarketOfferHandlerFish(t *testing.T, db *store.DB, uid int64, fishGUID string) {
	t.Helper()
	_, err := db.SQL.Exec(`
		INSERT INTO keepnet_items(echo_guid,uid,species,name_zh,weight_g,length_m,cat,
		                          keep_loc,keep_spot,keep_bait,keep_level,ts)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		fishGUID, uid, "ruffe", "梅花鲈", 300, 0.2, 2, "loc1_02", 1, "worm", "level_001_lake", 1.0)
	if err != nil {
		t.Fatal(err)
	}
}

func TestCafeSellOfferPaysOnceAndRejectsDoubleSale(t *testing.T) {
	srv, db := newEconomyTxTestServer(t)
	importFishStaticForProbe(t, db)
	ctx0 := context.Background()
	const uid = int64(3201)
	if _, err := db.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}
	orders, err := db.GetCafeOrders(ctx0, uid, "level_001_lake", "level_001_lake", time.Now().Unix(), 86400, []store.CafeOrderTemplate{{Species: "ruffe", MinWeightG: 100, Quantity: 2, RewardCents: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	marketGUID := orders[0].ID
	fish1 := "b2000000000000000000000000000001"
	fish2 := "b2000000000000000000000000000002"
	seedMarketOfferHandlerFish(t, db, uid, fish1)
	seedMarketOfferHandlerFish(t, db, uid, fish2)
	p0, err := db.GetProfile(ctx0, uid)
	if err != nil {
		t.Fatal(err)
	}

	req := proto.Args524(
		proto.StrVal("loc1_02"),
		proto.Guid(marketGUID),
		proto.GuidArr([]string{fish1, fish2}),
	)
	body, ok := hMarketSellOffer(economyTestCtx(srv, uid, req))
	if !ok || len(body) == 0 {
		t.Fatalf("20/4 未处理: ok=%v body=%x", ok, body)
	}
	resp, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatalf("20/4 响应不可解码: %v", err)
	}
	statusField := resp.Field(1)
	priceField := resp.Field(0)
	if resp.Kind != proto.KindObj || statusField.U32() != 0 || priceField.U64() == 0 {
		t.Fatalf("20/4 成功响应错误: %+v", resp)
	}
	offers, err := db.ListMarketOffers(ctx0, store.MarketOfferFilter{
		SellerUID: uid, Status: store.MarketOfferOpen,
	})
	if err != nil || len(offers) != 0 {
		t.Fatalf("咖啡厅不得创建玩家挂单: offers=%+v err=%v", offers, err)
	}
	p1, _ := db.GetProfile(ctx0, uid)
	var txnN int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM txn WHERE uid=?`, uid).Scan(&txnN); err != nil {
		t.Fatal(err)
	}
	if p1.Silver != p0.Silver+1000 || txnN != 1 {
		t.Fatalf("20/4 应一次性付款: silver %d->%d txn=%d", p0.Silver, p1.Silver, txnN)
	}

	// 客户端重试同一次交付：不得重复付款，也不得报错。
	body, ok = hMarketSellOffer(economyTestCtx(srv, uid, req))
	if !ok || len(body) == 0 {
		t.Fatal("20/4 幂等重试未处理")
	}
	if resp, _, err = proto.DecodeValue(body, 0, srv.Table()); err != nil {
		t.Fatalf("20/4 幂等重试应成功: resp=%+v err=%v", resp, err)
	}
	statusField = resp.Field(1)
	if statusField.U32() != 0 {
		t.Fatalf("20/4 幂等重试应成功: resp=%+v", resp)
	}
	if offers, err = db.ListMarketOffers(ctx0, store.MarketOfferFilter{SellerUID: uid}); err != nil || len(offers) != 0 {
		t.Fatalf("重复交付不得生成挂单: offers=%+v err=%v", offers, err)
	}

	// 同鱼换另一张鱼市单必须失败，原订单不被改写。
	other := proto.Args524(
		proto.StrVal("loc1_02"),
		proto.Guid("b3000000000000000000000000000001"),
		proto.GuidArr([]string{fish1}),
	)
	body, ok = hMarketSellOffer(economyTestCtx(srv, uid, other))
	if !ok || len(body) == 0 {
		t.Fatal("20/4 冲突请求未处理")
	}
	if resp, _, err = proto.DecodeValue(body, 0, srv.Table()); err != nil {
		t.Fatalf("同鱼不同鱼市单应失败: resp=%+v err=%v", resp, err)
	}
	statusField = resp.Field(1)
	if statusField.U32() == 0 {
		t.Fatalf("同鱼不同鱼市单应失败: resp=%+v", resp)
	}
	if offers, _ = db.ListMarketOffers(ctx0, store.MarketOfferFilter{SellerUID: uid, Status: store.MarketOfferOpen}); len(offers) != 0 {
		t.Fatalf("冲突请求不得改写/新增订单: %+v", offers)
	}

	// 20/2 不得再次卖掉已交付的鱼。
	sellReq := proto.Args524(proto.GuidArr([]string{fish1, fish2}))
	sellBody, ok := hMarketSell(economyTestCtx(srv, uid, sellReq))
	if !ok || len(sellBody) == 0 {
		t.Fatalf("20/2 请求未处理: ok=%v body=%x", ok, sellBody)
	}
	p2, _ := db.GetProfile(ctx0, uid)
	var keepN int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM keepnet_items WHERE uid=?`, uid).Scan(&keepN); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM txn WHERE uid=? AND kind='sell_fish'`, uid).Scan(&txnN); err != nil {
		t.Fatal(err)
	}
	if p2.Silver != p0.Silver+1000 || keepN != 0 || txnN != 0 {
		t.Fatalf("20/2 冲突后状态被污染: silver=%d keep=%d sell_txn=%d", p2.Silver, keepN, txnN)
	}
}
