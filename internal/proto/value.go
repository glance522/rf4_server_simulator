// Package proto 实现 RF4 realtime 协议的字节层：
//
//	帧:   [u32 body_len][u8 type][u64 wire_id][RC4(payload)]   (13B 头明文, 小端)
//	信封: 0x01 + i32 marker(-1 请求/推送 / -2 响应) + u32 call_id
//	      + [u8 main, u8 sub 仅当 marker==-1] + body [+ 1B expect_response 尾]
//	值:   marker 0x00..0x17 的自描述序列化，对象字段按位置编码
//
// 本包提供通用的 Value/Field 树层；生成的强类型模型层位于 internal/wiregen。
package proto

import "fmt"

// Kind 值种类。
type Kind string

const (
	KindNull        Kind = "null"
	KindObj         Kind = "obj"
	KindValList     Kind = "vallist"
	KindDict        Kind = "dict"
	KindArgs        Kind = "args"
	KindArray       Kind = "array" // 0x04：字节数组的数组
	KindU32Arr      Kind = "u32arr"
	KindGuidArr     Kind = "guidarr"
	KindTypedU32Arr Kind = "typed_u32arr"
	KindBytes       Kind = "bytes"
	KindStr3        Kind = "str3"
	KindKV          Kind = "kv"
	KindKVList      Kind = "kvlist"  // 0x03：对象数组容器 + typed KV 元素（401.f4 候选形状）
	KindKVArr       Kind = "kvarr"   // 裸类型化数组：[u16 n] + n×([键][值 marker+载荷])（401.f4 候选形状）
	KindTypedArr    Kind = "typedarr" // 自定义类型名数组：[0x03][类型名][u16 n] + 元素（探测用）
	KindKVEntry     Kind = "kventry" // KV 元素：[01][u32 typeid 400][键字节][值 marker+载荷]
	KindDateTime    Kind = "datetime"
	KindGuid        Kind = "guid"
	KindU32         Kind = "u32"
	KindU64         Kind = "u64"
	KindBool        Kind = "bool"
	KindStr         Kind = "str"
	KindElemNull    Kind = "elem_null"
)

const (
	// TypeSentinelValList 对象 type_id 哨兵：开放式值列表（嵌套值直到流尾）。
	TypeSentinelValList uint32 = 98765

	StrNullShort uint32 = 0xFF
	StrNullU16   uint32 = 0xFFFF
	StrNullU32   uint32 = 0x7FFFFFFF
	BytesNull    uint32 = 0x7FFFFFFF

	MaxDepth = 64
)

// Pair 字典键值对（marker 0x02）。K/V 均可为 nil（short str null）。
type Pair struct {
	K *string
	V *Value
}

// Value 是 marker 值的中性树节点。
//
// GUID/bytes 以 hex 串存储（便于日志查看），
// 编解码时与原始 16 字节/原始字节互转。IsNull 用于 array 元素的空哨兵。
type Value struct {
	Kind Kind

	// Marker 保留 wire marker：
	//   u32: 0x0d/0x10/0x13（默认 0x10）  u64: 0x11/0x12（默认 0x11）
	//   str: 0x14/0x15/0x16（0=按长度自动） guid: 0x0c/0x17（默认 0x0c）
	Marker byte

	IsNull bool // array 元素空哨兵（0x7FFFFFFF 长度）

	Bool bool
	Num  uint64 // u32 / u64 / datetime（FILETIME）
	Str  *string
	Hex  string // guid（32 hex）/ bytes（hex）/ guidarr 元素

	TypeID uint32  // obj
	Class  string  // obj（解码时查表填充；编码时按类查 ops）
	Fields []Field // obj

	Code     *string  // args 容器 code
	TypeName string   // dict 的 type / typed_u32arr 的类型名
	Items    []*Value // vallist / args / u32arr / guidarr / array 元素
	Pairs    []Pair   // dict
	S3       [3]*string
}

// WireError 字节层解析/编码错误。
type WireError struct {
	Op   string
	Pos  int
	Msg  string
	Kind string
}

func (e *WireError) Error() string {
	return fmt.Sprintf("wire %s %s: %s (pos=%d)", e.Op, e.Kind, e.Msg, e.Pos)
}

func errAt(op, kind, msg string, pos int) error {
	return &WireError{Op: op, Kind: kind, Msg: msg, Pos: pos}
}
