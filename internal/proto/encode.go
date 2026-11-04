package proto

import "fmt"

// 401.f4 官方 KV 参数数组的线上常量（客户端 mdbadlkabhk/dejdolfnooh）。
const (
	kvListElementType = "dejdolfnooh"
	kvEntryTypeID     = 400 // wiregen 注册表：400 = plbmkhnmeim（dejdolfnooh codec）
)

// EncodeValue 将树节点编码为 wire 字节。
// ops 表用于 obj：按类名取 op 序列，并要求字段数与 ops 数一致。
func EncodeValue(v *Value, table *OpsTable) ([]byte, error) {
	if v == nil {
		return []byte{0x00}, nil
	}
	switch v.Kind {
	case KindNull:
		return []byte{0x00}, nil

	case KindObj:
		// 未知类按空 ops 处理，只做 ops/字段数一致性校验。
		ops := table.OpsFor(v.Class)
		if len(ops) != len(v.Fields) {
			return nil, errAt("encode", "obj",
				fmt.Sprintf("ops/fields length mismatch %d vs %d (class %s)",
					len(ops), len(v.Fields), v.Class), 0)
		}
		out := []byte{0x01}
		out = append(out, PackU32(v.TypeID)...)
		for i, op := range ops {
			b, err := EncodeField(op, &v.Fields[i], table)
			if err != nil {
				return nil, err
			}
			out = append(out, b...)
		}
		return out, nil

	case KindValList:
		out := []byte{0x01}
		out = append(out, PackU32(TypeSentinelValList)...)
		for _, it := range v.Items {
			b, err := EncodeValue(it, table)
			if err != nil {
				return nil, err
			}
			out = append(out, b...)
		}
		return out, nil

	case KindDict:
		out := []byte{0x02}
		tn := v.TypeName
		sb, err := PackShortStr(&tn)
		if err != nil {
			return nil, err
		}
		out = append(out, sb...)
		out = append(out, PackU8(uint32(len(v.Pairs)))...)
		for _, p := range v.Pairs {
			kb, err := PackShortStr(p.K)
			if err != nil {
				return nil, err
			}
			out = append(out, kb...)
			vb, err := EncodeValue(p.V, table)
			if err != nil {
				return nil, err
			}
			out = append(out, vb...)
		}
		return out, nil

	case KindArgs:
		out := []byte{0x03}
		cb, err := PackShortStr(v.Code)
		if err != nil {
			return nil, err
		}
		out = append(out, cb...)
		out = append(out, PackU16(uint32(len(v.Items)))...)
		for _, it := range v.Items {
			b, err := EncodeValue(it, table)
			if err != nil {
				return nil, err
			}
			out = append(out, b...)
		}
		return out, nil

	case KindArray:
		out := []byte{0x04}
		out = append(out, PackU16(uint32(len(v.Items)))...)
		for _, it := range v.Items {
			if it == nil || it.IsNull {
				out = append(out, PackU32(BytesNull)...)
				continue
			}
			raw, err := hexDecode(it.Hex)
			if err != nil {
				return nil, err
			}
			out = append(out, PackU32(uint32(len(raw)))...)
			out = append(out, raw...)
		}
		return out, nil

	case KindU32Arr:
		out := []byte{0x05}
		out = append(out, PackU16(uint32(len(v.Items)))...)
		for _, it := range v.Items {
			out = append(out, PackU32(uint32(it.Num))...)
		}
		return out, nil

	case KindGuidArr:
		out := []byte{0x06}
		out = append(out, PackU16(uint32(len(v.Items)))...)
		for _, it := range v.Items {
			raw, err := hexDecode(it.Hex)
			if err != nil {
				return nil, err
			}
			out = append(out, raw...)
		}
		return out, nil

	case KindTypedU32Arr:
		out := []byte{0x07}
		tn := v.TypeName
		tb, err := PackShortStr(&tn)
		if err != nil {
			return nil, err
		}
		out = append(out, tb...)
		out = append(out, PackU16(uint32(len(v.Items)))...)
		for _, it := range v.Items {
			out = append(out, PackU32(uint32(it.Num))...)
		}
		return out, nil

	case KindBytes:
		raw, err := hexDecode(v.Hex)
		if err != nil {
			return nil, err
		}
		out := []byte{0x08}
		out = append(out, PackU32(uint32(len(raw)))...)
		return append(out, raw...), nil

	case KindStr3:
		out := []byte{0x09}
		for _, s := range v.S3 {
			sb, err := PackShortStr(s)
			if err != nil {
				return nil, err
			}
			out = append(out, sb...)
		}
		return out, nil

	case KindKV:
		out := []byte{0x0A}
		kb, err := PackShortStr(v.S3[0])
		if err != nil {
			return nil, err
		}
		out = append(out, kb...)
		vb, err := PackShortStr(v.S3[1])
		if err != nil {
			return nil, err
		}
		out = append(out, vb...)
		return out, nil

	case KindKVList:
		// 客户端 mdbadlkabhk（对象数组，marker 0x03）：
		// [0x03][元素类型名][u16 count] + 元素（各自带 marker）。
		// 读取端 Array.CreateInstance(元素类型, n) 产出真数组——f4 字段
		// 类型是 dejdolfnooh[]，用 List 容器(0x0A)会转型失败致客户端断连。
		out := []byte{0x03}
		et := kvListElementType
		etb, err := PackShortStr(&et)
		if err != nil {
			return nil, err
		}
		out = append(out, etb...)
		if len(v.Items) > 0xFFFF {
			return nil, errAt("encode", "kvlist", "too many entries", len(v.Items))
		}
		out = append(out, PackU16(uint32(len(v.Items)))...)
		for _, e := range v.Items {
			eb, err := EncodeValue(e, table)
			if err != nil {
				return nil, err
			}
			out = append(out, eb...)
		}
		return out, nil

	case KindKVArr:
		// 裸类型化数组（lpdfanjcalm<T> 静态类型直读的推测形状）：
		// [u16 n] + n×([键字节][值 marker+载荷])，无容器与元素头。
		if len(v.Items) > 0xFFFF {
			return nil, errAt("encode", "kvarr", "too many entries", len(v.Items))
		}
		out := PackU16(uint32(len(v.Items)))
		for _, e := range v.Items {
			if e == nil || e.Kind != KindKVEntry || len(e.Items) == 0 {
				return nil, errAt("encode", "kvarr", "entry must be kventry", 0)
			}
			vb, err := EncodeValue(e.Items[0], table)
			if err != nil {
				return nil, err
			}
			out = append(out, e.Marker)
			out = append(out, vb...)
		}
		return out, nil

	case KindTypedArr:
		// 自定义类型名数组（探测用）：[0x03][类型名][u16 n] + 元素（各带 marker）。
		out := []byte{0x03}
		tn := v.TypeName
		tb, err := PackShortStr(&tn)
		if err != nil {
			return nil, err
		}
		out = append(out, tb...)
		out = append(out, PackU16(uint32(len(v.Items)))...)
		for _, e := range v.Items {
			eb, err := EncodeValue(e, table)
			if err != nil {
				return nil, err
			}
			out = append(out, eb...)
		}
		return out, nil

	case KindKVEntry:
		// typed object（marker 0x01）+ typeid 400（plbmkhnmeim/dejdolfnooh）
		// + [1 字节键] + 通用值。读取端 ghgfmpajoej 按 marker 分发，自洽。
		// typed object（marker 0x01）+ typeid 400（plbmkhnmeim/dejdolfnooh）
		// + [1 字节键] + 通用值。读取端 ghgfmpajoej 按 marker 分发，自洽。
		var val []byte
		var err error
		if len(v.Items) > 0 {
			val, err = EncodeValue(v.Items[0], table)
		} else {
			val = []byte{0x00}
		}
		if err != nil {
			return nil, err
		}
		out := make([]byte, 0, 7+len(val))
		out = append(out, 0x01)
		out = append(out, PackU32(kvEntryTypeID)...)
		out = append(out, v.Marker)
		return append(out, val...), nil

	case KindDateTime:
		out := []byte{0x0B}
		return append(out, PackU64(v.Num)...), nil

	case KindGuid:
		// 0x17 = null guid 短格式（0 字节载荷，保往返一致）。
		if v.Marker == 0x17 {
			return []byte{0x17}, nil
		}
		raw, err := hexDecode(v.Hex)
		if err != nil {
			return nil, err
		}
		if len(raw) != 16 {
			return nil, errAt("encode", "guid", "guid must be 16 bytes", len(raw))
		}
		// GUID 常规编码一律以 0x0c（0x17 解码照常支持；客户端两种 marker 均可读）。
		return append([]byte{0x0C}, raw...), nil

	case KindU32:
		m := v.Marker
		if m != 0x0D && m != 0x13 {
			m = 0x10
		}
		return append([]byte{m}, PackU32(uint32(v.Num))...), nil

	case KindBool:
		if v.Bool {
			return []byte{0x0E}, nil
		}
		return []byte{0x0F}, nil

	case KindU64:
		m := v.Marker
		if m != 0x12 {
			m = 0x11
		}
		return append([]byte{m}, PackU64(v.Num)...), nil

	case KindStr:
		return encodeLongStr(v)

	case KindElemNull:
		return []byte{0xFF}, nil
	}
	return nil, errAt("encode", "value", "cannot encode kind "+string(v.Kind), 0)
}

// encodeLongStr 编码长字符串：nil 按 marker（0x15/0x16）编码为空值；
// marker=0 时按长度自动选择 0x14/0x16。
func encodeLongStr(v *Value) ([]byte, error) {
	if v.Str == nil {
		switch v.Marker {
		case 0x15:
			return append([]byte{0x15}, PackU16(StrNullU16)...), nil
		case 0x16:
			return append([]byte{0x16}, PackU32(StrNullU32)...), nil
		default:
			return []byte{0x14, byte(StrNullShort)}, nil
		}
	}
	b := []byte(*v.Str)
	m := v.Marker
	if m == 0 {
		if len(b) <= 254 {
			m = 0x14
		} else {
			m = 0x16
		}
	}
	switch m {
	case 0x14:
		s, err := PackShortStr(v.Str)
		if err != nil {
			return nil, err
		}
		return append([]byte{0x14}, s...), nil
	case 0x15:
		return append([]byte{0x15}, PackU16Str(v.Str)...), nil
	case 0x16:
		return append([]byte{0x16}, PackU32Str(v.Str)...), nil
	}
	return nil, errAt("encode", "str", fmt.Sprintf("bad str marker 0x%02x", m), 0)
}

// EncodeField 按 op 编码一个字段（op 来自类 ops 表，与 encode_field 一致）。
func EncodeField(op string, f *Field, table *OpsTable) ([]byte, error) {
	switch op {
	case "U32":
		return PackU32(u32Of(f.Num)), nil
	case "U64":
		return PackU64(u64Of(f.Num)), nil
	case "BYTE":
		return PackU8(u32Of(f.Num)), nil
	case "STR":
		return PackShortStr(f.Str)
	case "U16":
		if f.Num == nil {
			return PackU16(StrNullU16), nil
		}
		return PackU16(uint32(*f.Num)), nil
	case "U16STR":
		return PackU16Str(f.Str), nil
	case "U32N":
		return PackU32Str(f.Str), nil
	case "VAL":
		return EncodeValue(f.Val, table)
	case "BYTES":
		raw, err := hexDecode(f.Hex)
		if err != nil {
			return nil, err
		}
		out := PackU32(uint32(len(raw)))
		return append(out, raw...), nil
	case "GUID16":
		raw, err := hexDecode(f.Hex)
		if err != nil {
			return nil, err
		}
		if len(raw) != 16 {
			raw = make([]byte, 16) // 兜底缺省 00*16
		}
		return raw, nil
	case "OPTGUID":
		if f.Hex == "" {
			return []byte{0x00}, nil
		}
		raw, err := hexDecode(f.Hex)
		if err != nil {
			return nil, err
		}
		if len(raw) != 16 {
			return nil, errAt("encode", "optguid", "guid must be 16 bytes", len(raw))
		}
		return append([]byte{0x01}, raw...), nil
	case "OPT":
		return PackU8(u32Of(f.Num)), nil
	case "FLOAT":
		return PackU32(F32Bits(float32(f.Float))), nil
	case "DOUBLE":
		return PackU64(F64Bits(f.Float)), nil
	case "DICT", "ARGLIST", "ARRAY", "U32ARR", "STR3", "KV":
		return EncodeValue(f.Val, table)
	}
	return nil, errAt("encode", "op", "cannot encode field op "+op, 0)
}

func u32Of(p *uint64) uint32 {
	if p == nil {
		return 0
	}
	return uint32(*p)
}

func u64Of(p *uint64) uint64 {
	if p == nil {
		return 0
	}
	return *p
}

// ZeroField 按 op 生成合法零值字段，用于必须填满对象字段的场景。
func ZeroField(op string) (Field, error) {
	f := Field{Op: op}
	switch op {
	case "U32", "FLOAT", "DOUBLE":
		f.Num = ptrU64(0)
		f.Float = 0
	case "U64":
		f.Num = ptrU64(0)
	case "BYTE":
		f.Num = ptrU64(0)
	case "OPT":
		f.Num = ptrU64(0)
	case "OPTGUID":
		// Hex 留空 = null
	case "U16":
		f.Num = ptrU64(0)
	case "STR", "U16STR", "U32N":
		// Str nil = null
	case "GUID16":
		f.Hex = "00000000000000000000000000000000"
	case "BYTES":
		f.Hex = ""
	case "VAL", "DICT", "ARGLIST", "ARRAY", "U32ARR", "STR3", "KV":
		f.Val = &Value{Kind: KindNull}
	default:
		return f, errAt("encode", "op", "cannot zero field op "+op, 0)
	}
	return f, nil
}
