package gameserver

import (
	"context"
	"net"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/tunnel"
)

func progressNotificationSession(t *testing.T, srv *Server, sid uint64) (*Session, <-chan *proto.Envelope) {
	t.Helper()
	serverNet, peerNet := net.Pipe()
	deadline := time.Now().Add(2 * time.Second)
	_ = serverNet.SetDeadline(deadline)
	_ = peerNet.SetDeadline(deadline)
	type handshakeResult struct {
		connection *tunnel.Conn
		err        error
	}
	accepted := make(chan handshakeResult, 1)
	go func() {
		connection, err := tunnel.HandshakeServer(serverNet, "progress-notifications")
		accepted <- handshakeResult{connection, err}
	}()
	peer, err := tunnel.HandshakeClient(peerNet, "progress-notifications")
	if err != nil {
		_ = serverNet.Close()
		_ = peerNet.Close()
		t.Fatal(err)
	}
	result := <-accepted
	if result.err != nil {
		_ = peer.Close()
		t.Fatal(result.err)
	}
	connection := result.connection
	_ = serverNet.SetDeadline(time.Time{})
	_ = peerNet.SetDeadline(time.Time{})
	sess := newSession(srv, sid, "local|notifications|1000001|test", 1000001, connection)
	srv.mu.Lock()
	srv.sessions[sid], srv.byToken[sess.Token] = sess, sess
	srv.mu.Unlock()
	gsSessionsActive.Add(1)
	done := make(chan struct{})
	t.Cleanup(func() {
		close(done)
		sess.Close("test_finished")
		_ = connection.Close()
		_ = peer.Close()
	})
	received := make(chan *proto.Envelope, 64)
	go func() {
		defer close(received)
		for {
			record, _, payload, err := peer.ReadRecord()
			if err != nil {
				return
			}
			if record == tunnel.TypeData {
				if envelope, ok := proto.ParseEnvelope(payload); ok {
					select {
					case received <- envelope:
					case <-done:
						return
					}
				}
			}
		}
	}()
	return sess, received
}

// 用保活包确认此前同步推送已全部收到，避免靠等待判断“没有升级通知”。
func progressNotificationBatch(t *testing.T, sess *Session, received <-chan *proto.Envelope) []*proto.Envelope {
	t.Helper()
	barrier := sess.Push(2, 2, proto.Args524(BuildPose308()))
	if barrier == 0 {
		t.Fatal("推送通道未就绪")
	}
	var batch []*proto.Envelope
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case envelope, ok := <-received:
			if !ok {
				t.Fatal("推送通道提前关闭")
			}
			if envelope.CallID == barrier {
				return batch
			}
			batch = append(batch, envelope)
		case <-timer.C:
			t.Fatal("未收到推送确认包")
		}
	}
}

func progressNotificationCount(batch []*proto.Envelope, sub uint8) int {
	count := 0
	for _, envelope := range batch {
		if envelope.Main == 2 && envelope.Sub == sub {
			count++
		}
	}
	return count
}

func progressNotificationProfile(t *testing.T, srv *Server, level, xp int64) {
	t.Helper()
	const uid = int64(1000001)
	if _, err := srv.DB.EnsureProfile(context.Background(), uid); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.DB.SQL.Exec(`UPDATE profile SET level=?,xp=?,membership=0,membership_exp=0 WHERE uid=?`, level, xp, uid); err != nil {
		t.Fatal(err)
	}
	srv.tunCache = map[string]float64{"xp_gain": 1}
	srv.tunAt = time.Now()
}

func progressNotificationCatch(t *testing.T, sess *Session, fish *RolledFish) *proto.Value {
	t.Helper()
	st := sess.Srv.stateFor(sess.Userid)
	st.mu.Lock()
	st.Pending, st.Hooked, st.CastActive, st.Resolved = fish, true, false, false
	st.RigType = 1
	st.mu.Unlock()
	body, handled := sess.Srv.fishingEndSettle(&Ctx{Sess: sess, Main: 14, Sub: 4, Log: logging.Discard()})
	if !handled || len(body) == 0 {
		t.Fatal("上鱼结算没有响应")
	}
	value, n, err := proto.DecodeValue(body, 0, sess.Table())
	if err != nil || n != len(body) || value.TypeID != 801 {
		t.Fatalf("上鱼结算响应无效: n=%d len=%d err=%v", n, len(body), err)
	}
	return value
}

func assertProgressNotificationLevel(t *testing.T, sess *Session, level, xp int64) {
	t.Helper()
	profile, err := sess.Srv.DB.GetProfile(context.Background(), sess.Userid)
	if err != nil || profile.Level != level || profile.XP != xp {
		t.Fatalf("经验或等级入账不符: profile=%+v want=%d/%d err=%v", profile, level, xp, err)
	}
	snapshot, err := sess.Srv.BuildProfile307(context.Background(), sess.Userid)
	if err != nil || snapshot == nil || snapshot.TypeID != 307 || u32Of(snapshot.Field(2).Num) != uint32(level) {
		t.Fatalf("客户端档案等级与入账不一致: snapshot=%+v want=%d err=%v", snapshot, level, err)
	}
	points, err := sess.Srv.BuildSkillTree311(context.Background(), sess.Userid, ^uint64(0))
	if err != nil || u32Of(points.Field(0).Num) != uint32(skillPointsTotal(level)) {
		t.Fatalf("可用技能点没有随等级更新: tree=%+v err=%v", points, err)
	}
}

func assertProgressCatchProfile(t *testing.T, result *proto.Value, level int64) {
	t.Helper()
	snapshot := result.Field(7).Val
	if snapshot == nil || snapshot.TypeID != 307 || u32Of(snapshot.Field(2).Num) != uint32(level) {
		t.Fatalf("上鱼响应没有返回当前等级档案: snapshot=%+v want=%d", snapshot, level)
	}
}

func assertProgressCatchLevelGain(t *testing.T, result *proto.Value, levelsGained int64) {
	t.Helper()
	stats := result.Field(0).Val
	if stats == nil || stats.ArgCount() != 1 {
		t.Fatal("上鱼响应缺少结算统计")
	}
	experience := stats.ArgItem(0).Field(8).Val
	if experience == nil || experience.TypeID != 310 {
		t.Fatal("上鱼响应缺少经验块")
	}
	if got := u32Of(experience.Field(1).Num); int64(got) != levelsGained {
		t.Fatalf("客户端升级数=%d want=%d", got, levelsGained)
	}
}

func TestProgressNotificationsRequireRealLevelChange(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		oldLevel, oldXP, weight, gain int64
		wantLevel, wantUpgrades       int64
	}{
		{"below_boundary", 5, baseXp(6) - 401, 1120, 400, 5, 0},
		{"exact_boundary", 5, baseXp(6) - 400, 1120, 400, 6, 1},
		{"multiple_levels", 1, 0, 11208, 4000, 6, 1},
		{"level_50_without_boundary", 50, baseXp(50) + 10, 1120, 400, 50, 0},
		{"maximum_level", maxLevel, baseXp(maxLevel) + 10, 1120, 400, maxLevel, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newProgressSrv(t)
			progressNotificationProfile(t, srv, tc.oldLevel, tc.oldXP)
			sess, received := progressNotificationSession(t, srv, 1701)
			fish := &RolledFish{FishGUID: newFishGUID(), Species: "perch", Grade: 2, WeightG: tc.weight, LengthM: .3}
			result := progressNotificationCatch(t, sess, fish)
			assertProgressNotificationLevel(t, sess, tc.wantLevel, tc.oldXP+tc.gain)
			assertProgressCatchProfile(t, result, tc.wantLevel)
			assertProgressCatchLevelGain(t, result, tc.wantLevel-tc.oldLevel)
			batch := progressNotificationBatch(t, sess, received)
			if got := progressNotificationCount(batch, 6); int64(got) != tc.wantUpgrades {
				t.Fatalf("升级推送次数=%d want=%d", got, tc.wantUpgrades)
			}
			if got := progressNotificationCount(batch, 1); got != 1 {
				t.Fatalf("钓获后档案刷新次数=%d want=1", got)
			}
			for _, envelope := range batch {
				if envelope.Main != 2 || envelope.Sub != 6 {
					continue
				}
				value, _, err := proto.DecodeValue(envelope.Body, 0, sess.Table())
				if err != nil || value.ArgCount() != 1 || value.ArgItem(0).TypeID != 311 || u32Of(value.ArgItem(0).Field(0).Num) != uint32(skillPointsTotal(tc.wantLevel)) {
					t.Fatalf("升级推送未带当前可用技能点: value=%+v err=%v", value, err)
				}
			}
		})
	}
}

func TestProgressNotificationsAccumulateAndDoNotReplayLevelUp(t *testing.T) {
	srv, db := newProgressSrv(t)
	progressNotificationProfile(t, srv, 5, baseXp(5))
	sess, received := progressNotificationSession(t, srv, 1702)
	var fish *RolledFish
	for i := int64(1); i <= 3; i++ {
		fish = &RolledFish{FishGUID: newFishGUID(), Species: "perch", Grade: 2, WeightG: 1120, LengthM: .3}
		result := progressNotificationCatch(t, sess, fish)
		wantLevel, wantUpgrades := int64(5), 0
		if i == 3 {
			wantLevel, wantUpgrades = 6, 1
		}
		assertProgressNotificationLevel(t, sess, wantLevel, baseXp(5)+400*i)
		assertProgressCatchProfile(t, result, wantLevel)
		assertProgressCatchLevelGain(t, result, int64(wantUpgrades))
		if got := progressNotificationCount(progressNotificationBatch(t, sess, received), 6); got != wantUpgrades {
			t.Fatalf("第%d条鱼升级推送次数=%d want=%d", i, got, wantUpgrades)
		}
	}
	result := progressNotificationCatch(t, sess, fish)
	assertProgressNotificationLevel(t, sess, 6, baseXp(5)+1200)
	assertProgressCatchProfile(t, result, 6)
	assertProgressCatchLevelGain(t, result, 0)
	if got := progressNotificationCount(progressNotificationBatch(t, sess, received), 6); got != 0 {
		t.Fatalf("同鱼重复结算又推送%d次升级", got)
	}

	// 显式放回同一鱼验证业务回执，不依赖自动恢复。
	restarted := NewServer(srv.cfg, logging.Discard(), nil)
	restarted.SetDB(db)
	next, nextReceived := progressNotificationSession(t, restarted, 1703)
	result = progressNotificationCatch(t, next, fish)
	assertProgressNotificationLevel(t, next, 6, baseXp(5)+1200)
	assertProgressCatchProfile(t, result, 6)
	assertProgressCatchLevelGain(t, result, 0)
	if got := progressNotificationCount(progressNotificationBatch(t, next, nextReceived), 6); got != 0 {
		t.Fatalf("重启后同鱼重复结算又推送%d次升级", got)
	}
	var transactions int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM txn WHERE uid=? AND kind='xp'`, sess.Userid).Scan(&transactions); err != nil || transactions != 3 {
		t.Fatalf("三条鱼入账次数=%d want=3 err=%v", transactions, err)
	}
}

func TestProgressNotificationsPersonalRecordDoesNotMeanLevelUp(t *testing.T) {
	for _, tc := range []struct {
		name         string
		previousBest int64
		isRecord     bool
	}{{"record", 800, true}, {"ordinary", 1600, false}} {
		t.Run(tc.name, func(t *testing.T) {
			srv, db := newProgressSrv(t)
			progressNotificationProfile(t, srv, 50, baseXp(50)+10)
			sess, received := progressNotificationSession(t, srv, 1706)
			ctx := context.Background()
			if err := db.CatchLogInsert(ctx, sess.Userid, "loc", 1, "perch", "", tc.previousBest, .3, "keep", "worm", 2, 10, 10, "level_001_lake"); err != nil {
				t.Fatal(err)
			}
			fish := &RolledFish{FishGUID: newFishGUID(), Species: "perch", Grade: 2, WeightG: 1120, LengthM: .3}
			result := progressNotificationCatch(t, sess, fish)
			receipt, err := srv.settleCatchOnce(ctx, sess.Userid, fish, 1)
			if err != nil || receipt == nil || !receipt.replayed || receipt.isRecord != tc.isRecord || receipt.prevBest != tc.previousBest {
				t.Fatalf("个人纪录事实不符: receipt=%+v err=%v", receipt, err)
			}
			assertProgressNotificationLevel(t, sess, 50, baseXp(50)+410)
			assertProgressCatchProfile(t, result, 50)
			assertProgressCatchLevelGain(t, result, 0)
			if got := progressNotificationCount(progressNotificationBatch(t, sess, received), 6); got != 0 {
				t.Fatalf("未升级的鱼误发%d次升级通知", got)
			}
		})
	}
}

func TestProgressNotificationsAdminSkillRefreshIsSilent(t *testing.T) {
	srv, _ := newProgressSrv(t)
	progressNotificationProfile(t, srv, 5, baseXp(5))
	sess, received := progressNotificationSession(t, srv, 1705)
	srv.execAdminCmd(&AdminCommand{Op: "refresh_skills", UID: sess.Userid})
	batch := progressNotificationBatch(t, sess, received)
	if got := progressNotificationCount(batch, 4); got != 1 {
		t.Fatalf("普通技能刷新次数=%d want=1", got)
	}
	if got := progressNotificationCount(batch, 6); got != 0 {
		t.Fatalf("普通技能刷新误发%d次升级通知", got)
	}
}
