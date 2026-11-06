package gameserver

import (
	"testing"
	"time"
)

// TestFishAnnouncedWindow 回归：落地播报去重窗口内命中同鱼，跨窗/异鱼不命中。
func TestFishAnnouncedWindow(t *testing.T) {
	sess := &Session{}
	sess.markFishAnnounced("g1")
	if !sess.fishAnnounced("g1") {
		t.Fatal("刚记录的鱼应在窗口内")
	}
	if sess.fishAnnounced("g2") {
		t.Fatal("其他 GUID 不应命中")
	}
	sess.landedMu.Lock()
	sess.landedAt = time.Now().Add(-fishAnnouncedWindow - time.Second)
	sess.landedMu.Unlock()
	if sess.fishAnnounced("g1") {
		t.Fatal("超窗后不应命中")
	}
}
