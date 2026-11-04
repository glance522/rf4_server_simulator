package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/logging"
)

// 3/14/3/15 是管理机构的备用渔具领取：客户端提示成功但此前服务端只回 null、不发物品。
// 这里校验两套装备都真的进了背包，且槽位不重复。
func TestTackleAidGrantsSpareSets(t *testing.T) {
	srv, db := newEconomyTxTestServer(t)
	const uid = int64(2201)
	if _, err := db.EnsureProfile(context.Background(), uid); err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	sess.Userid = uid

	for _, sub := range []int{14, 15} {
		body, ok := hTackleAid(&Ctx{Sess: sess, Main: 3, Sub: sub, Log: logging.Discard()})
		if !ok || len(body) == 0 {
			t.Fatalf("3/%d 未处理", sub)
		}
	}

	var total, distinct int
	if err := db.SQL.QueryRow(
		`SELECT COUNT(*), COUNT(DISTINCT slot) FROM item WHERE uid=? AND container='backpack'`,
		uid).Scan(&total, &distinct); err != nil {
		t.Fatal(err)
	}
	want := len(spareFloatTackle) + len(spareSpinTackle)
	if total != want {
		t.Fatalf("备用渔具物品数=%d，期望 %d", total, want)
	}
	// 同一天再领一次：不应再发任何物品。
	if _, ok := hTackleAid(&Ctx{Sess: sess, Main: 3, Sub: 14, Log: logging.Discard()}); !ok {
		t.Fatal("3/14 未处理")
	}
	var after int
	if err := db.SQL.QueryRow(
		`SELECT COUNT(*) FROM item WHERE uid=?`, uid).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != want {
		t.Fatalf("同一天重复领取又发了 %d 件（应为 0）", after-want)
	}
	if total != distinct {
		t.Fatalf("槽位有重复：物品 %d 件，不同槽位 %d 个", total, distinct)
	}
}
