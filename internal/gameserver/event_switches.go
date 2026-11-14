package gameserver

import (
	"sort"
	"strconv"
	"strings"

	"rf4server/internal/proto"
)

// 限时活动开关。活动节点（5009.Idx1 != 0）不进资格面板，默认不下发。
// 新年面板收 Idx1=5，周年面板收 Idx1=6；5012.f0 中出现对应阶段号会点亮
// 主菜单入口。后台写入已做互斥，同一时刻只会有一个开关开启。
// 挂哪些分支由 event_branches 配置决定，两个面板共用同一份 ID 列表。
//
// 出厂默认：新年活动开、周年活动关、12 个分支全挂（见 eventDefaultNewYear /
// defaultEventBranches）。两个开关的默认值互斥，缺配置时不会同时点亮两个面板。
const (
	eventAnniversaryKey     = "event_anniversary_enabled"
	eventNewYearKey         = "event_newyear_enabled"
	eventBranchesKey        = "event_branches"
	eventEndKey             = "event_anniversary_end_unix" // 两个面板共用同一结束时间
	eventDefaultNewYear     = 1                            // 出厂默认开启新年活动
	eventDefaultAnniversary = 0                            // 出厂默认关闭周年活动
	defaultEventEndUnix     = 1825632000                   // 2027-11-08 00:00 UTC
	defaultEventBranches    = "9,10,11,12,13,14,15,16,17,18,19,20"
	eventStageNewYear       = uint32(5)
	eventStageAnniversary   = uint32(6)
	eventBranch09           = uint64(9)
	eventBranch10           = uint64(10)
	eventBranch11           = uint64(11)
	eventBranch12           = uint64(12)
	eventBranch13           = uint64(13)
	eventBranch14           = uint64(14)
	eventBranchVIII         = uint64(15)
	eventBranchVIIIExtra    = uint64(16)
	eventBranchMMXXVI       = uint64(17)
	eventBranchSpecial2026  = uint64(18)
	eventBranchIX           = uint64(19)
	eventBranchIXExtra      = uint64(20)
)

// eventTaskCounts 是全部 12 个活动分支每阶段的任务数，由客户端
// qlfctn_t_<id> 文案键分布实测生成（合计 273 条）。14/18 是 10×3，
// 19/20 略有增减，其余都是 7×3。任何分支都能挂到任意 Idx1 的面板下。
var eventTaskCounts = map[uint64][]uint64{
	eventBranch09:          {3, 3, 3, 3, 3, 3, 3},          // MMXXIV 21
	eventBranch10:          {3, 3, 3, 3, 3, 3, 3},          // MMXXIV EXTRA 21
	eventBranch11:          {3, 3, 3, 3, 3, 3, 3},          // VII 21
	eventBranch12:          {3, 3, 3, 3, 3, 3, 3},          // VII 额外 21
	eventBranch13:          {3, 3, 3, 3, 3, 3, 3},          // MMXXV 21
	eventBranch14:          {3, 3, 3, 3, 3, 3, 3, 3, 3, 3}, // 2025 EXTRA 30
	eventBranchVIII:        {3, 3, 3, 3, 3, 3, 3},          // VIII 21
	eventBranchVIIIExtra:   {3, 3, 3, 3, 3, 3, 3},          // VIII 额外 21
	eventBranchMMXXVI:      {3, 3, 3, 3, 3, 3, 3},          // MMXXVI 21
	eventBranchSpecial2026: {3, 3, 3, 3, 3, 3, 3, 3, 3, 3}, // 2026 特别奖 30
	eventBranchIX:          {3, 3, 3, 3, 3, 4, 3},          // IX 22
	eventBranchIXExtra:     {3, 4, 3, 3, 3, 3, 4},          // 9周年特别挑战 23
}

func (s *Server) anniversaryEnabled() bool {
	return s != nil && s.tunableF(eventAnniversaryKey, eventDefaultAnniversary) == 1
}

func (s *Server) newYearEnabled() bool {
	return s != nil && s.tunableF(eventNewYearKey, eventDefaultNewYear) == 1
}

// seasonEndFiletime 把后台配置的「活动结束 Unix 秒」转成 FILETIME。
// 实机定案：5013.Idx4 就是面板倒计时的结束时间，此前填 0 时客户端回退到
// 内置的 2022-08-06，才会显示 -1555 天。缺省用固定锚点，保证倒计时逐日递减。
func seasonEndFiletime(endUnix float64) uint64 {
	if endUnix <= 0 {
		endUnix = defaultEventEndUnix
	}
	return uint64(proto.UnixToFiletime(int64(endUnix)))
}

// seasonEnd 读取后台配置的结束时间；无 Server 的调用方用 seasonEndFiletime(0)。
func (s *Server) seasonEnd() uint64 {
	if s == nil {
		return seasonEndFiletime(0)
	}
	return seasonEndFiletime(s.tunableF(eventEndKey, 0))
}

// buildSeason5012 把「阶段数组」与「结束时间」两个运行参数收在一处。
func (s *Server) buildSeason5012() *proto.Value {
	return build5012(s.seasonStages(), s.seasonEnd())
}

// seasonStages 返回 5012.f0 应下发的 5013 阶段。0 是资格节点用的哨兵时段，
// 恒发；哪个活动开关打开就追加哪个阶段号。
//
// 实机结论（2026-11-08）：发 0..7 连续阶段时，客户端会因数组中出现阶段 5
// 而把主菜单入口切成「新年任务」、周年面板消失。所以阶段数组只能放 0 与
// 实际开放的活动阶段，不能补齐其它阶段号。
func (s *Server) seasonStages() []uint32 {
	stages := []uint32{0}
	if s.newYearEnabled() {
		stages = append(stages, eventStageNewYear)
	}
	if s.anniversaryEnabled() {
		stages = append(stages, eventStageAnniversary)
	}
	return stages
}

// eventBranches 解析后台配置的分支 ID 列表（逗号分隔，9..20）。只保留任务数表
// 已收录的 ID，去重后按数值升序返回；空配置或全非法时返回 nil（不下发活动节点）。
func (s *Server) eventBranches() []uint64 {
	raw := s.tunableS(eventBranchesKey, defaultEventBranches)
	seen := make(map[uint64]bool, len(eventTaskCounts))
	out := make([]uint64, 0, len(eventTaskCounts))
	for _, part := range strings.Split(raw, ",") {
		id, err := strconv.ParseUint(strings.TrimSpace(part), 10, 32)
		if err != nil {
			continue
		}
		branch := uint64(id)
		if _, ok := eventTaskCounts[branch]; !ok || seen[branch] {
			continue
		}
		seen[branch] = true
		out = append(out, branch)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// eventStage 返回当前活动应挂的 Idx1；没有活动开启时返回 0，调用方据此跳过下发。
func (s *Server) eventStage() uint32 {
	if s.newYearEnabled() {
		return eventStageNewYear
	}
	if s.anniversaryEnabled() {
		return eventStageAnniversary
	}
	return 0
}

// eventNodes 返回当前活动面板的分支节点。Idx1 由开启的开关决定，挂哪些分支由
// event_branches 配置决定；客户端按 qlfctn_n_<id> / qlfctn_t_<id> 查文案。
func (s *Server) eventNodes() []*proto.Value {
	stage := s.eventStage()
	if stage == 0 {
		return nil
	}
	giftPlans := s.eventStageGiftPlans()
	branches := s.eventBranches()
	out := make([]*proto.Value, 0, len(branches))
	for _, branch := range branches {
		out = append(out, eventBranchNode(branch, eventTaskCounts[branch], stage, giftPlans))
	}
	return out
}

// eventBranchNode 构造一个活动分支节点（5009）。Idx0 是分支 ID，客户端据此查
// qlfctn_n_<id> 名称；Idx1 决定被哪个面板的 gppikafllmb 谓词收下（5 新年 / 6 周年）。
func eventBranchNode(branch uint64, counts []uint64, stage uint32, giftPlans []eventStageGiftPlan) *proto.Value {
	return proto.Obj("bibmoppiacg", 5009,
		proto.Field{Op: "BYTE", Num: ptrU64(branch)},
		proto.Field{Op: "BYTE", Num: ptrU64(uint64(stage))},
		proto.Field{Op: "BYTE", Num: ptrU64(0)},
		proto.Field{Op: "VAL", Val: eventStageList(branch, counts, giftPlans)},
		proto.Field{Op: "VAL", Val: nullVal()},
		proto.Field{Op: "VAL", Val: nullVal()},
	)
}

// eventStageList 按每阶段任务数数组构造 5008 阶段列表。
func eventStageList(branch uint64, counts []uint64, giftPlans []eventStageGiftPlan) *proto.Value {
	stages := make([]*proto.Value, 0, len(counts))
	for i, count := range counts {
		stage := uint64(i + 1)
		stages = append(stages, proto.Obj("goglkgnhpcl", 5008,
			proto.Field{Op: "U32", Num: ptrU64(stage)},
			proto.Field{Op: "VAL", Val: eventTaskList(branch, stage, count)},
			proto.Field{Op: "VAL", Val: eventStageGiftPreview(giftPlans, branch, stage)},
			proto.Field{Op: "VAL", Val: nullVal()},
		))
	}
	return proto.Args(proto.S("205"), stages...)
}

// eventTaskList 构造一个阶段的 5019 任务列表。规则编号仍按
// branch*1000+(stage-1)*100+task，客户端按 qlfctn_t_<id> 显示活动文案；
// qualificationTarget 对未收录编号回退 1，保证进度分母 > 0。
// 缺口任务（eventGapRuleIDs）当前无判定信号，跳过不下发，避免面板永久 0/N。
func eventTaskList(branch, stage, count uint64) *proto.Value {
	tasks := make([]*proto.Value, 0, count)
	for task := uint64(1); task <= count; task++ {
		if !eventTaskEmittable(branch, stage, task) {
			continue
		}
		tasks = append(tasks, proto.Obj("gjbbiokohda", 5019,
			proto.Field{Op: "BYTE", Num: ptrU64(0)},
			proto.Field{Op: "VAL", Val: proto.Args(proto.S("204"),
				qualificationRule(qualificationRuleID(branch, stage, task)))},
		))
	}
	return proto.Args(proto.S("216"), tasks...)
}

// eventTaskEmittable 报告某任务是否下发到面板：缺口任务不下发。
func eventTaskEmittable(branch, stage, task uint64) bool {
	return !eventGapRuleIDs[int64(qualificationRuleID(branch, stage, task))]
}

// eventEmittableCount 返回某阶段过滤缺口后实际下发的任务数（测试与节点构造共用）。
func eventEmittableCount(branch, stage, count uint64) uint64 {
	n := uint64(0)
	for task := uint64(1); task <= count; task++ {
		if eventTaskEmittable(branch, stage, task) {
			n++
		}
	}
	return n
}
