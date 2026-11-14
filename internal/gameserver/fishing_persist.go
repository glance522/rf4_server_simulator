package gameserver

import (
	"encoding/json"
	"sync"
	"time"

	"rf4server/internal/store"
)

// fishStateMutex 在每次状态锁释放后做一次快照比较。钓鱼状态在多个既有
// handler 中直接改字段；集中在该锁上落库，可以覆盖这些变更而不必把持久化
// 逻辑散落到每个业务文件里。内容未变化时只做本地比较，不写 DB。
type fishStateMutex struct {
	sync.Mutex
	state *FishState
}

type fishPersistMeta struct {
	sync.Mutex
	server           *Server
	uid              int64
	rodGUID          string // 不可变快照键；装备失效清GearGuid时仍更新原行
	lastSnapshot     string
	sessionID        uint64 // 仅进程内：检测重连/会话接管
	recoverPending   bool   // 仅进程内：重连后重推同一尾 pending，不重新 roll
	recoverCompleted bool   // 仅登录恢复时自动入护，不改变在线收鱼选择
}

func (m *fishStateMutex) Unlock() {
	st := m.state
	if st == nil {
		m.Mutex.Unlock()
		return
	}
	meta := &st.persist
	meta.Lock()
	snapshot := fishingStateSnapshot(st)
	raw, err := json.Marshal(snapshot)
	same := err == nil && string(raw) == meta.lastSnapshot
	m.Mutex.Unlock()

	if !same && err == nil && meta.server != nil && meta.server.DB != nil {
		if saveErr := meta.server.DB.SaveFishingState(meta.server.dbCtx(), snapshot); saveErr != nil {
			meta.server.log.Warn("钓鱼状态持久化失败", "uid", meta.uid, "err", saveErr)
		} else {
			meta.lastSnapshot = string(raw)
		}
	}
	meta.Unlock()
}

// newFishState 首次建内存状态时从 DB 恢复。Keepnet 不在此恢复：DB 是权威源。
func (s *Server) newFishState(uid int64, saved *store.FishingState) *FishState {
	st := &FishState{
		Loc:  "loc1_02",
		Spot: 573,
		Bait: "worm_01",
		Bonus: fishingSkillBonus{
			BiteSpeed: 1,
		},
	}
	st.mu.state = st
	st.persist.server = s
	st.persist.uid = uid

	if saved != nil {
		st.persist.rodGUID = saved.RodGUID
		applyPersistedFishingState(st, saved)
		st.persist.recoverPending = saved.Pending != nil
	}
	if raw, err := json.Marshal(fishingStateSnapshot(st)); err == nil {
		st.persist.lastSnapshot = string(raw)
	}
	return st
}

func applyPersistedFishingState(st *FishState, saved *store.FishingState) {
	st.GearGuid = saved.GearGUID
	st.RigType = saved.RigType
	if saved.Loc != "" {
		st.Loc = saved.Loc
	}
	if saved.Spot != 0 {
		st.Spot = saved.Spot
	}
	st.DepthCM = saved.DepthCM
	st.CastM = saved.CastM
	st.CastActive = saved.CastActive
	st.CastTs = saved.CastTS
	st.FallTs = saved.FallTS
	st.FallSeen = saved.FallSeen
	st.RigSettled = saved.RigSettled
	st.TickSeen = saved.TickSeen
	st.TickSeq = saved.TickSeq
	st.LastAnim = saved.LastAnim
	st.PushSent = saved.PushSent
	st.Hooked = saved.Hooked
	st.Resolved = saved.Resolved
	st.Pending = rolledFishFromStore(saved.Pending)
	if st.Pending != nil && st.Pending.GearGUID == "" {
		st.Pending.GearGUID = saved.GearGUID
	}
	if saved.Bait != "" {
		st.Bait = saved.Bait
	}
	st.LastSkill = saved.LastSkill
	st.Armed = rolledFishFromStore(saved.Armed)
	st.Bonus = fishingSkillBonus{
		BiteSpeed:      saved.BiteSpeed,
		TrophyLuck:     saved.TrophyLuck,
		FightTolerance: saved.FightTolerance,
	}
	if st.Bonus.BiteSpeed <= 0 {
		st.Bonus.BiteSpeed = 1
	}
	st.BiteAt = saved.BiteAt
}

func fishingStateSnapshot(st *FishState) *store.FishingState {
	if st == nil {
		return nil
	}
	return &store.FishingState{
		UID:            st.persist.uid,
		RodGUID:        st.persist.rodGUID,
		GearGUID:       st.GearGuid,
		RigType:        st.RigType,
		Loc:            st.Loc,
		Spot:           st.Spot,
		DepthCM:        st.DepthCM,
		CastM:          st.CastM,
		CastActive:     st.CastActive,
		CastTS:         st.CastTs,
		FallTS:         st.FallTs,
		FallSeen:       st.FallSeen,
		RigSettled:     st.RigSettled,
		TickSeen:       st.TickSeen,
		TickSeq:        st.TickSeq,
		LastAnim:       st.LastAnim,
		PushSent:       st.PushSent,
		Hooked:         st.Hooked,
		Resolved:       st.Resolved,
		Pending:        rolledFishToStore(st.Pending),
		Bait:           st.Bait,
		LastSkill:      st.LastSkill,
		Armed:          rolledFishToStore(st.Armed),
		BiteSpeed:      st.Bonus.BiteSpeed,
		TrophyLuck:     st.Bonus.TrophyLuck,
		FightTolerance: st.Bonus.FightTolerance,
		BiteAt:         st.BiteAt,
	}
}

func rolledFishToStore(f *RolledFish) *store.FishingFish {
	if f == nil {
		return nil
	}
	return &store.FishingFish{
		GearGUID:    f.GearGUID,
		HookGUID:    f.HookGUID,
		BaitGUIDs:   f.BaitGUIDs,
		BaitsKnown:  f.BaitsKnown,
		BiteQuality: f.BiteQuality, HookHoldP: f.HookHoldP, LuckProtected: f.LuckProtected,
		HookConfirmed: f.HookConfirmed,
		HuntStage:     f.HuntStage, HuntStageKnown: f.HuntStageKnown,
		HappyHour: f.HappyHour, HappyHourKnown: f.HappyHourKnown,
		FishGUID: f.FishGUID,
		Species:  f.Species,
		Grade:    f.Grade,
		WeightG:  f.WeightG,
		LengthM:  f.LengthM,
		XP:       f.XP,
		Silver:   f.Silver,
		LocLevel: f.LocLevel,
		NamedID:  f.NamedID,
		Name:     f.Name,

		QualBaitID:       f.QualBait.ID,
		QualBaitCat:      f.QualBait.Cat,
		QualBaitClass:    f.QualBait.Class,
		QualBaitFlavor:   f.QualBait.Flavor,
		QualBaitLureType: f.QualBait.LureType,
		QualBaitLenCM:    f.QualBait.LenCM,
		QualBaitSizeMM:   f.QualBait.SizeMM,
		CastM:            f.CastM,
		IsNight:          f.IsNight,
		DepthCM:          f.DepthCM,
		OnBoat:           f.OnBoat,
		Drift:            f.Drift,
	}
}

func rolledFishFromStore(f *store.FishingFish) *RolledFish {
	if f == nil {
		return nil
	}
	return &RolledFish{
		GearGUID:    f.GearGUID,
		HookGUID:    f.HookGUID,
		BaitGUIDs:   f.BaitGUIDs,
		BaitsKnown:  f.BaitsKnown,
		BiteQuality: f.BiteQuality, HookHoldP: f.HookHoldP, LuckProtected: f.LuckProtected,
		HookConfirmed: f.HookConfirmed,
		HuntStage:     f.HuntStage, HuntStageKnown: f.HuntStageKnown,
		HappyHour: f.HappyHour, HappyHourKnown: f.HappyHourKnown,
		FishGUID: f.FishGUID,
		Species:  f.Species,
		Grade:    f.Grade,
		WeightG:  f.WeightG,
		LengthM:  f.LengthM,
		XP:       f.XP,
		Silver:   f.Silver,
		LocLevel: f.LocLevel,
		NamedID:  f.NamedID,
		Name:     f.Name,

		QualBait: qualificationBait{
			ID:       f.QualBaitID,
			Cat:      f.QualBaitCat,
			Class:    f.QualBaitClass,
			Flavor:   f.QualBaitFlavor,
			LureType: f.QualBaitLureType,
			LenCM:    f.QualBaitLenCM,
			SizeMM:   f.QualBaitSizeMM,
		},
		CastM:   f.CastM,
		IsNight: f.IsNight,
		DepthCM: f.DepthCM,
		OnBoat:  f.OnBoat,
		Drift:   f.Drift,
	}
}

// resumeFishingState 在首次恢复或重连后的任意钓鱼类请求上重排 timer。
// timer 已过期时 AfterFunc(0) 会立即进入 onBiteTimer，后者仍执行原有落水/
// 就位保护；pending 已存在则只重推原鱼，绝不走判鱼 roll。
func (s *Server) resumeFishingState(st *FishState) {
	if s == nil || st == nil || st.persist.uid == 0 {
		return
	}
	sess := s.liveSessionByUID(st.persist.uid)
	if sess == nil {
		return
	}

	st.mu.Lock()
	resumed := st.persist.sessionID != sess.ID
	if resumed {
		st.persist.sessionID = sess.ID
		if st.Pending != nil && st.CastActive && !st.Resolved {
			st.persist.recoverPending = true
		}
	}
	if !st.CastActive || st.Resolved || st.pushing != nil {
		st.mu.Unlock()
		return
	}
	if st.Pending != nil {
		if !st.persist.recoverPending || st.pendingTimer != nil {
			st.mu.Unlock()
			return
		}
		s.armPendingTimerLocked(st.persist.uid, st, 0)
		st.mu.Unlock()
		return
	}
	if st.PushSent || st.biteTimer != nil {
		st.mu.Unlock()
		return
	}
	due := st.BiteAt
	if due.IsZero() {
		// 快照缺少 BiteAt 时从 CastTs 重新计算；
		// onBiteTimer 的落水保护仍会阻止过早判鱼。
		due = st.CastTs
	}
	if !resumed && due.Before(time.Now()) {
		// 同一会话内 BiteAt 已过期，说明该咬钩时刻已被消费——典型是
		// “饵未入水”到点自毁的 NeedBite 等待态，应等 14/7 补排重新排期。
		// 用过期时刻重排会 arm 出立即触发的 timer，与补排赛跑：timer 先
		// 判鱼推出，补排随后清场抹掉再排一枪——搏鱼中被推出第二尾不同
		// 鱼种，入库与搏鱼所见不符（2026-11-06 23:57 日志实证）。
		// 过期补枪只保留给重启/重连恢复（resumed 为真）。
		st.mu.Unlock()
		return
	}
	s.armBiteTimerLocked(st.persist.uid, st, due)
	st.mu.Unlock()
}
