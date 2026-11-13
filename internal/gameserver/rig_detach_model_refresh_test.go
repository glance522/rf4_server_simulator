package gameserver

// 拆卸后竿建模刷新回归：
// 客户端的竿 3D 建模不随 800 f1 原位更新重建——装轮可见是随后 17/7 取竿的
// 158 重建在起作用，拆轮没有后续取竿，模型把轮留在竿上（线上 2026-11-12
// 实锤：17/3 已回 f4=空的竿树与退包轮树，建模仍挂轮）。修复：17/3、17/4
// 真实拆卸后回包补推 4/22 竿树，客户端按版本戳更大的新快照重建模型。

import (
	"context"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

// TestRodDetachPushesRodTreeAfterResponse 17/3 拆轮：响应后必须补推 4/22，
// 推送竿树 f4（挂轮）为空，且版本戳严格大于响应 f1 竿树的版本戳——客户端
// 只接受版本戳严格变大的 4/22 更新，同版本推送会被按「版本未变」丢弃
// （线上 2026-11-12 实锤：多次拆轮仅一次建模消失）。
func TestRodDetachPushesRodTreeAfterResponse(t *testing.T) {
	srv, _ := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	reel, err := srv.CreateItem(ctx, uid, "elcon_5999", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}

	sess, gc := newTunnelProbe(t, srv)
	mountGearForTest(t, srv, sess, rod.GUID, reel.GUID)

	// 与线上实抓一致：slot 在第二个整数位。
	req := proto.Args524(
		proto.U32(0),
		proto.Guid(rod.GUID),
		proto.U32(6),
		proto.Guid(reel.GUID),
		proto.U32(0),
	)
	c := &Ctx{Sess: sess, Main: 17, Sub: 3, Tree: rigRequestTree(t, req),
		ExpectResponse: true, Log: logging.Discard()}
	body, ok := hRodRemoveComp(c)
	if !ok || len(body) == 0 {
		t.Fatal("17/3 拆卸失败")
	}
	// 响应 f1 里竿树的版本戳（客户端 f1 应用后的缓存基线）。
	respVer := rigTreeVersion(t, body, rod.GUID)
	if respVer == 0 {
		t.Fatal("响应 f1 竿树读不到版本戳")
	}
	if len(c.afterResponse) != 1 {
		t.Fatalf("want 1 after-response callback, got %d", len(c.afterResponse))
	}
	c.afterResponse[0]()

	push := awaitPushEnvelope(t, gc)
	if push.Main != 4 || push.Sub != 22 {
		t.Fatalf("拆卸后推送 = %d/%d, want 4/22", push.Main, push.Sub)
	}
	dec, _, err := proto.DecodeValue(push.Body, 0, wiregen.Table())
	if err != nil {
		t.Fatal(err)
	}
	if dec.Kind != proto.KindArgs || dec.Code == nil || *dec.Code != "61" {
		t.Fatalf("推送形状不是 args(61): kind=%v code=%v", dec.Kind, dec.Code)
	}
	foundRod := false
	for _, it := range dec.Items {
		if it == nil || it.Kind != proto.KindObj || it.FieldCount() <= 4 {
			continue
		}
		gidx := fieldIdxByCS(it.Class, csGUID)
		if gidx < 0 || it.Fields[gidx].GuidHex() != rod.GUID {
			continue
		}
		foundRod = true
		if h := it.Fields[4].GuidHex(); h != "" {
			t.Fatalf("推送竿树 f4=%s, want 空（轮应已拆）", h)
		}
		cidx := fieldIdxByCS(it.Class, csCreated)
		if cidx < 0 || it.Fields[cidx].Num == nil || *it.Fields[cidx].Num <= respVer {
			t.Fatalf("推送版本戳 %v 必须严格大于响应版本戳 %d",
				it.Fields[cidx].Num, respVer)
		}
	}
	if !foundRod {
		t.Fatal("4/22 推送里没有竿树")
	}
}

// rigTreeVersion 从 800 响应 body 解码出竿树的版本戳（cnolibojkao f16）。
func rigTreeVersion(t *testing.T, body []byte, rodGUID string) uint64 {
	t.Helper()
	dec, _, err := proto.DecodeValue(body, 0, wiregen.Table())
	if err != nil {
		t.Fatal(err)
	}
	if dec.Kind != proto.KindObj || dec.FieldCount() < 2 {
		t.Fatal("800 响应形状异常")
	}
	updated := dec.Fields[1].Val
	if updated == nil || updated.Kind != proto.KindArgs {
		t.Fatal("800 f1 缺失")
	}
	for _, it := range updated.Items {
		if it == nil || it.Kind != proto.KindObj {
			continue
		}
		gidx := fieldIdxByCS(it.Class, csGUID)
		if gidx < 0 || it.Fields[gidx].GuidHex() != rodGUID {
			continue
		}
		cidx := fieldIdxByCS(it.Class, csCreated)
		if cidx >= 0 && it.Fields[cidx].Num != nil {
			return *it.Fields[cidx].Num
		}
	}
	return 0
}

// TestRodDetachNoComponentNoPush 槽位无组件时不得空推。
func TestRodDetachNoComponentNoPush(t *testing.T) {
	srv, _ := newRigTestServer(t)
	ctx := context.Background()
	rod, err := srv.CreateItem(ctx, 1000001, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := newTunnelProbe(t, srv)
	req := proto.Args524(proto.U32(0), proto.Guid(rod.GUID), proto.U32(6),
		proto.Guid("11111111222233334444555566667777"), proto.U32(0))
	c := &Ctx{Sess: sess, Main: 17, Sub: 3, Tree: rigRequestTree(t, req),
		ExpectResponse: true, Log: logging.Discard()}
	if _, ok := hRodRemoveComp(c); !ok {
		t.Fatal("17/3 空拆卸应照常回包")
	}
	if len(c.afterResponse) != 0 {
		t.Fatalf("无组件拆卸注册了 %d 个推送, want 0", len(c.afterResponse))
	}
}
