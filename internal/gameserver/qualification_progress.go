package gameserver

import (
	"context"
	"math"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// 资格进度下发：无独立推送命令。进度计数器（5004）内嵌在 5003 里，随两条通道下发：
//
//   - 28/2 请求-响应全量快照（hFog）；
//   - 14/4 渔获响应 801.f3（build5003），逐尾增量。
//
// 5003 三槽（ops {VAL,VAL,VAL}）：Idx0 赛季块 5012、Idx1 鱼记录列表 5005（容器
// "202" = 5005-4803）、Idx2 计数器列表 5004（容器 "201" = 5004-4803）。客户端把
// 计数器 Guid[] 里的每个 GUID 拿去 Idx1 鱼记录列表查同尾鱼渲染任务鱼表，
// 故两槽必须配套下发。

// qualificationCounterObject 构造一条任务计数器（ecmfhcmbjif / kkdcklpbnek 5004，
// ops {U16,VAL,U64}）：Idx0 规则编号、Idx1 已计数渔获 GUID 列表（进度分子=长度）、
// Idx2 完成 FILETIME UTC（>0 客户端直接判满，服务端写入）。
func qualificationCounterObject(ruleID int64, counted []store.QualificationFish, completedTS float64) *proto.Value {
	guids := make([]string, 0, len(counted))
	for _, f := range counted {
		guids = append(guids, f.GUID)
	}
	var ts uint64
	if completedTS > 0 {
		ts = unixToFiletime100ns(completedTS)
	}
	return wiregen.MustMakeObj("ecmfhcmbjif", map[int]*proto.Value{
		0: proto.U32(uint32(ruleID)),
		1: proto.GuidArr(guids),
		2: proto.U64(ts),
	})
}

// qualificationFishObject 构造一条渔获记录（mhnenmanmfn / nanehfeohjh 5005），
// 供任务鱼表渲染。体长 wire 侧为 U32 整数子单位，按厘米下发（显示换算待实机核对）。
//
// f5 与渔护 25.f11、溜鱼 27.f22、纪录卡 320.f13 同名字段（oajeeklppia），是客户端
// 取 FishFamily 模型族的 key，f6 才是名称文本——和 25 的 f11/f12 是同一对。
// 之前 f5 一直塞名称，客户端查不到同名族就只能退默认族，任务鱼表里又是那套老模型；
// 现在有变体族的鱼种写族名、名称挪到 f6，没有族名的鱼种保持原样（f5=名称）。
// 接收者允许为 nil：无会话的调用方（28/2 兜底、测试）只发赛季块，不推导模型族。
func (s *Server) qualificationFishObject(f store.QualificationFish) *proto.Value {
	modelKey := ""
	if s != nil {
		// 族名必须与渔护/纪录卡/溜鱼同源：都按**真鱼 guid**（catch_log.catch_guid）推导。
		// f.GUID 是资格去重键的 md5，跟这尾鱼的外观没有关系；拿它当种子会让同一尾鱼在
		// 任务鱼表里跟渔护里显示成两套模型。老存档没有 fish_guid 时留空，交给
		// fishVariantSeed 走「鱼种+克重+体长」的内容种子——那正是其余三条路径的兜底，
		// 四条路径才会一致。
		modelKey = s.fishModelVariantForFish(f.Species, f.WeightG, f.LengthM, f.Grade, f.FishGUID)
	}
	key, name := f.NameZh, (*string)(nil)
	if modelKey != "" {
		key = modelKey
		if f.NameZh != "" {
			zh := f.NameZh
			name = &zh
		}
	}
	return wiregen.MustMakeObj("mhnenmanmfn", map[int]*proto.Value{
		0: proto.Guid(f.GUID),
		1: proto.StrVal(f.Species),
		2: proto.U32(uint32(f.Grade)),
		3: proto.U32(uint32(f.WeightG)),
		4: proto.U32(uint32(math.Round(f.LengthM * 100))),
		5: proto.StrVal(key),
		6: proto.Str(name),
		7: proto.U32(uint32(f.RodType)),
		8: proto.U32(uint32(f.RigType)),
	})
}

// build5003 构造 5003 赛季+资格进度块（801.f3 与 28/2 共用）。
// 纯查询：读 player_qualifications 填充鱼记录与计数器，不做判定。
func (s *Server) build5003(ctx context.Context, uid int64) *proto.Value {
	var states map[int64]store.QualificationState
	if s.DB != nil {
		states, _ = s.DB.QualificationStates(ctx, uid)
	}
	return s.build5003FromStates(states, s.buildSeason5012())
}

// build5003FromStates 用已读取的进度装配 5003；states 为 nil 时只发赛季块与空槽。
// season 由 Server.buildSeason5012() 提供，无会话的调用方传默认赛季块。
func (s *Server) build5003FromStates(states map[int64]store.QualificationState, season *proto.Value) *proto.Value {
	fishArgs := proto.Args(proto.S("202"))
	counterArgs := proto.Args(proto.S("201"))
	seen := make(map[string]bool)
	for _, st := range states {
		if len(st.Counted) == 0 {
			continue
		}
		counterArgs.Items = append(counterArgs.Items,
			qualificationCounterObject(st.RuleID, st.Counted, st.CompletedTS))
		for _, f := range st.Counted {
			if seen[f.GUID] {
				continue
			}
			seen[f.GUID] = true
			fishArgs.Items = append(fishArgs.Items, s.qualificationFishObject(f))
		}
	}
	return wiregen.MustMakeObj("fopkeoppndn", map[int]*proto.Value{
		0: season,
		1: fishArgs,
		2: counterArgs,
	})
}
