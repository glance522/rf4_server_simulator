package gameserver

import (
	"testing"
	"time"

	"rf4server/internal/proto"
)

func TestPush4_22HookMountedFlag(t *testing.T) {
	t.Parallel()
	srv, _ := newRigTestServer(t)
	rod := baitTestRod(t, srv)
	hookGUID := mountHookForBaitTest(t, srv, rod, 0)

	for _, mounted := range []bool{false, true} {
		name := "unmounted"
		if mounted {
			name = "mounted"
		}
		t.Run(name, func(t *testing.T) {
			sess, peer := newTunnelProbe(t, srv)
			st := srv.stateFor(sess.Userid)
			st.mu.Lock()
			st.GearGuid = rod
			st.mu.Unlock()

			received := receiveTestPush(peer, 4, 22)
			sess.Push4_22Hook(mounted, rod)

			select {
			case env := <-received:
				if env == nil {
					t.Fatal("4/22 hook push missing")
				}
				tree, _, err := proto.DecodeValue(env.Body, 0, srv.Table())
				if err != nil {
					t.Fatal(err)
				}
				if tree.ArgCount() != 1 {
					t.Fatalf("4/22 hook push count = %d, want 1", tree.ArgCount())
				}
				hook := tree.ArgItem(0)
				if hook.Class != "agedjipcnpe" || hook.Field(fieldIdxByCS(hook.Class, csGUID)).Hex != hookGUID {
					t.Fatalf("4/22 hook identity = %s/%s, want agedjipcnpe/%s", hook.Class, hook.Field(fieldIdxByCS(hook.Class, csGUID)).Hex, hookGUID)
				}
				flags := u64Of(hook.Field(fieldIdxByCS(hook.Class, csStateEnum)).Num)
				if got := flags&hookMountedFlag != 0; got != mounted {
					t.Fatalf("hook mounted bit = %v, want %v (flags=%#x)", got, mounted, flags)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("waiting for 4/22 hook push timed out")
			}
		})
	}
}
