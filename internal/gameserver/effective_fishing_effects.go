package gameserver

import (
	"context"
	"math"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

const effectiveSkillCap = 110.0

// effectiveFishingEffects 分开保存永久能力和当前竿的临时加成。
type effectiveFishingEffects struct {
	Mastery     map[int64]float64
	GearMastery map[int64]float64
	Learned     map[int64]int64
	GearNodes   map[int64]int64
}

func (e effectiveFishingEffects) mastery(catID int64) float64 {
	return clampFloat(e.Mastery[catID]+e.GearMastery[catID], 0, effectiveSkillCap)
}

func (e effectiveFishingEffects) nodeLevel(nd skillNodeDef) int64 {
	learned := max64(0, e.Learned[nd.NodeID])
	if learned > nd.MaxLevel {
		learned = nd.MaxLevel
	}
	gear := e.GearNodes[nd.NodeID]
	for _, id := range store.SharedSkillNodeIDs(nd.CatID, nd.NodeID) {
		gear = max(gear, e.GearNodes[id])
	}
	total := learned + gear
	if total > nd.MaxLevel {
		total = nd.MaxLevel
	}
	return total
}

func (s *Server) currentSkillGear(ctx context.Context, uid int64, gears ...string) string {
	if len(gears) > 0 {
		return gears[0]
	}
	s.fishMu.Lock()
	st := s.fishStates[uid]
	s.fishMu.Unlock()
	if st != nil {
		st.mu.Lock()
		gear := st.GearGuid
		st.mu.Unlock()
		return gear
	}
	if s.DB != nil {
		if saved, err := s.DB.LoadFishingState(ctx, uid); err == nil && saved != nil {
			return saved.GearGUID
		}
	}
	return ""
}

func (s *Server) effectiveFishingSkills(ctx context.Context, uid int64, gears ...string) (effectiveFishingEffects, error) {
	e := effectiveFishingEffects{Mastery: map[int64]float64{}, GearMastery: map[int64]float64{}, Learned: map[int64]int64{}, GearNodes: map[int64]int64{}}
	if s == nil || s.DB == nil {
		return e, nil
	}
	points, err := s.DB.ListSkills(ctx, uid)
	if err != nil {
		return e, err
	}
	for cat, value := range points {
		if !math.IsNaN(value) && !math.IsInf(value, 0) {
			e.Mastery[cat] = clampFloat(value, 0, profCap)
		}
	}
	e.Learned, err = s.DB.ListSkillNodes(ctx, uid)
	if err != nil {
		return e, err
	}
	gear := s.currentSkillGear(ctx, uid, gears...)
	if gear == "" || gear == zeroGUID {
		return e, nil
	}
	rod, err := s.DB.GetItemByGUID(ctx, gear)
	if err != nil {
		return e, err
	}
	if rod == nil || rod.UID != uid || rod.Durability <= 0 {
		return e, nil
	}
	def, err := s.itemDefForRow(ctx, rod)
	if err != nil {
		return e, err
	}
	if def == nil || def.Category != gearCatRod {
		return e, nil
	}
	entries, _ := defAttrs(def)["dmcgapifbdm"].([]any)
	for _, raw := range entries {
		mod, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch mod["fopbhlmkcpe"] {
		case float64(3):
			cat := skillModifierInt(mod["pihmbmniond"])
			value, valid := decodeU32F32(mod["oalocpjcokm"])
			if cat > 0 && valid && value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0) {
				e.GearMastery[cat] = math.Min(effectiveSkillCap, e.GearMastery[cat]+value)
			}
		case float64(4):
			node, level := skillModifierInt(mod["iiijkdapekg"]), skillModifierInt(mod["oalocpjcokm"])
			if node > 0 && level > 0 {
				// 装备赠级独立于玩家投入的技能点。
				e.GearNodes[node] += level
			}
		}
	}
	return e, nil
}

func skillModifierInt(raw any) int64 {
	v, ok := raw.(float64)
	if !ok || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > math.MaxInt32 || v != math.Trunc(v) {
		return 0
	}
	return int64(v)
}

func (s *Server) skillCategory312(catID int64, effects effectiveFishingEffects) *proto.Value {
	catNodes := proto.Args(proto.S("122"))
	for _, nd := range s.skillDefsOfCat(catID) {
		level := effects.nodeLevel(nd)
		catNodes.Items = append(catNodes.Items,
			wiregen.MustMakeObj("bmdafhbpfci", map[int]*proto.Value{
				0: proto.U32(uint32(nd.NodeID)),
				1: proto.U32(f32bitsOf(nd.UnlockPct)),
				2: proto.U32(uint32(nd.MaxLevel)),
				3: proto.U32(uint32(level)),
				4: proto.U32(f32bitsOf(nd.EffectPct)),
				5: proto.U32(uint32(nd.GroupID)),
				6: proto.U32(boolU32(nodeUnlocked(nd, effects.Mastery[nd.CatID], effects.GearMastery[nd.CatID]))),
			}))
	}
	return wiregen.MustMakeObj("kmepameiibi", map[int]*proto.Value{
		0: proto.U32(uint32(catID)),
		1: proto.F32(float32(effects.Mastery[catID])),
		2: proto.F32(float32(effects.GearMastery[catID])),
		3: catNodes,
	})
}
