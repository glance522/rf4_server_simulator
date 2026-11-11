package gameserver

import (
	"fmt"
	"testing"
	"time"

	"rf4server/internal/store"
)

// 渔夫稀有鱼面板（3/17）与统计面板（3/2）的纪录卡 320.f13 必须下发模型族名。
//
// 之前 f13 恒为 null，客户端只能按鱼种取默认族——那套默认族是历史混拼资源，
// 也就是玩家说的"淘汰的老模型"；点进详情走的是渔护 25（f11 已带族名），
// 于是同一条鱼在列表和详情里是两个模型。这里锁住两条链路的一致性。
func TestRecordCard320CarriesModelFamily(t *testing.T) {
	db := seedSalmonLines(t)
	srv := newVariantTestServer(t, map[string]float64{})
	srv.SetDB(db)

	const guid = "855865616196983098b115328d6e78f9" // 线上那条 42.5kg 拉多加大西洋鲑
	rec := store.CatchRecord{
		ID: 990, FishKey: "a_salmon", WeightG: 42564, LengthM: 1.68549,
		Grade: 4, GUID: guid, LocLevel: "level_011_ladoga_02",
	}
	want := srv.fishModelVariantForFish(rec.FishKey, rec.WeightG, rec.LengthM, rec.Grade, rec.GUID)
	if want == "" {
		t.Fatal("大西洋鲑应当有模型族")
	}

	card := srv.recordCard320FromCatch(rec)
	if card == nil {
		t.Fatal("纪录卡不应为空")
	}
	got := card.Field(13).Str
	if got == nil || *got != want {
		t.Fatalf("稀有鱼面板 320.f13=%v，期望族名 %q", got, want)
	}

	// 统计面板的鱼列表用同一尾鱼也必须给出同一个族名。
	statCard := srv.recordCard320(catchStatRow{
		FishKey: rec.FishKey, WeightG: rec.WeightG, LengthM: rec.LengthM,
		Grade: rec.Grade, GUID: rec.GUID, LocLevel: rec.LocLevel,
	}, true, func(string) bool { return false })
	if f := statCard.Field(13).Str; f == nil || *f != want {
		t.Fatalf("统计面板 320.f13=%v，期望族名 %q", f, want)
	}

	// 渔护 25.f11 是详情页的模型来源，两者必须逐字相同。
	entry := &store.KeepnetEntry{
		EchoGUID: rec.GUID, Species: rec.FishKey, WeightG: rec.WeightG,
		LengthM: rec.LengthM, Cat: rec.Grade, KeepLevel: rec.LocLevel,
	}
	srv.fillKeepnetVariant(entry)
	keep := buildKeepnet25(entry, rec.LocLevel, 0, entry.ModelVariant)
	if f := keep.Field(11).Str; f == nil || *f != want {
		t.Fatalf("渔护 25.f11=%v，期望族名 %q", f, want)
	}
}

// 没有变体族的鱼种保持原来的 null：不能因为这次改动给所有鱼写空串，
// 空串在客户端是"查不到族"的另一种表现，和 null 的兼容路径不同。
func TestRecordCard320KeepsNullWithoutFamily(t *testing.T) {
	db := seedSalmonLines(t)
	srv := newVariantTestServer(t, map[string]float64{})
	srv.SetDB(db)

	card := srv.recordCard320FromCatch(store.CatchRecord{
		FishKey: "perch", WeightG: 1200, LengthM: 0.32, Grade: 2,
		GUID: "9f2c1d4e6a8b0c1d2e3f405162738495",
	})
	if got := card.Field(13).Str; got != nil {
		t.Fatalf("无变体族的鱼种 f13 应为 null，得到 %q", *got)
	}
}

// 任务/资格鱼表（5005 mhnenmanmfn）的 f5 与渔护 25.f11 是同一个模型字段，
// f6 才是名称文本。之前 f5 塞的是名称，任务面板里的鱼同样只能退默认族（老模型）。
func TestQualificationFishObjectUsesModelFamily(t *testing.T) {
	db := seedSalmonLines(t)
	srv := newVariantTestServer(t, map[string]float64{})
	srv.SetDB(db)

	const guid = "a7d6923818ab946be6d7de12eaa30594" // 拉多加那条 44.258kg 大西洋鲑
	f := store.QualificationFish{
		GUID: "6c1f0b1e8dd6a0e0c2c9a5a0d3f9b111", FishGUID: guid,
		Species: "a_salmon", NameZh: "大西洋鲑", WeightG: 44258,
		LengthM: 1.702, Grade: 4,
	}
	// 雌雄必须按真鱼 guid 推导（GUID 只是去重键的 md5，不能当种子）。
	want := srv.fishModelVariantForFish(f.Species, f.WeightG, f.LengthM, f.Grade, f.FishGUID)
	if want == "" {
		t.Fatal("大西洋鲑应当有模型族")
	}
	obj := srv.qualificationFishObject(f)
	if got := obj.Field(5).Str; got == nil || *got != want {
		t.Fatalf("5005.f5=%v，期望族名 %q", got, want)
	}
	if got := obj.Field(6).Str; got == nil || *got != f.NameZh {
		t.Fatalf("5005.f6=%v，期望名称 %q", got, f.NameZh)
	}

	// 无变体族的鱼种保持原样：f5 = 名称，f6 留空。
	plain := srv.qualificationFishObject(store.QualificationFish{
		GUID: "9f2c1d4e6a8b0c1d2e3f405162738495", Species: "perch",
		NameZh: "河鲈", WeightG: 1200, LengthM: 0.32, Grade: 2,
	})
	if got := plain.Field(5).Str; got == nil || *got != "河鲈" {
		t.Fatalf("无族鱼种 5005.f5=%v，期望名称", got)
	}
	if got := plain.Field(6).Str; got != nil {
		t.Fatalf("无族鱼种 5005.f6 应为 null，得到 %q", *got)
	}
}

// 美拟鲈的资源族叫 a_sleeper，鱼种 key 却是带点的 a.sleeper。
// 映射表原来写成 a_sleeper，等于这一族的变体从来没生效过。
func TestFishModelVariantDottedSleeperKey(t *testing.T) {
	srv := newVariantTestServer(t, map[string]float64{})
	if got := srv.fishModelVariantForFish("a.sleeper", 300, 0.30, 2, "9f2c1d4e6a8b0c1d2e3f405162738495"); got == "" {
		t.Fatal("a.sleeper 应当命中 a_sleeper 族")
	}
	if got := srv.fishModelVariantForFish("a_sleeper", 300, 0.30, 2, "9f2c1d4e6a8b0c1d2e3f405162738495"); got != "" {
		t.Fatalf("旧的错误 key a_sleeper 不应再命中，得到 %q", got)
	}

	// 三套外观随机出（与体重无关）：固定一个体重扫不同身份，三套都要出现。
	seen := map[string]bool{}
	for i := 0; i < 60; i++ {
		guid := fmt.Sprintf("%032x", i*2654435761)
		v := pickFishModelVariant("a.sleeper", 300, fishVariantMale(guid), guid, 0.30,
			defaultFishVariantParrMaxG, defaultFishVariantSmoltMaxG, defaultFishVariantJuvenileMaxM)
		if v == "" {
			t.Fatalf("a.sleeper 应当有变体族（guid=%s）", guid)
		}
		seen[v] = true
	}
	for _, want := range []string{"a_sleeper", "a_sleeper_2", "a_sleeper_3"} {
		if !seen[want] {
			t.Fatalf("族 %s 从未被选中：%v", want, seen)
		}
	}
}

// 历史 catch_log 里 catch_guid 为空的行：同一尾鱼在 3/17 面板、3/2 统计、渔护详情、
// 资格鱼表四处必须给出同一个族名，且反复刷新不变。
//
// 旧实现对这些行退化成 rand.Intn(2)，每刷新一次性别都可能换——正好破坏本改动要保证的
// 「同一尾鱼模型一致」（review P1）。现在改用「鱼种+克重+体长」内容种子兜底。
func TestRecordCard320StableWithoutCatchGUID(t *testing.T) {
	db := seedSalmonLines(t)
	srv := newVariantTestServer(t, map[string]float64{})
	srv.SetDB(db)

	rec := store.CatchRecord{
		ID: 991, FishKey: "a_salmon", WeightG: 42564, LengthM: 1.68549,
		Grade: 4, GUID: "", LocLevel: "level_011_ladoga_02", // catch_guid 缺失
	}
	first := srv.recordCard320FromCatch(rec).Field(13).Str
	if first == nil || *first == "" {
		t.Fatal("没有 catch_guid 的旧记录也必须拿到族名，不能留空走默认族")
	}
	// 反复读取必须一致（旧实现这里每次都可能变）。
	for i := 0; i < 20; i++ {
		again := srv.recordCard320FromCatch(rec).Field(13).Str
		if again == nil || *again != *first {
			t.Fatalf("同一尾旧鱼第 %d 次读到的族名变了：%v -> %v", i+2, first, again)
		}
	}

	// 3/2 统计面板同一尾鱼。
	statCard := srv.recordCard320(catchStatRow{
		FishKey: rec.FishKey, WeightG: rec.WeightG, LengthM: rec.LengthM,
		Grade: rec.Grade, GUID: "", LocLevel: rec.LocLevel,
	}, true, func(string) bool { return false })
	if f := statCard.Field(13).Str; f == nil || *f != *first {
		t.Fatalf("统计面板 320.f13=%v，期望与稀有鱼面板一致的 %q", f, *first)
	}

	// 渔护详情 25.f11 与结算用同一套推导。
	entry := &store.KeepnetEntry{
		EchoGUID: "", Species: rec.FishKey, WeightG: rec.WeightG,
		LengthM: rec.LengthM, Cat: rec.Grade, KeepLevel: rec.LocLevel,
	}
	srv.fillKeepnetVariant(entry)
	if entry.ModelVariant != *first {
		t.Fatalf("渔护 25.f11=%q，期望 %q", entry.ModelVariant, *first)
	}

	// 资格鱼表（5005.f5）在 FishGUID 缺失时同样要稳定。
	qf := store.QualificationFish{
		GUID: "ce1f0b1e8dd6a0e0c2c9a5a0d3f9b222", FishGUID: "",
		Species: rec.FishKey, NameZh: "大西洋鲑", WeightG: rec.WeightG,
		LengthM: rec.LengthM, Grade: rec.Grade,
	}
	q1 := srv.qualificationFishObject(qf).Field(5).Str
	q2 := srv.qualificationFishObject(qf).Field(5).Str
	if q1 == nil || q2 == nil || *q1 != *q2 {
		t.Fatalf("资格鱼表族名不稳定：%v / %v", q1, q2)
	}
	if *q1 != *first {
		t.Fatalf("资格鱼表 5005.f5=%q，期望 %q", *q1, *first)
	}
}

// 身份串全缺（没有 GUID、也没有克重）时固定按雌鱼处理：宁可少一种外观，
// 也不能每次刷新换性别。
func TestFishVariantSeedFallbacks(t *testing.T) {
	if got := fishVariantSeed("a_salmon", 1000, 0.5, ""); got == "" {
		t.Fatal("缺 GUID 时应有内容种子")
	}
	if got := fishVariantSeed("a_salmon", 1000, 0.5, zeroGUID); got == "" {
		t.Fatal("全零 GUID 应视为缺失，退回内容种子")
	}
	if got := fishVariantSeed("a_salmon", 1000, 0.5, "ABCDEF"); got != "abcdef" {
		t.Fatalf("有 GUID 时应归一化为小写原值，得到 %q", got)
	}
	if got := fishVariantSeed("", 0, 0, ""); got != "" {
		t.Fatalf("字段全缺时应返回空种子，得到 %q", got)
	}
	if fishVariantMale("") {
		t.Fatal("空种子必须稳定按雌鱼处理，不能随机")
	}
}

// fish_model_variant 置 0 后，已经缓存过族名的活跃鱼与渔护条目必须立刻回退，
// 不能继续挂着旧族名（review P2）。
func TestFishModelVariantTunableOffClearsCache(t *testing.T) {
	db := seedSalmonLines(t)
	srv := newVariantTestServer(t, map[string]float64{})
	srv.SetDB(db)

	f := &RolledFish{FishGUID: "0e1d2c3b4a5968778695a4b3c2d1e0f1", Species: "a_salmon",
		Grade: 2, WeightG: 20000, LengthM: 1.05}
	if got := srv.fishModelVariantFor(f); got == "" {
		t.Fatal("开关打开时应算出族名")
	}
	entry := &store.KeepnetEntry{EchoGUID: f.FishGUID, Species: f.Species,
		WeightG: f.WeightG, LengthM: f.LengthM, Cat: f.Grade}
	srv.fillKeepnetVariant(entry)
	if entry.ModelVariant == "" {
		t.Fatal("开关打开时渔护条目应带上族名")
	}

	// 运行中把开关置 0（模拟改库 30 秒生效）。
	srv.tunMu.Lock()
	srv.tunCache["fish_model_variant"] = 0
	srv.tunAt = time.Now()
	srv.tunMu.Unlock()

	if got := srv.fishModelVariantFor(f); got != "" {
		t.Fatalf("开关关闭后活跃鱼不应再用缓存的族名，得到 %q", got)
	}
	if f.ModelVariant != "" {
		t.Fatalf("开关关闭后应清掉 RolledFish 缓存，得到 %q", f.ModelVariant)
	}
	srv.fillKeepnetVariant(entry)
	if entry.ModelVariant != "" {
		t.Fatalf("开关关闭后渔护条目不应再用缓存的族名，得到 %q", entry.ModelVariant)
	}
}
