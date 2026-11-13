package gameserver

import "testing"

func TestProfileAndSkillTreeAvailablePoints(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		level int64
		bonus int64
		nodes map[int64]int64
		want  uint32
	}{
		{name: "new_player", level: 1, want: 0},
		{name: "unspent", level: 23, want: 26},
		{name: "spent", level: 10, nodes: map[int64]int64{502: 3}, want: 6},
		{name: "bonus", level: 10, bonus: 5, want: 14},
		{name: "bonus_and_spent", level: 10, bonus: 5, nodes: map[int64]int64{502: 3}, want: 11},
		{
			name: "shared_groups", level: 23,
			nodes: map[int64]int64{313: 3, 514: 3, 701: 3, 1002: 3, 501: 2, 503: 2},
			want:  21, // 26 - 3 - 2；每个共享身份只扣一次。
		},
		{name: "exhausted", level: 4, nodes: map[int64]int64{502: 3}, want: 0},
		{name: "overspent", level: 2, nodes: map[int64]int64{502: 3}, want: 0},
		{name: "negative_bonus", level: 2, bonus: -5, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := sharedSkillTestServer(t, tc.level)
			ctx := t.Context()
			if err := srv.DB.SetSkillBonus(ctx, sharedSkillUID, tc.bonus); err != nil {
				t.Fatal(err)
			}
			for node, rank := range tc.nodes {
				if err := srv.DB.SetSkillNode(ctx, sharedSkillUID, node, rank); err != nil {
					t.Fatal(err)
				}
			}
			assertProfileAndSkillTreePoints(t, srv, tc.want)
		})
	}
}

func assertProfileAndSkillTreePoints(t *testing.T, srv *Server, want uint32) {
	t.Helper()
	ctx := t.Context()
	// 登录档案与会话中推送共用计算；时钟参数不影响点数。
	for _, wNow := range []int64{0, 123} {
		profile, err := srv.BuildProfile307W(ctx, sharedSkillUID, wNow)
		if err != nil {
			t.Fatal(err)
		}
		if profile.TypeID != 307 || len(profile.Fields) != 26 {
			t.Fatalf("profile shape: type=%d fields=%d", profile.TypeID, len(profile.Fields))
		}
		field := profile.Field(6)
		if field.Op != "U32" || field.Num == nil {
			t.Fatalf("profile f6 must be U32: %v", field)
		}
		if got := u32Of(field.Num); got != want {
			t.Fatalf("profile f6=%d, want %d (wNow=%d)", got, want, wNow)
		}
	}
	for _, override := range []uint64{^uint64(0), 0, 7} {
		tree, err := srv.BuildSkillTree311(ctx, sharedSkillUID, override)
		if err != nil {
			t.Fatal(err)
		}
		if tree.TypeID != 311 || len(tree.Fields) != 4 {
			t.Fatalf("tree shape: type=%d fields=%d", tree.TypeID, len(tree.Fields))
		}
		field := tree.Field(0)
		if field.Op != "U32" || field.Num == nil {
			t.Fatalf("tree f0 must be U32: %v", field)
		}
		treeWant := want
		if override != ^uint64(0) {
			treeWant = uint32(override)
		}
		if got := u32Of(field.Num); got != treeWant {
			t.Fatalf("tree f0=%d, want %d (override=%d)", got, treeWant, override)
		}
	}
}

func TestProfileAvailablePointsAfterLevelUp(t *testing.T) {
	srv := sharedSkillTestServer(t, 9)
	ctx := t.Context()
	if err := srv.DB.SetSkillNode(ctx, sharedSkillUID, 502, 3); err != nil {
		t.Fatal(err)
	}
	assertProfileAndSkillTreePoints(t, srv, 5)
	if _, err := srv.DB.SQL.ExecContext(ctx, `UPDATE profile SET level=10,xp=? WHERE uid=?`, baseXp(10), sharedSkillUID); err != nil {
		t.Fatal(err)
	}
	assertProfileAndSkillTreePoints(t, srv, 6)
}
