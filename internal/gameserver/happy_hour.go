package gameserver

import (
	"fmt"
	"math"
	"math/rand"
	"time"

	"rf4server/internal/store"
)

func (sess *Session) updateHappyHour(now time.Time, closing bool) store.HappyHourStatus {
	if sess == nil || sess.Srv == nil || sess.Srv.DB == nil {
		return store.HappyHourStatus{}
	}
	s := sess.Srv
	sess.happyOpMu.Lock()
	defer sess.happyOpMu.Unlock()
	closing = closing || sess.Closed()
	fishing := false
	if !closing {
		st := s.fishingState(sess.Userid)
		st.mu.Lock()
		fishing = st.CastActive && st.InWater && !st.Resolved
		gear := st.GearGuid
		pending := st.Pending != nil
		st.mu.Unlock()
		if fishing && gear != "" {
			rod, err := s.DB.GetItemByGUID(sess.pushCtx(), gear)
			fishing = err == nil && rod != nil && rod.UID == sess.Userid && rod.Durability > 0
			if fishing {
				def, err := s.itemDefForRow(sess.pushCtx(), rod)
				fishing = err == nil && def != nil && def.Category == gearCatRod && (pending || s.canFishWithBait(sess.pushCtx(), sess.Userid, gear))
			}
		} else {
			fishing = false
		}
	}
	owner := fmt.Sprintf("%d:%d", sess.ID, sess.startedAt.UnixNano())
	status, err := s.DB.AdvanceHappyHour(s.dbCtx(), sess.Userid, owner, now, fishing, !closing,
		max(30*time.Second, 3*s.cfg.KeepaliveInterval()))
	if err != nil {
		s.log.Warn("欢乐时光状态未保存", "err", err)
		return store.HappyHourStatus{}
	}
	sess.happyMu.Lock()
	sess.happyStatus = status
	sess.happyMu.Unlock()
	return status
}

func (sess *Session) currentHappyHour() store.HappyHourStatus {
	if sess == nil {
		return store.HappyHourStatus{}
	}
	sess.happyMu.RLock()
	defer sess.happyMu.RUnlock()
	return sess.happyStatus
}

// happyHourBiteChance 只提高已有吃饵概率，保留低兴趣上限及禁饵。
func happyHourBiteChance(interest, bonus float64) float64 {
	if interest <= 0 {
		return 0
	}
	chance := interest * (1 + clampFloat(bonus, 0, .1))
	if interest <= .1 {
		return math.Min(.1, chance)
	}
	return math.Min(1, chance)
}

func (c fishRollContext) acceptHappyHourBite(interest float64, activities ...float64) bool {
	roll := rand.Float64
	if c.biteRoll != nil {
		roll = c.biteRoll
	}
	chance := happyHourBiteChance(interest, c.happyBite)
	if len(activities) > 0 {
		chance *= clampFloat(activities[0], 0, 1)
	}
	return roll() < chance
}

func happyHourProficiencyRoll(current, roll float64, active bool) float64 {
	if !active {
		return roll
	}
	base := fishingProficiencyGainChance(current)
	chance := math.Min(.95, base*1.25)
	if chance > 0 {
		return roll * base / chance
	}
	return roll
}
