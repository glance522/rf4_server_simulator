package gameserver

import (
	"context"
	"testing"
	"time"
)

// 多竿：每根竿一个独立状态，切竿只换指针，互不覆盖。
func TestMultiRodKeepsIndependentStatePerRod(t *testing.T) {
	srv, db := newDetachTestServer(t)
	if _, err := db.EnsureProfile(context.Background(), recoveryUID); err != nil {
		t.Fatal(err)
	}
	const (
		rodA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		rodB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	stA := srv.swapActiveRod(recoveryUID, rodA)
	stA.mu.Lock()
	stA.Pending = &RolledFish{FishGUID: "11111111111111111111111111111111", Species: "perch", WeightG: 300, LengthM: .24}
	stA.CastActive, stA.Hooked = true, true
	stA.mu.Unlock()

	stB := srv.swapActiveRod(recoveryUID, rodB)
	if stB == stA {
		t.Fatal("切竿后应拿到独立状态，而不是同一对象")
	}
	stB.mu.Lock()
	hasPendingB := stB.Pending != nil
	stB.mu.Unlock()
	if hasPendingB {
		t.Fatal("新竿不应继承另一根竿的待处理渔获")
	}

	// A 竿的渔获必须留在副竿表里，可被按 GUID 找回。
	if got := srv.pendingStateByGUID(recoveryUID, "11111111111111111111111111111111"); got != stA {
		t.Fatal("副竿上的待处理渔获丢失")
	}

	// 切回 A 竿：状态原样回来。
	back := srv.swapActiveRod(recoveryUID, rodA)
	if back != stA {
		t.Fatal("切回原竿应复用原状态")
	}
	back.mu.Lock()
	pending := back.Pending
	cast := back.CastActive
	back.mu.Unlock()
	if pending == nil || !cast {
		t.Fatal("切回后待处理渔获或抛竿状态丢失")
	}
	stopFishingRecoveryTimers(srv)
}

// 多竿：副竿状态写入 KV，重启后能连同待处理渔获一起恢复。
func TestMultiRodSideRodStateRestoredAfterRestart(t *testing.T) {
	srv, db := newDetachTestServer(t)
	if _, err := db.EnsureProfile(context.Background(), recoveryUID); err != nil {
		t.Fatal(err)
	}
	const (
		rodA = "cccccccccccccccccccccccccccccccc"
		rodB = "dddddddddddddddddddddddddddddddd"
	)
	stA := srv.swapActiveRod(recoveryUID, rodA)
	stA.mu.Lock()
	stA.Pending = &RolledFish{FishGUID: "22222222222222222222222222222222", Species: "perch", WeightG: 400, LengthM: .26}
	stA.CastActive, stA.Hooked, stA.CastTs = true, true, time.Now().Add(-5*time.Second)
	stA.mu.Unlock()
	// 触发一次快照落库（Unlock 内部会比较并写 KV）。
	func() {
		stA.mu.Lock()
		if stA.PushSent {
			stA.PushSent = false
		} else {
			stA.PushSent = true
		}
		stA.mu.Unlock()
	}()

	srv.swapActiveRod(recoveryUID, rodB) // rodA 进副竿表并写 KV
	stopFishingRecoveryTimers(srv)

	restarted := newFishingRecoveryServer(t, db)
	restarted.fishingState(recoveryUID) // 触发副竿恢复
	restarted.fishMu.Lock()
	side := restarted.sideRods[recoveryUID][rodA]
	restarted.fishMu.Unlock()
	if side == nil {
		t.Fatal("重启后副竿状态没有恢复")
	}
	side.mu.Lock()
	pending := side.Pending
	cast := side.CastActive
	side.mu.Unlock()
	if pending == nil || pending.FishGUID != "22222222222222222222222222222222" || !cast {
		t.Fatalf("副竿恢复后状态不完整: pending=%v cast=%v", pending != nil, cast)
	}
	stopFishingRecoveryTimers(restarted)
}
