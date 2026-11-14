package gameserver

import "context"

// 抛投技能效果注册表（skill_cast_effect_def）的官方种子数据。
//
// 来源：客户端 4.0.25034 本地化文案逐字提取（key 形如 ability_<node_id>，
// 提取方法与完整对照表见 docs/cast-skill-official-effects.md）。官方阶梯
// 统一为「抛掷距离和准确性 +5%/级」，按装备类型生效：
//   - 竿侧行（gate_kind='rod'）：文案主语是具体竿型（用枪柄竿钓鱼/用纺车竿钓鱼…），
//     按竿实例 fopbhlmkcpe 匹配；
//   - 轮侧行（gate_kind='reel'）：文案主语是具体轮型（使用纺车轮时…），按轮实例
//     fopbhlmkcpe 匹配，且官方注明跨钓法大类通用（「只需学一次」）。
//
// 德语原名（区分中文误译）：
//
//	709 Baitcastrolle (Round-Reel)=鼓轮、717 Low-Profile Baitcastrolle=水滴轮、
//	718/721 kleine/große Conventional Multirollen=传统轮、514/701/313/1002
//	Stationärrolle=纺车轮、708 Casting-Rute=枪柄竿、702 Spinnrute=纺车竿。
//
// 已知局限：传统轮的轻型(718/1008)/重型(721/1020)细分在 fopbhlmkcpe 里同为
// type 4，本表统一 gate 4（见 spec Out of Scope）。
var castSkillEffectSeeds = []struct {
	CatID       int64
	NodeID      int64
	EffectKey   string
	PerLevelPct float64
	GateKind    string
	GateType    int64
}{
	// ---- 竿侧：+5%/级 ----
	{1, 501, "cast_distance", 5, "rod", 3},    // 用手竿钓鱼（手竿=telescope）
	{1, 503, "cast_distance", 5, "rod", 4},    // 使用博洛尼亚钓竿
	{1, 507, "cast_distance", 5, "rod", 5},    // 用赛竿钓鱼
	{2, 702, "cast_distance", 5, "rod", 1},    // 用纺车竿钓鱼
	{2, 708, "cast_distance", 5, "rod", 2},    // 用枪柄竿钓鱼
	{2, 715, "cast_distance", 5, "rod", 12},   // 用抽停竿(jerking)捕钓
	{3, 302, "cast_distance", 5, "rod", 10},   // 用飞德钓竿捕钓
	{3, 307, "cast_distance", 5, "rod", 9},    // 用PICKER竿钓鱼
	{3, 310, "cast_distance", 5, "rod", 11},   // 使用鲤鱼竿捕钓
	{3, 315, "cast_distance", 5, "rod", 13},   // 使用打窝竿（SPOD）
	{3, 316, "cast_distance", 5, "rod", 14},   // 使用测竿
	{11, 1001, "cast_distance", 5, "rod", 15}, // 使用柳叶亮片鱼竿 (pilker rod)
	{11, 1010, "cast_distance", 5, "rod", 16}, // 使用轻型船用鱼竿
	{11, 1015, "cast_distance", 5, "rod", 16}, // 使用中型船用鱼竿
	{11, 1023, "cast_distance", 5, "rod", 16}, // 使用重型船用鱼竿
	// ---- 轮侧：+5%/级，跨钓法大类生效 ----
	{1, 514, "cast_distance", 5, "reel", 1},   // 使用纺车轮
	{2, 701, "cast_distance", 5, "reel", 1},   // 使用纺车轮（与 514 同义，按大类各留一行）
	{3, 313, "cast_distance", 5, "reel", 1},   // 使用带有纺车轮的鱼竿
	{11, 1002, "cast_distance", 5, "reel", 1}, // 高速轮(纺车式)的使用
	{2, 709, "cast_distance", 5, "reel", 3},   // 使用动力辅助轮（鼓轮）= Baitcastrolle Round
	{2, 717, "cast_distance", 5, "reel", 2},   // 使用水滴轮 = Low-Profile Baitcastrolle
	{2, 718, "cast_distance", 5, "reel", 4},   // 使用轻型传统轮（Conventional）
	{2, 721, "cast_distance", 5, "reel", 4},   // 使用重型传统轮（Conventional）
	{11, 1008, "cast_distance", 5, "reel", 4}, // 使用轻型传统轮（海钓）
	{11, 1020, "cast_distance", 5, "reel", 4}, // 使用重型传统轮（海钓）
}

// ensureCastSkillEffects 启动路径种子官方抛投技能效果；可重复执行。
// 这些节点的官方语义是纯投距（竿侧附带鱼可控性、传统轮附带搏鱼能耗减免，
// 二者均不在本表范围），skill_fish_effect_def 里对应节点的自造鱼侧行
// （source='local-sim'）与官方文案矛盾，按 docs/equipped-fishing-skills.md
// 「已确认含义的节点不统一增加鱼口」的政策在此删除；官方文本本身含鱼侧
// 语义的节点（301/502/504/506/508/510 等稀有鱼/渔获量类）不受影响。
func (s *Server) ensureCastSkillEffects(ctx context.Context) (int, error) {
	if s == nil || s.DB == nil {
		return 0, nil
	}
	var inserted int
	for _, seed := range castSkillEffectSeeds {
		res, err := s.DB.SQL.ExecContext(ctx, `
			INSERT INTO skill_cast_effect_def(
				cat_id,node_id,effect_key,per_level_pct,gate_kind,gate_type,source,confidence
			) VALUES(?,?,?,?,?,?, 'client-loc-4.0.25034', 0.9)
			ON CONFLICT(cat_id,node_id) DO NOTHING`,
			seed.CatID, seed.NodeID, seed.EffectKey, seed.PerLevelPct, seed.GateKind, seed.GateType)
		if err != nil {
			return inserted, err
		}
		if n, err := res.RowsAffected(); err == nil {
			inserted += int(n)
		}
	}
	res, err := s.DB.SQL.ExecContext(ctx, `
		DELETE FROM skill_fish_effect_def
		WHERE (cat_id, node_id) IN (
			SELECT cat_id, node_id FROM skill_cast_effect_def
		)`)
	if err != nil {
		return inserted, err
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		s.log.Info("清理已确认抛投技能节点的自造鱼口效果行", "rows", n)
	}
	return inserted, nil
}
