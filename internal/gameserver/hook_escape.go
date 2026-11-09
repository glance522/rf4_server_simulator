package gameserver

import (
	"strconv"
	"strings"
)

// 脱钩规则（后台可配）。
//
// 规则只在 14/11 中鱼确认的瞬间判一次，命中就让这尾鱼脱钩离开（不计入鱼护、
// 不算跑鱼统计），不影响其它鱼。默认全部关闭，也就是「永不脱钩」。
const (
	hookEscapeEnabledKey = "hook_escape_enabled"
	hookEscapeAllowKey   = "hook_escape_species_allow"
	hookEscapeDenyKey    = "hook_escape_species_deny"
	hookEscapeOverKgKey  = "hook_escape_over_kg"
	hookEscapeUnderKgKey = "hook_escape_under_kg"
	hookEscapeStarOnly   = "hook_escape_star_only"
	hookEscapeQualOnly   = "hook_escape_qualified_only"
)

// hookEscapeDecision 返回 true 表示这尾鱼应当脱钩离开。
func (s *Server) hookEscapeDecision(f *RolledFish) (bool, string) {
	if s == nil || f == nil {
		return false, ""
	}
	if s.tunableF(hookEscapeEnabledKey, 0) <= 0 {
		return false, ""
	}

	// 档位门槛：只要星鱼 / 只要达标鱼。不满足就脱钩。
	grade := f.Grade
	if grade <= 0 {
		grade = 1
	}
	if s.tunableF(hookEscapeStarOnly, 0) > 0 && grade < 3 {
		return true, "不是星鱼"
	}
	if s.tunableF(hookEscapeQualOnly, 0) > 0 && grade < 2 {
		return true, "未达标"
	}

	// 重量门槛。单位 kg，0 表示不启用。
	kg := float64(f.WeightG) / 1000.0
	if over := s.tunableF(hookEscapeOverKgKey, 0); over > 0 && kg > over {
		return true, "超过重量上限"
	}
	if under := s.tunableF(hookEscapeUnderKgKey, 0); under > 0 && kg < under {
		return true, "低于重量下限"
	}

	// 鱼种白/黑名单，逗号分隔。白名单非空时只留名单内的鱼。
	if allow := strings.TrimSpace(s.tunableS(hookEscapeAllowKey, "")); allow != "" {
		if !speciesListMatch(allow, f.Species) {
			return true, "不在只要名单"
		}
	}
	if deny := strings.TrimSpace(s.tunableS(hookEscapeDenyKey, "")); deny != "" {
		if speciesListMatch(deny, f.Species) {
			return true, "命中不要名单"
		}
	}
	return false, ""
}

// speciesListMatch 判断 species 是否在逗号分隔的名单里（忽略空白与大小写）。
func speciesListMatch(list, species string) bool {
	want := strings.ToLower(strings.TrimSpace(species))
	if want == "" {
		return false
	}
	for _, part := range strings.Split(list, ",") {
		if strings.ToLower(strings.TrimSpace(part)) == want {
			return true
		}
	}
	return false
}

// parseKgOption 供面板回显用：把 tunable 文本转成数字，失败给默认值。
func parseKgOption(raw string, def float64) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil {
		return v
	}
	return def
}
