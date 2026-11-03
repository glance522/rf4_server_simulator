package gameserver

import (
	"context"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"testing"
)

func TestTakingRodRefreshesActualRigFamily(t *testing.T) {
	srv, _ := newRigTestServer(t)
	sess := newProbeSess(srv)
	ctx := context.Background()
	for _, tc := range []struct {
		item string
		rig  int64
	}{{"bolo_10022_LB400", 1}, {"boatr_10213_050", 55}, {"pilkr_10208_f88", 50}, {"feeder_10125_FD360", 12}, {"cast_10055_CSM68UL", 5}} {
		row, err := srv.CreateItem(ctx, sess.Userid, tc.item, "backpack", 1)
		if err != nil {
			t.Fatal(err)
		}
		body, ok := hRodTake(&Ctx{Sess: sess, Tree: rigRequestTree(t, proto.Guid(row.GUID)), Log: logging.Discard()})
		if !ok || len(body) == 0 {
			t.Fatalf("take %s failed", tc.item)
		}
		st := srv.stateFor(sess.Userid)
		st.mu.Lock()
		got, gear := st.RigType, st.GearGuid
		st.mu.Unlock()
		if got != tc.rig || gear != row.GUID {
			t.Fatalf("%s retained previous rod: rig=%d want=%d gear=%s", tc.item, got, tc.rig, gear)
		}
	}
}
func TestTakeRejectsForeignNonRodAndMissingItem(t *testing.T) {
	srv, _ := newRigTestServer(t)
	sess := newProbeSess(srv)
	ctx := context.Background()
	foreign, err := srv.CreateItem(ctx, sess.Userid+1, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := srv.CreateItem(ctx, sess.Userid, "clas_20163", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, guid := range []string{foreign.GUID, hook.GUID, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		body, ok := hRodTake(&Ctx{Sess: sess, Tree: rigRequestTree(t, proto.Guid(guid)), Log: logging.Discard()})
		if !ok || len(body) == 0 {
			t.Fatal("invalid rod did not return empty-hand context")
		}
		decoded, _, err := proto.DecodeValue(body, 0, srv.Table())
		if err != nil {
			t.Fatal(err)
		}
		first := decoded.Field(0)
		if first.GuidHex() != zeroGUID {
			t.Fatalf("invalid item taken: %s", guid)
		}
	}
}
