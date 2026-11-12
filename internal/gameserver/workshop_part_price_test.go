package gameserver

import (
	"testing"

	"rf4server/internal/store"
)

// 覆盖层（workshop_part_price，迁移 9100010）的取值口径由玩家 2026-11-12 定：
// **库里有值就用库里的，库里没有走兜底，任何缺失都不许让报价报错。**
//
// 这一组用例只守这条链，不碰残值/倍率本身（那是 workshop_repair_test.go 和
// repair_part_mult_test.go 的事）。
const workshopOverrideBearing = "ef-sp_001_bb"

func int64p(v int64) *int64 { return &v }

// TestWorkshopPartPriceOverrideWinsAndSkipsMult：命中覆盖行就原样用，
// 不再乘部位倍率——运营把价写死了，再乘一次就不是他填的数。
func TestWorkshopPartPriceOverrideWinsAndSkipsMult(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	defer db.Close()
	ctx := t.Context()

	baseS, _ := srv.partPrice(ctx, workshopOverrideBearing, reelPartCatBearing)
	if baseS <= 0 {
		t.Fatalf("用例前提不成立：%s 标价应为正，实得 %d", workshopOverrideBearing, baseS)
	}
	const mapKey = "level_003_viiveri_lake" // 廓里湖：轴承部位倍率 2.02
	multS, _ := srv.partPriceForStore(ctx, "", mapKey, workshopOverrideBearing, reelPartCatBearing)
	if multS == baseS {
		t.Fatalf("用例前提不成立：%s 应被部位倍率放大，实得仍为标价 %d", mapKey, baseS)
	}

	if err := db.SaveWorkshopPartPrice(ctx, workshopPartRec(workshopOverrideBearing, "", int64p(999), nil)); err != nil {
		t.Fatal(err)
	}
	gotS, gotG := srv.partPriceForStore(ctx, "", mapKey, workshopOverrideBearing, reelPartCatBearing)
	if gotS != 999 {
		t.Fatalf("全局覆盖行应生效且不乘倍率，期望 999，实得 %d", gotS)
	}
	// 只写银币时金币按银/110 重算，保住既有汇率。
	if gotG != 9 {
		t.Fatalf("只覆盖银币时金币应按 银/110 重算为 9，实得 %d", gotG)
	}
}

// TestWorkshopPartPriceOverrideMapBeatsGlobal：该图精确行优先于全局行。
func TestWorkshopPartPriceOverrideMapBeatsGlobal(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	defer db.Close()
	ctx := t.Context()

	if err := db.SaveWorkshopPartPrice(ctx, workshopPartRec(workshopOverrideBearing, "", int64p(999), nil)); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveWorkshopPartPrice(ctx, workshopPartRec(workshopOverrideBearing, "level_001_lake", int64p(1111), nil)); err != nil {
		t.Fatal(err)
	}
	if got, _ := srv.partPriceForStore(ctx, "", "level_001_lake", workshopOverrideBearing, reelPartCatBearing); got != 1111 {
		t.Fatalf("该图精确行应优先，期望 1111，实得 %d", got)
	}
	if got, _ := srv.partPriceForStore(ctx, "", "level_009_ahtuba", workshopOverrideBearing, reelPartCatBearing); got != 999 {
		t.Fatalf("别的图应退到全局行，期望 999，实得 %d", got)
	}
}

// TestWorkshopPartPriceOverrideGoldOnly 锁官服轮油那种写法：0 银 + 49 金。
// 这也验证"0 是有效值、不是缺失"——NULL 和 0 必须能表达出区别。
func TestWorkshopPartPriceOverrideGoldOnly(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	defer db.Close()
	ctx := t.Context()

	rec := workshopPartRec("oil_fan_001", "", nil, int64p(49))
	rec.Silver = int64p(0)
	if err := db.SaveWorkshopPartPrice(ctx, rec); err != nil {
		t.Fatal(err)
	}
	silver, gold := srv.partPriceForStore(ctx, "", "", "oil_fan_001", 10)
	if silver != 0 || gold != 49 {
		t.Fatalf("0 银 + 49 金应原样生效，实得 银=%d 金=%d", silver, gold)
	}
}

// TestWorkshopPartPriceOverrideMissingFallsBack：没有覆盖行时行为与以前一致，
// 即标价 × 部位倍率，绝不因为查不到覆盖就报错/报 0。
func TestWorkshopPartPriceOverrideMissingFallsBack(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	defer db.Close()
	ctx := t.Context()

	baseS, baseG := srv.partPrice(ctx, workshopOverrideBearing, reelPartCatBearing)
	const mapKey = "level_003_viiveri_lake"
	gotS, gotG := srv.partPriceForStore(ctx, "", mapKey, workshopOverrideBearing, reelPartCatBearing)
	wantS, wantG := applyPartMult(baseS, baseG, srv.partMultiplierForStore("", mapKey, reelPartCatBearing))
	if gotS != wantS || gotG != wantG {
		t.Fatalf("无覆盖行时应回落到 标价×倍率 (%d,%d)，实得 (%d,%d)", wantS, wantG, gotS, gotG)
	}
	// 标价本身也有兜底：不存在的件也不能返回 0（否则零金额判定会拒单）。
	if s, _ := srv.partPrice(ctx, "", 52); s <= 0 {
		t.Fatalf("未知件也必须走 partBasePrice 兜底，实得 %d", s)
	}
}

// TestWorkshopPartHoursOverride：工时覆盖 0 是有效值（即时），NULL 才回落到品类默认。
func TestWorkshopPartHoursOverride(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	defer db.Close()
	ctx := t.Context()

	const cat = int64(10) // 轮油
	def := srv.workshopPartHours(cat)
	if def == 0 {
		t.Fatalf("用例前提不成立：轮油品类默认工时应 >0")
	}
	if got := srv.workshopPartHoursFor(ctx, "", "oil_fan_001", cat); got != def {
		t.Fatalf("没有覆盖行时应用品类默认 %d，实得 %d", def, got)
	}

	rec := workshopPartRec("oil_fan_001", "", nil, nil)
	rec.Hours = int64p(0)
	if err := db.SaveWorkshopPartPrice(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if got := srv.workshopPartHoursFor(ctx, "", "oil_fan_001", cat); got != 0 {
		t.Fatalf("覆盖工时 0（即时）应生效，实得 %d", got)
	}

	// 该图精确行只写了价格、没写工时：工时仍要能取到全局行的值。
	if err := db.SaveWorkshopPartPrice(ctx, workshopPartRec("oil_fan_001", "level_001_lake", int64p(70), nil)); err != nil {
		t.Fatal(err)
	}
	if got := srv.workshopPartHoursFor(ctx, "level_001_lake", "oil_fan_001", cat); got != 0 {
		t.Fatalf("精确行没写工时时应继续用全局行的 0，实得 %d", got)
	}
}

// TestWorkshopPartPriceDeleteRestoresBase：删掉覆盖行立即回到标价。
func TestWorkshopPartPriceDeleteRestoresBase(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	defer db.Close()
	ctx := t.Context()

	baseS, _ := srv.partPrice(ctx, workshopOverrideBearing, reelPartCatBearing)
	if err := db.SaveWorkshopPartPrice(ctx, workshopPartRec(workshopOverrideBearing, "", int64p(999), nil)); err != nil {
		t.Fatal(err)
	}
	if got, _ := srv.partPriceForStore(ctx, "", "", workshopOverrideBearing, reelPartCatBearing); got != 999 {
		t.Fatalf("覆盖应先生效，实得 %d", got)
	}
	if err := db.DeleteWorkshopPartPrice(ctx, workshopOverrideBearing, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := srv.partPriceForStore(ctx, "", "", workshopOverrideBearing, reelPartCatBearing); got != baseS {
		t.Fatalf("删除覆盖后应回到标价 %d，实得 %d", baseS, got)
	}
}

// workshopPartRec 拼一条覆盖行，减少各用例的样板。
func workshopPartRec(itemID, mapKey string, silver, gold *int64) store.WorkshopPartPrice {
	return store.WorkshopPartPrice{ItemID: itemID, MapKey: mapKey, Silver: silver, Gold: gold}
}
