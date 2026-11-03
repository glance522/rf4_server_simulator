package gameserver

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"

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

// 有效会员的分解、鱼条目和实际累计经验必须一致，过期后不再加成。
func TestSettleMembershipAddsMatchingXP(t *testing.T) {
	for _, test := range []struct {
		name         string
		kind, expiry int64
		active       bool
	}{
		{"none", 0, 0, false}, {"permanent", 2, 0, true},
		{"temporary", 1, time.Now().Unix() + 86400, true},
		{"expired", 1, time.Now().Unix() - 1, false}, {"legacy_missing_expiry", 1, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv, db := newProgressSrv(t)
			ctx := context.Background()
			const uid = int64(1000001)
			if _, err := db.EnsureProfile(ctx, uid); err != nil {
				t.Fatal(err)
			}
			if _, err := db.SQL.Exec(`UPDATE profile SET membership=?,membership_exp=? WHERE uid=?`, test.kind, test.expiry, uid); err != nil {
				t.Fatal(err)
			}
			f := &RolledFish{FishGUID: newFishGUID(), Species: "perch", Grade: 2, WeightG: 642, LengthM: .28, LocLevel: "level_001_lake"}
			result := srv.settleCatchXP(ctx, uid, f, 1)
			total, bonus := int64(229), int64(0)
			if test.active {
				total, bonus = 458, 229
			}
			if result == nil || result.baseXP != 229 || result.totalXP != total || result.premiumXP != bonus || f.XP != total {
				t.Fatalf("会员奖励计算不符: %+v fishXP=%d", result, f.XP)
			}
			p, err := db.GetProfile(ctx, uid)
			if err != nil || p.XP != total {
				t.Fatalf("实际入账不符: %+v %v", p, err)
			}
			tree := srv.buildSettle813(ctx, uid, f, result)
			raw, err := proto.EncodeValue(tree, srv.Table())
			if err != nil {
				t.Fatal(err)
			}
			decoded, _, err := proto.DecodeValue(raw, 0, srv.Table())
			if err != nil {
				t.Fatal(err)
			}
			exp := decoded.Field(8).Val
			if got := u64Of(exp.Field(0).Num); got != uint64(total) {
				t.Fatalf("显示总经验=%d，实际%d", got, total)
			}
			bonuses := exp.Field(6).Val
			if test.active {
				if bonuses == nil || bonuses.ArgCodeStr() != "31" || len(bonuses.Items) != 1 {
					t.Fatalf("缺少Premium分解: %+v", bonuses)
				}
				item := bonuses.Items[0]
				if item.TypeID != 111 || u64Of(item.Field(2).Num) != 229 || u32Of(item.Field(3).Num) != 9 || u32Of(item.Field(4).Num) != 4 {
					t.Fatalf("Premium分解类型/数值错误: %+v", item)
				}
			} else if bonuses == nil || len(bonuses.Items) != 0 {
				t.Fatal("无有效会员仍发加成")
			}
			profile, err := srv.BuildProfile307(ctx, uid)
			if err != nil {
				t.Fatal(err)
			}
			kind := uint32(0)
			if test.active {
				kind = uint32(test.kind)
			}
			if got := u32Of(profile.Field(10).Num); got != kind {
				t.Fatalf("档案会员状态=%d want%d", got, kind)
			}
			expiry := uint64(0)
			if test.active && test.kind == 1 {
				expiry = uint64(proto.UnixToFiletime(test.expiry))
			}
			if got := u64Of(profile.Field(11).Num); got != expiry {
				t.Fatalf("到期时间被伪造: %d want%d", got, expiry)
			}
			if _, err := db.SQL.Exec(`UPDATE profile SET membership=0,membership_exp=0 WHERE uid=?`, uid); err != nil {
				t.Fatal(err)
			}
			replay := srv.settleCatchXP(ctx, uid, f, 1)
			if replay == nil || !replay.replayed || replay.totalXP != total || replay.premiumXP != bonus {
				t.Fatalf("重放改变原奖励: %+v", replay)
			}
			p, err = db.GetProfile(ctx, uid)
			if err != nil || p.XP != total {
				t.Fatalf("重复入账: %+v %v", p, err)
			}
		})
	}
}

// TestGradeLinesFromSpeciesWeight 验证档位由 species_weight 的三条重量线决定：
// trophy_g 是达标线，trophy_line_g 是奖杯线，super_trophy_line_g 是蓝冠线。
func TestGradeLinesFromSpeciesWeight(t *testing.T) {
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
		{"perch_test", 339, 1, "w < 达标线(trophy_g)"},
		{"perch_test", 340, 2, "w >= 达标线"},
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

	// 内置池回退路径同样接表：表有行按三线，无行回退池内 valuable 单线
	srv.stateFor(1000001).GearGuid = ""
	e := fishPoolEntry{species: "perch_test", minG: 50, maxG: 9000, valuableLineG: 340}
	if got := srv.poolGrade(e, 2000); got != 3 {
		t.Fatalf("poolGrade(表行)=%d, want 3", got)
	}
	e2 := fishPoolEntry{species: "not_in_table", valuableLineG: 400}
	if got := srv.poolGrade(e2, 400); got != 2 {
		t.Fatalf("poolGrade(无表行,=valuable)=%d, want 2", got)
	}
	if got := srv.poolGrade(e2, 399); got != 1 {
		t.Fatalf("poolGrade(无表行,<valuable)=%d, want 1", got)
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

func TestPersonalRecordFlagFollowsKeptWeight(t *testing.T) {
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
	if got := u32Of(srv.buildSettle813(ctx, uid, lighter, res).Field(8).Val.Field(1).Num); got != 0 {
		t.Fatalf("310.f1=%d, want 0", got)
	}
	heavier := &RolledFish{FishGUID: newFishGUID(), Species: "perch", Grade: 4, WeightG: 900, LengthM: 0.35, LocLevel: "level_001_lake"}
	res2 := srv.settleCatchXP(ctx, uid, heavier, 1)
	if !res2.isRecord {
		t.Fatal("900g 超过已入护的 800g，应记为个人纪录")
	}
	if got := u32Of(srv.buildSettle813(ctx, uid, heavier, res2).Field(8).Val.Field(1).Num); got != 1 {
		t.Fatalf("310.f1=%d, want 1", got)
	}
	if got := u32Of(srv.build813(ctx, uid, heavier, 900, 0).Field(8).Val.Field(1).Num); got != 0 {
		t.Fatalf("同重 310.f1=%d, want 0", got)
	}
}

func ptrI64(v int64) *int64 { return &v }

func TestLegacyCatchReceiptRemainsUnchangedAfterMembershipActivation(t *testing.T) {
	srv, db := newProgressSrv(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET membership=2,xp=229 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	const guid = "11111111111111111111111111111111"
	if err := db.KVSet(ctx, "catch-settlement:1000001:"+guid, `{"BaseXP":229,"TotalXP":229,"Level":1,"AccumulatedXP":229,"SkillID":1}`); err != nil {
		t.Fatal(err)
	}
	result := srv.settleCatchXP(ctx, uid, &RolledFish{FishGUID: guid, Species: "perch", WeightG: 642, Grade: 2}, 1)
	if result == nil || !result.replayed || result.totalXP != 229 || result.premiumXP != 0 {
		t.Fatalf("旧回执被追补会员奖励: %+v", result)
	}
	profile, err := db.GetProfile(ctx, uid)
	if err != nil || profile.XP != 229 {
		t.Fatalf("旧鱼重复入账: %+v %v", profile, err)
	}
}

// 来源：4.0.25034 客户端序列化器；经验类别 9 才参与分项和基础经验扣减。
func TestPremiumXPClientFixture(t *testing.T) {
	srv, _ := newProgressSrv(t)
	result := &settleXpResult{baseXP: 758, totalXP: 1516, premiumXP: 758, skillID: 1, skillGain: 0.2}
	fish := &RolledFish{FishGUID: newFishGUID(), Species: "perch", Grade: 2, WeightG: 642}
	tree := srv.buildSettle813(context.Background(), 1000001, fish, result)
	raw, err := proto.EncodeValue(tree.Field(8).Val, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	const expected = "0136010000ec050000000000000000000001cdcc4c3e0000000000030233310100016f0000000000803f00000000000000000000000000000000f602000000000000090400"
	if hex.EncodeToString(raw) != expected {
		t.Fatalf("客户端经验分解格式不符: %x", raw)
	}
}
