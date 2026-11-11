package gameserver

import (
	"math"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

// gbDeposit 测试用的投放封装：坐标固定在 (100,100) 附近同一个 5 米格。
func gbDeposit(t *testing.T, s *Server, uid int64, itemID string, portions float64, channel, txn string) {
	t.Helper()
	s.groundbaitDeposit(t.Context(), uid, "gb-test-loc", 100, 100, true, 7, itemID, portions, channel, txn, "")
}

func gbRecordRecipe(t *testing.T, s *Server, guid string, mats []string) {
	t.Helper()
	tx, err := s.DB.SQL.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := groundbaitRecordInstanceMaterialsTx(t.Context(), tx, guid, mats); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func gbSpotState(t *testing.T, s *Server, uid int64) groundbaitSpotState {
	t.Helper()
	return s.groundbaitSpot(t.Context(), uid, "gb-test-loc",
		int64(100/groundbaitCellSizeM), int64(100/groundbaitCellSizeM))
}

// gbAge 把某格所有批次往前推 gameHours 游戏小时（改投放时的游戏钟读数）。
func gbAge(t *testing.T, s *Server, uid int64, gameHours float64) {
	t.Helper()
	_, err := s.DB.SQL.Exec(`UPDATE groundbait_deposit SET deposit_hour=deposit_hour-?
		WHERE uid=? AND loc='gb-test-loc'`, gameHours, uid)
	if err != nil {
		t.Fatalf(" aging 失败: %v", err)
	}
}

// gbDepositCount 数某一发（call_id 结尾）在窝账里记了几批。
func gbDepositCount(t *testing.T, s *Server, uid int64, callSuffix string) int {
	t.Helper()
	var n int
	if err := s.DB.SQL.QueryRow(`SELECT COUNT(*) FROM groundbait_deposit WHERE uid=? AND txn_guid LIKE ?`,
		uid, "%:"+callSuffix).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func gbCount(t *testing.T, s *Server, uid int64) int {
	t.Helper()
	var n int
	if err := s.DB.SQL.QueryRow(`SELECT COUNT(*) FROM groundbait_deposit WHERE uid=?`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestGroundbaitLedgerPortionTimeAndOverdose(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	const uid = int64(1000001)

	gbDeposit(t, s, uid, "feed_karp", 10, "catapult", "gb:t1")
	st := gbSpotState(t, s, uid)
	if got := st.units; got < 9.99 || got > 10.01 {
		t.Fatalf("10 份窝料应得 N=10，实得 %.3f", got)
	}
	if got := st.hoursLeft; got < 5.99 || got > 6.01 {
		t.Fatalf("10 份窝料应得 T=6 游戏小时（产品口径：10 份=6 小时），实得 %.3f", got)
	}
	if p := s.groundbaitOverdoseP(st.units); p != 1 {
		t.Fatalf("15 份以内不该死窝，实得 P=%.2f", p)
	}
	if gate := s.groundbaitBiteGate(ctx, uid, "gb-test-loc", 100, 100, true, 7, 1); gate != 1 {
		t.Fatalf("正常窝的咬钩闸门应为 1，实得 %.2f", gate)
	}

	// 幂等：同一条上报重复投 3 次，只算一次。
	gbDeposit(t, s, uid, "feed_karp", 10, "catapult", "gb:t1")
	gbDeposit(t, s, uid, "feed_karp", 10, "catapult", "gb:t1")
	if n := gbCount(t, s, uid); n != 1 {
		t.Fatalf("同 txn_guid 重复上报应只入账一次，实得 %d 批", n)
	}

	// 死窝：再堆 12 份，N=22 → 开口率归零。
	gbDeposit(t, s, uid, "feed_karp", 12, "spod", "gb:t2")
	st2 := gbSpotState(t, s, uid)
	// 上限卡死 22（重复上报不能多算）；下限留一点余量：读窝的每一次都会推进
	// 玩家游戏钟，窝按比例稀释，整包并发跑时几次数据库往返就是秒级误差。
	if st2.units < 21.9 || st2.units > 22.01 {
		t.Fatalf("堆完应为 N=22，实得 %.3f", st2.units)
	}
	if gate := s.groundbaitBiteGate(ctx, uid, "gb-test-loc", 100, 100, true, 7, 1); gate > 0.001 {
		t.Fatalf("22 份以上这个格子应完全不开口，实得 P=%.3f", gate)
	}
	// 路亚族豁免死窝闸门（拟饵不是被窝料喂出来的口）。
	if gate := s.groundbaitBiteGate(ctx, uid, "gb-test-loc", 100, 100, true, 7, rigSpinningTypeTest()); gate == 0 {
		t.Fatal("路亚族应豁免死窝闸门")
	}

	// 窝时到点自动解除死窝：整格过期后被清掉。
	gbAge(t, s, uid, 7)
	st3 := gbSpotState(t, s, uid)
	if st3.units != 0 {
		t.Fatalf("超过 6 游戏小时后窝应归零，实得 %.3f", st3.units)
	}
	if n := gbCount(t, s, uid); n != 0 {
		t.Fatalf("过期批次应被清理，实得 %d 行", n)
	}
	if gate := s.groundbaitBiteGate(ctx, uid, "gb-test-loc", 100, 100, true, 7, 1); gate != 1 {
		t.Fatalf("死窝应随窝时耗尽自动解除，实得 %.2f", gate)
	}
}

func rigSpinningTypeTest() int64 {
	for rt := range rigTypeLureFamily {
		return rt
	}
	return 50
}

// 掠食者不蹭窝：冻结鱼种权重一动不动；对口鱼种被抬起来。
func TestGroundbaitWeightRaisesPreyNotPredator(t *testing.T) {
	s, _ := newRigTestServer(t)
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	const uid = int64(1000001)
	gbDeposit(t, s, uid, "feed_karp", 8, "feeder", "gb:w1")

	c := fishRollContext{
		uid: uid, loc: "gb-test-loc", x: 100, z: 100, hasPos: true, spot: 7,
		now: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC), rigType: 1, depthCM: 100,
		cache:   newFishingRollCache(),
		weather: fishingWeather{known: true, kind: fishingWeatherClear, airTemp: 18, previousAirTemp: 18, wind: 3},
	}
	keys := []string{"carp", "n.pike", "unknown.fish"}
	wts := []float64{100, 100, 100}
	s.applyGroundbaitWeight(t.Context(), &c, keys, wts)

	if wts[0] <= 100 {
		t.Fatalf("喂鲤鱼的窝料应抬高普通鲤鱼权重，实得 %.2f", wts[0])
	}
	if wts[1] != 100 {
		t.Fatalf("掠食性鱼种（常见狗鱼）必须 x1.00 不蹭窝，实得 %.2f", wts[1])
	}
	if wts[2] != 100 {
		t.Fatalf("无名册鱼种不该被窝料改动，实得 %.2f", wts[2])
	}
	if wts[0] > 100*2.6001 {
		t.Fatalf("窝料乘子必须封顶 2.60，实得 %.3f", wts[0]/100)
	}
}

// 天气软化：烂天气里的乘子被抬起来，但绝不能超过该鱼好天气的基线。
func TestGroundbaitWeatherSofteningLiftsWithinBaseline(t *testing.T) {
	s, _ := newRigTestServer(t)
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	const uid = int64(1000001)
	gbDeposit(t, s, uid, "feed_karp", 8, "feeder", "gb:s1")

	mult := func(c fishRollContext) (float64, float64) {
		keys := []string{"carp"}
		on := []float64{100}
		s.applyGroundbaitWeight(t.Context(), &c, keys, on)
		setWeatherTestTunable(t, s, "groundbait_soft_beta", "0")
		base := []float64{100}
		s.applyGroundbaitWeight(t.Context(), &c, keys, base)
		setWeatherTestTunable(t, s, "groundbait_soft_beta", "0.45")
		return on[0] / 100, base[0] / 100
	}

	// 暖/冷必须落在同一游戏时刻：游戏历 24 倍速，若暖场景取真实时间、冷场景写死
	// 日期，比较的就是季节而不是天气。这里遍历四季，确认「软化抬高坏天气、但不
	// 超过同季节好天气」在四季都成立，而不是挑一个季节碰运气。
	for _, season := range []struct {
		name string
		day  int64
	}{
		{"春", 90}, {"夏", 180}, {"秋", 270}, {"冬", 350},
	} {
		now := time.Unix(worldDayEpochUnix+season.day*24*realSecPerGameHour+12*realSecPerGameHour, 0)
		warm := fishRollContext{uid: uid, loc: "gb-test-loc", x: 100, z: 100, hasPos: true, spot: 7,
			now: now, rigType: 1, depthCM: 100, cache: newFishingRollCache(),
			weather: fishingWeather{known: true, kind: fishingWeatherClear, airTemp: 18, previousAirTemp: 18, wind: 3}}
		cold := warm
		cold.weather = fishingWeather{known: true, kind: fishingWeatherHeavyRain, airTemp: -4, previousAirTemp: -4, wind: 6}

		hotM, hotBase := mult(warm)
		coldM, coldBase := mult(cold)
		if coldM <= coldBase {
			t.Fatalf("%s: 烂天气里软化应把乘子抬起来：软化后 %.3f vs 关闭 %.3f", season.name, coldM, coldBase)
		}
		if hotM < hotBase-1e-9 {
			t.Fatalf("%s: 软化不能把天气带来的优势吃掉：暖 %.3f < 关软化 %.3f", season.name, hotM, hotBase)
		}
		if coldM > hotM {
			t.Fatalf("%s: 「烂天气 + 一锅好料」不能打过「好天气」：冷 %.3f > 暖 %.3f", season.name, coldM, hotM)
		}
	}
}

// 开关关掉时必须与上线窝料系统之前一模一样。
func TestGroundbaitDisabledLeavesRollUntouched(t *testing.T) {
	s, _ := newRigTestServer(t)
	setWeatherTestTunable(t, s, "groundbait_enabled", "0")
	const uid = int64(1000001)
	gbDeposit(t, s, uid, "feed_karp", 30, "feeder", "gb:off")
	if n := gbCount(t, s, uid); n != 0 {
		t.Fatalf("开关关闭时不该记账，实得 %d 批", n)
	}
	c := fishRollContext{uid: uid, loc: "gb-test-loc", x: 100, z: 100, hasPos: true, spot: 7,
		now: time.Now(), rigType: 1, depthCM: 100, cache: newFishingRollCache()}
	keys := []string{"carp"}
	wts := []float64{100}
	s.applyGroundbaitWeight(t.Context(), &c, keys, wts)
	if wts[0] != 100 {
		t.Fatalf("开关关闭时权重不该变，实得 %.2f", wts[0])
	}
	if gate := s.groundbaitBiteGate(t.Context(), uid, "gb-test-loc", 100, 100, true, 7, 1); gate != 1 {
		t.Fatalf("开关关闭时闸门应为 1，实得 %.2f", gate)
	}
}

// 弹弓通道：抛一发就得有一笔 catapult 投放落进落点格子。
func TestGroundbaitCatapultThrowDepositsSpot(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	const uid = int64(1000001)

	hand, err := s.CreateItem(ctx, uid, "wh_pc_gb", "backpack", 1)
	if err != nil {
		t.Fatalf("创建弹弓失败: %v", err)
	}
	// 弹药格挂的是窝料（cat46），必须走真实装填拆实例路径。
	stack, err := s.CreateItem(ctx, uid, "feedcompleted", "backpack", 1)
	if err != nil {
		t.Fatalf("创建窝料失败: %v", err)
	}
	s.setStackCount(ctx, stack, 5)
	gbRecordRecipe(t, s, stack.GUID, []string{"mix_groundpeas", "feedadds_corn", "aroma_vanila"})
	loaded, _ := s.materializeHanditemChild(ctx, uid, hand.GUID, stack.GUID)
	if got := s.groundbaitInstanceMaterials(ctx, loaded); len(got) != 3 {
		t.Fatalf("装填子实例没有继承配方: %v", got)
	}
	s.setHanditemLoadedRef(ctx, hand.GUID, loaded)

	st := s.stateFor(uid)
	st.mu.Lock()
	st.Loc, st.CastPosX, st.CastPosZ, st.HasCastPos, st.Spot = "gb-test-loc", 100, 100, true, 7
	st.mu.Unlock()

	sess, _ := newTunnelProbe(t, s)
	req := rigRequestTree(t, proto.Guid(hand.GUID),
		wiregen.MustMakeObj("olieodeoaof", map[int]*proto.Value{0: proto.F32(12)}))
	hctx := &Ctx{Sess: sess, Main: 13, Sub: 5, CallID: 9001, Tree: req,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()}
	if _, ok := hDynamicHanditemThrow(hctx); !ok {
		t.Fatal("13/5 未处理")
	}
	gate := s.groundbaitSpot(ctx, uid, "gb-test-loc", int64(100/groundbaitCellSizeM), int64(100/groundbaitCellSizeM))
	// 窝料游戏钟按整秒推进；投放后立即读取时可能已经走过一个 tick，
	// 默认 1 份 0.6 游戏小时会按剩余时间稀释到约 0.989。精确份数由
	// 账本字段断言，这里只要求它确实落在目标格且没有被放大。
	if gate.units <= 0 || gate.units > 1.000001 {
		t.Fatalf("弹弓一弹应投进目标窝点，实得 %.3f", gate.units)
	}
	var channel, itemID, matsJSON string
	var portions, unitsLeft float64
	if err := s.DB.SQL.QueryRowContext(ctx, `SELECT channel,item_id,materials_json,portions,units_left FROM groundbait_deposit WHERE uid=?`, uid).Scan(&channel, &itemID, &matsJSON, &portions, &unitsLeft); err != nil {
		t.Fatalf("弹弓投放没落账: %v", err)
	}
	if channel != "catapult" || itemID != "feedcompleted" {
		t.Fatalf("投放通道/物品不符: %s %s", channel, itemID)
	}
	if portions != 1 || unitsLeft != 1 {
		t.Fatalf("弹弓一弹应入账 1 份且未消耗，实得 portions=%.3f units_left=%.3f", portions, unitsLeft)
	}
	mats := groundbaitDecodeMaterials(matsJSON)
	if len(mats) != 3 || mats[0] != "mix_groundpeas" || mats[1] != "feedadds_corn" || mats[2] != "aroma_vanila" {
		t.Fatalf("弹弓窝账丢失实际用料: %v", mats)
	}
}

func TestGroundbaitRecipeFollowsSplitMountableChild(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	stack, err := s.CreateItem(ctx, uid, "feedcompleted", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	s.setStackCount(ctx, stack, 5)
	gbRecordRecipe(t, s, stack.GUID, []string{"mix_groundpeas", "feedadds_corn"})
	parent, err := s.CreateItem(ctx, uid, "wh_pc_gb", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	child, _ := s.splitMountableChild(ctx, uid, parent.GUID, stack.GUID, 2)
	if child == stack.GUID {
		t.Fatal("没有拆出新实例")
	}
	got := s.groundbaitInstanceMaterials(ctx, child)
	if len(got) != 2 || got[0] != "mix_groundpeas" || got[1] != "feedadds_corn" {
		t.Fatalf("拆分子实例没有继承配方: %v", got)
	}
}

// 食笼/PVA 通道：入水扣掉的那一份必须同时进窝料账本。
func TestGroundbaitCastDepositMapsChannels(t *testing.T) {
	s, _ := newRigTestServer(t)
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	const uid = int64(1000001)
	sess, _ := newTunnelProbe(t, s)
	st := s.stateFor(uid)
	st.mu.Lock()
	st.Loc, st.CastPosX, st.CastPosZ, st.HasCastPos, st.Spot = "gb-test-loc", 100, 100, true, 7
	st.mu.Unlock()

	s.depositCastGroundbait(sess, st, baitChange{Groundbait: []groundbaitUse{
		{ItemID: "feedcompleted", Units: 1, Category: 46},
		{ItemID: "pva_stick_m", Units: 1, Category: 30},
	}}, "rod-x:123")
	// 打窝器入水倒空，通道名单独记一条，日志里好和食笼区分。
	st.mu.Lock()
	st.RigType = rigTypeSpod
	st.mu.Unlock()
	s.depositCastGroundbait(sess, st, baitChange{Groundbait: []groundbaitUse{
		{ItemID: "feed_dry", Units: 1, Category: 46},
	}}, "rod-y:456")

	rows, err := s.DB.SQL.Query(`SELECT channel, item_id FROM groundbait_deposit ORDER BY channel`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for rows.Next() {
		var ch, it string
		if serr := rows.Scan(&ch, &it); serr != nil {
			t.Fatal(serr)
		}
		got[ch] = it
	}
	_ = rows.Close()
	if got["feeder"] != "feedcompleted" || got["pva"] != "pva_stick_m" || got["spod"] != "feed_dry" {
		t.Fatalf("通道映射错了: %+v", got)
	}
}

// 打窝竿实测抛过一次（19:38:34 落点 264.12/422.55），但服务端既没扣槽上的窝料、
// 也没记窝：打窝器不发 14/7，入水时唯一到服务端的信号是 14/3。
// 这里验 14/3 会扣一份、落到本杆真实落点格，且重复上报不再扣。
func TestGroundbaitSpodDeductsOnSplashDown(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	const uid = int64(1000001)

	rod, err := s.CreateItem(ctx, uid, "spod_10132_3_5LB", "backpack", 1)
	if err != nil {
		t.Fatalf("创建打窝竿失败: %v", err)
	}
	if err := s.DB.UpsertRig(ctx, rod.GUID, uid, rigTypeSpod, "", "{}"); err != nil {
		t.Fatal(err)
	}
	pot, err := s.CreateItem(ctx, uid, "feed_dry", "backpack", 1)
	if err != nil {
		t.Fatalf("创建窝料失败: %v", err)
	}
	s.setStackCount(ctx, pot, 2)
	// 客户端把槽 46 的窝料挂在打窝器上（实测 slot=46、extra=225）。
	if _, err := s.DB.SQL.ExecContext(ctx,
		`UPDATE item SET container='rig', slot=46, rig_guid=? WHERE guid=?`, rod.GUID, pot.GUID); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.UpsertRigComponent(ctx, rod.GUID, 46, pot.GUID, 225); err != nil {
		t.Fatal(err)
	}

	sess, _ := newTunnelProbe(t, s)
	st := s.stateFor(uid)
	st.mu.Lock()
	st.GearGuid, st.RigType = rod.GUID, rigTypeSpod
	st.Loc, st.CastPosX, st.CastPosZ, st.HasCastPos, st.Spot = "gb-test-loc", 100, 100, true, 7
	st.CastTs = time.Now()
	st.mu.Unlock()

	fall143 := func() {
		t.Helper()
		hFishingFall(&Ctx{Sess: sess, Main: 14, Sub: 3, CallID: 9301,
			Tree: rigRequestTree(t, proto.Guid(rod.GUID)), DecodeOK: true,
			ExpectResponse: false, Log: logging.Discard()})
	}
	qty := func() int64 {
		t.Helper()
		row, rerr := s.DB.GetItemByGUID(ctx, pot.GUID)
		if rerr != nil || row == nil {
			return -1
		}
		return s.stackCountOf(ctx, row)
	}

	fall143()
	if got := qty(); got != 1 {
		t.Fatalf("打窝器入水应扣掉 1 份（2→1），实得 %d", got)
	}
	var (
		ch, itemID     string
		portions       float64
		hasPos, bx, bz int64
	)
	if err := s.DB.SQL.QueryRowContext(ctx,
		`SELECT channel,item_id,portions,has_pos,bx,bz FROM groundbait_deposit WHERE uid=?`, uid).
		Scan(&ch, &itemID, &portions, &hasPos, &bx, &bz); err != nil {
		t.Fatalf("打窝器这一发没记进窝账: %v", err)
	}
	wantBX, wantBZ := groundbaitCell(100, 100)
	if ch != "spod" || itemID != "feed_dry" || portions != 1 {
		t.Fatalf("窝账应是 spod 通道 1 份 feed_dry，实得 %s / %s / %.2f", ch, itemID, portions)
	}
	if hasPos != 1 || bx != wantBX || bz != wantBZ {
		t.Fatalf("窝应记在落点格 (%d,%d)，实得 has_pos=%d (%d,%d)", wantBX, wantBZ, hasPos, bx, bz)
	}
	if got := s.groundbaitSpot(ctx, uid, "gb-test-loc", wantBX, wantBZ).units; got < 0.99 || got > 1.01 {
		t.Fatalf("打窝器那份窝应全额生效，实得 %.3f", got)
	}

	// 客户端重复上报同一次落水不能再扣（幂等键沿用本次抛竿）。
	fall143()
	if got := qty(); got != 1 {
		t.Fatalf("重复上报不该再扣，实得 %d", got)
	}
	if n := gbCount(t, s, uid); n != 1 {
		t.Fatalf("重复上报不该多记窝，实得 %d 批", n)
	}
}

// 先打窝、后抛竿的顺序实测过：弹弓与手抛只上报距离、不上报落点坐标，
// 这种窝记在「落点未知」那一格。玩家抛竿后判鱼用的是浮标落点格子，两者不同格——
// 这份窝必须按衰减系数继续生效（否则实机「先用弹弓打窝再抛竿」等于白打），
// 但抬升要低于同一份数、落点正好压在浮标格上的窝；衰减调 0 退回「只认同格」。
func TestGroundbaitUnknownLandingStillWorks(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	const uidUnknown = int64(1000001)
	const uidKnown = int64(1000002)

	// 落点未知：hasPos=false，落点退化成「整个钓点一格」(spot,-1)。
	s.groundbaitDeposit(ctx, uidUnknown, "gb-test-loc", 0, 0, false, 7, "feed_karp", 8, "catapult", "gb:n1", "")
	// 同份数、落点已知，并且正好压在浮标所在的 (300,300) 格。
	s.groundbaitDeposit(ctx, uidKnown, "gb-test-loc", 300, 300, true, 7, "feed_karp", 8, "feeder", "gb:n2", "")

	roll := func(uid int64) float64 {
		c := fishRollContext{
			uid: uid, loc: "gb-test-loc", x: 300, z: 300, hasPos: true, spot: 7,
			now: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC), rigType: 1, depthCM: 100,
			cache:   newFishingRollCache(),
			weather: fishingWeather{known: true, kind: fishingWeatherClear, airTemp: 18, previousAirTemp: 18, wind: 3},
		}
		keys := []string{"carp", "n.pike"}
		wts := []float64{100, 100}
		s.applyGroundbaitWeight(ctx, &c, keys, wts)
		if wts[1] != 100 {
			t.Fatalf("掠食者任何时候都不该蹭窝，实得 %.2f", wts[1])
		}
		return wts[0]
	}

	diffuse := roll(uidUnknown)
	if diffuse <= 100 {
		t.Fatalf("落点未知的窝也应在浮标格生效，实得 %.2f", diffuse)
	}
	full := roll(uidKnown)
	if full <= diffuse {
		t.Fatalf("落点未知的抬升（%.2f）必须低于同份数落点已知的窝（%.2f）", diffuse, full)
	}

	// 落点未知的批次要能在状态里看出来，现场日志靠这两个数分辨窝打没打上。
	st := s.groundbaitSpot(ctx, uidUnknown, "gb-test-loc", int64(300/groundbaitCellSizeM), int64(300/groundbaitCellSizeM))
	if st.batches != 1 || st.diffuse != 1 {
		t.Fatalf("应看到 1 批落点未知的窝，实得 batches=%d diffuse=%d", st.batches, st.diffuse)
	}
	if st.units < 3.19 || st.units > 3.21 {
		t.Fatalf("8 份落点未知按 0.40 衰减应得 N=3.2，实得 %.3f", st.units)
	}

	// 衰减调成 0：退回「只认同格」，浮标格彻底看不见这份窝。
	setWeatherTestTunable(t, s, "groundbait_diffuse_fall", "0")
	if back := roll(uidUnknown); back != 100 {
		t.Fatalf("衰减=0 时落点未知的窝不该影响浮标格，实得 %.2f", back)
	}
	setWeatherTestTunable(t, s, "groundbait_diffuse_fall", "0.40")

	// 咬口扣份也要能从落点未知那几行里扣，否则窝永远不吃薄、死窝也解不开。
	s.groundbaitConsumeBite(ctx, uidUnknown, "gb-test-loc", 300, 300, true, 7, 1)
	var left float64
	if err := s.DB.SQL.QueryRow(`SELECT units_left FROM groundbait_deposit WHERE txn_guid='gb:n1'`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left < 6.99 || left > 7.01 {
		t.Fatalf("咬口应从没落点的窝行里扣掉 1 份（8→7），实得 %.3f", left)
	}
}

// TestGroundbaitNeighborCellCountsWeaker 验证圆形落点范围：浮标离窝料越远，
// 窝效越低；范围外为 0；咬口扣份优先扣离浮标最近的窝。
func TestGroundbaitNeighborCellCountsWeaker(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	const (
		uidNear = int64(1000001)
		uidFar  = int64(1000002)
		uidSame = int64(1000003)
	)
	// 浮标在 (300,300)，即格子 (60,60)。
	s.groundbaitDeposit(ctx, uidNear, "gb-test-loc", 305, 302, true, 7, "feed_karp", 8, "catapult", "gb:nb1", "")
	s.groundbaitDeposit(ctx, uidFar, "gb-test-loc", 320, 300, true, 7, "feed_karp", 8, "catapult", "gb:nb2", "")
	s.groundbaitDeposit(ctx, uidSame, "gb-test-loc", 300, 300, true, 7, "feed_karp", 8, "feeder", "gb:nb3", "")

	roll := func(uid int64) float64 {
		c := fishRollContext{
			uid: uid, loc: "gb-test-loc", x: 300, z: 300, hasPos: true, spot: 7,
			now: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC), rigType: 1, depthCM: 100,
			cache:   newFishingRollCache(),
			weather: fishingWeather{known: true, kind: fishingWeatherClear, airTemp: 18, previousAirTemp: 18, wind: 3},
		}
		keys := []string{"carp", "n.pike"}
		wts := []float64{100, 100}
		s.applyGroundbaitWeight(ctx, &c, keys, wts)
		if wts[1] != 100 {
			t.Fatalf("掠食者任何时候都不该蹭窝，实得 %.2f", wts[1])
		}
		return wts[0]
	}

	near := roll(uidNear)
	same := roll(uidSame)
	far := roll(uidFar)
	if near <= 100 {
		t.Fatalf("相邻一格的窝也应在浮标格生效，实得 %.2f", near)
	}
	if far != 100 {
		t.Fatalf("隔四格的窝不该影响浮标格，实得 %.2f", far)
	}
	if near >= same {
		t.Fatalf("邻格抬升（%.2f）必须低于同份数本格窝（%.2f）", near, same)
	}

	st := s.groundbaitSpotAt(ctx, uidNear, "gb-test-loc", 300, 300, true, 7)
	if st.batches != 1 || st.near != 1 || st.diffuse != 0 {
		t.Fatalf("应看到 1 批距离衰减窝且不算落点未知，实得 batches=%d near=%d diffuse=%d", st.batches, st.near, st.diffuse)
	}
	// (305,302) 距浮标 sqrt(5²+2²)=5.385m，圆形权重约 0.815。
	if st.units < 6.5 || st.units > 6.55 {
		t.Fatalf("8 份 5.385m 外窝按圆形衰减应得 N≈6.52，实得 %.3f", st.units)
	}

	// 把影响半径压到 0，验证范围外彻底无关。
	setWeatherTestTunable(t, s, "groundbait_range_core_m", "0")
	setWeatherTestTunable(t, s, "groundbait_range_mid_m", "0")
	setWeatherTestTunable(t, s, "groundbait_range_outer_m", "0")
	if back := roll(uidNear); back != 100 {
		t.Fatalf("影响半径=0 时范围外窝不该影响浮标格，实得 %.2f", back)
	}
	setWeatherTestTunable(t, s, "groundbait_range_core_m", "4")
	setWeatherTestTunable(t, s, "groundbait_range_mid_m", "7")
	setWeatherTestTunable(t, s, "groundbait_range_outer_m", "10")

	// 咬口扣份优先本格：本格只剩 1 份，先吃掉它，邻格那 8 份此刻不动。
	const uidMix = int64(1000004)
	s.groundbaitDeposit(ctx, uidMix, "gb-test-loc", 300, 300, true, 7, "feed_karp", 1, "feeder", "gb:mix1", "")
	s.groundbaitDeposit(ctx, uidMix, "gb-test-loc", 305, 302, true, 7, "feed_karp", 8, "catapult", "gb:mix2", "")
	s.groundbaitConsumeBite(ctx, uidMix, "gb-test-loc", 300, 300, true, 7, 1)
	var sameLeft, nearLeft float64
	if err := s.DB.SQL.QueryRow(`SELECT units_left FROM groundbait_deposit WHERE txn_guid='gb:mix1'`).Scan(&sameLeft); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.SQL.QueryRow(`SELECT units_left FROM groundbait_deposit WHERE txn_guid='gb:mix2'`).Scan(&nearLeft); err != nil {
		t.Fatal(err)
	}
	if sameLeft > 0.001 {
		t.Fatalf("咬口应先扣掉本格那 1 份，实得剩余 %.3f", sameLeft)
	}
	if nearLeft < 7.99 || nearLeft > 8.01 {
		t.Fatalf("邻格那 8 份此刻不该被扣，实得 %.3f", nearLeft)
	}
}

// TestGroundbaitThrowFallsBackToPlayerCell 还没抛过竿时弹弓的窝记在玩家自己站的格子。
// 玩家坐标来自 3/9（每 5 秒上报，实测与 14/2 的落点数值逐条吻合），比「整个钓点一格」
// 的模糊窝准得多；关掉兜底要能原样退回落点未知那一格。
func TestGroundbaitThrowFallsBackToPlayerCell(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	const uid = int64(1000001)

	hand, err := s.CreateItem(ctx, uid, "wh_pc_gb", "backpack", 1)
	if err != nil {
		t.Fatalf("创建弹弓失败: %v", err)
	}
	stack, err := s.CreateItem(ctx, uid, "feedcompleted", "rig", 1)
	if err != nil {
		t.Fatalf("创建窝料失败: %v", err)
	}
	s.setStackCount(ctx, stack, 5)
	s.setHanditemLoadedRef(ctx, hand.GUID, stack.GUID)

	st := s.stateFor(uid)
	st.mu.Lock()
	// 人站在钓点上，但一竿都没抛过：没有浮标落点，只有 3/9 报上来的玩家坐标。
	st.Loc, st.Spot = "gb-test-loc", 7
	st.PosX, st.PosZ, st.HasPos, st.HasCastPos = 101, 99, true, false
	st.mu.Unlock()

	sess, _ := newTunnelProbe(t, s)
	throwOnce := func(callID uint32) {
		t.Helper()
		req := rigRequestTree(t, proto.Guid(hand.GUID),
			wiregen.MustMakeObj("olieodeoaof", map[int]*proto.Value{0: proto.F32(12)}))
		hctx := &Ctx{Sess: sess, Main: 13, Sub: 5, CallID: callID, Tree: req,
			DecodeOK: true, ExpectResponse: true, Log: logging.Discard()}
		if _, ok := hDynamicHanditemThrow(hctx); !ok {
			t.Fatalf("13/5 未处理（call=%d）", callID)
		}
	}

	throwOnce(9101)
	var (
		hasPosRow int
		bx, bz    int64
	)
	if err := s.DB.SQL.QueryRowContext(ctx, `SELECT has_pos,bx,bz FROM groundbait_deposit WHERE uid=? AND txn_guid LIKE '%9101'`, uid).
		Scan(&hasPosRow, &bx, &bz); err != nil {
		t.Fatalf("第一弹没落账: %v", err)
	}
	wantBX, wantBZ := groundbaitCell(101, 99)
	if hasPosRow != 1 || bx != wantBX || bz != wantBZ {
		t.Fatalf("没抛过竿时应记在玩家脚下格子，期望 has_pos=1 (%d,%d)，实得 has_pos=%d (%d,%d)",
			wantBX, wantBZ, hasPosRow, bx, bz)
	}
	if got := s.groundbaitSpot(ctx, uid, "gb-test-loc", wantBX, wantBZ).units; got < 0.99 || got > 1.01 {
		t.Fatalf("玩家格上的窝应全额生效 1 份，实得 %.3f", got)
	}

	// 兜底关掉：退回原来的「整个钓点一格」落点未知。
	setWeatherTestTunable(t, s, "groundbait_throw_pos_fallback", "0")
	throwOnce(9102)
	if err := s.DB.SQL.QueryRowContext(ctx, `SELECT has_pos FROM groundbait_deposit WHERE uid=? AND txn_guid LIKE '%9102'`, uid).
		Scan(&hasPosRow); err != nil {
		t.Fatalf("第二弹没落账: %v", err)
	}
	if hasPosRow != 0 {
		t.Fatalf("兜底关闭后应退回落点未知，实得 has_pos=%d", hasPosRow)
	}
}

// TestGroundbaitInstanceCarriesUnitWeight 窝料实例必须自带单件克重。
// 原生品类 46 的 11 个定义 ijbjficbhog 全是 0（实测连商店买的「干混合」在客户端
// 也显示「质量 0 克」），真实服务端是按实例下发的。客户端「质量」那行读实例
// 重量字段的 IEEE754 位模式、单位克：写 30.0 显示「30 克」，写 30000.0 显示
// 「30 公斤」（2026-11-07 两堆各写一个值实测）。
func TestGroundbaitInstanceCarriesUnitWeight(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	cases := []struct {
		item  string
		grams float32
	}{
		{"feed_karp", 45},          // 窝料档案里带 unit_grams
		{"feed_probe_missing", 30}, // 档案缺项 â 兜底一球 30 克
	}
	for i, c := range cases {
		if def, err := s.DB.GetItemDefByItemID(ctx, c.item); err != nil || def == nil {
			if _, err := s.DB.SQL.ExecContext(ctx,
				`INSERT INTO item_def(numeric_id,item_id,category,attrs_json)
				 VALUES(999860,?,46,'{"fopbhlmkcpe":1,"ijbjficbhog":0,"ljnnpokapig":0,"hgaijieklek":1069120}')`,
				c.item); err != nil {
				t.Fatal(err)
			}
		}
		row, err := s.CreateItem(ctx, uid, c.item, "backpack", 60)
		if err != nil {
			t.Fatalf("第 %d 个 %s 建实例失败: %v", i+1, c.item, err)
		}
		if row.WireClass != "cilmgoaoofg" {
			t.Fatalf("窝料实例类应为 cilmgoaoofg，实得 %s", row.WireClass)
		}
		fields, err := FieldsFromJSON(row.FieldsJSON)
		if err != nil {
			t.Fatal(err)
		}
		idx := fieldIdxByCS(row.WireClass, castAttrWaterWeight)
		if idx < 0 || idx >= len(fields) {
			t.Fatalf("%s 没有重量字段 %s", row.WireClass, castAttrWaterWeight)
		}
		if got := math.Float32frombits(uint32(u64Of(fields[idx].Num))); got != c.grams {
			t.Fatalf("第 %d 个 %s 的实例克重应为 %.1f，实得 %.4f", i+1, c.item, c.grams, got)
		}
	}
}

// TestGroundbaitRepairBackfillsZeroWeight 老库存里照抄定义生成的窝料实例重量恒 0，
// 启动修复必须补回单件克重，否则玩家已经做好的那几堆永远显示「质量 0 克」。
func TestGroundbaitRepairBackfillsZeroWeight(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	row, err := s.CreateItem(ctx, 1000001, "feed_karp", "backpack", 30)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	idx := fieldIdxByCS(row.WireClass, castAttrWaterWeight)
	if idx < 0 || idx >= len(fields) {
		t.Fatalf("%s 没有重量字段", row.WireClass)
	}
	fields[idx].Num = ptrU64(0) // 模拟旧版本照抄定义产生的「0 克」实例
	fj, err := FieldsToJSON(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.UpdateItemFields(ctx, row.GUID, fj, row.Qty, row.Durability, row.Workmanship, row.RigGUID); err != nil {
		t.Fatal(err)
	}
	if err := s.RepairItemPhysics(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := s.DB.GetItemByGUID(ctx, row.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if after == nil {
		t.Fatal("修复后物品消失")
	}
	fields, err = FieldsFromJSON(after.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if got := math.Float32frombits(uint32(u64Of(fields[idx].Num))); got != 45 {
		t.Fatalf("启动修复应把窝料实例克重补成 45 克，实得 %.4f", got)
	}
}

// keepalive 应持续推进在线窝料钟，避免长时间没有判鱼时整段在线时间被丢弃。
func TestGroundbaitKeepaliveAdvancesOnlineClock(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	const uid = int64(1000001)
	oldTS := float64(time.Now().Add(-20 * time.Second).Unix())
	if _, err := s.DB.SQL.ExecContext(ctx, `INSERT INTO groundbait_clock(uid,real_ts,game_hours,updated_ts)
		VALUES(?,?,?,?)`, uid, oldTS, 1.0, oldTS); err != nil {
		t.Fatal(err)
	}
	sess := newSession(s, 1, "groundbait-clock-test", uid, nil)
	sess.advanceGroundbaitClock()
	var acc float64
	if err := s.DB.SQL.QueryRowContext(ctx, `SELECT game_hours FROM groundbait_clock WHERE uid=?`, uid).Scan(&acc); err != nil {
		t.Fatal(err)
	}
	if acc < 1.10 || acc > 1.16 {
		t.Fatalf("在线 20 秒应推进约 0.133 游戏小时，实得 %.3f", acc)
	}
}

// TestGroundbaitHandThrowConsumesAndDeposits 手抛（12/1）必须真扣一份并记进窝账。
// 实机实测扔两发后背包数字减少，但库里 qty 一动不动：客户端只扣本地，
// 服务端不接就会在下次全量快照里把这两份补回来（幽灵窝料）。
// 请求形态实测：args(物件实例 GUID, obj19)，f0=f1=投距（6.108 / 7.567 米），f3=钓点串。
func TestGroundbaitHandThrowConsumesAndDeposits(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	const uid = int64(1000001)

	stack, err := s.CreateItem(ctx, uid, "feedcompleted", "backpack", 1)
	if err != nil {
		t.Fatalf("创建窝料失败: %v", err)
	}
	s.setStackCount(ctx, stack, 3)

	st := s.stateFor(uid)
	st.mu.Lock()
	st.Loc, st.CastPosX, st.CastPosZ, st.HasCastPos, st.Spot = "gb-test-loc", 100, 100, true, 7
	st.PosX, st.PosZ, st.HasPos = 60, 60, true
	st.mu.Unlock()

	sess, _ := newTunnelProbe(t, s)
	// 12/1 是 fire-and-forget：dispatch 不看返回值、也不走兜底链，
	// 所以这里只看副作用（扣了几份、记没记窝）。
	throw := func(callID uint32, dist float32, guid string) {
		t.Helper()
		req := rigRequestTree(t, proto.Guid(guid),
			wiregen.MustMakeObj("olieodeoaof", map[int]*proto.Value{0: proto.F32(dist), 3: proto.StrVal("gb-test-loc")}))
		hctx := &Ctx{Sess: sess, Main: 12, Sub: 1, CallID: callID, Tree: req,
			DecodeOK: true, ExpectResponse: false, Log: logging.Discard()}
		hHandThrowGroundbait(hctx)
	}
	qty := func() int64 {
		t.Helper()
		row, rerr := s.DB.GetItemByGUID(ctx, stack.GUID)
		if rerr != nil || row == nil {
			return -1
		}
		return s.stackCountOf(ctx, row)
	}
	depositRow := func(callID string) (string, float64, int, int64, int64) {
		t.Helper()
		var (
			ch, itemID     string
			portions       float64
			hasPos, bx, bz int64
		)
		err := s.DB.SQL.QueryRowContext(ctx, `SELECT channel,item_id,portions,has_pos,bx,bz
			FROM groundbait_deposit WHERE uid=? AND txn_guid LIKE ?`, uid, "%hand:feedcompleted:%:"+callID).
			Scan(&ch, &itemID, &portions, &hasPos, &bx, &bz)
		if err != nil {
			t.Fatalf("手抛没记进窝账（call=%s）: %v", callID, err)
		}
		return itemID, portions, int(hasPos), bx, bz
	}

	throw(9201, 6.108, stack.GUID)
	if got := qty(); got != 2 {
		t.Fatalf("手抛一发应扣掉 1 份（3→2），实得 %d", got)
	}
	itemID, portions, hasPos, bx, bz := depositRow("9201")
	wantBX, wantBZ := groundbaitCell(100, 100)
	if itemID != "feedcompleted" || portions != 1 {
		t.Fatalf("窝账应是 1 份 feedcompleted，实得 %s / %.2f", itemID, portions)
	}
	if hasPos != 1 || bx != wantBX || bz != wantBZ {
		t.Fatalf("有浮标落点时应记在浮标格 (%d,%d)，实得 has_pos=%d (%d,%d)", wantBX, wantBZ, hasPos, bx, bz)
	}
	if got := s.groundbaitSpot(ctx, uid, "gb-test-loc", wantBX, wantBZ).units; got < 0.99 || got > 1.01 {
		t.Fatalf("手抛那份窝应全额生效，实得 %.3f", got)
	}

	// 一竿都没抛过时记在玩家脚下格（浮标落点清掉，玩家坐标仍在）。
	st.mu.Lock()
	st.HasCastPos, st.CastPosX, st.CastPosZ = false, 0, 0
	st.mu.Unlock()
	throw(9202, 7.567, stack.GUID)
	if got := qty(); got != 1 {
		t.Fatalf("第二发应再扣 1 份（2→1），实得 %d", got)
	}
	_, _, hasPos, bx, bz = depositRow("9202")
	pBX, pBZ := groundbaitCell(60, 60)
	if hasPos != 1 || bx != pBX || bz != pBZ {
		t.Fatalf("没抛过竿时应记在玩家脚下格 (%d,%d)，实得 has_pos=%d (%d,%d)", pBX, pBZ, hasPos, bx, bz)
	}

	// 最后一份：库里整堆没了，但这一发照样算进窝账。
	throw(9203, 5.5, stack.GUID)
	if got := qty(); got != -1 {
		t.Fatalf("第三发后整堆应被删掉，实得 qty=%d", got)
	}
	if _, _, _, _, bz := depositRow("9203"); bz == 0 {
		t.Fatal("最后一发也应记进窝账")
	}
	if rows := gbCount(t, s, uid); rows != 3 {
		t.Fatalf("三发应各记一批，实得 %d 批", rows)
	}

	// 非窝料物件（弹弓本身）不该被窝料通道吃掉。
	tool, terr := s.CreateItem(ctx, uid, "wh_pc_gb", "backpack", 1)
	if terr != nil {
		t.Fatalf("创建手持工具失败: %v", terr)
	}
	throw(9204, 6.0, tool.GUID)
	if n := gbDepositCount(t, s, uid, "9204"); n != 0 {
		t.Fatalf("非窝料品类不该记进窝账，实得 %d 批", n)
	}
	row, rerr := s.DB.GetItemByGUID(ctx, tool.GUID)
	if rerr != nil || row == nil {
		t.Fatal("非窝料物件不该被扣掉")
	}
	if got := s.stackCountOf(ctx, row); got != 1 {
		t.Fatalf("非窝料物件数量应不变，实得 %d", got)
	}
}
