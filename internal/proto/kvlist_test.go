package proto

import (
	"bytes"
	"testing"
)

// TestKVListEncode 401.f4 官方 KV 参数数组编码（mdbadlkabhk 对象数组）：
// [0x03]["dejdolfnooh"][u16 n] + n×([01][u32 400][键][值])。
// f4 字段类型是 dejdolfnooh[]，容器必须是数组(marker 0x03)——
// List 容器(0x0A)解析成功但客户端硬转型失败会断连回登录。
func TestKVListEncode(t *testing.T) {
	v := KVList(
		KVEntry{Key: 1, Val: StrVal("g_tench")},
		KVEntry{Key: 2, Val: U32(3280)},
	)
	raw, err := EncodeValue(v, nil)
	if err != nil {
		t.Fatal(err)
	}
	if raw[0] != 0x03 {
		t.Fatalf("容器 marker 必须是 0x03（对象数组）: % x", raw)
	}
	if raw[1] != 11 { // "dejdolfnooh".len
		t.Fatalf("元素类型名长度字节错误: %d", raw[1])
	}
	if !bytes.HasPrefix(raw[2:], []byte("dejdolfnooh")) {
		t.Fatalf("缺元素类型名: % x", raw)
	}
	off := 2 + 11
	if raw[off] != 2 || raw[off+1] != 0 { // u16 count = 2 LE
		t.Fatalf("元素数编码错误: % x", raw[off:off+2])
	}
	// 元素1：[01][400 LE][key=1][0x14 短串 g_tench]
	want1 := append([]byte{0x01, 0x90, 0x01, 0x00, 0x00, 0x01, 0x14, 0x07}, []byte("g_tench")...)
	if !bytes.HasPrefix(raw[off+2:], want1) {
		t.Fatalf("元素1编码错误:\n got % x\nwant % x", raw[off+2:off+2+len(want1)], want1)
	}
	// 元素2：[01][400 LE][key=2][0x10 int 3280=0x0CD0]
	want2 := []byte{0x01, 0x90, 0x01, 0x00, 0x00, 0x02, 0x10, 0xD0, 0x0C, 0x00, 0x00}
	if !bytes.HasSuffix(raw, want2) {
		t.Fatalf("元素2编码错误:\n got % x\nwant % x", raw, want2)
	}
	// 解码侧：0x03 走 args 容器，code=dejdolfnooh、2 个元素。
	got, _, err := DecodeValue(raw, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindArgs || got.Code == nil || *got.Code != "dejdolfnooh" || len(got.Items) != 2 {
		t.Fatalf("解码形状错误: %s code=%v n=%d", got.Kind, got.Code, len(got.Items))
	}
}
