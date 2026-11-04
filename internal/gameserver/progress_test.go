package gameserver

import (
	"context"
	"path/filepath"
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// 进度与钓获结算测试。

func newProgressSrv(t *testing.T) (*Server, *store.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "progress.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "probe"}}, logging.Discard(), nil)
	srv.SetDB(db)
	return srv, db
}

// TestXpMonotonicLevel 验证等级只升不降，并验证管理端修改等级时 xp 会回填到等级门槛。
func TestXpMonotonicLevel(t *testing.T) {
	srv, db := newProgressSrv(t)
	ctx := context.Background()
	uid := int64(1000001)

	if _, err := srv.DB.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.ExecContext(ctx,
		`UPDATE profile SET level=15, xp=320 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}

	// 曲线语义锚点：744xp 对应 3 级，baseXp(4)=1200。
	if got := levelForXp(744); got != 3 {
		t.Fatalf("levelForXp(744)=%d, want 3", got)
	}
	if got := baseXp(4); got != 1200 {
		t.Fatalf("baseXp(4)=%d, want 1200（300+400+500 累计）", got)
	}
	if got := baseXp(15); got != 180000 {
		t.Fatalf("baseXp(15)=%d, want 180000", got)
	}
	if maxLevel != 58 {
		t.Fatalf("maxLevel=%d, want 58（等级上限）", maxLevel)
	}
	// baseXp(L) ↔ levelForXp 互逆（全级别）
	for lv := int64(1); lv <= maxLevel; lv++ {
		if got := levelForXp(baseXp(lv)); got != lv {
			t.Fatalf("levelForXp(baseXp(%d))=%d, want %d", lv, got, lv)
		}
	}

	// 钓获加经验后等级必须保持 15。
	newLevel, totalXp, gained := srv.grantXpMonotonic(ctx, uid, 229)
	if newLevel != 15 {
		t.Fatalf("钓获后等级=%d, want 15（只升不降）", newLevel)
	}
	if gained != 0 {
		t.Fatalf("levelsGained=%d, want 0", gained)
	}
	if want := baseXp(15); totalXp != want {
		t.Fatalf("累计 xp=%d, want %d（不足门槛时回填到 baseXp(15)）", totalXp, want)
	}

	// 连续钓获：等级单调不降
	prev := newLevel
	for i := 0; i < 5; i++ {
		lv, _, _ := srv.grantXpMonotonic(ctx, uid, 500)
		if lv < prev {
			t.Fatalf("第 %d 杆等级 %d < 上一杆 %d（非单调）", i, lv, prev)
		}
		prev = lv
	}

	// 升级仍然可用：补足 16 级门槛 → 升到 16
	lv16, _, gained16 := srv.grantXpMonotonic(ctx, uid, baseXp(16)-baseXp(15))
	if lv16 != 16 || gained16 != 1 {
		t.Fatalf("补足经验后 level=%d gained=%d, want 16/1", lv16, gained16)
	}

	// (c) 管理后台改等级：xp 回填 baseXp(level)
	l15 := int64(15)
	lvl, xpAfter := AdminResolveLevelXP(&l15, nil, 25, 710)
	if lvl != 15 || xpAfter != baseXp(15) {
		t.Fatalf("AdminResolveLevelXP(15,nil)=%d/%d, want 15/%d", lvl, xpAfter, baseXp(15))
	}
	// 显式给定低于门槛的 xp 时，也必须回填到当前等级门槛。
	bad := int64(320)
	lvl, xpAfter = AdminResolveLevelXP(&l15, &bad, 25, 710)
	if lvl != 15 || xpAfter < baseXp(15) {
		t.Fatalf("AdminResolveLevelXP(15,320)=%d/%d, want 15/>=%d（等级不低于门槛）", lvl, xpAfter, baseXp(15))
	}
	// xp-only 修改：等级由累计经验托底，不低于重算值
	xpOnly := int64(1000000)
	lvl, xpAfter = AdminResolveLevelXP(nil, &xpOnly, 3, 1200)
	if want := levelForXp(xpOnly); lvl < want {
		t.Fatalf("xp-only 修改后 level=%d < levelForXp(%d)=%d", lvl, xpOnly, want)
	}
}

// 没有有效会员或竿体加成时，实际入账和原生经验块保持基础值。
func TestSettleWithoutXPBonusSources(t *testing.T) {
	srv, db := newProgressSrv(t)
	ctx := context.Background()

	mk := func(uid int64, membership int64) {
		t.Helper()
		if _, err := srv.DB.EnsureProfile(ctx, uid); err != nil {
			t.Fatal(err)
		}
		if _, err := db.SQL.ExecContext(ctx,
			`UPDATE profile SET membership=? WHERE uid=?`, membership, uid); err != nil {
			t.Fatal(err)
		}
	}
	fish := func(uid int64) *RolledFish {
		return &RolledFish{
			FishGUID: newFishGUID(), Species: "perch", Grade: 2, WeightG: 642,
			LengthM: 0.28, LocLevel: "level_001_lake",
		}
	}

	// 无有效会员和手持竿时，不产生 Premium 或竿体分解项。
	mk(1000001, 0)
	f := fish(1000001)
	res := srv.settleCatchXP(ctx, 1000001, f, 1)
	if res.baseXP != 229 {
		t.Fatalf("基础经验=%d, want 229（perch 642g grade2）", res.baseXP)
	}
	if res.totalXP != 229 {
		t.Fatalf("无加成总经验=%d, want 229", res.totalXP)
	}
	if f.XP != 229 {
		t.Fatalf("25 条目 XP=%d, want 229", f.XP)
	}
	p, _ := srv.DB.GetProfile(ctx, 1000001)
	if p.XP != 229 {
		t.Fatalf("入账累计 xp=%d, want 229", p.XP)
	}

	// 813 内嵌 310：f0=229、f5=0、f6 必须为空列表。
	obj813 := srv.buildSettle813(ctx, 1000001, f, res)
	obj310 := obj813.Field(8).Val
	if obj310 == nil || obj310.Class != "kegeppimjbm" {
		t.Fatalf("813.f8 不是 310 块: %v", obj813.Field(8).Val)
	}
	if got := u64Of(obj310.Field(0).Num); got != 229 {
		t.Fatalf("310.f0=%d, want 229（本条鱼总经验）", got)
	}
	if got := u32Of(obj310.Field(1).Num); got != 0 {
		t.Fatalf("310.f1=%d, want 0（229 XP 尚未达到升级门槛）", got)
	}
	if got := u32Of(obj310.Field(5).Num); got != 0 {
		t.Fatalf("310.f5=%d, want 0（229 XP 不足以升 2 级）", got)
	}
	bonusList := obj310.Field(6).Val
	if bonusList == nil || len(bonusList.Items) != 0 {
		t.Fatalf("310.f6 应为空列表，got %v", bonusList)
	}

	// 非会员逐值一致。
	mk(1000002, 0)
	f2 := fish(1000002)
	res2 := srv.settleCatchXP(ctx, 1000002, f2, 1)
	if res2.totalXP != 229 {
		t.Fatalf("非会员总经验=%d, want 229", res2.totalXP)
	}
	obj813b := srv.buildSettle813(ctx, 1000002, f2, res2)
	b2 := obj813b.Field(8).Val.Field(6).Val
	if b2 == nil || len(b2.Items) != 0 {
		t.Fatalf("非会员 310.f6 应为空列表，got %v", b2)
	}

	// 编码烟测：813 全树可编码（wire 合同）
	if _, err := proto.EncodeValue(obj813, srv.Table()); err != nil {
		t.Fatalf("813 编码失败: %v", err)
	}
}

// 结算尺优先沿用内置值；其他鱼种使用尺上的补全分界。
func TestGradeLinesFollowSettlementRuler(t *testing.T) {
	srv, db := newProgressSrv(t)
	ctx := srv.dbCtx()

	seed := func(key string, val, tro, sup any) {
		t.Helper()
		_, err := db.SQL.ExecContext(ctx, `INSERT OR REPLACE INTO species_weight
			(fish_key, min_g, max_g, trophy_g, src, trophy_line_g, super_trophy_line_g)
			VALUES(?,?,?,?,?,?,?)`, key, 50, 9000, val, "test", tro, sup)
		if err != nil {
			t.Fatal(err)
		}
	}
	seed("perch_test", 340, 1500, 3000)       // 三线齐全
	seed("degenerate_test", 3000, 3000, 3000) // 三条线重合的退化数据

	cases := []struct {
		key  string
		w    int64
		want int64
		note string
	}{
		{"perch_test", 259, 1, "w < 结算尺达标线"},
		{"perch_test", 260, 2, "w >= 结算尺达标线"},
		{"perch_test", 1499, 2, "达标 ≤ w < 奖杯线(trophy_line_g)"},
		{"perch_test", 1500, 3, "w >= 奖杯线"},
		{"perch_test", 2999, 3, "奖杯 ≤ w < 蓝冠线(super_trophy_line_g)"},
		{"perch_test", 3000, 4, "w >= 蓝冠线"},
		{"degenerate_test", 3000, 3, "tro==sup 退化：判 grade3 不造 grade4"},
	}
	for _, c := range cases {
		if got := srv.rollGradeDB(ctx, c.key, c.w); got != c.want {
			t.Fatalf("rollGradeDB(%s,%d)=%d, want %d（%s）", c.key, c.w, got, c.want, c.note)
		}
	}
	// 纯函数与 DB 路径同判定
	if got := gradeFromLines(ptrI64(340), ptrI64(1500), ptrI64(3000), 1500); got != 3 {
		t.Fatalf("gradeFromLines 边界=%d, want 3", got)
	}

	// 内置池与静态库都按结算尺，缺表时也沿用尺上的补全分界。
	srv.stateFor(1000001).GearGuid = ""
	e := fishPoolEntry{species: "perch_test", minG: 50, maxG: 9000, valuableLineG: 340}
	if got := srv.poolGrade(e, 2000); got != 3 {
		t.Fatalf("poolGrade(表行)=%d, want 3", got)
	}
	e2 := fishPoolEntry{species: "not_in_table", valuableLineG: 400}
	if got := srv.poolGrade(e2, 277); got != 2 {
		t.Fatalf("poolGrade(无表行,=结算尺达标线)=%d, want 2", got)
	}
	if got := srv.poolGrade(e2, 276); got != 1 {
		t.Fatalf("poolGrade(无表行,<结算尺达标线)=%d, want 1", got)
	}

	// 25 条目/14/14 的档位必须与三线判定一致（grade 即档位线落点）
	w := int64(3200)
	g := srv.rollGradeDB(ctx, "perch_test", w)
	if g != 4 {
		t.Fatalf("rollGradeDB(3200)=%d, want 4", g)
	}
	_ = wiregen.MustMakeObj("ecknbkpeeji", map[int]*proto.Value{
		1: proto.U32(uint32(w)),
		3: proto.U32(uint32(g)),
	})
}

func TestPersonalRecordFollowsKeptWeight(t *testing.T) {
	srv, db := newProgressSrv(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if _, err := srv.DB.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.CatchLogInsert(ctx, uid, "loc", 1, "perch", "", 800, 0.3, "keep", "worm", 3, 10, 10, "level_001_lake"); err != nil {
		t.Fatal(err)
	}
	lighter := &RolledFish{FishGUID: newFishGUID(), Species: "perch", Grade: 2, WeightG: 642, LengthM: 0.28, LocLevel: "level_001_lake"}
	res := srv.settleCatchXP(ctx, uid, lighter, 1)
	if res.isRecord {
		t.Fatal("642g 未超过已入护的 800g，不应记为个人纪录")
	}
	if got := u32Of(srv.buildSettle813(ctx, uid, lighter, res).Field(8).Val.Field(1).Num); int64(got) != res.levelsGained {
		t.Fatalf("310.f1=%d, want 升级数%d", got, res.levelsGained)
	}
	heavier := &RolledFish{FishGUID: newFishGUID(), Species: "perch", Grade: 4, WeightG: 900, LengthM: 0.35, LocLevel: "level_001_lake"}
	res2 := srv.settleCatchXP(ctx, uid, heavier, 1)
	if !res2.isRecord {
		t.Fatal("900g 超过已入护的 800g，应记为个人纪录")
	}
	if got := u32Of(srv.buildSettle813(ctx, uid, heavier, res2).Field(8).Val.Field(1).Num); int64(got) != res2.levelsGained {
		t.Fatalf("310.f1=%d, want 升级数%d", got, res2.levelsGained)
	}
	if got := u32Of(srv.build813(ctx, uid, heavier, 900, 0, 0).Field(8).Val.Field(1).Num); got != 0 {
		t.Fatalf("同重 310.f1=%d, want 0", got)
	}
}

func ptrI64(v int64) *int64 { return &v }
