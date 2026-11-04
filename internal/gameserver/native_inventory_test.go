package gameserver

import (
	"context"
	"fmt"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

func nativeInventoryCtx(srv *Server, container uint64, args ...*proto.Value) *Ctx {
	values := append([]*proto.Value{proto.U32(uint32(container))}, args...)
	return &Ctx{Sess: newSession(srv, 1, "test|native|1000001|session", 1000001, nil), Tree: proto.Args(proto.S("524"), values...), DecodeOK: true, Log: logging.Discard()}
}

func TestImportedInventoryPagingAndDynamicCounts(t *testing.T) {
	db := connectTestDB(t)
	_, srv := startGameserverWithDB(t, db)
	ctx := context.Background()
	for index, container := range []string{"native:17", "native:17", "native:18"} {
		guid := fmt.Sprintf("%032x", index+1)
		tree := wiregen.MustMakeObj("kbnbcppjkaf", map[int]*proto.Value{})
		tree.Fields[fieldIdxByCS(tree.Class, csGUID)].Hex = guid
		fields, err := FieldsToJSON(tree.Fields)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.InsertItem(ctx, &store.Item{GUID: guid, UID: 1000001, WireClass: tree.Class, TypeID: 100, Container: container, Slot: int64(index), Qty: 1, FieldsJSON: fields, Durability: 1, Workmanship: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.KVSet(ctx, "native-import:1000001:inventory", `{"countsByKind":{"17":{"a":2147483647,"b":2147483647,"c":899}}}`); err != nil {
		t.Fatal(err)
	}
	decodeBody := func(body []byte) *proto.Value {
		t.Helper()
		v, pos, err := proto.DecodeValue(body, 0, wiregen.Table())
		if err != nil || pos != len(body) {
			t.Fatalf("decode pos=%d err=%v", pos, err)
		}
		return v
	}
	body, ok := hItemCount(nativeInventoryCtx(srv, 17))
	if !ok {
		t.Fatal("count unhandled")
	}
	v := decodeBody(body)
	if f := v.Field(0); f.U32() != 2 {
		t.Fatal("stale imported count replaced current count")
	}
	if f := v.Field(1); f.U32() != 2147483647 {
		t.Fatal("imported capacity lost")
	}
	for start, want := range []int{1, 1, 0} {
		body, ok = hItemGUIDs(nativeInventoryCtx(srv, 17, proto.U32(uint32(start)), proto.U32(1)))
		if !ok {
			t.Fatal("page unhandled")
		}
		if got := len(decodeBody(body).Items); got != want {
			t.Fatalf("page %d got %d want %d", start, got, want)
		}
	}
	if _, err := db.SQL.Exec("DELETE FROM item WHERE guid=?", fmt.Sprintf("%032x", 1)); err != nil {
		t.Fatal(err)
	}
	body, _ = hItemCount(nativeInventoryCtx(srv, 17))
	v = decodeBody(body)
	if f := v.Field(0); f.U32() != 1 {
		t.Fatal("removed item remained counted")
	}
	body, _ = hItemDetails(nativeInventoryCtx(srv, 17, proto.GuidArr([]string{fmt.Sprintf("%032x", 3)})))
	if len(decodeBody(body).Items) != 0 {
		t.Fatal("other container item leaked")
	}
	body, _ = hItemDetails(nativeInventoryCtx(srv, 18, proto.GuidArr([]string{fmt.Sprintf("%032x", 3)})))
	if len(decodeBody(body).Items) != 1 {
		t.Fatal("imported container detail missing")
	}
}
