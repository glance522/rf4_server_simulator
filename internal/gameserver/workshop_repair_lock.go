package gameserver

// 工坊"银币修理要等"的客户端投影：物品树 f24（时效对象）+ 21/1 的档位位。
//
// 官服口径（2026-11-11 20:10 官服抓包 + 玩家实机口述）：
//   - 银币修理下单后，物品**仍在背包里**，但带一把锁、整体置灰不可用；
//   - 工坊面板里那件东西显示修理剩余时间，约「N 小时」并逐秒递减；
//   - 金币修理即时完成，没有锁。
//
// 客户端怎么认这件事（见 wiregen 里 lcjcffibmbi 的注释与 boat.go 的反向验证）：
//   - knmjphiefdm.falmopiaoca 的判定是「时效对象 != null 且类型位 == 1」才进
//     kItemState_Repairing，也就是那条"在工作坊里 X 天后修理完"；
//   - 剩余时间由 f2/f3 两格算，**两格都只能是 32 位**（见下）。
//
// f2/f3 的口径是**用日志逐帧回溯对出来的**（不是猜的）。2026-11-11 一晚的
// 下发字节与玩家实机表现严格对得上：
//
//	时段         f2                      f3                      玩家看到
//	21:31–21:52  unix 分钟 29906731      剩余现实分钟 60          角标 00:00，物品"秒修好"
//	22:08–22:13  世界游戏分钟 142407567  剩余**游戏**分钟 1440→1320  **13:05 / 19:40 / 20:24，正常倒数**
//	22:43(当前)  int32 上限 2147483647   剩余游戏分钟 1440         角标 00:00
//
// 由此定案：
//   - f2 = **下发时刻的世界游戏分钟**（`worldTotalGameMin`，与 307 f13.f0、
//     keepalive 拉回客户端的 W 同一坐标，今天约 1.42 亿）；
//   - f3 = **剩余时长的游戏分钟数**（现实秒 × 0.4，1 小时 = 1440）。
//
// 一个负数剩余同时解释了两个症状：21:52 那版 f2 用 unix 分钟（2990 万）远小于
// 客户端 W（1.42 亿），剩余被算成负数 → 角标 00:00 且客户端认为"已到期"
// （玩家说的"秒修好/秒换好"），状态位却没人去摘（"00:00 但一直正在维修"）。
//
// 另外两条**实机撞出来的铁律**，别再推翻：
//   - **f2/f3 的 wire 类型只能是 U32。** 22:22 按字段注解 "iingicaldbk =
//     到期/租用结束时间（FILETIME）" 改成 U64 FILETIME → **客户端当场崩**：
//     登录卡在"正在载入个人资料"，会话每 500ms 起一次、推完 2/2 就断。
//     说明它对这一格的 reader 是定长 32 位、不看 op tag。
//   - int32 上限（21.47 亿）也不是"更远的未来"：它比客户端 W 大太多，剩余溢出
//     成垃圾，角标照样 00:00。f2 不是"到期时刻"，是"起算点"，必须贴着 W。
//
// 官服抓包里那组 { f0="workshop_01", f1=1, f2=597, f3=120 } 与本口径不符
// （f2=597 不可能是世界游戏分钟），大概率是解码时该对象结构错位读出的残值；
// 以我们自己在客户端上验证过的组合为准。
//
// 为什么加一层短 TTL 缓存：ItemToTree 是背包/装备/邮件/交易等所有下发路径的
// 唯一出口，一次背包同步就是几十上百件。每件各查一次任务表会把 SQLite 打满，
// 所以按玩家一次取全量、进程内缓存 1 秒，写操作（入队/结算）主动失效。
// 1 秒的陈旧窗口对一个按分钟走的倒计时无影响，而失效保证了"刚下单的那一帧"
// 和"刚修好的那一帧"都拿到正确状态。

import (
	"context"
	"math"
	"sync/atomic"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// workshopRepairLockCacheTTL 是在办任务列表的进程内缓存时长。
const workshopRepairLockCacheTTL = time.Second

// workshopRepairingGrade 是 21/1 里"这件东西正在工坊修理"的档位位。
//
// 官服实测（2026-11-11 20:10）：同一件渔轮送修前 ookkpknacja.f2=4，
// 送修后（21/1 重查三次）稳定为 5。客户端拿这一格把列表行画成带锁、不可点。
const workshopRepairingGrade = 5

// workshopRepairLockEntry 是某个玩家的在办任务快照。
//
// 存的是任务行本身（而不是算好的剩余秒），因为剩余时间随时在变：缓存只用来
// 省掉"查库"这一步，取值那一刻现算，倒计时才跟着走。
type workshopRepairLockEntry struct {
	at   time.Time
	jobs map[string]*store.WorkshopRepairJob
}

// workshopRepairLocks 返回该玩家在办任务（guid → 任务行），走进程内缓存。
//
// 返回 nil（而不是空 map）表示"没有、也查不到"——调用方一律按"没有在办修理"
// 处理：少画一把锁是显示问题，凭空画一把锁会让玩家以为东西取不回来。
func (s *Server) workshopRepairLocks(ctx context.Context, uid int64) map[string]*store.WorkshopRepairJob {
	if s == nil || s.DB == nil || uid <= 0 {
		return nil
	}
	now := time.Now()
	s.repairLockMu.Lock()
	if e, ok := s.repairLockCache[uid]; ok && now.Sub(e.at) < workshopRepairLockCacheTTL {
		jobs := e.jobs
		s.repairLockMu.Unlock()
		return jobs
	}
	s.repairLockMu.Unlock()

	jobs, err := s.DB.PendingWorkshopRepairJobs(ctx, uid)
	if err != nil {
		// 查不到就当没有：锁只是显示状态，不能因为一次查询失败就把
		// 背包下发整条链路拖垮。错误留在日志里，下一轮重试。
		s.log.Info("工坊修理：读取在办任务失败", "uid", uid, "err", err)
		jobs = nil
	}
	s.repairLockMu.Lock()
	if s.repairLockCache == nil {
		s.repairLockCache = make(map[int64]workshopRepairLockEntry)
	}
	s.repairLockCache[uid] = workshopRepairLockEntry{at: now, jobs: jobs}
	s.repairLockMu.Unlock()
	return jobs
}

// invalidateWorkshopRepairLocks 让该玩家的在办任务缓存立即失效。
//
// 入队、结算、删任务之后必须调：这两个时刻客户端紧接着就会读物品树
// （21/3 的响应体 + 4/22 推送），吃到 1 秒前的旧缓存就会"下完单没锁"或
// "修好了还带锁"。
func (s *Server) invalidateWorkshopRepairLocks(uid int64) {
	if s == nil || uid <= 0 {
		return
	}
	s.repairLockMu.Lock()
	delete(s.repairLockCache, uid)
	s.repairLockMu.Unlock()
}

// workshopRepairLockFor 查这件物品是否正在工坊修理，是则返回要下发的时效对象。
//
// 不能拿"本体耐久 < 1"当预筛：修理是按零件报价的，竿柄/摩擦片磨了而本体耐久
// 仍是 1.0 的组合很常见（21/3 请求里明确列了零件），一筛就会漏掉锁。
// 真正的开销在查库，那一步已经按玩家缓存过了，这里剩下的只是一次 map 命中。
func (s *Server) workshopRepairLockFor(ctx context.Context, it *store.Item) *workshopRepairLock {
	if s == nil || it == nil || it.GUID == "" {
		return nil
	}
	jobs := s.workshopRepairLocks(ctx, it.UID)
	job := jobs[it.GUID]
	if job == nil {
		return nil
	}
	remainS := int64(job.RemainingS + 0.5)
	if remainS <= 0 {
		return nil
	}
	// 现实秒 → 游戏分钟（1 现实秒 = 0.4 游戏分钟 = 24 倍速）。
	// 向上取整并保底 1：0 会让客户端认为"已到期"，角标 00:00。
	remainGameMin := int64(math.Ceil(float64(remainS) * gameMinutesPerRealSecond))
	if remainGameMin < 1 {
		remainGameMin = 1
	}
	if remainGameMin > workshopRepairAnchorCeiling {
		remainGameMin = workshopRepairAnchorCeiling
	}
	anchor := worldTotalGameMin(time.Now())
	if anchor > workshopRepairAnchorCeiling {
		anchor = workshopRepairAnchorCeiling
	}
	return &workshopRepairLock{
		StoreID: job.StoreID,
		// f2 = 起算点：下发这一刻的世界游戏分钟，必须和客户端世界时钟 W
		// （307 f13.f0 那套坐标，今天约 1.42 亿）同一个原点。每次下发重算，
		// 所以玩家下线（工时冻结）后重登会自动对表。
		AnchorMin: anchor,
		// f3 = 时长：剩余时长的**游戏分钟**数（现实秒 × 0.4，1 小时 = 1440）。
		// 不是现实分钟——22:08 那版发 1440 时面板显示 24:00 正常倒数，发 60
		// 时角标是 00:00。
		RemainMin: remainGameMin,
	}
}

// workshopRepairAnchorCeiling 是 f2 的饱和上限（int32 上限）。
//
// 只用于"锚点算出来超出 32 位"这种理论情况（worldTotalGameMin 要到 2091 年
// 才追到 21.47 亿）。**不要拿它当"更远的未来"用**：实机试过，比客户端 W 大
// 太多会把剩余算成垃圾，角标照样 00:00。详见文件头的三段字节/现象对照表。
const workshopRepairAnchorCeiling = int64(math.MaxInt32)

// workshopRepairLock 是按客户端时效对象填进 f24 的三格内容。
type workshopRepairLock struct {
	StoreID   string // f0：工坊 id（例如 level_004_workshop），客户端拿它查工坊名
	AnchorMin int64  // f2：起算点，世界游戏分钟（32 位）
	RemainMin int64  // f3：时长，剩余游戏分钟（32 位）
}

// pushWorkshopItem 把一件刚被工坊动过的物品刷给客户端（4/22）。
//
// **args code 必须是 "524"，不是 "61"。** 这不是风格问题，是实机撞出来的：
//
//   - `args("61")` 是"背包物品增量更新"，捡起/制作/钓鱼都走它；
//   - 官服工坊维修那条 4/22 是 `args("524")` + 单件物品树 + f24 时效对象
//     （logs/net_capture_official.jsonl seq=337，591B，f24=lcjcffibmbi{f1=1}）。
//
// 2026-11-11 23:05 实机：发 "61" 时客户端只按普通物品更新处理，**不进
// kItemState_Repairing** —— 下单后工坊面板仍显示"修好了"、没有倒计时，退出重进
// （走 4/5 全量背包）才出现"修理中 + 倒计时"。21/3 响应体、21/1 的 f2=5 当时
// 都是对的，唯独这条推送的 code 错，所以"重连就好了"。
func pushWorkshopItem(sess *Session, tree *proto.Value) {
	if sess == nil || tree == nil {
		return
	}
	sess.Push(4, 22, proto.Args524(tree))
}

// workshopOrderPush422 报告"下单回包之后还要不要补一条 4/22"。
//
// tunable workshop_order_push422：1=照发（默认），0=下单路径不发。
// 到期落地那条 4/22 不受影响——那是对外唯一通道，必须留着。
func workshopOrderPush422(sess *Session) bool {
	if sess == nil || sess.Srv == nil {
		return false
	}
	return sess.Srv.tunableF("workshop_order_push422", 1) != 0
}

// workshopOrderPushDelay 报告下单回包之后隔多久再推 4/22。
//
// 为什么要隔：2026-11-12 00:05 实机对照——
//   - 贴着回包发（0ms）：面板立刻变"修理中 + 倒计时"，但**紧接着弹「发生内部错误」**；
//   - 不发：不弹错，但面板一直"修好了"，要退出重连才变"修理中"。
//
// 也就是说这条推送既是进 kItemState_Repairing 的唯一通道（21/3 回包本身改不动
// 面板，客户端会按 csCreated 把它丢掉），又不能在客户端还站在下单确认回调里
// 的时候塞进去。官服那条 4/22 在 21/3 回包之后 **4.0 秒**才到
// （logs/net_capture_official.jsonl：s2c#334 21/3 回包 → s2c#337 4/22，+4004ms，
// 且它前面还夹着一次 21/4 往返），从来不是同一毫秒。
//
// tunable workshop_order_push_delay_ms，默认 2500ms。
//
// 2500 这个值是按客户端自己的收尾节奏定的（2026-11-12 00:09 实测，`TRACE 收/回`）：
// `21/3` 回包之后，客户端还会连着补两帧——`21/4`（支付确认）与 `21/2`
// （重新诊断报价），都在 **+1.19 秒**左右到达并各自要一次回包。把 `4/22` 放在
// 这两帧之后再发，才不会撞进客户端还没退出的下单事务里。<=0 表示同步发（测试用）。
func workshopOrderPushDelay(sess *Session) time.Duration {
	if sess == nil || sess.Srv == nil {
		return 0
	}
	ms := sess.Srv.tunableF("workshop_order_push_delay_ms", 2500)
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

// workshopStampSeq 是工坊链路上最后一次发出的版本戳。
//
// 只做一件事：保证"发放顺序 = 戳的大小顺序"。光靠 netTicksNow() 不够——
// 一次交互里要把同一件东西下发三四回（勾选预览、下单回包、下完单又来的预览、
// 4/22 推送），间隔可以短到微秒，实测同一进程里两次 netTicksNow() 会撞出
// **完全相同**的值；而客户端是按"戳必须变大"来采纳的，撞上就等于后一棵树被丢弃。
var workshopStampSeq atomic.Uint64

// nextWorkshopStamp 发放工坊链路的下一个版本戳：贴着当前时刻，且严格大于
// 此前发放过的任何值、也严格大于这棵树自己原来的值。
func nextWorkshopStamp(old uint64) uint64 {
	now := uint64(netTicksNow())
	for {
		last := workshopStampSeq.Load()
		next := now
		if next <= last {
			next = last + 1
		}
		if next <= old {
			next = old + 1
		}
		if workshopStampSeq.CompareAndSwap(last, next) {
			return next
		}
	}
}

// bumpWorkshopCreated 把一组物品字段里的版本戳换成工坊链路的下一个号。
//
// 与 bumpCreatedTicks 的区别只有一处：后者的单调性只在本进程内"贴着此刻"，
// 撞上同一微秒就会给出相同的值；工坊这条链路要求跨多次下发严格递增，所以走
// 序号发放。写入库的那一刻也被这条覆盖——任务快照里的戳就是这个号，完工落地
// 时再 bumpCreatedTicks 抬一次，只会更大。
func bumpWorkshopCreated(fields []proto.Field, wireClass string) {
	idx := fieldIdxByCS(wireClass, csCreated)
	if idx < 0 || idx >= len(fields) {
		return
	}
	fields[idx].Num = ptrU64(nextWorkshopStamp(u64Of(fields[idx].Num)))
}

// stampWorkshopTree 把一棵物品树的版本戳（csCreated）换成工坊链路的下一个号。
//
// 为什么不能沿用下单时算好的戳：这条 4/22 是延迟发的（见 workshopOrderPushDelay，
// 默认 2500ms），而客户端在这段窗口里还会补两帧 21/3（预览，回包也带同一件物品、
// 也带锁）和 21/2（重新诊断，各要一次回包）。推送若沿用下单那刻的戳，就与中间
// 那几棵树的戳**打平甚至更小**，客户端按 csCreated 判"不比手上的新"、整包丢弃，
// 于是锁进不去、面板一直停在"修好了"。
//
// 2026-11-12 01:31 那单就是这条：21/4 回包 f21=…955954，之后 21/3 回包回到
// …462105（那版还没 bump，已在 hWorkshopQuote 修掉），4/22 又推 …955954 ——
// 客户端早已见过这个值，推送被丢掉，只有重连才对上。
//
// 入参树必须是本次下发独占的：调用点都是"回包已经编码完、树再无他用"之后再进
// 这里（下单路径与幂等重放路径都满足）。
func stampWorkshopTree(tree *proto.Value) {
	if tree == nil || tree.Kind != proto.KindObj {
		return
	}
	bumpWorkshopCreated(tree.Fields, tree.Class)
}

// scheduleWorkshopItemPush 在 21/3/21/4 回包之后把物品刷新推给客户端。
//
// 这是下单路径唯一该用的入口：不要直接调 pushWorkshopItem，否则会回到"点确认
// 立刻弹内部错误"那个 bug。延迟为 0（测试）时同步发，保证测试里推送的顺序和
// 存在性与线上一致、不引入游离 goroutine。
func scheduleWorkshopItemPush(sess *Session, tree *proto.Value) {
	if sess == nil || tree == nil || !workshopOrderPush422(sess) {
		return
	}
	delay := workshopOrderPushDelay(sess)
	if delay <= 0 {
		stampWorkshopTree(tree)
		pushWorkshopItem(sess, tree)
		return
	}
	go func() {
		time.Sleep(delay)
		if sess.closed.Load() {
			return
		}
		stampWorkshopTree(tree)
		pushWorkshopItem(sess, tree)
	}()
}

// stripWorkshopRepairLock 把时效槽位清成 null。
//
// 只动内存里的树，不碰库：锁本来就是"只动 wire、不落库"的投影（见 ItemToTree
// 末尾），摘掉也只是这一次下发不带而已。
func stripWorkshopRepairLock(obj *proto.Value) bool {
	if obj == nil || obj.Kind != proto.KindObj {
		return false
	}
	idx := fieldIdxByCS(obj.Class, "bkmnbppciao")
	if idx < 0 || idx >= len(obj.Fields) {
		return false
	}
	if obj.Fields[idx].Val == nil {
		return false
	}
	obj.Fields[idx].Val = proto.Null()
	return true
}

// workshopOrderReplyLock 报告 21/4 的回包要不要带时效对象。
//
// **默认不带。** 我们原来回包和 4/22 推送两处都带（当时判断"多给一次不伤"），
// 实机证明是伤的：2026-11-12 00:45:53 修一根耐久 0.9992 的竿，21/4 回包带着
// 锁（605B），客户端**当场弹「发生内部错误」而且没有下发下一个 21/4**——本该在
// +1.19 秒补上的支付确认帧没来，直接跳去 17/4 拆卸。同一晚 00:46:52 换摩擦片
// 那单，回包同样带锁却照常补了 21/4（f3=1463，正是服务端扣的钱）。
//
// 两者的差别是"回包里这件东西有没有真的变"：换件换上去一只新型号，树变了；
// 同型号修理排的是队，回包内容和下单前**一模一样**，再叠一把"已经在修"的锁
// 就自相矛盾。所以回包摘掉锁，让"进入修理中"这个状态只从 4/22 进——
// 那是客户端认的唯一通道，也是官服的做法。
//
// 摘锁之后 01:01:38 那单当场拿到 01:01:40 的 21/4，握手正常。
//
// tunable workshop_order_reply_lock：0=不带（默认），1=带（回滚用）。
func workshopOrderReplyLock(ctx *Ctx) bool {
	if ctx == nil || ctx.Sess == nil || ctx.Sess.Srv == nil {
		return false
	}
	return ctx.Sess.Srv.tunableF("workshop_order_reply_lock", 0) != 0
}

// workshopOrderReplyBytes 生成 21/4 的回包字节。
//
// 摘锁必须在**副本**上做：同一棵树紧接着还要被 scheduleWorkshopItemPush 当作
// 4/22 推出去，而那条推送必须带锁——它是客户端进 kItemState_Repairing 的唯一
// 通道。所以这里编码一次 → 解码成副本 → 摘锁 → 再编码，绝不回写入参 tree。
// 每次下单多一次编解码（几百字节），换的是"回包与推送各自独立"，值得。
func workshopOrderReplyBytes(ctx *Ctx, tree *proto.Value) ([]byte, bool) {
	if ctx == nil || ctx.Sess == nil || tree == nil {
		return nil, false
	}
	table := ctx.Sess.Table()
	raw, err := proto.EncodeValue(tree, table)
	if err != nil {
		return nil, false
	}
	if workshopOrderReplyLock(ctx) {
		return raw, true
	}
	clone, _, derr := proto.DecodeValue(raw, 0, table)
	if derr != nil || clone == nil {
		return nil, false
	}
	if !stripWorkshopRepairLock(clone) {
		return raw, true // 本来就没锁，不必再编一遍
	}
	raw, err = proto.EncodeValue(clone, table)
	if err != nil {
		return nil, false
	}
	return raw, true
}

// applyWorkshopRepairLock 把时效对象写进时效槽位（cs 名 bkmnbppciao）。
//
// 用 cs 名定位而不是写死下标：这个槽位在不同装备类里的下标完全不同——
// 渔轮 dgjinoebdop 是 f24、竿 cnolibojkao 是 f19、前导 oldfpclaabo 是 f14
// （来源：wire_spec.json 里各类的 fields[].name）。写死一个值就会写到别的
// 字段上，比如 cnolibojkao 的 f11 是 odflendafcg（父容器 guid），写进去
// 客户端当场解析失败。
func applyWorkshopRepairLock(class string, obj *proto.Value, lock *workshopRepairLock) bool {
	if obj == nil || obj.Kind != proto.KindObj || lock == nil {
		return false
	}
	idx := fieldIdxByCS(class, "bkmnbppciao")
	if idx < 0 || idx >= len(obj.Fields) {
		return false
	}
	obj.Fields[idx].Val = wiregen.MustMakeObj("lcjcffibmbi", map[int]*proto.Value{
		0: proto.StrVal(lock.StoreID),
		1: proto.U32(1),                      // 1 = 有时效 → 客户端进 kItemState_Repairing
		2: proto.U32(uint32(lock.AnchorMin)), // 这一格只能 32 位，见 workshopRepairAnchorCeiling
		3: proto.U32(uint32(lock.RemainMin)),
	})
	return true
}
