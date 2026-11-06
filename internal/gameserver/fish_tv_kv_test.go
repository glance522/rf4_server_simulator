package gameserver

import (
	"bytes"
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

// TestFishTVShape30MatchesProduction 回归：面板 kv_shape=30 的编码字节必须与
// 生产 fishTVKv 完全一致（typed 数组 "135"）。否则面板探测的结果代表不了
// 线上行为——旧实现把 Items 重包成 KindKVList，编码器落到硬编码的
// dejdolfnooh 类型名，字节与生产分叉。
func TestFishTVShape30MatchesProduction(t *testing.T) {
	srv := NewServer(config.Game{}, logging.Discard(), nil)
	sess := newProbeSess(srv)
	table := sess.Table()

	prod, err := proto.EncodeValue(fishTVKv("g_tench", 3280), table)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := proto.EncodeValue(fishTVKvShape(30, "g_tench", 3280), table)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(prod, probe) {
		t.Fatalf("shape30 bytes differ from production fishTVKv:\n prod=%x\nprobe=%x", prod, probe)
	}
	if !bytes.Contains(probe, []byte("135")) {
		t.Fatalf("shape30 must use typed array type name 135, got %x", probe)
	}
}
