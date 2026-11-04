package gameserver

import (
	"testing"
	"time"
)

// 同一尾鱼在窗口内重复推送会让客户端重建鱼实体，导致"溜的鱼"和"结算的鱼"对不上；
// claimFishPush 保证同一 FishGUID 在窗口内只放行一次。
func TestClaimFishPushDedupe(t *testing.T) {
	sess := &Session{}
	if !sess.claimFishPush("fish-a") {
		t.Fatal("首次推送应放行")
	}
	if sess.claimFishPush("fish-a") {
		t.Fatal("窗口内同 GUID 应被忽略")
	}

	// 窗口过期后同一尾鱼可以再次推送（重新抛竿后重新判到同一条鱼）。
	sess.fishPushMu.Lock()
	sess.fishPushAt = time.Now().Add(-fishPushDedupeWindow - time.Second)
	sess.fishPushMu.Unlock()
	if !sess.claimFishPush("fish-a") {
		t.Fatal("窗口过期后应放行")
	}
	if !sess.claimFishPush("fish-b") {
		t.Fatal("不同鱼应放行")
	}
	if !sess.claimFishPush("") {
		t.Fatal("空 GUID 不应抑制")
	}
}
