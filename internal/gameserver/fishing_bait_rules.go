package gameserver

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
)

//go:embed fishing_bait_rules.txt
var fishBaitRuleText string

type fishBaitRule struct {
	allow, deny []string
	bottomOnly  []string
	condition   string
	// 攻略前置条件里可核对的部分：海图区号列表（chartAny 表示只要求
	// “在海图上，区域不限”），以及顺水漂流、jig 阶梯、饵在水中运动三个动作要求。
	chartZones []int
	chartAny   bool
	needDrift  bool
	needJig    bool
	needMotion bool
	// needBoat 表示攻略要求“使用有效船票并已上船”。
	needBoat bool
	// jigOrMotion 用于“出现 jig 阶梯，或采用路亚钓法且已上船、饵在水中运动”
	// 这类二选一前置条件，不能拆成同时要求。船是二选一里的一条路，不单独封鱼。
	jigOrMotion bool
}

var fishBaitRules = parseFishBaitRules(fishBaitRuleText)

const maxBaitPreference = 1.5 * 1.15

func ruleName(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\ufeff', '(', ')', '（', '）':
			return -1
		case '鯡':
			return '鲱'
		case '鮭':
			return '鲑'
		case '鰻':
			return '鳗'
		case '鳌':
			return '螯'
		}
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, s)
}

// BaitRuleInfo 一条"鱼吃饵"规则的可读形态（供管理后台展示）。
type BaitRuleInfo struct {
	Allow      []string `json:"allow"`
	Deny       []string `json:"deny"`
	BottomOnly []string `json:"bottom_only"`
	Condition  string   `json:"condition"`
}

// ExportedBaitRules 按中文鱼名查规则表，供管理后台只读展示。
// 返回 ok=false 表示该鱼在规则表里没有条目（严格模式下只能靠饵效证据放行）。
func ExportedBaitRules(fishZh string) (BaitRuleInfo, bool) {
	r, ok := fishBaitRules[ruleName(fishZh)]
	if !ok {
		return BaitRuleInfo{}, false
	}
	return BaitRuleInfo{Allow: r.allow, Deny: r.deny, BottomOnly: r.bottomOnly, Condition: r.condition}, true
}

// ExportedBaitRuleCount 规则表条目数（前端展示用）。
func ExportedBaitRuleCount() int { return len(fishBaitRules) }

// deriveConditionFlags 从规则文本解析出判鱼要核对的硬条件：
// 海图区号、顺水漂流、jig 阶梯、饵在水中运动、已上船。
// 内置规则与后台覆盖共用同一套解析，避免两条路径判定不一致。
func (r *fishBaitRule) deriveConditionFlags() {
	r.chartZones, r.chartAny = fishChartCondition(r.condition)
	r.needDrift = strings.Contains(r.condition, "顺水漂流")
	needJig := strings.Contains(r.condition, "jig阶梯")
	needMotion := strings.Contains(r.condition, "饵在水中运动")
	r.needJig, r.needMotion, r.needBoat, r.jigOrMotion = false, false, false, false
	if needJig && needMotion && strings.Contains(r.condition, "或") {
		r.jigOrMotion = true
		return
	}
	r.needJig, r.needMotion = needJig, needMotion
	r.needBoat = strings.Contains(r.condition, "已上船")
}

func parseFishBaitRules(raw string) map[string]fishBaitRule {
	out := map[string]fishBaitRule{}
	for _, line := range strings.Split(raw, "\n") {
		name, tail, ok := strings.Cut(line, "  吃 ")
		if !ok {
			continue
		}
		bait, condition, ok := strings.Cut(tail, "  前置条件 ")
		if !ok {
			continue
		}
		bait = strings.NewReplacer("（", "(", "）", ")").Replace(bait)
		allowed, extra, _ := strings.Cut(bait, "(")
		r := fishBaitRule{allow: strings.Split(strings.TrimSpace(allowed), "/"), condition: strings.TrimSpace(condition)}
		r.deriveConditionFlags()
		if _, denied, ok := strings.Cut(extra, "不吃"); ok {
			denied = strings.FieldsFunc(denied, func(c rune) bool { return c == ')' || c == '；' || c == ';' })[0]
			r.deny = strings.Split(denied, "/")
		}
		if _, only, ok := strings.Cut(extra, "底钓只吃"); ok {
			only = strings.TrimRight(only, ") \r\t")
			r.bottomOnly = strings.Split(only, "/")
		}
		out[ruleName(name)] = r
	}
	return out
}

type ruleBait struct {
	id, name                     string
	category, lureType, baitType int64
}

func (b ruleBait) matches(term string) bool {
	t := ruleName(term)
	n := ruleName(b.name)
	aliases := map[string][]string{
		"muckworm":    {"粪便蠕虫", "红蚯蚓"},
		"dough_sweet": {"甜的面团"},
		"caddisfly":   {"毛翅目幼虫"},
		"baitfrog":    {"青蛙"},
		"fish_flesh":  {"鱼肉块"},
	}
	for _, alias := range aliases[b.id] {
		if t == ruleName(alias) {
			return true
		}
	}
	if t == "各类饵" {
		return true
	}
	if t == "拟饵" {
		return b.category == 17
	}
	if t == "活饵类" {
		return b.category == 27 && b.baitType == 4
	}
	if t == "饵鱼类" || t == "饵鱼" || t == "各类饵鱼" {
		return b.category == 27 && (b.baitType == 4 || b.baitType == 15)
	}
	if b.category == 27 {
		switch t {
		case "蠕虫", "蠕虫类":
			return b.baitType == 6
		case "昆虫幼虫":
			return b.baitType == 2
		case "昆虫":
			return b.baitType == 7
		case "鱼肉类":
			return b.baitType == 14 || b.id == "fish_flesh"
		}
	}
	if b.category == 17 {
		switch t {
		case "亮片", "亮片类", "亮片系列饵":
			return b.lureType == 1 || b.lureType == 2
		case "旋转饵", "旋转饵类", "旋转亮片路亚":
			return b.lureType == 1
		case "软饵类", "拟真软饵":
			return b.lureType == 3
		}
	}
	for _, suffix := range []string{"系列饵", "系列", "类"} {
		if strings.HasSuffix(t, suffix) {
			stem := strings.TrimSuffix(t, suffix)
			if strings.HasPrefix(n, stem) {
				// 型号 1 不能匹配型号 1000；颜色编号通常由连字符分隔。
				tail := strings.TrimPrefix(n, stem)
				if len(stem) > 0 && len(tail) > 0 && stem[len(stem)-1] >= '0' && stem[len(stem)-1] <= '9' && tail[0] >= '0' && tail[0] <= '9' {
					return false
				}
				return true
			}
			return false
		}
	}
	if n == t {
		return true
	}
	if strings.ContainsAny(t, "abcdefghijklmnopqrstuvwxyz") && strings.HasPrefix(n, t) {
		tail := strings.TrimPrefix(n, t)
		return len(tail) > 0 && (tail[0] == '-' || tail[0] == '#' || (t[len(t)-1] < '0' || t[len(t)-1] > '9') && tail[0] >= '0' && tail[0] <= '9')
	}
	return false
}

// filterFishBaits 对喜好饵和其他饵分别加权，明确禁饵仍然排除。
func (s *Server) filterFishBaits(loc, bait string, keys []string, weights []float64) ([]string, []float64) {
	return s.filterFishBaitSet(loc, []string{bait}, keys, weights, time.Now())
}

func (s *Server) loadRuleBait(ctx context.Context, id string) (ruleBait, bool) {
	b := ruleBait{id: id}
	var raw string
	if err := s.DB.SQL.QueryRowContext(ctx, `SELECT COALESCE(c.name_zh,c.name,''),d.category,COALESCE(d.attrs_json,'{}')
   FROM item_def d LEFT JOIN item_catalog c ON c.item_id=d.item_id WHERE d.item_id=?`, id).Scan(&b.name, &b.category, &raw); err != nil {
		return ruleBait{}, false
	}
	if b.category != 17 && b.category != 27 {
		return ruleBait{}, false
	}
	var attrs struct {
		LureType int64 `json:"mbemakijdeh"`
		BaitType int64 `json:"fopbhlmkcpe"`
	}
	if json.Unmarshal([]byte(raw), &attrs) != nil {
		return ruleBait{}, false
	}
	b.lureType, b.baitType = attrs.LureType, attrs.BaitType
	return b, true
}

func (s *Server) loadFishNames(ctx context.Context) (map[string]string, bool) {
	names := map[string]string{}
	// 先取覆盖，再开游标：连接池可能只有一条连接，游标未关时再查询会互相等待。
	overrides := s.baitOverrides(ctx)
	rows, err := s.DB.SQL.QueryContext(ctx, `SELECT fish_key,fish_zh FROM loc_wiki_fish
		UNION SELECT fish_key,name_zh FROM loc_fish`)
	if err != nil {
		return names, false
	}
	defer rows.Close()
	for rows.Next() {
		var key, name string
		if rows.Scan(&key, &name) == nil {
			if _, known := fishBaitRules[ruleName(name)]; known {
				names[key] = name
				continue
			}
			// 后台新增规则覆盖的鱼种同样参与严格模式判鱼。
			if _, ok := overrides[ruleName(name)]; ok {
				names[key] = name
			}
		}
	}
	return names, true
}

func (s *Server) loadLocalBaitEvidence(ctx context.Context, loc, baitID, baitName string) map[string]bool {
	evidence := map[string]bool{}
	rows, err := s.DB.SQL.QueryContext(ctx, `SELECT fish_key FROM loc_bait_fish WHERE loc=? AND bait IN (?,?) AND catches>0`, loc, baitID, baitName)
	if err != nil {
		return evidence
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if rows.Scan(&key) == nil {
			evidence[key] = true
		}
	}
	return evidence
}

func (s *Server) filterFishBaitSet(loc string, ids []string, keys []string, weights []float64, now time.Time, contexts ...fishRollContext) ([]string, []float64) {
	if s.DB == nil {
		return nil, nil
	}
	// fish_bait_strict_recipe=1 时只认食谱（fish_bait_pref），忽略实战样本表。
	strictRecipe := s.tunableF("fish_bait_strict_recipe", 0) > 0
	c := rollContext(contexts)
	ctx := s.dbCtx()
	var baits []ruleBait
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		b, ok := s.cachedBaitDefinition(ctx, c, id)
		if !ok {
			return nil, nil
		}
		baits = append(baits, b)
	}
	if len(baits) == 0 {
		return nil, nil
	}
	c.lureBait = false
	for _, b := range baits {
		if b.category == 17 {
			c.lureBait = true
			break
		}
	}
	names, namesOK := s.cachedFishNames(ctx, c)
	if !namesOK {
		return nil, nil
	}
	evidence := make([]map[string]int64, len(baits))
	localEvidence := make([]map[string]bool, len(baits))
	for i, b := range baits {
		evidence[i] = s.cachedBaitTiers(ctx, c, b.id)
		localEvidence[i] = s.cachedLocalBaitEvidence(ctx, c, loc, b.id, b.name)
	}
	outKeys := make([]string, 0, len(keys))
	outWeights := make([]float64, 0, len(keys))
	period := (now.Unix() - worldDayEpochUnix) / (4 * realSecPerGameHour)
	for i, key := range keys {
		if weights[i] <= 0 {
			continue
		}
		r, rot := s.resolveBaitRule(ctx, names[key])
		// 轮换：按时间桶取当前组；strict 只认当前组，soft 组外降权但仍可咬。
		activeGroup := activeBaitGroup(rot, now)
		strictRotation := activeGroup != nil && rot.mode == "strict"
		if !ruleConditionAllows(r, c) {
			continue
		}
		denied := false
		best := 0.0
		preferred := 0
		for j, b := range baits {
			if !rotationKeepsBait(activeGroup, strictRotation, b) {
				continue // strict 轮换：组外的饵本轮不咬
			}
			if bottomRigTypes[c.rigType] && len(r.bottomOnly) > 0 && !b.matchesAny(r.bottomOnly) {
				denied = true
				break
			}
			for _, term := range r.deny {
				if b.matches(term) {
					denied = true
					break
				}
			}
			if denied {
				break
			}
			allowed := false
			for _, term := range r.allow {
				if b.matches(term) {
					allowed = true
					break
				}
			}
			// 当前轮换组的饵视为本轮主推，即使不在静态允许列表里。
			if activeGroup != nil && b.matchesAny(activeGroup) {
				allowed = true
			}
			score := 0.0
			variation := fishVariation(fmt.Sprintf("%s/%s/%s/%d", loc, key, b.id, period))
			if allowed || (!strictRecipe && (evidence[j][key] > 0 || localEvidence[j][key])) {
				preferred++
				score = 1.0 + 0.5*variation
			} else if b.legalAlternative(r) {
				score = maxBaitPreference * 0.05
			}
			if activeGroup != nil {
				score *= rotationScoreMultiplier(activeGroup, b)
			}
			best = math.Max(best, score)
		}
		if denied || best <= 0 {
			continue
		}
		if preferred > 1 {
			best *= 1.15
		}
		outKeys = append(outKeys, key)
		outWeights = append(outWeights, weights[i]*best)
	}
	return outKeys, outWeights
}

// fishChartCondition 解析攻略里的海图前置条件：“海图120号区域”给出区号列表，
// “海图，区域不限”只要求当前在海图上。区号数字与客户端水域 systemID 编号同源。
func fishChartCondition(condition string) ([]int, bool) {
	if !strings.Contains(condition, "海图") {
		return nil, false
	}
	var zones []int
	for i := 0; i < len(condition); {
		if condition[i] < '0' || condition[i] > '9' {
			i++
			continue
		}
		j := i
		for j < len(condition) && condition[j] >= '0' && condition[j] <= '9' {
			j++
		}
		zone, err := strconv.Atoi(condition[i:j])
		if err == nil && zone > 0 {
			zones = append(zones, zone)
		}
		i = j
	}
	return zones, len(zones) == 0
}

// ruleConditionAllows 只用客户端给得出的证据判断攻略前置条件：海图条件要有能
// 定位的抛竿落点，拟饵动作条件要有钓组动作上报。天然饵不套用“使用拟饵时”的
// 动作和船条件；拿不到证据时保持中性，避免把没有对应上报的钓法一律判成不合格。
func ruleConditionAllows(r fishBaitRule, c fishRollContext) bool {
	if r.chartAny || len(r.chartZones) > 0 {
		if c.runtime == nil {
			return true
		}
		if !c.runtime.chartKnown {
			return false
		}
		if len(r.chartZones) > 0 && c.runtime.zoneKnown {
			inside := false
			for _, zone := range r.chartZones {
				if zone == c.runtime.zone {
					inside = true
					break
				}
			}
			if !inside {
				return false
			}
		}
	}
	if !c.lureBait {
		return true
	}
	if r.needBoat && c.boatKnown && !c.onBoat {
		return false
	}
	if !lureMotionEvidence(c.lureMask) {
		return true
	}
	if r.jigOrMotion {
		if c.lureMask&lureMotionJigBit != 0 {
			return true
		}
		if c.lureMask&lureMotionInWaterMask == 0 {
			return false
		}
		if c.boatKnown && !c.onBoat {
			return false
		}
		return true
	}
	if r.needDrift && c.lureMask&lureMotionDriftBit == 0 {
		return false
	}
	if r.needJig && c.lureMask&lureMotionJigBit == 0 {
		return false
	}
	if r.needMotion && c.lureMask&lureMotionInWaterMask == 0 {
		return false
	}
	return true
}

func (b ruleBait) matchesAny(terms []string) bool {
	for _, term := range terms {
		if b.matches(term) {
			return true
		}
	}
	return false
}

// 非偏好只在已识别食性族内保留杂口，不把所有天然饵视为通用食物。
func (b ruleBait) legalAlternative(r fishBaitRule) bool {
	if b.category != 27 {
		return false
	}
	var diet string
	switch b.baitType {
	case 2, 6, 7:
		diet = "虫"
	case 8:
		diet = "煮饵"
	case 9:
		diet = "颗粒"
	case 10:
		diet = "玉米"
	case 11, 13:
		diet = "植物"
	case 4, 15:
		diet = "饵鱼"
	case 14:
		diet = "鱼肉"
	}
	if b.id == "fish_flesh" {
		diet = "鱼肉"
	}
	terms := strings.Join(r.allow, "/")
	switch diet {
	case "虫":
		for _, term := range []string{"蠕虫", "蚯蚓", "红虫", "苍蝇", "蛆虫", "毛翅", "树皮甲虫", "昆虫", "地龙", "土蚕", "水蛭", "蜉蝣", "襀翅", "牛虻", "草蜢", "蟋蟀", "蜣螂", "金龟", "龙虱", "蝼蛄"} {
			if strings.Contains(terms, term) {
				return true
			}
		}
	case "植物":
		for _, term := range []string{"面包", "面团", "粥", "玉米", "豌豆", "坚果", "奶酪", "水果", "蜂蜜", "奶油", "椰子", "中立", "自然风味"} {
			if strings.Contains(terms, term) {
				return true
			}
		}
	case "煮饵":
		return strings.Contains(terms, "煮饵") || strings.Contains(terms, "煮成饵") || strings.Contains(strings.ToLower(terms), "boilie") || strings.Contains(strings.ToLower(terms), "popup") || strings.Contains(strings.ToLower(terms), "pop up")
	case "颗粒":
		return strings.Contains(terms, "颗粒") || strings.Contains(strings.ToLower(terms), "pellet")
	case "玉米":
		return strings.Contains(terms, "玉米")
	case "饵鱼":
		return strings.Contains(terms, "饵鱼") || strings.Contains(terms, "活饵")
	case "鱼肉":
		return strings.Contains(terms, "鱼肉") || strings.Contains(terms, "鱼片")
	}
	return false
}

// 技能只提高已有的正兴趣，不能放行禁饵或不适用的拟饵。
func fishingBaitInterest(interest float64, bonus fishingSkillBonus) float64 {
	if interest <= 0 {
		return 0
	}
	multiplier := 1.0
	if bonus.BiteSpeed > 0 && !math.IsNaN(bonus.BiteSpeed) && !math.IsInf(bonus.BiteSpeed, 0) {
		multiplier = clampFloat(1/bonus.BiteSpeed, 1, 2)
	}
	limit := 1.0
	if interest <= .1 {
		limit = .1
	}
	return math.Min(limit, interest*multiplier)
}
