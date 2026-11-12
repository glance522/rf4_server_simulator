package gameserver

// 工坊"银币修理要等、金币即时"的回归用例。
//
// 官服口径（官方用户手册·工作坊 + 2026-11-11 实机截图）：
//   - 银币修理：物品留在工坊，要等 N 小时，修好自己取回；
//   - 金币修理：立即完成；
//   - 界面上的「N 小时 / 即时」两个角标，差的就是 hpopgbjohpk.f3 那一格工时。
//
// 服务端落地成"扣费 → 入队 → 到期生效"三步（见 workshop_repair_queue.go）。
// 这组用例守的正是这三步的边界：
//  1. 入队必须**先扣钱、不动物品**——否则玩家能看到白修的漏洞；
//  2. 未到期不许生效，到期才落快照；
//  3. 结算幂等，重放不二次扣费；
//  4. 入队写库失败必须把钱包一起回滚；
//  5. 即时路径（金币支付/回滚开关）必须清掉在办任务，不能留下旧快照回灌。

import (
	"math"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// rodCatBlank 是"竿坯"品类：竿的子件里那一根坯（item_def 里 rodblank_* 全是 36）。
// 竿的工坊报价按"这根坯"下单价，所以用例里要点它。
const rodCatBlank = int64(36)

// setWorkshopSilverRepairDelay 改银币排队开关并让运行参数缓存立即失效。
//
// 直接写 tunables 表再清缓存，比只塞 srv.tunCache 可靠：tunCache 是 30 秒
// 缓存，单塞 map 而不改 tunAt 时，只要 tunAt 已过期就会被 DB 内容整体覆盖。
func setWorkshopSilverRepairDelay(t *testing.T, srv *Server, db *store.DB, on bool) {
	t.Helper()
	val := "0"
	if on {
		val = "1"
	}
	if err := db.TunableSet(t.Context(), tunableWorkshopSilverRepair, val, "test"); err != nil {
		t.Fatalf("设置工坊修理开关失败: %v", err)
	}
	srv.tunMu.Lock()
	srv.tunCache, srv.tunAt = nil, time.Time{}
	srv.tunMu.Unlock()
}

// mustWorkshopJob 读取在办任务，没有就判失败。
func mustWorkshopJob(t *testing.T, db *store.DB, guid string) *store.WorkshopRepairJob {
	t.Helper()
	job, err := db.WorkshopRepairJobFor(t.Context(), guid)
	if err != nil {
		t.Fatalf("读取在办任务失败: %v", err)
	}
	if job == nil {
		t.Fatalf("应存在在办任务，实际没有: %s", guid)
	}
	return job
}

// workshopJobRows 统计在办任务行数（按物品 GUID）。
func workshopJobRows(t *testing.T, db *store.DB, guid string) int {
	t.Helper()
	var n int
	if err := db.SQL.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM workshop_repair_job WHERE guid=?`, guid).Scan(&n); err != nil {
		t.Fatalf("统计在办任务失败: %v", err)
	}
	return n
}

// expireWorkshopJobs 把该玩家所有在办任务改成"已完工"，模拟等够了在线工时。
//
// 不走 sleep：判定只看 remaining_s，把它清零就等于工时走完了。
func expireWorkshopJobs(t *testing.T, db *store.DB, uid int64) {
	t.Helper()
	if _, err := db.SQL.ExecContext(t.Context(),
		`UPDATE workshop_repair_job SET remaining_s=0 WHERE uid=?`, uid); err != nil {
		t.Fatalf("把在办任务改成完工: %v", err)
	}
}

// settleWorkshopForTest 结算该玩家的到期任务。
func settleWorkshopForTest(t *testing.T, srv *Server, uid int64) []string {
	t.Helper()
	return srv.workshopSettleRepairs(t.Context(), uid)
}

// workshopOrderOnce 走一次 21/3，返回是否成功。
func workshopOrderOnce(srv *Server, guid string, category uint32, sysID string, gold bool) ([]byte, bool) {
	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPartPayment(guid, category, sysID, 0, gold),
		Log:  logging.Discard(),
	}
	return hWorkshopOrder(c)
}

// TestWorkshopSilverRepairQueuesInsteadOfApplyingImmediately 守"先扣钱、不动物品"。
//
// 这是整个排队机制最容易被写错的一步：只要入队阶段顺手把 item 行改了，
// 就等于"付钱立刻修好"，银币修理要等的口径当场失效，而且没有任何报错。
func TestWorkshopSilverRepairQueuesInsteadOfApplyingImmediately(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}
	rod, partID := seedWornWorkshopRod(t, srv, db, .4, .5)

	if body, ok := workshopOrderOnce(srv, rod.GUID, 4, partID, false); !ok || len(body) == 0 {
		t.Fatal("银币修理订单应成功入队")
	}

	// 钱先扣掉：这是"已经付过款"的唯一证据。
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	paid := int64(10000) - profile.Silver
	if paid <= 0 {
		t.Fatalf("银币修理应先扣费，余额仍为 %d", profile.Silver)
	}

	// 物品保持磨损：修理还没完成，这才是"还在工坊里"的表现。
	fresh := mustItem(t, db, rod.GUID)
	if fresh.Durability != .5 {
		t.Fatalf("入队阶段本体耐久不应变化，实得 %v", fresh.Durability)
	}
	fields := snapshotFields(t, fresh.FieldsJSON)
	part, _ := snapshotPart(t, srv, fields, 4)
	if got := snapshotFloat(part.Fields, part.Class, csDurability); math.Abs(got-.4) > 1e-6 {
		t.Fatalf("入队阶段竿柄耐久不应变化，实得 %v", got)
	}

	job := mustWorkshopJob(t, db, rod.GUID)
	if job.SilverPaid != paid {
		t.Fatalf("任务记录已付银币=%d，实际扣费=%d", job.SilverPaid, paid)
	}
	if job.RemainingS <= 0 {
		t.Fatalf("剩余在线工时应为正数，实得 %v", job.RemainingS)
	}
	// 工时按品类固定档位，与磨损无关：nc.jsonl 那单五件的 hpopgbjohpk.f3
	// 是 52→2、7→36、9→24、8→36、10→18，而回包的锁 RemainMin=120
	// 正好等于齿轮那 2 个**游戏小时**。竿柄（4）没有官服样本，走默认档。
	//
	// 这一格里存的是现实秒，所以折回来要除 1 游戏小时 = 150 现实秒，
	// 不是 3600（见 workshopRepairSeconds 的单位铁律）。
	wantHours := int(srv.workshopPartHours(4))
	gotHours := int(math.Round(job.RemainingS / workshopGameHourSeconds))
	if gotHours != wantHours {
		t.Fatalf("排队工时=%d 游戏小时，期望该品类固定档位 %d 游戏小时", gotHours, wantHours)
	}
	if job.TickTS <= 0 {
		t.Fatalf("入队必须写入计时基准 tick_ts，实得 %v", job.TickTS)
	}
}

// TestWorkshopSilverRepairSettlesWhenDueTimeArrives 守"到了才生效 + 结算幂等"。
func TestWorkshopSilverRepairSettlesWhenDueTimeArrives(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}
	rod, partID := seedWornWorkshopRod(t, srv, db, .4, .5)

	if body, ok := workshopOrderOnce(srv, rod.GUID, 4, partID, false); !ok || len(body) == 0 {
		t.Fatal("银币修理订单应成功入队")
	}

	// 未到期：结算必须什么都不做，任务留在表里等下一轮。
	if settled := settleWorkshopForTest(t, srv, uid); len(settled) != 0 {
		t.Fatalf("未到期的任务不应生效，实际结算了 %v", settled)
	}
	if fresh := mustItem(t, db, rod.GUID); fresh.Durability != .5 {
		t.Fatalf("未到期时本体耐久不应变化，实得 %v", fresh.Durability)
	}
	if n := workshopJobRows(t, db, rod.GUID); n != 1 {
		t.Fatalf("未到期时任务应保留，实得 %d 行", n)
	}

	// 到期：快照落地，本体与用到的零件都回满。
	expireWorkshopJobs(t, db, uid)
	settled := settleWorkshopForTest(t, srv, uid)
	if len(settled) != 1 || settled[0] != rod.GUID {
		t.Fatalf("到期应结算该物品，实际 %v", settled)
	}
	fresh := mustItem(t, db, rod.GUID)
	if fresh.Durability != 1 {
		t.Fatalf("到期结算后本体耐久应回 1，实得 %v", fresh.Durability)
	}
	fields := snapshotFields(t, fresh.FieldsJSON)
	part, _ := snapshotPart(t, srv, fields, 4)
	if got := snapshotFloat(part.Fields, part.Class, csDurability); math.Abs(got-1) > 1e-6 {
		t.Fatalf("到期结算后竿柄耐久应回 1，实得 %v", got)
	}
	if n := workshopJobRows(t, db, rod.GUID); n != 0 {
		t.Fatalf("结算成功后任务应删除，实得 %d 行", n)
	}

	// 幂等：再跑一轮不应重复生效，也不应报错。
	if again := settleWorkshopForTest(t, srv, uid); len(again) != 0 {
		t.Fatalf("已结算的任务不应重复生效，实际 %v", again)
	}
}

// TestWorkshopSilverRepairReplayWhilePendingKeepsSingleCharge 守"在办期间重放不二次扣费"。
//
// 客户端会连发两次相同 21/3（2026-11-11 15:26 实测，间隔 17ms）。入队阶段不改
// item 行，第二次看到的磨损仍是原值、价格照算，不挡就会排两次队扣两次钱。
func TestWorkshopSilverRepairReplayWhilePendingKeepsSingleCharge(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}
	rod, partID := seedWornWorkshopRod(t, srv, db, .4, .5)

	if body, ok := workshopOrderOnce(srv, rod.GUID, 4, partID, false); !ok || len(body) == 0 {
		t.Fatal("首次银币修理应成功入队")
	}
	first, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	firstJob := mustWorkshopJob(t, db, rod.GUID)

	// 重放：必须幂等成功回物品树（不能落兜底链让客户端弹内部错误），
	// 但绝不二次扣费、绝不刷新到期时间。
	body, ok := workshopOrderOnce(srv, rod.GUID, 4, partID, false)
	if !ok || len(body) == 0 {
		t.Fatal("在办期间的重放请求必须幂等成功回物品树")
	}
	second, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if second.Silver != first.Silver {
		t.Fatalf("重放不得二次扣费：期望 %d，实得 %d", first.Silver, second.Silver)
	}
	var txnCount int
	if err := db.SQL.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM txn WHERE uid=? AND kind='workshop_part'`, uid).Scan(&txnCount); err != nil {
		t.Fatal(err)
	}
	if txnCount != 1 {
		t.Fatalf("只应有首次那一条流水，实得 %d 条", txnCount)
	}
	if n := workshopJobRows(t, db, rod.GUID); n != 1 {
		t.Fatalf("同一物品只应有一行在办任务，实得 %d 行", n)
	}
	if again := mustWorkshopJob(t, db, rod.GUID); again.DueTS != firstJob.DueTS {
		t.Fatalf("重放不应推迟到期时间: %v -> %v", firstJob.DueTS, again.DueTS)
	}
	if fresh := mustItem(t, db, rod.GUID); fresh.Durability != .5 {
		t.Fatalf("重放不应改动物品，本体耐久实得 %v", fresh.Durability)
	}
}

// TestWorkshopSilverRepairRollsBackWalletWhenJobWriteFails 守"扣费与入队同生共死"。
//
// 排队路径不写 item 行，原来那个"物品写库失败"的触发器就失效了；这里换成注入
// 任务表写库失败，守的是同一件事：入队失败时钱包和流水都必须一起回滚，不能
// 出现"钱扣了、任务没建、东西也没修"的状态。
func TestWorkshopSilverRepairRollsBackWalletWhenJobWriteFails(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 1000, 0, 0); err != nil {
		t.Fatal(err)
	}
	rod, partID := seedWornWorkshopRod(t, srv, db, .8, .5)

	if _, err := db.SQL.ExecContext(ctx,
		`CREATE TRIGGER fail_workshop_job BEFORE INSERT ON workshop_repair_job
		 FOR EACH ROW BEGIN SELECT RAISE(ABORT,'注入工坊任务写库失败'); END`); err != nil {
		t.Fatalf("注入失败: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.SQL.Exec(`DROP TRIGGER IF EXISTS fail_workshop_job`)
	})

	if _, ok := workshopOrderOnce(srv, rod.GUID, 4, partID, false); ok {
		t.Fatal("入队写库失败时工坊订单不应成功")
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Silver != 1000 {
		t.Fatalf("入队失败后余额必须回滚为 1000，实得 %d", profile.Silver)
	}
	var txnCount int
	if err := db.SQL.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM txn WHERE uid=? AND kind='workshop_part'`, uid).Scan(&txnCount); err != nil {
		t.Fatal(err)
	}
	if txnCount != 0 {
		t.Fatalf("入队失败后流水必须回滚，实得 %d 条", txnCount)
	}
	if n := workshopJobRows(t, db, rod.GUID); n != 0 {
		t.Fatalf("入队失败后不应留下任务行，实得 %d 行", n)
	}
	fresh := mustItem(t, db, rod.GUID)
	if fresh.Durability != .5 {
		t.Fatalf("入队失败后本体耐久应保持 0.5，实得 %v", fresh.Durability)
	}
}

// TestWorkshopInstantPathClearsPendingSilverJob 守"即时修好必须作废在办任务"。
//
// 不清会出事：玩家先用银币排了队，之后这笔修理以即时方式完成（金币支付走同一
// 分支），旧任务到期时会把付银币时算出的旧快照写回去，把已经修好的状态覆盖成
// 过时数据——甚至把后来换上/修好的零件回退。
//
// 金币换件在单测里构造不出来（item_price 里轮内零件没有金币价，partPrice 的
// 兜底路径只算银币，costGold=0 会被零金额拦截拒掉），所以这里用回滚开关把订单
// 打到同一个即时分支上：两者在代码里是同一个 deferred=false。
func TestWorkshopInstantPathClearsPendingSilverJob(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}
	rod, partID := seedWornWorkshopRod(t, srv, db, .4, .5)

	if body, ok := workshopOrderOnce(srv, rod.GUID, 4, partID, false); !ok || len(body) == 0 {
		t.Fatal("银币修理订单应成功入队")
	}
	if n := workshopJobRows(t, db, rod.GUID); n != 1 {
		t.Fatalf("入队后应有 1 行任务，实得 %d", n)
	}

	setWorkshopSilverRepairDelay(t, srv, db, false)
	if body, ok := workshopOrderOnce(srv, rod.GUID, 4, partID, false); !ok || len(body) == 0 {
		t.Fatal("即时路径下同一物品应能直接修好")
	}
	if n := workshopJobRows(t, db, rod.GUID); n != 0 {
		t.Fatalf("即时修好后必须清掉在办任务，实得 %d 行", n)
	}
	fresh := mustItem(t, db, rod.GUID)
	if fresh.Durability != 1 {
		t.Fatalf("即时修好后本体耐久应回 1，实得 %v", fresh.Durability)
	}
}

// workshopListGUIDs 走一次 21/1，返回清单里的物品 GUID 集合。
func workshopListGUIDs(t *testing.T, srv *Server) map[string]bool {
	t.Helper()
	body, ok := hWorkshopList(&Ctx{Sess: newProbeSess(srv), Main: 21, Sub: 1, Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatal("21/1 未返回物品清单")
	}
	root, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatalf("解码 21/1 失败: %v", err)
	}
	out := map[string]bool{}
	for _, it := range root.Items {
		if it == nil {
			continue
		}
		out[it.Field(0).Hex] = true
	}
	return out
}

// workshopDiagParts 走一次 21/2，返回零件行（hlopebmchhp）。
func workshopDiagParts(t *testing.T, srv *Server, guid string) []*proto.Value {
	t.Helper()
	body, ok := hWorkshopDiag(&Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 2,
		Tree: proto.Args524(proto.Guid(guid)), Log: logging.Discard(),
	})
	if !ok || len(body) == 0 {
		t.Fatal("21/2 未返回诊断")
	}
	root, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatalf("解码 21/2 失败: %v", err)
	}
	args := root.Field(1).Val
	if args == nil {
		return nil
	}
	return args.Items
}

// TestWorkshopRepairClockOnlyRunsWhileOnline 守"在线才走、下线冻结"。
//
// 官服口径（玩家 2026-11-11 口述）：物件进工坊后带锁和倒计时，只有在线时倒计时
// 才走，下线就冻住。实现上 due_ts 是个绝对时间戳，离线期间照走，所以判定必须
// 换成 remaining_s + tick_ts 这套在线秒表；这里守的就是它不会被离线段污染。
func TestWorkshopRepairClockOnlyRunsWhileOnline(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}
	rod, partID := seedWornWorkshopRod(t, srv, db, .4, .5)
	if body, ok := workshopOrderOnce(srv, rod.GUID, 4, partID, false); !ok || len(body) == 0 {
		t.Fatal("银币修理订单应成功入队")
	}

	job := mustWorkshopJob(t, db, rod.GUID)
	base := job.TickTS
	if base <= 0 || job.RemainingS <= 0 {
		t.Fatalf("入队必须写好在线的计时基准与剩余工时，实得 tick=%v remain=%v",
			job.TickTS, job.RemainingS)
	}

	// 在线 600 秒：剩余工时正好少 600。
	if err := db.TickWorkshopRepairJobs(ctx, uid, base+600); err != nil {
		t.Fatalf("扣在线工时失败: %v", err)
	}
	mid := mustWorkshopJob(t, db, rod.GUID)
	if got := job.RemainingS - mid.RemainingS; math.Abs(got-600) > 1 {
		t.Fatalf("在线 600 秒应扣 600 秒工时，实际扣了 %v", got)
	}

	// 下线 2 小时后再上线：登录只重置基准，不补扣离线段。
	backAt := base + 7200
	if err := db.ResetWorkshopRepairTick(ctx, uid, backAt); err != nil {
		t.Fatalf("重置计时基准失败: %v", err)
	}
	back := mustWorkshopJob(t, db, rod.GUID)
	if math.Abs(back.RemainingS-mid.RemainingS) > 1e-6 {
		t.Fatalf("离线期间剩余工时必须冻结：%v -> %v", mid.RemainingS, back.RemainingS)
	}
	if back.TickTS != backAt {
		t.Fatalf("重置后基准应为 %v，实得 %v", backAt, back.TickTS)
	}

	// 冻结期间不该完工：结算什么都不做，物品保持磨损。
	if settled := settleWorkshopForTest(t, srv, uid); len(settled) != 0 {
		t.Fatalf("工时没走完不应生效，实际结算了 %v", settled)
	}
	if fresh := mustItem(t, db, rod.GUID); fresh.Durability != .5 {
		t.Fatalf("工时没走完本体耐久不应变化，实得 %v", fresh.Durability)
	}

	// 上线后继续走：再扣满剩下的工时，才允许落地。
	if err := db.TickWorkshopRepairJobs(ctx, uid, back.TickTS+back.RemainingS+1); err != nil {
		t.Fatalf("扣完剩余工时失败: %v", err)
	}
	if settled := settleWorkshopForTest(t, srv, uid); len(settled) != 1 {
		t.Fatalf("工时走完后应结算 1 件，实际 %v", settled)
	}
	if fresh := mustItem(t, db, rod.GUID); fresh.Durability != 1 {
		t.Fatalf("结算后本体耐久应回 1，实得 %v", fresh.Durability)
	}
}

// TestWorkshopPendingRepairKeepsQuotesButBlocksDoubleCharge 守"在办期间面板照常报价、
// 但重复下单不扣钱"。
//
// 2026-11-11 21:31 实机翻车：为了堵住"再修一次"，21/2 在办期间直接回空 args(143)，
// 客户端把整个面板画成空白——零件行、价格、修理按钮全没有，玩家看到的是
// "修理选项都没有了"。真正要挡的是重复扣费，那一步本来就由 21/3 的幂等分支负责：
// 它把重复请求回成"已在工坊修理中，还需约 X"，既不扣钱也不让界面变空。
//
// 官服的表现（玩家口述）：工坊列表里物件**还在**，带一把锁 + 倒计时；点进去
// 照样能看到零件和价格，只是再点一次不会重复生效。
func TestWorkshopPendingRepairKeepsQuotesButBlocksDoubleCharge(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}
	rod, partID := seedWornWorkshopRod(t, srv, db, .4, .5)

	if !workshopListGUIDs(t, srv)[rod.GUID] {
		t.Fatal("下单前 21/1 应列出这物品")
	}
	before := len(workshopDiagParts(t, srv, rod.GUID))
	if before == 0 {
		t.Fatal("下单前 21/2 应给出零件报价")
	}

	if body, ok := workshopOrderOnce(srv, rod.GUID, 4, partID, false); !ok || len(body) == 0 {
		t.Fatal("银币修理订单应成功入队")
	}
	afterFirst, err := db.EnsureProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}

	if !workshopListGUIDs(t, srv)[rod.GUID] {
		t.Fatal("在办期间 21/1 仍应列出这物品：官服的工坊面板就是「物件还在、带锁和倒计时」，不是凭空消失")
	}
	if got := len(workshopDiagParts(t, srv, rod.GUID)); got != before {
		t.Fatalf("在办期间 21/2 应照常报价（%d 个零件行），实得 %d：回空表会把整个面板画成空白", before, got)
	}

	// 客户端会补发一次支付确认（21/4 也是换件结构）：必须幂等成功，且绝不二次扣费。
	if body, ok := workshopOrderOnce(srv, rod.GUID, 4, partID, false); !ok || len(body) == 0 {
		t.Fatal("重复下单应按幂等成功回包")
	}
	if again, gerr := db.EnsureProfile(ctx, uid); gerr != nil {
		t.Fatal(gerr)
	} else if again.Silver != afterFirst.Silver {
		t.Fatalf("重复下单不能二次扣费：%d → %d", afterFirst.Silver, again.Silver)
	}

	expireWorkshopJobs(t, db, uid)
	if got := settleWorkshopForTest(t, srv, uid); len(got) != 1 {
		t.Fatalf("到期结算应落地 1 件，实得 %d", len(got))
	}
	if !workshopListGUIDs(t, srv)[rod.GUID] {
		t.Fatal("修好后应重新出现在 21/1")
	}
	parts := workshopDiagParts(t, srv, rod.GUID)
	if len(parts) == 0 {
		t.Fatal("修好后 21/2 应能正常报价（全部 0 元）")
	}
	for _, p := range parts {
		if p.Field(1).Num == nil {
			t.Fatalf("零件行缺 f1: %v", p.Class)
		}
		if cond := float64(proto.F32FromBits(uint32(*p.Field(1).Num))); math.Abs(cond-1) > 1e-6 {
			t.Fatalf("修好后零件完好度应为 1，实得 %v", cond)
		}
	}
}

// workshopListGrade 走一次 21/1，取某件物品的档位位（ookkpknacja.f2）。
//
// 官服实测：同一件渔轮送修前 4、送修后 5（21/1 重查三次稳定为 5）。客户端
// 拿这一格把列表行画成"修理中、不可操作"。
func workshopListGrade(t *testing.T, srv *Server, guid string) (uint32, bool) {
	t.Helper()
	body, ok := hWorkshopList(&Ctx{Sess: newProbeSess(srv), Main: 21, Sub: 1, Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatal("21/1 未返回物品清单")
	}
	root, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatalf("解码 21/1 失败: %v", err)
	}
	for _, it := range root.Items {
		if it == nil || it.Field(0).Hex != guid {
			continue
		}
		f2 := it.Field(2)
		return f2.U32(), true
	}
	return 0, false
}

// workshopLockOf 取物品树里的时效对象（f24 / bkmnbppciao），没有则返回 nil。
func workshopLockOf(t *testing.T, srv *Server, it *store.Item) *proto.Value {
	t.Helper()
	tree, err := srv.ItemToTree(t.Context(), it)
	if err != nil {
		t.Fatalf("组装物品树失败: %v", err)
	}
	idx := fieldIdxByCS(it.WireClass, "bkmnbppciao")
	if idx < 0 || idx >= len(tree.Fields) {
		return nil
	}
	if v := tree.Fields[idx].Val; v != nil && v.Kind == proto.KindObj {
		return v
	}
	return nil
}

// TestWorkshopRepairLockAppearsOnItemTreeWhilePending 守"在办期间画锁、修好就摘锁"。
//
// 官服口径（2026-11-11 20:10 抓包 + 玩家实机口述）：银币修理下单后物品仍在
// 背包里，但背包里带一把锁、整体置灰不可用，工坊里显示剩余修理时间；金币修
// 即时完成，没有锁。
//
// 客户端不认我们自己编的状态位，它认的是物品树的时效对象
// （lcjcffibmbi）：对象存在且类型位 == 1 才进 kItemState_Repairing。
// 倒计时由 f2（起算点，世界游戏分钟）+ f3（时长，游戏分钟）决定，口径是
// 用日志逐帧回溯对出来的（见 workshop_repair_lock.go 文件头的对照表）：
// f2 偏离客户端世界时钟 W 太多，角标就恒为 00:00。这里守的就是这三格，
// 以及"修好之后必须摘干净"——否则修好的东西会永远显示成修理中。
func TestWorkshopRepairLockAppearsOnItemTreeWhilePending(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}
	rod, partID := seedWornWorkshopRod(t, srv, db, .4, .5)

	// 下单前：没有时效对象，21/1 档位位是常态值（不是 5）。
	if lock := workshopLockOf(t, srv, mustItem(t, db, rod.GUID)); lock != nil {
		t.Fatal("未送修的物品不应带时效对象")
	}
	if grade, ok := workshopListGrade(t, srv, rod.GUID); !ok || grade == workshopRepairingGrade {
		t.Fatalf("未送修时档位位不应是修理中(%d)，实得 %d", workshopRepairingGrade, grade)
	}

	if body, ok := workshopOrderOnce(srv, rod.GUID, 4, partID, false); !ok || len(body) == 0 {
		t.Fatal("银币修理订单应成功入队")
	}

	// 在办期间：时效对象必须出现，且三格口径正确。
	job := mustWorkshopJob(t, db, rod.GUID)
	lock := workshopLockOf(t, srv, mustItem(t, db, rod.GUID))
	if lock == nil {
		t.Fatal("在办期间物品树必须带时效对象（f24），否则客户端不会画锁和倒计时")
	}
	f0 := lock.Field(0)
	if got := f0.StrOr(); got != "workshop_01" {
		t.Fatalf("时效对象 f0 应回显下单时的工坊 id，实得 %q", got)
	}
	f1 := lock.Field(1)
	if got := f1.U32(); got != 1 {
		t.Fatalf("时效对象 f1 必须是 1（有时效），实得 %d：0 会让客户端根本不进修理段", got)
	}
	// f2 只能是 32 位（wire op U32）。这条是实机证实的：改成 U64 FILETIME 之后
	// 客户端直接崩在"正在载入个人资料"（会话每 500ms 起一次、推完 2/2 就断），
	// 说明它对这个字段的 reader 是定长 32 位、不看 op tag。
	f2 := lock.Field(2)
	if f2.Op != "U32" {
		t.Fatalf("时效对象 f2 的 wire op 必须是 U32，实得 %q：换成 U64 会把客户端打崩", f2.Op)
	}
	// f2 = 下发时刻的世界游戏分钟，必须和客户端世界时钟 W 同坐标。
	// 口径依据见 workshop_repair_lock.go 文件头那张「字节 ↔ 实机表现」对照表：
	// 填 unix 分钟（2990 万）或 int32 上限时角标都是 00:00，只有贴着 W（约 1.42 亿）
	// 才正常倒数。
	anchor := int64(f2.U32())
	if want := worldTotalGameMin(time.Now()); anchor < want-120 || anchor > want+120 {
		t.Fatalf("时效对象 f2=%d 应是当前世界游戏分钟（约 %d，允许 120 分钟误差）"+
			"：填 unix 分钟或 int32 上限都会让客户端算成已过期", anchor, want)
	}
	// f3 是时长，单位是**游戏分钟**，而且它的来源必须是品类档位本身：
	//
	//	f3(游戏分钟) = workshopPartHours(品类) × 60
	//
	// 这里**不能**拿 job.RemainingS 反推（×0.4）——那样写等于把入队的换算和
	// 这里的换算绑在一起，两边一起错也照样绿。2026-11-12 就是这么漏掉"把游戏
	// 小时当现实小时"这个 bug 的：6 游戏小时被排成 6 现实小时，而 f3 反推出来
	// 又是自洽的。
	wantMin := int64(srv.workshopPartHours(4)) * 60
	f3 := lock.Field(3)
	if got := int64(f3.U32()); got != wantMin {
		t.Fatalf("时效对象 f3 应为 %d 游戏分钟（%d 游戏小时 × 60），实得 %d",
			wantMin, srv.workshopPartHours(4), got)
	}
	// 剩下这一格是现实秒：1 游戏小时 = 150 现实秒，不是 3600。
	if want := float64(srv.workshopPartHours(4)) * workshopGameHourSeconds; math.Abs(job.RemainingS-want) > 1 {
		t.Fatalf("剩余现实秒应为 %.0f（%d 游戏小时 × 150 秒），实得 %.0f",
			want, srv.workshopPartHours(4), job.RemainingS)
	}

	// 21/1 同步改成"修理中"档位位（官服 4 → 5）。
	if grade, ok := workshopListGrade(t, srv, rod.GUID); !ok || grade != workshopRepairingGrade {
		t.Fatalf("在办期间 21/1 档位位应为 %d，实得 %d", workshopRepairingGrade, grade)
	}

	// 到期结算后：锁必须摘干净，档位位回常态。
	expireWorkshopJobs(t, db, uid)
	if got := settleWorkshopForTest(t, srv, uid); len(got) != 1 {
		t.Fatalf("到期应结算 1 件，实得 %d", len(got))
	}
	if lock := workshopLockOf(t, srv, mustItem(t, db, rod.GUID)); lock != nil {
		t.Fatal("修好后物品树不该再带时效对象，否则客户端一直显示修理中")
	}
	if grade, ok := workshopListGrade(t, srv, rod.GUID); !ok || grade == workshopRepairingGrade {
		t.Fatalf("修好后档位位不该再是 %d，实得 %d", workshopRepairingGrade, grade)
	}
}

// TestWorkshopRepairLockDropsWhenInstantPathClearsJob 守"即时修好当场摘锁"。
//
// 玩家先用银币排了队，之后管理员把排队开关关掉（tunable 秒级回滚），同一件
// 东西再修一次就走即时路径：任务行被删、物品当场修好。锁是从"在办任务"这份
// 缓存里来的，即时路径删任务后不主动失效的话，接下来那棵回包树（以及紧跟的
// 4/22 推送）还会带着时效对象，玩家看到的是"已经修好了却还锁着、还在倒计时"。
func TestWorkshopRepairLockDropsWhenInstantPathClearsJob(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}
	rod, partID := seedWornWorkshopRod(t, srv, db, .4, .5)

	if body, ok := workshopOrderOnce(srv, rod.GUID, 4, partID, false); !ok || len(body) == 0 {
		t.Fatal("银币修理订单应成功入队")
	}
	if lock := workshopLockOf(t, srv, mustItem(t, db, rod.GUID)); lock == nil {
		t.Fatal("在办期间应带时效对象")
	}

	// 关掉排队开关：同一件东西再修一次走即时路径，任务当场清掉。
	setWorkshopSilverRepairDelay(t, srv, db, false)
	if body, ok := workshopOrderOnce(srv, rod.GUID, 4, partID, false); !ok || len(body) == 0 {
		t.Fatal("即时路径应修好并回物品树")
	}
	if n := workshopJobRows(t, db, rod.GUID); n != 0 {
		t.Fatalf("即时修好后在办任务应清空，实得 %d 行", n)
	}
	if lock := workshopLockOf(t, srv, mustItem(t, db, rod.GUID)); lock != nil {
		t.Fatal("即时修好后不该再有锁：东西已经好了，再挂着倒计时等于骗玩家继续等")
	}
}

// TestWorkshopPushUsesArgs524 守"工坊 4/22 推送的 args code 必须是 524"。
//
// 不是格式洁癖，是实机撞出来的：2026-11-11 发 "61"（普通背包增量更新）时，
// 客户端只按普通物品更新处理，**不进 kItemState_Repairing** —— 工坊面板照样
// 显示"修好了"、没有倒计时，退出重进（走 4/5 全量背包）才出现"修理中+倒计时"。
// 当时 21/3 响应体、21/1 的档位位 f2=5 都是对的，唯独这条推送的 code 错，
// 所以症状是"重连就好了"。
//
// 官服那条 4/22 是 args("524") + 单件物品树 + f24 时效对象
// （logs/net_capture_official.jsonl seq=337，591B）。
func TestWorkshopPushUsesArgs524(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	// 用真实物品树，顺带把"整棵树能编码"也覆盖上——推送被编码器挡掉时
	// Push 只返回 0，线上表现为"点了没反应"，很难从客户端看出来。
	tree, err := srv.ItemToTree(ctx, rod)
	if err != nil {
		t.Fatalf("组装物品树失败: %v", err)
	}

	sess, peer := newTunnelProbe(t, srv)
	pushWorkshopItem(sess, tree)

	env := awaitPushEnvelope(t, peer)
	if env.Main != 4 || env.Sub != 22 {
		t.Fatalf("工坊物品刷新应走 4/22，实得 %d/%d", env.Main, env.Sub)
	}
	body, _, err := proto.DecodeValue(env.Body, 0, srv.Table())
	if err != nil {
		t.Fatalf("推送体解码失败: %v", err)
	}
	if body.Kind != proto.KindArgs || body.Code == nil {
		t.Fatalf("推送体顶层应是 args 容器，实得 kind=%v", body.Kind)
	}
	if got := *body.Code; got != "524" {
		t.Fatalf("工坊 4/22 的 args code 必须是 524，实得 %q：发 61 客户端不进修理中，只认成普通物品更新", got)
	}
	if len(body.Items) != 1 {
		t.Fatalf("args 应恰好带 1 件物品树，实得 %d 项", len(body.Items))
	}
	if body.Items[0].Kind != proto.KindObj {
		t.Fatalf("args[0] 应是物品树(obj)，实得 kind=%v", body.Items[0].Kind)
	}
}

// TestWorkshopQueuedOrderStillPushes422 守"下单路径的 4/22 走延迟通道后没有弄丢"。
//
// 这条推送现在不在回包里同步发，而是排进 AfterResponse 再睡一段
// （见 workshopOrderPushDelay：贴着回包发客户端当场弹「发生内部错误」，完全不发
// 则要退出重连才进 kItemState_Repairing）。调度层一旦把推送吞掉，症状就是
// 退回"不弹错但也没角标"，而且没有任何报错——所以这里必须从隧道那一端真收到它。
//
// 测试脚手架把 workshop_order_push_delay_ms 压成 0（newStaticRigTestServer），
// 所以回调执行时是同步落地的，不存在要等 1.5 秒醒来才断言的 goroutine。
func TestWorkshopQueuedOrderStillPushes422(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}
	rod, partID := seedWornWorkshopRod(t, srv, db, .4, .5)

	sess, peer := newTunnelProbe(t, srv)
	c := &Ctx{
		Sess: sess, Main: 21, Sub: 3,
		Tree: snapshotWorkshopRequestPartPayment(rod.GUID, 4, partID, 0, false),
		Log:  logging.Discard(),
	}
	body, ok := hWorkshopOrder(c)
	if !ok || len(body) == 0 {
		t.Fatal("银币修理订单应成功入队")
	}
	if len(c.afterResponse) == 0 {
		t.Fatal("排队下单必须在回包后补一条 4/22：不发的话客户端要退出重连才进修理中")
	}
	// 线上由 sendRequestResult 在回包写出后执行这些回调；这里手工跑一遍。
	for _, fn := range c.afterResponse {
		fn()
	}

	// 同一个回调里还有一条系统聊天（24/27），顺序不保证，所以按序找到 4/22 为止。
	var env *proto.Envelope
	for i := 0; i < 4; i++ {
		e := awaitPushEnvelope(t, peer)
		if e.Main == 4 && e.Sub == 22 {
			env = e
			break
		}
	}
	if env == nil {
		t.Fatal("下单回包之后始终没收到 4/22：客户端不会进 kItemState_Repairing")
	}
	tree, _, err := proto.DecodeValue(env.Body, 0, srv.Table())
	if err != nil {
		t.Fatalf("4/22 推送体解码失败: %v", err)
	}
	if tree.Kind != proto.KindArgs || tree.Code == nil || *tree.Code != "524" {
		t.Fatalf("工坊 4/22 的 args code 必须是 524，实得 %+v", tree.Code)
	}
	if len(tree.Items) != 1 || tree.Items[0].Kind != proto.KindObj {
		t.Fatalf("args 应恰好带 1 件物品树，实得 %d 项", len(tree.Items))
	}
	item := tree.Items[0]
	idx := fieldIdxByCS(item.Class, "bkmnbppciao")
	if idx < 0 || idx >= len(item.Fields) {
		t.Fatalf("物品树 %s 没有时效槽位 bkmnbppciao", item.Class)
	}
	lock := item.Fields[idx].Val
	if lock == nil || lock.Kind != proto.KindObj {
		t.Fatal("这条 4/22 必须带时效对象：不带的话客户端只当普通物品更新，面板不会变修理中")
	}
	f1 := lock.Field(1)
	if got := f1.U32(); got != 1 {
		t.Fatalf("时效对象 f1 必须是 1（有时效），实得 %d：0 会让客户端根本不进修理段", got)
	}
}

// wireFloatOf 读子件某一格的 F32 位型数值。
//
// 耐久这类字段在线上是 U32 位型（fieldOpAt = U32，装的是 Float32bits），不是
// 原生 FLOAT，所以不能用 snapshotFloat 那条走 Val.Float 的路。缺字段返回 -1，
// 让调用方的 `got < 0.999` 这类断言直接失败——返回 NaN 会被比较运算符静默放过。
func wireFloatOf(v *proto.Value, name string) float64 {
	if v == nil {
		return -1
	}
	i := fieldIdxByCS(v.Class, name)
	if i < 0 || i >= v.FieldCount() {
		return -1
	}
	return float64(proto.F32FromBits(v.Fields[i].U32()))
}

// workshopLockInTree 取一棵已解码物品树里的时效对象，没有（null）则返回 nil。
//
// 与 workshopLockOf 的区别：那个吃 store.Item 走 ItemToTree，守的是"通用下发"；
// 这个吃已经解开的下发字节，守的是"这一帧到底带没带锁"。
func workshopLockInTree(t *testing.T, tree *proto.Value) *proto.Value {
	t.Helper()
	if tree == nil || tree.Kind != proto.KindObj {
		t.Fatalf("物品树不是 obj，实得 %v", tree)
	}
	idx := fieldIdxByCS(tree.Class, "bkmnbppciao")
	if idx < 0 || idx >= tree.FieldCount() {
		t.Fatalf("物品树 %s 没有时效槽位 bkmnbppciao", tree.Class)
	}
	if v := tree.Fields[idx].Val; v != nil && v.Kind == proto.KindObj {
		return v
	}
	return nil
}

// itemStamp 取物品树上的版本戳（csCreated）。客户端按它决定要不要采纳这棵树。
func itemStamp(t *testing.T, tree *proto.Value) uint64 {
	t.Helper()
	if tree == nil || tree.Kind != proto.KindObj {
		t.Fatalf("物品树不是 obj，实得 %v", tree)
	}
	idx := fieldIdxByCS(tree.Class, csCreated)
	if idx < 0 || idx >= tree.FieldCount() {
		t.Fatalf("物品树 %s 没有版本戳 %s", tree.Class, csCreated)
	}
	return u64Of(tree.Fields[idx].Num)
}

// TestWorkshopOrderReplyCarriesNoLock 守"下单回包不带锁，锁只由 4/22 送进去"。
//
// 我们原来回包和 4/22 两处都带，实机证明是伤的：2026-11-12 00:45:53 修一根
// 0.9992 的竿，回包带锁（605B），客户端当场弹「发生内部错误」并且**没有下发
// 21/4**（本该在 +1.19 秒补上的支付确认帧没来，直接跳去 17/4 拆卸）；同一晚
// 00:46:52 换摩擦片那单回包同样带锁却照常补了 21/4（f3=1463，正是服务端扣的钱）。
// 差别在"回包里这件东西有没有真的变"——同型号修理排的是队，回包内容和下单前
// 一模一样，再叠一把"已经在修"的锁就自相矛盾。
//
// 摘锁之后 01:01:38 那单当场拿到了 01:01:40 的 21/4，握手正常。
//
// 这里守三件事，缺一条都会退回那个弹窗：
//  1. 下单（21/4）回包**不带**锁（null）；
//  2. 同一单的 4/22 推送**必须带**锁且 f1=1——它是进 kItemState_Repairing 的唯一通道；
//  3. 重复下单走幂等重放，回包同样**不带**锁（树没变化时更不能带）。
func TestWorkshopOrderReplyCarriesNoLock(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}
	rod, partID := seedWornWorkshopRod(t, srv, db, .4, .5)
	req := snapshotWorkshopRequestPartPayment(rod.GUID, 4, partID, 0, false)

	sess, peer := newTunnelProbe(t, srv)
	c := &Ctx{Sess: sess, Main: 21, Sub: 4, Tree: req, DecodeOK: true, Log: logging.Discard()}
	body, ok := hWorkshopOrder(c)
	if !ok || len(body) == 0 {
		t.Fatal("银币修理订单应成功入队")
	}
	reply, _, derr := proto.DecodeValue(body, 0, srv.Table())
	if derr != nil {
		t.Fatalf("解码 21/4 回包失败: %v", derr)
	}
	if lock := workshopLockInTree(t, reply); lock != nil {
		t.Fatal("下单回包不能带时效对象：树没变还叠一把锁，客户端当场弹" +
			"「发生内部错误」且不下发后续确认帧")
	}

	// 同一棵树的 4/22 必须留着锁。
	for _, fn := range c.afterResponse {
		fn()
	}
	var pushed *proto.Value
	for i := 0; i < 4; i++ {
		e := awaitPushEnvelope(t, peer)
		if e.Main != 4 || e.Sub != 22 {
			continue
		}
		v, _, perr := proto.DecodeValue(e.Body, 0, srv.Table())
		if perr != nil {
			t.Fatalf("解码 4/22 失败: %v", perr)
		}
		pushed = v.ArgItem(0)
		break
	}
	if pushed == nil {
		t.Fatal("下单回包之后没收到 4/22")
	}
	lock := workshopLockInTree(t, pushed)
	if lock == nil {
		t.Fatal("4/22 必须带时效对象：摘锁只能摘回包那一份，推送这份是进修理中的唯一通道")
	}
	f1 := lock.Field(1)
	if got := f1.U32(); got != 1 {
		t.Fatalf("4/22 的时效对象 f1 必须是 1（有时效），实得 %d", got)
	}

	// 重复下单（客户端补发）走幂等重放：树没变化，回包也不能带锁。
	c4 := &Ctx{Sess: sess, Main: 21, Sub: 4, Tree: req, DecodeOK: true, Log: logging.Discard()}
	body4, ok4 := hWorkshopOrder(c4)
	if !ok4 || len(body4) == 0 {
		t.Fatal("重复下单应幂等成功")
	}
	reply4, _, derr4 := proto.DecodeValue(body4, 0, srv.Table())
	if derr4 != nil {
		t.Fatalf("解码重复下单回包失败: %v", derr4)
	}
	if workshopLockInTree(t, reply4) != nil {
		t.Fatal("幂等重放的回包不能带时效对象：内容和下单前一模一样")
	}
}

// TestWorkshopRodBodyRepairOnNearlyNewGear 守"修几乎全新的装备不能报错"。
//
// 2026-11-12 00:27 实机：玩家在工坊点「修理」（不是换零件），客户端弹
// 「发生内部错误」，日志里是
//
//	WARN "工坊换件：银币价格为 0，拒绝零金额交易" item=a1db7046
//	TRACE 回(兜底) cmd=21/3 kind=args len=7      ← 空 args
//
// 请求体解码出来是 args(503) 只有一项：`{BYTE 36, STR "rodblank_syb_fortuna_s68mh"}`。
// **36 是「竿坯」品类**（item_def 里 rodblank_* 全是 36），它就是竿的本体那一格。
//
// 真因不是品类，是**计价四舍五入到 0**：修理费按 `round((1-耐久)*500)` 折，
// 那根竿耐久 0.9992 → 折出 0.4 银币 → 舍成 0；但"确实要修"（didWork）为真，
// 于是越过幂等分支，撞上"拒绝零金额交易"的守卫 → 返回空 args → 客户端弹错。
// 症状因此极难复现：只有耐久落在 (0.999, 1.0) 这个窄区间的装备会中招。
//
// 这里守三件事：
//  1. 0.9992 的竿坯修理必须被接受（回非空物品树，不落兜底空 args）；
//  2. 真·零磨损（耐久 1.0）仍走幂等成功，不能被当新订单再排一次队；
//  3. 到期落地后竿坯与本体耐久都要回到 1.0——竿的本体耐久是竿坯那一格的投影，
//     只修子件不改本体，玩家会看到"修完了但耐久没动"。
func TestWorkshopRodBodyRepairOnNearlyNewGear(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}
	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	fields := snapshotFields(t, rod.FieldsJSON)
	blank, blankDef := snapshotPart(t, srv, fields, rodCatBlank)
	// 玩家那根竿就是这个数：磨损 0.08%，折出来的钱是 0。
	const nearlyNew = 0.9992
	if !setWireFloat(blank.Fields, blank.Class, fieldIdxByCS(blank.Class, csDurability), nearlyNew) {
		t.Fatal("设置竿坯耐久失败")
	}
	raw := snapshotJSON(t, fields)
	if err := db.UpdateItemFields(ctx, rod.GUID, raw, rod.Qty, nearlyNew, rod.Workmanship, rod.RigGUID); err != nil {
		t.Fatalf("预置竿坯耐久失败: %v", err)
	}

	body, ok := workshopOrderOnce(srv, rod.GUID, uint32(rodCatBlank), blankDef.ItemID, false)
	if !ok || len(body) == 0 {
		t.Fatal("修 0.9992 的竿坯被拒：客户端会弹「发生内部错误」。零金额只该拒绝换件，不该拒绝纯修理")
	}
	// 官服口径下"修理"= 换一个同型号新件，收 (新件价 − 旧件残值)。
	// 几乎全新的旧件残值高、实付少，但不会再出现"1 银都不到"的零金额：
	// 客户端界面上写的也是这个差额，两边必须一分不差。
	job := mustWorkshopJob(t, db, rod.GUID)
	wantPaid := workshopPageSilver(t, srv, rod, rodCatBlank)
	if wantPaid <= 0 {
		t.Fatalf("竿坯页面价应为正数，实得 %d", wantPaid)
	}
	if job.SilverPaid != wantPaid {
		t.Fatalf("页面报价 %d，实收 %d", wantPaid, job.SilverPaid)
	}

	expireWorkshopJobs(t, db, uid)
	if settled := settleWorkshopForTest(t, srv, uid); len(settled) == 0 {
		t.Fatal("到期应落地这件修理")
	}
	after := mustItem(t, db, rod.GUID)
	af, ferr := FieldsFromJSON(after.FieldsJSON)
	if ferr != nil {
		t.Fatal(ferr)
	}
	fixed, _ := snapshotPart(t, srv, af, rodCatBlank)
	if got := wireFloatOf(fixed, csDurability); got < 0.999 {
		t.Fatalf("竿坯耐久应被修满，实得 %.4f", got)
	}
	if after.Durability < 0.999 {
		t.Fatalf("本体耐久应随竿坯一起回满，实得 %.4f：只修子件不改本体，玩家会看到「修完了耐久没动」", after.Durability)
	}
}

// TestWorkshopQueuedOrderReturnsBumpedStamp 守"排队回包的版本戳必须是新的"。
//
// 客户端按 csCreated（创建 Ticks）决定要不要采纳一次物品更新：戳没比它手里
// 的大就整包丢弃——连 f24 时效对象一起丢。2026-11-11 23:41 实机就是这个症状：
// 下单回包的 f21 = 639300029129805947，与下单前库里的值一模一样，而任务快照
// 里已经是 639300084682957096，玩家看到的是"界面毫无反应、退出重连才进维修中"。
//
// 这棵树的内容必须仍是**磨损态**（修理还没完成，不能提前把耐久改回去），
// 只有版本戳要换成 bump 后的值，而且要和任务快照里的那一个完全相等——
// 不一致的话到期落地那次更新会被客户端当成"没变"再丢一次。
func TestWorkshopQueuedOrderReturnsBumpedStamp(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}
	rod, partID := seedWornWorkshopRod(t, srv, db, .4, .5)
	idx := fieldIdxByCS(rod.WireClass, csCreated)
	if idx < 0 {
		t.Fatal("竿类找不到创建 Ticks 字段（csCreated）")
	}
	before, err := FieldsFromJSON(rod.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	oldStamp := u64Of(before[idx].Num)

	body, ok := workshopOrderOnce(srv, rod.GUID, 4, partID, false)
	if !ok || len(body) == 0 {
		t.Fatal("银币修理应成功入队")
	}
	tree, _, derr := proto.DecodeValue(body, 0, srv.Table())
	if derr != nil {
		t.Fatalf("解码回包失败: %v", derr)
	}
	stampField := tree.Field(idx)
	if stampField.Num == nil {
		t.Fatalf("回包第 %d 个字段应是版本戳，实得空", idx)
	}
	got := stampField.U64()

	job := mustWorkshopJob(t, db, rod.GUID)
	jobFields, jerr := FieldsFromJSON(job.FieldsJSON)
	if jerr != nil {
		t.Fatal(jerr)
	}
	want := u64Of(jobFields[idx].Num)
	if got != want {
		t.Fatalf("回包版本戳应与任务快照一致(%d)，实得 %d：不一致会让到期落地那次更新被客户端忽略", want, got)
	}
	if got <= oldStamp {
		t.Fatalf("回包版本戳必须严格大于下单前的值（旧 %d，实得 %d），否则客户端整包丢弃这次更新", oldStamp, got)
	}
}

// TestWorkshopSettleBumpsStampBeyondQueued 守"到期落地再抬一档版本戳"。
//
// 任务快照里的戳正是下单那一刻发给客户端的值。原样写回去，客户端会认为
// "没有更新"，修好的东西会一直挂在"修理中"。落地必须严格大于它。
func TestWorkshopSettleBumpsStampBeyondQueued(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}
	rod, partID := seedWornWorkshopRod(t, srv, db, .4, .5)
	idx := fieldIdxByCS(rod.WireClass, csCreated)
	if idx < 0 {
		t.Fatal("竿类找不到创建 Ticks 字段（csCreated）")
	}
	if body, ok := workshopOrderOnce(srv, rod.GUID, 4, partID, false); !ok || len(body) == 0 {
		t.Fatal("银币修理应成功入队")
	}
	job := mustWorkshopJob(t, db, rod.GUID)
	jobFields, jerr := FieldsFromJSON(job.FieldsJSON)
	if jerr != nil {
		t.Fatal(jerr)
	}
	queued := u64Of(jobFields[idx].Num)

	expireWorkshopJobs(t, db, uid)
	if got := settleWorkshopForTest(t, srv, uid); len(got) != 1 {
		t.Fatalf("到期应结算 1 件，实得 %d", len(got))
	}
	after, err := FieldsFromJSON(mustItem(t, db, rod.GUID).FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	landed := u64Of(after[idx].Num)
	if landed <= queued {
		t.Fatalf("落地版本戳必须大于排队时下发的 %d，实得 %d：否则客户端认不出这次更新", queued, landed)
	}
}

// TestWorkshopQuotePreviewDoesNotCommit 守"21/3 只是选中预览，不落库不扣费"。
//
// 客户端每勾选/取消一个部位就重发一次 21/3（条目累积），真正下单是 21/4。
// 以前把 21/3 当成交，第一帧就入队扣费，后面几帧全都撞在办任务分支被当成幂等
// 重放吞掉——玩家勾 3 个部位、界面写着 2176 银，实际只修 1 个、只扣 8 银。
// 这条用例把"21/3 不许有副作用"钉死。
func TestWorkshopQuotePreviewDoesNotCommit(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}
	rod, partID := seedWornWorkshopRod(t, srv, db, .4, .5)

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 3,
		Tree:     snapshotWorkshopRequestPart(rod.GUID, 4, partID, 0),
		DecodeOK: true, Log: logging.Discard(),
	}
	body, ok := hWorkshopQuote(c)
	if !ok || len(body) == 0 {
		t.Fatal("21/3 预览应成功回当前物品树")
	}
	if v, _, derr := proto.DecodeValue(body, 0, srv.Table()); derr != nil || v == nil ||
		v.Kind != proto.KindObj {
		t.Fatalf("21/3 回包应是一棵物品树: err=%v kind=%v", derr, v)
	}
	if n := workshopJobRows(t, db, rod.GUID); n != 0 {
		t.Fatalf("21/3 不能登记在办任务，实得 %d 行", n)
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Silver != 10000 {
		t.Fatalf("21/3 不能扣费，余额 %d", profile.Silver)
	}
	fresh := mustItem(t, db, rod.GUID)
	fields := snapshotFields(t, fresh.FieldsJSON)
	part, _ := snapshotPart(t, srv, fields, 4)
	if got := snapshotFloat(part.Fields, part.Class, csDurability); math.Abs(got-.4) > 1e-6 {
		t.Fatalf("21/3 不能改耐久，竿柄实得 %v", got)
	}
}

// TestWorkshopOrderCommitsEverySelectedPart 守"一帧 21/4 里的条目全部生效"。
//
// 实测客户端多选会连发 n=1、n=2、n=3 三帧 21/3，最后只发一帧 21/4 带总价
// （2026-11-12 23:07:55 n=3 cost=2176）。下单必须按整张单一次算清：三个部位
// 都修、按三份计价，而不是只认第一条。
func TestWorkshopOrderCommitsEverySelectedPart(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 100000, 0, 0); err != nil {
		t.Fatal(err)
	}
	cats := []int64{rodCatBlank, 4, 5}
	rod, entries := seedWornWorkshopRodForCats(t, srv, db, cats, .5, .5)
	if len(entries) != 3 {
		t.Fatalf("应凑齐 3 个部位，实得 %d", len(entries))
	}

	c := &Ctx{
		Sess: newProbeSess(srv), Main: 21, Sub: 4,
		Tree:     snapshotWorkshopRequestParts(rod.GUID, entries, 0, false),
		DecodeOK: true, Log: logging.Discard(),
	}
	body, ok := hWorkshopOrder(c)
	if !ok || len(body) == 0 {
		t.Fatal("三个部位的下单应成功")
	}

	// 三个部位各收 (新件价 − 旧件残值)，合计就是客户端界面上的那个数。
	wantPaid := workshopPageSilver(t, srv, rod, rodCatBlank, 4, 5)
	profile, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if paid := int64(100000) - profile.Silver; paid != wantPaid {
		t.Fatalf("三个部位各 0.5 磨损应合计 %d 银，实扣 %d", wantPaid, paid)
	}
	job := mustWorkshopJob(t, db, rod.GUID)
	if job.SilverPaid != wantPaid {
		t.Fatalf("任务记录已付 %d，期望 %d", job.SilverPaid, wantPaid)
	}
	if job.Parts != 3 {
		t.Fatalf("任务应记 3 个部位，实得 %d", job.Parts)
	}

	// 结算后三个部位都要满耐久，缺一个就说明有条目被丢了。
	expireWorkshopJobs(t, db, uid)
	if got := settleWorkshopForTest(t, srv, uid); len(got) != 1 {
		t.Fatalf("到期应结算 1 件，实得 %d", len(got))
	}
	after := snapshotFields(t, mustItem(t, db, rod.GUID).FieldsJSON)
	for _, cat := range cats {
		part, _ := snapshotPart(t, srv, after, cat)
		if got := snapshotFloat(part.Fields, part.Class, csDurability); got < 0.999 {
			t.Fatalf("部位 %d 结算后应满耐久，实得 %v", cat, got)
		}
	}
}

// TestWorkshopVersionStampStrictlyIncreases 守"工坊这一串下发的 csCreated 严格递增"。
//
// 2026-11-12 01:31 实机：多部位下单成功了，但面板要**退出重连**才变成"修理中"。
// 把那一单的四帧全解出来，版本戳是这样走的：
//
//	01:31:22.099  21/3 回包（勾选预览）   f21=…462105  ← item 行的旧戳
//	01:31:23.006  21/4 回包（下单）       f21=…955954  ← bump 后的新戳
//	01:31:23.011  21/3 回包（下单后又发） f21=…462105  ← **回退**
//	01:31:25.506  4/22 推送（延迟 2500ms） f21=…955954 ← 与 21/4 回包同值
//
// 客户端按 csCreated 决定要不要采纳一棵物品树：4/22 那个值它片刻前刚从 21/4
// 回包拿过，于是判旧丢弃，连 f24 那把锁一起丢；重连走 4/5 全量背包、不参与版本
// 比较，才显示"修理中"。官服同一条链路（logs/nc.jsonl s2c#334→#336→#337→#338）
// 帧帧递增、从不持平也从不回退，这里照抄。
//
// 所以下单路径上的四棵树必须一段比一段新：预览 < 下单回包 < 下单后的预览 < 4/22。
func TestWorkshopVersionStampStrictlyIncreases(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBalance(ctx, uid, 10000, 0, 0); err != nil {
		t.Fatal(err)
	}
	rod, partID := seedWornWorkshopRod(t, srv, db, .4, .5)
	sess, peer := newTunnelProbe(t, srv)

	stamp := func(body []byte) uint64 {
		t.Helper()
		v, _, err := proto.DecodeValue(body, 0, srv.Table())
		if err != nil || v == nil || v.Kind != proto.KindObj {
			t.Fatalf("回包不是一棵物品树: err=%v kind=%v", err, v)
		}
		return itemStamp(t, v)
	}
	preview := func() uint64 {
		t.Helper()
		c := &Ctx{
			Sess: sess, Main: 21, Sub: 3,
			Tree:     snapshotWorkshopRequestPart(rod.GUID, 4, partID, 0),
			DecodeOK: true, Log: logging.Discard(),
		}
		body, ok := hWorkshopQuote(c)
		if !ok || len(body) == 0 {
			t.Fatal("21/3 预览应成功回当前物品树")
		}
		return stamp(body)
	}

	preOrder := preview()

	order := &Ctx{
		Sess: sess, Main: 21, Sub: 4,
		Tree:     snapshotWorkshopRequestPart(rod.GUID, 4, partID, 0),
		DecodeOK: true, Log: logging.Discard(),
	}
	orderBody, ok := hWorkshopOrder(order)
	if !ok || len(orderBody) == 0 {
		t.Fatal("下单应成功入队")
	}
	onOrder := stamp(orderBody)
	if onOrder <= preOrder {
		t.Fatalf("下单回包必须比勾选预览更新：预览 %d → 下单 %d", preOrder, onOrder)
	}

	// 客户端下完单会补一帧 21/3（01:31:23.009 实测），这一帧不能把戳拉回去。
	afterOrder := preview()
	if afterOrder <= onOrder {
		t.Fatalf("下单后的 21/3 预览必须比下单回包更新，否则客户端会把它整包丢掉："+
			"下单 %d → 预览 %d", onOrder, afterOrder)
	}

	// 4/22 是延迟发的，必须带一个比此前所有下发都新的戳，否则一样被丢掉。
	for _, fn := range order.afterResponse {
		fn()
	}
	var pushed uint64
	for i := 0; i < 4; i++ {
		e := awaitPushEnvelope(t, peer)
		if e.Main != 4 || e.Sub != 22 {
			continue
		}
		v, _, perr := proto.DecodeValue(e.Body, 0, srv.Table())
		if perr != nil || v == nil {
			t.Fatalf("解码 4/22 失败: %v", perr)
		}
		item := v.ArgItem(0)
		if item == nil || item.Kind != proto.KindObj {
			t.Fatal("4/22 里应有一件物品")
		}
		pushed = itemStamp(t, item)
		break
	}
	if pushed == 0 {
		t.Fatal("下单回包之后没收到 4/22")
	}
	if pushed <= afterOrder {
		t.Fatalf("4/22 必须比此前所有下发都新，否则客户端按 csCreated 判旧丢弃、"+
			"锁进不去、面板要重连才变修理中：预览 %d → 下单 %d → 预览 %d → 推送 %d",
			preOrder, onOrder, afterOrder, pushed)
	}
}

// TestWorkshopRepairSecondsAreGameHours 钉住"工时是**游戏**小时"这条单位铁律。
//
// 2026-11-12 用户实机指出：客户端把 `hpopgbjohpk.f3` 当游戏内小时，我们却按
// 现实小时入队 —— 界面写「6 小时」就要干等 6 小时现实，而按 24 倍速只该等
// 15 分钟。抓包也同向：官服那一单 f3=2、回包锁 RemainMin=120（= 2 游戏小时）。
func TestWorkshopRepairSecondsAreGameHours(t *testing.T) {
	// 与世界时钟同源：世界时钟 24 倍速 = 1 现实秒走 0.4 游戏分钟，
	// 所以 1 游戏小时 = 60 / 0.4 = 150 现实秒。
	if want := 60 / gameMinutesPerRealSecond; math.Abs(workshopGameHourSeconds-want) > 1e-9 {
		t.Fatalf("1 游戏小时应折 %.1f 现实秒，实得 %.1f", want, workshopGameHourSeconds)
	}
	cases := []struct {
		hours uint32
		want  float64
		note  string
	}{
		{2, 300, "官服齿轮档：2 游戏小时 = 5 现实分钟，对应锁 RemainMin=120 游戏分钟"},
		{6, 900, "用户实机看到的 6 小时 = 15 现实分钟"},
		{24, 3600, "竿件默认档 24 游戏小时 = 1 现实小时"},
		{36, 5400, "轴承档 36 游戏小时 = 1.5 现实小时"},
		{0, 150, "0 按 1 兜底，不能退化成即时完成"},
	}
	for _, tc := range cases {
		if got := workshopRepairSeconds(tc.hours); got != tc.want {
			t.Fatalf("workshopRepairSeconds(%d)=%.0f，期望 %.0f 现实秒（%s）",
				tc.hours, got, tc.want, tc.note)
		}
	}
}
