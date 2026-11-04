package proto

// builders.go 提供 marker 值的构造入口。调用点应使用这些构造器，
// 避免手写不完整的 Value 导致编码阶段才暴露错误。

func Null() *Value { return &Value{Kind: KindNull} }

func Bool(v bool) *Value { return &Value{Kind: KindBool, Bool: v} }

// U32 0x0d/0x10/0x13（默认 0x10）。
func U32(v uint32, marker ...byte) *Value {
	m := byte(0x10)
	if len(marker) > 0 {
		m = marker[0]
	}
	return &Value{Kind: KindU32, Marker: m, Num: uint64(v)}
}

// U64 0x11/0x12（默认 0x11）。
func U64(v uint64, marker ...byte) *Value {
	m := byte(0x11)
	if len(marker) > 0 {
		m = marker[0]
	}
	return &Value{Kind: KindU64, Marker: m, Num: v}
}

// F32 float32：wire 上用 u32 承载位模式。
func F32(v float32) *Value { return &Value{Kind: KindU32, Marker: 0x10, Num: uint64(F32Bits(v))} }

// F64 float64：wire 上用 u64 承载位模式。
func F64(v float64) *Value { return &Value{Kind: KindU64, Marker: 0x11, Num: F64Bits(v)} }

// DateTime 0x0b（u64 FILETIME）。
func DateTime(filetime int64) *Value { return &Value{Kind: KindDateTime, Num: uint64(filetime)} }

// Str 字符串。marker=0 按长度自动选 0x14/0x16；nil → null 串。
func Str(v *string, marker ...byte) *Value {
	m := byte(0)
	if len(marker) > 0 {
		m = marker[0]
	}
	return &Value{Kind: KindStr, Marker: m, Str: v}
}

func StrVal(v string) *Value { return Str(&v) }

// Guid GUID（16 字节，hex 串输入）。marker 0x0c 默认 / 0x17 可选。
func Guid(hex string, marker ...byte) *Value {
	m := byte(0x0C)
	if len(marker) > 0 {
		m = marker[0]
	}
	return &Value{Kind: KindGuid, Marker: m, Hex: normalizeHex(hex)}
}

func GuidArr(hexes []string) *Value {
	items := make([]*Value, 0, len(hexes))
	for _, h := range hexes {
		items = append(items, &Value{Kind: KindGuid, Hex: normalizeHex(h)})
	}
	return &Value{Kind: KindGuidArr, Items: items}
}

// BytesOf 0x08 字节数组（raw 输入）。
func BytesOf(raw []byte) *Value { return &Value{Kind: KindBytes, Hex: hexEncode(raw)} }

// BytesHex 0x08 字节数组（hex 输入）。
func BytesHex(hex string) *Value { return &Value{Kind: KindBytes, Hex: normalizeHex(hex)} }

// Args 0x03 参数列表（RF4 里 code 恒为 "524"）。
func Args(code *string, items ...*Value) *Value {
	return &Value{Kind: KindArgs, Code: code, Items: items}
}

// Args524 最常用：code="524" 的空参数列表（boot 0/1 等）。
func Args524(items ...*Value) *Value {
	code := "524"
	return Args(&code, items...)
}

// DictOf 0x02 字典。
func DictOf(typeName string, pairs ...Pair) *Value {
	return &Value{Kind: KindDict, TypeName: typeName, Pairs: pairs}
}

// ArrayOf 0x04 字节数组的数组（nil 元素 → 空哨兵）。
func ArrayOf(hexes []string) *Value {
	items := make([]*Value, 0, len(hexes))
	for _, h := range hexes {
		if h == "" {
			items = append(items, &Value{Kind: KindBytes, IsNull: true})
			continue
		}
		items = append(items, &Value{Kind: KindBytes, Hex: normalizeHex(h)})
	}
	return &Value{Kind: KindArray, Items: items}
}

func U32Arr(v []uint32) *Value {
	items := make([]*Value, 0, len(v))
	for _, x := range v {
		items = append(items, &Value{Kind: KindU32, Num: uint64(x)})
	}
	return &Value{Kind: KindU32Arr, Items: items}
}

func TypedU32Arr(typeName string, v []uint32) *Value {
	val := U32Arr(v)
	val.Kind = KindTypedU32Arr
	val.TypeName = typeName
	return val
}

func Str3(a, b, c *string) *Value { return &Value{Kind: KindStr3, S3: [3]*string{a, b, c}} }

func KV(k, v *string) *Value { return &Value{Kind: KindKV, S3: [3]*string{k, v}} }

// KVEntry 客户端 dejdolfnooh 模板参数条目。Key 为 ncimfffikjb 键位
// （1=鱼种key 字符串 2=重量克 int 3/4/7=int 6=榜单类型字符串）。
type KVEntry struct {
	Key byte
	Val *Value
}

// KVList 构造 401.f4 官方 KV 参数数组：对象数组容器（marker 0x03）+
// typed KV 元素。值用 StrVal（短串 0x14）与 U32（int）。
func KVList(entries ...KVEntry) *Value {
	return KVListCustom(kvListElementType, entries...)
}

// KVListCustom 同 KVList 但允许自定义元素类型名（探测注册表用）。
func KVListCustom(typeName string, entries ...KVEntry) *Value {
	items := make([]*Value, 0, len(entries))
	for _, e := range entries {
		items = append(items, &Value{Kind: KindKVEntry, Marker: e.Key, Items: []*Value{e.Val}})
	}
	return &Value{Kind: KindTypedArr, TypeName: typeName, Items: items}
}

func ElemNull() *Value { return &Value{Kind: KindElemNull} }

// ---- 字段级构造 ----

// FStr STR/U16STR/U32N 字段。
func FStr(op string, v *string) Field { return Field{Op: op, Str: v} }

// FNum U32/U64/BYTE/U16 字段。
func FNum(op string, v uint64) Field { return Field{Op: op, Num: &v} }

// FOpt OPT 字段（存在哨兵）。
func FOpt(v uint8) Field { return Field{Op: "OPT", Num: ptrU64(uint64(v))} }

// FOptGuid OPTGUID 字段（hex 为空 = 未设置）。
func FOptGuid(hex string) Field { return Field{Op: "OPTGUID", Hex: normalizeHex(hex)} }

// FGuid GUID16 字段。
func FGuid(hex string) Field { return Field{Op: "GUID16", Hex: normalizeHex(hex)} }

// FVal VAL/DICT/ARGLIST/ARRAY/U32ARR/STR3/KV 字段。
func FVal(op string, v *Value) Field { return Field{Op: op, Val: v} }

// FBytes BYTES 字段。
func FBytes(raw []byte) Field { return Field{Op: "BYTES", Hex: hexEncode(raw)} }

// FFloat / FDouble 浮点字段。
func FFloat(v float32) Field  { return Field{Op: "FLOAT", Float: float64(v)} }
func FDouble(v float64) Field { return Field{Op: "DOUBLE", Float: v} }

// Obj 构造对象树（字段数必须与类 ops 数一致，编码期强校验）。
func Obj(class string, typeID uint32, fields ...Field) *Value {
	return &Value{Kind: KindObj, Class: class, TypeID: typeID, Fields: fields}
}

func S(v string) *string { return &v }

func normalizeHex(h string) string {
	out := make([]byte, 0, len(h))
	for i := 0; i < len(h); i++ {
		c := h[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
			out = append(out, c)
		case c >= 'A' && c <= 'F':
			out = append(out, c+('a'-'A'))
		case c == '-':
			// 跳过 GUID 分隔符
		}
	}
	return string(out)
}
