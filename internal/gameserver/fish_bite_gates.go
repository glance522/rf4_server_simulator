package gameserver

import (
	"context"
	"math"
)

// 硬性鱼口资格门（tunable bite_gate_enabled，默认关）。
//
// 生态层（fishBiteEnvironmentWeight）是全软乘子：水层底线 ×0.005、总 clamp ≥0.08，
// 荒谬组合只降权、永不剔除，还会被 weightedChoice 的全零退化捞回来。门层补上
// 「这口饵根本钓不到这条鱼」的资格裁定，插在水域鱼种覆盖之后、饵料规则/分布/
// 生态权重之前：
//
//   - G1 钓法×食性（硬剔除）：钩上实际饵是拟饵（item_def 类别 17，含路亚族与
//     海钓铁板/飞领挂拟饵）时，鱼的生态食性 diet_json.lure 低于
//     bite_gate_lure_diet_min 直接出候选。默认 0.30：泥鳅/鲫鱼 0.20~0.25、
//     鲤族 0 不攻拟饵；狗鱼 2.07、江鳕 0.63、鲟族 0.30+（RF4 里 jig 钓鲟是
//     真实玩法）照常。
//   - G2 水层（软地板）：目标水层超出 [LayerMin−0.15, LayerMax+0.15] 时权重乘
//     bite_gate_layer_floor（默认 0.01），不剔除——深水鱼偶尔在浅水被钓起是
//     有意保留的极小杂口。0.15 容差经 252 种全量数据核对：没有任何鱼种的
//     LayerMax < 0.78，底钓（目标 0.93）零误伤；104 种底层鱼（鳗/七鳃鳗/鲟/
//     庸鲽）会被水面拟饵（0.06+0.15）地板。
//   - G-diet 天然饵双票（默认随门开启）：生态食性表 diet_json 首次参与咬口，
//     与攻略证据（fishing_bait_rules.txt / companion_bait_fish / loc_bait_fish）
//     合成双票，口径沿用窝料已验证的「双票打折」语义——
//     攻略认且生态认 → 原权重；明确禁饵或双不认 → 剔除；单边认 →
//     ×bite_gate_diet_floor 保留小口。
//
// 门层不动延迟链、概率掷骰、档位采样、嘬饵/保持率；GM 指定鱼（st.Armed）
// 在更早的分支已绕过全部判鱼。候选被剔空时返回空切片 + gateBlocked 置位，
// 由调用方走「本轮无符合条件的鱼」重排（与空轮掷骰同一语义），且不落
// 内置鱼池兜底。
type biteGateConfig struct {
	enabled     bool
	lureDietMin float64
	layerFloor  float64
	dietMin     float64
	dietFloor   float64
}

// biteGateLayerTol 水层容差。与生态层 fishLayerWeightProfile 的拟饵向上追击
// 容差（+.15）同口径，这里对两个方向统一用 0.15：浅滩浮钓（100cm→0.2）仍能
// 钓到 LayerMin≈0.31 的鲤科鱼，而水面拟饵钓不到 LayerMin≥0.44 的底栖鱼。
const biteGateLayerTol = 0.15

func (s *Server) biteGateConfig() biteGateConfig {
	if s.tunableF("bite_gate_enabled", 0) != 1 {
		return biteGateConfig{}
	}
	cfg := biteGateConfig{
		enabled:     true,
		lureDietMin: s.tunableF("bite_gate_lure_diet_min", 0.30),
		layerFloor:  s.tunableF("bite_gate_layer_floor", 0.01),
		dietMin:     s.tunableF("bite_gate_diet_min", 0.25),
		dietFloor:   s.tunableF("bite_gate_diet_floor", 0.05),
	}
	if cfg.lureDietMin < 0 {
		cfg.lureDietMin = 0
	}
	if cfg.layerFloor < 0 {
		cfg.layerFloor = 0
	}
	if cfg.layerFloor > 1 {
		cfg.layerFloor = 1
	}
	if cfg.dietMin < 0 {
		cfg.dietMin = 0
	}
	if cfg.dietFloor < 0 {
		cfg.dietFloor = 0
	}
	if cfg.dietFloor > 1 {
		cfg.dietFloor = 1
	}
	return cfg
}

// biteGateBaitSet 一次判鱼里钩上饵的归类结果。
// defsOK=false（饵定义读不到或没挂饵）时 G1/G-diet 保持中性，只剩 G2。
type biteGateBaitSet struct {
	defsOK  bool
	lure    bool
	natural []ruleBait
}

func (s *Server) biteGateBaitSet(ctx context.Context, c fishRollContext) biteGateBaitSet {
	if s.DB == nil || len(c.baits) == 0 {
		return biteGateBaitSet{}
	}
	out := biteGateBaitSet{defsOK: true}
	for _, id := range c.baits {
		if id == "" {
			continue
		}
		b, ok := s.cachedBaitDefinition(ctx, c, id)
		if !ok {
			// 与 filterFishBaitSet 同口径：饵定义读不到时整体放行，不因数据缺失封鱼。
			return biteGateBaitSet{}
		}
		switch b.category {
		case 17:
			out.lure = true
		case 27:
			out.natural = append(out.natural, b)
		}
	}
	return out
}

// biteGateDietKeys 把天然饵映射到 diet_json 的食性键。
// 类别 1 是杂类（香煮成饵/玉米/虾仁/贻贝肉混装），按任一命中放行生态票。
// 未知类型返回 nil：生态票按不认处理，只看攻略证据票。
func biteGateDietKeys(b ruleBait) []string {
	switch b.baitType {
	case 2, 6, 7: // 幼虫/蚯蚓水蛭/昆虫成虫
		return []string{"worm"}
	case 3, 4, 14, 15: // 蛙/活饵鱼/鱼片/死饵鱼
		return []string{"fish"}
	case 8, 9, 10, 17: // 煮饵/波趴/风味饵/Zig 球
		return []string{"boilie"}
	case 11, 13: // 老虎坚果/面团与粥
		return []string{"plant"}
	case 12, 16: // 钩虾/海虫贝类
		return []string{"crustacean"}
	case 1: // 杂类：植物/煮饵/动物肉任一
		return []string{"plant", "boilie", "crustacean"}
	}
	return nil
}

// applyBiteGates 过滤候选鱼种：G1 剔除食性不符的，G-diet 按双票打折，
// G2 给水层超界的乘地板。权重 ≤0 的候选直接跳过（与 filterFishBaitSet 同口径）。
// statLoc 是统计名册 loc（本地饵证据 loc_bait_fish 按它查），兜底鱼池路径没有时传空。
func (s *Server) applyBiteGates(ctx context.Context, keys []string, wts []float64, c fishRollContext, cfg biteGateConfig, statLoc string) ([]string, []float64) {
	if len(keys) == 0 {
		return keys, wts
	}
	baits := s.biteGateBaitSet(ctx, c)
	target := s.rigWaterLayer(c)

	// 天然饵双票的攻略侧材料：鱼名→规则、companion 档位、本地实证。
	// 任一读取失败就跳过 G-diet（保持中性），G1/G2 照常。
	dietReady := baits.defsOK && len(baits.natural) > 0
	var names map[string]string
	var companion []map[string]int64
	var local []map[string]bool
	if dietReady {
		var ok bool
		if names, ok = s.cachedFishNames(ctx, c); !ok {
			dietReady = false
		}
	}
	if dietReady {
		for _, b := range baits.natural {
			companion = append(companion, s.cachedBaitTiers(ctx, c, b.id))
			local = append(local, s.cachedLocalBaitEvidence(ctx, c, statLoc, b.id, b.name))
		}
	}

	outKeys := make([]string, 0, len(keys))
	outWts := make([]float64, 0, len(wts))
	for i, key := range keys {
		if wts[i] <= 0 {
			continue
		}
		p := s.cachedFishEcology(ctx, c, key)
		diet := fishEcologyJSONMap(p.DietJSON)
		if baits.lure && diet["lure"] < cfg.lureDietMin {
			continue
		}
		if dietReady {
			score := s.biteGateDietScore(ctx, key, diet, baits.natural, names, companion, local, c, cfg)
			if score <= 0 {
				continue
			}
			wts[i] *= score
		}
		w := wts[i]
		if target < p.LayerMin-biteGateLayerTol || target > p.LayerMax+biteGateLayerTol {
			// 保底杂口（c.pityEnv）：地板抬到至少 0.05，让连续空杆的线能动；
			// 食性类硬门（G1/G-diet）不受 pityEnv 影响。
			floor := cfg.layerFloor
			if c.pityEnv && floor < 0.05 {
				floor = 0.05
			}
			w *= floor
		}
		outKeys = append(outKeys, key)
		outWts = append(outWts, w)
	}
	return outKeys, outWts
}

// biteGateDietScore 天然饵双票：返回 0（剔除）/ dietFloor（单票）/ 1（双票）。
// 多饵/多钩取最好的一票；任一饵命中明确禁饵直接剔除（与 strict 通道同语义）。
func (s *Server) biteGateDietScore(ctx context.Context, key string, diet map[string]float64,
	baits []ruleBait, names map[string]string, companion []map[string]int64, local []map[string]bool, c fishRollContext, cfg biteGateConfig) float64 {
	r, _ := s.resolveBaitRule(ctx, names[key])
	best := 0.0
	for j, b := range baits {
		if bottomRigTypes[c.rigType] && len(r.bottomOnly) > 0 && !b.matchesAny(r.bottomOnly) {
			return 0
		}
		denied := false
		for _, term := range r.deny {
			if b.matches(term) {
				denied = true
				break
			}
		}
		if denied {
			return 0
		}
		allowed := false
		for _, term := range r.allow {
			if b.matches(term) {
				allowed = true
				break
			}
		}
		evidence := allowed || companion[j][key] > 0 || local[j][key]
		ecology := false
		for _, dk := range biteGateDietKeys(b) {
			if diet[dk] >= cfg.dietMin {
				ecology = true
				break
			}
		}
		switch {
		case evidence && ecology:
			return 1
		case evidence || ecology:
			best = math.Max(best, cfg.dietFloor)
		}
	}
	return best
}
