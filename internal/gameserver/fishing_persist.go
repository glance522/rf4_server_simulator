package gameserver

import (
	"encoding/json"
	"fmt"
	"slices"
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
	lastSnapshot     string
	sessionID        uint64 // 仅进程内：检测重连/会话接管
	recoverPending   bool   // 仅进程内：重连后重推同一尾 pending，不重新 roll
	recoverCompleted bool   // 仅登录恢复时自动入护，不改变在线收鱼选择
	// side 表示这是"副竿"状态：快照写到 KV（fishing-rod:<uid>:<gear>），
	// 不写 fishing_state 表，避免副竿空快照覆盖当前竿存档。
	side bool
	gear string // 副竿的竿 GUID，作为 KV 键的一部分
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
		var saveErr error
		if meta.side && meta.gear != "" {
			// 副竿：写自己的 KV 快照，不碰当前竿的存档行。
			saveErr = meta.server.saveSideRodSnapshot(meta.uid, meta.gear, string(raw))
		} else {
			saveErr = meta.server.DB.SaveFishingState(meta.server.dbCtx(), snapshot)
		}
		if saveErr != nil {
			meta.server.log.Warn("钓鱼状态持久化失败", "uid", meta.uid, "side", meta.side, "err", saveErr)
		} else {
			meta.lastSnapshot = string(raw)
		}
	}
	meta.Unlock()
}

// newFishState 首次建内存状态时从 DB 恢复。Keepnet 不在此恢复：DB 是权威源。
func (s *Server) newFishState(uid int64) *FishState {
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

	if s.DB != nil {
		saved, err := s.DB.LoadFishingState(s.dbCtx(), uid)
		if err != nil {
			s.log.Warn("读取钓鱼恢复状态失败", "uid", uid, "err", err)
		} else if saved != nil {
			applyPersistedFishingState(st, saved)
			st.persist.recoverPending = saved.Pending != nil
		}
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
		GearGUID:       st.GearGuid,
		RigType:        st.RigType,
		Loc:            st.Loc,
		Spot:           st.Spot,
		DepthCM:        st.DepthCM,
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
		HookGUID:   f.HookGUID,
		BaitGUIDs:  f.BaitGUIDs,
		BaitsKnown: f.BaitsKnown,
		HappyHour:  f.HappyHour, HappyHourKnown: f.HappyHourKnown,
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
	}
}

func rolledFishFromStore(f *store.FishingFish) *RolledFish {
	if f == nil {
		return nil
	}
	return &RolledFish{
		HookGUID:   f.HookGUID,
		BaitGUIDs:  f.BaitGUIDs,
		BaitsKnown: f.BaitsKnown,
		HappyHour:  f.HappyHour, HappyHourKnown: f.HappyHourKnown,
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

// ---- 副竿持久化（多竿同时作钓）----
//
// 当前竿写 fishing_state 表（上游行为不变）；副竿写 KV：
//   fishing-rod:<uid>:<gear>     单根副竿的状态快照（JSON）
//   fishing-rods-index:<uid>     该玩家所有副竿的 gear 列表（JSON 数组）
//
// 这样重启/重连后，newFishState 能把副竿连同 Pending、CastActive、
// CastTs、BiteAt 一起恢复，并按原来的咬钩时刻重排定时器。

const (
	sideRodKeyPrefix  = "fishing-rod:%d:%s"
	sideRodIndexKey   = "fishing-rods-index:%d"
)

func (s *Server) saveSideRodSnapshot(uid int64, gear, raw string) error {
	if s == nil || s.DB == nil || uid == 0 || gear == "" {
		return nil
	}
	return s.DB.KVSet(s.dbCtx(), fmt.Sprintf(sideRodKeyPrefix, uid, gear), raw)
}

// writeSideRodIndex 写入该玩家副竿 gear 列表。
// gears 由调用方在自己持锁的临界区里算好传进来，函数本身不再取任何锁。
func (s *Server) writeSideRodIndex(uid int64, gears []string) {
	if s == nil || s.DB == nil || uid == 0 {
		return
	}
	slices.Sort(gears)
	raw, err := json.Marshal(gears)
	if err != nil {
		return
	}
	if err := s.DB.KVSet(s.dbCtx(), fmt.Sprintf(sideRodIndexKey, uid), string(raw)); err != nil {
		s.log.Warn("副竿索引写入失败", "uid", uid, "err", err)
	}
}

// loadSideRodStates 按索引把副竿状态读回内存（只读 DB，不碰 s.sideRods）。
// 调用方负责在持有 s.fishMu 的临界区里把返回值合并进 sideRods。
func (s *Server) loadSideRodStates(uid int64) []*FishState {
	if s == nil || s.DB == nil || uid == 0 {
		return nil
	}
	out := make([]*FishState, 0, 4)
	raw, ok, err := s.DB.KVGet(s.dbCtx(), fmt.Sprintf(sideRodIndexKey, uid))
	if err != nil || !ok || raw == "" {
		return out
	}
	var gears []string
	if err := json.Unmarshal([]byte(raw), &gears); err != nil {
		return out
	}
	for _, gear := range gears {
		if gear == "" {
			continue
		}
		snapRaw, found, err := s.DB.KVGet(s.dbCtx(), fmt.Sprintf(sideRodKeyPrefix, uid, gear))
		if err != nil || !found || snapRaw == "" {
			continue
		}
		var saved store.FishingState
		if err := json.Unmarshal([]byte(snapRaw), &saved); err != nil {
			continue
		}
		st := &FishState{Loc: "loc1_02", Spot: 573, Bait: "worm_01", Bonus: fishingSkillBonus{BiteSpeed: 1}}
		st.mu.state = st
		applyPersistedFishingState(st, &saved)
		st.GearGuid = gear
		st.ActiveRod = false
		st.persist.server = s
		st.persist.uid = uid
		st.persist.side = true
		st.persist.gear = gear
		st.persist.lastSnapshot = snapRaw
		st.persist.recoverPending = saved.Pending != nil
		out = append(out, st)
	}
	return out
}
