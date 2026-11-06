package gameserver

// 制作份数上限与固定产出：一套材料窝料出 60 个、PVA 棒出 60/30/12 个、
// 茶咖啡出 6 份；同时验证材料槽路径按“需求数量 × 制作份数”扣料。

import (
	"context"
	"strings"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func TestCraftBatchSpec(t *testing.T) {
	cases := []struct {
		name  string
		def   store.ItemDef
		limit int64
		yield int64
	}{
		{"窝料60套出60", store.ItemDef{ItemID: "feedcompleted", Category: 46}, 60, 60},
		{"窝料草鱼60套出60", store.ItemDef{ItemID: "feed_amur", Category: 46}, 60, 60},
		{"PVA10g 60套出60", store.ItemDef{ItemID: "pva_stick_s", Category: 30}, 60, 60},
		{"PVA20g 30套出30", store.ItemDef{ItemID: "pva_stick_m", Category: 30}, 30, 30},
		{"PVA50g 12套出12", store.ItemDef{ItemID: "pva_stick_l", Category: 30}, 12, 12},
		{"拟饵1套出1", store.ItemDef{ItemID: "hm_crank_65s_hq", Category: 17}, 1, 1},
		{"引线1套出1", store.ItemDef{ItemID: "leader_sample", Category: 13}, 1, 1},
		{"PVA网1套出1", store.ItemDef{ItemID: "pvas_b10", Category: 61}, 1, 1},
		{"奖板1套出1", store.ItemDef{ItemID: "board_sample", Category: 71}, 1, 1},
		{"茶按物品计件单位", store.ItemDef{ItemID: "thermos_tea", Category: 26}, maxCraftQty, 1},
		{"面包按物品计件单位", store.ItemDef{ItemID: "bread", Category: 27}, maxCraftQty, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec := craftBatchSpecFor(&c.def)
			if spec.Limit != c.limit || spec.Yield != c.yield {
				t.Fatalf("craftBatchSpecFor(%s)=%+v, want limit=%d yield=%d",
					c.def.ItemID, spec, c.limit, c.yield)
			}
		})
	}
	if spec := craftBatchSpecFor(nil); spec.Limit != 1 || spec.Yield != 1 {
		t.Fatalf("nil 定义应按 1 套出 1，实得 %+v", spec)
	}
}

func TestCraftTimesFor(t *testing.T) {
	cases := []struct {
		name string
		def  store.ItemDef
		qty  uint64
		want int64
	}{
		{"窝料钳到60", store.ItemDef{ItemID: "feedcompleted", Category: 46}, 100, 60},
		{"窝料不放大", store.ItemDef{ItemID: "feedcompleted", Category: 46}, 30, 30},
		{"PVA10g钳到60", store.ItemDef{ItemID: "pva_stick_s", Category: 30}, 100, 60},
		{"PVA20g钳到30", store.ItemDef{ItemID: "pva_stick_m", Category: 30}, 100, 30},
		{"PVA50g钳到12", store.ItemDef{ItemID: "pva_stick_l", Category: 30}, 100, 12},
		{"面包不限", store.ItemDef{ItemID: "bread", Category: 27}, 101, 101},
		{"茶不限", store.ItemDef{ItemID: "ab_tea_can", Category: 26}, 5000, 5000},
		{"拟饵钳到1", store.ItemDef{ItemID: "hm_crank_65s_hq", Category: 17}, 5, 1},
		{"引线钳到1", store.ItemDef{ItemID: "leader_sample", Category: 13}, 3, 1},
		{"PVA网钳到1", store.ItemDef{ItemID: "pvas_b10", Category: 61}, 3, 1},
		{"奖板钳到1", store.ItemDef{ItemID: "board_sample", Category: 71}, 9, 1},
		{"零份按1", store.ItemDef{ItemID: "bread", Category: 27}, 0, 1},
		{"防溢出上限", store.ItemDef{ItemID: "bread", Category: 27}, ^uint64(0), maxCraftQty},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := craftTimesFor(craftBatchSpecFor(&c.def), c.qty); got != c.want {
				t.Fatalf("craftTimesFor(%s, %d)=%d, want %d", c.def.ItemID, c.qty, got, c.want)
			}
		})
	}
}

func TestScaleDemandsOverflow(t *testing.T) {
	per := int64(1) << 61
	if _, ok := scaleDemands([]craftDemand{{DemandID: "x", Qty: &per}}, 8); ok {
		t.Fatal("材料数量溢出应被拒绝")
	}
	four := int64(4)
	got, ok := scaleDemands([]craftDemand{{DemandID: "loaf", Qty: &four}}, 101)
	if !ok || got[0].need() != 404 {
		t.Fatalf("scaleDemands 应为 404，实得 %d ok=%v", got[0].need(), ok)
	}
}

func TestCraftBatchCapsAndSlotScaling(t *testing.T) {
	db := openRecipeTestDB(t)
	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)
	srv.randFloat = fixedRolls(0.9)

	setup := func(uid int64) *handlerBox {
		t.Helper()
		if _, err := db.EnsureProfile(ctx, uid); err != nil {
			t.Fatal(err)
		}
		sess := &Session{ID: uint64(uid), Userid: uid, Srv: srv, log: logging.Discard()}
		return &handlerBox{srv: srv, sess: sess, main: 9, sub: 1}
	}
	seed := func(uid int64, itemID string, count int64) *store.Item {
		t.Helper()
		it, err := srv.prepareNewItemWithCount(ctx, uid, itemID, "backpack", count, "")
		if err != nil {
			t.Fatalf("构造材料 %s 失败: %v", itemID, err)
		}
		if err := db.InsertItem(ctx, it); err != nil {
			t.Fatalf("写入材料 %s 失败: %v", itemID, err)
		}
		return it
	}
	countOf := func(uid int64, itemID string) int64 {
		t.Helper()
		rows, err := db.ListItemsByContainers(ctx, uid, []string{"backpack", "rig"})
		if err != nil {
			t.Fatal(err)
		}
		var n int64
		for _, it := range rows {
			if it.ItemID == itemID {
				n += srv.stackCountOf(ctx, it)
			}
		}
		return n
	}
	// 一组制作产出必须只占一格：背包中该物品的行数与数量同时校验。
	rowsOf := func(uid int64, itemID string) (rows int, qty int64) {
		t.Helper()
		if err := db.SQL.QueryRowContext(ctx,
			`SELECT COUNT(*), COALESCE(SUM(qty),0) FROM item
			  WHERE uid=? AND item_id=? AND container='backpack'`, uid, itemID).
			Scan(&rows, &qty); err != nil {
			t.Fatal(err)
		}
		return rows, qty
	}
	craft := func(box *handlerBox, recipe string, slots []craftSlot, qty uint64) bool {
		t.Helper()
		box.tree = proto.Args524(craftObj8(recipe, slots, qty))
		body, ok := box.invoke(t)
		if !ok {
			t.Fatalf("%s 未处理", recipe)
		}
		v := decodeTreeValue(t, srv, body)
		if !v.IsKind(proto.KindObj) || v.TypeID != 803 {
			t.Fatalf("%s 响应应为 803，实得 %v/%d", recipe, v.Kind, v.TypeID)
		}
		return u64Of(v.Fields[0].Num) == 1
	}

	t.Run("窝料钳到60并按份扣料", func(t *testing.T) {
		uid := int64(2200001)
		box := setup(uid)
		mix := seed(uid, "mix_combi", 100)
		slot := []craftSlot{{ItemID: "craft_feed_generic_mix_1", GUID: mix.GUID}}
		if !craft(box, "craft_feed_universal", slot, 100) {
			t.Fatal("窝料制作应成功")
		}
		// 上限 60 套，每套固定 60 个。
		if got := countOf(uid, "feedcompleted"); got != 60*60 {
			t.Fatalf("窝料应为 3600 个，实得 %d", got)
		}
		if rows, qty := rowsOf(uid, "feedcompleted"); rows != 1 || qty != 60*60 {
			t.Fatalf("一组窝料应只占一格 1x3600，实得 rows=%d qty=%d", rows, qty)
		}
		if got := countOf(uid, "mix_combi"); got != 40 {
			t.Fatalf("应扣 60 份混合饲料，实得剩余 %d", got)
		}
		// 客户端临时 GUID 走类目补扣路径，份数同样收敛到 60 套以内。
		if !craft(box, "craft_feed_universal", []craftSlot{{
			ItemID: "craft_feed_generic_mix_1",
			GUID:   "00112233445566778899aabbccddeeff",
		}}, 10) {
			t.Fatal("类目补扣制作应成功")
		}
		if got := countOf(uid, "feedcompleted"); got != 70*60 {
			t.Fatalf("两次制作应共 4200 个，实得 %d", got)
		}
		if rows, qty := rowsOf(uid, "feedcompleted"); rows != 2 || qty != 70*60 {
			t.Fatalf("窝料两组应各占一格，实得 rows=%d qty=%d", rows, qty)
		}
		if got := countOf(uid, "mix_combi"); got != 30 {
			t.Fatalf("类目补扣后应剩 30 份混合饲料，实得 %d", got)
		}
	})

	pvaCases := []struct {
		name    string
		recipe  string
		output  string
		dryEach int64
		cap     int64
		units   int64
	}{
		{"PVA10g钳到60套", "craft_feed_pva_s", "pva_stick_s", 1, 60, 60 * 60},
		{"PVA20g钳到30套", "craft_feed_pva_m", "pva_stick_m", 2, 30, 30 * 30},
		{"PVA50g钳到12套", "craft_feed_pva_l", "pva_stick_l", 5, 12, 12 * 12},
	}
	for i, c := range pvaCases {
		t.Run(c.name, func(t *testing.T) {
			uid := int64(2200010 + i)
			box := setup(uid)
			dry := seed(uid, "feed_dry", 200)
			mat := seed(uid, "hvz_pva_material", 200)
			slots := []craftSlot{
				{ItemID: "craft_mat_feed_dry", GUID: dry.GUID},
				{ItemID: "craft_feed_pva_material", GUID: mat.GUID},
			}
			if !craft(box, c.recipe, slots, 100) {
				t.Fatal("PVA 制作应成功")
			}
			if got := countOf(uid, c.output); got != c.units {
				t.Fatalf("%s 应为 %d 份，实得 %d", c.output, c.units, got)
			}
			if rows, qty := rowsOf(uid, c.output); rows != 1 || qty != c.units {
				t.Fatalf("%s 应只占一格 1x%d，实得 rows=%d qty=%d", c.output, c.units, rows, qty)
			}
			if want := 200 - c.dryEach*c.cap; countOf(uid, "feed_dry") != want {
				t.Fatalf("干混合应剩 %d，实得 %d", want, countOf(uid, "feed_dry"))
			}
			if want := 200 - c.cap; countOf(uid, "hvz_pva_material") != want {
				t.Fatalf("PVA 原料应剩 %d，实得 %d", want, countOf(uid, "hvz_pva_material"))
			}
			// 再做一组：PVA 不与其他批次合并，应新增一格。
			if !craft(box, c.recipe, slots, 1) {
				t.Fatal("第二次 PVA 制作应成功")
			}
			per := c.units / c.cap
			if rows, qty := rowsOf(uid, c.output); rows != 2 || qty != c.units+per {
				t.Fatalf("%s 两组应各占一格，实得 rows=%d qty=%d", c.output, rows, qty)
			}
		})
	}

	t.Run("拟饵单份", func(t *testing.T) {
		uid := int64(2200020)
		box := setup(uid)
		for _, id := range []string{"material_najdak", "material_steel_wire", "material_birch_bar"} {
			seed(uid, id, 5)
		}
		if !craft(box, "craft_lure_woodcrank_65", nil, 5) {
			t.Fatal("拟饵制作应成功")
		}
		if got := countOf(uid, "hm_crank_65s_hq"); got != 1 {
			t.Fatalf("拟饵应只产出 1 个，实得 %d", got)
		}
		for _, id := range []string{"material_najdak", "material_steel_wire", "material_birch_bar"} {
			if got := countOf(uid, id); got != 4 {
				t.Fatalf("材料 %s 应只扣 1 份，实得剩余 %d", id, got)
			}
		}
	})

	t.Run("面包不限份", func(t *testing.T) {
		uid := int64(2200030)
		box := setup(uid)
		seed(uid, "loaf_01", 500)
		if !craft(box, "craft_bait_bread", nil, 101) {
			t.Fatal("面包制作应成功")
		}
		if got := countOf(uid, "bread"); got != 30*101 {
			t.Fatalf("面包应为 %d 份，实得 %d", 30*101, got)
		}
		if got := countOf(uid, "loaf_01"); got != 96 {
			t.Fatalf("面包胚应剩 96，实得 %d", got)
		}
	})

	t.Run("茶一套6份", func(t *testing.T) {
		uid := int64(2200035)
		box := setup(uid)
		seed(uid, "ab_tea_can", 50)
		if !craft(box, "craft_cook_thermos_tea", nil, 1) {
			t.Fatal("茶制作应成功")
		}
		if got := countOf(uid, "thermos_tea"); got != 6 {
			t.Fatalf("一套茶材应出 6 份，实得 %d", got)
		}
		if got := countOf(uid, "ab_tea_can"); got != 49 {
			t.Fatalf("茶包应只扣 1 份，实得剩余 %d", got)
		}
		if !craft(box, "craft_cook_thermos_tea", nil, 3) {
			t.Fatal("批量茶制作应成功")
		}
		if got := countOf(uid, "thermos_tea"); got != 6+3*6 {
			t.Fatalf("再 3 套应共 24 份，实得 %d", got)
		}
		if got := countOf(uid, "ab_tea_can"); got != 46 {
			t.Fatalf("再扣 3 份茶包后应剩 46，实得 %d", got)
		}
	})

	t.Run("PVA网单份", func(t *testing.T) {
		uid := int64(2200040)
		box := setup(uid)
		seed(uid, "hvz_pva_string", 5)
		if !craft(box, "craft_pva_stinger_boil_10", nil, 5) {
			t.Fatal("PVA 网制作应成功")
		}
		var rows, qty int64
		if err := db.SQL.QueryRow(`SELECT COUNT(*), COALESCE(SUM(qty),0) FROM item
			WHERE uid=? AND item_id='pvas_b10' AND container='backpack'`, uid).
			Scan(&rows, &qty); err != nil {
			t.Fatal(err)
		}
		if rows != 1 || qty != 1 {
			t.Fatalf("PVA 网应为 1 个实例 x1，实得 rows=%d qty=%d", rows, qty)
		}
		if got := countOf(uid, "hvz_pva_string"); got != 4 {
			t.Fatalf("PVA 线应只扣 1 份，实得剩余 %d", got)
		}
	})

	t.Run("跨多个材料堆扣料上报全部GUID", func(t *testing.T) {
		uid := int64(2200045)
		box := setup(uid)
		first := seed(uid, "mix_combi", 1)
		second := seed(uid, "mix_combi", 1)
		// 客户端临时 GUID 走类目补扣；2 套需求跨两个各 1 份的材料堆扣完。
		box.tree = proto.Args524(craftObj8("craft_feed_universal", []craftSlot{{
			ItemID: "craft_feed_generic_mix_1",
			GUID:   "00112233445566778899aabbccddeeff",
		}}, 2))
		body, ok := box.invoke(t)
		if !ok {
			t.Fatal("9/1 未处理")
		}
		v := decodeTreeValue(t, srv, body)
		if !v.IsKind(proto.KindObj) || v.TypeID != 803 || u64Of(v.Fields[0].Num) != 1 {
			t.Fatalf("制作应成功，实得 %v/%d", v.Kind, v.TypeID)
		}
		f8 := v.Fields[8].Val
		if f8 == nil || f8.Kind != proto.KindGuidArr {
			t.Fatalf("803.f8 应为 GUID 列表，实得 %+v", f8)
		}
		removed := map[string]bool{}
		for _, it := range f8.Items {
			removed[it.GuidHex()] = true
		}
		if len(removed) != 2 || !removed[first.GUID] || !removed[second.GUID] {
			t.Fatalf("803.f8 应包含两个耗尽的材料堆 %s/%s，实得 %v",
				first.GUID, second.GUID, removed)
		}
		if got := countOf(uid, "mix_combi"); got != 0 {
			t.Fatalf("两个材料堆都应耗尽，实得剩余 %d", got)
		}
	})

	t.Run("零材料配方限制1份", func(t *testing.T) {
		uid := int64(2200048)
		box := setup(uid)
		for _, c := range []struct{ recipe, output string }{
			{"craft_crushed_boils", "crushed_boils"},
			{"craft_bait_cut_fish", "fish_pieces"},
		} {
			if !craft(box, c.recipe, nil, 1000) {
				t.Fatalf("%s 零材料制作应成功", c.recipe)
			}
			// 物品定义自带单位量（fish_pieces 每批 30），按事务记录的套数断言批次。
			var note string
			if err := db.SQL.QueryRow(
				`SELECT note FROM txn WHERE uid=? AND kind='craft' ORDER BY id DESC LIMIT 1`, uid).
				Scan(&note); err != nil {
				t.Fatalf("%s 流水缺失: %v", c.recipe, err)
			}
			if !strings.HasSuffix(note, "qty=1") {
				t.Fatalf("%s 零材料配方应限制 1 套，流水为 %q", c.recipe, note)
			}
			if got := countOf(uid, c.output); got <= 0 || got >= 1000 {
				t.Fatalf("%s 零材料配方发生批量产出，实得 %d", c.output, got)
			}
		}
	})

	t.Run("材料不足不产出", func(t *testing.T) {
		uid := int64(2200050)
		box := setup(uid)
		dry := seed(uid, "feed_dry", 10)
		mat := seed(uid, "hvz_pva_material", 200)
		slots := []craftSlot{
			{ItemID: "craft_mat_feed_dry", GUID: dry.GUID},
			{ItemID: "craft_feed_pva_material", GUID: mat.GUID},
		}
		if craft(box, "craft_feed_pva_l", slots, 12) {
			t.Fatal("干混合不足时不应产出")
		}
		if got := countOf(uid, "pva_stick_l"); got != 0 {
			t.Fatalf("失败后不应有产物，实得 %d", got)
		}
		if got := countOf(uid, "feed_dry"); got != 10 {
			t.Fatalf("失败后材料不应被扣，实得 %d", got)
		}
		if got := countOf(uid, "hvz_pva_material"); got != 200 {
			t.Fatalf("失败后 PVA 原料不应被扣，实得 %d", got)
		}
	})
}
