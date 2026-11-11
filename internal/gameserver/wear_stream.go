package gameserver

// 搏鱼中受力磨损的逐帧下发。
//
// 官服搏鱼 4/22 帧的形状是 args("61") 裸部件子树（lure2 46 帧 + 底钓2 274 帧逐帧
// 验证：轮件/竿件/旋钮/摩擦片/前导线作为独立对象并列，不含竿/轮容器树），客户端按
// GUID 原位更新零件数值，因此不会重建鱼实体。我们绝不能走全量 4/22（竿+线全树重建
// 会打断冲刺与体力阶段，见 hFishingLoad 注释），必须按这个形状只发零件子树。

import (
	"context"

	"rf4server/internal/proto"
)

// fightWearComponentTrees 收集受力磨损物品树里的零件子树：渔轮树的全部子对象
// （六件/摩擦片/旋钮，电轮含电机）与竿树内的竿侧零件（竿坯/手柄/导环，排除 f5 钓组
// 等非零件对象）。缺件/缺定义的静默跳过。
func (s *Server) fightWearComponentTrees(ctx context.Context, uid int64, w fightReelWear) []*proto.Value {
	out := []*proto.Value{}
	if s == nil || s.DB == nil {
		return out
	}
	if row, err := s.DB.GetItemByGUID(ctx, w.reelGUID); err == nil && row != nil && row.UID == uid {
		if fields, ferr := FieldsFromJSON(row.FieldsJSON); ferr == nil {
			for i := range fields {
				if child := fields[i].Val; child != nil && child.Kind == proto.KindObj {
					out = append(out, child)
				}
			}
		}
	}
	if row, err := s.DB.GetItemByGUID(ctx, w.rodGUID); err == nil && row != nil && row.UID == uid {
		if fields, ferr := FieldsFromJSON(row.FieldsJSON); ferr == nil {
			for i := range fields {
				child := fields[i].Val
				if child == nil || child.Kind != proto.KindObj {
					continue
				}
				def := s.reelPartDef(ctx, child)
				if def == nil {
					continue
				}
				switch def.Category {
				case reelPartCatRodBlank, reelPartCatRodHandle, reelPartCatGuides:
					out = append(out, child)
				}
			}
		}
	}
	return out
}

// pushFightWearComponents 把受力磨损涉及的零件以官服裸部件树形状推给在线客户端；
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
