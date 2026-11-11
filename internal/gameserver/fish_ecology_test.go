package gameserver

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestFishEcologySeedsSpeciesAndMatchesWaterLayers(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	staticPath := filepath.Join("..", "..", "server_data", "static.sqlite3")
	if _, err := db.SQL.Exec(`ATTACH DATABASE ? AS src`, staticPath); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT OR IGNORE INTO species_weight SELECT * FROM src.species_weight`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`DETACH DATABASE src`); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ensureFishEcology(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM fish_ecology`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 252 {
		t.Fatalf("鱼种生态档案初始化 %d 条，期望 252", count)
	}
	if _, err := db.SQL.Exec(`UPDATE fish_ecology SET profile_version=1, layer_pref=.01 WHERE fish_key='n.pike'`); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ensureFishEcology(ctx); err != nil {
		t.Fatal(err)
	}
	var version int
	var layer float64
	if err := db.SQL.QueryRow(`SELECT profile_version,layer_pref FROM fish_ecology WHERE fish_key='n.pike'`).Scan(&version, &layer); err != nil {
		t.Fatal(err)
	}
	if version != fishEcologyProfileVersion || layer <= .01 {
		t.Fatalf("旧版本生态行未自动升级: version=%d layer=%f", version, layer)
	}
	srv.tunCache = map[string]float64{"fish_ecology_enabled": 1}
	srv.tunAt = time.Now()

	// 水层匹配与季节/时段无关，直接测水层因子：合成鱼口权重还乘了季节与水温，
	// 冬季鲶鱼季节权重低，会把「底层更偏鲶鱼」这条断言带偏。
	pike := srv.fishEcology(ctx, "n.pike")
	catfish := srv.fishEcology(ctx, "w_catfish")
	surface := fishRollContext{rigType: 1, depthCM: 50}
	bottom := fishRollContext{rigType: 12, depthCM: 500}
	pikeSurface := srv.fishLayerWeightProfile(pike, surface)
	pikeBottom := srv.fishLayerWeightProfile(pike, bottom)
	catSurface := srv.fishLayerWeightProfile(catfish, surface)
	catBottom := srv.fishLayerWeightProfile(catfish, bottom)
	if pikeSurface <= catSurface {
		t.Fatalf("上层水层应更偏向掠食鱼: pike=%f catfish=%f", pikeSurface, catSurface)
	}
	if catBottom <= pikeBottom {
		t.Fatalf("底层应更偏向鲶鱼: catfish=%f pike=%f", catBottom, pikeBottom)
	}

	// 合成鱼口权重：暖季底层偏向鲶鱼，冬季鲶鱼被季节权重压低（season_mask 不含冬）。
	// 游戏历 24 倍速，这里固定游戏时刻，避免真实日期漂移。
	summer := time.Unix(worldDayEpochUnix+180*24*realSecPerGameHour+12*realSecPerGameHour, 0)
	winter := time.Unix(worldDayEpochUnix+350*24*realSecPerGameHour+12*realSecPerGameHour, 0)
	catSummer := srv.fishBiteEnvironmentWeight(ctx, "w_catfish", fishRollContext{rigType: 12, depthCM: 500, now: summer})
	catWinter := srv.fishBiteEnvironmentWeight(ctx, "w_catfish", fishRollContext{rigType: 12, depthCM: 500, now: winter})
	pikeSummer := srv.fishBiteEnvironmentWeight(ctx, "n.pike", fishRollContext{rigType: 12, depthCM: 500, now: summer})
	if catSummer <= pikeSummer {
		t.Fatalf("暖季底层应偏向鲶鱼: catfish=%f pike=%f", catSummer, pikeSummer)
	}
	if catWinter >= catSummer {
		t.Fatalf("冬季鲶鱼应被季节权重压低: 冬=%f 夏=%f", catWinter, catSummer)
	}
}

func TestEffectiveBaitDepthTracksFloatSinkingAndLureLayer(t *testing.T) {
	now := time.Now()
	floatState := &FishState{
		RigType: 1,
		DepthCM: 300,
		CastTs:  now.Add(-4 * time.Second),
		FallTs:  now.Add(-4 * time.Second),
	}
	if got := floatState.effectiveBaitDepthCMLocked(now); got != 100 {
		t.Fatalf("手杆下沉 4 秒后饵深=%d，期望 100cm", got)
	}
	floatState.FallTs = now.Add(-20 * time.Second)
	if got := floatState.effectiveBaitDepthCMLocked(now); got != 300 {
		t.Fatalf("手杆应停在调漂目标 300cm，实得 %d", got)
	}
	lureState := &FishState{RigType: 5, LureLayer: .8}
	if got := lureState.effectiveBaitDepthCMLocked(now); got != 400 {
		t.Fatalf("路亚水层 0.8 应折算 400cm，实得 %d", got)
	}
	if got := classifyLureMotion(0x800); got != "bottom_motionless" {
		t.Fatalf("0x800 应识别为在水底无移动，实得 %q", got)
	}
	if got := lureLayerFromMotion(0x1000, .5); got != .08 {
		t.Fatalf("水面运动应映射到浅层，实得 %f", got)
	}
	// 水面系动作：犬步/激溅必须落到水面层，否则水面拟饵按中层判鱼，
	// 只能抽到中层鱼种，客户端表现为"鱼来了但不咬"。
	if got := lureLayerFromMotion(0xD1000100, .5); got != .06 {
		t.Fatalf("犬步(walk_dog)应映射水面层 .06，实得 %f", got)
	}
	for _, mask := range []uint32{0x20, 0x420, 0x220, 0x820} {
		if got := lureLayerFromMotion(mask, .5); got != .08 {
			t.Fatalf("激溅组合 mask=%#x 应映射水面层 .08，实得 %f", mask, got)
		}
	}
	// 犬步与激溅同时出现时，犬步仍是更具体的水面动作，保持 .06。
	if got := lureLayerFromMotion(0x520, .5); got != .06 {
		t.Fatalf("犬步+激溅+收线 mask=0x520 应映射 .06，实得 %f", got)
	}
	if got := lureLayerFromMotion(0x400, .5); got != .53 {
		t.Fatalf("普通收线应仍按 +.03 变化，实得 %f", got)
	}
	if got := (&Server{}).rigWaterLayer(fishRollContext{rigType: 5, lureLayer: lureLayerFromMotion(0xD1000100, .5)}); got > .2 {
		t.Fatalf("水面拟饵收线时水层应判水面，实得 %f", got)
	}
}

func TestClassifyLureMotionUsesClientStatusBits(t *testing.T) {
	cases := []struct {
		mask uint32
		want string
	}{
		{0xD1000010, "twitch"},
		{0xD1000080, "jig"},
		{0xD1000100, "walk_dog"},
		{0xD1002000, "bottom_move"},
		{0xD1004000, "trolling"},
		{0xD1010000, "stop_go"},
		{0xD1020000, "medium_jerk"},
		{0xD1040000, "little_jerk"},
		{0xD1400000, "bottom_layer_move"},
		{0xD1000404, "retrieval_pause"},
		{0xD1000400, "steady"},
		{0xD1000420, "splash"},
		{0xD1000520, "walk_dog"},
		{0xD1008000, ""},
		{0xD1100000, ""},
	}
	for _, mask := range []uint32{0x4, 0x40, 0x80, 0x100, 0x2000, 0x4000, 0x20000, 0x40000} {
		if lureMotionInWaterMask&mask == 0 {
			t.Errorf("0x%X 应算饵在水中运动", mask)
		}
	}
	for _, mask := range []uint32{0x8, 0x200, 0x800, 0x1000} {
		if lureMotionInWaterMask&mask != 0 {
			t.Errorf("0x%X 是静置或触底姿态，不应算饵在水中运动", mask)
		}
	}
	for _, tc := range cases {
		if got := classifyLureMotion(tc.mask); got != tc.want {
			t.Errorf("classifyLureMotion(0x%X)=%q，期望 %q", tc.mask, got, tc.want)
		}
	}
	if got := lureLayerFromMotion(0xD1080000, .1); got != .18 {
		t.Fatalf("下沉动作应增加水层深度到 .18，实得 %f", got)
	}
}

func TestFishEcologyActionWeightFallsBackToLegacyRows(t *testing.T) {
	legacy := map[string]float64{
		"moving":                  1.25,
		"bottom_motionless":       1.4,
		"water_column_motionless": 1.1,
		"surface_motionless":      1.2,
	}
	cases := []struct {
		action string
		want   float64
	}{
		{"steady", 1.25},
		{"bottom_move", 1.4},
		{"bottom_contact", 1.4},
		{"surface", 1.2},
		{"sinking", 1.1},
	}
	for _, tc := range cases {
		got, ok := fishEcologyActionWeight(legacy, tc.action)
		if !ok || got != tc.want {
			t.Errorf("action %s weight=%f,%v，期望 %f,true", tc.action, got, ok, tc.want)
		}
	}
}

func TestLurePulseOverridesSteadyReelState(t *testing.T) {
	now := time.Now()
	st := &FishState{LureMotionMask: 0xD1000400}
	st.recordLurePulseLocked(now)
	if st.LureAction != "twitch" {
		t.Fatalf("单次右键脉冲应覆盖 steady，得到 %q", st.LureAction)
	}
	st.recordLurePulseLocked(now.Add(100 * time.Millisecond))
	if st.LureAction != "jerk" {
		t.Fatalf("第二次右键脉冲应为 jerk，得到 %q", st.LureAction)
	}
	st.recordLurePulseLocked(now.Add(200 * time.Millisecond))
	if st.LureAction != "hard_twitch" {
		t.Fatalf("连续三次右键脉冲应为 hard_twitch，得到 %q", st.LureAction)
	}
}
