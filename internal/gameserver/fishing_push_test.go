package gameserver

import (
	"context"
	"testing"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/tunnel"
)

func registerBiteTestSession(t *testing.T, srv *Server, sid uint64, c *tunnel.Conn) *Session {
	t.Helper()
	sess := newSession(srv, sid, "local|bite|1000001|test", 1000001, c)
	srv.mu.Lock()
	srv.sessions[sid], srv.byToken[sess.Token] = sess, sess
	srv.mu.Unlock()
	gsSessionsActive.Add(1)
	t.Cleanup(func() { sess.Close("test_finished") })
	return sess
}

// receiveTestPush 只报告目标推送，并持续读取后续通知，避免 net.Pipe 写端阻塞。
func receiveTestPush(peer *tunnel.Conn, main, sub uint8) <-chan *proto.Envelope {
	received := make(chan *proto.Envelope, 1)
	go func() {
		defer close(received)
		reported := false
		for {
			kind, _, payload, err := peer.ReadRecord()
			if err != nil {
				return
			}
			if kind != tunnel.TypeData {
				continue
			}
			env, ok := proto.ParseEnvelope(payload)
			if !reported && ok && env.Main == main && env.Sub == sub {
				received <- env
				reported = true
			}
		}
	}()
	return received
}

// 失败必须保留原鱼，且重启后通过新隧道重推同一 GUID/重量，不能重 roll。
func TestBitePushFailurePreservesFishForReconnect(t *testing.T) {
	for _, failure := range []string{"encoding", "missing_tunnel", "closed_session", "disconnected_tunnel"} {
		t.Run(failure, func(t *testing.T) {
			db := connectTestDB(t)
			srv := newFishingRecoveryServer(t, db)
			connection, peer := cleanupTestTunnel(t, "bite-push-test")
			if failure == "missing_tunnel" {
				connection = nil
			}
			sess := registerBiteTestSession(t, srv, 101, connection)
			if failure == "encoding" {
				srv.table = proto.NewOpsTable(nil, nil)
			} else if failure == "disconnected_tunnel" {
				_ = peer.Close()
			}
			st := srv.stateFor(sess.Userid)
			fish := &RolledFish{FishGUID: "22222222222222222222222222222222", Species: "perch", Grade: 2, WeightG: 321, LengthM: .24}
			st.mu.Lock()
			st.GearGuid = "11111111111111111111111111111111"
			st.CastActive, st.Resolved, st.InWater = true, false, true
			st.CastTs = time.Now().Add(-20 * time.Second)
			if failure == "closed_session" {
				st.Pending = fish
			} else {
				st.Armed = fish
			}
			st.mu.Unlock()
			if failure == "closed_session" {
				sess.Close("test_closed_before_send")
				srv.pushPendingBite(sess, st, fish, st.GearGuid, st.castGeneration)
			} else {
				srv.onBiteTimer(sess.Userid)
			}
			st.mu.Lock()
			if st.PushSent || st.Pending != fish || !st.CastActive || st.Resolved || !st.persist.recoverPending {
				t.Errorf("失败后丢失恢复状态: sent=%v same=%v active=%v resolved=%v recover=%v", st.PushSent, st.Pending == fish, st.CastActive, st.Resolved, st.persist.recoverPending)
			}
			want := *fish
			st.mu.Unlock()
			saved, err := db.LoadFishingState(context.Background(), sess.Userid)
			if err != nil || saved == nil || saved.PushSent || saved.Pending == nil || saved.Pending.FishGUID != fish.FishGUID || saved.Pending.WeightG != fish.WeightG {
				t.Fatalf("失败状态未持久化: saved=%+v err=%v", saved, err)
			}
			sess.Close("test_reconnect")
			stopFishingRecoveryTimers(srv)

			restarted := newFishingRecoveryServer(t, db)
			nextConnection, nextPeer := cleanupTestTunnel(t, "bite-push-test")
			registerBiteTestSession(t, restarted, 102, nextConnection)
			received := receiveTestPush(nextPeer, 14, 14)
			restarted.stateFor(sess.Userid)
			select {
			case env := <-received:
				if env == nil || env.Main != 14 || env.Sub != 14 {
					t.Fatalf("重连未重推咬钩: %+v", env)
				}
				value, _, err := proto.DecodeValue(env.Body, 0, restarted.table)
				if err != nil || value.ArgCount() != 2 {
					t.Fatalf("重推解码失败: %v", err)
				}
				obj := value.ArgItem(1)
				guid, weight, species := obj.Field(0), obj.Field(4), obj.Field(1)
				if guid.Hex != want.FishGUID || weight.U32() != uint32(want.WeightG) || species.StrOr() != want.Species {
					t.Fatalf("重连重判了鱼: guid=%s weight=%d species=%s", guid.Hex, weight.U32(), species.StrOr())
				}
			case <-time.After(time.Second):
				t.Fatal("重连重推超时")
			}
			deadline := time.Now().Add(time.Second)
			for {
				saved, err = db.LoadFishingState(context.Background(), sess.Userid)
				if err == nil && saved != nil && saved.PushSent {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("发送成功未持久化: saved=%+v err=%v", saved, err)
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestBitePushSuccessPreservesNormalFlow(t *testing.T) {
	db := connectTestDB(t)
	srv := newFishingRecoveryServer(t, db)
	connection, peer := cleanupTestTunnel(t, "bite-push-success")
	sess := registerBiteTestSession(t, srv, 201, connection)
	st := srv.stateFor(sess.Userid)
	fish := &RolledFish{FishGUID: "33333333333333333333333333333333", Species: "perch", Grade: 2, WeightG: 456, LengthM: .3}
	st.mu.Lock()
	st.GearGuid = "11111111111111111111111111111111"
	st.CastActive, st.InWater = true, true
	st.CastTs = time.Now().Add(-20 * time.Second)
	st.Armed = fish
	st.mu.Unlock()
	received := receiveTestPush(peer, 14, 14)
	srv.onBiteTimer(sess.Userid)
	select {
	case env := <-received:
		if env == nil || env.Main != 14 || env.Sub != 14 {
			t.Fatalf("未发送正常咬钩推送: %+v", env)
		}
	case <-time.After(time.Second):
		t.Fatal("正常咬钩推送超时")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.PushSent || st.Pending != fish || st.persist.recoverPending || st.pendingTimer != nil || st.Hooked || st.Resolved {
		t.Fatalf("正常推送改变钓鱼流程: sent=%v same=%v recover=%v retry=%v hooked=%v resolved=%v", st.PushSent, st.Pending == fish, st.persist.recoverPending, st.pendingTimer != nil, st.Hooked, st.Resolved)
	}
}

// 隧道写入尚未结束时，恢复查询和重复 timer 不能发送第二份鱼讯。
func TestBitePushInFlightDoesNotRepush(t *testing.T) {
	db := connectTestDB(t)
	srv := newFishingRecoveryServer(t, db)
	connection, peer := cleanupTestTunnel(t, "bite-push-inflight")
	sess := registerBiteTestSession(t, srv, 301, connection)
	st := srv.stateFor(sess.Userid)
	fish := &RolledFish{FishGUID: "44444444444444444444444444444444", Species: "perch", Grade: 2, WeightG: 345, LengthM: .25}
	gear := "11111111111111111111111111111111"
	st.mu.Lock()
	st.GearGuid, st.Pending = gear, fish
	st.CastActive, st.InWater = true, true
	st.CastTs = time.Now().Add(-20 * time.Second)
	st.mu.Unlock()
	done := make(chan struct{})
	go func() { srv.pushPendingBite(sess, st, fish, gear, st.castGeneration); close(done) }()
	deadline := time.Now().Add(time.Second)
	for {
		st.mu.Lock()
		sending := st.pushing == fish
		st.mu.Unlock()
		if sending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("未进入隧道写入")
		}
		time.Sleep(time.Millisecond)
	}
	srv.stateFor(sess.Userid)
	srv.onBiteTimer(sess.Userid)
	srv.repushPending14_14(sess.Userid)
	st.mu.Lock()
	if st.pendingTimer != nil || st.biteTimer != nil || st.PushSent {
		t.Error("正在发送时又排入恢复推送，或过早记录成功")
	}
	st.mu.Unlock()
	rt, _, _, err := peer.ReadRecord()
	if err != nil || rt != tunnel.TypeData {
		t.Fatalf("正常写入失败: %v type=%v", err, rt)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("隧道写入完成后未退出")
	}
	srv.callMu.Lock()
	defer srv.callMu.Unlock()
	if srv.callSeq[sess.Token] != 1 {
		t.Fatalf("发生重复推送: %d", srv.callSeq[sess.Token])
	}
}
