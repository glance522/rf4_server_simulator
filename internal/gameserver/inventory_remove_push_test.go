package gameserver

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func TestDiscardOrdinaryItemPushesOnlyRemovedOwnedGUIDs(t *testing.T) {
	srv, db := newRigTestServer(t)
	connection, peer := cleanupTestTunnel(t, "discard-push-test")
	sess := registerBiteTestSession(t, srv, 501, connection)
	ctx := context.Background()
	own, err := srv.CreateItem(ctx, sess.Userid, "fishing_pliers", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := srv.CreateItem(ctx, sess.Userid+1, "fishing_pliers", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	received := receiveTestPush(peer, 4, 21)
	body, handled := hItemRemove(&Ctx{Sess: sess, Main: 4, Sub: 7, ExpectResponse: true, DecodeOK: true,
		Tree: proto.Args524(proto.GuidArr([]string{own.GUID, foreign.GUID, own.GUID})), Log: logging.Discard()})
	if !handled || len(body) == 0 {
		t.Fatal("discard did not return a snapshot")
	}
	select {
	case envelope := <-received:
		if envelope == nil || envelope.Main != 4 || envelope.Sub != 21 {
			t.Fatalf("missing removal push: %+v", envelope)
		}
		value, _, err := proto.DecodeValue(envelope.Body, 0, srv.Table())
		if err != nil {
			t.Fatal(err)
		}
		guids := deepGUIDs(value)
		if len(guids) != 1 || guids[0] != own.GUID {
			t.Fatalf("wrong removed GUIDs: %v", guids)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ordinary item removal was not pushed")
	}
	if _, err := db.GetItemByGUID(ctx, own.GUID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("owned item remained: %v", err)
	}
	if _, err := db.GetItemByGUID(ctx, foreign.GUID); err != nil {
		t.Fatalf("foreign item removed: %v", err)
	}
}
