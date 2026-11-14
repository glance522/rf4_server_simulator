package fishmaps

import "testing"

// 行数与逐行形状守卫：本表是查鱼扫描与后台地图编辑器共用的单一事实源，
// 搬家/改值必须在这里先红。
func TestScenesShape(t *testing.T) {
	if len(Scenes) != 17 {
		t.Fatalf("Scenes 行数 = %d, 期望 17（与 docs/fish-query-coordinates.md 支持表一致）", len(Scenes))
	}
	for key, sc := range Scenes {
		if sc.Paper == nil {
			t.Errorf("%s 缺纸地图标定", key)
			continue
		}
		if sc.Paper.Scale <= 0 {
			t.Errorf("%s scale = %v, 必须为正", key, sc.Paper.Scale)
		}
		if sc.Paper.Width != 2048 || sc.Paper.Height != 2048 {
			t.Errorf("%s 贴图尺寸 = %v×%v, 期望 2048×2048", key, sc.Paper.Width, sc.Paper.Height)
		}
		if sc.Size <= 0 {
			t.Errorf("%s 地形边长 = %v, 必须为正", key, sc.Size)
		}
		if name, known := TextureFor(key); !known {
			t.Errorf("%s 有标定但不在贴图名单 paperMapLevels", key)
		} else if name == "" {
			t.Errorf("%s 贴图名为空", key)
		}
	}
}

// 抽查已核对的标定数值（与客户端离线资源一致，改动此处需重新核对客户端）。
func TestSceneCalibrationSpotChecks(t *testing.T) {
	sc, ok := Scenes["level_001_lake"]
	if !ok {
		t.Fatal("缺 level_001_lake")
	}
	if sc.OriginX != 0 || sc.OriginZ != 0 || sc.Size != 600 {
		t.Errorf("level_001_lake 原点/边长 = %v/%v/%v, 期望 0/0/600", sc.OriginX, sc.OriginZ, sc.Size)
	}
	if sc.Paper.OffsetX != -745 || sc.Paper.OffsetZ != -835 || sc.Paper.Scale != 5 {
		t.Errorf("level_001_lake 标定 = %v/%v/%v, 期望 -745/-835/5", sc.Paper.OffsetX, sc.Paper.OffsetZ, sc.Paper.Scale)
	}
	sc, ok = Scenes["level_011_ladoga_02"]
	if !ok {
		t.Fatal("缺 level_011_ladoga_02")
	}
	if sc.OriginX != -500 || sc.OriginZ != -500 || sc.Size != 3000 {
		t.Errorf("level_011_ladoga_02 原点/边长 = %v/%v/%v, 期望 -500/-500/3000（外层主地形）", sc.OriginX, sc.OriginZ, sc.Size)
	}
}

func TestTextureFor(t *testing.T) {
	cases := []struct {
		level, wantName string
		wantKnown       bool
	}{
		{"level_001_lake", "map_level_001_lake", true},
		{"level_002_kirzah_river", "map_level_002_kirzah", true},
		{"level_004_torfyanoe_ozero", "map_level_004_torfyanoe", true},
		{"level_012_north_sea", "map_level_012_north_sea", true},
		{"level_000_home", "", false},
		{"", "", false},
		{"unknown", "", false},
	}
	for _, tc := range cases {
		name, known := TextureFor(tc.level)
		if name != tc.wantName || known != tc.wantKnown {
			t.Errorf("TextureFor(%q) = %q,%v; 期望 %q,%v", tc.level, name, known, tc.wantName, tc.wantKnown)
		}
	}
	// 挪威海有海图贴图但无网格标定（海图区号语义，不进 Scenes）。
	if _, ok := Scenes["level_012_north_sea"]; ok {
		t.Error("level_012_north_sea 不应出现在 Scenes（海图用声呐区号，不用纸地图网格标定）")
	}
}
