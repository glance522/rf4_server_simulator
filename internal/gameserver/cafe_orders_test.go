package gameserver

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/testutil"
	"rf4server/internal/wiregen"
)

func TestCafeGenerationUsesWaterRosterAndSparseLargeOrders(t *testing.T) {
	srv, db := newEconomyTxTestServer(t)
	importFishStaticForProbe(t, db)
	ctx := context.Background()
	const level = "level_001_lake"
	roster, _ := db.LocRoster(ctx, db.ResolveStatLoc(ctx, level))
	keys := map[string]bool{}
	for _, r := range roster {
		keys[r.FishKey] = true
	}
	rare, ordinary := 0, 0
	for day := 0; day < 100; day++ {
		now := int64(1700000000 + day*86400)
		orders, err := srv.cafeTemplates(ctx, level, now)
		if err != nil {
			t.Fatal(err)
		}
		if len(orders) != 10 {
			t.Fatal("订单数错误")
		}
		again, err := srv.cafeTemplates(ctx, level, now)
		if err != nil || !reflect.DeepEqual(orders, again) {
			t.Fatal("同一时刻生成结果不稳定")
		}
		rareN := 0
		deadlineSet := map[int64]bool{}
		for _, o := range orders {
			if !keys[o.Species] || o.Quantity < 1 || o.Quantity > 1000 || o.MinWeightG < 1 || o.RewardCents < 1 {
				t.Fatalf("不合理需求: %+v", o)
			}
			v, tr, su := db.GradeLines(ctx, o.Species)
			if o.OfferSize != gradeFromLines(v, tr, su, o.MinWeightG) {
				t.Fatal("重量和档位不一致")
			}
			if o.OfferSize >= 3 && o.Quantity != 1 {
				t.Fatalf("大鱼单应只收1条: %+v", o)
			}
			if o.OfferSize == 4 {
				rareN++
				rare++
			} else {
				ordinary++
			}
			deadlineSet[o.PhaseSeconds+(now-o.PhaseSeconds)/o.PeriodSeconds*o.PeriodSeconds+o.PeriodSeconds] = true
		}
		if rareN > 1 || len(deadlineSet) < 5 {
			t.Fatalf("稀有单过多或时间集中: rare=%d deadlines=%d", rareN, len(deadlineSet))
		}
	}
	if rare == 0 || rare >= ordinary/5 {
		t.Fatalf("稀有分布不符: rare=%d ordinary=%d", rare, ordinary)
	}
	t.Logf("100次现实日期采样：普通/大鱼单%d，稀有单%d；每次稀有单至多1张且只收1条", ordinary, rare)
	rows, err := db.SQL.Query(`SELECT DISTINCT a.alias FROM loc_alias a JOIN loc_wiki_fish w ON w.loc=a.loc ORDER BY a.alias`)
	if err != nil {
		t.Fatal(err)
	}
	var waters []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		waters = append(waters, key)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, water := range waters {
		orders, err := srv.cafeTemplates(ctx, water, 1700000000)
		want := 10
		if water == "level_007_grass_lake" {
			want = 0
		}
		if err != nil || len(orders) != want {
			t.Fatalf("水域%s生成失败: %v", water, err)
		}
	}
	t.Logf("已检查%d个水域：17个使用核对报价，熊湖暂无报价模板", len(waters))
	for _, tc := range []struct{ scene, want string }{{"fmarket_02", "level_002_kirzah_river"}, {"fishmarket_009", "level_009_ahtuba"}, {"level_001_fmarket", level}, {"unknown", ""}} {
		_, got := srv.cafeScene(ctx, 1, proto.Args524(proto.StrVal(tc.scene)))
		if got != tc.want {
			t.Fatalf("场景%s映射到%s want%s", tc.scene, got, tc.want)
		}
	}
}

func TestCafeNativeFlowPaysAndPushesRemoval(t *testing.T) {
	db := connectTestDB(t)
	defer db.Close()
	importFishStaticForProbe(t, db)
	addr, _ := startGameserverWithDB(t, db)
	gw := testutil.StartGateway(t, addr, testutil.TestPSK)
	token, uid := gw.Login(t)
	fc := testutil.NewFakeClient(t, gw.RealAddr)
	fc.Dial(token, false)
	if boot := fc.WaitPush(2 * time.Second); boot == nil || boot.Main != 0 {
		t.Fatal("缺少启动推送")
	}
	table := wiregen.Table()
	req, _ := proto.EncodeValue(proto.Args524(proto.StrVal("fishmarket_009")), table)
	fc.SendRequest(800, 20, 3, req...)
	resp := fc.WaitResponse(3 * time.Second)
	list, n, err := proto.DecodeValue(resp.Body, 0, table)
	if err != nil || n != len(resp.Body) || list.ArgCodeStr() != "110" || list.ArgCount() != 10 {
		t.Fatalf("订单响应错误: n=%d len=%d err=%v", n, len(resp.Body), err)
	}
	o := list.ArgItem(0)
	if o.Class != "jbfajcjlfek" || o.Field(1).Str == nil || *o.Field(1).Str != "level_009_ahtuba" || *o.Field(2).Num <= uint64(proto.WindowsFiletimeNow()) {
		t.Fatal("水域或截止时间错误")
	}
	ctx := context.Background()
	var species, level string
	var grams, quantity, reward int64
	if err := db.SQL.QueryRow(`SELECT species,level_key,min_weight_g,quantity,reward_cents FROM cafe_order WHERE order_id=?`, o.Field(0).Hex).Scan(&species, &level, &grams, &quantity, &reward); err != nil {
		t.Fatal(err)
	}
	gs := []string{}
	for i := int64(0); i < quantity; i++ {
		guid := fmt.Sprintf("ca%030x", i+1)
		gs = append(gs, guid)
		if _, err := db.SQL.Exec(`INSERT INTO keepnet_items(echo_guid,uid,species,name_zh,weight_g,length_m,cat,keep_loc,keep_spot,keep_bait,keep_level,ts) VALUES(?,?,?,'',?,0.2,2,'lvl2_01',0,'worm',?,?)`, guid, uid, species, grams, level, float64(time.Now().Unix())); err != nil {
			t.Fatal(err)
		}
	}
	p0, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := proto.EncodeValue(proto.Args524(proto.StrVal("fishmarket_009"), proto.Guid(o.Field(0).Hex), proto.GuidArr(gs)), table)
	fc.SendRequest(801, 20, 4, body...)
	resp = fc.WaitResponse(3 * time.Second)
	result, n, err := proto.DecodeValue(resp.Body, 0, table)
	if err != nil || n != len(resp.Body) || result.Class != "imhmlbejglk" || *result.Field(1).Num != 0 || *result.Field(0).Num != uint64(reward) {
		t.Fatalf("结算响应错误: %+v %v", result, err)
	}
	removed := waitPushCmd(t, fc, 4, 21, 3*time.Second)
	push, _, err := proto.DecodeValue(removed.Body, 0, table)
	if err != nil || !reflect.DeepEqual(deepGUIDs(push), gs) {
		t.Fatalf("移除推送错误: %+v %v", push, err)
	}
	p1, _ := db.GetProfile(ctx, uid)
	if p1.Silver != p0.Silver+reward {
		t.Fatal("钱包未同步")
	}
	fc.SendRequest(802, 20, 4, body...)
	fc.WaitResponse(3 * time.Second)
	p2, _ := db.GetProfile(ctx, uid)
	if p2.Silver != p1.Silver {
		t.Fatal("网络重试重复付款")
	}
	fc.SendRequest(803, 20, 3, req...)
	resp = fc.WaitResponse(3 * time.Second)
	list, _, err = proto.DecodeValue(resp.Body, 0, table)
	if err != nil || list.ArgItem(0).Field(0).Hex != o.Field(0).Hex || *list.ArgItem(0).Field(3).Num != 1 {
		t.Fatal("完成状态未持久化到响应")
	}
}

func TestCafeAndFishMarketKeepPlayerTradeLocks(t *testing.T) {
	srv, db := newEconomyTxTestServer(t)
	ctx := context.Background()
	const uid = int64(3202)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	const fish = "b2000000000000000000000000000001"
	seedMarketOfferHandlerFish(t, db, uid, fish)
	if _, err := db.CreateMarketOffers(ctx, uid, "b1000000000000000000000000000001", []store.MarketOfferCreateRequest{{FishGUID: fish, PriceCents: 100}}); err != nil {
		t.Fatal(err)
	}
	before, _ := db.GetProfile(ctx, uid)
	body, ok := hMarketSell(economyTestCtx(srv, uid, proto.Args524(proto.GuidArr([]string{fish}))))
	if !ok || len(body) == 0 || body[0] != 0x0F {
		t.Fatalf("挂单鱼不得售卖: %x", body)
	}
	after, _ := db.GetProfile(ctx, uid)
	if before.Silver != after.Silver {
		t.Fatal("未成交却改余额")
	}
	var n int
	if err := db.SQL.QueryRow("SELECT COUNT(*) FROM keepnet_items WHERE uid=?", uid).Scan(&n); err != nil || n != 1 {
		t.Fatalf("挂单鱼被删除: %d %v", n, err)
	}
}
