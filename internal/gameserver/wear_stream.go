package gameserver

// 搏鱼中受力磨损的逐帧下发。
//
// 官服搏鱼 4/22 帧的形状是 args("61") 裸部件子树（lure2 46 帧 + 底钓2 274 帧逐帧
// 验证：竿坯/手柄/导环/轴承/线杯/摩擦片/轮油/旋钮/主齿轮作为独立对象并列，客户端
// 按 GUID 原位更新零件数值），因此不会重建鱼实体。我们绝不能走全量 4/22（竿+线
// 全树重建会打断冲刺与体力阶段，见 hFishingLoad 注释），必须按这个形状只发部件子树，
// 且只发官服帧里实际出现的部件品类——轮树里还存有非部件辅助对象（如 f14 电机扩展位
// 之外的 pcfflcdpjgk 记录对象），混进推送会被 4/22 的 item 白名单契约整条拒发
// （2026-11-11 实测：实得 166，352 帧全数被守门人吞掉）。

import (
	"context"

	"rf4server/internal/proto"
)

// fightWearPartCategories 是官服搏鱼帧实际出现的部件品类白名单：
// 轮侧轴承/线杯/摩擦片/轮油/旋钮/主齿轮（+电轮电机），竿侧竿坯/手柄/导环。
var fightWearPartCategories = map[int64]bool{
	reelPartCatBearing:   true,
	reelPartCatSpool:     true,
	reelPartCatWasher:    true,
	reelPartCatOil:       true,
	reelPartCatHandle:    true,
	reelPartCatGear:      true,
	reelPartCatMotor:     true,
	reelPartCatRodBlank:  true,
	reelPartCatRodHandle: true,
	reelPartCatGuides:    true,
}

// fightWearComponentTrees 收集受力磨损物品树里的部件子树：轮树与竿树统一按
// fightWearPartCategories 过滤（按子件 numeric_id 解析定义取品类），非部件辅助
// 对象与无法解析定义的子件一律跳过。
func (s *Server) fightWearComponentTrees(ctx context.Context, uid int64, w fightReelWear) []*proto.Value {
	out := []*proto.Value{}
	if s == nil || s.DB == nil {
		return out
	}
	for _, guid := range []string{w.reelGUID, w.rodGUID} {
		if guid == "" {
			continue
		}
		row, err := s.DB.GetItemByGUID(ctx, guid)
		if err != nil || row == nil || row.UID != uid {
			continue
		}
		fields, ferr := FieldsFromJSON(row.FieldsJSON)
		if ferr != nil {
			continue
		}
		for i := range fields {
			child := fields[i].Val
			if child == nil || child.Kind != proto.KindObj {
				continue
			}
			def := s.reelPartDef(ctx, child)
			if def == nil || !fightWearPartCategories[def.Category] {
				continue
			}
			out = append(out, child)
		}
	}
	return out
}

// pushFightWearComponents 把受力磨损涉及的部件以官服裸部件树形状推给在线客户端；
// 前导走既有背包物品通道（Push4_22ItemGUIDs）。
func (s *Server) pushFightWearComponents(uid int64, w fightReelWear) {
	trees := s.fightWearComponentTrees(s.dbCtx(), uid, w)
	leaderGUID := w.leaderGUID
	if len(trees) == 0 && leaderGUID == "" {
		return
	}
	for _, sess := range s.sessionsForUID(uid) {
		if len(trees) > 0 {
			sess.Push4_22Trees(trees)
		}
		if leaderGUID != "" {
			sess.Push4_22ItemGUIDs([]string{leaderGUID})
		}
	}
}
