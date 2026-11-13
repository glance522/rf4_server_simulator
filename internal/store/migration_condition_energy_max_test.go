package store

// 00056 迁移：历史遗留的 condition_energy_max=0.85 要升到 1.0，
// 维护者显式标定的其它值原样保留。期望值来自迁移意图，不用被测代码回算。

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigrationConditionEnergyMax(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name string
		seed string // 迁移前写进 tunables 的值；空表示没有这一行
		want string
		ok   bool // 迁移后是否应存在该行
	}{
		{name: "历史遗留 0.85 升到 1.0", seed: "0.85", want: "1.0", ok: true},
		{name: "维护者标定值原样保留", seed: "0.9", want: "0.9", ok: true},
		{name: "已改好的 1.0 保持不变", seed: "1.0", want: "1.0", ok: true},
		{name: "没有该行时不凭空创建", seed: "", want: "", ok: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := Open(filepath.Join(t.TempDir(), "m.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()

			// 先建全库，再把状态回退到"00056 尚未应用"，模拟旧库升级。
			if err := db.Migrate(); err != nil {
				t.Fatal(err)
			}
			if _, err := db.SQL.ExecContext(ctx,
				`DELETE FROM schema_migrations WHERE name='migrations/00059_condition_energy_max.sql'`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.SQL.ExecContext(ctx, `DELETE FROM tunables WHERE key='condition_energy_max'`); err != nil {
				t.Fatal(err)
			}
			if tc.seed != "" {
				if _, err := db.SQL.ExecContext(ctx,
					`INSERT INTO tunables(key,value,updated_by,updated_ts) VALUES('condition_energy_max',?,'admin',1)`, tc.seed); err != nil {
					t.Fatal(err)
				}
			}

			if err := db.Migrate(); err != nil {
				t.Fatal(err)
			}

			var got string
			err = db.SQL.QueryRowContext(ctx,
				`SELECT value FROM tunables WHERE key='condition_energy_max'`).Scan(&got)
			if tc.ok {
				if err != nil {
					t.Fatalf("迁移后应存在该行: %v", err)
				}
				if got != tc.want {
					t.Fatalf("值应为 %q，实得 %q", tc.want, got)
				}
				return
			}
			if err == nil {
				t.Fatalf("不该凭空创建该行，实得 %q", got)
			}
		})
	}
}
