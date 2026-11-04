package gameserver

import (
	"net"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/tunnel"
)

func TestBaitDigReportsGainAndKeepsStackTotal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tool    string
		initial int64
		mastery float64
	}{
		{name: "first_and_repeated_gain", tool: baitShovelItemID},
		{name: "merged_shovel", tool: baitShovelItemID, initial: 100},
		{name: "merged_mastered_shovel", tool: baitShovelItemID, initial: 100, mastery: 100},
		{name: "merged_scoop", tool: baitScoopItemID, initial: 100},
		{name: "merged_new_year_shovel", tool: "shovel_ny25", initial: 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, db := newDetachTestServer(t)
			ctx := t.Context()
			const uid = int64(1000001)
			tool, err := srv.CreateItem(ctx, uid, tc.tool, "backpack", 1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.SQL.ExecContext(ctx, `INSERT OR REPLACE INTO skill(uid, skill_id, points) VALUES(?,?,?)`,
				uid, captureBaitSkillID, tc.mastery); err != nil {
				t.Fatal(err)
			}
			before := map[string]int64{}
			guids := map[string]string{}
			for _, bands := range baitDigBands {
				for _, band := range bands {
					for _, entry := range band.Table {
						if _, seen := before[entry.Key]; seen {
							continue
						}
						before[entry.Key] = tc.initial
						if tc.initial == 0 {
							continue
						}
						row, err := srv.prepareNewItem(ctx, uid, entry.Key, "backpack", 1, "")
						if err != nil {
							t.Fatal(err)
						}
						if err := setPreparedItemQuantity(row, tc.initial); err != nil {
							t.Fatal(err)
						}
						if err := db.InsertItem(ctx, row); err != nil {
							t.Fatal(err)
						}
						guids[entry.Key] = row.GUID
					}
				}
			}
			for dig := 1; dig <= 2; dig++ {
				hctx := &Ctx{Sess: newProbeSess(srv), Main: 9, Sub: 3,
					Tree: proto.Args524(proto.Guid(tool.GUID)), DecodeOK: true, Log: logging.Discard()}
				body, handled := hCraftMine2(hctx)
				if !handled || len(body) == 0 {
					t.Fatalf("dig %d was not handled", dig)
				}
				result := decodeTreeValue(t, srv, body)
				if result.TypeID != 803 || u64Of(result.Field(0).Num) != 1 {
					t.Fatalf("dig %d did not return successful 803: %+v", dig, result)
				}
				produced := baitDigSingleItem(t, result.Field(1).Val)
				guid := fieldGuidHex(produced, fieldIdxByCS(produced.Class, csGUID))
				row, err := db.GetItemByGUID(ctx, guid)
				if err != nil || row == nil || row.UID != uid || row.Container != "backpack" {
					t.Fatalf("dig %d result does not identify the backpack stack: row=%+v err=%v", dig, row, err)
				}
				prior, allowed := before[row.ItemID]
				if !allowed {
					t.Fatalf("unexpected bait %q", row.ItemID)
				}
				gain := row.Qty - prior
				maxGain := int64(1)
				if tc.mastery == 100 {
					maxGain = 3
				}
				if gain < 1 || gain > maxGain {
					t.Fatalf("dig %d gain=%d, want 1..%d", dig, gain, maxGain)
				}
				if got := baitDigTreeQuantity(t, produced); got != gain {
					t.Fatalf("dig %d 803.f1 gain=%d, want %d (prior=%d, backpack total=%d)",
						dig, got, gain, prior, row.Qty)
				}
				for _, idx := range []int{6, 7} {
					inventory := baitDigSingleItem(t, result.Field(idx).Val)
					if got := baitDigTreeQuantity(t, inventory); got != row.Qty {
						t.Fatalf("dig %d 803.f%d inventory total=%d, want %d", dig, idx, got, row.Qty)
					}
					if got := fieldGuidHex(inventory, fieldIdxByCS(inventory.Class, csGUID)); got != guid {
						t.Fatalf("803.f%d stack GUID=%s, want %s", idx, got, guid)
					}
				}
				if old := guids[row.ItemID]; old != "" && old != guid {
					t.Fatalf("merge changed stack GUID: %s -> %s", old, guid)
				}
				storedTree, err := srv.ItemToTree(ctx, row)
				if err != nil {
					t.Fatal(err)
				}
				if got := baitDigTreeQuantity(t, storedTree); got != row.Qty {
					t.Fatalf("persisted fields quantity=%d, row qty=%d", got, row.Qty)
				}
				var stacks int
				if err := db.SQL.QueryRowContext(ctx, `SELECT COUNT(*) FROM item WHERE uid=? AND item_id=? AND container='backpack'`,
					uid, row.ItemID).Scan(&stacks); err != nil {
					t.Fatal(err)
				}
				if stacks != 1 {
					t.Fatalf("bait %s has %d stacks, want 1", row.ItemID, stacks)
				}
				before[row.ItemID], guids[row.ItemID] = row.Qty, guid
			}
			if kept, err := db.GetItemByGUID(ctx, tool.GUID); err != nil || kept == nil || kept.ItemID != tc.tool {
				t.Fatalf("digging removed the tool: row=%+v err=%v", kept, err)
			}
		})
	}
}

func TestBaitDigInventoryPushKeepsStackTotal(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	tool, err := srv.CreateItem(ctx, uid, baitShovelItemID, "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"worm_01", "maggot"} {
		row, err := srv.prepareNewItem(ctx, uid, key, "backpack", 1, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := setPreparedItemQuantity(row, 100); err != nil {
			t.Fatal(err)
		}
		if err := db.InsertItem(ctx, row); err != nil {
			t.Fatal(err)
		}
	}

	serverNet, peerNet := net.Pipe()
	t.Cleanup(func() { _ = serverNet.Close(); _ = peerNet.Close() })
	if err := serverNet.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := peerNet.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	ready := make(chan *tunnel.Conn, 1)
	handshakeErr := make(chan error, 1)
	go func() {
		conn, err := tunnel.HandshakeServer(serverNet, srv.cfg.Tunnel.PSK)
		if err != nil {
			handshakeErr <- err
			return
		}
		ready <- conn
	}()
	peer, err := tunnel.HandshakeClient(peerNet, srv.cfg.Tunnel.PSK)
	if err != nil {
		t.Fatal(err)
	}
	var conn *tunnel.Conn
	select {
	case conn = <-ready:
	case err := <-handshakeErr:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	hctx := &Ctx{Sess: newSession(srv, 1, "baitdig-push-test", uid, conn), Main: 9, Sub: 3,
		Tree: proto.Args524(proto.Guid(tool.GUID)), DecodeOK: true, Log: logging.Discard()}
	body, handled := hCraftMine2(hctx)
	if !handled || len(body) == 0 || len(hctx.afterResponse) != 1 {
		t.Fatalf("digging did not register inventory push: handled=%t callbacks=%d", handled, len(hctx.afterResponse))
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		hctx.afterResponse[0]()
	}()
	for _, cmd := range [][2]uint8{{2, 4}, {4, 22}} {
		kind, sid, payload, err := peer.ReadRecord()
		if err != nil {
			t.Fatal(err)
		}
		env, ok := proto.ParseEnvelope(payload)
		if kind != tunnel.TypeData || sid != 1 || !ok || env.Main != cmd[0] || env.Sub != cmd[1] {
			t.Fatalf("expected %d/%d push, got kind=%v sid=%d envelope=%+v", cmd[0], cmd[1], kind, sid, env)
		}
		if cmd[0] == 4 {
			inventory := baitDigSingleItem(t, decodeTreeValue(t, srv, env.Body))
			if got := baitDigTreeQuantity(t, inventory); got != 101 {
				t.Fatalf("4/22 inventory total=%d, want 101 (not acquisition gain 1)", got)
			}
			guid := fieldGuidHex(inventory, fieldIdxByCS(inventory.Class, csGUID))
			row, err := db.GetItemByGUID(ctx, guid)
			if err != nil || row == nil || row.Qty != 101 {
				t.Fatalf("4/22 does not match persisted stack: row=%+v err=%v", row, err)
			}
		}
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("after-response callback did not finish")
	}
}

func baitDigSingleItem(t *testing.T, list *proto.Value) *proto.Value {
	t.Helper()
	if list == nil || list.Kind != proto.KindArgs || list.ArgCount() != 1 || list.ArgItem(0).Kind != proto.KindObj {
		t.Fatalf("expected one bait item, got %+v", list)
	}
	return list.ArgItem(0)
}

func baitDigTreeQuantity(t *testing.T, tree *proto.Value) int64 {
	t.Helper()
	idx := fieldIdxByCS(tree.Class, csQty)
	if idx < 0 || tree.Field(idx).Num == nil {
		t.Fatalf("bait tree has no quantity field: %+v", tree)
	}
	return int64(u64Of(tree.Field(idx).Num))
}
