package gameserver

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"

	"rf4server/internal/store"
)

// 水域装备限制（服务端镜像）。
//
// 客户端 ebjhdhighdj 静态注册表按 level key 记录「本水域禁用什么装备」，
// 由地点详情 UI 和库存格在客户端自行拦截；但装配/抛竿入口原本
// 不设防，直接发消息可以绕过。本文件把同一张规则表内嵌到服务端，
// 在 17/7 取竿、14/1 抛竿准备、14/2 抛竿三个入口做第二道防线，
// 保证 DB 不会出现官服不可能出现的钓组状态。
//
// 官服语义（实测对照）：限制渔轮允许装配（17/2 不拦，客户端装配弹窗
// 也不校验），只在抛竿时禁止。

//go:embed assets/places_restrictions.json
var placesRestrictionsJSON []byte

// placeGearRule 一条物品限制。匹配条件 = item_def.category +
// attrs_json 子类字段 placeAttrSubtype；mode="ban" 全禁，
// mode="tier_max" 只放行档位 ≤ max_tier（档位字段缺失或 0 放行）。
type placeGearRule struct {
	Category int64  `json:"category"`
	Subtype  int64  `json:"subtype"`
	Mode     string `json:"mode"`
	TierAttr string `json:"tier_attr,omitempty"`
	MaxTier  int64  `json:"max_tier,omitempty"`
	Note     string `json:"note,omitempty"`
}

type placeRestriction struct {
	Caption string          `json:"caption"`
	Rules   []placeGearRule `json:"rules"`
}

// placeAttrSubtype item_def.attrs_json 里的竿/轮子类字段，
// 与客户端判定函数读取的描述器字段同名。
const placeAttrSubtype = "fopbhlmkcpe"

var (
	placesRestrictionsOnce sync.Once
	placesRestrictions     map[string]placeRestriction
)

func loadPlacesRestrictions() map[string]placeRestriction {
	placesRestrictionsOnce.Do(func() {
		var doc struct {
			Source string                      `json:"source"`
			Levels map[string]placeRestriction `json:"levels"`
		}
		if err := json.Unmarshal(placesRestrictionsJSON, &doc); err != nil {
			panic(fmt.Sprintf("places_restrictions.json 解析失败: %v", err))
		}
		placesRestrictions = doc.Levels
	})
	return placesRestrictions
}

// restrictionsForLevel 返回某地图的装备规则；未登记的地图返回 nil。
func restrictionsForLevel(levelKey string) []placeGearRule {
	if levelKey == "" {
		return nil
	}
	if r, ok := loadPlacesRestrictions()[levelKey]; ok {
		return r.Rules
	}
	return nil
}

// placeGearRuleViolated 判断物品定义是否违反单条规则。
func placeGearRuleViolated(def *store.ItemDef, rule placeGearRule) bool {
	if def == nil || def.Category != rule.Category {
		return false
	}
	sub, ok := defAttrU64(def, placeAttrSubtype)
	if !ok || int64(sub) != rule.Subtype {
		return false
	}
	switch rule.Mode {
	case "ban":
		return true
	case "tier_max":
		tier, ok := defAttrU64(def, rule.TierAttr)
		if !ok || tier == 0 {
			return false
		}
		return int64(tier) > rule.MaxTier
	}
	return false
}

// placeGearViolationReason 返回物品违反规则的说明；未违规返回空串。
// 多条规则命中时取第一条（规则表保持与客户端判定相同的顺序）。
func placeGearViolationReason(def *store.ItemDef, rules []placeGearRule) string {
	for _, rule := range rules {
		if placeGearRuleViolated(def, rule) {
			if rule.Note != "" {
				return rule.Note
			}
			return fmt.Sprintf("类别%d子类%d受本水域限制", rule.Category, rule.Subtype)
		}
	}
	return ""
}

// currentPlaceRules 读取玩家当前地图对应的装备规则；无位置记录或
// 地图未登记返回 nil。
func (s *Server) currentPlaceRules(ctx context.Context, uid int64) []placeGearRule {
	if s == nil || s.DB == nil {
		return nil
	}
	loc, err := s.DB.GetPlayerLocation(ctx, uid)
	if err != nil || loc == nil {
		return nil
	}
	return restrictionsForLevel(loc.LevelKey)
}

// placeRodViolation 检查一根竿（含其整体定义）是否违反当前地图规则。
// 竿不存在/不属于该玩家按未违规处理（归属校验由各入口原有逻辑负责）。
func (s *Server) placeRodViolation(ctx context.Context, uid int64, rodGUID string) string {
	rules := s.currentPlaceRules(ctx, uid)
	if len(rules) == 0 || rodGUID == "" {
		return ""
	}
	row, err := s.DB.GetItemByGUID(ctx, rodGUID)
	if err != nil || row == nil || row.UID != uid {
		return ""
	}
	def, err := s.itemDefForRow(ctx, row)
	if err != nil || def == nil {
		return ""
	}
	return placeGearViolationReason(def, rules)
}

// placeRigViolation 检查整条钓组（竿 + 已装渔轮）是否违反当前地图规则。
// 抛竿入口用：竿和轮任一违规即拒绝。
func (s *Server) placeRigViolation(ctx context.Context, uid int64, rodGUID string) string {
	rules := s.currentPlaceRules(ctx, uid)
	if len(rules) == 0 || rodGUID == "" {
		return ""
	}
	if reason := s.placeRodViolation(ctx, uid, rodGUID); reason != "" {
		return reason
	}
	ids := s.rigGearIDs(ctx, uid, rodGUID)
	if ids.ReelGUID == "" {
		return ""
	}
	reel, err := s.DB.GetItemByGUID(ctx, ids.ReelGUID)
	if err != nil || reel == nil || reel.UID != uid {
		return ""
	}
	def, err := s.itemDefForRow(ctx, reel)
	if err != nil || def == nil {
		return ""
	}
	return placeGearViolationReason(def, rules)
}
