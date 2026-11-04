package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// TestKeepnetThrowRemovesFish 4/7（扔回水里）：容器=5 时按 keepnet 回显 GUID
// 删除护内鱼；此前按 item 表删除会静默无操作，鱼护里的鱼扔不出去。
func TestKeepnetThrowRemovesFish(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	entry := &store.KeepnetEntry{
		EchoGUID: "aa112233445566778899aabbccddeeff", UID: uid,
		Species: "g_tench", NameZh: "丁鱵", WeightG: 3280, Cat: 2,
	}
	if err := db.KeepnetInsert(ctx, entry); err != nil {
		t.Fatal(err)
	}

	sess := newProbeSess(srv)
	tree := proto.Args(nil, proto.U32(uint32(containerKeepnet)), proto.GuidArr([]string{entry.EchoGUID}))
	hctx := &Ctx{Sess: sess, Main: 4, Sub: 7, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()}
	body, ok := hItemRemove(hctx)
	if !ok || len(body) == 0 {
		t.Fatal("4/7 未处理或无响应")
	}
	if got, err := db.KeepnetGet(ctx, uid, entry.EchoGUID); err == nil && got != nil {
		t.Fatalf("护内鱼应已删除: %+v", got)
	}
}
