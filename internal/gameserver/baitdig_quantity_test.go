package gameserver

import (
	"math/rand"
	"net"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/tunnel"
)

func randIntnFromSeed(seed int64) func(int) int {
	rng := rand.New(rand.NewSource(seed))
	return rng.Intn
}

func TestRollBaitDigLootKinds(t *testing.T) {
	// 熟练度 100：铲子取 80 档表（7 种可选），一次最多出 baitMaxKinds 种且不重复。
	table := baitLootTableFor(baitShovelItemID, 100)
	allowed := map[string]bool{}
	for _, entry := range table {
		allowed[entry.Key] = true
	}
	multi := 0
	for seed := int64(1); seed <= 500; seed++ {
		rng := randIntnFromSeed(seed)
		loot := rollBaitDigLoot(baitShovelItemID, 100, rng)
		if len(loot) < 1 || len(loot) > baitMaxKinds {
			t.Fatalf("seed=%d 种类=%d, want 1..%d", seed, len(loot), baitMaxKinds)
		}
		if len(loot) > 1 {
			multi++
		}
		seen := map[string]bool{}
		for _, drop := range loot {
			if !allowed[drop.Key] {
				t.Fatalf("seed=%d 未授权饵 %s", seed, drop.Key)
			}
			if seen[drop.Key] {
				t.Fatalf("seed=%d 饵重复: %+v", seed, loot)
			}
			seen[drop.Key] = true
			if drop.Qty < 1 || drop.Qty > 3 {
				t.Fatalf("seed=%d %s 数量=%d, want 1..3", seed, drop.Key, drop.Qty)
			}
		}
	}
	if multi < 100 {
		t.Fatalf("500 次只有 %d 次出多饵，随机分布异常", multi)
	}
}

// 种类数随熟练度分档推进：档位越高，下界与上界都更高，最高 baitMaxKinds 种。
func TestBaitDigKindCountGrowsWithMastery(t *testing.T) {
	lowest := func(int) int { return 0 }
	highest := func(n int) int { return n - 1 }
	for _, tc := range []struct {
		mastery          float64
		minKinds, maxKnd int
	}{
		{0, 1, 1},
		{19.9, 1, 1},
		{20, 1, 2},
		{40, 2, 3},
		{60, 3, 5},
		{80, 4, 7},
		{100, 4, 7},
	} {
		if got := baitDigKindCount(tc.mastery, lowest); got != tc.minKinds {
			t.Fatalf("熟练度 %.1f 最小种类=%d，want %d", tc.mastery, got, tc.minKinds)
		}
		if got := baitDigKindCount(tc.mastery, highest); got != tc.maxKnd {
			t.Fatalf("熟练度 %.1f 最大种类=%d，want %d", tc.mastery, got, tc.maxKnd)
		}
	}
	// 任何熟练度、任何 rng 都不能少于 1 种或超过上限。
	for m := 0.0; m <= 100; m += 0.5 {
		if got := baitDigKindCount(m, highest); got < 1 || got > baitMaxKinds {
			t.Fatalf("熟练度 %.1f 种类=%d，越界 1..%d", m, got, baitMaxKinds)
		}
	}
	// 上界随熟练度单调不减。
	prev := 0
	for _, mastery := range []float64{0, 20, 40, 60, 80, 100} {
		cur := baitDigKindCount(mastery, highest)
		if cur < prev {
			t.Fatalf("熟练度 %.0f 种类上界 %d 低于上一档 %d", mastery, cur, prev)
		}
		prev = cur
	}
}

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
		// 勺子的解锁锚点从 45 起：熟练度 0 时无饵可挖，因此这里用满熟练度。
		{name: "merged_scoop", tool: baitScoopItemID, initial: 100, mastery: 100},
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
			for _, pool := range baitDigPools {
				for _, entry := range pool {
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
				produced := baitDigItems(t, result.Field(1).Val)
				if len(produced) < 1 || len(produced) > baitMaxKinds {
					t.Fatalf("dig %d 产出种类=%d, want 1..%d", dig, len(produced), baitMaxKinds)
				}
				type gained struct {
					guid  string
					qty   int64
					stack int64
				}
				records := make([]gained, 0, len(produced))
				seen := map[string]bool{}
				for _, item := range produced {
					guid := fieldGuidHex(item, fieldIdxByCS(item.Class, csGUID))
					row, err := db.GetItemByGUID(ctx, guid)
					if err != nil || row == nil || row.UID != uid || row.Container != "backpack" {
						t.Fatalf("dig %d result does not identify the backpack stack: row=%+v err=%v", dig, row, err)
					}
					if seen[row.ItemID] {
						t.Fatalf("dig %d 同一种饵出现两次: %s", dig, row.ItemID)
					}
					seen[row.ItemID] = true
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
						t.Fatalf("dig %d %s gain=%d, want 1..%d", dig, row.ItemID, gain, maxGain)
					}
					if got := baitDigTreeQuantity(t, item); got != gain {
						t.Fatalf("dig %d 803.f1 %s gain=%d, want %d (prior=%d, backpack total=%d)",
							dig, row.ItemID, got, gain, prior, row.Qty)
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
					records = append(records, gained{guid: guid, qty: gain, stack: row.Qty})
				}
				for _, idx := range []int{6, 7} {
					inventory := baitDigItems(t, result.Field(idx).Val)
					if len(inventory) != len(records) {
						t.Fatalf("dig %d 803.f%d 件数=%d, want %d", dig, idx, len(inventory), len(records))
					}
					for i, item := range inventory {
						if got := baitDigTreeQuantity(t, item); got != records[i].stack {
							t.Fatalf("dig %d 803.f%d[%d] inventory total=%d, want %d",
								dig, idx, i, got, records[i].stack)
						}
						if got := fieldGuidHex(item, fieldIdxByCS(item.Class, csGUID)); got != records[i].guid {
							t.Fatalf("803.f%d[%d] stack GUID=%s, want %s", idx, i, got, records[i].guid)
						}
					}
				}
			}
			if kept, err := db.GetItemByGUID(ctx, tool.GUID); err != nil || kept == nil || kept.ItemID != tc.tool {
				t.Fatalf("digging removed the tool: row=%+v err=%v", kept, err)
			}
		})
	}
}

func TestBaitDigReportsAchievementForEveryProducedKind(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	tool, err := srv.CreateItem(ctx, uid, baitScoopItemID, "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	// 勺子的饵从熟练度 45 起解锁，满熟练度一次最多可覆盖 7 种饵与多个成就。
	if _, err := db.SQL.ExecContext(ctx, `INSERT OR REPLACE INTO skill(uid, skill_id, points) VALUES(?,?,?)`,
		uid, captureBaitSkillID, baitMasteryCap); err != nil {
		t.Fatal(err)
	}
	hctx := &Ctx{Sess: newProbeSess(srv), Main: 9, Sub: 3,
		Tree: proto.Args524(proto.Guid(tool.GUID)), DecodeOK: true, Log: logging.Discard()}
	body, handled := hCraftMine2(hctx)
	if !handled || len(body) == 0 {
		t.Fatal("9/3 挖饵未处理")
	}
	result := decodeTreeValue(t, srv, body)
	produced := baitDigItems(t, result.Field(1).Val)

	achievementByBait := map[string]int64{
		"worm_01":         250,
		"bloodworm":       258,
		"caddisfly":       259,
		"naiad":           261,
		"unibeetle_larva": 272,
	}
	states, err := db.PlayerAchievementStates(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, item := range produced {
		guid := fieldGuidHex(item, fieldIdxByCS(item.Class, csGUID))
		row, err := db.GetItemByGUID(ctx, guid)
		if err != nil || row == nil {
			t.Fatalf("产出的饵没有落库: guid=%s err=%v", guid, err)
		}
		achID, ok := achievementByBait[row.ItemID]
		if !ok {
			continue
		}
		checked++
		st := states[achID]
		if !st.Unlocked || !st.Claimed {
			t.Fatalf("产出 %s 后成就 %d 未解锁: %+v", row.ItemID, achID, st)
		}
	}
	if checked == 0 {
		t.Fatalf("本次产出 %v 没有覆盖任何挖饵成就", produced)
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
	for _, key := range []string{"worm_01", "muckworm"} {
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
	// 挖饵成功后先推技能树，再按产出种类各推一条背包增量；这里只要求
	// 至少有一条回调，不断言回调总数（成就解锁也可能追加）。
	if !handled || len(body) == 0 || len(hctx.afterResponse) < 1 {
		t.Fatalf("digging did not register inventory push: handled=%t callbacks=%d", handled, len(hctx.afterResponse))
	}
	result := decodeTreeValue(t, srv, body)
	kinds := result.Field(6).Val.ArgCount()
	if kinds < 1 || kinds > baitMaxKinds {
		t.Fatalf("803.f6 种类=%d, want 1..%d", kinds, baitMaxKinds)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		hctx.afterResponse[0]()
	}()
	// 第一帧是 2/4 技能树，随后每种饵各一条 4/22。
	for i := 0; i < kinds+1; i++ {
		kind, sid, payload, err := peer.ReadRecord()
		if err != nil {
			t.Fatal(err)
		}
		env, ok := proto.ParseEnvelope(payload)
		wantMain, wantSub := uint8(2), uint8(4)
		if i > 0 {
			wantMain, wantSub = 4, 22
		}
		if kind != tunnel.TypeData || sid != 1 || !ok || env.Main != wantMain || env.Sub != wantSub {
			t.Fatalf("expected %d/%d push, got kind=%v sid=%d envelope=%+v", wantMain, wantSub, kind, sid, env)
		}
		if i == 0 {
			continue
		}
		inventory := baitDigItems(t, decodeTreeValue(t, srv, env.Body))
		if len(inventory) != 1 {
			t.Fatalf("4/22 应只带一件物品，实际 %d", len(inventory))
		}
		item := inventory[0]
		guid := fieldGuidHex(item, fieldIdxByCS(item.Class, csGUID))
		row, err := db.GetItemByGUID(ctx, guid)
		if err != nil || row == nil {
			t.Fatalf("4/22 does not match persisted stack: row=%+v err=%v", row, err)
		}
		if row.Qty < 101 || row.Qty > 103 {
			t.Fatalf("4/22 stack total=%d, want 101..103", row.Qty)
		}
		if got := baitDigTreeQuantity(t, item); got != row.Qty {
			t.Fatalf("4/22 inventory total=%d, want %d (not acquisition gain)", got, row.Qty)
		}
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("after-response callback did not finish")
	}
}

// baitDigItems 解出 803.f1/f6/f7 的物品数组。
func baitDigItems(t *testing.T, list *proto.Value) []*proto.Value {
	t.Helper()
	if list == nil || list.Kind != proto.KindArgs || list.ArgCount() < 1 {
		t.Fatalf("expected bait item list, got %+v", list)
	}
	out := make([]*proto.Value, 0, list.ArgCount())
	for i := 0; i < list.ArgCount(); i++ {
		item := list.ArgItem(i)
		if item.Kind != proto.KindObj {
			t.Fatalf("expected bait item object, got %+v", item)
		}
		out = append(out, item)
	}
	return out
}

func baitDigTreeQuantity(t *testing.T, tree *proto.Value) int64 {
	t.Helper()
	idx := fieldIdxByCS(tree.Class, csQty)
	if idx < 0 || tree.Field(idx).Num == nil {
		t.Fatalf("bait tree has no quantity field: %+v", tree)
	}
	return int64(u64Of(tree.Field(idx).Num))
}

// 土生 / 水生饵池：铲子（含新年铲）只能出土生饵、红虫长柄勺只能出水生饵，
// 两个池不允许有交集——换工具就换饵种，也是“单次不重复”的前提。
var (
	baitSoilPool = []string{
		"worm_01", "muckworm", "vypolzok_01", "dorbeetle",
		"unibeetle_larva", "maybug_larva", "gryllotalpa",
	}
	baitAquaticPool = []string{
		"bloodworm", "watergrass", "caddisfly", "naiad",
		"black_leech", "gammarus_fresh", "waterbug",
	}
	// baitUnlockAnchor 是策划给定的解锁锚点：熟练度未达锚点不得提前挖出。
	baitUnlockAnchor = map[string]int{
		"worm_01":         0,
		"muckworm":        0,
		"bloodworm":       45,
		"watergrass":      45,
		"naiad":           55,
		"caddisfly":       70,
		"vypolzok_01":     75,
		"black_leech":     80,
		"dorbeetle":       80,
		"maybug_larva":    90,
		"gryllotalpa":     95,
		"waterbug":        100,
		"gammarus_fresh":  100,
		"unibeetle_larva": 100,
	}
)

func baitKeySet(keys []string) map[string]bool {
	set := make(map[string]bool, len(keys))
	for _, key := range keys {
		set[key] = true
	}
	return set
}

func TestBaitDigPoolsAreToolExclusive(t *testing.T) {
	soil, aquatic := baitKeySet(baitSoilPool), baitKeySet(baitAquaticPool)
	for key := range soil {
		if aquatic[key] {
			t.Fatalf("饵 %s 同时属于土生池与水生池", key)
		}
	}
	for _, tc := range []struct {
		tool string
		want map[string]bool
	}{
		{tool: baitShovelItemID, want: soil},
		{tool: baitScoopItemID, want: aquatic},
	} {
		pool := baitDigPools[tc.tool]
		if len(pool) == 0 {
			t.Fatalf("%s 饵池为空", tc.tool)
		}
		seen := map[string]bool{}
		for _, entry := range pool {
			if !tc.want[entry.Key] {
				t.Fatalf("%s 池里混入池外饵 %s", tc.tool, entry.Key)
			}
			if seen[entry.Key] {
				t.Fatalf("%s 池里重复饵 %s", tc.tool, entry.Key)
			}
			seen[entry.Key] = true
			if entry.Weight <= 0 {
				t.Fatalf("%s 的 %s 权重=%d，必须为正", tc.tool, entry.Key, entry.Weight)
			}
			anchor, ok := baitUnlockAnchor[entry.Key]
			if !ok {
				t.Fatalf("%s 的 %s 未在测试锚点表登记", tc.tool, entry.Key)
			}
			if entry.Unlock != anchor {
				t.Fatalf("%s 的 %s 锚点=%d，want %d", tc.tool, entry.Key, entry.Unlock, anchor)
			}
		}
		// 池子必须完全一致：既不能少（饵挖不出来），也不能多（池外饵混入）。
		for key := range tc.want {
			if !seen[key] {
				t.Fatalf("%s 的饵池缺少 %s，该饵永远挖不出来", tc.tool, key)
			}
		}
		for key := range seen {
			if !tc.want[key] {
				t.Fatalf("%s 的饵池混入了池外饵 %s", tc.tool, key)
			}
		}
		// 满熟练度必须能解析出整池。
		table := baitLootTableFor(tc.tool, baitMasteryCap)
		if len(table) != len(tc.want) {
			t.Fatalf("%s 满熟练度可用饵种=%d，want %d", tc.tool, len(table), len(tc.want))
		}
		for _, entry := range table {
			if !tc.want[entry.Key] {
				t.Fatalf("%s 满熟练度解析出池外饵 %s", tc.tool, entry.Key)
			}
		}
	}
	// 新年铲没有独立池，走别名复用铲子池。
	if len(baitDigPools["shovel_ny25"]) != 0 {
		t.Fatalf("新年铲不应有独立饵池")
	}
	alias := baitLootTableFor("shovel_ny25", baitMasteryCap)
	shovel := baitLootTableFor(baitShovelItemID, baitMasteryCap)
	if len(alias) != len(shovel) {
		t.Fatalf("新年铲别名解析=%d 种，铲子=%d 种", len(alias), len(shovel))
	}
}

// 一次挖取只能翻出本工具池里的饵，且同一种饵不会重复出现。
func TestBaitDigRollStaysInToolPoolWithoutDuplicates(t *testing.T) {
	pools := map[string]map[string]bool{
		baitShovelItemID: baitKeySet(baitSoilPool),
		baitScoopItemID:  baitKeySet(baitAquaticPool),
	}
	for tool, want := range pools {
		for _, mastery := range []float64{0, 10, 20, 39.9, 40, 60, 79.9, 80, 100} {
			table := baitLootTableFor(tool, mastery)
			if len(table) == 0 {
				// 未达任何解锁锚点：不得产出任何饵。
				if loot := rollBaitDigLoot(tool, mastery, randIntnFromSeed(1)); len(loot) != 0 {
					t.Fatalf("%s 熟练度 %.1f 未解锁任何饵，却挖出 %+v", tool, mastery, loot)
				}
				continue
			}
			for seed := int64(1); seed <= 200; seed++ {
				loot := rollBaitDigLoot(tool, mastery, randIntnFromSeed(seed))
				if len(loot) < 1 || len(loot) > baitMaxKinds || len(loot) > len(table) {
					t.Fatalf("%s 熟练度 %.1f seed=%d 种类=%d（表内 %d 种），want 1..%d",
						tool, mastery, seed, len(loot), len(table), baitMaxKinds)
				}
				seen := map[string]bool{}
				for _, drop := range loot {
					if !want[drop.Key] {
						t.Fatalf("%s 熟练度 %.1f seed=%d 挖到池外饵 %s", tool, mastery, seed, drop.Key)
					}
					if seen[drop.Key] {
						t.Fatalf("%s 熟练度 %.1f seed=%d 同次挖取出现重复饵 %s: %+v",
							tool, mastery, seed, drop.Key, loot)
					}
					seen[drop.Key] = true
					if drop.Qty < 1 || drop.Qty > 3 {
						t.Fatalf("%s 熟练度 %.1f seed=%d %s 数量=%d，want 1..3",
							tool, mastery, seed, drop.Key, drop.Qty)
					}
				}
			}
		}
	}
}

// 解锁锚点：未达锚点不得挖出，恰好达到锚点必须能挖出，满熟练度覆盖整池。
func TestBaitDigUnlockAnchors(t *testing.T) {
	pools := map[string][]string{
		baitShovelItemID: baitSoilPool,
		baitScoopItemID:  baitAquaticPool,
	}
	for tool, keys := range pools {
		for _, key := range keys {
			anchor := baitUnlockAnchor[key]
			if anchor > 0 {
				below := baitLootTableFor(tool, float64(anchor)-0.1)
				if baitTableHasKey(below, key) {
					t.Fatalf("%s 的 %s 在熟练度 %.1f 就出现了，锚点是 %d", tool, key, float64(anchor)-0.1, anchor)
				}
			}
			at := baitLootTableFor(tool, float64(anchor))
			if !baitTableHasKey(at, key) {
				t.Fatalf("%s 的 %s 在锚点 %d 上仍不可挖", tool, key, anchor)
			}
		}
	}
	// 铲子在 0 就有两种（蚯蚓、红蚯蚓）；勺子锚点从 45 起，44.9 时无饵可挖。
	if got := baitLootTableFor(baitShovelItemID, 0); len(got) != 2 {
		t.Fatalf("铲子熟练度 0 应有 2 种饵，实得 %d", len(got))
	}
	if got := baitLootTableFor(baitScoopItemID, 44.9); len(got) != 0 {
		t.Fatalf("长柄勺熟练度 44.9 时饵表应为空，实得 %d 种", len(got))
	}
	if got := baitLootTableFor(baitScoopItemID, 45); len(got) != 2 {
		t.Fatalf("长柄勺熟练度 45 应有 2 种饵（红虫、一束藻类），实得 %d", len(got))
	}
}

func baitTableHasKey(table []baitLootEntry, key string) bool {
	for _, entry := range table {
		if entry.Key == key {
			return true
		}
	}
	return false
}

// “挖到某饵”成就点名的每种饵都必须能挖到，且只属于一个工具：
// 挖饵池与成就规则是两份清单，任何一边漏改都会让成就永远点不亮。
func TestBaitDigAchievementBaitsAreReachable(t *testing.T) {
	achievementBaits := map[int64]string{
		250: "worm_01",
		251: "muckworm",
		258: "bloodworm",
		259: "caddisfly",
		261: "naiad",
		265: "vypolzok_01",
		266: "black_leech",
		267: "dorbeetle",
		269: "maybug_larva",
		270: "gryllotalpa",
		271: "waterbug",
		272: "unibeetle_larva",
		275: "watergrass",
	}
	owners := map[string][]string{}
	for tool, pool := range baitDigPools {
		for _, entry := range pool {
			owners[entry.Key] = append(owners[entry.Key], tool)
		}
	}
	for id, key := range achievementBaits {
		tools := owners[key]
		if len(tools) != 1 {
			t.Fatalf("成就 %d 依赖的饵 %s 出现在 %v，want 恰好一个工具", id, key, tools)
		}
		found := false
		for _, entry := range baitLootTableFor(tools[0], baitMasteryCap) {
			if entry.Key == key {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("成就 %d 依赖的饵 %s 不在满熟练度档位表里", id, key)
		}
	}
}
