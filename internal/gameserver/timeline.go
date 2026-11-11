package gameserver

// timeline.go 实现登录和进图时间线中的辅助命令。
// 这些命令大多只需要返回结构合法的最小响应；响应类型错误会导致客户端解析失败。

import (
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

func registerTimelineHandlers(d *Dispatch) { // fire-and-forget：客户端不等响应，绝不回包
	d.Register(1, 7, hNoReply)        // game/locale 区域上报
	d.Register(1, 15, hNoReply)       // game/enter_place_notify 进入地图单向通知
	d.Register(3, 10, hBoatUse)       // boat_use 上船/下船单向上报，据此维护在船状态
	d.Register(3, 11, hCampfireStart) // campfire_start：保存到期时间并返回有效篝火状态
	d.Register(1, 18, hBoolTrue)      // game/telemetry 设备指纹 → bool true

	// 空集/无数据语义
	d.Register(6, 2, hNull)           // comp/load 无竞赛 → null
	d.Register(24, 3, hSocialCurrent) // social/current：返回当前公共聊天频道
	d.Register(24, 12, hU32ArrEmpty)  // social/bl_get 黑名单 → 空 u32 数组
	d.Register(9, 5, hCraftTemplates)
	d.Register(3, 18, hPlayerWarnings)
	d.RegisterResponseContract(3, 18, ResponseContractRule{
		Kind: proto.KindArgs, ArgCode: "189", ArgItemTypeIDs: []uint32{3005},
	})

	// 环境/视觉（28 族，纯构造最小合法结构）
	d.Register(28, 1, hEnvironment)
	d.Register(28, 2, hFog)

	// 金币商店货架（23/1 storereal/list）
	d.Register(23, 1, hStoreRealList)
}

// hNoReply 处理但不回包（fire-and-forget）。
func hNoReply(*Ctx) ([]byte, bool) { return nil, true }

// hBoolTrue 回 bool true（0x0e）。
func hBoolTrue(*Ctx) ([]byte, bool) { return []byte{0x0e}, true }

// hNull 回 null（0x00）。
func hNull(*Ctx) ([]byte, bool) { return []byte{0x00}, true }

// hU32ArrEmpty 回空 u32 数组（05 0000）。
func hU32ArrEmpty(*Ctx) ([]byte, bool) { return []byte{0x05, 0x00, 0x00}, true }

// hCraftTemplates 处理 9/5，返回该玩家的配方模板列表（args(97) 的 171 数组）。
//
// 列表容器 code 97 来自 argcode_table.json 的 by_cmd["9/5"] 与 by_type_id["171"]。
// 没配 DB 或查询失败时退回空列表：客户端拿到空列表只是列表为空，
// 拿到 null 会在 foreach 上崩。
func hCraftTemplates(ctx *Ctx) ([]byte, bool) {
	body, err := proto.EncodeValue(craftTemplatesTree(ctx), ctx.Sess.Table())
	if err != nil {
		return emptyArgsBody(), true
	}
	return body, true
}

// craftTemplatesTree 读库构造 9/5 响应树；任何失败都返回空 args(97)。
func craftTemplatesTree(ctx *Ctx) *proto.Value {
	srv := ctx.Sess.Srv
	if srv == nil || srv.DB == nil {
		return proto.Args(proto.S("97"))
	}
	rows, err := srv.DB.ListCraftTemplates(ctx.Sess.pushCtx(), ctx.Sess.Userid)
	if err != nil {
		ctx.Log.Warn("9/5 模板列表读取失败", "err", err)
		return proto.Args(proto.S("97"))
	}
	items := make([]*proto.Value, 0, len(rows))
	for _, row := range rows {
		items = append(items, craftTemplate(
			row.GUID, row.Name, row.Kind,
			craftTemplateEntriesFromJSON(row.ToolsJSON),
			craftTemplateEntriesFromJSON(row.MaterialsJSON),
		))
	}
	return proto.Args(proto.S(craftTemplateListCode), items...)
}

// hPlayerWarnings 返回警告数组；客户端按数组解包，null 会触发空引用。
func hPlayerWarnings(ctx *Ctx) ([]byte, bool) {
	body, err := proto.EncodeValue(proto.Args(proto.S("189")), ctx.Sess.Table())
	return body, err == nil
}

// nullVal 共享的 null 值。
func nullVal() *proto.Value { return &proto.Value{Kind: proto.KindNull} }

// 资格面板规模：8 个钓法分支 × 7 阶段 × 3 任务 = 168 条任务。
const (
	qualificationBranches = 8
	qualificationStages   = 7
	qualificationTasks    = 3
)

// qualificationRuleID 返回某条任务的规则类型编号。
//
// 该编号同时是客户端本地化键 qlfctn_t_<编号> 的后缀，客户端的任务文案、条件说明
// 全部按它查表得出，服务端只发数字。168 个编号与客户端本地化键逐条吻合：
// 分支 1..8、阶段 1..7、任务 1..3 → 1001..1603 / 2001..2603 / … / 8001..8603。
func qualificationRuleID(branch, stage, task uint64) uint64 {
	return branch*1000 + (stage-1)*100 + task
}

// hEnvironment 处理 28/1，返回 5011 资格内容对象。
//
// 5011.f1 是资格分支数组（5009）：每个分支含 7 阶段（5008），每阶段含 3 条任务
// （5019），任务内嵌 1 条规则（5007）。客户端按规则编号查 qlfctn_t_<id> 文案。
//
// 约束：
//  1. 永久资格节点 5009.Idx1 必须为 0；活动节点 Idx1=6 只进周年面板
//     （三个面板的 gppikafllmb 谓词分别比较 0/5/6）。
//  2. 数组容器名 = DTO 类型 ID - 4803：5008→"205"、5009→"206"、5019→"216"、5007→"204"；
//     名字错了客户端建数组会抛异常。
//  3. 每条任务至少 1 条规则，且规则 Idx3（进度分母）> 0；否则进度 0/0 会让面板异常。
func hEnvironment(ctx *Ctx) ([]byte, bool) {
	nodes := make([]*proto.Value, 0, qualificationBranches+len(eventTaskCounts))
	for branch := uint64(1); branch <= qualificationBranches; branch++ {
		nodes = append(nodes, qualificationBranchNode(branch))
	}
	// 活动开关默认关闭；开启后追加对应 Idx1 的活动节点。28/1 只在全新登录
	// 请求，所以开关切换后客户端必须完整退出重登才会看到节点变化。
	if ctx != nil && ctx.Sess != nil && ctx.Sess.Srv != nil {
		nodes = append(nodes, ctx.Sess.Srv.eventNodes()...)
	}
	obj := proto.Obj("acjaheimjge", 5011,
		proto.Field{Op: "BYTE", Num: ptrU64(0)},
		proto.Field{Op: "VAL", Val: proto.Args(proto.S("206"), nodes...)},
	)
	body, err := proto.EncodeValue(obj, wiregen.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// qualificationBranchNode 构造一个永久资格分支节点（5009）。
// Idx0 分支 ID（客户端据此查 qlfctn_n_<id> 名称与勋章）；Idx1 恒为 0
// （资格面板只收 0）。周年/活动节点不在本树下发，必须与资格分支严格分开。
func qualificationBranchNode(branch uint64) *proto.Value {
	return proto.Obj("bibmoppiacg", 5009,
		proto.Field{Op: "BYTE", Num: ptrU64(branch)},                // Idx0 分支 ID
		proto.Field{Op: "BYTE", Num: ptrU64(0)},                     // Idx1 常驻（资格面板只收 0）
		proto.Field{Op: "BYTE", Num: ptrU64(0)},                     // Idx2 解锁模式
		proto.Field{Op: "VAL", Val: qualificationStageList(branch)}, // Idx3 coljdmbcknh[]
		proto.Field{Op: "VAL", Val: nullVal()},                      // Idx4 string[]
		proto.Field{Op: "VAL", Val: nullVal()},                      // Idx5 miobbnhdpif
	)
}

// qualificationStageList 构造一个分支的 7 个阶段（coljdmbcknh 5008，数组名 "205"）。
func qualificationStageList(branch uint64) *proto.Value {
	stages := make([]*proto.Value, 0, qualificationStages)
	for stage := uint64(1); stage <= qualificationStages; stage++ {
		stages = append(stages, proto.Obj("goglkgnhpcl", 5008,
			proto.Field{Op: "U32", Num: ptrU64(stage)},                        // Idx0 阶段号 1..7
			proto.Field{Op: "VAL", Val: qualificationTaskList(branch, stage)}, // Idx1 iolhlkoilph[]
			proto.Field{Op: "VAL", Val: nullVal()},                            // Idx2 string[]
			proto.Field{Op: "VAL", Val: nullVal()},                            // Idx3 阶段元数据 miobbnhdpif
		))
	}
	return proto.Args(proto.S("205"), stages...)
}

// qualificationTaskList 构造一个阶段的 3 条任务（iolhlkoilph 5019，数组名 "216"）。
func qualificationTaskList(branch, stage uint64) *proto.Value {
	tasks := make([]*proto.Value, 0, qualificationTasks)
	for task := uint64(1); task <= qualificationTasks; task++ {
		tasks = append(tasks, proto.Obj("gjbbiokohda", 5019,
			proto.Field{Op: "BYTE", Num: ptrU64(0)}, // Idx0 枚举 fidehpgfmmo（仅成员 0）
			proto.Field{Op: "VAL", Val: proto.Args(proto.S("204"),
				qualificationRule(qualificationRuleID(branch, stage, task)))},
		))
	}
	return proto.Args(proto.S("216"), tasks...)
}

// qualificationRule 构造一条任务规则（bcijcdlkjbk / hgnkdommpea 5007）。
// Idx0 为规则类型（= 本地化键 qlfctn_t_<id>），Idx3 为进度分母（静态目标值）。
// Idx4 钓组大类、Idx5 竿型、Idx6 竿硬度档、Idx27 顺流位用于客户端「鱼竿/条件」文案，
// 与服务端 qualificationRodTypesFor 同源；其余字段按 ClassOps 零值（数组 → null）。
func qualificationRule(ruleID uint64) *proto.Value {
	fields := map[int]*proto.Value{
		0: proto.U32(uint32(ruleID)),
		3: proto.U32(qualificationTarget(ruleID)),
	}
	if rigClass := qualificationRigClassFor(int64(ruleID)); rigClass > 0 {
		fields[4] = proto.U32(uint32(rigClass))
	}
	if types := qualificationRodTypesFor(int64(ruleID)); len(types) > 0 {
		fields[5] = proto.U32Arr(u32Values(types))
	}
	if powers := qualificationRodPowerFor(int64(ruleID)); len(powers) > 0 {
		fields[6] = proto.U32Arr(u32Values(powers))
	}
	if driftRuleIDs[int64(ruleID)] {
		fields[27] = proto.U32(64) // StreamMoving 顺水漂流
	}
	return wiregen.MustMakeObj("hgnkdommpea", fields)
}

func u32Values(v []int64) []uint32 {
	out := make([]uint32, len(v))
	for i, x := range v {
		out[i] = uint32(x)
	}
	return out
}

// hFog 处理 28/2，返回 5003（fopkeoppndn / mkooaaknoee）：Idx0 赛季块、
// Idx1 资格渔获记录列表、Idx2 资格进度计数器列表。这是资格进度的全量快照通道
// （登录时请求）；逐尾增量走 14/4 的 801.f3（同用 build5003）。赛季时段与玩家
// 档案 307 共用 seasonBounds()。无会话时（如测试）只发赛季块与空槽。
func hFog(ctx *Ctx) ([]byte, bool) {
	var obj *proto.Value
	if ctx != nil && ctx.Sess != nil && ctx.Sess.Srv != nil {
		obj = ctx.Sess.Srv.build5003(ctx.Sess.pushCtx(), ctx.Sess.Userid)
	} else {
		obj = (*Server)(nil).build5003FromStates(nil, build5012([]uint32{0}, seasonEndFiletime(0)))
	}
	body, err := proto.EncodeValue(obj, wiregen.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// hStoreRealList 23/1 金币商店：本项目不提供充值或付费功能，恒回空货架。
func hStoreRealList(ctx *Ctx) ([]byte, bool) {
	body, err := proto.EncodeValue(proto.Args(proto.S("147")), ctx.Sess.Table())
	if err != nil {
		return emptyArgsBody(), true
	}
	return body, true
}

// 3/10 boat_use 的动作字节。协议契约里 3/10 没有 DTO（fallback_kinds 为 none），
// 解码器会把请求体尾字节当结束标记吞掉，所以按尾字节判上下船。实机样本：
// 0x0e 两次都紧跟在 3/7 验票通过之后 9~10 毫秒，即上船；0x0f 出现在玩家靠岸
// 按 E 下船的瞬间，另有 6 条历史样本时机吻合。
const (
	boatUseBoard     byte = 0x0e
	boatUseDisembark byte = 0x0f
)

// boatUseIsBoard 从 3/10 请求体取上下船结论。取值认不出时 ok=false，
// 调用方只记日志不改状态，避免把“没证据”写成“已下船”。
func boatUseIsBoard(body []byte) (board bool, ok bool) {
	if len(body) == 0 {
		return false, false
	}
	switch body[len(body)-1] {
	case boatUseBoard:
		return true, true
	case boatUseDisembark:
		return false, true
	}
	return false, false
}

// hBoatUse 处理 3/10 boat_use：单向上报，不回包。上船时 3/7 已经写过租期，
// 这里补的是“下船瞬间”的实时证据，否则只能等租期到期才算下船。
func hBoatUse(ctx *Ctx) ([]byte, bool) {
	board, ok := boatUseIsBoard(ctx.Body)
	if !ok {
		if ctx.Log != nil {
			ctx.Log.Info("3/10 boat_use", "len", len(ctx.Body), "hex", traceHexHead(ctx.Body, 160))
		}
		return nil, true
	}
	srv := ctx.Sess.Srv
	for _, st := range srv.fishingStatesFor(ctx.Sess.Userid) {
		st.mu.Lock()
		st.markBoatClientEventLocked(time.Now().Unix(), board)
		st.mu.Unlock()
	}
	if ctx.Log != nil {
		if board {
			ctx.Log.Info("3/10 boat_use 已上船")
		} else {
			ctx.Log.Info("3/10 boat_use 已下船")
		}
	}
	return nil, true
}
