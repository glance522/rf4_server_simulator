package gameserver

// 14/8 负载观测回归：观测器按字段路径累计次数与取值范围，并给整数载荷一份按
// 浮点位型的再读法（线型里浮点没有独立 marker，鱼拉力只会以位型出现）。
// 观测不进结算，只进本竿的渔轮损伤日志。

import (
	"math"
	"testing"

	"rf4server/internal/proto"
)

func probeU64(v uint64) *uint64 { return &v }

func TestLoadProbeTracksRangesAndBitReadings(t *testing.T) {
	var p fightLoadProbe
	p.observe(proto.Args524(proto.U32(1), proto.F32(3.5), proto.U64(7)), true)
	p.observe(proto.Args524(proto.U32(2), proto.F32(9.25), proto.U64(7)), true)

	fields := p.logFields()
	if fields == nil {
		t.Fatal("有上报时必须给出诊断字段")
	}
	if got := fields["load_reports"]; got != 2 {
		t.Fatalf("上报条数应为 2，实得 %v", got)
	}
	rows, ok := fields["load_fields"].([]map[string]any)
	if !ok || len(rows) != 3 {
		t.Fatalf("应跟踪三个字段路径，实得 %#v", fields["load_fields"])
	}
	byPath := map[string]map[string]any{}
	for _, row := range rows {
		byPath[row["path"].(string)] = row
	}
	// 变化的小整数：只给原始值，位型读数会掉进 denormal 噪声，不该污染日志。
	first := byPath[".0"]
	if first["kind"] != loadKindU32 || first["min"] != uint64(1) || first["max"] != uint64(2) || first["last"] != uint64(2) {
		t.Fatalf("u32 路径应记 1..2 且带上末值，实得 %#v", first)
	}
	if _, bad := first["bits_min"]; bad {
		t.Fatalf("不合理位型读数不该出现： %#v", first)
	}
	// F32 载荷：原始位型是大整数，必须同时给出按位型解出来的浮点范围。
	load := byPath[".1"]
	if load["kind"] != loadKindU32 || load["min"].(uint64) < 1<<28 {
		t.Fatalf("F32 载荷原始值应是位型大整数，实得 %#v", load)
	}
	if got := load["bits_min"]; got == nil || math.Abs(got.(float64)-3.5) > 1e-6 {
		t.Fatalf("F32 载荷应解出 3.5 起步，实得 %#v", load)
	}
	if got := load["bits_max"]; got == nil || math.Abs(got.(float64)-9.25) > 1e-6 {
		t.Fatalf("F32 载荷应解到 9.25，实得 %#v", load)
	}
	// 恒定字段：min==max 时不再重复记 last。
	steady := byPath[".2"]
	if steady["min"] != uint64(7) || steady["max"] != uint64(7) {
		t.Fatalf("恒定字段应记 7..7，实得 %#v", steady)
	}
	if _, bad := steady["last"]; bad {
		t.Fatalf("恒定字段不该带 last： %#v", steady)
	}
}

func TestLoadProbeWalksObjectFields(t *testing.T) {
	var p fightLoadProbe
	tree := proto.Args524(&proto.Value{Kind: proto.KindObj, Class: "oiikpeaajln", Fields: []proto.Field{
		{Op: "U32", Num: probeU64(3)},
		{Op: "FLOAT", Float: 12.5},
		{Op: "VAL", Val: proto.Bool(true)},
	}})
	p.observe(tree, true)
	fields := p.logFields()
	rows, ok := fields["load_fields"].([]map[string]any)
	if !ok || len(rows) != 3 {
		t.Fatalf("对象字段与嵌套值都要走访到，实得 %#v", fields["load_fields"])
	}
	byPath := map[string]map[string]any{}
	for _, row := range rows {
		byPath[row["path"].(string)] = row
	}
	if row := byPath[".0.0"]; row == nil || row["min"] != uint64(3) {
		t.Fatalf("对象第 0 个字段应记 3，实得 %#v", byPath[".0.0"])
	}
	if row := byPath[".0.1"]; row == nil || row["kind"] != loadKindF64 || math.Abs(row["min"].(float64)-12.5) > 1e-9 {
		t.Fatalf("FLOAT 字段应直接记浮点 12.5，实得 %#v", byPath[".0.1"])
	}
	if row := byPath[".0.2"]; row == nil || row["kind"] != loadKindBool || row["max"] != uint64(1) {
		t.Fatalf("嵌套 bool 字段应记 0/1，实得 %#v", byPath[".0.2"])
	}
}

func TestLoadProbeCountsUndecodedAndBoundsSlots(t *testing.T) {
	var p fightLoadProbe
	if p.logFields() != nil {
		t.Fatal("一条上报都没收到时不该给出诊断字段")
	}
	p.observe(nil, false)
	p.observe(proto.Args524(proto.Null()), false)
	if fields := p.logFields(); fields["load_reports"] != 2 || fields["load_undecoded"] != 2 {
		t.Fatalf("解不出值树也要计数： %#v", fields)
	}
	if len(p.slots) != 0 {
		t.Fatalf("解不出值树时不该有字段读数： %#v", p.slots)
	}

	// 超过槽位上限：新增的只计 dropped，已有字段的累计不能被打断。
	var p2 fightLoadProbe
	for i := 0; i < 4; i++ {
		items := make([]*proto.Value, 0, loadProbeMaxSlots+4)
		for j := 0; j < loadProbeMaxSlots+4; j++ {
			items = append(items, proto.U32(uint32(i*100+j)))
		}
		p2.observe(proto.Args524(items...), true)
	}
	if len(p2.slots) > loadProbeMaxSlots {
		t.Fatalf("字段槽位必须封顶 %d，实得 %d", loadProbeMaxSlots, len(p2.slots))
	}
	if fields := p2.logFields(); fields["load_dropped"] == nil {
		t.Fatalf("超出槽位的字段必须计入 dropped： %#v", fields)
	}
	if p2.slots[0].count != 4 || p2.slots[0].rawMin > p2.slots[0].rawMax {
		t.Fatalf("封顶后已跟踪字段的累计要完整： %#v", p2.slots[0])
	}
}

// TestFightWearDetailCarriesLoadProbe 断观测结果确实跟着磨损账走到损伤日志里：
// 攒凭据的地方就是这条日志，日志里没有就等于白观测。
func TestFightWearDetailCarriesLoadProbe(t *testing.T) {
	w := newFightWearForTest(7.7, 5, 11.55, 1)
	w.loadProbe.observe(proto.Args524(proto.F32(6.5)), true)
	detail := fightWearDetailFields(w, 0.01, nil)
	if detail["load_reports"] != 1 {
		t.Fatalf("损伤日志应带负载观测条数： %#v", detail)
	}
	rows, ok := detail["load_fields"].([]map[string]any)
	if !ok || len(rows) != 1 || rows[0]["path"] != ".0" {
		t.Fatalf("损伤日志应带负载字段范围： %#v", detail["load_fields"])
	}
}
