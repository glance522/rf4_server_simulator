package gameserver

import (
	"context"
	"strings"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func TestPurchasedLoadBearingCatalogHasUsableStrength(t *testing.T) {
	_, db := newBalanceTestServer(t)
	ctx := context.Background()
	rows, err := db.SQL.QueryContext(ctx, `SELECT d.numeric_id,d.item_id,d.category,COALESCE(d.attrs_json,''),EXISTS(SELECT 1 FROM shop_item p WHERE p.item_id=d.item_id) FROM item_def d WHERE d.category IN (3,6,11,12,13,14,15)`)
	if err != nil {
		t.Fatal(err)
	}
	type entry struct {
		def       store.ItemDef
		purchased bool
	}
	var defs []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.def.NumericID, &e.def.ItemID, &e.def.Category, &e.def.AttrsJSON, &e.purchased); err != nil {
			t.Fatal(err)
		}
		defs = append(defs, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	counts, sold, missing := map[int64]int{}, map[int64]int{}, map[int64]int{}
	for _, e := range defs {
		d := &e.def
		counts[d.Category]++
		var strength float64
		switch d.Category {
		case catReel:
			strength = reelLockedLoadKg(d, reelDragMaxKg(d), 1, reelBearingFactor(nestedNumber(itemDefNested(d, "fkmdfljgann"), reelPartQualityAttr, 100)))
		case gearCatLeader:
			strength = leaderStrengthKg(d)
		default:
			strength = itemDefF32(d, gearAttrMaxLoad)
		}
		if !isFinitePositive(strength) {
			missing[d.Category]++
		}
		if e.purchased {
			sold[d.Category]++
			if !isFinitePositive(strength) {
				t.Errorf("purchasable %s has no usable load rating", d.ItemID)
			}
		}
	}
	if len(defs) < 4000 || sold[catHook] < 1000 || counts[gearCatLeader] < 180 {
		t.Fatal("incomplete catalog traversal", len(defs), counts)
	}
	t.Logf("load-bearing definitions=%d categories=%v sold=%v missing static ratings=%v", len(defs), counts, sold, missing)
}

func craftLeaderForTest(t *testing.T, s *Server, recipe string, slots []craftSlot, qty uint64) bool {
	t.Helper()
	body, handled := hCraft(&Ctx{Sess: newProbeSess(s), Tree: proto.Args524(craftObj8(recipe, slots, qty)), DecodeOK: true, ExpectResponse: true, Log: logging.Discard()})
	if !handled {
		t.Fatal("craft was not handled")
	}
	v, _, err := proto.DecodeValue(body, 0, s.Table())
	if err != nil {
		t.Fatal(err)
	}
	return v.Fields[0].Num != nil && u64Of(v.Fields[0].Num) != 0
}

func TestEveryCraftedLeaderRecipePersistsActualMaterials(t *testing.T) {
	s, db := newBalanceTestServer(t)
	ctx := context.Background()
	line, err := s.CreateItem(ctx, testUID, "mono_15095", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := s.CreateItem(ctx, testUID, "swlock_40009", "backpack", 100)
	if err != nil {
		t.Fatal(err)
	}
	recipes, err := db.ListRecipes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	count, consumedLine := 0, int64(0)
	before := s.itemLength(ctx, line)
	for _, r := range recipes {
		if !strings.HasPrefix(r.OutputID, "leader_crafted_") {
			continue
		}
		var slots []craftSlot
		for _, d := range parseDemands(r.MaterialsJSON) {
			guid := lock.GUID
			if d.DemandID == "craft_mat_line" {
				guid = line.GUID
				consumedLine += d.need()
			}
			slots = append(slots, craftSlot{ItemID: d.DemandID, GUID: guid})
		}
		if !craftLeaderForTest(t, s, r.RecipeID, slots, 1) {
			t.Fatalf("recipe %s failed", r.RecipeID)
		}
		count++
	}
	rows, err := db.ListItemsByContainers(ctx, testUID, []string{"backpack"})
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, row := range rows {
		if !isCraftedLeader(row) {
			continue
		}
		tree, err := s.ItemToTree(ctx, row)
		if err != nil || leaderFieldsStrength(tree.Fields, row.Durability) <= 0 {
			t.Fatalf("%s lost material strength: %v", row.ItemID, err)
		}
		found++
	}
	after, _ := db.GetItemByGUID(ctx, line.GUID)
	if count != 52 || found != count || s.itemLength(ctx, after) != before-consumedLine {
		t.Fatal(count, found, consumedLine)
	}
	t.Logf("recipes=%d persisted leaders=%d line consumed=%dm", count, found, consumedLine)
}

func TestCraftedLeaderInvalidSelectionAndShortageRollBack(t *testing.T) {
	s, db := newBalanceTestServer(t)
	ctx := context.Background()
	line, _ := s.CreateItem(ctx, testUID, "mono_15095", "backpack", 1)
	lock, _ := s.CreateItem(ctx, testUID, "swlock_40009", "backpack", 1)
	foreign, _ := s.CreateItem(ctx, testUID+1, "swlock_40009", "backpack", 1)
	before, _ := db.GetItemByGUID(ctx, line.GUID)
	for _, slots := range [][]craftSlot{
		{{ItemID: "craft_mat_line", GUID: line.GUID}},
		{{ItemID: "craft_mat_line", GUID: lock.GUID}, {ItemID: "craft_mat_swivels", GUID: line.GUID}},
		{{ItemID: "craft_mat_line", GUID: line.GUID}, {ItemID: "craft_mat_swivels", GUID: foreign.GUID}},
		{{ItemID: "craft_mat_line", GUID: line.GUID}, {ItemID: "craft_mat_line", GUID: line.GUID}},
	} {
		if craftLeaderForTest(t, s, "craft_leader_10", slots, 1) {
			t.Fatal("invalid material selection accepted")
		}
	}
	slots := []craftSlot{{ItemID: "craft_mat_line", GUID: line.GUID}, {ItemID: "craft_mat_swivels", GUID: lock.GUID}}
	if craftLeaderForTest(t, s, "craft_leader_10", slots, 100) {
		t.Fatal("material shortage accepted")
	}
	after, _ := db.GetItemByGUID(ctx, line.GUID)
	if after.FieldsJSON != before.FieldsJSON {
		t.Fatal("failed craft consumed line before later material shortage")
	}
	var produced int
	if err := db.SQL.QueryRowContext(ctx, `SELECT COUNT(*) FROM item WHERE item_id LIKE 'leader_crafted_%'`).Scan(&produced); err != nil || produced != 0 {
		t.Fatal("failed craft left outputs", produced, err)
	}
}
