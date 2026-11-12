package gameserver

import (
	"fmt"
	"math"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// expectProfilePush 校验下单后紧跟的第一条推送是 2/1 档案（刷新余额）。
//
// 工坊 21/4 扣完钱要先推一次档案，再回包，最后才是延迟发的 4/22。官服同一单的
// 顺序是 nc.jsonl s2c#335 2/1（银币 5000→4970，正是这单修理费）→ #336 21/4← →
// #337 4/22。少了这条，客户端显示的余额不会变，玩家要退出重登才看到扣款。
// 这里顺带把「余额刷新必须存在」钉成契约，别在重构时漏掉。
func expectProfilePush(t *testing.T, env *proto.Envelope) {
	t.Helper()
	if env == nil {
		t.Fatal("下单后应有一条 2/1 档案推送，实际没有")
	}
	if env.Main != 2 || env.Sub != 1 {
		t.Fatalf("下单后第一条推送应是 2/1 档案（刷新余额），实得 %d/%d", env.Main, env.Sub)
	}
}

// assertRefreshMatchesReply 比对 4/22 推送与回包的物品树。
//
// 除版本戳（csCreated）之外必须一模一样，而且推送的戳必须**更大**：客户端按这一格
// 决定要不要采纳一次物品刷新，戳没变大就整包丢弃（连 f24 那把锁一起丢），面板会
// 一直停在"修好了"，要退出重连才变"修理中"。推送是延迟发的，发之前会把戳抬到
// 工坊链路的下一个号，见 stampWorkshopTree。官服同一单的 21/4 回包（s2c#336）与
// 4/22（s2c#337）也只差这一格。
func assertRefreshMatchesReply(t *testing.T, srv *Server, item, reply *proto.Value,
	replyBody []byte, label string) {
	t.Helper()
	if item == nil || item.Kind != proto.KindObj {
		t.Fatalf("%s: 推送里没有物品树", label)
	}
	if reply == nil || reply.Kind != proto.KindObj {
		t.Fatalf("%s: 回包不是物品树", label)
	}
	pi := fieldIdxByCS(item.Class, csCreated)
	ri := fieldIdxByCS(reply.Class, csCreated)
	if pi < 0 || ri < 0 {
		t.Fatalf("%s: 物品树没有版本戳 %s", label, csCreated)
	}
	if u64Of(item.Fields[pi].Num) <= u64Of(reply.Fields[ri].Num) {
		t.Fatalf("%s: 4/22 的版本戳必须比回包更新，否则客户端按 csCreated 丢弃这次刷新",
			label)
	}
	item.Fields[pi].Num = reply.Fields[ri].Num
	got, err := proto.EncodeValue(item, srv.Table())
	if err != nil {
		t.Fatalf("%s: 编码推送树失败: %v", label, err)
	}
	if string(got) != string(replyBody) {
		t.Fatalf("%s: 除版本戳之外，4/22 推送必须与回包一致", label)
	}
}

func snapshotFields(t *testing.T, raw string) []proto.Field {
	t.Helper()
	fields, err := FieldsFromJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return fields
}
func snapshotJSON(t *testing.T, fields []proto.Field) string {
	t.Helper()
	raw, err := FieldsToJSON(fields)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func snapshotPart(t *testing.T, srv *Server, fields []proto.Field, category int64) (*proto.Value, *store.ItemDef) {
	t.Helper()
	child, def, _, ok := srv.selectedReelPart(t.Context(), fields, category)
	if !ok || child == nil || def == nil {
		t.Fatalf("missing part category %d", category)
	}
	return child, def
}

func snapshotFloat(fields []proto.Field, class, name string) float64 {
	i := fieldIdxByCS(class, name)
	if i < 0 || i >= len(fields) {
		return math.NaN()
	}
	if fields[i].Num != nil {
		return float64(proto.F32FromBits(uint32(*fields[i].Num)))
	}
	return fields[i].Float
}

func assertReelSnapshot(t *testing.T, srv *Server, guid, gearID string, drag float64) *store.Item {
	t.Helper()
	ctx := t.Context()
	row := mustItem(t, srv.DB, guid)
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := srv.ItemToTree(ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	expected := snapshotFloat(tree.Fields, tree.Class, csStrength)
	if math.IsNaN(expected) || math.IsInf(expected, 0) {
		t.Fatal("invalid root")
	}
	// 新口径：轮根=出厂规格×轴承耐久（规格表收录的轮子），与摩擦片值解耦；
	// 未收录轮子保持旧语义（轮根=摩擦片值）。drag 参数只断言摩擦片状态。
	wantRoot := drag
	if spec, ok := reelRootSpec(row.ItemID); ok {
		_, _, bearingDur := srv.reelPartStateFromRow(ctx, row, reelPartCatBearing)
		wantRoot = spec * clampUnit(bearingDur)
	}
	if math.Abs(expected-wantRoot) > 1e-5 {
		t.Fatalf("root=%g want %g (washer=%g)", expected, wantRoot, drag)
	}
	if got := snapshotFloat(fields, row.WireClass, csStrength); math.IsNaN(got) || math.IsInf(got, 0) || math.Abs(got-expected) > 1e-5 {
		t.Fatalf("stored root=%g, normalized=%g", got, expected)
	}
	for _, fs := range [][]proto.Field{fields, tree.Fields} {
		child, def, _, ok := srv.selectedReelPart(ctx, fs, 52)
		if !ok || def == nil || def.ItemID != gearID || child == nil {
			t.Fatalf("installed gear changed: %+v", def)
		}
		expectedDef, err := srv.DB.GetItemDefByItemID(ctx, gearID)
		if err != nil || expectedDef == nil {
			t.Fatalf("gear definition: %v", err)
		}
		if itemDefF32(def, gearAttrRatio) != itemDefF32(expectedDef, gearAttrRatio) {
			t.Fatal("gear ratio changed")
		}
		washer, _, _, ok := srv.selectedReelPart(ctx, fs, 9)
		if !ok {
			t.Fatal("washer missing")
		}
		value, present, _ := frictionWasherDrag(washer)
		if !present || math.Abs(value-drag) > 1e-5 {
			t.Fatalf("washer state changed: %g want %g", value, drag)
		}
	}
	raw, err := proto.EncodeValue(tree, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	decoded, end, err := proto.DecodeValue(raw, 0, srv.Table())
	if err != nil || end != len(raw) {
		t.Fatalf("tree codec: %v", err)
	}
	if snapshotFloat(decoded.Fields, decoded.Class, csStrength) != expected {
		t.Fatal("wire root differs")
	}
	return row
}

func snapshotWorkshopRequest(guid, id string) *proto.Value {
	return proto.Args524(proto.StrVal("workshop_01"), wiregen.MustMakeObj("nfmkdcbnkhp", map[int]*proto.Value{
		0: proto.Guid(guid), 1: proto.Args(proto.S("503"), wiregen.MustMakeObj("gcllbdkgkna", map[int]*proto.Value{0: proto.U32(52), 1: proto.StrVal(id)})), 2: proto.U32(0), 3: proto.U64(0)}))
}

func TestReelPartSnapshotWorkshop(t *testing.T) {
	t.Parallel()
	for _, drag := range []float64{2.25, 0} {
		t.Run(fmt.Sprintf("drag_%g", drag), func(t *testing.T) {
			t.Parallel()
			srv, db := newStaticRigTestServer(t)
			defer db.Close()
			// 这组用例守的是"零件树快照装配"，不是排队时序：工坊换件必须当场
			// 落库才谈得上比对响应/推送与库内状态，所以显式打到即时路径。
			// 银币排队的语义由 workshop_repair_queue_test.go 单独覆盖。
			setWorkshopSilverRepairDelay(t, srv, db, false)
			ctx := t.Context()
			const uid = int64(1000001)
			if err := srv.EnsurePlayer(ctx, uid); err != nil {
				t.Fatal(err)
			}
			profile, err := db.EnsureProfile(ctx, uid)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.UpdateBalance(ctx, uid, 1000000, profile.Gold, profile.Bank); err != nil {
				t.Fatal(err)
			}
			reel, err := srv.CreateItem(ctx, uid, "spin_5354", "backpack", 1)
			if err != nil {
				t.Fatal(err)
			}
			fields := snapshotFields(t, reel.FieldsJSON)
			washer, _, _, ok := srv.selectedReelPart(ctx, fields, 9)
			if !ok {
				t.Fatal("washer missing")
			}
			setWireFloat(washer.Fields, washer.Class, fieldIdxByCS(washer.Class, csDragForce), drag)
			raw := snapshotJSON(t, fields)
			reel.FieldsJSON = raw
			srv.canonicalizeReelStrength(ctx, reel, fields)
			raw = snapshotJSON(t, fields)
			if err := db.UpdateItemFields(ctx, reel.GUID, raw, reel.Qty, reel.Durability, reel.Workmanship, reel.RigGUID); err != nil {
				t.Fatal(err)
			}
			_, stock := snapshotPart(t, srv, fields, 52)
			assertReelSnapshot(t, srv, reel.GUID, stock.ItemID, drag)
			session, peer := newTunnelProbe(t, srv)
			var previousRatio float64
			for _, id := range []string{"rgm_zmn_jet_jsg-r", "rgm_ef_spark10021"} {
				req := snapshotWorkshopRequest(reel.GUID, id)
				encoded, err := proto.EncodeValue(req, srv.Table())
				if err != nil {
					t.Fatal(err)
				}
				decoded, _, err := proto.DecodeValue(encoded, 0, srv.Table())
				if err != nil {
					t.Fatal(err)
				}
				c := &Ctx{Sess: session, Main: 21, Sub: 3, Tree: decoded, DecodeOK: true, ExpectResponse: true, Log: logging.Discard()}
				// 残值按槽位里**当前装着的旧件**折（客户端报价也是这么算的：
				// Σ(新件价 − 该槽位旧件残值)）。换型号时新旧两件价不同，拿新件
				// 价再折一次会算错差额 —— 这正是 2026-11-12 那三种报错的根因。
				curRow := mustItem(t, srv.DB, reel.GUID)
				_, curGearDef := snapshotPart(t, srv, snapshotFields(t, curRow.FieldsJSON), 52)
				oldBase, _ := srv.partPrice(ctx, curGearDef.ItemID, 52)
				balanceBefore, err := db.EnsureProfile(ctx, uid)
				if err != nil {
					t.Fatal(err)
				}
				body, ok := hWorkshopOrder(c)
				if !ok || len(body) == 0 {
					t.Fatal("workshop failed")
				}
				balanceAfter, err := db.EnsureProfile(ctx, uid)
				if err != nil {
					t.Fatal(err)
				}
				partDef, err := db.GetItemDefByItemID(ctx, id)
				if err != nil || partDef == nil {
					t.Fatalf("读取零件定义失败: %v", err)
				}
				baseSilver, _ := srv.partPrice(ctx, id, partDef.Category)
				residual, _ := srv.workshopPartResidual(partDef.Category, oldBase, 0)
				wantSilver := baseSilver - residual
				if wantSilver < 0 {
					// 旧件比新件值钱：差额按 0 收（以旧换新不退差价）。
					wantSilver = 0
				}
				if balanceBefore.Silver-balanceAfter.Silver != wantSilver {
					t.Fatalf("工坊扣费=%d，期望服务端价格=%d",
						balanceBefore.Silver-balanceAfter.Silver, wantSilver)
				}
				fresh := assertReelSnapshot(t, srv, reel.GUID, id, drag)
				reply, _, err := proto.DecodeValue(body, 0, srv.Table())
				if err != nil {
					t.Fatal(err)
				}
				stored := snapshotFields(t, fresh.FieldsJSON)
				if snapshotFloat(reply.Fields, reply.Class, csStrength) != snapshotFloat(stored, fresh.WireClass, csStrength) {
					t.Fatal("response differs from database")
				}
				if len(c.afterResponse) != 1 {
					t.Fatal("missing refresh")
				}
				for _, fn := range c.afterResponse {
					fn()
				}
				expectProfilePush(t, awaitPushEnvelope(t, peer))
				push := awaitPushEnvelope(t, peer)
				if push.Main != 4 || push.Sub != 22 {
					t.Fatalf("wrong push %d/%d", push.Main, push.Sub)
				}
				v, _, err := proto.DecodeValue(push.Body, 0, srv.Table())
				if err != nil {
					t.Fatal(err)
				}
				assertRefreshMatchesReply(t, srv, v.ArgItem(0), reply, body,
					"AfterResponse differs from response")
				def, defErr := db.GetItemDefByItemID(ctx, id)
				if defErr != nil || def == nil {
					t.Fatalf("gear definition: %v", defErr)
				}
				ratio := itemDefF32(def, gearAttrRatio)
				if previousRatio != 0 && ratio == previousRatio {
					t.Fatal("fixtures need different ratios")
				}
				previousRatio = ratio
			}
			// 同型号修理也是**按件**：21/4 只处理 args(503) 里列出的件。
			// nc.jsonl 那一单只列了齿轮，官服回包的 21/2 里轴承/摩擦片的 GUID
			// 一个都没动。旧口径在这里当成"整机大保养"，等于付一件的钱修整机。
			fresh := mustItem(t, db, reel.GUID)
			fs := snapshotFields(t, fresh.FieldsJSON)
			gear, _ := snapshotPart(t, srv, fs, 52)
			setWireFloat(gear.Fields, gear.Class, fieldIdxByCS(gear.Class, csDurability), .4)
			bearing, _ := snapshotPart(t, srv, fs, 7)
			setWireFloat(bearing.Fields, bearing.Class, fieldIdxByCS(bearing.Class, csDurability), .4)
			washer, washerDef := snapshotPart(t, srv, fs, 9)
			setWireFloat(washer.Fields, washer.Class, fieldIdxByCS(washer.Class, csDurability), .4)
			if _, present, idx := frictionWasherDrag(washer); present {
				setWireFloat(washer.Fields, washer.Class, idx, itemDefF32(washerDef, gearAttrDragMax)*fresh.Workmanship*.4)
			}
			raw = snapshotJSON(t, fs)
			if err := db.UpdateItemFields(ctx, fresh.GUID, raw, fresh.Qty, fresh.Durability, fresh.Workmanship, fresh.RigGUID); err != nil {
				t.Fatal(err)
			}
			if err := db.SetItemDurability(ctx, reel.GUID, .6); err != nil {
				t.Fatal(err)
			}
			c := &Ctx{Sess: newProbeSess(srv), Tree: snapshotWorkshopRequest(reel.GUID, "rgm_ef_spark10021"), Log: logging.Discard()}
			gearRepairBody, gearRepaired := hWorkshopOrder(c)
			if !gearRepaired {
				t.Fatal("gear repair failed")
			}
			gearRepairTree, _, gearRepairErr := proto.DecodeValue(gearRepairBody, 0, srv.Table())
			if gearRepairErr != nil {
				t.Fatal(gearRepairErr)
			}
			repairedGear, repairedDef := snapshotPart(t, srv, gearRepairTree.Fields, 52)
			if snapshotFloat(repairedGear.Fields, repairedGear.Class, csDurability) != 1 {
				t.Fatal("gear repair response condition")
			}
			if repairedDef.ItemID != "rgm_ef_spark10021" {
				t.Fatal("gear repair response model")
			}
			// 摩擦片没下单，还是 0.4 那一档，所以根受力也只能停在 0.4 倍额定值上。
			fresh = assertReelSnapshot(t, srv, reel.GUID, "rgm_ef_spark10021",
				itemDefF32(washerDef, gearAttrDragMax)*fresh.Workmanship*.4)
			if math.Abs(fresh.Durability-.6) > 1e-6 {
				t.Fatalf("只换齿轮时本体耐久应保持 0.6，实得 %v", fresh.Durability)
			}
			fs = snapshotFields(t, fresh.FieldsJSON)
			gear, _ = snapshotPart(t, srv, fs, 52)
			if snapshotFloat(gear.Fields, gear.Class, csDurability) != 1 {
				t.Fatal("gear not repaired")
			}
			// 没下单的件必须原样留着：官服按件收费，顺手刷满就是白送。
			bearing, _ = snapshotPart(t, srv, fs, 7)
			if got := snapshotFloat(bearing.Fields, bearing.Class, csDurability); math.Abs(got-.4) > 1e-6 {
				t.Fatalf("没下单的轴承应保持 0.4，实得 %v", got)
			}
			washer, washerDef = snapshotPart(t, srv, fs, 9)
			if got := snapshotFloat(washer.Fields, washer.Class, csDurability); math.Abs(got-.4) > 1e-6 {
				t.Fatalf("没下单的摩擦片应保持 0.4，实得 %v", got)
			}
			if current, present, _ := frictionWasherDrag(washer); !present ||
				math.Abs(current-itemDefF32(washerDef, gearAttrDragMax)*fresh.Workmanship*.4) > 1e-6 {
				t.Fatalf("没下单的摩擦片拖曳应停在 0.4 档，实得 %v", current)
			}
			if err := db.SetItemDurability(ctx, reel.GUID, .6); err != nil {
				t.Fatal(err)
			}
			repair := proto.Args524(proto.StrVal("workshop_01"), wiregen.MustMakeObj("nfmkdcbnkhp", map[int]*proto.Value{0: proto.Guid(reel.GUID), 1: proto.Args524(proto.U32(52)), 2: proto.U32(0), 3: proto.U64(0)}))
			c = &Ctx{Sess: newProbeSess(srv), Tree: repair, Log: logging.Discard()}
			repairBody, repaired := hWorkshopRepair(false)(c)
			if !repaired {
				t.Fatal("reel repair failed")
			}
			repairTree, _, repairErr := proto.DecodeValue(repairBody, 0, srv.Table())
			if repairErr != nil {
				t.Fatal(repairErr)
			}
			if snapshotFloat(repairTree.Fields, repairTree.Class, csDurability) != 1 {
				t.Fatal("reel repair response durability")
			}
			// 进店修理是整机大保养：轮内烧掉、磨旧的件一并恢复，否则本体修回 1.0
			// 也收不了线。所以修完之后根受力回到当前安装摩擦片的额定值。
			repairedRow := mustItem(t, db, reel.GUID)
			ratedDrag, ratedOK := srv.reelWasherRatedFromRow(ctx, repairedRow)
			if !ratedOK || ratedDrag <= 0 {
				t.Fatal("installed washer rated drag after repair")
			}
			if repairedRow.Durability != 1 {
				t.Fatalf("reel durability after repair=%v", repairedRow.Durability)
			}
			assertReelSnapshot(t, srv, reel.GUID, "rgm_ef_spark10021", ratedDrag)
		})
	}
}

func TestReelPartSnapshotStartup(t *testing.T) {
	t.Parallel()
	for _, drag := range []float64{2.25, 0} {
		t.Run(fmt.Sprintf("drag_%g", drag), func(t *testing.T) {
			t.Parallel()
			srv, db := newStaticRigTestServer(t)
			defer db.Close()
			ctx := t.Context()
			reel, err := srv.CreateItem(ctx, 1000001, "spin_5354", "backpack", 1)
			if err != nil {
				t.Fatal(err)
			}
			fields := snapshotFields(t, reel.FieldsJSON)
			gear, def := snapshotPart(t, srv, fields, 52)
			washer, _ := snapshotPart(t, srv, fields, 9)
			setWireFloat(gear.Fields, gear.Class, fieldIdxByCS(gear.Class, csDurability), .4)
			setWireFloat(washer.Fields, washer.Class, fieldIdxByCS(washer.Class, csDragForce), drag)
			setWireFloat(fields, reel.WireClass, fieldIdxByCS(reel.WireClass, csStrength), itemDefF32(def, gearAttrRatio))
			raw := snapshotJSON(t, fields)
			if err := db.UpdateItemFields(ctx, reel.GUID, raw, reel.Qty, reel.Durability, reel.Workmanship, reel.RigGUID); err != nil {
				t.Fatal(err)
			}
			// The legacy zero-washer migration is independently covered; this fixture is a worn current save.
			if err := db.KVSet(ctx, reelFrictionZeroMigrationKey, "done"); err != nil {
				t.Fatal(err)
			}
			if _, err := srv.RepairReelComponents(ctx); err != nil {
				t.Fatal(err)
			}
			assertReelSnapshot(t, srv, reel.GUID, def.ItemID, drag)
			originalGear, err := proto.EncodeValue(gear, srv.Table())
			if err != nil {
				t.Fatal(err)
			}
			var previous string
			for i := 0; i < 2; i++ {
				if i > 0 {
					path := db.Path
					if err := db.Close(); err != nil {
						t.Fatal(err)
					}
					db = openStaticDBAtPath(t, path)
					srv = NewServer(srv.cfg, logging.Discard(), nil)
					srv.SetDB(db)
				}
				if err := srv.RepairItemPhysics(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err := srv.RepairReelComponents(ctx); err != nil {
					t.Fatal(err)
				}
				fresh := assertReelSnapshot(t, srv, reel.GUID, def.ItemID, drag)
				if i > 0 && fresh.FieldsJSON != previous {
					t.Fatal("startup not idempotent")
				}
				saved, err := FieldsFromJSON(fresh.FieldsJSON)
				if err != nil {
					t.Fatal(err)
				}
				savedGear, _, _, ok := srv.selectedReelPart(ctx, saved, 52)
				if !ok {
					t.Fatal("gear missing after startup")
				}
				afterGear, err := proto.EncodeValue(savedGear, srv.Table())
				if err != nil {
					t.Fatal(err)
				}
				if string(afterGear) != string(originalGear) {
					t.Fatal("startup changed installed gear identity or condition")
				}
				previous = fresh.FieldsJSON
			}
		})
	}
}

func TestReelPartSnapshotBatchAndUnrelatedParts(t *testing.T) {
	t.Parallel()
	for _, drag := range []float64{2.25, 0} {
		t.Run(fmt.Sprintf("drag_%g", drag), func(t *testing.T) {
			t.Parallel()
			srv, db := newStaticRigTestServer(t)
			defer db.Close()
			// 与 TestReelPartSnapshotWorkshop 同理：这里比对的是多件同批换装后
			// 库内零件树，必须当场落库，所以显式走即时路径。
			setWorkshopSilverRepairDelay(t, srv, db, false)
			ctx := t.Context()
			const uid = int64(1000001)
			if err := srv.EnsurePlayer(ctx, uid); err != nil {
				t.Fatal(err)
			}
			profile, err := db.EnsureProfile(ctx, uid)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.UpdateBalance(ctx, uid, 1000000, profile.Gold, profile.Bank); err != nil {
				t.Fatal(err)
			}
			reel, err := srv.CreateItem(ctx, uid, "spin_5354", "backpack", 1)
			if err != nil {
				t.Fatal(err)
			}
			fields := snapshotFields(t, reel.FieldsJSON)
			washer, _ := snapshotPart(t, srv, fields, 9)
			setWireFloat(washer.Fields, washer.Class, fieldIdxByCS(washer.Class, csDragForce), drag)
			protected := map[int64][]byte{}
			for _, part := range []struct {
				category                int64
				workmanship, durability float64
			}{{7, .7, 0}, {10, .8, .5}} {
				child, _ := snapshotPart(t, srv, fields, part.category)
				setWireFloat(child.Fields, child.Class, fieldIdxByCS(child.Class, csWorkmanship), part.workmanship)
				setWireFloat(child.Fields, child.Class, fieldIdxByCS(child.Class, csDurability), part.durability)
				raw, err := proto.EncodeValue(child, srv.Table())
				if err != nil {
					t.Fatal(err)
				}
				protected[part.category] = raw
			}
			save := func() {
				t.Helper()
				raw := snapshotJSON(t, fields)
				if err := db.UpdateItemFields(ctx, reel.GUID, raw, reel.Qty, reel.Durability, reel.Workmanship, reel.RigGUID); err != nil {
					t.Fatal(err)
				}
			}
			checkProtected := func() {
				t.Helper()
				stored := snapshotFields(t, mustItem(t, db, reel.GUID).FieldsJSON)
				for category, before := range protected {
					child, _ := snapshotPart(t, srv, stored, category)
					after, err := proto.EncodeValue(child, srv.Table())
					if err != nil {
						t.Fatal(err)
					}
					if string(after) != string(before) {
						t.Fatalf("unrelated category %d changed", category)
					}
				}
			}
			// A legacy root is corrected without changing unrelated installed parts.
			setWireFloat(fields, reel.WireClass, fieldIdxByCS(reel.WireClass, csStrength), 123)
			save()
			for i := 0; i < 2; i++ {
				if _, err := srv.RepairReelComponents(ctx); err != nil {
					t.Fatal(err)
				}
				checkProtected()
			}
			_, stock := snapshotPart(t, srv, fields, 52)
			assertReelSnapshot(t, srv, reel.GUID, stock.ItemID, drag)
			session, peer := newTunnelProbe(t, srv)
			// A gear-only order must preserve both worn and zero-valued washers and other parts.
			request := snapshotWorkshopRequest(reel.GUID, "rgm_zmn_jet_jsg-r")
			c := &Ctx{Sess: session, Tree: request, Log: logging.Discard()}
			if _, ok := hWorkshopOrder(c); !ok {
				t.Fatal("gear-only order failed")
			}
			// 这一单也扣了钱，同样会先推一次 2/1；读掉它，后面的批量单才是干净的序列。
			expectProfilePush(t, awaitPushEnvelope(t, peer))
			checkProtected()
			assertReelSnapshot(t, srv, reel.GUID, "rgm_zmn_jet_jsg-r", drag)
			// The protocol accepts multiple part orders in the same request.
			orders := proto.Args(proto.S("503"),
				wiregen.MustMakeObj("gcllbdkgkna", map[int]*proto.Value{0: proto.U32(9), 1: proto.StrVal("ef-fb-sp.orig-5805")}),
				wiregen.MustMakeObj("gcllbdkgkna", map[int]*proto.Value{0: proto.U32(52), 1: proto.StrVal("rgm_ef_spark10021")}),
			)
			request = proto.Args524(proto.StrVal("workshop_01"), wiregen.MustMakeObj("nfmkdcbnkhp", map[int]*proto.Value{0: proto.Guid(reel.GUID), 1: orders, 2: proto.U32(0), 3: proto.U64(0)}))
			requestBytes, err := proto.EncodeValue(request, srv.Table())
			if err != nil {
				t.Fatal(err)
			}
			decoded, end, err := proto.DecodeValue(requestBytes, 0, srv.Table())
			if err != nil || end != len(requestBytes) {
				t.Fatalf("request decode: %v", err)
			}
			c = &Ctx{Sess: session, Main: 21, Sub: 3, Tree: decoded, DecodeOK: true, ExpectResponse: true, Log: logging.Discard()}
			body, ok := hWorkshopOrder(c)
			if !ok || len(body) == 0 {
				t.Fatal("batch replacement failed")
			}
			checkProtected()
			// A newly purchased washer has its definition force; this is a replacement, not revival of the old washer.
			row := assertReelSnapshot(t, srv, reel.GUID, "rgm_ef_spark10021", 3.5)
			stored := snapshotFields(t, row.FieldsJSON)
			_, newWasher := snapshotPart(t, srv, stored, 9)
			if newWasher.ItemID != "ef-fb-sp.orig-5805" {
				t.Fatal("washer was not replaced")
			}
			reply, end, err := proto.DecodeValue(body, 0, srv.Table())
			if err != nil || end != len(body) {
				t.Fatalf("response decode: %v", err)
			}
			// 新口径：响应里的轮根=规格×轴承耐久（与摩擦片值解耦）。
			// 本夹具轴承耐久被置 0（受保护状态），轮根按公式应为 0。
			if got := snapshotFloat(reply.Fields, reply.Class, csStrength); got != 0 {
				t.Fatalf("response root=%g, want 0（夹具轴承耐久=0）", got)
			}
			_, replyGear := snapshotPart(t, srv, reply.Fields, 52)
			if replyGear.ItemID != "rgm_ef_spark10021" {
				t.Fatal("response uses old gear")
			}
			if len(c.afterResponse) != 1 {
				t.Fatal("batch refresh missing")
			}
			c.afterResponse[0]()
			// 批量单自己的 2/1，之后才是 4/22。
			expectProfilePush(t, awaitPushEnvelope(t, peer))
			push := awaitPushEnvelope(t, peer)
			if push.Main != 4 || push.Sub != 22 {
				t.Fatalf("wrong refresh command: %d/%d", push.Main, push.Sub)
			}
			value, _, err := proto.DecodeValue(push.Body, 0, srv.Table())
			if err != nil {
				t.Fatal(err)
			}
			assertRefreshMatchesReply(t, srv, value.ArgItem(0), reply, body,
				"batch refresh differs from response")
			// Even after building and sending a projected tree, the persistent child state remains untouched.
			checkProtected()
		})
	}
}

func TestReelZeroDragRootPaths(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"tree", "repair", "workshop"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			srv, db := newStaticRigTestServer(t)
			defer db.Close()
			// workshop 分支要比对换件后库内根字段，必须当场落库 → 即时路径。
			setWorkshopSilverRepairDelay(t, srv, db, false)
			ctx := t.Context()
			const uid = int64(1000001)
			if err := srv.EnsurePlayer(ctx, uid); err != nil {
				t.Fatal(err)
			}
			profile, err := db.EnsureProfile(ctx, uid)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.UpdateBalance(ctx, uid, 1000000, profile.Gold, profile.Bank); err != nil {
				t.Fatal(err)
			}
			reel, err := srv.CreateItem(ctx, uid, "spin_5354", "backpack", 1)
			if err != nil {
				t.Fatal(err)
			}
			fields := snapshotFields(t, reel.FieldsJSON)
			washer, _ := snapshotPart(t, srv, fields, 9)
			setWireFloat(washer.Fields, washer.Class, fieldIdxByCS(washer.Class, csDragForce), 0)
			raw := snapshotJSON(t, fields)
			if err := db.UpdateItemFields(ctx, reel.GUID, raw, reel.Qty, reel.Durability, reel.Workmanship, reel.RigGUID); err != nil {
				t.Fatal(err)
			}
			switch path {
			case "repair":
				if _, err := srv.RepairReelComponents(ctx); err != nil {
					t.Fatal(err)
				}
			case "workshop":
				c := &Ctx{Sess: newProbeSess(srv), Tree: snapshotWorkshopRequest(reel.GUID, "rgm_zmn_jet_jsg-r"), Log: logging.Discard()}
				if _, ok := hWorkshopOrder(c); !ok {
					t.Fatal("workshop failed")
				}
			}
			row := mustItem(t, db, reel.GUID)
			// 新口径：轮根=出厂规格×轴承耐久，摩擦片磨穿只写摩擦片字段，轮根保持规格。
			wantRoot, ok := reelRootSpec("spin_5354")
			if !ok {
				t.Fatal("规格表缺 spin_5354")
			}
			if path != "tree" {
				stored := snapshotFields(t, row.FieldsJSON)
				if got := snapshotFloat(stored, row.WireClass, csStrength); math.Abs(got-wantRoot) > 1e-5 {
					t.Fatalf("stored zero-drag root=%g, want %g", got, wantRoot)
				}
			}
			tree, err := srv.ItemToTree(ctx, row)
			if err != nil {
				t.Fatal(err)
			}
			if got := snapshotFloat(tree.Fields, tree.Class, csStrength); math.Abs(got-wantRoot) > 1e-5 {
				t.Fatalf("wire zero-drag root=%g, want %g", got, wantRoot)
			}
			child, _ := snapshotPart(t, srv, tree.Fields, 9)
			value, present, _ := frictionWasherDrag(child)
			if !present || value != 0 {
				t.Fatal("washer zero lost")
			}
		})
	}
}

func TestReelDragFieldPresenceAndFallback(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		value   float64
		present bool
		want    float64
	}{
		{"positive", 2.25, true, 2.25}, {"zero", 0, true, 0}, {"missing", 0, false, 3.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv, db := newStaticRigTestServer(t)
			defer db.Close()
			ctx := t.Context()
			row, err := srv.CreateItem(ctx, 1000001, "spin_5354", "backpack", 1)
			if err != nil {
				t.Fatal(err)
			}
			fields := snapshotFields(t, row.FieldsJSON)
			_, _, slot, found := srv.selectedReelPart(ctx, fields, 9)
			if !found {
				t.Fatal("washer missing")
			}
			washer, err := srv.buildPartInstance(ctx, row, "ef-fb-sp.orig-5805", "test-washer")
			if err != nil || washer == nil {
				t.Fatalf("washer: %v", err)
			}
			idx := fieldIdxByCS(washer.Class, csDragForce)
			if idx < 0 || idx >= len(washer.Fields) {
				t.Fatal("force field missing")
			}
			if tc.present {
				setWireFloat(washer.Fields, washer.Class, idx, tc.value)
			} else {
				washer.Fields[idx].Num = nil
			}
			fields[slot].Val = washer
			setWireFloat(fields, row.WireClass, fieldIdxByCS(row.WireClass, csStrength), 123)
			row.FieldsJSON = snapshotJSON(t, fields)
			got, present := srv.reelInstalledDragFieldFromRow(ctx, row)
			if present != tc.present || (present && got != tc.value) {
				t.Fatalf("stored source=%g,%v", got, present)
			}
			// The existing reader keeps its original behavior for other callers.
			if value := srv.reelBaseDragFromRow(ctx, row); math.Abs(value-tc.want) > 1e-5 {
				t.Fatalf("base reader=%g want %g", value, tc.want)
			}
			childBefore := snapshotJSON(t, washer.Fields)
			srv.canonicalizeReelStrength(ctx, row, fields)
			// spin_5354 已入规格表：canon 后轮根=规格×轴承耐久（与 washer 状态无关）；
			// washer 存在性/回退语义由 readers（上方）与 old_spinreel 回退用例覆盖。
			wantRoot, ok := reelRootSpec("spin_5354")
			if !ok {
				t.Fatal("规格表缺 spin_5354")
			}
			if value := snapshotFloat(fields, row.WireClass, csStrength); math.Abs(value-wantRoot) > 1e-5 {
				t.Fatalf("root=%g want %g", value, wantRoot)
			}
			if snapshotJSON(t, washer.Fields) != childBefore {
				t.Fatal("root normalization changed washer")
			}
			raw := snapshotJSON(t, fields)
			if err := db.UpdateItemFields(ctx, row.GUID, raw, row.Qty, row.Durability, row.Workmanship, row.RigGUID); err != nil {
				t.Fatal(err)
			}
			fresh := mustItem(t, db, row.GUID)
			stored := snapshotFields(t, fresh.FieldsJSON)
			if value := snapshotFloat(stored, fresh.WireClass, csStrength); math.Abs(value-wantRoot) > 1e-5 {
				t.Fatal("stored root mismatch")
			}
			tree, err := srv.ItemToTree(ctx, fresh)
			if err != nil {
				t.Fatal(err)
			}
			if value := snapshotFloat(tree.Fields, tree.Class, csStrength); math.Abs(value-wantRoot) > 1e-5 {
				t.Fatal("wire root mismatch")
			}
		})
	}
}

func TestReelDegradePersistsRoot(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"degrade", "slip"} {
		for _, wearOut := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/worn_%t", path, wearOut), func(t *testing.T) {
				t.Parallel()
				srv, db := newStaticRigTestServer(t)
				defer db.Close()
				ctx := t.Context()
				const uid = int64(1000001)
				reel, err := srv.CreateItem(ctx, uid, "spin_5354", "backpack", 1)
				if err != nil {
					t.Fatal(err)
				}
				specRoot, ok := reelRootSpec("spin_5354")
				if !ok {
					t.Fatal("规格表缺 spin_5354")
				}
				initial := snapshotFields(t, reel.FieldsJSON)
				if snapshotFloat(initial, reel.WireClass, csStrength) <= 0 {
					t.Fatal("new reel root must be positive")
				}
				washer, def := snapshotPart(t, srv, initial, 9)
				step := itemDefF32(def, gearAttrDragMax) * reel.Workmanship * reelSlipWear
				if step <= 0 {
					t.Fatal("invalid wear fixture")
				}
				current := step * 3
				if wearOut {
					current = step / 2
				}
				setWireFloat(washer.Fields, washer.Class, fieldIdxByCS(washer.Class, csDragForce), current)
				reel.FieldsJSON = snapshotJSON(t, initial)
				srv.canonicalizeReelStrength(ctx, reel, initial)
				reel.FieldsJSON = snapshotJSON(t, initial)
				if err := db.UpdateItemFields(ctx, reel.GUID, reel.FieldsJSON, reel.Qty, reel.Durability, reel.Workmanship, reel.RigGUID); err != nil {
					t.Fatal(err)
				}
				washerBefore := snapshotJSON(t, washer.Fields)
				// Call the actual wear persistence path; do not run a repair or workshop operation.
				if path == "degrade" {
					worn, found, err := srv.degradeFrictionWasher(ctx, reel)
					if err != nil || !found || worn != wearOut {
						t.Fatalf("wear result %t/%t: %v", worn, found, err)
					}
				} else {
					out, err := srv.damageReelSlip(ctx, uid, zeroGUID, reel.GUID)
					if err != nil || out == nil || out.FrictionWorn != wearOut {
						t.Fatalf("slip result: %+v %v", out, err)
					}
				}
				fresh := mustItem(t, db, reel.GUID)
				fields := snapshotFields(t, fresh.FieldsJSON)
				afterWasher, _ := snapshotPart(t, srv, fields, 9)
				unchanged := append([]proto.Field(nil), afterWasher.Fields...)
				setWireFloat(unchanged, afterWasher.Class, fieldIdxByCS(afterWasher.Class, csDragForce), current)
				// 磨损还会把摩擦片耐久写成制动力剩余比例的镜像（背包与工坊靠它看损耗%），
				// 比对时一并还原，确认其余身份字段没被动过。
				setWireFloat(unchanged, afterWasher.Class, fieldIdxByCS(afterWasher.Class, csDurability),
					snapshotFloat(washer.Fields, washer.Class, csDurability))
				if snapshotJSON(t, unchanged) != washerBefore {
					t.Fatal("wear changed washer identity, condition or other fields")
				}
				force, present, _ := frictionWasherDrag(afterWasher)
				if !present || math.IsNaN(force) || math.IsInf(force, 0) {
					t.Fatal("stored washer force missing or invalid")
				}
				if wearOut && force != 0 {
					t.Fatalf("washer did not wear out: %g", force)
				}
				if !wearOut && (force <= 0 || force >= current) {
					t.Fatalf("intermediate wear missing: %g", force)
				}
				// 新口径：轮根=规格×轴承耐久，恒为规格（轴承未动），不跟随摩擦片。
				if root := snapshotFloat(fields, fresh.WireClass, csStrength); math.Abs(root-specRoot) > 1e-5 {
					t.Errorf("immediate stored root=%g washer=%g want=%g", root, force, specRoot)
				}
				tree, err := srv.ItemToTree(ctx, fresh)
				if err != nil {
					t.Fatal(err)
				}
				if root := snapshotFloat(tree.Fields, tree.Class, csStrength); math.Abs(root-specRoot) > 1e-5 {
					t.Errorf("wire root=%g washer=%g want=%g", root, force, specRoot)
				}
				encoded, err := proto.EncodeValue(tree, srv.Table())
				if err != nil {
					t.Fatal(err)
				}
				decoded, end, err := proto.DecodeValue(encoded, 0, srv.Table())
				if err != nil || end != len(encoded) {
					t.Fatalf("worn reel codec: %v", err)
				}
				if math.Abs(snapshotFloat(decoded.Fields, decoded.Class, csStrength)-specRoot) > 1e-5 {
					t.Fatal("encoded worn root mismatch")
				}
				// 本体耐久是轮内零件的综合：磨薄摩擦片它就得降（只降不升），
				// 烧穿时熔断到 0，客户端才会红框、才修不好就收不了线。
				if fresh.Durability > reel.Durability {
					t.Fatalf("wear raised reel durability: %v -> %v", reel.Durability, fresh.Durability)
				}
				if path == "slip" {
					if wearOut != (fresh.Durability == 0) {
						t.Fatalf("reel durability must follow washer wear: worn=%t durability=%v", wearOut, fresh.Durability)
					}
				} else if fresh.Durability != reel.Durability {
					// 只磨零件字段的入口不负责本体：聚合发生在结算与伤害入口。
					t.Fatalf("bare washer degradation must not touch reel durability: %v -> %v", reel.Durability, fresh.Durability)
				}
				perf, err := srv.syncReelPerformance(ctx, fresh)
				if err != nil {
					t.Fatal(err)
				}
				if math.Abs(perf.BaseDragKg-force) > 1e-5 {
					t.Fatal("performance mismatch")
				}
				storedAgain := snapshotFields(t, mustItem(t, db, reel.GUID).FieldsJSON)
				if root := snapshotFloat(storedAgain, fresh.WireClass, csStrength); math.Abs(root-specRoot) > 1e-5 {
					t.Errorf("after performance sync root=%g washer=%g want=%g", root, force, specRoot)
				}
			})
		}
	}
}
