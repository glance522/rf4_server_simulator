package gameserver

import (
	"context"
	"fmt"
	"testing"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

func seedImportedKeepnetFish(t *testing.T, srv *Server, uid int64, id int, imported bool) (*store.KeepnetEntry, string) {
	t.Helper()
	entry := &store.KeepnetEntry{UID: uid, EchoGUID: fmt.Sprintf("%032x", id), Species: "asp", WeightG: int64(300 + id), LengthM: .4, Cat: 2, KeepLevel: "level_002_kirzah_river", KeepBait: "stonefly_nymph", TS: float64(id)}
	if err := srv.DB.KeepnetInsert(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	if !imported {
		return entry, ""
	}
	tree := buildKeepnet25(entry, entry.KeepLevel)
	tree.Fields[9].Num = ptrU64(0)
	tree.Fields[18].Num = ptrU64(0)
	tree.Fields[22].Num = ptrU64(639292851529062897)
	tree.Fields[24].Num = ptrU64(16794116)
	fields, err := FieldsToJSON(tree.Fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.DB.InsertItem(context.Background(), &store.Item{GUID: entry.EchoGUID, UID: uid, WireClass: tree.Class, TypeID: 25, Container: "keepnet", Qty: 1, FieldsJSON: fields, Durability: 1, Workmanship: 1}); err != nil {
		t.Fatal(err)
	}
	return entry, fields
}

func decodeKeepnetBody(t *testing.T, body []byte) *proto.Value {
	t.Helper()
	value, pos, err := proto.DecodeValue(body, 0, wiregen.Table())
	if err != nil || pos != len(body) {
		t.Fatalf("decode pos=%d size=%d err=%v", pos, len(body), err)
	}
	return value
}

func keepnetTestCtx(srv *Server, uid int64, args ...*proto.Value) *Ctx {
	return economyTestCtx(srv, uid, proto.Args524(args...))
}

func TestImportedAndNewKeepnetCountPagesAndDetailsAgree(t *testing.T) {
	srv, _ := newEconomyTxTestServer(t)
	const uid int64 = 7357
	a, original := seedImportedKeepnetFish(t, srv, uid, 1, true)
	b, _ := seedImportedKeepnetFish(t, srv, uid, 2, true)
	c, _ := seedImportedKeepnetFish(t, srv, uid, 3, false)
	foreign, _ := seedImportedKeepnetFish(t, srv, uid+1, 4, true)

	body, ok := hItemCount(keepnetTestCtx(srv, uid, proto.U32(5)))
	if !ok || decodeKeepnetBody(t, body).Fields[0].U32() != 3 {
		t.Fatal("keepnet count must include imported and newly caught fish")
	}
	var listed []string
	for start := 0; start < 4; start += 2 {
		body, ok = hItemGUIDs(keepnetTestCtx(srv, uid, proto.U32(5), proto.U32(uint32(start)), proto.U32(2)))
		if !ok {
			t.Fatal("keepnet page unhandled")
		}
		for _, guid := range decodeKeepnetBody(t, body).Items {
			listed = append(listed, guid.Hex)
		}
	}
	if len(listed) != 3 || listed[0] != a.EchoGUID || listed[1] != b.EchoGUID || listed[2] != c.EchoGUID {
		t.Fatalf("keepnet pages do not match rows: %v", listed)
	}
	listed = append(listed, foreign.EchoGUID)
	body, ok = hItemDetails(keepnetTestCtx(srv, uid, proto.U32(5), proto.GuidArr(listed)))
	if !ok {
		t.Fatal("keepnet details unhandled")
	}
	items := decodeKeepnetBody(t, body).Items
	if len(items) != 3 {
		t.Fatalf("got %d details for 3 owned fish", len(items))
	}
	for i, item := range items {
		if item.TypeID != 25 || item.Fields[16].Hex != listed[i] || item.Fields[18].U32() != 5 {
			t.Fatalf("fish %d has stale identity or container", i)
		}
		if item.Fields[22].U64() <= 639292851529062897 {
			t.Fatalf("fish %d reused the old server version", i)
		}
	}
	if items[0].Fields[9].U32() != 0 || items[0].Fields[24].U32() != 16794116 || items[0].Fields[5].U64() != uint64(filetimeFromUnixFloat(a.TS)) {
		t.Fatal("normalization changed bait type, status or catch time")
	}
	stored, err := srv.DB.GetItemByGUID(context.Background(), a.EchoGUID)
	if err != nil || stored.FieldsJSON != original {
		t.Fatal("reading keepnet mutated the imported snapshot")
	}
}

func TestImportedAndNewKeepnetReleaseSellAndRestartAgree(t *testing.T) {
	srv, db := newEconomyTxTestServer(t)
	const uid int64 = 7357
	if _, err := db.EnsureProfile(context.Background(), uid); err != nil {
		t.Fatal(err)
	}
	a, _ := seedImportedKeepnetFish(t, srv, uid, 11, true)
	b, _ := seedImportedKeepnetFish(t, srv, uid, 12, true)
	c, _ := seedImportedKeepnetFish(t, srv, uid, 13, false)
	foreign, _ := seedImportedKeepnetFish(t, srv, uid+1, 14, true)
	body, ok := hItemRemove(keepnetTestCtx(srv, uid, proto.GuidArr([]string{a.EchoGUID, c.EchoGUID, foreign.EchoGUID, a.EchoGUID})))
	if !ok || len(body) == 0 {
		t.Fatal("release failed")
	}
	if count, err := db.CountItemsByContainers(context.Background(), uid, []string{"keepnet"}); err != nil || count != 1 {
		t.Fatalf("released imported item remained: %d %v", count, err)
	}
	body, ok = hItemCount(keepnetTestCtx(srv, uid, proto.U32(5)))
	if !ok || decodeKeepnetBody(t, body).Fields[0].U32() != 1 {
		t.Fatal("released fish still occupied keepnet slots")
	}
	body, ok = hItemDetails(keepnetTestCtx(srv, uid, proto.U32(5), proto.GuidArr([]string{a.EchoGUID, b.EchoGUID, c.EchoGUID})))
	if !ok || len(decodeKeepnetBody(t, body).Items) != 1 {
		t.Fatal("released fish remained visible")
	}

	sold := keepnetTestCtx(srv, uid, proto.Guid(b.EchoGUID), proto.Guid(b.EchoGUID), proto.Guid(foreign.EchoGUID))
	body, ok = hMarketSell(sold)
	if !ok || len(decodeKeepnetBody(t, body).Items) != 1 {
		t.Fatal("imported fish was not sold exactly once")
	}
	if count, _ := db.CountItemsByContainers(context.Background(), uid, []string{"keepnet"}); count != 0 {
		t.Fatal("sold imported snapshot remained")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(db.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if err := reopened.Migrate(); err != nil {
		t.Fatal(err)
	}
	db = reopened
	restarted := NewServer(srv.cfg, srv.log, nil)
	restarted.SetDB(db)
	body, _ = hItemCount(keepnetTestCtx(restarted, uid, proto.U32(5)))
	if decodeKeepnetBody(t, body).Fields[0].U32() != 0 {
		t.Fatal("removed fish reappeared after reconnect")
	}
	if list, err := db.KeepnetList(context.Background(), uid+1); err != nil || len(list) != 1 {
		t.Fatal("another player's fish was removed")
	}
	var transactions int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM txn WHERE uid=? AND kind='sell_fish'`, uid).Scan(&transactions); err != nil || transactions != 1 {
		t.Fatalf("sell transactions=%d err=%v", transactions, err)
	}
}

func TestImportedKeepnetSellFailureRollsBackWalletAndBothRows(t *testing.T) {
	srv, db := newEconomyTxTestServer(t)
	const uid int64 = 7357
	if _, err := db.EnsureProfile(context.Background(), uid); err != nil {
		t.Fatal(err)
	}
	fish, _ := seedImportedKeepnetFish(t, srv, uid, 21, true)
	if _, err := db.SQL.Exec(`UPDATE profile SET silver=100 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`CREATE TRIGGER fail_imported_fish_cleanup BEFORE DELETE ON item
		BEGIN SELECT RAISE(ABORT,'snapshot unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	body, ok := hMarketSell(keepnetTestCtx(srv, uid, proto.Guid(fish.EchoGUID)))
	if !ok || len(body) != 1 || body[0] != 0x0f {
		t.Fatalf("sale failure was not reported: %x", body)
	}
	var silver, transactions int64
	if err := db.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=?`, uid).Scan(&silver); err != nil || silver != 100 {
		t.Fatalf("failed sale changed wallet: %d %v", silver, err)
	}
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM txn WHERE uid=? AND kind='sell_fish'`, uid).Scan(&transactions); err != nil || transactions != 0 {
		t.Fatal("failed sale left a credit transaction")
	}
	if _, err := db.KeepnetGet(context.Background(), uid, fish.EchoGUID); err != nil {
		t.Fatal("failed sale removed the keepnet row")
	}
	if _, err := db.GetItemByGUID(context.Background(), fish.EchoGUID); err != nil {
		t.Fatal("failed sale removed the imported snapshot")
	}
}
