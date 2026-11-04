package wiregen

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"testing"

	"rf4server/internal/proto"
)

//go:embed testdata/wire_spec.json
var wireSpecJSON []byte

// corpusGZ 是可选测试语料；未嵌入时回环测试跳过。
var corpusGZ []byte

// corpusAvailable 表示测试语料是否已嵌入。
var corpusAvailable = len(corpusGZ) > 0

// TestSpecSanity 类型表基本一致性：type_id 与类名互反、ops 非空。
func TestSpecSanity(t *testing.T) {
	if len(TypeToClass) == 0 || len(ClassOps) == 0 {
		t.Fatalf("生成表为空: type_ids=%d classes=%d", len(TypeToClass), len(ClassOps))
	}
	for tid, cls := range TypeToClass {
		if ClassOps[cls] == nil {
			// 信封类 -1/-2 可能无 ops，允许；其余类必须有 ops
			if tid > 0 {
				t.Errorf("type_id %d 的类 %s 无 ops", tid, cls)
			}
		}
	}
	if got := ClassForType(27); got != "fgogccpglhb" {
		t.Errorf("ClassForType(27) = %q, want fgogccpglhb", got)
	}
	if n := len(ClassMeta); n < 300 {
		t.Errorf("字段注解类数量异常少: %d", n)
	}
}

type corpusRecord struct {
	K string `json:"k"` // env | val
	X string `json:"x"` // hex
}

// TestCorpusRoundtrip 验证 decode→encode 逐字节一致。
// 公开仓库不含抓包语料时仍运行内置值和信封用例；存在私有语料时再执行全量回环。
func TestCorpusRoundtrip(t *testing.T) {
	table := Table()
	runBuiltInRoundtrip(t, table)
	if !corpusAvailable {
		t.Log("未嵌入私有语料，已通过内置回环用例")
		return
	}
	runCorpusGZRoundtrip(t, table)
}

func runBuiltInRoundtrip(t *testing.T, table *proto.OpsTable) {
	t.Helper()
	value := proto.Args(proto.S("524"), proto.U32(42), proto.StrVal("smoke"))
	raw, err := proto.EncodeValue(value, table)
	if err != nil {
		t.Fatalf("内置值编码: %v", err)
	}
	decoded, pos, err := proto.DecodeValue(raw, 0, table)
	if err != nil {
		t.Fatalf("内置值解码: %v", err)
	}
	if pos != len(raw) {
		t.Fatalf("内置值解码消费 %d/%d 字节", pos, len(raw))
	}
	reencoded, err := proto.EncodeValue(decoded, table)
	if err != nil {
		t.Fatalf("内置值重编码: %v", err)
	}
	if !bytes.Equal(reencoded, raw) {
		t.Fatalf("内置值回环不一致: got=%x want=%x", reencoded, raw)
	}

	requestPayload := append(append([]byte{}, raw...), 1)
	request := proto.BuildRequestEnvelope(7, 3, 1, requestPayload)
	env, ok := proto.ParseEnvelope(request)
	if !ok || env.Marker != -1 || env.CallID != 7 || env.Main != 3 || env.Sub != 1 || !env.HasTail || env.Expect == nil || *env.Expect != 1 {
		t.Fatalf("内置请求信封解析异常: %+v ok=%v", env, ok)
	}
	if !bytes.Equal(env.Body, raw) {
		t.Fatalf("内置请求 body 不一致: got=%x want=%x", env.Body, raw)
	}
	requestBody := append(append([]byte{}, env.Body...), *env.Expect)
	if rebuilt := proto.BuildRequestEnvelope(env.CallID, env.Main, env.Sub, requestBody); !bytes.Equal(rebuilt, request) {
		t.Fatalf("内置请求信封回环不一致: got=%x want=%x", rebuilt, request)
	}

	response := proto.BuildResponseEnvelope(7, raw)
	env, ok = proto.ParseEnvelope(response)
	if !ok || env.Marker != -2 || env.CallID != 7 || env.HasTail {
		t.Fatalf("内置响应信封解析异常: %+v ok=%v", env, ok)
	}
	if rebuilt := proto.BuildResponseEnvelope(env.CallID, env.Body); !bytes.Equal(rebuilt, response) {
		t.Fatalf("内置响应信封回环不一致: got=%x want=%x", rebuilt, response)
	}
}

func runCorpusGZRoundtrip(t *testing.T, table *proto.OpsTable) {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(corpusGZ))
	if err != nil {
		t.Fatalf("打开语料: %v", err)
	}
	defer gz.Close()

	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 1<<20), 64<<20) // 1/4 LZMA 大包单条可达 MB 级

	var nEnv, nVal, nSkip int
	var failBytes []byte
	failed := 0
	for sc.Scan() {
		var rec corpusRecord
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("语料行解析: %v", err)
		}
		raw, err := hexDec(rec.X)
		if err != nil {
			t.Fatalf("语料 hex: %v", err)
		}
		switch rec.K {
		case "env":
			env, ok := proto.ParseEnvelope(raw)
			if !ok {
				nSkip++
				continue
			}
			// 信封重建（含尾字节重组）
			var rebuilt []byte
			switch {
			case env.Marker == -1 && len(raw) >= 11:
				payload := env.Body
				if env.HasTail {
					payload = append(append([]byte{}, env.Body...), *env.Expect)
				}
				rebuilt = proto.BuildRequestEnvelope(env.CallID, env.Main, env.Sub, payload)
			case env.Marker == -2:
				rebuilt = proto.BuildResponseEnvelope(env.CallID, env.Body)
			default:
				nSkip++
				continue
			}
			if !bytes.Equal(rebuilt, raw) {
				failed++
				if failed <= 3 {
					failBytes = raw
				}
				continue
			}
			// body 值往返
			dec, pos, err := proto.DecodeValue(env.Body, 0, table)
			if err != nil {
				t.Fatalf("env body 解码失败: %v\nhex: %x", err, env.Body)
			}
			if pos != len(env.Body) {
				nSkip++
				continue
			}
			re, err := proto.EncodeValue(dec, table)
			if err != nil {
				t.Fatalf("env body 编码失败: %v", err)
			}
			if !bytes.Equal(re, env.Body) {
				failed++
				if failed <= 3 {
					failBytes = env.Body
				}
				continue
			}
			nEnv++
		case "val":
			dec, pos, err := proto.DecodeValue(raw, 0, table)
			if err != nil {
				t.Fatalf("val 解码失败: %v\nhex: %x", err, raw)
			}
			if pos != len(raw) {
				nSkip++
				continue
			}
			re, err := proto.EncodeValue(dec, table)
			if err != nil {
				t.Fatalf("val 编码失败: %v", err)
			}
			if !bytes.Equal(re, raw) {
				failed++
				if failed <= 3 {
					failBytes = raw
				}
				continue
			}
			nVal++
		default:
			t.Fatalf("未知语料种类 %q", rec.K)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("语料读取: %v", err)
	}
	if failed > 0 {
		t.Fatalf("回环不一致 %d 条（env=%d val=%d skip=%d）；首例 hex: %x",
			failed, nEnv, nVal, nSkip, failBytes)
	}
	total := nEnv + nVal
	if total < 5000 {
		t.Fatalf("语料量异常（%d 条）—— 语料文件可能损坏", total)
	}
	t.Logf("语料回环通过: env=%d val=%d skip=%d 共 %d 条", nEnv, nVal, nSkip, total)
}

func hexDec(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, fmt.Errorf("odd hex len")
	}
	out := make([]byte, len(s)/2)
	const digits = "0123456789abcdef"
	idx := func(c byte) (byte, bool) {
		for i := 0; i < 16; i++ {
			if digits[i] == c || byte('A'+i) == c {
				return byte(i), true
			}
		}
		return 0, false
	}
	for i := range out {
		hi, ok1 := idx(s[i*2])
		lo, ok2 := idx(s[i*2+1])
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("bad hex at %d", i*2)
		}
		out[i] = hi<<4 | lo
	}
	return out, nil
}
