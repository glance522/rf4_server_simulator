package gameserver

import (
	"fmt"

	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

// 成就解锁推送。
//
// 客户端 4.0.25034 实测（成就推送探针，2026-11-07）：
//   - 2/8 + args("114") + 305 对象（claimed=1）会弹出成就提示；
//   - 同样的负载走 3/3 无任何反应（3/3 是查询命令，不在 push_args
//     登记表里，客户端不会把它当通知处理）。
//
// 2/8 在 command_contract.json 的 push_args 中登记为通配符 "*"，
// 形状守卫放行；仓库里没有成就解锁瞬间的官服抓包，这是当前唯一的
// 客户端实证依据。若日后拿到真实报文，应替换命令号与负载。
//
// 保留 achievement_push_test 管理指令作为后续协议探针的触发入口。

var achievementProbeVariants = []string{"3_3", "2_8", "2_9", "chat"}

func achievementStatePushObject(id int64) *proto.Value {
	return wiregen.MustMakeObj("efnkohdcnld", map[int]*proto.Value{
		0: proto.U32(uint32(id)),
		1: proto.U32(1), // unlocked
		2: proto.F32(1), // progress
		3: proto.U32(1), // claimed：客户端实测只有这一位会点亮图标
		4: proto.U64(uint64(proto.WindowsFiletimeNow())),
	})
}

// pushAchievementUnlocked 推送一条成就完成通知（正式链路，已实证会弹窗）。
func (s *Session) pushAchievementUnlocked(achID int64) bool {
	if s == nil || s.Closed() {
		return false
	}
	s.Push(2, 8, proto.Args(proto.S("114"), achievementStatePushObject(achID)))
	return true
}

// queueAchievementUnlockPushes 把新解锁成就的弹窗排到应答之后。
// 只由 evaluateAchievements 在事件判定后调用。
func (s *Server) queueAchievementUnlockPushes(ctx *Ctx, ids []int64) {
	if len(ids) == 0 || ctx == nil || ctx.Sess == nil {
		return
	}
	sess := ctx.Sess
	log := ctx.Log
	ctx.AfterResponse(func() {
		for _, id := range ids {
			if !sess.pushAchievementUnlocked(id) {
				return
			}
		}
		if log != nil {
			log.Info("成就解锁推送", "uid", sess.Userid, "count", len(ids), "ids", ids)
		}
	})
}

// pushAchievementProbe 向当前会话发一种候选推送，返回是否已尝试发送。
func (s *Session) pushAchievementProbe(variant string, achID int64, notifyType int64) bool {
	if s == nil || s.Closed() {
		return false
	}
	switch variant {
	case "3_3":
		// 与 3/3 查询响应完全同形状，但客户端实测不弹窗；留作对照组。
		s.Push(3, 3, proto.Args(proto.S("114"), achievementStatePushObject(achID)))
	case "2_8":
		// 已实证可用，复用正式链路。
		return s.pushAchievementUnlocked(achID)
	case "2_9":
		// 通用通知推送 obj:306；notifyType 值域与成就的对应关系未知。
		if notifyType < 0 || notifyType > 255 {
			notifyType = 100
		}
		s.Push2_9(byte(notifyType), 0)
	case "chat":
		// 已验证能显示的对照通道，用于确认推送链路确实到达客户端。
		s.pushSystemChat(fmt.Sprintf("成就推送探针 id=%d", achID))
	default:
		return false
	}
	return true
}
