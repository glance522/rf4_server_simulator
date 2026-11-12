package gameserver

// 工坊"银币修理要等、金币即时"的到期结算。
//
// 官服口径（官方用户手册·工作坊一节 + 2026-11-11 实机截图）：
//   - 银币修理：物品留在工坊，要等 N 小时，修好自己取回；
//   - 金币修理：立即完成；
//   - 界面上「2 小时 / 即时」两个角标，差的就是 hpopgbjohpk.f3 那一格工时。
//
// 我们的实现是一条"先扣钱、后生效"的最小状态机：
//   1. 21/3 银币分支算出修理完成后的 fields/durability 快照，扣钱，把快照写进
//      workshop_repair_job（剩余工时 = 折算小时数），**不改 item 行**；
//   2. 工时按**在线时长**扣：后台 5 秒扫描只给在线玩家扣，下线即冻结
//      （官服口径：下线倒计时停表）；登录时先把计时基准重置到当前，绝不补扣离线段；
//   3. 扣到 0 之后由 workshopSettleRepairs 把快照写回 item 行，并推 4/22 刷新；
//   4. 结算走惰性——登录、打开工坊（21/1/21/2）、以及后台扫描都会触发，
//      幂等：快照落地和删任务行在同一事务里，重复触发不会二次生效。
//
// 为什么不在 21/3 里就地延迟（比如直接把 item 行改坏）：
// 扣费和生效必须同生共死——先扣钱后建任务是错的，玩家可能在两步之间断线。
// 反过来先建任务后扣钱也不行，任务会白送。所以两者放在同一个事务里，
// 快照在事务内一次写成，到期只是"把已经付过钱的结果落地"。

import (
	"context"
	"fmt"
	"time"

	"rf4server/internal/store"
)

// workshopSilverRepairEnabled 是银币修理排队的总开关。
//
// 做成 tunable 而不是常量：万一客户端对"付了钱但东西没立刻好"的表现不对
// （例如不显示倒计时、或者把物品判成不可用），可以把它置 0 秒级回滚成
// "付钱立即修好"的原行为，不用改代码重新发版。
const tunableWorkshopSilverRepair = "workshop_silver_repair_delay"

// workshopSilverRepairDefault 默认开启：这是官服口径，也是玩家明确要的行为。
const workshopSilverRepairDefault = 1.0

// workshopSilverRepairOn 报告银币修理是否要走排队。
func (s *Server) workshopSilverRepairOn() bool {
	if s == nil {
		return false
	}
	return s.tunableF(tunableWorkshopSilverRepair, workshopSilverRepairDefault) > 0.5
}

// workshopGameHourSeconds 是 1 **游戏**小时折成现实秒的系数。
//
// 世界时钟 24 倍速（world_clock.go 的 gameMinutesPerRealSecond = 0.4，
// spawn.go 的 gameHoursPerRealHr = 24），所以 1 游戏小时 = 150 现实秒。
const workshopGameHourSeconds = float64(gameHourReal) / float64(time.Second)

// workshopRepairSeconds 把工时折算成"需要在线的**现实**秒数"。
//
// 单位铁律（2026-11-12 用户指出 + 抓包互证）：`hpopgbjohpk.f3` 是**游戏内小时**，
// 不是现实小时。三条证据同向：
//
//	官服那单 f3=2，回包锁 RemainMin=120 —— 120 游戏分钟 = 2 游戏小时；
//	锁的 f2 锚点用的是 worldTotalGameMin（世界游戏分钟），两份必须同单位；
//	我们世界时钟 24 倍速，1 游戏小时 = 150 现实秒。
//
// 所以官服"修 2 小时"= 5 现实分钟。旧口径按 1 小时 = 3600 现实秒算，把 6 游戏
// 小时排成 6 现实小时、36 游戏小时排成 36 现实小时 —— 玩家看着界面写「6 小时」
// 就要干等 6 小时现实，这就是"修理时间不对"的另一半。
//
// 工时为 0（理论上不会出现，workshopPartHours 的档位表最小也是 2）时按 1 兜底，
// 避免算出"立刻完工"把排队退化成即时完成。
func workshopRepairSeconds(hours uint32) float64 {
	if hours == 0 {
		hours = 1
	}
	return float64(hours) * workshopGameHourSeconds
}

// workshopPendingRepairJob 返回该物品在办的任务；没有则 (nil, nil)。
//
// 调用方用它做**幂等判定**：客户端会连发两次相同 21/3（2026-11-11 15:26 实测，
// 同 GUID 同品类同件，间隔 17ms）。第二次来时 item 行还是旧的（我们刻意没改），
// 于是磨损仍是原值、价格仍大于 0，看起来像"又要修一次"。必须先查在办任务，
// 命中就按幂等成功回包，否则会排两次队、扣两次钱。
func (s *Server) workshopPendingRepairJob(cctx context.Context, guid string) *store.WorkshopRepairJob {
	if s == nil || s.DB == nil || guid == "" {
		return nil
	}
	job, err := s.DB.WorkshopRepairJobFor(cctx, guid)
	if err != nil || job == nil {
		return nil
	}
	return job
}

// workshopSettleRepairs 扣一次在线工时，再把该玩家所有**已完工**的修理落库，
// 返回应用了快照的物品 GUID。
//
// 先扣时后判定，是为了让"打开工坊那一刻"看到的是真实进度：计时基准上一次刷新
// 可能是几秒前，不补这一下，倒计时就会永远差一个扫描周期。
//
// 调用方必须保证"玩家在线"：这个函数会按 now-tick_ts 扣减，离线时调等于把
// 下线时段算成在线。登录路径要先 ResetWorkshopRepairTick 再进来（见
// recoverWorkshopRepairsOnLogin），此时 delta≈0，不会误扣。
//
// 幂等：快照应用成功后才删任务行，删之前先删了再写库会丢结果；两步在同一事务里，
// 任何一步失败整体回滚，任务留在表里等下一轮重试。
//
// 竞态：同一玩家可能被登录路径和后台扫描同时结算。这里的 upsert/delete 都在
// 事务内，后到的一方会发现任务已不存在（Delete 影响 0 行）——那说明对方已经
// 应用过，本次跳过即可，不会重复写坏数据。
func (s *Server) workshopSettleRepairs(ctx context.Context, uid int64) []string {
	if s == nil || s.DB == nil || uid <= 0 {
		return nil
	}
	now := float64(time.Now().Unix())
	if err := s.DB.TickWorkshopRepairJobs(ctx, uid, now); err != nil {
		s.log.Info("工坊修理：扣减在线工时失败", "uid", uid, "err", err)
	}
	jobs, err := s.DB.DueWorkshopRepairJobs(ctx, uid)
	if err != nil || len(jobs) == 0 {
		return nil
	}
	var settled []string
	for _, job := range jobs {
		if err := s.applyWorkshopRepairJob(ctx, job); err != nil {
			s.log.Info("工坊修理：到期结算失败", "guid", shortGUID(job.GUID), "err", err)
			continue
		}
		settled = append(settled, job.GUID)
	}
	// 任务行没了，锁也该没了。不失效的话，接下来几次物品树下发（4/22 推送、
	// 背包快照）还会在缓存窗口里带着 f24，客户端把修好的东西画成"修理中"。
	if len(settled) > 0 {
		s.invalidateWorkshopRepairLocks(uid)
	}
	return settled
}

// applyWorkshopRepairJob 在事务内把一处到期修理的快照写回 item 行并删除任务。
func (s *Server) applyWorkshopRepairJob(ctx context.Context, job store.WorkshopRepairJob) error {
	row, err := s.DB.GetItemByGUID(ctx, job.GUID)
	if err != nil {
		return err
	}
	if row == nil {
		// 物品已经不存在（卖掉/删号）：任务作废，直接删行。
		tx, terr := beginEconomyTx(ctx, s.DB)
		if terr != nil {
			return terr
		}
		if derr := store.DeleteWorkshopRepairJobTx(ctx, tx, job.GUID); derr != nil {
			_ = tx.Rollback()
			return derr
		}
		return tx.Commit()
	}
	durability := clampFloat(job.Durability, 0, 1)
	// 落地必须把版本戳再抬一档。任务快照里的戳正是**下单那一刻**发给客户端的
	// 值，原样写回去客户端会当成"没有更新"整包丢弃，修好的东西会一直挂在
	// "修理中"；只有戳比客户端手里的大，4/22 那次刷新才会被采纳。
	fields, ferr := FieldsFromJSON(job.FieldsJSON)
	if ferr != nil {
		return ferr
	}
	bumpCreatedTicks(fields, row.WireClass)
	fj, jerr := FieldsToJSON(fields)
	if jerr != nil {
		return jerr
	}
	// 轮子的物理参数（强度/曳力）依赖零件树，快照写回后必须一起重建，
	// 否则会出现"耐久满了但拉力还是旧的"。
	var preparedPerf *store.ReelPerformance
	snapshot := *row
	snapshot.FieldsJSON = fj
	snapshot.Durability = durability
	if row.WireClass == "dgjinoebdop" {
		if perf, perr := s.buildReelPerformance(ctx, &snapshot); perr == nil {
			preparedPerf = perf
		}
	}
	tx, err := beginEconomyTx(ctx, s.DB)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := s.DB.UpdateItemFieldsTx(ctx, tx, row.GUID, fj, row.Qty,
		durability, row.Workmanship, row.RigGUID); err != nil {
		return err
	}
	if preparedPerf != nil {
		if err := s.DB.UpsertReelPerformanceTx(ctx, tx, *preparedPerf); err != nil {
			return err
		}
	}
	if err := store.DeleteWorkshopRepairJobTx(ctx, tx, job.GUID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	s.log.Info("工坊修理：到期生效", "item", row.ItemID, "guid", shortGUID(row.GUID),
		"parts", job.Parts, "silver", job.SilverPaid)
	return nil
}

// workshopPendingRemainingSeconds 返回在办任务的剩余**在线**秒数（<=0 表示已完工）。
func workshopPendingRemainingSeconds(job *store.WorkshopRepairJob) int64 {
	if job == nil {
		return 0
	}
	if job.RemainingS <= 0 {
		return 0
	}
	return int64(job.RemainingS + 0.5)
}

// workshopRemainText 把剩余秒数写成人能读的时长，用于系统提示。
func workshopRemainText(seconds int64) string {
	if seconds <= 0 {
		return "不到 1 分钟"
	}
	if seconds < 3600 {
		m := (seconds + 59) / 60
		return fmt.Sprintf("%d 分钟", m)
	}
	h := (seconds + 3599) / 3600
	return fmt.Sprintf("%d 小时", h)
}

// notifyWorkshopRepairsSettled 把"到期生效"的物品推给该玩家的所有在线会话。
//
// 用 4/22（物品树更新）：客户端收到后会重绘该物品，背包里立刻能看到修好的状态。
// 找不到会话就跳过——离线玩家的结算结果已经写库，下次登录自然读到。
func (s *Server) notifyWorkshopRepairsSettled(uid int64, guids []string) {
	if s == nil || len(guids) == 0 {
		return
	}
	sessions := s.sessionsForUID(uid)
	if len(sessions) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, g := range guids {
		row, err := s.DB.GetItemByGUID(ctx, g)
		if err != nil || row == nil {
			continue
		}
		tree, terr := s.ItemToTree(ctx, row)
		if terr != nil {
			continue
		}
		name := s.lootItemName(ctx, row.ItemID)
		for _, sess := range sessions {
			pushWorkshopItem(sess, tree)
			// 光靠 4/22 玩家看不出"等待结束"这件事——背包里那件物品本来就在，
			// 只是数值被改回来了。补一句系统提示，让"到点了"有明确回执。
			sess.pushSystemChat(fmt.Sprintf("《%s》工坊修理完成", name))
		}
	}
}

// SettleWorkshopRepairsDue 给**在线**玩家扣一次工时并落地已完工的修理，供后台
// 周期任务调用。
//
// 为什么必须按在线名单扫，而不是"全库找 due_ts 到点的行"：工时只在在线时走，
// 离线玩家的 remaining_s 不会减少，扫他们既没意义又会在将来把离线段误算进去
// （只要有人手滑传了 now）。按 OnlineUIDs 逐个来，就不会有第二个入口去碰离线玩家。
//
// 为什么需要它：玩家可能就站在工坊里等倒计时。只靠"打开界面才结算"的话，
// 他得反复开关工坊才能看到修好；有了这轮扫描，到点几秒内就会收到 4/22 刷新。
func (s *Server) SettleWorkshopRepairsDue() {
	if s == nil || s.DB == nil {
		return
	}
	uids := s.OnlineUIDs()
	if len(uids) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, uid := range uids {
		settled := s.workshopSettleRepairs(ctx, uid)
		if len(settled) > 0 {
			s.notifyWorkshopRepairsSettled(uid, settled)
		}
	}
}

// recoverWorkshopRepairsOnLogin 在会话建立时重置计时基准，并落地已完工的修理。
//
// 顺序不能反：Reset 把 tick_ts 拨到当前时刻（离线那段直接蒸发，这就是冻结语义），
// 之后 workshopSettleRepairs 里那次扣时才不会把下线的几小时补扣进去。至于
// "上次下线前刚好扣到 0"的任务，Reset 之后 remaining_s 仍是 0，照样会被结算掉。
//
// 这里不推 4/22——客户端此时还没拿到物品列表，随后的完整同步自然会读到修好的状态。
func (s *Server) recoverWorkshopRepairsOnLogin(sess *Session) {
	if s == nil || sess == nil || s.DB == nil || sess.Userid <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := float64(time.Now().Unix())
	if err := s.DB.ResetWorkshopRepairTick(ctx, sess.Userid, now); err != nil {
		s.log.Info("工坊修理：登录重置计时基准失败", "uid", sess.Userid, "err", err)
	}
	settled := s.workshopSettleRepairs(ctx, sess.Userid)
	if len(settled) > 0 {
		s.log.Info("工坊修理：登录时结算完工任务", "uid", sess.Userid, "items", len(settled))
	}
}
