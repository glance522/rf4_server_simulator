package gameserver

import (
	"encoding/json"
	"math"
	"net"
	"strings"
	"testing"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/tunnel"
)

func selectedHookMarineRig(t *testing.T, s *Server, ids []string) (string, []string, []baitedHookSet) {
	t.Helper()
	rod, err := s.CreateItem(t.Context(), 1000001, "boatr_10222_SBH80_100", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !s.rigSetType(t.Context(), 1000001, rod.GUID, 59) {
		t.Fatal("marine rig rejected")
	}
	sources := []string{}
	for i, id := range ids {
		mountHookForBaitTest(t, s, rod.GUID, int64(i))
		source, _ := baitTestMount(t, s, rod.GUID, id, 4, int64(350+i))
		sources = append(sources, source)
	}
	groups := s.baitedHookSets(t.Context(), rod.GUID)
	if len(groups) != len(ids) {
		t.Fatalf("groups=%+v", groups)
	}
	return rod.GUID, sources, groups
}

func TestSelectedHookPartialNewMountAndSparseLegacyChain(t *testing.T) {
	s, db := newRigTestServer(t)
	rod, _, groups := selectedHookMarineRig(t, s, []string{"worm_01", "muckworm"})
	if groups[0].hookGUID == groups[1].hookGUID {
		t.Fatal("different hooks were merged")
	}
	// 新装饵拥有明确关联；移除映射后不能把旧稀疏数组当成位置证明。
	mountHookForBaitTest(t, s, rod, 2)
	if got := s.baitedHookSets(t.Context(), rod); len(got) != 2 {
		t.Fatalf("new partial chain=%+v", got)
	}
	if _, err := db.SQL.Exec("DELETE FROM rod_setting WHERE guid=? AND key LIKE 'bait_hook:%'", rod); err != nil {
		t.Fatal(err)
	}
	restarted := newFishingRecoveryServer(t, db)
	if got := restarted.baitedHookSets(t.Context(), rod); len(got) != 0 {
		t.Fatalf("unmapped sparse legacy chain guessed: %+v", got)
	}
	if f := restarted.rollFishForRig(t.Context(), 1000001, rod, "unknown-map", "worm_01", neutralFishingSkillBonus()); f != nil {
		t.Fatal("unmapped sparse chain issued fish")
	}
}

func TestSelectedHookReloadKeepsOtherMissingRequestsAcrossRestart(t *testing.T) {
	s, db := newRigTestServer(t)
	rod, sources, groups := selectedHookMarineRig(t, s, []string{"worm_01", "muckworm", "fly"})
	for i := 0; i < 2; i++ {
		if _, err := s.consumeBaitEvent(t.Context(), 1000001, rod, "missing-"+groups[i].hookGUID, groups[i].baitGUIDs, false); err != nil {
			t.Fatal(err)
		}
	}
	// 第二个缺位暂时没库存；补第一个位置不能清掉它的请求。
	if _, err := db.SQL.Exec("UPDATE item SET qty=0 WHERE guid=?", sources[1]); err != nil {
		t.Fatal(err)
	}
	restarted := newFishingRecoveryServer(t, db)
	if _, err := restarted.reloadBaitForCast(t.Context(), 1000001, rod); err != nil {
		t.Fatal(err)
	}
	got := restarted.baitedHookSets(t.Context(), rod)
	if len(got) != 2 || got[0].hookGUID != groups[0].hookGUID || got[1].hookGUID != groups[2].hookGUID {
		t.Fatalf("partial reload=%+v", got)
	}
	var request string
	if err := db.SQL.QueryRow("SELECT value FROM rod_setting WHERE guid=? AND key='rebait:1'", rod).Scan(&request); err != nil {
		t.Fatalf("other missing request was removed: %v", err)
	}
	var decoded rebaitRequest
	if err := json.Unmarshal([]byte(request), &decoded); err != nil || decoded.HookGUID != groups[1].hookGUID {
		t.Fatalf("remaining request=%q/%v", request, err)
	}
	var successful int
	if err := db.SQL.QueryRow("SELECT COUNT(*) FROM rod_setting WHERE guid=? AND key='rebait:0'", rod).Scan(&successful); err != nil || successful != 0 {
		t.Fatalf("successful request not cleared: %d/%v", successful, err)
	}
	if _, err := db.SQL.Exec("UPDATE item SET qty=3 WHERE guid=?", sources[1]); err != nil {
		t.Fatal(err)
	}
	restarted = newFishingRecoveryServer(t, db)
	if _, err := restarted.reloadBaitForCast(t.Context(), 1000001, rod); err != nil {
		t.Fatal(err)
	}
	got = restarted.baitedHookSets(t.Context(), rod)
	if len(got) != 3 {
		t.Fatalf("both missing hooks were not restored: %+v", got)
	}
	for i, g := range got {
		if g.hookGUID != groups[i].hookGUID || len(g.baits) != 1 || g.baits[0] != []string{"worm_01", "muckworm", "fly"}[i] {
			t.Fatalf("hook %d changed: %+v", i, g)
		}
	}
	var requests int
	if err := db.SQL.QueryRow("SELECT COUNT(*) FROM rod_setting WHERE guid=? AND key LIKE 'rebait:%'", rod).Scan(&requests); err != nil || requests != 0 {
		t.Fatalf("requests=%d/%v", requests, err)
	}
	row, _ := db.GetItemByGUID(t.Context(), sources[1])
	qty := row.Qty
	if _, err := restarted.reloadBaitForCast(t.Context(), 1000001, rod); err != nil {
		t.Fatal(err)
	}
	row, _ = db.GetItemByGUID(t.Context(), sources[1])
	if row.Qty != qty {
		t.Fatal("repeated reload deducted more inventory")
	}
}

func TestSelectedHookRollUsesEachHooksDefinitionAndBaitSnapshot(t *testing.T) {
	s, db := newRigTestServer(t)
	rod, _, groups := selectedHookMarineRig(t, s, []string{"worm_01", "muckworm"})
	// 派鱼默认不按钩号截断，这里要验证“某个钩组判不出鱼就换下一个钩组”的兜底路径，
	// 所以显式打开钩入口截断，让 1mm 那个钩（上限 67.5g）判不出 80g 起的美洲池鱼。
	if err := db.TunableSet(t.Context(), "hook_entry_cap", "1", "test"); err != nil {
		t.Fatal(err)
	}
	s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
	for i, g := range groups {
		row, err := db.GetItemByGUID(t.Context(), g.hookGUID)
		if err != nil {
			t.Fatal(err)
		}
		def, err := s.itemDefForRow(t.Context(), row)
		if err != nil {
			t.Fatal(err)
		}
		var attrs map[string]any
		if err := json.Unmarshal([]byte(def.AttrsJSON), &attrs); err != nil {
			t.Fatal(err)
		}
		// 第一个钩按现有下限最多约67.5克，美洲池鱼重至少80克；第二个钩可判鱼。
		length := float32(1)
		if i == 1 {
			length = 20
		}
		attrs[gearAttrHookLen] = uint64(math.Float32bits(length))
		data, _ := json.Marshal(attrs)
		numeric := int64(990100 + i)
		itemID := []string{"selected_tiny_hook", "selected_large_hook"}[i]
		if _, err := db.SQL.Exec("INSERT INTO item_def(numeric_id,item_id,category,kind,brand,attrs_json) VALUES(?,?,12,'hook','',?)", numeric, itemID, string(data)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.SQL.Exec("UPDATE item SET numeric_id=?,item_id=? WHERE guid=?", numeric, itemID, row.GUID); err != nil {
			t.Fatal(err)
		}
	}
	for range 32 {
		fish := s.rollFishForRig(t.Context(), 1000001, rod, "level_019_american_pond", "worm_01", neutralFishingSkillBonus())
		if fish == nil || fish.HookGUID != groups[1].hookGUID || !fish.BaitsKnown || fish.BaitGUIDs != groups[1].baitGUIDs[0] {
			t.Fatalf("selected hook roll=%+v", fish)
		}
	}
}

func TestSelectedHookPendingSnapshotKeepsHookAndSupportsLegacyJSON(t *testing.T) {
	s, db := newRigTestServer(t)
	rod, _, groups := selectedHookMarineRig(t, s, []string{"worm_01", "muckworm"})
	f := &RolledFish{FishGUID: newFishGUID(), HookGUID: groups[1].hookGUID, BaitGUIDs: strings.Join(groups[1].baitGUIDs, " "), BaitsKnown: true, Species: "crucian", WeightG: 200, Grade: 1}
	state := &store.FishingState{UID: 1000001, GearGUID: rod, CastActive: true, PushSent: true, Pending: rolledFishToStore(f), BiteSpeed: 1}
	if err := db.SaveFishingState(t.Context(), state); err != nil {
		t.Fatal(err)
	}
	restarted := newFishingRecoveryServer(t, db)
	got := restarted.stateFor(1000001).Pending
	if got == nil || got.HookGUID != f.HookGUID || got.BaitGUIDs != f.BaitGUIDs || got.FishGUID != f.FishGUID {
		t.Fatalf("restored fish=%+v", got)
	}
	var legacy store.FishingFish
	if err := json.Unmarshal([]byte(`{"fish_guid":"11111111111111111111111111111111","species":"crucian","weight_g":200}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if old := rolledFishFromStore(&legacy); old == nil || old.HookGUID != "" || old.BaitsKnown || old.FishGUID != legacy.FishGUID {
		t.Fatalf("legacy fish=%+v", old)
	}
}

func TestSelectedHookPushUsesSelectedHookAndConsumesOnlyItsBait(t *testing.T) {
	s, db := newRigTestServer(t)
	rod, _, groups := selectedHookMarineRig(t, s, []string{"worm_01", "muckworm"})
	serverNet, peerNet := net.Pipe()
	ready := make(chan *tunnel.Conn, 1)
	handshakeErr := make(chan error, 1)
	go func() {
		c, err := tunnel.HandshakeServer(serverNet, s.cfg.Tunnel.PSK)
		if err != nil {
			handshakeErr <- err
			return
		}
		ready <- c
	}()
	peer, err := tunnel.HandshakeClient(peerNet, s.cfg.Tunnel.PSK)
	if err != nil {
		t.Fatal(err)
	}
	var connection *tunnel.Conn
	select {
	case connection = <-ready:
	case err := <-handshakeErr:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("handshake timed out")
	}
	t.Cleanup(func() { connection.Close(); peer.Close() })
	sess := newSession(s, 905, "selected-hook-test", 1000001, connection)
	pushes := make(chan *proto.Envelope, 16)
	go func() {
		for {
			kind, _, payload, err := peer.ReadRecord()
			if err != nil {
				return
			}
			if kind == tunnel.TypeData {
				if env, ok := proto.ParseEnvelope(payload); ok {
					pushes <- env
				}
			}
		}
	}()
	f := &RolledFish{FishGUID: newFishGUID(), HookGUID: groups[1].hookGUID, BaitGUIDs: strings.Join(groups[1].baitGUIDs, " "), BaitsKnown: true, Species: "crucian", WeightG: 200, Grade: 1, LengthM: .2}
	s.pushBaitedFish(sess, f, rod, 24)
	select {
	case env := <-pushes:
		if env.Main != 14 || env.Sub != 14 {
			t.Fatalf("first push=%d/%d", env.Main, env.Sub)
		}
		value, _, err := proto.DecodeValue(env.Body, 0, s.Table())
		if err != nil {
			t.Fatal(err)
		}
		if hook := value.ArgItem(1).Field(23).Hex; hook != groups[1].hookGUID {
			t.Fatalf("push selected hook=%s", hook)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("selected hook push missing")
	}
	if row, _ := db.GetItemByGUID(t.Context(), groups[0].baitGUIDs[0]); row == nil || baitQuantity(row) != 1 {
		t.Fatal("other hook bait was consumed")
	}
	if row, _ := db.GetItemByGUID(t.Context(), groups[1].baitGUIDs[0]); row == nil || baitQuantity(row) != 1 {
		t.Fatal("selected hook bait was consumed before hook confirmation")
	}
	if err := s.consumeHookedBait(sess, f, rod); err != nil {
		t.Fatal(err)
	}
	if row, _ := db.GetItemByGUID(t.Context(), groups[1].baitGUIDs[0]); row != nil {
		t.Fatal("selected hook bait was not consumed after hook confirmation")
	}
	s.pushBaitedFish(sess, f, rod, 24)
	if err := s.consumeHookedBait(sess, f, rod); err != nil {
		t.Fatal(err)
	}
	if row, _ := db.GetItemByGUID(t.Context(), groups[0].baitGUIDs[0]); row == nil || baitQuantity(row) != 1 {
		t.Fatal("replay consumed another hook bait")
	}
}

func TestSelectedHookMappingKeepsEmbeddedLureHookInsteadOfSameOrdinalExternalHook(t *testing.T) {
	s, db := newRigTestServer(t)
	rod, err := s.CreateItem(t.Context(), 1000001, "boatr_10222_SBH80_100", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !s.rigSetType(t.Context(), 1000001, rod.GUID, 55) {
		t.Fatal("dual-lure rig rejected")
	}
	external := mountHookForBaitTest(t, s, rod.GUID, 0)
	_, lure := baitTestMount(t, s, rod.GUID, "spinnerbaits_1244", 1, 325)
	embedded := s.lureActiveHookGUID(t.Context(), lure)
	if embedded == "" || embedded == external {
		t.Fatal("test lure has no distinct factory hook")
	}
	if err := db.UpsertRodSetting(t.Context(), rod.GUID, "bait_hook:"+lure, embedded); err != nil {
		t.Fatal(err)
	}
	got := s.baitedHookSets(t.Context(), rod.GUID)
	if len(got) != 1 || got[0].hookGUID != embedded {
		t.Fatalf("saved factory hook replaced by ordinal: %+v", got)
	}
}

func TestSelectedHookFailedRequestCleanupCanResumeWithoutRemounting(t *testing.T) {
	s, db := newRigTestServer(t)
	rod, sources, groups := selectedHookMarineRig(t, s, []string{"worm_01", "muckworm", "fly"})
	for i := 0; i < 2; i++ {
		if _, err := s.consumeBaitEvent(t.Context(), 1000001, rod, "resume-"+groups[i].hookGUID, groups[i].baitGUIDs, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.SQL.Exec("CREATE TRIGGER selected_hook_rebait_failure BEFORE DELETE ON rod_setting WHEN OLD.key='rebait:0' BEGIN SELECT RAISE(ABORT,'rebait cleanup failure'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.reloadBaitForCast(t.Context(), 1000001, rod); err == nil {
		t.Fatal("forced request cleanup failure was ignored")
	}
	var count int
	if err := db.SQL.QueryRow("SELECT COUNT(*) FROM rod_setting WHERE guid=? AND key IN ('rebait:0','rebait:1')", rod).Scan(&count); err != nil || count != 2 {
		t.Fatalf("uncompleted request lost: %d/%v", count, err)
	}
	first, _ := db.GetItemByGUID(t.Context(), sources[0])
	remaining := first.Qty
	if _, err := db.SQL.Exec("DROP TRIGGER selected_hook_rebait_failure"); err != nil {
		t.Fatal(err)
	}
	restarted := newFishingRecoveryServer(t, db)
	if _, err := restarted.reloadBaitForCast(t.Context(), 1000001, rod); err != nil {
		t.Fatal(err)
	}
	got := restarted.baitedHookSets(t.Context(), rod)
	if len(got) != 3 {
		t.Fatalf("recovery failed: %+v", got)
	}
	first, _ = db.GetItemByGUID(t.Context(), sources[0])
	if first.Qty != remaining {
		t.Fatal("already restored hook was mounted again after restart")
	}
	for i, g := range got {
		if g.hookGUID != groups[i].hookGUID {
			t.Fatalf("hook %d changed after cleanup failure", i)
		}
	}
}
