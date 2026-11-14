package gameserver

// 玩家状态条测试：推进函数手算样例、懒计算读写、开关旁路、
// 进食行为（3/13 → 饱食度恢复 → 2/2 推送 f1=1.0）与抛投/精力扣减。
// 期望值来自手算与官服抓包观察，不用同一份代码回算。

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

func testConditionParams() conditionParams {
	return conditionParams{
		Enabled:            true,
		HungerDecayPerHour: 0.24,
		EnergyRegenPerHour: 0.5,
		EnergyMax:          1.0,
		EnergyRegenCap:     0.85,
		HungerRegenFloor:   0.25,
		CastEnergyCost:     0.01,
		PullEnergyCost:     0.005,
		MaxBackfillHours:   12,

		ComfortRainPenalty:   0.35,
		ComfortColdPenalty:   0.35,
		ComfortColdTempC:     5,
		ComfortCampfireBoost: 0.5,
		ComfortSunBoost:      0.1,
		ComfortDriftPerHour:  0.5,
		SunEnergyRegenMult:   1.25,
		GearEnergyRegenCap:   0.5,
		GearEnergyMaxCap:     0.15,

		// 既有手算用例不启用“回精力吃食”折算，单独用例覆盖。
		EnergyRegenFoodCost: 0,

		HealthDecayPerHour:     0.10,
		HealthRegenPerHour:     0.05,
		HealthHungerThreshold:  0.10,
		HealthComfortThreshold: 0.30,
		MedicineHealthRestore:  0.50,
	}
}

// 手算样例：0.5h → 饱食 1.00-0.24*0.5=0.88；
// 精力因子 = 0.25+0.75*0.88 = 0.91，恢复 0.5*0.91*0.5 = 0.2275。
func TestAdvancePlayerConditionHandComputed(t *testing.T) {
	t.Parallel()
	cur := store.PlayerCondition{Primary: 0.10, Secondary: 1.0, Comfort: 1.0, UpdatedTS: 1000}
	next := advancePlayerCondition(cur, 1000+1800, testConditionParams(), conditionEnv{})
	if math.Abs(next.Secondary-0.88) > 1e-9 {
		t.Fatalf("饱食度手算不符: %v", next.Secondary)
	}
	if math.Abs(next.Primary-(0.10+0.5*0.91*0.5)) > 1e-9 {
		t.Fatalf("精力手算不符: %v", next.Primary)
	}
	// 舒适度无环境输入时不动；健康无饥寒压力时已在 0（满血），自愈被地板钳住。
	if next.Comfort != cur.Comfort || next.LuckPenalty != 0 {
		t.Fatalf("舒适度应透传、健康应停在地板 0: comfort=%v luck=%v", next.Comfort, next.LuckPenalty)
	}
}

func TestAdvancePlayerConditionClampsAndCaps(t *testing.T) {
	t.Parallel()
	p := testConditionParams()
	// 安全阀：单次推进跨度封顶 12h（会话钟停滞兜底），饱食触底，精力恢复到上限。
	cur := store.PlayerCondition{Primary: 0.10, Secondary: 1.0, UpdatedTS: 0}
	next := advancePlayerCondition(cur, 100*3600, p, conditionEnv{})
	if next.Secondary != 0 {
		t.Fatalf("长离线饱食度应触底: %v", next.Secondary)
	}
	if math.Abs(next.Primary-p.EnergyRegenCap) > 1e-9 {
		t.Fatalf("长离线精力应封在自然恢复上限: %v (cap=%v)", next.Primary, p.EnergyRegenCap)
	}
	// 时钟倒退不推进状态值。
	same := advancePlayerCondition(cur, cur.UpdatedTS-10, p, conditionEnv{})
	if same.Primary != cur.Primary || same.Secondary != cur.Secondary {
		t.Fatalf("时钟倒退不应改变状态值: %+v", same)
	}
}

func TestAdvancePlayerConditionDisabledIsPassthrough(t *testing.T) {
	t.Parallel()
	p := testConditionParams()
	p.Enabled = false
	cur := store.PlayerCondition{Primary: 0.3, Secondary: 0.4, UpdatedTS: 100}
	next := advancePlayerCondition(cur, 99999, p, conditionEnv{})
	if next.Primary != 0.3 || next.Secondary != 0.4 {
		t.Fatalf("开关关闭应原样透传: %+v", next)
	}
}

// 懒计算读取口：会话内的在线跨度推进并写回。
func TestCurrentPlayerConditionAdvancesAndPersists(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	old := float64(time.Now().Add(-10 * time.Minute).Unix())
	if err := db.UpsertPlayerCondition(ctx, store.PlayerCondition{
		UID: uid, Primary: 0.10, Secondary: 1.0, UpdatedTS: old,
	}); err != nil {
		t.Fatal(err)
	}
	got := srv.currentPlayerCondition(ctx, uid)
	if got.Secondary >= 1.0 {
		t.Fatalf("10 分钟在线跨度后饱食度应已下降: %v", got.Secondary)
	}
	row, err := db.EnsurePlayerCondition(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if row.Secondary >= 1.0 {
		t.Fatalf("推进结果未持久化: %+v", row)
	}
}

// 离线停扣：登录冻结状态钟，只推 updated_ts，不改状态值。
func TestResumePlayerConditionClockSwallowsOfflineGap(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	old := float64(time.Now().Add(-10 * time.Hour).Unix())
	if err := db.UpsertPlayerCondition(ctx, store.PlayerCondition{
		UID: uid, Primary: 0.30, Secondary: 0.40, UpdatedTS: old,
	}); err != nil {
		t.Fatal(err)
	}
	srv.resumePlayerConditionClock(ctx, uid)
	row, err := db.EnsurePlayerCondition(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if row.Primary != 0.30 || row.Secondary != 0.40 {
		t.Fatalf("冻结不应改变状态值: %+v", row)
	}
	if row.UpdatedTS <= old {
		t.Fatalf("冻结应把 updated_ts 推到当前: %+v", row)
	}
}

// 总开关关闭：读口回协议默认常量且不改写库存值。
func TestCurrentPlayerConditionGateOffUsesDefaults(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if _, err := db.SQL.Exec(`INSERT INTO tunables(key,value) VALUES('player_condition_enabled','0')`); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertPlayerCondition(ctx, store.PlayerCondition{
		UID: uid, Primary: 0.10, Secondary: 0.20, UpdatedTS: 1,
	}); err != nil {
		t.Fatal(err)
	}
	got := srv.currentPlayerCondition(ctx, uid)
	if got.Primary != store.DefaultPlayerConditionPrimary || got.Secondary != store.DefaultPlayerConditionSecondary {
		t.Fatalf("开关关闭应回协议默认值: %+v", got)
	}
	row, err := db.EnsurePlayerCondition(ctx, uid)
	if err != nil || row.Primary != 0.10 || row.Secondary != 0.20 {
		t.Fatalf("开关关闭不应改写库存值: %+v %v", row, err)
	}
}

// 3/13 进食：饱食度恢复 1.0（官服抓包口径），响应 null，补推 2/2 且 f1=1.0。
func TestKitchenEatRestoresHungerAndPushes(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx0 := context.Background()
	const uid = int64(1000001)
	if err := db.UpsertPlayerCondition(ctx0, store.PlayerCondition{
		UID: uid, Primary: 0.5, Secondary: 0.30, Comfort: 1.0, UpdatedTS: float64(time.Now().Unix()) + 3600,
	}); err != nil {
		t.Fatal(err)
	}
	sess, received := progressNotificationSession(t, srv, 77)
	ctx := &Ctx{Sess: sess, Main: 3, Sub: 13, CallID: 42, ExpectResponse: true, Log: logging.Discard()}
	body, handled := hKitchenEat(ctx)
	if !handled || len(body) != 1 || body[0] != 0x00 {
		t.Fatalf("3/13 应回 null: handled=%v body=%v", handled, body)
	}
	for _, fn := range ctx.afterResponse {
		fn()
	}
	row, err := db.EnsurePlayerCondition(ctx0, uid)
	if err != nil || row.Secondary != 1.0 {
		t.Fatalf("进食应恢复饱食度: %+v %v", row, err)
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case envelope, ok := <-received:
			if !ok {
				t.Fatal("推送通道提前关闭")
			}
			if envelope.Main != 2 || envelope.Sub != 2 {
				continue
			}
			v, _, err := proto.DecodeValue(envelope.Body, 0, wiregen.Table())
			if err != nil {
				t.Fatalf("2/2 解码失败: %v", err)
			}
			obj := v.ArgItem(0)
			if obj == nil || obj.Kind != proto.KindObj {
				t.Fatal("2/2 载荷应为 args(524)[obj308]")
			}
			if hunger := f32Field(obj, 1); math.Abs(float64(hunger-1.0)) > 1e-6 {
				t.Fatalf("进食推送 f1 应为 1.0: %v", hunger)
			}
			if comfort := f32Field(obj, 3); comfort != 1.0 {
				t.Fatalf("f3 应透传 1.0: %v", comfort)
			}
			if luck := f32Field(obj, 4); luck != 0.0 {
				t.Fatalf("f4 应透传 0.0: %v", luck)
			}
			return
		case <-deadline.C:
			t.Fatal("未收到进食后的 2/2 推送")
		}
	}
}

func f32Field(obj *proto.Value, idx int) float32 {
	if idx < 0 || idx >= obj.FieldCount() {
		return 0
	}
	f := obj.Field(idx)
	if f.Num == nil {
		return 0
	}
	return math.Float32frombits(uint32(u64Of(f.Num)))
}

// 零精力、旧门槛两侧均受理抛投并排咬钩；抛投仍扣精力，最低为零。
func TestCastStartsBiteAtAnyEnergy(t *testing.T) {
	for _, energy := range []float64{0, 0.01, 0.05, 0.85} {
		t.Run(fmt.Sprintf("energy_%g", energy), func(t *testing.T) {
			srv, db := newRigTestServer(t)
			ctx := context.Background()
			const uid = int64(1000001)
			if _, err := db.EnsureProfile(ctx, uid); err != nil {
				t.Fatal(err)
			}
			rod, err := srv.CreateItem(ctx, uid, "tele_10012_tl16", "backpack", 1)
			if err != nil {
				t.Fatal(err)
			}
			// 冻结自然恢复，防止低精力在进入 handler 前涨回旧门槛之上。
			if err := db.UpsertPlayerCondition(ctx, store.PlayerCondition{
				UID: uid, Primary: energy, Secondary: 1, UpdatedTS: float64(time.Now().Unix()) + 3600,
			}); err != nil {
				t.Fatal(err)
			}
			sess := newProbeSess(srv)
			st := srv.selectFishingRod(uid, rod.GUID)
			t.Cleanup(func() {
				st.mu.Lock()
				defer st.mu.Unlock()
				st.cancelBiteTimerLocked()
				st.cancelPendingTimerLocked()
			})
			body, handled := hCast(&Ctx{Sess: sess, Main: 14, Sub: 2,
				Tree: rigRequestTree(t, proto.Guid(rod.GUID)), DecodeOK: true,
				ExpectResponse: true, Log: logging.Discard()})
			if !handled || len(body) == 0 {
				t.Fatalf("14/2 未受理: handled=%v len=%d", handled, len(body))
			}
			response, _, err := proto.DecodeValue(body, 0, srv.Table())
			if err != nil || response.Class != "dlbahppbdad" {
				t.Fatalf("14/2 应回 800: response=%v err=%v", response, err)
			}
			items := rigSeg(response, 0)
			if items == nil || items.Kind != proto.KindArgs || items.ArgCount() != 1 ||
				items.ArgItem(0).Class != "cnolibojkao" {
				t.Fatalf("14/2 应返回钓组快照: %v", items)
			}
			st.mu.Lock()
			active := st.CastActive && !st.Resolved && !st.BiteAt.IsZero()
			st.mu.Unlock()
			if !active {
				t.Fatal("已受理抛竿必须启动咬钩计时")
			}
			condition, err := db.EnsurePlayerCondition(ctx, uid)
			if err != nil {
				t.Fatal(err)
			}
			if want := math.Max(0, energy-0.01); math.Abs(condition.Primary-want) > 1e-9 {
				t.Fatalf("抛投扣精力: got=%g want=%g", condition.Primary, want)
			}
		})
	}
}

// 动作扣减：直接落库（读满值 0.85 封顶后扣 0.10）。
func TestSpendEnergyConsumesAndPersists(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if err := db.UpsertPlayerCondition(ctx, store.PlayerCondition{
		UID: uid, Primary: 0.85, Secondary: 1.0, UpdatedTS: float64(time.Now().Unix()) + 3600,
	}); err != nil {
		t.Fatal(err)
	}
	srv.spendEnergy(ctx, uid, 0.10)
	row, err := db.EnsurePlayerCondition(ctx, uid)
	if err != nil || math.Abs(row.Primary-(0.85-0.10)) > 1e-9 {
		t.Fatalf("精力扣减未持久化: %+v %v", row, err)
	}
}

// 舒适度环境漂移：雨天均衡点 1−0.35=0.65，1h 漂移 0.5 触底；
// 篝火抵消雨天；晒阳精力恢复乘 1.25、均衡点 1.0+0.1 钳回 1.0。
func TestAdvancePlayerConditionComfortDriftAndSun(t *testing.T) {
	t.Parallel()
	p := testConditionParams()
	cur := store.PlayerCondition{Primary: 0.85, Secondary: 1.0, Comfort: 1.0, UpdatedTS: 1000}
	next := advancePlayerCondition(cur, 1000+3600, p, conditionEnv{Rain: true})
	if math.Abs(next.Comfort-0.65) > 1e-9 {
		t.Fatalf("雨天 1h 舒适度应漂到均衡点 0.65: %v", next.Comfort)
	}
	warm := advancePlayerCondition(cur, 1000+3600, p, conditionEnv{Rain: true, Campfire: true})
	if warm.Comfort != 1.0 {
		t.Fatalf("篝火应抵消雨天惩罚: %v", warm.Comfort)
	}
	sun := advancePlayerCondition(store.PlayerCondition{Primary: 0.10, Secondary: 1.0, Comfort: 0.5, UpdatedTS: 1000},
		1000+3600, p, conditionEnv{Sun: true})
	// 饱食先衰减 1h：1.0-0.24=0.76；factor = 0.25+0.75*0.76 = 0.82；
	// regen = 0.5*1.25*0.82 = 0.5125 → 0.10+0.5125 = 0.6125
	if math.Abs(sun.Primary-0.6125) > 1e-9 {
		t.Fatalf("晒阳精力恢复 0.5*1.25*1h: %v", sun.Primary)
	}
	if sun.Comfort != 1.0 {
		t.Fatalf("晒阳均衡点 1.0+0.1 应钳回 1.0: %v", sun.Comfort)
	}
}

// 装备被动加成：attrs_json 携带 oiicogcccao/oodjilnjeen 时按竿解析
// （静态库当前不带这些值，标定写入后同源生效）。
func TestRigEnergyBonusesFromAttrs(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if _, err := db.SQL.Exec(`INSERT INTO item_def(numeric_id,item_id,category,attrs_json)
		VALUES(910001,'rod_energy_test',3,'{"oiicogcccao":0.3,"oodjilnjeen":0.1}')`); err != nil {
		t.Fatal(err)
	}
	rod, err := srv.CreateItem(ctx, uid, "rod_energy_test", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	regen, maxAdd := srv.rigEnergyBonuses(ctx, uid, rod.GUID)
	if math.Abs(regen-0.3) > 1e-9 || math.Abs(maxAdd-0.1) > 1e-9 {
		t.Fatalf("装备加成解析错误: regen=%v max=%v", regen, maxAdd)
	}
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.GearGuid = rod.GUID
	st.mu.Unlock()
	if err := db.UpsertPlayerCondition(ctx, store.PlayerCondition{
		UID: uid, Primary: 0.90, Secondary: 1.0, Comfort: 1.0,
		UpdatedTS: float64(time.Now().Unix()) + 3600,
	}); err != nil {
		t.Fatal(err)
	}
	srv.spendEnergy(ctx, uid, 0.01)
	row, err := db.EnsurePlayerCondition(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(row.Primary-0.89) > 1e-9 {
		t.Fatalf("装备上限加成不应被动作路径截断: primary=%v want=0.89", row.Primary)
	}
	if _, ok := consumableRestoreForItem("rod_energy_test"); ok {
		t.Fatal("装备不应进消耗品恢复白名单")
	}
}

// 307/档案响应里的 308 只读，不推进在线钟。
func TestBuildPose308DoesNotAdvanceCondition(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	oldTS := float64(time.Now().Add(-2 * time.Hour).Unix())
	if err := db.UpsertPlayerCondition(ctx, store.PlayerCondition{
		UID: uid, Primary: 0.40, Secondary: 0.50, Comfort: 0.60, LuckPenalty: 0.1,
		UpdatedTS: oldTS,
	}); err != nil {
		t.Fatal(err)
	}
	srv.buildPose308(ctx, uid)
	row, err := db.EnsurePlayerCondition(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if row.Primary != 0.40 || row.Secondary != 0.50 || row.Comfort != 0.60 || row.UpdatedTS != oldTS {
		t.Fatalf("非 2/2 的 308 构建不应推进状态: %+v", row)
	}
}

// 篝火舒适度只对同一地点/钓点生效。
func TestConditionEnvCampfireRequiresSameLocation(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	sess := newProbeSess(srv)
	st := srv.stateFor(sess.Userid)
	st.mu.Lock()
	st.Loc = "loc_a"
	st.Spot = 10
	st.mu.Unlock()
	if err := db.UpsertCampfireState(t.Context(), store.CampfireState{
		UID: sess.Userid, Loc: "loc_a", Spot: 10, DurationMin: 60,
		ExpiresFT: int64(proto.WindowsFiletimeNow()) + 10_000_000,
	}); err != nil {
		t.Fatal(err)
	}
	env := sess.conditionEnvForSession(time.Now(), srv.conditionParams())
	if !env.Campfire {
		t.Fatal("同一地点/钓点应获得篝火加成")
	}
	st.mu.Lock()
	st.Loc = "loc_b"
	st.Spot = 10
	st.mu.Unlock()
	env = sess.conditionEnvForSession(time.Now(), srv.conditionParams())
	if env.Campfire {
		t.Fatal("离开篝火地点后不应继续获得加成")
	}
}

// 免费餐冷却：同游戏日首次进食恢复并落日号，同日二次进食回官服结果码 16
// 且不改状态；游戏日翻天后恢复可用。
func TestKitchenEatFreeMealCooldown(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx0 := context.Background()
	const uid = int64(1000001)
	future := float64(time.Now().Unix()) + 3600
	if err := db.UpsertPlayerCondition(ctx0, store.PlayerCondition{
		UID: uid, Primary: 0.5, Secondary: 0.30, Comfort: 1.0, UpdatedTS: future,
	}); err != nil {
		t.Fatal(err)
	}
	sess, _ := progressNotificationSession(t, srv, 88)
	first := &Ctx{Sess: sess, Main: 3, Sub: 13, CallID: 60, ExpectResponse: true,
		Log: logging.Discard(), replyCapture: &ctxReplyCapture{}}
	if body, handled := hKitchenEat(first); !handled || len(body) != 1 || body[0] != 0x00 {
		t.Fatalf("首次进食应成功回 null: handled=%v body=%v", handled, body)
	}
	if row, err := db.EnsurePlayerCondition(ctx0, uid); err != nil || row.Secondary != 1.0 {
		t.Fatalf("首次进食应恢复饱食度: %+v %v", row, err)
	}
	// 模拟随后的衰减，同游戏日二次进食回结果码 16 且不改状态。
	if err := db.UpsertPlayerCondition(ctx0, store.PlayerCondition{
		UID: uid, Primary: 0.5, Secondary: 0.5, Comfort: 1.0, UpdatedTS: future,
	}); err != nil {
		t.Fatal(err)
	}
	second := &Ctx{Sess: sess, Main: 3, Sub: 13, CallID: 61, ExpectResponse: true,
		Log: logging.Discard(), replyCapture: &ctxReplyCapture{}}
	if body, handled := hKitchenEat(second); !handled || body != nil {
		t.Fatalf("同游戏日二次进食应回结果码 16: handled=%v body=%v", handled, body)
	}
	_, present, _, failure := second.replyCapture.snapshot()
	if !present || !failure {
		t.Fatalf("同游戏日二次进食应弹官服文案(结果码 16): present=%v failure=%v", present, failure)
	}
	if row, err := db.EnsurePlayerCondition(ctx0, uid); err != nil || row.Secondary != 0.5 {
		t.Fatalf("冷却期内不应恢复饱食度: %+v %v", row, err)
	}
	// 游戏日翻天（冷却戳写成昨日）后恢复可用。
	gameDay := gameDayNumberAt(time.Now())
	if _, err := db.SQL.Exec(`UPDATE kv SET v=? WHERE k=?`,
		strconv.FormatInt(gameDay-1, 10), fmt.Sprintf(conditionFreeMealKV, uid)); err != nil {
		t.Fatal(err)
	}
	third := &Ctx{Sess: sess, Main: 3, Sub: 13, CallID: 62, ExpectResponse: true,
		Log: logging.Discard(), replyCapture: &ctxReplyCapture{}}
	if body, handled := hKitchenEat(third); !handled || len(body) != 1 || body[0] != 0x00 {
		t.Fatalf("新游戏日进食应恢复: handled=%v body=%v", handled, body)
	}
	if row, err := db.EnsurePlayerCondition(ctx0, uid); err != nil || row.Secondary != 1.0 {
		t.Fatalf("新游戏日应恢复饱食度: %+v %v", row, err)
	}
}

// 恢复精力要按比例吃掉饱食度：饱食度额外下降 = 回复量 * 系数。
func TestAdvanceEnergyRegenConsumesFood(t *testing.T) {
	t.Parallel()
	p := testConditionParams()
	p.EnergyRegenFoodCost = 0.5
	cur := store.PlayerCondition{Primary: 0.10, Secondary: 1.0, Comfort: 1.0, LuckPenalty: 0, UpdatedTS: 1000}
	next := advancePlayerCondition(cur, 1000+1800, p, conditionEnv{})
	gained := next.Primary - cur.Primary
	if gained <= 0 {
		t.Fatalf("精力应上升，实得 %v -> %v", cur.Primary, next.Primary)
	}
	// 先按 natural decay 掉 0.24*0.5，再按回复量扣 0.5*系数。
	want := 1.0 - p.HungerDecayPerHour*0.5 - gained*p.EnergyRegenFoodCost
	if math.Abs(next.Secondary-want) > 1e-9 {
		t.Fatalf("饱食度折算不符: got %v want %v (gained=%v)", next.Secondary, want, gained)
	}
}

// 系数为 0 时不额外消耗饱食度，行为与改动前一致。
func TestAdvanceEnergyRegenNoFoodCostWhenDisabled(t *testing.T) {
	t.Parallel()
	p := testConditionParams()
	p.EnergyRegenFoodCost = 0
	cur := store.PlayerCondition{Primary: 0.10, Secondary: 1.0, Comfort: 1.0, LuckPenalty: 0, UpdatedTS: 1000}
	next := advancePlayerCondition(cur, 1000+1800, p, conditionEnv{})
	want := 1.0 - p.HungerDecayPerHour*0.5
	if math.Abs(next.Secondary-want) > 1e-9 {
		t.Fatalf("关闭时饱食度应只按自然衰减: got %v want %v", next.Secondary, want)
	}
}
