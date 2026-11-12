package gameserver

import (
	"math"
	"testing"
)

// TestPartKeyForCategory 钉住"品类 → 参考图六个部位"的归类。
//
// 参考图（各图装备维修参考图 2021-06-11）只有六列：鱼竿导环 / 装置 / 轴承 /
// 摩擦制动器 / 线轴 / 润滑油。我们的零件品类比它多，归类必须显式写死，
// 不然漏一个品类就会被悄悄按 1.0 处理，玩家在该图修理时看不到任何倍率。
func TestPartKeyForCategory(t *testing.T) {
	cases := []struct {
		cat  int64
		want string
	}{
		{5, "guide"},   // 导环
		{7, "bearing"}, // 轴承
		{9, "drag"},    // 摩擦制动器
		{8, "spool"},   // 线轴/线杯
		{10, "oil"},    // 润滑油
		// 装置：整机/结构件。齿轮是传动机构的一部分，图上没有独立列，归这里。
		{52, "device"},
		{6, "device"},  // 渔轮本体
		{3, "device"},  // 竿身
		{4, "device"},  // 竿柄
		{36, "device"}, // 竿坯
		{34, "device"}, // 摇柄（已废弃但仍可能出现在老物品树里）
		{63, "device"}, // 电机
		{81, "device"}, // 引擎
		// 未知品类不归类：宁可少乘，也不要把一个没归类的品类塞进某个部位。
		{999, ""},
	}
	for _, c := range cases {
		if got := partKeyForCategory(c.cat); got != c.want {
			t.Fatalf("品类 %d 的部位键应为 %q，实得 %q", c.cat, c.want, got)
		}
	}
}

// TestPartMultForMapPriority 验取值优先级：
// brand_parts[品牌][部位] → parts[部位] → 旧列 repair_* → 1.0。
//
// 数据全部来自 assets/map_price_multipliers.json（玩家提供的参考图），
// 断言写的是"这一格等于图上的那个数"，改数据就会红。
func TestPartMultForMapPriority(t *testing.T) {
	cases := []struct {
		name    string
		mapKey  string
		brand   string
		partKey string
		want    float64
	}{
		// 地图六列：拉多加湖 导环/轴承/摩擦/线轴 +37%、装置/轮油 +25%。
		{"拉多加湖导环", "level_006_ladoga", "", "guide", 1.37},
		{"拉多加湖装置", "level_006_ladoga", "", "device", 1.25},
		{"拉多加湖轮油", "level_006_ladoga", "", "oil", 1.25},
		// 廓里湖：四部位 +102%、装置/轮油 +35%。
		{"廓里湖导环", "level_003_viiveri_lake", "", "guide", 2.02},
		{"廓里湖装置", "level_003_viiveri_lake", "", "device", 1.35},
		// 阿赫图巴：四部位 +10%、装置/轮油 基础价。
		{"阿赫图巴轴承", "level_009_ahtuba", "", "bearing", 1.10},
		{"阿赫图巴装置", "level_009_ahtuba", "", "device", 1.00},
		// 品牌店（Beluga，拉多加湖）：装置 0.90、轴承/摩擦/线轴 0.81。
		{"Beluga装置", "level_006_ladoga", "beluga", "device", 0.90},
		{"Beluga轴承", "level_006_ladoga", "beluga", "bearing", 0.81},
		// Beluga 的导环/轮油在品牌列里是 null（不单列），退到地图六列。
		{"Beluga导环退地图列", "level_006_ladoga", "beluga", "guide", 1.37},
		{"Beluga轮油退地图列", "level_006_ladoga", "beluga", "oil", 1.25},
		// 品牌店（Zeiman，琥珀湖）：装置 0.90、三件 0.81、轮油 0.90。
		{"Zeiman轮油", "level_014_carp_lake", "zeiman", "oil", 0.90},
		{"Zeiman导环退地图列", "level_014_carp_lake", "zeiman", "guide", 1.15},
		// 参考图写「无」的图（梅德韦杰湖）：六列全 null、旧列也 null → 1.0。
		{"梅德韦杰湖无维修点", "level_007_grass_lake", "", "bearing", 1.00},
		// 完全没登记的地图 → 1.0。
		{"未知地图", "level_999_nowhere", "", "bearing", 1.00},
	}
	for _, c := range cases {
		if got := partMultForMap(c.mapKey, c.brand, c.partKey); math.Abs(got-c.want) > 1e-9 {
			t.Fatalf("%s：%s/%s/%s 倍率应为 %v，实得 %v",
				c.name, c.mapKey, c.brand, c.partKey, c.want, got)
		}
	}
}

// TestPartMultiplierForStore 验"工坊 id → 地图 + 品牌"这一跳，
// 以及"店铺没登记时用当前地图兜底"这条保命路径。
func TestPartMultiplierForStore(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	defer db.Close()
	cases := []struct {
		name     string
		storeID  string
		fallback string
		cat      int64
		want     float64
	}{
		// 普通工坊：倍率只由地图六列决定。
		{"拉多加湖普通工坊-导环", "workshop_006", "", 5, 1.37},
		{"拉多加湖普通工坊-齿轮归装置", "workshop_006", "", 52, 1.25},
		// 品牌工坊：品牌列优先。
		{"Beluga工坊-轴承", "belugaworkshop_006", "", 7, 0.81},
		{"Beluga工坊-齿轮归装置", "belugaworkshop_006", "", 52, 0.90},
		// 店铺不在表里 → 用 fallbackMapKey（玩家当前所在地图）。
		{"未登记工坊用当前地图", "workshop_not_registered", "level_003_viiveri_lake", 5, 2.02},
		// 两个都没值 → 1.0，绝不乱乘。
		{"未知工坊且未知地图", "workshop_not_registered", "level_999_nowhere", 5, 1.00},
	}
	for _, c := range cases {
		if got := srv.partMultiplierForStore(c.storeID, c.fallback, c.cat); math.Abs(got-c.want) > 1e-9 {
			t.Fatalf("%s：倍率应为 %v，实得 %v", c.name, c.want, got)
		}
	}
}

// TestPartMultiplierSwitchOff 验总开关：置 0 后一切回到标价。
//
// 这个开关是防"客户端自己在显示端也乘了一次 location_index"的双乘风险。
// 抓包里只有一单 21/4 且在倍率 1.00 的图，无法从证据上排除双乘，
// 所以必须能一键（改库里的 tunable，不重新编译）退回。
func TestPartMultiplierSwitchOff(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	defer db.Close()
	if got := srv.partMultiplierForStore("workshop_006", "", 5); math.Abs(got-1.37) > 1e-9 {
		t.Fatalf("默认应启用部位倍率（导环 1.37），实得 %v", got)
	}
	if err := db.TunableSet(t.Context(), tunableWorkshopPartMapMult, "0", "test"); err != nil {
		t.Fatalf("设置开关失败: %v", err)
	}
	// tunableF 有 30 秒缓存，直接清掉让它重读。
	srv.tunMu.Lock()
	srv.tunCache = nil
	srv.tunMu.Unlock()
	if got := srv.partMultiplierForStore("workshop_006", "", 5); math.Abs(got-1.0) > 1e-9 {
		t.Fatalf("开关置 0 后倍率应为 1.0，实得 %v", got)
	}
}

// TestApplyPartMultKeepsResidualRatio 是本轮改动的核心契约：
// 倍率必须让 (新件价 − 旧件残值) 这一整个差额同比例放大。
//
// 客户端算报价用的是"新件价 f2 − 旧件残值 f3"，服务端按同一个差额收钱。
// 如果只乘 f2、不乘 f3，客户端复算出来的价格和我们收的钱就会差一个倍率，
// 报价校验（5% 容差）直接拒单 → 玩家看到「发生内部错误」。
func TestApplyPartMultKeepsResidualRatio(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	defer db.Close()
	// 齿轮：标价 296、残值率 0.9 → 差额 30（与官服抓包 21/4 f3=30 一致）。
	const base = int64(296)
	for _, mult := range []float64{0.81, 0.85, 0.90, 1.00, 1.10, 1.25, 1.35, 1.37, 2.02} {
		eff, _ := applyPartMult(base, 0, mult)
		baseRes, _ := srv.workshopPartResidual(52, base, 0)
		effRes, _ := srv.workshopPartResidual(52, eff, 0)
		wantDiff := float64(base-baseRes) * mult
		gotDiff := float64(eff - effRes)
		if math.Abs(gotDiff-wantDiff) > math.Max(2, wantDiff*0.05) {
			t.Fatalf("倍率 %v：差额应为约 %.1f，实得 %.0f（新件 %d − 残值 %d）",
				mult, wantDiff, gotDiff, eff, effRes)
		}
		// 残值必须严格小于新件价，否则客户端会算出 0 或负数报价。
		if effRes >= eff {
			t.Fatalf("倍率 %v：残值 %d 不小于新件价 %d", mult, effRes, eff)
		}
	}
	// 倍率 1.0 必须原样返回，不做任何取整抖动。
	if s, g := applyPartMult(base, 5, 1.0); s != base || g != 5 {
		t.Fatalf("倍率 1.0 应原样返回 (296,5)，实得 (%d,%d)", s, g)
	}
}

// TestPartPriceForStoreScalesBothCurrencies 验倍率同时放大金银两列，
// 不把 partPrice 里"金币 = 银币/110"的官方汇率关系拆开。
func TestPartPriceForStoreScalesBothCurrencies(t *testing.T) {
	srv, db := newStaticRigTestServer(t)
	defer db.Close()
	ctx := t.Context()
	const gearID = "rgm_ef_spark10021" // 齿轮，category 52 → 装置档
	baseSilver, baseGold := srv.partPrice(ctx, gearID, 52)
	if baseSilver <= 0 || baseGold <= 0 {
		t.Fatalf("齿轮标价异常: (%d,%d)", baseSilver, baseGold)
	}
	// 拉多加湖普通工坊：装置档 1.25。
	gotSilver, gotGold := srv.partPriceForStore(ctx, "workshop_006", "", gearID, 52)
	if want := int64(math.Round(float64(baseSilver) * 1.25)); gotSilver != want {
		t.Fatalf("银币应为 %d（%d×1.25），实得 %d", want, baseSilver, gotSilver)
	}
	if want := int64(math.Round(float64(baseGold) * 1.25)); gotGold != want {
		t.Fatalf("金币应为 %d（%d×1.25），实得 %d", want, baseGold, gotGold)
	}
	// 空店铺 + 空地图 = 标价原样，供不传倍率的兜底路径使用。
	if s, g := srv.partPriceForStore(ctx, "", "", gearID, 52); s != baseSilver || g != baseGold {
		t.Fatalf("空店铺应返回标价 (%d,%d)，实得 (%d,%d)", baseSilver, baseGold, s, g)
	}
}
