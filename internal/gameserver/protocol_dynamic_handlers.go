package gameserver

import (
	"context"
	"fmt"
	"math"
	"strings"

	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

// registerDynamicProtocolHandlers 注册动态协议命令。
func registerDynamicProtocolHandlers(d *Dispatch) {
	d.Register(12, 1, hHandThrowGroundbait) // 12/1 手抛：手里攥一团窝料直接扔进水里
	d.Register(13, 5, hDynamicHanditemThrow)
	d.Register(13, 6, hDynamicRigSnapshot)
	d.Register(17, 9, hDynamicRodBreak)
	d.Register(17, 10, hRodCutLine)
	d.Register(17, 11, hDynamicRigSnapshot)
	d.Register(3, 40, hDynamicRewardEntry)
	d.Register(3, 41, hDynamicEquippedItems)
	d.Register(1, 10, hDynamicSonar)

	objRule := func(typeID uint32, class string) ResponseContractRule {
		return ResponseContractRule{Kind: proto.KindObj, TypeIDs: []uint32{typeID}, Classes: []string{class}}
	}
	d.RegisterResponseContract(13, 5, objRule(805, "lmbomkliaad"))
	d.RegisterResponseContract(13, 6, objRule(800, "dlbahppbdad"))
	d.RegisterResponseContract(17, 9, objRule(11028, "ikmdmfoojfg"))
	d.RegisterResponseContract(17, 10, objRule(800, "dlbahppbdad"))
	d.RegisterResponseContract(17, 11, objRule(800, "dlbahppbdad"))
	d.RegisterResponseContract(3, 40, objRule(2015, "enikeammfen"))
	d.RegisterResponseContract(3, 41, ResponseContractRule{
		Kind: proto.KindArgs, ArgCode: "61", ArgItemsAreItems: true,
	})
	d.RegisterResponseContract(1, 10, objRule(15003, "efnipphkbce"))
}

// hDynamicHanditemThrow 13/5 handitem/throw。
// 请求 [手持物 Guid, olieodeoaof 投掷目标]，响应 805 lmbomkliaad。
// 距离从目标 f0 的 f32 位模式读取；实际抛出时消耗 1 份已装填物。
func hDynamicHanditemThrow(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	dist := throwDistance(ctx.Tree)
	if math.IsNaN(dist) || math.IsInf(dist, 0) {
		dist = 20
	}
	if dist <= 0 {
		dist = 0
	} else {
		dist = math.Max(1, math.Min(150, dist))
	}

	// handitem_throw_dist_force：用服务端定的抛投距离覆盖客户端报上来的 f0。
	// 实测客户端 13/5 的 f0 恒为 10.0，力度与朝向都没进包（f10 位置块全零），
	// 所以这一发的飞行距离只能由服务端下发。缺省 0=沿用客户端值，仅供单变量实测。
	clientDist := dist
	if forced := srv.tunableF("handitem_throw_dist_force", 0); forced > 0 && dist > 0 {
		dist = math.Max(1, math.Min(150, forced))
	}
	if srv.tunableF("handitem_throw_debug", 0) != 0 {
		if target := findThrowTarget(ctx.Tree); target != nil {
			ctx.Log.Info("13/5 目标原始字段", "tree", debugThrowTree(target, 0))
		}
	}
	handGUID := ""
	for _, g := range deepGUIDs(ctx.Tree) {
		handGUID = normalizeDynamicGUID(g)
		if handGUID != "" && handGUID != zeroGUID {
			break
		}
	}
	// 弹弓抛饵前先把弹药格里的物品实例取出来：窝账必须绑定真正被消耗的
	// 子实例 GUID，不能用弹弓自己的 GUID。消耗完之后引用会被清掉，所以必须在扣料之前读。
	baitItemID, baitGUID := "", ""
	if handGUID != "" && handGUID != zeroGUID {
		if loaded := srv.handitemLoadedGuid(ctx.Sess.pushCtx(), handGUID); loaded != "" && loaded != zeroGUID {
			if row, rerr := srv.DB.GetItemByGUID(ctx.Sess.pushCtx(), loaded); rerr == nil && row != nil {
				baitItemID, baitGUID = row.ItemID, row.GUID
			}
		}
	}
	var upserts []*proto.Value
	var removed []string
	if dist > 0 && handGUID != "" && handGUID != zeroGUID {
		got := srv.consumeHanditemLoadedForThrow(ctx.Sess.pushCtx(), handGUID)
		upserts = got.upserts
		removed = got.removed
	}
	// f2 是姿态块 pmdcpeegbbd：客户端用它写全局时间与玩家生物特征；
	// f4 是本次抛出的物件实例数组，客户端读到新实例才会生成飞行中的饵。
	// 两个字段各由一个 tunable 控制，便于实机单变量验证。实测：姿态块缺省开；抛出物实例缺省改关——
	// 客户端自己会画这一发（关掉后水面仍出现饵团），我们再多塞一个实例就是重复渲染。
	// 窝料系统：这一弹的饵落到落点格子里（通道=catapult）。落点只有距离，
	// 世界坐标取服务端已经存下的本次抛竿落点。
	// 落点：世界坐标 + 朝向。805 里没有独立坐标字段，客户端读的是 f2 那五个 float，
	// 所以这一发落在这里统一算，窝账格子与下发给客户端的落点用的是同一个值。
	var land throwLanding
	if dist > 0 {
		if uid := ctx.Sess.Userid; uid != 0 {
			if st := srv.stateFor(uid); st != nil {
				st.mu.Lock()
				land = handitemThrowLanding(st, dist, srv.tunableF("handitem_throw_fallback_yaw", 0))
				st.mu.Unlock()
			}
		}
	}
	if dist > 0 && handGUID != "" && handGUID != zeroGUID && baitItemID != "" {
		if uid := ctx.Sess.Userid; uid != 0 {
			st := srv.stateFor(uid)
			if st != nil {
				st.mu.Lock()
				gbLoc, gbSpot := st.Loc, st.Spot
				gbX, gbZ, gbHas, gbSrc := groundbaitThrowCell(srv, st, throwLandingIfOn(srv, land))
				st.mu.Unlock()
				// 落点来源进日志：窝到底记在浮标格、玩家脚下格还是算出来的格，
				// 现场一眼能分清楚，调参不用再猜。
				ctx.Log.Info("手持工具窝料落点", "src", gbSrc, "x", gbX, "z", gbZ,
					"has_pos", gbHas, "dist", dist)
				srv.groundbaitDeposit(ctx.Sess.pushCtx(), uid, gbLoc, gbX, gbZ, gbHas, gbSpot,
					baitItemID, 1, "catapult", handitemThrowTxn(handGUID, ctx.Sess.ID, ctx.CallID), baitGUID)
			}
		}
	}
	withPose := srv.tunableF("handitem_throw_pose", 1) != 0
	withProj := srv.tunableF("handitem_throw_projectile", 0) != 0
	withLanding := srv.tunableF("handitem_throw_pose_pos", 0) != 0
	var pose *proto.Value
	if withPose {
		pose = srv.buildPose308(ctx.Sess.pushCtx(), ctx.Sess.Userid)
		if withLanding && land.OK {
			pose = throwLandingPose308(land)
		}
	} else {
		pose = proto.Null()
	}
	thrown := proto.Args(proto.S("61"))
	thrownGUID := ""
	if pj := handitemThrowProjectile(upserts, handGUID); withProj && pj != nil {
		thrown = proto.Args(proto.S("61"), pj)
		thrownGUID = throwProjectileGUID(pj)
	}
	// 一次性自清：把抛出物 GUID 同时放进 f5 移除列表，客户端建好飞行体后
	// 这条临时实例就不会留在弹弓弹药格里（实测缺省 0=不启用）。
	if thrownGUID != "" && srv.tunableF("handitem_throw_selfclean", 0) != 0 {
		removed = append(removed, thrownGUID)
	}
	// 实机标定：0%/5% 五颗球粘成一团，3% 才散得开，20% 直接散到看不见。
	scatter := srv.tunableF("handitem_throw_scatter", 3.0)
	if math.IsNaN(scatter) || math.IsInf(scatter, 0) {
		scatter = 3.0
	}
	scatter = groundbaitClamp(scatter, 0, 100)
	ctx.Log.Info("805 落点与抛出物", "hand", shortGUID(handGUID),
		"dist_m", dist, "dist_client_m", clientDist,
		"scatter_pct", scatter, "upserts", len(upserts), "removed", len(removed),
		"pose", withPose, "pose_landing", withLanding && land.OK,
		"land_m", fmt.Sprintf("%.2f/%.2f", land.X, land.Z),
		"land_yaw", fmt.Sprintf("%.1f", land.YawDeg), "land_dir", land.Direction,
		"land_ok", land.OK, "projectile", thrown.ArgCount())
	obj, err := wiregen.MakeObj("lmbomkliaad", map[int]*proto.Value{
		0: proto.F32(float32(dist)),
		1: proto.F32(float32(scatter)),
		2: pose,
		3: proto.Args(proto.S("61"), upserts...),
		4: thrown,
		5: proto.GuidArr(removed),
	})
	if err != nil {
		return nil, false
	}
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// handitemThrowTxn 弹弓这一发的窝账幂等键。
// CallID 是客户端每次连接重新编号的，只用它一定和上一次登录的早期投掷撞键：
// 实机 13:10 三发的 call=35/36/37 与 02:55 自动实测那几发同号，饵照扣、
// 窝账一笔没记。所以必须再带上网关分配的会话号（每次登录都不一样）。
// throwLanding 这一发抛投的落点：世界坐标 + 朝向（度）。
type throwLanding struct {
	X, Y, Z   float64
	YawDeg    float64
	PitchDeg  float64
	OK        bool
	Direction string // cast=朝上次抛竿落点；fallback=没抛过竿，按兜底偏航
}

// handitemThrowLanding 算出这一发饵的落点世界坐标与朝向。三条证据：
//  1. 805 响应没有独立坐标/角度字段，客户端读的是 f2 那个 float×5+long+int 块；
//     同样形状的类型 325 deggnlnohoh，五个 float 的实测范围是 x[0,366.961]、
//     y[-0.028,0.604]、z[0,462.295]、f3[-178.125,179.75]、f4[-48.125,44.875]，
//     前两位和 z 的跨度正好是地图尺寸、后两位正好是 ±180/±45 的角，就是「坐标+朝向」。
//  2. 客户端 13/5 请求里同一个位置块（olieodeoaof.f10）整块全零：它自己不知道落点，
//     等服务端下发（f0 也恒报 10.0，44/65/90 三种力度的包一模一样）。
//  3. 实机 13:48 五发、dist_force=60 四发，饵团一律朝世界原点飞；我们发的姿态块
//     前三位是体力 0.85/0.99/0.0，落点正好落在原点附近，与观测吻合。
//
// 13/5 不带朝向，所以方向优先取「玩家→上次抛竿落点」，没抛过竿时用
// handitem_throw_fallback_yaw（度，Unity 口径：0=+Z，顺时针为正）兜底。
// 调用方必须持有 st.mu。
func handitemThrowLanding(st *FishState, dist float64, fallbackYawDeg float64) throwLanding {
	var out throwLanding
	if st == nil || dist <= 0 {
		return out
	}
	px, pz, ok := st.PosX, st.PosZ, st.HasPos
	if !ok && st.HasCastPos {
		px, pz, ok = st.CastPosX, st.CastPosZ, true
	}
	if !ok || math.IsNaN(px) || math.IsNaN(pz) || math.IsInf(px, 0) || math.IsInf(pz, 0) {
		return out
	}
	dx, dz := 0.0, 0.0
	out.Direction = "fallback"
	if st.HasCastPos {
		cx, cz := st.CastPosX-px, st.CastPosZ-pz
		if l := math.Hypot(cx, cz); l > 0.5 && !math.IsInf(l, 0) {
			dx, dz = cx/l, cz/l
			out.Direction = "cast"
		}
	}
	if dx == 0 && dz == 0 {
		a := fallbackYawDeg * math.Pi / 180
		dx, dz = math.Sin(a), math.Cos(a)
		out.Direction = "fallback"
	}
	out.X, out.Z = px+dist*dx, pz+dist*dz
	out.Y = 0
	out.YawDeg = math.Atan2(dx, dz) * 180 / math.Pi
	out.PitchDeg = 0
	out.OK = true
	return out
}

// throwLandingPose308 把落点写进 805.f2 的前五个 float（x, y, z, 偏航, 俯仰）。
// 后四位（体力常量/世界钟 FILETIME/标志）沿用姿态块原值，改动面只有一个变量：
// 前三位从体力值换成世界坐标。实机若证明 f2 只是姿态，把
// handitem_throw_pose_pos 调回 0 即可原样退回。
func throwLandingPose308(land throwLanding) *proto.Value {
	return wiregen.MustMakeObj("pnbphpdmppe", map[int]*proto.Value{
		0: proto.F32(float32(land.X)), 1: proto.F32(float32(land.Y)), 2: proto.F32(float32(land.Z)),
		3: proto.F32(float32(land.YawDeg)), 4: proto.F32(float32(land.PitchDeg)),
		5: proto.F32(11.375), 6: proto.F32(0.0),
		7: proto.U64(uint64(proto.WindowsFiletimeNow())),
		8: proto.U32(0),
	})
}

func handitemThrowTxn(hand string, sessID uint64, callID uint32) string {
	return fmt.Sprintf("catapult:%s:%d:%d", shortGUID(hand), sessID, callID)
}

// hDynamicRigSnapshot 为 13/6、17/11 返回当前容器状态的 800 全量快照。
func hDynamicRigSnapshot(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	body := srv.rigFullSnapshotBody(ctx)
	if len(body) == 0 || body[0] != 0x01 {
		return nil, false
	}
	return body, true
}

// hDynamicRodBreak 17/9 rod/break：按断件 GUID 持久化断线/断竿并回 11028。
// 请求参数顺序为 [竿 GUID, 断件 GUID, 物理模型]；没有断件时回退到当前竿。
func hDynamicRodBreak(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	rodGUID := normalizeDynamicGUID(ctx.Tree.ArgItem(0).GuidHex())
	brokenGUID := normalizeDynamicGUID(ctx.Tree.ArgItem(1).GuidHex())
	if brokenGUID == zeroGUID {
		brokenGUID = ""
	}
	st := srv.fishingStateForRequest(ctx)
	if rodGUID == "" || rodGUID == zeroGUID {
		st.mu.Lock()
		rodGUID = normalizeDynamicGUID(st.GearGuid)
		st.mu.Unlock()
	}
	if rodGUID == "" {
		rodGUID = zeroGUID
	}

	// 已结算、等待 14/5 入护 / 14/6 放生的渔获：断件上报迟于落网（同一
	// realtime 连接 TCP 有序，14/4 之后到达的只能是迟到/重复帧）。此刻
	// 不再动装备，也不清鱼，否则 XP 已入账而渔获无处安放（幽灵鱼）；
	// 回零断件结果保持 11028 形状，搏鱼中（CastActive=true）不受影响。
	st.mu.Lock()
	settledPending := completedCatchPending(st)
	st.mu.Unlock()
	if settledPending {
		ctx.Log.Info("17/9 已结算渔获等待处理，忽略断件上报",
			"rod", shortGUID(rodGUID), "part", shortGUID(brokenGUID))
		echo := brokenGUID
		if echo == "" {
			echo = rodGUID
		}
		obj, berr := buildGearBreakResult(&gearBreakOutcome{BrokenGUID: echo})
		if berr != nil {
			return nil, false
		}
		body, berr := proto.EncodeValue(obj, ctx.Sess.Table())
		if berr != nil {
			return nil, false
		}
		return body, true
	}

	if srv.DB != nil && rodGUID != zeroGUID && ctx.DecodeOK {
		outcome, err := srv.breakLineOrRod(ctx.Sess.pushCtx(), ctx.Sess.Userid, rodGUID, brokenGUID)
		if err == nil {
			st.mu.Lock()
			if st.biteTimer != nil {
				st.biteTimer.Stop()
			}
			pending := st.Pending
			st.Pending = nil
			st.CastActive = false
			st.Hooked = false
			st.Resolved = true
			if !outcome.LineBroken && !outcome.ReelDamaged && !outcome.ComponentBroken {
				st.GearGuid = ""
			}
			st.mu.Unlock()
			if outcome.ReelDamaged {
				// 轮子只是降一个耐久阶段，仍留在竿上；本次鱼按超载跑鱼处理。
				srv.bumpFishingCounter(ctx.Sess.Userid, "escapes")
			} else if outcome.LineBroken {
				srv.bumpFishingCounter(ctx.Sess.Userid, "line_breaks")
			} else if outcome.ComponentBroken {
				// 钩/拟饵等终端件断裂：竿仍在手上，钓组摘除损坏件。
				srv.bumpFishingCounter(ctx.Sess.Userid, "component_breaks")
			} else {
				srv.bumpFishingCounter(ctx.Sess.Userid, "rod_breaks")
			}
			obj, berr := buildGearBreakResult(outcome)
			if berr != nil {
				return nil, false
			}
			body, berr := proto.EncodeValue(obj, ctx.Sess.Table())
			if berr != nil {
				return nil, false
			}
			ctx.Log.Info("17/9 渔具超载", "rod", shortGUID(rodGUID), "part", outcome.BreakPart,
				"broken", shortGUID(outcome.BrokenGUID), "line", outcome.LineBroken,
				"comp", outcome.ComponentBroken, "reel_broken", outcome.ReelBroken,
				"line_loss_m", outcome.LineLossM, "removed", len(outcome.RemovedGUIDs))
			// 断件会改变竿/轮的耐久与装配状态，需要全量同步刷新客户端物品模型。
			// 断竿时配件只是摘回背包，11028 的移除清单会让客户端先删掉本地模型，
			// 所以刷新必须排在回包之后，否则配件要到重登才重新出现。
			// 轮子只掉耐久、不进移除清单：不把 BrokenGUID 一并推过去的话，
			// 客户端缓存停在旧耐久，玩家看不到"损坏"。
			syncGUIDs := append([]string(nil), outcome.RemovedGUIDs...)
			// 拆分子饵并回来源堆时，来源堆数量变了但客户端没有收到 4/22，
			// 需要和移除件一起刷新，否则背包仍显示旧数量。
			syncGUIDs = append(syncGUIDs, outcome.StackRefreshGUIDs...)
			if g := strings.TrimSpace(outcome.BrokenGUID); g != "" {
				syncGUIDs = append(syncGUIDs, g)
			}
			reelBroken := outcome.ReelBroken
			frictionWorn := outcome.FrictionWorn
			ctx.AfterResponse(func() {
				ctx.Sess.Push4_22Items(syncGUIDs)
				switch {
				case reelBroken:
					// 轮子本体耐久归零，整体损坏。
					ctx.Sess.pushSystemChat("渔线轮已损坏，请到工坊修理后再使用")
				case frictionWorn:
					// 摩擦片磨穿、泄力失效，本体没坏，换摩擦片即可。
					ctx.Sess.pushSystemChat("摩擦片磨穿、泄力失效，请到工坊更换摩擦片")
				}
			})
			// 断件必丢鱼：11028 只表达装备损坏，另经聊天频道提示跑鱼。
			reason := "断竿"
			switch {
			case outcome.ComponentBroken:
				reason = "钓组组件断裂"
			case outcome.ReelBroken:
				// 轮子本体磨穿，真坏了。
				reason = "渔线轮超载"
			case outcome.FrictionWorn:
				// 摩擦片磨穿、泄力失效：鱼借线跑了，轮子本体没坏。
				reason = "泄力失效"
			case outcome.ReelDamaged:
				// 泄力打滑：鱼借线跑了，摩擦片只是轻微磨损。
				reason = "泄力打滑"
			case outcome.LineBroken && outcome.BreakPart == "leader":
				reason = "断子线"
			case outcome.LineBroken:
				reason = "断主线"
			}
			srv.notifyFishEscape(ctx.Sess, pending, reason)
			return body, true
		}
		ctx.Log.Warn("17/9 断件处理失败", "rod", shortGUID(rodGUID), "err", err)
	}
	if srv.DB != nil {
		ctx.ReplyFailure("System.InvalidOperationException", "断件处理失败，请重新取竿后重试")
		return nil, true
	}

	// 无存储时保留原有兼容响应。
	fallbackGUID := brokenGUID
	if fallbackGUID == "" {
		fallbackGUID = rodGUID
	}
	obj, err := wiregen.MakeObj("ikmdmfoojfg", map[int]*proto.Value{
		0: proto.U32(0),
		1: proto.Guid(fallbackGUID),
		2: proto.Args(proto.S("61")),
		3: proto.Null(),
		4: proto.Null(),
	})
	if err != nil {
		return nil, false
	}
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// hRodCutLine 17/10 rod/cutline：玩家按 X 主动剪线。
// 请求参数为 [竿 GUID, 本次放出的线长（米）]，响应 800 容器全量快照。
// 语义：放出多少米就从竿上主线剩余长度里扣多少米，主线以下的前导、子线、
// 钩、饵、拟饵、浮漂全部随线丢失；主线扣到 0 时整条消失。
// 客户端只负责播剪线动画，物品模型仍以服务端为准。800 快照的三段语义是
// f0 新增物品 / f1 更新物品 / f2 移除 GUID（见 buildRigSnapshot800），剪线没有新物品，
// 所以竿和主线走 f1、剪掉的钩组走 f2。不能把整个背包塞进 f0：那会让客户端把
// 已有物品当新物品重新入库，表现为剪掉的配件又冒出来、甚至落进背包。
// 回包之后再补 4/21 删除和 4/22 竿线刷新，保证搏鱼中的模型也同步。
func hRodCutLine(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	snapshot := func() ([]byte, bool) {
		body := srv.rigFullSnapshotBody(ctx)
		if len(body) == 0 || body[0] != 0x01 {
			return nil, false
		}
		return body, true
	}
	rodGUID, cutM := parseRodGuidInt(ctx.Tree)
	rodGUID = normalizeDynamicGUID(rodGUID)
	if cutM < 0 {
		cutM = 0
	}
	st := srv.fishingStateForRequest(ctx)
	if rodGUID == "" || rodGUID == zeroGUID {
		st.mu.Lock()
		rodGUID = normalizeDynamicGUID(st.GearGuid)
		st.mu.Unlock()
	}
	st.mu.Lock()
	inWater := st.CastActive || st.InWater
	pending := st.Pending
	st.mu.Unlock()
	if rodGUID == "" || rodGUID == zeroGUID {
		ctx.Log.Warn("17/10 剪线被拒：没有在手的鱼竿", "cut_m", cutM)
		return snapshot()
	}
	// 没有放线时不接受剪线：客户端误触也不该把整套钩组清空。
	if cutM <= 0 && !inWater {
		ctx.Log.Info("17/10 剪线被拒：当前没有放线", "rod", shortGUID(rodGUID))
		return snapshot()
	}
	outcome, err := srv.cutLineTx(ctx.Sess.pushCtx(), ctx.Sess.Userid, rodGUID, cutM)
	if err != nil {
		ctx.Log.Warn("17/10 剪线失败", "rod", shortGUID(rodGUID), "cut_m", cutM, "err", err)
		return snapshot()
	}
	// 线已剪断，本次抛投到此结束；线上还挂着鱼就算跑鱼。
	st.mu.Lock()
	if st.biteTimer != nil {
		st.biteTimer.Stop()
	}
	st.Pending = nil
	st.CastActive = false
	st.Hooked = false
	st.Resolved = true
	st.InWater = false
	st.NeedBite = false
	st.mu.Unlock()
	if pending != nil {
		srv.bumpFishingCounter(ctx.Sess.Userid, "escapes")
		srv.notifyFishEscape(ctx.Sess, pending, "剪线跑鱼")
	}
	ctx.Log.Info("17/10 剪线", "rod", shortGUID(rodGUID), "cut_m", cutM,
		"line_loss_m", outcome.LineLossM, "line_left", srv.rodLineLengthLeft(ctx.Sess.pushCtx(), rodGUID),
		"removed", len(outcome.RemovedGUIDs))
	removed := append([]string(nil), outcome.RemovedGUIDs...)
	stackRefresh := append([]string(nil), outcome.StackRefreshGUIDs...)
	ctx.AfterResponse(func() {
		if len(removed) > 0 {
			ctx.Sess.Push4_21(removed)
		}
		if len(stackRefresh) > 0 {
			// 与正常吃饵一致：4/22 只推变更的背包堆，不能夹带全套竿线。
			ctx.Sess.Push4_22ItemGUIDs(stackRefresh)
		} else {
			ctx.Sess.Push4_22Full()
		}
	})
	body, err := proto.EncodeValue(buildRigSnapshot800(nil, outcome.UpdatedTrees, removed), ctx.Sess.Table())
	if err != nil || len(body) == 0 || body[0] != 0x01 {
		return snapshot()
	}
	return body, true
}

// hDynamicRewardEntry 处理 3/40，返回 2015 奖励条目并回显请求中的 guid。
// 没有奖励持久化表时保留空物品 ID，不生成不存在的 systemID。
func hDynamicRewardEntry(ctx *Ctx) ([]byte, bool) {
	guid := zeroGUID
	for _, g := range deepGUIDs(ctx.Tree) {
		if n := normalizeDynamicGUID(g); n != "" {
			guid = n
			break
		}
	}
	obj, err := wiregen.MakeObj("enikeammfen", map[int]*proto.Value{
		0:  proto.U32(0),
		1:  proto.U32(1),
		2:  proto.Guid(guid),
		3:  proto.Guid(""),
		4:  proto.U32(10),
		5:  proto.U32(0),
		6:  proto.U32(0),
		7:  proto.U32(0),
		8:  proto.U64(0),
		9:  proto.U64(0),
		10: proto.U32(0),
		11: proto.Null(),
		12: proto.Null(),
		13: proto.U32(0),
		14: proto.U32(0),
	})
	if err != nil {
		return nil, false
	}
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// hDynamicEquippedItems 3/41：args("61")[container=rig 的 DB 物品树]。
func hDynamicEquippedItems(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	cctx := ctx.Sess.pushCtx()
	rows, err := srv.DB.ListItemsByContainer(cctx, ctx.Sess.Userid, "rig")
	if err != nil {
		return nil, false
	}
	out := proto.Args(proto.S("61"))
	for _, row := range rows {
		tree, terr := srv.ItemToTree(cctx, row)
		if terr != nil {
			continue
		}
		out.Items = append(out.Items, tree)
	}
	body, err := proto.EncodeValue(out, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// hDynamicSonar 处理 1/10，返回 15003 声呐响应并以请求水域名作为 f0。
// 已配置水域生成 15004/15005 回声；未知或空水域返回空网格。
func hDynamicSonar(ctx *Ctx) ([]byte, bool) {
	key := argFirstStr(ctx.Tree)
	obj, err := ctx.Sess.Srv.sonarResponse(ctx.Sess.pushCtx(), key)
	if err != nil {
		return nil, false
	}
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

type handitemThrowConsume struct {
	upserts []*proto.Value
	removed []string
}

// consumeHanditemLoadedForThrow 消耗一份手持工具中的已装填物：
// 只扣 1 份，未用尽更新子实例；用尽删除子实例并清手持物装填引用。
// 弹弓只装 1 颗模式下（handitem_load_one=1），用尽后自动从背包补 1 颗同种饵，
// 保持弹药格始终 1 颗 ready，可连抛；背包无来源堆时不补（弹药格空）。
func (s *Server) consumeHanditemLoadedForThrow(ctx context.Context, parentGUID string) handitemThrowConsume {
	var out handitemThrowConsume
	loaded := s.handitemLoadedGuid(ctx, parentGUID)
	if loaded == "" || loaded == zeroGUID {
		return out
	}
	row, err := s.DB.GetItemByGUID(ctx, loaded)
	if err != nil || row == nil {
		s.setHanditemLoadedRef(ctx, parentGUID, "")
		if parent, perr := s.DB.GetItemByGUID(ctx, parentGUID); perr == nil && parent != nil {
			if tree, terr := s.itemTree(ctx, parent); terr == nil {
				out.upserts = append(out.upserts, tree)
			}
		}
		return out
	}
	itemID, uid := row.ItemID, row.UID // 扣料前记下，供用尽后按同种饵补填
	if s.consumeItemQty(ctx, row, 1) < 1 {
		return out
	}
	left, err := s.DB.GetItemByGUID(ctx, loaded)
	if err == nil && left != nil {
		if tree, terr := s.itemTree(ctx, left); terr == nil {
			out.upserts = append(out.upserts, tree)
		}
		return out
	}
	// 用完：清引用，被消耗的子实例 guid 进 f5。
	s.setHanditemLoadedRef(ctx, parentGUID, "")
	out.removed = append(out.removed, loaded)
	// 自动补 1 颗：新子实例进 f3（客户端据此重建皮兜里的 ready 球），
	// res.UpdatedTrees 已含装好新引用的手持物与减 1 后的背包来源堆。
	if res := s.reloadHanditemAfterThrow(ctx, uid, parentGUID, itemID); res != nil {
		out.upserts = append(out.upserts, res.AddedTrees...)
		out.upserts = append(out.upserts, res.UpdatedTrees...)
		out.removed = append(out.removed, res.RemovedGUIDs...)
		return out
	}
	// 无来源堆可补：弹药格空，回更新后的手持物（引用已清）。
	if parent, perr := s.DB.GetItemByGUID(ctx, parentGUID); perr == nil && parent != nil {
		if tree, terr := s.itemTree(ctx, parent); terr == nil {
			out.upserts = append(out.upserts, tree)
		}
	}
	return out
}

// reloadHanditemAfterThrow 抛完一颗后从背包补 1 颗同种饵到手持工具，保持弹药格
// 始终 1 颗 ready。仅在 handitem_load_one 开启且弹药格已空时生效；按 item_id 找
// 背包里最早的同种来源堆，复用 rigSetComponent 整套装填 wiring（写装填引用、
// 派生索引、快照素材）。找不到可补的来源堆时返回 nil。
func (s *Server) reloadHanditemAfterThrow(ctx context.Context, uid int64, parentGUID, itemID string) *rigSetResult {
	if s.DB == nil || uid == 0 || parentGUID == "" || parentGUID == zeroGUID || itemID == "" {
		return nil
	}
	if s.tunableF("handitem_load_one", 1) == 0 {
		return nil // 整堆模式不自动补填
	}
	if loaded := s.handitemLoadedGuid(ctx, parentGUID); loaded != "" && loaded != zeroGUID {
		if row, _ := s.DB.GetItemByGUID(ctx, loaded); row != nil {
			return nil // 弹药格还有料，不补
		}
	}
	rows, err := s.DB.SQL.QueryContext(ctx,
		`SELECT guid FROM item WHERE uid=? AND item_id=? AND container='backpack' AND qty>0 ORDER BY created_ts,guid`,
		uid, itemID)
	if err != nil {
		return nil
	}
	var candidates []string
	for rows.Next() {
		var g string
		if serr := rows.Scan(&g); serr == nil {
			candidates = append(candidates, g)
		}
	}
	rows.Close()
	for _, g := range candidates {
		row, rerr := s.DB.GetItemByGUID(ctx, g)
		if rerr != nil || row == nil || s.stackCountOf(ctx, row) < 1 {
			continue
		}
		res := s.rigSetComponent(ctx, uid, parentGUID, 0, g, 0,
			rigSetOpts{ParentType: familyHanditem, EnforceCategory: true, HanditemFullStack: true})
		if res.Rejected || res.MountedGUID == "" {
			continue
		}
		s.log.Info("弹弓抛后自动补填", "parent", shortGUID(parentGUID), "item", itemID,
			"src", shortGUID(g), "child", shortGUID(res.MountedGUID))
		return res
	}
	return nil
}

// throwProjectileGUID 读出抛出物实例的 GUID，供 f5 自清使用。
func throwProjectileGUID(pj *proto.Value) string {
	if pj == nil {
		return ""
	}
	i := fieldIdxByCS(pj.Class, csGUID)
	if i < 0 || i >= len(pj.Fields) {
		return ""
	}
	f := pj.Field(i)
	return f.GuidHex()
}

// handitemThrowProjectile 复制一份「这一发抛出去的饵」：沿用装填堆的实例记录，
// 换成新 GUID、数量 1、父件为手持工具本身。805.f4 是物品实例数组，客户端读到
// 新实例才会生成飞行物。这条记录不落库，紧随其后的 13/6 全量快照会把它清掉，
// 不会留在背包里。
func handitemThrowProjectile(upserts []*proto.Value, parentGUID string) *proto.Value {
	var src *proto.Value
	for _, u := range upserts {
		if u != nil && u.Kind == proto.KindObj {
			src = u
			break
		}
	}
	if src == nil {
		return nil
	}
	out := *src
	out.Fields = append([]proto.Field(nil), src.Fields...)
	set := func(csName, op string, apply func(*proto.Field)) {
		i := fieldIdxByCS(src.Class, csName)
		if i < 0 || i >= len(out.Fields) {
			return
		}
		if out.Fields[i].Op == "" {
			out.Fields[i].Op = op
		}
		apply(&out.Fields[i])
	}
	set(csGUID, "GUID16", func(f *proto.Field) { f.Hex = NewItemGUID() })
	set(csParent, "OPTGUID", func(f *proto.Field) { f.Hex = parentGUID })
	set(csQty, "U32", func(f *proto.Field) { f.Num = ptrU64(1) })
	set(csCreated, "U64", func(f *proto.Field) { f.Num = ptrU64(uint64(netTicksNow())) })
	return &out
}

func findThrowTarget(root *proto.Value) *proto.Value {
	var target *proto.Value
	var walk func(*proto.Value)
	walk = func(v *proto.Value) {
		if v == nil || target != nil {
			return
		}
		if v.Kind == proto.KindObj {
			if v.Class == "olieodeoaof" {
				target = v
				return
			}
			for i := range v.Fields {
				walk(v.Fields[i].Val)
			}
			return
		}
		for _, it := range v.Items {
			walk(it)
		}
	}
	walk(root)
	return target
}

func debugThrowTree(v *proto.Value, depth int) string {
	if v == nil || depth > 3 {
		return "…"
	}
	switch v.Kind {
	case proto.KindObj:
		fields := make([]string, 0, v.FieldCount())
		for i := 0; i < v.FieldCount(); i++ {
			f := v.Field(i)
			fields = append(fields, fmt.Sprintf("f%d=%s", i, debugThrowField(f, depth+1)))
		}
		return fmt.Sprintf("obj(%s/%d){%s}", v.Class, v.TypeID, strings.Join(fields, " "))
	case proto.KindArgs:
		items := make([]string, 0, len(v.Items))
		for _, it := range v.Items {
			items = append(items, debugThrowTree(it, depth+1))
		}
		return fmt.Sprintf("args(%s)[%s]", v.ArgCodeStr(), strings.Join(items, " "))
	case proto.KindValList, proto.KindArray:
		items := make([]string, 0, len(v.Items))
		for _, it := range v.Items {
			items = append(items, debugThrowTree(it, depth+1))
		}
		return "list[" + strings.Join(items, " ") + "]"
	default:
		return fmt.Sprintf("kind:%v", v.Kind)
	}
}

func debugThrowField(f proto.Field, depth int) string {
	switch {
	case f.Num != nil:
		return fmt.Sprintf("%d/f32(%.3f)", *f.Num, proto.F32FromBits(f.U32()))
	case f.Str != nil:
		return fmt.Sprintf("%q", *f.Str)
	case f.Hex != "":
		return "guid:" + f.Hex
	case f.Val != nil:
		return debugThrowTree(f.Val, depth)
	default:
		return "null"
	}
}

// throwDistance 从 olieodeoaof.f0 读取投掷距离。
// 字段缺失时使用 20m；值不大于 0 时返回 0，客户端会跳过落点创建。
func throwDistance(root *proto.Value) float64 {
	target := findThrowTarget(root)
	if target == nil || target.FieldCount() == 0 || target.Field(0).Num == nil {
		return 20
	}
	bits := uint32(u64Of(target.Field(0).Num))
	return float64(math.Float32frombits(bits))
}

func normalizeDynamicGUID(g string) string {
	g = strings.ToLower(strings.ReplaceAll(g, "-", ""))
	if len(g) == 32 && g != zeroGUID {
		return g
	}
	if g == zeroGUID {
		return zeroGUID
	}
	return ""
}

// throwLandingIfOn 只在开关打开时把算出来的落点交给选格逻辑，关掉即原样退回。
func throwLandingIfOn(s *Server, land throwLanding) *throwLanding {
	if s.tunableF("handitem_throw_land_cell", 0) == 0 || !land.OK {
		return nil
	}
	return &land
}

// groundbaitThrowCell 决定这一份窝料记在哪一格，调用方必须持有 st.mu。
//
//  1. 已经有浮标落点：记在浮标那一格（实机「先打窝后抛竿」实测走的就是这条）；
//  2. 一竿都没抛过：记在玩家自己站的格子，坐标来自 3/9 每 5 秒的世界坐标上报，
//     实测与 14/2 落点数值逐条吻合（loc10_12 x=362.76 z=236.92 = 3/9 f0/f2）；
//  3. 开关 handitem_throw_land_cell 打开：改用「玩家坐标 + 出手方向 + 距离」算出的格子；
//  4. 什么都没有：has_pos=false，退化成「整个钓点一格」，按衰减系数生效。
//
// 返回 src 只给日志用：cast / player / computed / none。
func groundbaitThrowCell(s *Server, st *FishState, computed *throwLanding) (float64, float64, bool, string) {
	x, z, has, src := st.CastPosX, st.CastPosZ, st.HasCastPos, "cast"
	if !has && s.tunableF("groundbait_throw_pos_fallback", 1) != 0 && st.HasPos {
		x, z, has, src = st.PosX, st.PosZ, true, "player"
	}
	if computed != nil {
		x, z, has, src = computed.X, computed.Z, true, "computed"
	}
	if !has {
		src = "none"
	}
	return x, z, has, src
}

// handThrowItemGUID 取 12/1 第一个参数：被扔出去的物件实例 GUID。
func handThrowItemGUID(root *proto.Value) string {
	for _, g := range deepGUIDs(root) {
		if n := normalizeDynamicGUID(g); n != "" {
			return n
		}
	}
	return ""
}

// handThrowDistance 取 obj19 olieodeoaof.f0 的本次投距。没带就当 0，
// 不像弹弓那样兜底 20 米：手抛的距离是客户端真报上来的（实测 6.108 / 7.567 米），
// 缺了就不该编一个出来。
func handThrowDistance(root *proto.Value) float64 {
	target := findThrowTarget(root)
	if target == nil || target.FieldCount() == 0 || target.Field(0).Num == nil {
		return 0
	}
	d := float64(math.Float32frombits(uint32(u64Of(target.Field(0).Num))))
	if math.IsNaN(d) || math.IsInf(d, 0) || d < 0 {
		return 0
	}
	return math.Min(d, 150)
}

// hHandThrowGroundbait 处理 12/1：手里攥一团窝料直接扔进水里，不用弹弓也不用打窝竿。
//
// 实测 2026-11-07 19:37 loc1_02 两发：请求 = args(被扔物件的实例 GUID, obj19 olieodeoaof)，
// obj19 f0=f1=这一发的投距（6.108 米、7.567 米，随力度真的变，不像弹弓 13/5 的 f0 恒为 10.0），
// f3=当前钓点字符串（loc1_02）。前一条 11/2 是快捷键绑定（hShortcutBind），与消耗无关。
//
// 客户端扔完只在自己本地把背包数字扣掉，服务端不接就会在下次全量快照里把这两份补回来
// （幽灵窝料）。所以这里必须真扣一份，再把这一份记进窝账（通道 hand）。
func hHandThrowGroundbait(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	pushCtx := ctx.Sess.pushCtx()
	guid := handThrowItemGUID(ctx.Tree)
	if guid == "" {
		return nil, false
	}
	row, err := srv.DB.GetItemByGUID(pushCtx, guid)
	if err != nil || row == nil || row.UID != ctx.Sess.Userid {
		return nil, false
	}
	cat, known := srv.itemCategoryOf(pushCtx, row)
	if !known || (cat != catGroundbaitMix && cat != catPVAStick) {
		// 手抛别的物件眼下不归窝料系统管；留一行日志，下次扔别的品类能立刻认出来再接。
		ctx.Log.Info("手抛暂不接管", "item", row.ItemID, "category", cat)
		return nil, false
	}
	if srv.consumeItemQty(pushCtx, row, 1) < 1 {
		ctx.Log.Warn("手抛扣料失败", "item", row.ItemID, "hand", shortGUID(guid))
		return nil, false
	}
	// 客户端本地那一格已经少了：服务端要么更新数量要么删掉，否则重登就复活。
	if left, lerr := srv.DB.GetItemByGUID(pushCtx, guid); lerr == nil && left != nil {
		ctx.Sess.Push4_22ItemGUIDs([]string{guid})
	} else {
		ctx.Sess.Push4_21([]string{guid})
	}
	srv.depositHandThrownGroundbait(ctx, row.ItemID, row.GUID, handThrowDistance(ctx.Tree))
	return nil, false
}

// depositHandThrownGroundbait 把手抛出去的这一团记进窝账：通道 hand，份数缺省 1
// （groundbait_hand_portions 可调），落点沿用弹弓那套三档选择。
// 幂等键带 call_id：同一次请求重发只入一次账。
func (s *Server) depositHandThrownGroundbait(ctx *Ctx, itemID, guid string, dist float64) {
	uid := ctx.Sess.Userid
	st := s.stateFor(uid)
	if st == nil {
		return
	}
	portions := s.tunableF("groundbait_hand_portions", 1)
	if portions <= 0 {
		portions = 1
	}
	st.mu.Lock()
	gbLoc, gbSpot := st.Loc, st.Spot
	var computed *throwLanding
	if s.tunableF("handitem_throw_land_cell", 0) != 0 && dist > 0 && st.HasPos {
		if land := handitemThrowLanding(st, dist, s.tunableF("handitem_throw_fallback_yaw", 0)); land.OK {
			computed = &land
		}
	}
	gbX, gbZ, gbHas, gbSrc := groundbaitThrowCell(s, st, computed)
	st.mu.Unlock()
	if gbLoc == "" {
		return
	}
	ctx.Log.Info("手抛窝料落点", "src", gbSrc, "x", gbX, "z", gbZ, "has_pos", gbHas, "dist", dist)
	s.groundbaitDeposit(ctx.Sess.pushCtx(), uid, gbLoc, gbX, gbZ, gbHas, gbSpot, itemID, portions, "hand",
		fmt.Sprintf("hand:%s:%d:%d", itemID, ctx.Sess.ID, ctx.CallID), guid)
}
