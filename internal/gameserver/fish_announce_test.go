package gameserver

import (
	"testing"
	"time"
)

// 颜色分档：25=蓝冠及以上、26=奖杯、3=达标；未达标不发模板。
func TestFishAnnouncementKind(t *testing.T) {
	cases := []struct {
		name  string
		grade int64
		want  int
	}{
		{"蓝冠", 4, 25},
		{"奖杯", 3, 26},
		{"达标", 2, 3},
		{"未达标", 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fishAnnouncementKind(tc.grade); got != tc.want {
				t.Fatalf("grade=%d: got %d, want %d", tc.grade, got, tc.want)
			}
		})
	}
}

// 入护播报已永久停用：未达标与达标鱼都不再补白色“入护：…”行。
func TestFishKeptDoesNotBroadcast(t *testing.T) {
	srv, sess, gc := newEscapeProbeServer(t)
	srv.notifyFishKept(sess, &RolledFish{FishGUID: "undersized", Species: "g_tench", WeightG: 400, Grade: 1})
	srv.notifyFishKept(sess, &RolledFish{FishGUID: "kept", Species: "g_tench", WeightG: 900, Grade: 2})

	done := make(chan string, 1)
	go func() {
		_, _, payload, err := gc.ReadRecord()
		if err != nil {
			done <- ""
			return
		}
		done <- string(payload)
	}()
	select {
	case payload := <-done:
		if payload != "" {
			t.Fatalf("入护播报应永久停用，仍收到推送: %q", payload)
		}
	case <-time.After(300 * time.Millisecond):
		// 没有推送 = 期望结果
	}
}
