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

func snapshotWorkshopRequestPartPayment(guid string, category uint32, systemID string, clientCost uint64, useGold bool) *proto.Value {
	goldFlag := uint32(0)
	if useGold {
		goldFlag = 1
	}
	return proto.Args524(proto.StrVal("workshop_01"), wiregen.MustMakeObj("nfmkdcbnkhp", map[int]*proto.Value{
		0: proto.Guid(guid),
		1: proto.Args(proto.S("503"), wiregen.MustMakeObj("gcllbdkgkna", map[int]*proto.Value{
			0: proto.U32(category),
			1: proto.StrVal(systemID),
		})),
		2: proto.U32(goldFlag),
		3: proto.U64(clientCost),
	}))
}

func snapshotWorkshopRequestPart(guid string, category uint32, systemID string, clientCost uint64) *proto.Value {
	return snapshotWorkshopRequestPartPayment(guid, category, systemID, clientCost, false)
}

// workshopEntry 是 21/4 请求里的一条换件/修理工单。
type workshopEntry struct {
	Cat   uint32
	SysID string
}

// snapshotWorkshopRequestParts 造一张多条目工单。
//
// 客户端多选部位时只发**一帧** 21/4，条目全在 args(503) 里（实测 23:07:55
// n=3 cost=2176）。单条目的 wrapper 见 snapshotWorkshopRequestPart。
func snapshotWorkshopRequestParts(guid string, entries []workshopEntry, clientCost uint64, useGold bool) *proto.Value {
	goldFlag := uint32(0)
	if useGold {
		goldFlag = 1
	}
	items := make([]*proto.Value, 0, len(entries))
	for _, e := range entries {
		items = append(items, wiregen.MustMakeObj("gcllbdkgkna", map[int]*proto.Value{
			0: proto.U32(e.Cat),
			1: proto.StrVal(e.SysID),
		}))
	}
	return proto.Args524(proto.StrVal("workshop_01"), wiregen.MustMakeObj("nfmkdcbnkhp", map[int]*proto.Value{
		0: proto.Guid(guid),
		1: proto.Args(proto.S("503"), items...),
		2: proto.U32(goldFlag),
		3: proto.U64(clientCost),
	}))
}

// seedWornWorkshopRodForCats 造一根竿，把指定品类的子件耐久都压到 partLife。
//
// 返回的条目用**装着的那只**型号，所以下单走"同型号修理"分支而不是换件。
func seedWornWorkshopRodForCats(t *testing.T, srv *Server, db *store.DB,
	cats []int64, partLife, bodyLife float64) (*store.Item, []workshopEntry) {
	t.Helper()
	ctx := t.Context()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	fields := snapshotFields(t, rod.FieldsJSON)
	entries := make([]workshopEntry, 0, len(cats))
	for _, cat := range cats {
		part, def := snapshotPart(t, srv, fields, cat)
		setWireFloat(part.Fields, part.Class, fieldIdxByCS(part.Class, csDurability), partLife)
		entries = append(entries, workshopEntry{Cat: uint32(cat), SysID: def.ItemID})
	}
	raw := snapshotJSON(t, fields)
	rod.FieldsJSON = raw
	if err := db.UpdateItemFields(ctx, rod.GUID, raw, rod.Qty, bodyLife, rod.Workmanship, rod.RigGUID); err != nil {
		t.Fatalf("预置竿状态失败: %v", err)
	}
	rod.Durability = bodyLife
	return rod, entries
}

func seedWornWorkshopRod(t *testing.T, srv *Server, db *store.DB, partLife, bodyLife float64) (*store.Item, string) {
	t.Helper()
	ctx := t.Context()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	fields := snapshotFields(t, rod.FieldsJSON)
	part, partDef := snapshotPart(t, srv, fields, 4)
	if part == nil || partDef == nil {
		t.Fatal("竿柄部件缺失")
	}
	setWireFloat(part.Fields, part.Class, fieldIdxByCS(part.Class, csDurability), partLife)
	raw := snapshotJSON(t, fields)
	rod.FieldsJSON = raw
	if err := db.UpdateItemFields(ctx, rod.GUID, raw, rod.Qty, bodyLife, rod.Workmanship, rod.RigGUID); err != nil {
		t.Fatalf("预置竿状态失败: %v", err)
	}
	rod.Durability = bodyLife
	return rod, partDef.ItemID
}

func seedWornWorkshopReel(t *testing.T, srv *Server, db *store.DB, gearLife, washerLife, bearingLife, bodyLife float64) (*store.Item, string) {
	t.Helper()
	ctx := t.Context()
	const uid = int64(1000001)

	reel, err := srv.CreateItem(ctx, uid, "spin_5354", "backpack", 1)
	if err != nil {
		t.Fatalf("造轮失败: %v", err)
	}
	fields := snapshotFields(t, reel.FieldsJSON)
	gear, gearDef := snapshotPart(t, srv, fields, reelPartCatGear)
	washer, washerDef := snapshotPart(t, srv, fields, reelPartCatWasher)
	bearing, _ := snapshotPart(t, srv, fields, reelPartCatBearing)
	setWireFloat(gear.Fields, gear.Class, fieldIdxByCS(gear.Class, csDurability), gearLife)
	setWireFloat(bearing.Fields, bearing.Class, fieldIdxByCS(bearing.Class, csDurability), bearingLife)
	setWireFloat(washer.Fields, washer.Class, fieldIdxByCS(washer.Class, csDurability), washerLife)
	if _, present, idx := frictionWasherDrag(washer); present {
		setWireFloat(washer.Fields, washer.Class, idx,
			itemDefF32(washerDef, gearAttrDragMax)*reel.Workmanship*washerLife)
	}
	raw := snapshotJSON(t, fields)
	reel.FieldsJSON = raw
	if err := db.UpdateItemFields(ctx, reel.GUID, raw, reel.Qty, bodyLife, reel.Workmanship, reel.RigGUID); err != nil {
		t.Fatalf("预置轮状态失败: %v", err)
	}
	reel.Durability = bodyLife
	return reel, gearDef.ItemID
}

func TestWorkshopSameModelRepairRestoresItemDurability(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	rod, partID := seedWornWorkshopRod(t, srv, db, .4, .5)

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(rod.GUID, 4, partID, 0),
		Log:  logging.Discard(),
	}
	body, ok := hWorkshopOrder(c)
	if !ok || len(body) == 0 {
		t.Fatal("同型号竿柄修理失败")
	}
	// 银币修理要等：入队当场不改 item 行，到期结算才把快照落地
	// （口径与触发点见 workshop_repair_queue.go）。
	expireWorkshopJobs(t, db, 1000001)
	if settled := settleWorkshopForTest(t, srv, 1000001); len(settled) != 1 {
		t.Fatalf("到期应结算 1 件，实际 %v", settled)
	}
	fresh := mustItem(t, db, rod.GUID)
	if fresh.Durability != 1 {
		t.Fatalf("同型号修理后物品本体耐久应回 1，实得 %v", fresh.Durability)
	}
	freshFields := snapshotFields(t, fresh.FieldsJSON)
	repairedPart, _ := snapshotPart(t, srv, freshFields, 4)
	if got := snapshotFloat(repairedPart.Fields, repairedPart.Class, csDurability); got != 1 {
		t.Fatalf("同型号修理后竿柄耐久应回 1，实得 %v", got)
	}
}

func TestWorkshopOrderRejectsLowClientQuote(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	rod, partID := seedWornWorkshopRod(t, srv, db, .8, .5)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 1000, 0, 0); err != nil {
		t.Fatal(err)
	}

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(rod.GUID, 4, partID, 1),
		Log:  logging.Discard(),
	}
	if _, ok := hWorkshopOrder(c); ok {
		t.Fatal("1 银币伪造报价应被拒绝")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Silver != 1000 {
		t.Fatalf("报价被拒绝后余额应保持 1000，实得 %d", profile.Silver)
	}
	fresh := mustItem(t, db, rod.GUID)
	if fresh.Durability != .5 {
		t.Fatalf("报价被拒绝后本体耐久应保持 0.5，实得 %v", fresh.Durability)
	}
	fields := snapshotFields(t, fresh.FieldsJSON)
	part, _ := snapshotPart(t, srv, fields, 4)
	if got := snapshotFloat(part.Fields, part.Class, csDurability); math.Abs(got-.8) > 1e-6 {
		t.Fatalf("报价被拒绝后竿柄耐久应保持 0.8，实得 %v", got)
	}
}

func TestWorkshopOrderRollsBackWalletWhenItemWriteFails(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	// 这条用例守的是"扣费与 item 写库同事务"：只有即时路径才写 item 行
	// （银币排队路径写的是任务表，失败回滚由
	// TestWorkshopSilverRepairRollsBackWalletWhenJobWriteFails 覆盖）。
	setWorkshopSilverRepairDelay(t, srv, db, false)
	rod, partID := seedWornWorkshopRod(t, srv, db, .8, .5)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 1000, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.ExecContext(ctx, fmt.Sprintf(
		`CREATE TRIGGER fail_workshop_item BEFORE UPDATE OF fields_json ON item
		 FOR EACH ROW WHEN OLD.guid='%s'
		 BEGIN SELECT RAISE(ABORT,'注入工坊物品写库失败'); END`, rod.GUID)); err != nil {
		t.Fatalf("注入失败: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.SQL.Exec(`DROP TRIGGER IF EXISTS fail_workshop_item`)
	})
	if _, err := db.SQL.ExecContext(ctx,
		`UPDATE item SET fields_json=fields_json WHERE guid=?`, rod.GUID); err == nil {
		t.Fatal("注入未生效：物品字段写库没有失败")
	}

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(rod.GUID, 4, partID, 0),
		Log:  logging.Discard(),
	}
	if _, ok := hWorkshopOrder(c); ok {
		t.Fatal("物品写库失败时工坊订单不应成功")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Silver != 1000 {
		t.Fatalf("物品写库失败后余额必须回滚为 1000，实得 %d", profile.Silver)
	}
	var txnCount int
	if err := db.SQL.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM txn WHERE uid=? AND kind='workshop_part'`, uid).Scan(&txnCount); err != nil {
		t.Fatal(err)
	}
	if txnCount != 0 {
		t.Fatalf("物品写库失败后流水必须回滚，实得 %d 条", txnCount)
	}
	fresh := mustItem(t, db, rod.GUID)
	if fresh.Durability != .5 {
		t.Fatalf("物品写库失败后本体耐久应保持 0.5，实得 %v", fresh.Durability)
	}
}

func TestWorkshopReelRepairRejectsGoldPayment(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	reel, gearID := seedWornWorkshopReel(t, srv, db, 1, .2, .5, .5)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 1000, 1000, 0); err != nil {
		t.Fatal(err)
	}

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPartPayment(reel.GUID, uint32(reelPartCatGear), gearID, 0, true),
		Log:  logging.Discard(),
	}
	if _, ok := hWorkshopOrder(c); ok {
		t.Fatal("同型号修理不应接受金币支付")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Silver != 1000 || profile.Gold != 1000 {
		t.Fatalf("金币修理被拒绝后余额应保持 1000/1000，实得 %d/%d",
			profile.Silver, profile.Gold)
	}
	fresh := mustItem(t, db, reel.GUID)
	if fresh.Durability != .5 {
		t.Fatalf("金币修理被拒绝后本体耐久应保持 0.5，实得 %v", fresh.Durability)
	}
}

// TestWorkshopReelRepairOnlyTouchesOrderedPart 锁定"按件收费就按件施工"。
//
// 两条一起成立才算对：
//
//  1. **满耐久的件照样收钱**。官服的"修理"是换同型号新件，收 (新件价 − 旧件残值)，
//     而残值是品类属性、与磨损无关 —— nc.jsonl 那单四件完好度都在 0.9999 附近，
//     残值率却分别是 0.383 / 0.383 / 0.383 / 0.9。所以"几乎全新的件修理免费"
//     这个直觉是错的，旧口径的 round(磨损×500) 才会折出 0 银。
//     （注意：整件全满的极端情况仍走"幂等成功不扣费"分支，那是为了挡住即时
//     路径上的重放重复扣费，见 hWorkshopOrder 的 didWork 判定。本条用例里本体
//     还有 0.5 的磨损，所以会正常计费。）
//  2. **没下单的件一个都不许动**。官服 21/4 只处理 args(503) 里列出的件：那一单
//     只列了齿轮，回包的 21/2 里轴承/摩擦片的 GUID 原样不变，只有齿轮换成了新
//     GUID（6414fec0… → b411a83e…）。旧口径在这里把本体耐久直接写成 1.0，等于
//     玩家付一件的钱、拿整机保养。
func TestWorkshopReelRepairOnlyTouchesOrderedPart(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	// gear 满耐久、摩擦片 0.2、轴承 0.5、本体 0.5：只勾齿轮，其余必须原样。
	reel, gearID := seedWornWorkshopReel(t, srv, db, 1, .2, .5, .5)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(reel.GUID, uint32(reelPartCatGear), gearID, 0),
		Log:  logging.Discard(),
	}
	if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
		t.Fatal("满耐久齿轮也应能下单（官服修理 = 换新件，与磨损无关）")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	want := workshopPageSilver(t, srv, reel, reelPartCatGear)
	if want <= 0 {
		t.Fatalf("齿轮页面价应为正数，实得 %d", want)
	}
	if got := int64(10000) - profile.Silver; got != want {
		t.Fatalf("实扣 %d，页面报价 %d", got, want)
	}

	// 银币修理要等：到期结算后才落库。
	expireWorkshopJobs(t, db, uid)
	if settled := settleWorkshopForTest(t, srv, uid); len(settled) != 1 {
		t.Fatalf("到期应结算 1 件，实际 %v", settled)
	}
	fresh := mustItem(t, db, reel.GUID)
	after := snapshotFields(t, fresh.FieldsJSON)
	gear, _ := snapshotPart(t, srv, after, reelPartCatGear)
	if got := snapshotFloat(gear.Fields, gear.Class, csDurability); math.Abs(got-1) > 1e-6 {
		t.Fatalf("付了钱的齿轮应回到满耐久，实得 %v", got)
	}
	for _, tc := range []struct {
		cat  int64
		want float64
		what string
	}{
		{reelPartCatWasher, .2, "摩擦片"},
		{reelPartCatBearing, .5, "轴承"},
	} {
		part, _ := snapshotPart(t, srv, after, tc.cat)
		if got := snapshotFloat(part.Fields, part.Class, csDurability); math.Abs(got-tc.want) > 1e-6 {
			t.Fatalf("没下单的%s应保持 %v，实得 %v", tc.what, tc.want, got)
		}
	}
	if math.Abs(fresh.Durability-.5) > 1e-6 {
		t.Fatalf("没下单本体时本体耐久应保持 0.5，实得 %v", fresh.Durability)
	}
}

// TestWorkshopReelRepairRejectsZeroCost 锁定"零金额修理"的收口方式。
//
// 原始意图（2026-11-09）是"没有实际磨损时不许白扣费"。2026-11-11 15:26 实机发现
// 客户端会连发两次相同的 21/3（同 GUID、同品类、同件，间隔 17ms）：第一次把件修满，
// 第二次算出来的磨损是 0。老实现把"价格为 0"当异常拒绝 → 落兜底链回 7 字节空 args
// → 客户端弹「发生内部错误」。所以"拒绝"这个收口方式是错的：
//
//	零金额必须**幂等成功**（回当前物品树），但绝不扣费、绝不写流水。
//
// 这两条同时成立才是对的：客户端拿到有效响应不再报错，玩家也没有被白扣钱。
func TestWorkshopReelRepairRejectsZeroCost(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	reel, gearID := seedWornWorkshopReel(t, srv, db, 1, 1, 1, 1)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 1000, 0, 0); err != nil {
		t.Fatal(err)
	}

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(reel.GUID, uint32(reelPartCatGear), gearID, 0),
		Log:  logging.Discard(),
	}
	body, ok := hWorkshopOrder(c)
	if !ok || len(body) == 0 {
		t.Fatal("零件已满的重放请求必须幂等成功回物品树，不能落兜底链（会让客户端弹内部错误）")
	}
	// 幂等成功不等于免费扣费：不能有任何流水，余额也不能动。
	var txnCount int
	if err := db.SQL.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM txn WHERE uid=? AND kind='workshop_part'`, uid).Scan(&txnCount); err != nil {
		t.Fatal(err)
	}
	if txnCount != 0 {
		t.Fatalf("零金额修理不得写流水，实得 %d 条", txnCount)
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Silver != 1000 {
		t.Fatalf("零金额修理不得扣费，余额应保持 1000，实得 %d", profile.Silver)
	}
	fresh := mustItem(t, db, reel.GUID)
	if fresh.Durability != 1 {
		t.Fatalf("幂等返回不应改动本体耐久，实得 %v", fresh.Durability)
	}
}

// TestWorkshopPartPriceIgnoresWear 锁定"残值是品类属性、与磨损无关"。
//
// 这是官服化之后最反直觉的一条，也是旧口径错得最狠的地方：旧实现按
// round(磨损×500) 收费，几乎全新的件折出来是 0 银，而客户端报的是
// (新件价 − 旧件残值)。残值率只跟品类有关：
//
//	齿轮 266/296 = 0.9、轴承 324/845 = 0.383、摩擦片 289/754 = 0.383
//
// 而 nc.jsonl 那单四件的完好度全在 0.9999 附近 —— 磨损根本没进公式。
//
// 所以同一件东西，0.2 磨损和满耐久下单，服务端必须收同一个数，也必须和
// 客户端界面上那个数一模一样。
func TestWorkshopPartPriceIgnoresWear(t *testing.T) {
	charge := func(gearLife float64) int64 {
		t.Helper()
		srv, db := newStaticRigTestServer(t)
		defer db.Close()
		reel, gearID := seedWornWorkshopReel(t, srv, db, gearLife, .5, .5, 1)
		ctx := t.Context()
		const uid = int64(1000001)
		if _, err := db.EnsureProfile(ctx, uid); err != nil {
			t.Fatal(err)
		}
		if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
			t.Fatal(err)
		}
		c := &Ctx{
			Sess: newProbeSess(srv), Main: 21, Sub: 3,
			Tree: snapshotWorkshopRequestPart(reel.GUID, uint32(reelPartCatGear), gearID, 0),
			Log:  logging.Discard(),
		}
		if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
			t.Fatal("齿轮修理下单失败")
		}
		profile, err := db.GetProfile(ctx, uid)
		if err != nil {
			t.Fatal(err)
		}
		want := workshopPageSilver(t, srv, reel, reelPartCatGear)
		if got := int64(10000) - profile.Silver; got != want {
			t.Fatalf("实扣 %d，页面报价 %d", got, want)
		}
		if want <= 0 {
			t.Fatalf("齿轮页面价应为正数，实得 %d", want)
		}
		return want
	}
	worn := charge(.2)
	full := charge(1)
	if worn != full {
		t.Fatalf("磨损 0.2 收 %d、满耐久收 %d：残值率是品类属性，两者必须相等", worn, full)
	}
}

func TestWorkshopDiagReelRepairQuoteMatchesWholeService(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	defer db.Close()
	reel, gearID := seedWornWorkshopReel(t, srv, db, .8, .2, .5, .5)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}

	diagBody, ok := hWorkshopDiag(&Ctx{
		Sess: newProbeSess(srv),
		Tree: proto.Args524(proto.Guid(reel.GUID)),
		Log:  logging.Discard(),
	})
	if !ok || len(diagBody) == 0 {
		t.Fatal("21/2 未返回诊断")
	}
	diag, _, err := proto.DecodeValue(diagBody, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	comps := diag.Field(1).Val
	if comps == nil || comps.ArgCount() == 0 {
		t.Fatal("21/2 缺少内件报价")
	}
	price := comps.ArgItem(0).Field(3).Val
	if price == nil {
		t.Fatal("21/2 缺少价格对象")
	}
	quoteField := price.Field(0)
	quote := int64(quoteField.U64())
	// comps 按 workshopPartRank 排序，齿轮（52）恒为第一行；f3 是齿轮的
	// 旧件残值，客户端拿"新件价 − 这一格"报单。
	var wantResidual, wantClient int64
	for _, p := range srv.repairPartsOf(ctx, reel) {
		if p.Cat != uint32(reelPartCatGear) {
			continue
		}
		ps, _ := srv.partRepairBasePrice(ctx, "", "", p)
		rs, _ := srv.workshopPartResidual(int64(p.Cat), ps, 0)
		wantResidual, wantClient = rs, ps-rs
	}
	if quote != wantResidual {
		t.Fatalf("21/2 齿轮报价应为旧件残值 %d，实得 %d", wantResidual, quote)
	}

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(reel.GUID, uint32(reelPartCatGear), gearID, uint64(wantClient)),
		Log:  logging.Discard(),
	}
	if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
		t.Fatal("客户端按 21/2 的差额报价提交同型号修理应成功")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if got := int64(10000) - profile.Silver; got != wantClient {
		t.Fatalf("工坊扣费=%d，页面报价=%d", got, wantClient)
	}
}

func TestWorkshopReelRepairAcceptsAggregatedClientQuote(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	defer db.Close()
	reel, gearID := seedWornWorkshopReel(t, srv, db, .8, .2, .5, .5)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}

	// 客户端多选聚合会把同一件重复塞进 args(503)：报价翻倍，服务端只认一件。
	quote := workshopPageSilver(t, srv, reel, reelPartCatGear)
	orders := proto.Args(proto.S("503"),
		wiregen.MustMakeObj("gcllbdkgkna", map[int]*proto.Value{
			0: proto.U32(uint32(reelPartCatGear)),
			1: proto.StrVal(gearID),
		}),
		wiregen.MustMakeObj("gcllbdkgkna", map[int]*proto.Value{
			0: proto.U32(uint32(reelPartCatGear)),
			1: proto.StrVal(gearID),
		}),
	)
	req := proto.Args524(proto.StrVal("workshop_01"), wiregen.MustMakeObj("nfmkdcbnkhp", map[int]*proto.Value{
		0: proto.Guid(reel.GUID),
		1: orders,
		2: proto.U32(0),
		3: proto.U64(uint64(quote * 2)),
	}))
	c := &Ctx{Sess: newProbeSess(srv), Main: 21, Sub: 3, Tree: req, Log: logging.Discard()}
	if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
		t.Fatal("客户端多选累加后的整机报价应被接受")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if got := int64(10000) - profile.Silver; got != quote {
		t.Fatalf("同一个件重复出现应只扣一次 %d，实扣 %d", quote, got)
	}
}

func TestWorkshopReelRepairRejectsLowQuote(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	defer db.Close()
	reel, gearID := seedWornWorkshopReel(t, srv, db, .8, .2, .5, .5)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(reel.GUID, uint32(reelPartCatGear), gearID, 1),
		Log:  logging.Discard(),
	}
	if _, ok := hWorkshopOrder(c); ok {
		t.Fatal("低于整机保养价的客户端报价应被拒绝")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Silver != 10000 {
		t.Fatalf("低报价被拒绝后余额应保持 10000，实得 %d", profile.Silver)
	}
}

// TestWorkshopReelRepairReplayIsIdempotent 锁定 2026-11-11 15:26 的实机现场：
// 客户端对同一个零件**连发两次** 21/3（同 GUID、同品类、同件，间隔 17ms）。
// 第一次把件修满并扣费；第二次磨损算出来是 0，必须幂等成功回物品树，
// 不能让请求落兜底链——兜底回 7 字节空 args，客户端当场弹「发生内部错误」。
func TestWorkshopReelRepairReplayIsIdempotent(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	reel, gearID := seedWornWorkshopReel(t, srv, db, 1, .5, .5, .5)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 100000, 0, 0); err != nil {
		t.Fatal(err)
	}

	// 第一次：真的修，应该成功并扣费。
	first := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(reel.GUID, uint32(reelPartCatGear), gearID, 0),
		Log:  logging.Discard(),
	}
	if body, ok := hWorkshopOrder(first); !ok || len(body) == 0 {
		t.Fatal("首次修理必须成功")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	silverAfterFirst := profile.Silver
	if silverAfterFirst >= 100000 {
		t.Fatalf("首次修理应当扣费，余额仍为 %d", silverAfterFirst)
	}

	// 第二次：客户端重放同一请求。零件已满，必须幂等成功、且不再扣费。
	second := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(reel.GUID, uint32(reelPartCatGear), gearID, 0),
		Log:  logging.Discard(),
	}
	body, ok := hWorkshopOrder(second)
	if !ok || len(body) == 0 {
		t.Fatal("重放请求必须幂等成功回物品树，不能落兜底链")
	}
	profile, err = db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Silver != silverAfterFirst {
		t.Fatalf("重放不得二次扣费：期望 %d，实得 %d", silverAfterFirst, profile.Silver)
	}
	var txnCount int
	if err := db.SQL.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM txn WHERE uid=? AND kind='workshop_part'`, uid).Scan(&txnCount); err != nil {
		t.Fatal(err)
	}
	if txnCount != 1 {
		t.Fatalf("只应有首次那一条流水，实得 %d 条", txnCount)
	}
	fresh := mustItem(t, db, reel.GUID)
	fields := snapshotFields(t, fresh.FieldsJSON)
	gear, _ := snapshotPart(t, srv, fields, reelPartCatGear)
	if got := snapshotFloat(gear.Fields, gear.Class, csDurability); math.Abs(got-1) > 1e-6 {
		t.Fatalf("零件修好后耐久应为 1，实得 %v", got)
	}
}

// TestWorkshopOilSwapChargesFullPrice 锁定 2026-11-12 03:28 的实机现场：
// 客户端勾「润滑油 → 更换」后弹「发生内部错误」。
//
// 根因不是订单格式，是**报价口径**：我们把轮油并进了轴承那档 0.383 残值，
// 21/2 发给客户端 f3=19，客户端却按新件全价 50 报（日志 client=50 server=31），
// 换件的严格报价校验判不过就直接拒了。官服 21/2 的轮油槽 f3 本来就是 (0,0)
// ——旧油没有折抵价值。改完之后界面、21/4 报价、实扣三者必须都是全价。
func TestWorkshopOilSwapChargesFullPrice(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	defer db.Close()
	reel, _ := seedWornWorkshopReel(t, srv, db, .8, .2, .5, .5)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}

	fields := snapshotFields(t, reel.FieldsJSON)
	_, oilDef := snapshotPart(t, srv, fields, reelPartCatOil)
	var alt string
	if err := db.SQL.QueryRowContext(ctx,
		`SELECT item_id FROM item_def WHERE category=? AND item_id<>? ORDER BY numeric_id LIMIT 1`,
		reelPartCatOil, oilDef.ItemID).Scan(&alt); err != nil {
		t.Fatalf("找不到第二种轮油: %v", err)
	}
	full, _ := srv.partPrice(ctx, alt, reelPartCatOil)
	if full <= 0 {
		t.Fatalf("轮油新件价应为正数，实得 %d", full)
	}

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		// 客户端按新件全价报（没有折抵），这正是实机里那个 50。
		Tree: snapshotWorkshopRequestPart(reel.GUID, uint32(reelPartCatOil), alt, uint64(full)),
		Log:  logging.Discard(),
	}
	if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
		t.Fatal("换轮油被拒——客户端那边看到的就是「发生内部错误」")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if got := int64(10000) - profile.Silver; got != full {
		t.Fatalf("换轮油应按界面全价扣 %d，实扣 %d", full, got)
	}
}

// TestWorkshopSwapChargesClientQuoteWhenClientSkipsResidual 是上一条的通用形态：
// 换件时客户端报价**高于**服务端算的（它没折抵旧件残值），但没超过新件全价。
// 这种不能拒绝（拒绝就是「发生内部错误」），要按界面上的数字收。
//
// 这条专护"残值率还没对上的品类"（竿件/本体只有耐用件默认档 0.9，官服抓包
// 里没有样本）。若哪天客户端对这些品类不认 f3，玩家至少不会被报错挡住。
func TestWorkshopSwapChargesClientQuoteWhenClientSkipsResidual(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	defer db.Close()
	reel, _ := seedWornWorkshopReel(t, srv, db, .8, .2, .5, .5)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 100000, 0, 0); err != nil {
		t.Fatal(err)
	}

	fields := snapshotFields(t, reel.FieldsJSON)
	_, gearDef := snapshotPart(t, srv, fields, reelPartCatGear)
	var alt string
	if err := db.SQL.QueryRowContext(ctx,
		`SELECT item_id FROM item_def WHERE category=? AND item_id<>? ORDER BY numeric_id LIMIT 1`,
		reelPartCatGear, gearDef.ItemID).Scan(&alt); err != nil {
		t.Fatalf("找不到第二种齿轮: %v", err)
	}
	full, _ := srv.partPrice(ctx, alt, reelPartCatGear)
	if full <= 0 {
		t.Fatalf("齿轮新件价应为正数，实得 %d", full)
	}

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(reel.GUID, uint32(reelPartCatGear), alt, uint64(full)),
		Log:  logging.Discard(),
	}
	if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
		t.Fatal("客户端未折抵残值时应按界面报价成交，不该拒绝")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if got := int64(100000) - profile.Silver; got != full {
		t.Fatalf("应按界面报价扣 %d，实扣 %d", full, got)
	}
}

// TestWorkshopSwapRejectsQuoteAboveFullPrice 是上一条的边界：同一件被列两次时
// 客户端报价会翻倍，超过新件全价，这种只能按去重后的价收，不能跟着翻倍。
func TestWorkshopSwapRejectsQuoteAboveFullPrice(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	defer db.Close()
	reel, _ := seedWornWorkshopReel(t, srv, db, .8, .2, .5, .5)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 100000, 0, 0); err != nil {
		t.Fatal(err)
	}

	fields := snapshotFields(t, reel.FieldsJSON)
	_, gearDef := snapshotPart(t, srv, fields, reelPartCatGear)
	var alt string
	if err := db.SQL.QueryRowContext(ctx,
		`SELECT item_id FROM item_def WHERE category=? AND item_id<>? ORDER BY numeric_id LIMIT 1`,
		reelPartCatGear, gearDef.ItemID).Scan(&alt); err != nil {
		t.Fatalf("找不到第二种齿轮: %v", err)
	}
	full, _ := srv.partPrice(ctx, alt, reelPartCatGear)
	// 去重后的应收价 = 目标新件价 − 残值（与 21/2 界面同源）。
	residual, _ := srv.workshopPartResidual(reelPartCatGear, full, 0)
	page := full - residual

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPart(reel.GUID, uint32(reelPartCatGear), alt, uint64(full*2)),
		Log:  logging.Discard(),
	}
	if body, ok := hWorkshopOrder(c); !ok || len(body) == 0 {
		t.Fatal("报价翻倍也应成交（按去重后的价收），不该拒绝")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if got := int64(100000) - profile.Silver; got != page {
		t.Fatalf("报价超过新件全价时应按去重后 %d 收，实扣 %d", page, got)
	}
}
