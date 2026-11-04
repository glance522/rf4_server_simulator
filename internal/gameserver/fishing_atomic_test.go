package gameserver

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func TestFailureReceiptReplaysAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failure.db")
	srv, db := newReceiptTestServer(t, path)
	calls := 0
	srv.dispatch.Register(250, 7, func(ctx *Ctx) ([]byte, bool) {
		calls++
		ctx.ReplyFailure("System.IO.IOException", "未保存")
		return []byte{0}, true
	})
	first := newReceiptTestSession(t, srv, 1, "1|2|1000001|failure")
	first.send(250, 7, 42, emptyArgsBody(), 0)
	response := waitReceiptResponse(t, first)
	if response.Marker != -3 || response.CallID != 42 {
		t.Fatalf("失败被包装为成功: %+v", response)
	}
	want := append([]byte(nil), response.Raw...)
	first.send(250, 7, 42, emptyArgsBody(), 0)
	if got := waitReceiptResponse(t, first); !bytes.Equal(got.Raw, want) || calls != 1 {
		t.Fatalf("错误回执未重放: calls=%d raw=%x", calls, got.Raw)
	}
	_ = first.peer.Close()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, reopened := newReceiptTestServer(t, path)
	defer reopened.Close()
	restarted.dispatch.Register(250, 7, func(ctx *Ctx) ([]byte, bool) {
		t.Error("重启后错误回执不应重做业务")
		return nil, false
	})
	second := newReceiptTestSession(t, restarted, 2, "1|2|1000001|failure")
	second.send(250, 7, 42, emptyArgsBody(), 0)
	if got := waitReceiptResponse(t, second); !bytes.Equal(got.Raw, want) {
		t.Fatalf("重启后错误回执改变: %x", got.Raw)
	}
}

func TestFishingKeepFailurePreservesPendingAndReturnsFailure(t *testing.T) {
	srv, db := newDetachTestServer(t)
	sess := newProbeSess(srv)
	st := srv.stateFor(sess.Userid)
	fish := &RolledFish{FishGUID: "11111111111111111111111111111111", Species: "perch", WeightG: 321, LengthM: .2, LocLevel: "level_001_lake"}
	st.mu.Lock()
	st.Pending, st.Hooked = fish, true
	st.mu.Unlock()
	if _, err := db.SQL.Exec(`CREATE TRIGGER fail_catch_log BEFORE INSERT ON catch_log
		BEGIN SELECT RAISE(ABORT,'test write failure'); END`); err != nil {
		t.Fatal(err)
	}
	srv.dispatch.Register(250, 8, func(ctx *Ctx) ([]byte, bool) {
		return srv.fishingKeep(ctx)
	})
	ctx := &Ctx{Sess: sess, Main: 250, Sub: 8, CallID: 77, ExpectResponse: true,
		Tree: rigRequestTree(t, proto.Guid(fish.FishGUID)), Log: logging.Discard()}
	result := srv.dispatch.executeRequest(ctx, true)
	if !result.nativeFailure || len(result.body) < 9 || result.body[1] != 253 {
		t.Fatalf("保存失败仍返回成功: %+v", result)
	}
	st.mu.Lock()
	pending := st.Pending
	st.mu.Unlock()
	if pending == nil || pending.FishGUID != fish.FishGUID {
		t.Fatal("保存失败丢失 pending")
	}
	var count int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM keepnet_items`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("护网未回滚: n=%d err=%v", count, err)
	}
}

func TestCatchReleaseFailureThenRestartPreservesSingleReward(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.Pending = &RolledFish{FishGUID: "44444444444444444444444444444444", Species: "perch", Grade: 2, XP: 100, WeightG: 321}
	st.mu.Unlock()
	if _, err := db.SQL.Exec(`CREATE TRIGGER fail_release BEFORE INSERT ON kv
		WHEN NEW.k LIKE 'catch-release:%' BEGIN SELECT RAISE(ABORT,'release failure'); END`); err != nil {
		t.Fatal(err)
	}
	capture := &ctxReplyCapture{}
	body, handled := srv.fishingRelease(&Ctx{Sess: sess, CallID: 80, Main: 14, Sub: 6,
		ExpectResponse: true, replyCapture: capture, Log: logging.Discard()})
	if !handled || body != nil || !capture.failure {
		t.Fatal("放生保存失败仍报告成功")
	}
	st.mu.Lock()
	pending := st.Pending
	st.mu.Unlock()
	p, _ := db.GetProfile(ctx, uid)
	if pending == nil || p.XP != 0 {
		t.Fatal("失败时鱼或奖励状态未保留")
	}
	if _, err := db.SQL.Exec(`DROP TRIGGER fail_release`); err != nil {
		t.Fatal(err)
	}
	body, handled = srv.fishingRelease(&Ctx{Sess: sess, CallID: 81, Main: 14, Sub: 6,
		ExpectResponse: true, Log: logging.Discard()})
	if !handled || len(body) == 0 {
		t.Fatal("重试放生未成功")
	}
	restarted := newFishingRecoveryServer(t, db)
	_, handled = restarted.fishingRelease(&Ctx{Sess: newProbeSess(restarted), CallID: 82,
		Main: 14, Sub: 6, ExpectResponse: true, Log: logging.Discard()})
	if !handled {
		t.Fatal("重连后放生请求未处理")
	}
	p, _ = db.GetProfile(ctx, uid)
	saved, err := db.LoadFishingState(ctx, uid)
	if p.XP != 50 || err != nil || saved.Pending != nil {
		t.Fatalf("放生后状态不一致: xp=%d state=%+v err=%v", p.XP, saved, err)
	}
}
