package gameserver

import (
	"context"
	"database/sql"
	"math"
	"time"

	"rf4server/internal/store"
)

const (
	drinkUseReceiptRetention     = 10 * time.Minute
	drinkUseReceiptCleanupPeriod = 5 * time.Minute
)

func (s *Server) drinkRunKey() string {
	s.drinkRunOnce.Do(func() { s.drinkKey = newFishGUID() })
	return s.drinkKey
}

func (s *Server) drinkOnline(uid int64) bool { return s.liveSessionByUID(uid) != nil }

func (s *Server) drinkMaxGap() time.Duration {
	return max(30*time.Second, 3*s.cfg.KeepaliveInterval())
}

func (sess *Session) updateDrinkBonuses(now time.Time) {
	if sess == nil || sess.Srv == nil || sess.Srv.DB == nil || sess.Userid <= 0 {
		return
	}
	s := sess.Srv
	s.drinkMu.Lock()
	defer s.drinkMu.Unlock()
	_, err := s.DB.AdvanceDrinkBonuses(s.dbCtx(), sess.Userid, s.drinkRunKey(), now,
		s.drinkOnline(sess.Userid), s.drinkMaxGap())
	if err != nil {
		s.log.Warn("酒兴剩余时间未保存", "err", err)
	}
}

// drinkXPBonusTx 在首次结算事务内冻结酒兴经验，回执重放不重新计时或计算。
func (s *Server) drinkXPBonusTx(ctx context.Context, tx *sql.Tx, uid, baseXP int64, now time.Time) (*catchXPBonus, error) {
	states, err := store.AdvanceDrinkBonusesTx(ctx, tx, uid, s.drinkRunKey(), now,
		s.drinkOnline(uid), s.drinkMaxGap())
	if err != nil {
		return nil, err
	}
	var best catchXPBonus
	for _, state := range states {
		rate := drinkRate(state.Category, time.Duration(state.RemainingMillis)*time.Millisecond)
		if rate > best.Multiplier {
			guid := state.SourceGUID
			if guid == "" {
				guid = zeroGUID
			}
			best = catchXPBonus{Kind: 55, GUID: guid, Multiplier: rate}
		}
	}
	best.XP = int64(math.Round(float64(baseXP) * best.Multiplier))
	if best.XP <= 0 {
		return nil, nil
	}
	return &best, nil
}

func (s *Server) runDrinkUseReceiptCleanup(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	if s == nil || s.DB == nil {
		return
	}
	s.cleanupDrinkUseReceipts(time.Now())
	ticker := time.NewTicker(drinkUseReceiptCleanupPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			s.cleanupDrinkUseReceipts(now)
		}
	}
}

func (s *Server) cleanupDrinkUseReceipts(now time.Time) {
	if s == nil || s.DB == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	removed, err := s.DB.CleanupDrinkUseReceipts(ctx, now.Add(-drinkUseReceiptRetention))
	if err != nil {
		s.log.Warn("饮酒事务回执清理失败", "err", err)
		return
	}
	if removed > 0 {
		s.log.Info("饮酒事务回执已清理", "rows", removed)
	}
}
