//go:build stress

package gameserver

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

// stressLog 统计压测期间 WARN 日志的失败原因，用于定位过载时的卡点。
type stressLog struct {
	mu      sync.Mutex
	counts  map[string]int
	samples []string
}

func (w *stressLog) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.counts == nil {
		w.counts = map[string]int{}
	}
	line := strings.TrimSpace(string(p))
	switch {
	case strings.Contains(line, "开启事务失败"):
		w.counts["begin_tx"]++
	case strings.Contains(line, "产出物不可构造"):
		w.counts["prepare_item"]++
	case strings.Contains(line, "读取材料失败"):
		w.counts["list_items"]++
	case strings.Contains(line, "材料不足"):
		w.counts["material_short"]++
	case strings.Contains(line, "产出物入库失败"):
		w.counts["grant_failed"]++
	case strings.Contains(line, "事务提交失败"):
		w.counts["commit_failed"]++
	default:
		w.counts["other"]++
	}
	if len(w.samples) < 8 {
		w.samples = append(w.samples, line)
	}
	return len(p), nil
}

func stressEnvInt(t *testing.T, key string, def int) int {
	t.Helper()
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		t.Fatalf("%s 非法: %q", key, raw)
	}
	return n
}

// TestCraftStackExtreme 高并发制作压测：同账号多条 goroutine 同时制作，
// 验证可计件产物在高并发下仍合并为单堆、材料精确扣减、流水与库完整。
// 通过 CRAFT_STRESS_USERS / CRAFT_STRESS_ROUNDS 可放大规模。
func TestCraftStackExtreme(t *testing.T) {
	users := stressEnvInt(t, "CRAFT_STRESS_USERS", 20)
	rounds := stressEnvInt(t, "CRAFT_STRESS_ROUNDS", 50)

	db := openRecipeTestDB(t)
	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)
	srv.randFloat = func() float64 { return 0 }
	logSink := &stressLog{}
	srvLog := slog.New(slog.NewTextHandler(logSink, &slog.HandlerOptions{Level: slog.LevelWarn}))

	type job struct {
		name   string
		tree   *proto.Value
		rounds int
	}
	type account struct {
		uid  int64
		sess *Session
		jobs []job
	}

	accounts := make([]*account, 0, users)
	for i := 0; i < users; i++ {
		uid := int64(3100000 + i)
		if _, err := db.EnsureProfile(ctx, uid); err != nil {
			t.Fatal(err)
		}
		seedStack := func(itemID string, count int64) {
			t.Helper()
			it, err := srv.prepareNewItemWithCount(ctx, uid, itemID, "backpack", count, "")
			if err != nil {
				t.Fatalf("uid %d 构造材料 %s 失败: %v", uid, itemID, err)
			}
			if err := srv.DB.InsertItem(ctx, it); err != nil {
				t.Fatalf("uid %d 写入材料 %s 失败: %v", uid, itemID, err)
			}
		}
		seedStack("food_egg", int64(2*rounds))
		seedStack("mix_combi", int64(rounds))
		seedStack("hvz_pva_string", int64(rounds))

		sess := &Session{ID: uint64(i + 1), Userid: uid, Srv: srv, log: logging.Discard()}
		sess.closed.Store(true)
		feedSlot := []craftSlot{{
			ItemID: "craft_feed_generic_mix_1",
			GUID:   "00112233445566778899aabbccddeeff",
		}}
		accounts = append(accounts, &account{uid: uid, sess: sess, jobs: []job{
			{name: "cook-a", tree: proto.Args524(craftObj8("craft_cook_boiled_egg", nil, 1)), rounds: rounds},
			{name: "cook-b", tree: proto.Args524(craftObj8("craft_cook_boiled_egg", nil, 1)), rounds: rounds},
			{name: "feed", tree: proto.Args524(craftObj8("craft_feed_universal", feedSlot, 1)), rounds: rounds},
			{name: "pva", tree: proto.Args524(craftObj8("craft_pva_stinger_boil_10", nil, 1)), rounds: rounds},
		}})
	}

	type result struct {
		d   time.Duration
		err string
	}
	total := users * 4 * rounds
	results := make(chan result, total)
	var wg sync.WaitGroup
	start := time.Now()
	for _, acc := range accounts {
		for _, jb := range acc.jobs {
			wg.Add(1)
			go func(acc *account, jb job) {
				defer wg.Done()
				for i := 0; i < jb.rounds; i++ {
					cctx := &Ctx{
						Sess: acc.sess, Main: 9, Sub: 1, CallID: uint32(i + 1),
						Tree: jb.tree, DecodeOK: true, Log: srvLog,
					}
					t0 := time.Now()
					body, ok := hCraft(cctx)
					d := time.Since(t0)
					msg := ""
					if !ok || len(body) == 0 {
						msg = jb.name + " 未返回响应"
					} else if v, _, derr := proto.DecodeValue(body, 0, srv.Table()); derr != nil {
						msg = fmt.Sprintf("%s 响应解码失败: %v", jb.name, derr)
					} else if !v.IsKind(proto.KindObj) || v.TypeID != 803 || u64Of(v.Fields[0].Num) != 1 {
						msg = fmt.Sprintf("%s 803 失败: kind=%v type=%d", jb.name, v.Kind, v.TypeID)
					}
					results <- result{d: d, err: msg}
				}
			}(acc, jb)
		}
	}
	wg.Wait()
	wall := time.Since(start)
	close(results)

	lat := make([]time.Duration, 0, total)
	failures := 0
	reported := 0
	for r := range results {
		lat = append(lat, r.d)
		if r.err != "" {
			failures++
			if reported < 10 {
				t.Errorf("%s", r.err)
				reported++
			}
		}
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	pct := func(p float64) time.Duration {
		if len(lat) == 0 {
			return 0
		}
		idx := int(math.Ceil(p*float64(len(lat)))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(lat) {
			idx = len(lat) - 1
		}
		return lat[idx]
	}
	t.Logf("极限压测: 账号=%d 并发/账号=4 总制作=%d wall=%s QPS=%.1f p50=%s p95=%s p99=%s 失败=%d",
		users, total, wall.Round(time.Millisecond), float64(total)/wall.Seconds(),
		pct(0.50).Round(time.Microsecond), pct(0.95).Round(time.Microsecond),
		pct(0.99).Round(time.Microsecond), failures)
	t.Logf("失败日志分类: %v", logSink.counts)
	for _, s := range logSink.samples {
		t.Logf("日志样本: %s", s)
	}

	for _, acc := range accounts {
		rows := map[string][2]int64{}
		q, err := db.SQL.Query(`SELECT item_id, COUNT(*), COALESCE(SUM(qty),0) FROM item
			WHERE uid=? AND container='backpack' GROUP BY item_id`, acc.uid)
		if err != nil {
			t.Fatal(err)
		}
		for q.Next() {
			var itemID string
			var n, qty int64
			if err := q.Scan(&itemID, &n, &qty); err != nil {
				q.Close()
				t.Fatal(err)
			}
			rows[itemID] = [2]int64{n, qty}
		}
		if err := q.Err(); err != nil {
			q.Close()
			t.Fatal(err)
		}
		q.Close()

		expect := []struct {
			item string
			rows int64
			qty  int64
		}{
			{"boiled_egg", 1, int64(2 * rounds)},
			{"feedcompleted", int64(rounds), int64(60 * rounds)},
			{"pvas_b10", int64(rounds), int64(rounds)},
		}
		for _, e := range expect {
			got := rows[e.item]
			if got[0] != e.rows || got[1] != e.qty {
				t.Errorf("uid=%d %s 期望 rows=%d qty=%d，实得 rows=%d qty=%d",
					acc.uid, e.item, e.rows, e.qty, got[0], got[1])
			}
		}
		for _, m := range []string{"food_egg", "mix_combi", "hvz_pva_string"} {
			if got := rows[m]; got[0] != 0 || got[1] != 0 {
				t.Errorf("uid=%d 材料 %s 未耗尽，rows=%d qty=%d", acc.uid, m, got[0], got[1])
			}
		}
	}

	var txns, badQty int64
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM txn WHERE kind='craft'`).Scan(&txns); err != nil {
		t.Fatal(err)
	}
	if txns != int64(total) {
		t.Errorf("craft 流水应为 %d 条，实得 %d", total, txns)
	}
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM item WHERE qty<=0`).Scan(&badQty); err != nil {
		t.Fatal(err)
	}
	if badQty != 0 {
		t.Errorf("存在 %d 条数量非正的物品行", badQty)
	}
	var integrity string
	if err := db.SQL.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		t.Fatal(err)
	}
	if integrity != "ok" {
		t.Errorf("integrity_check=%s", integrity)
	}
}
