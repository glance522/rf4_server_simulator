package gameserver

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// 登录只恢复已收鱼的决定；在线原有的入护/放生选择不受影响。
func (s *Server) recoverCompletedCatchOnLogin(sess *Session) {
	if s.DB == nil {
		return
	}
	st := s.fishingState(sess.Userid)
	st.finishMu.Lock()
	st.mu.Lock()
	st.persist.recoverCompleted = completedCatchPending(st)
	st.mu.Unlock()
	st.finishMu.Unlock()
	if err := s.recoverCompletedCatch(sess.Userid, nil); err != nil && !errors.Is(err, store.ErrKeepnetFull) {
		s.log.Warn("待处理渔获恢复失败，原鱼已保留", "uid", sess.Userid, "err", err)
	}
}

func (s *Server) recoverCompletedCatch(uid int64, notify *Session) error {
	if s.DB == nil {
		return nil
	}
	st := s.fishingState(uid)
	st.finishMu.Lock()
	entry, err := s.recoverCompletedCatchLocked(st)
	st.finishMu.Unlock()
	if err == nil && entry != nil && notify != nil {
		notify.Push(4, 22, proto.Args(proto.S("61"), s.keepnetItemTree(notify.pushCtx(), entry, entry.KeepLevel)))
	}
	return err
}

func (s *Server) recoverCompletedCatchLocked(st *FishState) (*store.KeepnetEntry, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.persist.recoverCompleted || !completedCatchPending(st) {
		return nil, nil
	}
	uid := st.persist.uid
	fish := st.Pending
	ctx := s.dbCtx()
	raw, _, err := s.DB.KVGet(ctx, fmt.Sprintf("catch-settlement:%d:%s", uid, strings.ToLower(fish.FishGUID)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("待处理渔获尚未完成结算")
	}
	if err != nil {
		return nil, err
	}
	var receipt catchReceipt
	if err := json.Unmarshal([]byte(raw), &receipt); err != nil {
		return nil, err
	}
	capacity, err := s.keepnetCapacity(ctx, uid)
	if err != nil {
		return nil, err
	}
	level := fish.LocLevel
	if level == "" {
		level = s.currentLevel(uid)
	}
	caughtAt := receipt.CaughtAt
	if caughtAt <= 0 {
		caughtAt = float64(st.CastTs.UnixNano()) / 1e9
		if st.CastTs.IsZero() {
			caughtAt = float64(time.Now().UnixNano()) / 1e9
		}
	}
	entry := &store.KeepnetEntry{UID: uid, EchoGUID: fish.FishGUID, Species: fish.Species,
		NameZh: fish.Name, WeightG: fish.WeightG, LengthM: fish.LengthM, Cat: fish.Grade,
		KeepLoc: st.Loc, KeepSpot: int64(st.Spot), KeepBait: st.Bait, KeepLevel: level, TS: caughtAt}
	kept, err := s.DB.KeepFishingCatchWithCapacity(ctx, entry, receipt.TotalXP, receipt.Silver, capacity)
	if err != nil {
		return nil, err
	}
	st.Pending = nil
	st.Hooked, st.CastActive, st.Resolved = false, false, true
	st.persist.recoverCompleted, st.persist.recoverPending = false, false
	st.cancelBiteTimerLocked()
	st.cancelPendingTimerLocked()
	st.pushing = nil
	if !kept {
		return nil, nil
	}
	copy := *fish
	copy.XP, copy.Silver, copy.LocLevel = receipt.TotalXP, receipt.Silver, level
	st.Keepnet = append(st.Keepnet, &copy)
	return entry, nil
}

func (s *Server) retryCompletedCatchRecovery(sess *Session) {
	if err := s.recoverCompletedCatch(sess.Userid, sess); err != nil && !errors.Is(err, store.ErrKeepnetFull) {
		s.log.Warn("待处理渔获未入护，原鱼已保留", "uid", sess.Userid, "err", err)
	}
}
