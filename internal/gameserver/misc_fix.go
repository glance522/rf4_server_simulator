package gameserver

// 补充基础注册之外的一组协议命令：物品状态位与格位、玩家档案、公聊和容器枚举。
//
// 这些处理器通过 init 和 RegisterExtra 注册。扩展注册先执行，内建注册随后覆盖
// 相同命令，因此这里只负责补齐缺失的处理。

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

func init() {
	RegisterExtra(func(d *Dispatch) {
		d.Register(4, 9, hItemSetFlag)
		d.Register(4, 10, hItemSetSlot)
		d.Register(3, 20, hChangeName)
		d.Register(3, 21, hChangeRegion)
		d.Register(3, 23, hVerifyEmail)
		d.Register(24, 10, hSocialPublic)
		d.Register(24, 1, hSocialMove)
		d.Register(24, 2, hSocialCurrent)
		d.Register(24, 3, hSocialCurrent)
	})
}

// ---------------------------------------------------------------------------
// 4/9 items/suflag
// ---------------------------------------------------------------------------

// hItemSetFlag 处理 4/9 items/suflag，对一批物品置位或清位状态标记。
//
// 请求中的 GUID 和整数都从整棵树收集。至少两个整数时，
// flag=ints[0]、op=ints[1]；只有一个整数时执行置位。op 非 0 使用 cur|flag，
// op 为 0 使用 cur&^flag。未知 GUID 或没有状态字段的物品会被跳过。
// 响应为容器快照 800，与 4/6、4/7 使用相同的 rigSnapshot800 构造。
func hItemSetFlag(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	guids := deepGUIDs(ctx.Tree)
	ints := treeIntsDeep(ctx.Tree)
	var flag, op uint64
	switch {
	case len(ints) >= 2:
		flag, op = ints[0], ints[1]
	case len(ints) == 1:
		flag, op = ints[0], 1
	}
	cctx := ctx.Sess.pushCtx()
	for _, g := range guids {
		row, err := srv.DB.GetItemByGUID(cctx, g)
		if err != nil || row == nil || row.UID != ctx.Sess.Userid {
			continue
		}
		fields, ferr := FieldsFromJSON(row.FieldsJSON)
		if ferr != nil {
			continue
		}
		idx := fieldIdxByCS(row.WireClass, csStateEnum)
		if idx < 0 || idx >= len(fields) {
			continue // 该类没有状态位字段
		}
		cur := u64Of(fields[idx].Num) & 0xFFFFFFFF
		var nw uint64
		if op != 0 {
			nw = cur | flag
		} else {
			nw = cur &^ flag
		}
		nw &= 0xFFFFFFFF
		fields[idx].Num = ptrU64(nw)
		if fj, jerr := FieldsToJSON(fields); jerr == nil {
			_ = srv.DB.UpdateItemFields(cctx, row.GUID, fj, row.Qty, row.Durability, row.Workmanship, row.RigGUID)
		}
	}
	ctx.Log.Info("4/9 标记", "count", len(guids), "flag", fmt.Sprintf("0x%x", flag), "op", op)
	return srv.rigSnapshot800(ctx), true
}

// ---------------------------------------------------------------------------
// 4/10 items/set_slot
// ---------------------------------------------------------------------------

// hItemSetSlot 处理 4/10 items/set_slot，将物品绑定到指定容器格位。
// 请求参数顺序为 [容器号, 物品 GUID, 槽位]。
//
// 同一槽位的原占用物品会被解绑回背包未指派格。目标物品同时更新
// item.container、item.slot 和 container_slot 绑定。该命令不返回响应。
func hItemSetSlot(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	ints := treeIntsDeep(ctx.Tree)
	guids := deepGUIDs(ctx.Tree)
	container, slot := int64(0), int64(0)
	if len(ints) > 0 {
		container = int64(ints[0])
	}
	if len(ints) > 1 {
		slot = int64(ints[1])
	}
	if len(guids) == 0 {
		return nil, true // GUID 为空时直接忽略
	}
	guid := guids[0]
	cctx := ctx.Sess.pushCtx()
	row, err := srv.DB.GetItemByGUID(cctx, guid)
	if err != nil || row == nil || row.UID != ctx.Sess.Userid {
		ctx.Log.Info("4/10 物品不存在，忽略", "guid", guidShort(guid))
		return nil, true
	}
	cname := containerEnumName(uint64(container))
	// 格位绑定、原占用者解绑和物品位置必须同事务。container_slot.guid
	// 有 UNIQUE 约束，因此先删除目标物品的旧绑定再插入新绑定。
	tx, err := srv.DB.SQL.BeginTx(cctx, nil)
	if err != nil {
		ctx.Log.Warn("4/10 开启事务失败", "err", err)
		return nil, true
	}
	defer func() { _ = tx.Rollback() }()
	var prev string
	if err := tx.QueryRowContext(cctx,
		`SELECT COALESCE(guid,'') FROM container_slot WHERE uid=? AND container=? AND slot=?`,
		ctx.Sess.Userid, container, slot).Scan(&prev); err == nil && prev != "" && prev != guid {
		if _, err := tx.ExecContext(cctx,
			`UPDATE item SET container='backpack', slot=0 WHERE uid=? AND guid=?`,
			ctx.Sess.Userid, prev); err != nil {
			ctx.Log.Warn("4/10 解绑旧占用者失败", "err", err)
			return nil, true
		}
	}
	if _, err := tx.ExecContext(cctx,
		`DELETE FROM container_slot WHERE uid=? AND guid=?`, ctx.Sess.Userid, guid); err != nil {
		ctx.Log.Warn("4/10 清理物品旧格位失败", "err", err)
		return nil, true
	}
	if _, err := tx.ExecContext(cctx, `
		INSERT INTO container_slot(uid, container, slot, guid) VALUES(?,?,?,?)
		ON CONFLICT (uid, container, slot) DO UPDATE SET guid=?`,
		ctx.Sess.Userid, container, slot, guid, guid); err != nil {
		ctx.Log.Warn("4/10 绑定落库失败", "err", err)
		return nil, true
	}
	if _, err := tx.ExecContext(cctx,
		`UPDATE item SET container=?, slot=? WHERE uid=? AND guid=?`, cname, slot, ctx.Sess.Userid, guid); err != nil {
		ctx.Log.Warn("4/10 更新物品位置失败", "err", err)
		return nil, true
	}
	if err := tx.Commit(); err != nil {
		ctx.Log.Warn("4/10 提交事务失败", "err", err)
		return nil, true
	}
	ctx.Log.Info("4/10 绑定", "guid", guidShort(guid), "container", container, "slot", slot)
	return nil, true // fire-and-forget，不返回响应
}

func guidShort(g string) string {
	if len(g) > 8 {
		return g[:8]
	}
	return g
}

// ---------------------------------------------------------------------------
// 3/20 change_name / 3/21 change_region / 3/23 邮箱验证码
// ---------------------------------------------------------------------------

// hChangeName 处理 3/20 改名：读取首个非空字符串，写入 profile，
// 然后返回数据库中的完整档案 307。空名称交给兜底链。
func hChangeName(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	name := argFirstStr(ctx.Tree)
	if name == "" {
		return nil, false // 交给兜底链
	}
	cctx := ctx.Sess.pushCtx()
	if _, err := srv.DB.SQL.ExecContext(cctx,
		`UPDATE profile SET name=?, updated_ts=? WHERE uid=?`,
		name, float64(time.Now().Unix()), ctx.Sess.Userid); err != nil {
		ctx.Log.Warn("3/20 改名落库失败", "err", err)
		return nil, false
	}
	ctx.Log.Info("3/20 改名", "name", name)
	return replyProfile307(ctx)
}

// hChangeRegion 处理 3/21 换区：读取首个非空字符串或整数作为区域，
// 以字符串形式写入 profile，然后返回完整档案 307。无参数时仅返回当前档案。
func hChangeRegion(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	if region, has := argFirstStrOrInt(ctx.Tree); has {
		if _, err := srv.DB.SQL.ExecContext(ctx.Sess.pushCtx(),
			`UPDATE profile SET region=?, updated_ts=? WHERE uid=?`,
			region, float64(time.Now().Unix()), ctx.Sess.Userid); err != nil {
			ctx.Log.Warn("3/21 换区落库失败", "err", err)
		} else {
			ctx.Log.Info("3/21 换区", "region", region)
		}
	}
	return replyProfile307(ctx)
}

// hVerifyEmail 处理 3/23 邮箱验证。请求为 [token, code]。
//
// 当前没有验证码校验依据，因此接受任意提交。处理器把 3/22 保存在
// email_pending 中的邮箱写入 profile，清除挂起状态，设置 email_verified，
// 并返回数据库中的完整档案 307。
func hVerifyEmail(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	strs := argStringsTop(ctx.Tree)
	token, code := "", ""
	if len(strs) > 0 {
		token = strs[0]
	}
	if len(strs) > 1 {
		code = strs[1]
	}
	// 邮箱来自 3/22 写入的 email_pending，支持 map 和裸字符串两种形态。
	// 没有挂起邮箱时仍返回当前档案，保持重复请求幂等。
	email := ""
	if v, ok := ctx.Sess.GetState("email_pending"); ok && v != nil {
		switch t := v.(type) {
		case map[string]any:
			if s, isStr := t["email"].(string); isStr {
				email = s
			}
		case string:
			email = t
		}
	}
	cctx := ctx.Sess.pushCtx()
	if email != "" {
		if _, err := srv.DB.SQL.ExecContext(cctx,
			`UPDATE profile SET email=?, updated_ts=? WHERE uid=?`,
			email, float64(time.Now().Unix()), ctx.Sess.Userid); err != nil {
			ctx.Log.Warn("3/23 邮箱落库失败", "err", err)
		}
	}
	ctx.Sess.SetState("email_pending", nil) // 清除挂起邮箱
	ctx.Sess.SetState("email_verified", true)
	ctx.Log.Info("3/23 邮箱验证 -> 回档案", "token", token, "hasCode", code != "")
	return replyProfile307(ctx)
}

// replyProfile307 构造并返回数据库中的完整档案 307，供 3/20、3/21、3/23 共用。
func replyProfile307(ctx *Ctx) ([]byte, bool) {
	tree, err := ctx.Sess.Srv.BuildProfile307(ctx.Sess.pushCtx(), ctx.Sess.Userid)
	if err != nil {
		ctx.Log.Warn("307 构造失败", "err", err)
		return nil, false
	}
	body, err := proto.EncodeValue(tree, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// ---------------------------------------------------------------------------
// 24/10 social/public 公聊回显
// ---------------------------------------------------------------------------

// chatChannel 表示聊天频道。
type chatChannel struct {
	Name    string
	Region  string
	Users   int64
	Private bool
}

// sessChannel 读取会话当前频道；没有记录时返回默认频道 nav1_03[CN]。
// 状态值支持 map 形态，便于后续切换频道功能复用。
func sessChannel(sess *Session) chatChannel {
	def := chatChannel{Name: "nav1_03", Region: "CN", Users: 1}
	if v, ok := sess.GetState("channel"); ok {
		if m, isMap := v.(map[string]any); isMap {
			ch := def
			if s, isStr := m["name"].(string); isStr && s != "" {
				ch.Name = s
			}
			if s, isStr := m["region"].(string); isStr && s != "" {
				ch.Region = s
			}
			if n, isNum := m["users"].(int64); isNum && n > 0 {
				ch.Users = n
			}
			if b, isBool := m["private"].(bool); isBool {
				ch.Private = b
			}
			return ch
		}
	}
	return def
}

// buildChatEchoPush 构造 24/27 公聊回显推送，合同形状为 [obj:401, str, u32]：
//
//	args[0] = 401 lfbdbaffeai{f0 消息类型(0=玩家发言 CHAT_KIND_SAY), f1 文本,
//	          f2 发送者 uid, f3 FILETIME, f4 KV 参数(null)}
//	args[1] = locationId（频道显示名形态 name[region]，与 402 f0 同源）
//	args[2] = 频道在线人数
func buildChatEchoPush(loc, msg string, uid uint64, users uint32) *proto.Value {
	msgObj := wiregen.MustMakeObj("lfbdbaffeai", map[int]*proto.Value{
		0: proto.U32(0), // CHAT_KIND_SAY
		1: proto.StrVal(msg),
		2: proto.U32(uint32(uid)),
		3: proto.U64(uint64(proto.WindowsFiletimeNow())),
		4: proto.Null(),
	})
	return proto.Args524(msgObj, proto.StrVal(loc), proto.U32(users))
}

// pushSystemChat 经聊天推送通道发送一条系统提示。
// 默认走已验证的公聊回显（24/27，kind=0）；管理面板可调 tunables 探测
// 客户端的其他聊天通道与消息类型（约 30s 热生效）：
//
//	fish_tv_channel  27..31（24/27..31 是客户端接受的聊天推送族）
//	fish_tv_kind     401.f0 消息类型（0=玩家发言，其余待探测，可能有颜色）
func (s *Session) pushSystemChat(text string) {
	if s == nil || s.closed.Load() {
		return
	}
	channel := int(s.Srv.tunableF("fish_tv_channel", 27))
	kind := int(s.Srv.tunableF("fish_tv_kind", 0))
	s.pushChatProbe(channel, kind, text, nil)
}

// pushChatProbe 经指定聊天推送通道/消息类型发送文本。
// 28..31 的载荷形状官服未抓样（合同通配），按 [消息, 频道名] / [消息]
// 试探；无效组合客户端直接忽略。kv 非空时作为 401.f4 模板参数（kind=3
// 的钓获播报模板：键1=鱼种key 键2=重量 键6=榜单类型）。
func (s *Session) pushChatProbe(channel, kind int, text string, kv *proto.Value) {
	if s == nil || s.closed.Load() {
		return
	}
	f2 := uint32(s.Userid)
	// 探测专用外层覆盖：str 槽默认频道名、u32 槽默认人数。
	outerStr := ""
	outerU32 := uint32(0)
	haveOuter := false
	switch kind {
	case -26: // 探测专用：重量裸值放 f2
		kind = 26
		f2 = 7777
	case -27: // 探测专用：重量按 f32 位模式放 f2
		kind = 26
		f2 = proto.F32Bits(7777)
	case -28: // 探测专用：等级按 f32 位模式放 f2（k00"达到X级别"）
		kind = 0
		f2 = proto.F32Bits(34)
	case -29: // 外层=鱼名(中文)+重量f32位
		kind, outerStr, outerU32, haveOuter = 26, "丁鱵", proto.F32Bits(7777), true
	case -30: // 外层=鱼名(key)+重量f32位
		kind, outerStr, outerU32, haveOuter = 26, "g_tench", proto.F32Bits(7777), true
	case -31: // k03 普通钓获：外层=鱼名(中文)+重量f32位
		kind, outerStr, outerU32, haveOuter = 3, "丁鱵", proto.F32Bits(7777), true
	case -32: // 外层=频道名+重量f32位（str 保持合法频道名）
		kind, outerU32, haveOuter = 26, proto.F32Bits(7777), true
	case -33: // 外层=频道名+重量裸值
		kind, outerU32, haveOuter = 26, 7777, true
	case -34: // f1=重量数字文本，k26 稀有鱼模板
		kind = 26
	case -35: // f1=重量数字文本，k25 超级稀有鱼模板
		kind = 25
	case -36: // f1=重量数字文本，k03 普通钓获模板
		kind = 3
	}
	ch := sessChannel(s)
	users := ch.Users
	if users < 1 {
		users = 1
	}
	f4 := proto.Null()
	if kv != nil {
		f4 = kv
	}
	msgObj := wiregen.MustMakeObj("lfbdbaffeai", map[int]*proto.Value{
		0: proto.U32(uint32(kind)),
		1: proto.StrVal(text),
		2: proto.U32(f2),
		3: proto.U64(uint64(proto.WindowsFiletimeNow())),
		4: f4,
	})
	switch channel {
	case 28, 29:
		// 形状未抓样（合同为通配）：按 [消息, 频道名] 试探。
		s.Push(24, uint8(channel), proto.Args524(msgObj, proto.StrVal(ch.Name)))
	case 30, 31:
		s.Push(24, uint8(channel), proto.Args524(msgObj))
	default: // 27 及一切未知值回退到已验证的公聊回显形状
		str2 := proto.StrVal(ch.Name)
		u3 := proto.U32(uint32(users))
		if haveOuter {
			if outerStr != "" {
				str2 = proto.StrVal(outerStr)
			}
			u3 = proto.U32(outerU32)
		}
		s.Push(24, 27, proto.Args524(msgObj, str2, u3))
	}
}

// pushLevelUpChat 官方升级播报：k00 模板（"player 达到X级别"），
// f2 填等级的 f32 位模式（探测验证：等级 34 → "达到34级别"）。
func (s *Session) pushLevelUpChat(level int64) {
	if s == nil || s.closed.Load() {
		return
	}
	msgObj := wiregen.MustMakeObj("lfbdbaffeai", map[int]*proto.Value{
		0: proto.U32(0),
		1: proto.StrVal(""),
		2: proto.U32(proto.F32Bits(float32(level))),
		3: proto.U64(uint64(proto.WindowsFiletimeNow())),
		4: proto.Null(),
	})
	ch := sessChannel(s)
	users := ch.Users
	if users < 1 {
		users = 1
	}
	s.Push(24, 27, proto.Args524(msgObj, proto.StrVal(ch.Name), proto.U32(uint32(users))))
}

// pushChatRaw 绕过推送形状守卫的探测通道：参数可能在外层 args 里
// （24/27 合同只登记了 [401, 频道名, 人数]，官方钓获播报可能带第 4 项）。
// variant: 101=24/27+[401,loc,users,KV×3]  102=24/28+[401,KV×3]
//
//	103=24/30+[401,KV×3]            104=24/27+[401,loc,users,DICT]
//	105=24/29+[401,KV×3]            106=24/31+[401,KV×3]
func (s *Session) pushChatRaw(variant, kind int, text string) {
	if s == nil || s.closed.Load() {
		return
	}
	ch := sessChannel(s)
	users := ch.Users
	if users < 1 {
		users = 1
	}
	k1, k2, k6 := "1", "2", "6"
	v1, v2, v6 := "g_tench", "7777", "0"
	kvs := []*proto.Value{
		proto.KV(&k1, &v1), proto.KV(&k2, &v2), proto.KV(&k6, &v6),
	}
	msgObj := wiregen.MustMakeObj("lfbdbaffeai", map[int]*proto.Value{
		0: proto.U32(uint32(kind)),
		1: proto.StrVal(text),
		2: proto.U32(uint32(s.Userid)),
		3: proto.U64(uint64(proto.WindowsFiletimeNow())),
		4: proto.Null(),
	})
	kvArr := &proto.Value{Kind: proto.KindArray, Items: kvs}
	kvDict := proto.DictOf("",
		proto.Pair{K: &k1, V: proto.StrVal(v1)},
		proto.Pair{K: &k2, V: proto.StrVal(v2)},
		proto.Pair{K: &k6, V: proto.StrVal(v6)},
	)
	var body *proto.Value
	switch variant {
	case 101:
		body = proto.Args524(msgObj, proto.StrVal(ch.Name), proto.U32(uint32(users)), kvArr)
	case 104:
		body = proto.Args524(msgObj, proto.StrVal(ch.Name), proto.U32(uint32(users)), kvDict)
	case 102, 105, 103, 106:
		body = proto.Args524(msgObj, kvArr)
	default:
		return
	}
	bodyBytes, err := proto.EncodeValue(body, s.Srv.Table())
	if err != nil {
		return
	}
	sub := uint8(27)
	switch variant {
	case 102:
		sub = 28
	case 103:
		sub = 30
	case 105:
		sub = 29
	case 106:
		sub = 31
	}
	s.PushRaw(24, sub, bodyBytes)
}

// fishTVKv 构造 401.f4 的鱼名和重量参数。
func fishTVKv(fishKey string, weightG int64) *proto.Value {
	if fishKey == "" {
		fishKey = "g_tench"
	}
	if weightG < 0 {
		weightG = 0
	}
	return proto.KVList(
		proto.KVEntry{Key: 1, Val: proto.StrVal(fishKey)},
		proto.KVEntry{Key: 2, Val: proto.U32(uint32(weightG))},
	)
}

// fishTVKvShape 按 shape 编号构造 401.f4 钓获模板 KV 参数（键1=鱼种key
// 键2=重量 键6=榜单类型），用于探测客户端的模板渲染形状。
func fishTVKvShape(shape int64, fishKey string, weightG int64) *proto.Value {
	k1, k2, k6 := "1", "2", "6"
	v1 := fishKey
	if v1 == "" {
		v1 = "g_tench"
	}
	v2 := fmt.Sprintf("%d", weightG)
	if weightG <= 0 {
		v2 = "3280"
	}
	v6 := "0"
	kvs := []*proto.Value{
		proto.KV(&k1, &v1),
		proto.KV(&k2, &v2),
		proto.KV(&k6, &v6),
	}
	switch shape {
	case 1:
		return &proto.Value{Kind: proto.KindArray, Items: kvs}
	case 2:
		return proto.Args524(kvs...)
	case 3:
		fk, wk, bk := "fish", "weight", "board"
		return &proto.Value{Kind: proto.KindArray, Items: []*proto.Value{
			proto.KV(&fk, &v1), proto.KV(&wk, &v2), proto.KV(&bk, &v6),
		}}
	case 4:
		// DICT，字符串值。
		return proto.DictOf("",
			proto.Pair{K: &k1, V: proto.StrVal(v1)},
			proto.Pair{K: &k2, V: proto.StrVal(v2)},
			proto.Pair{K: &k6, V: proto.StrVal(v6)},
		)
	case 5:
		// DICT，键2/键6 用数值。
		w, _ := strconv.ParseInt(v2, 10, 64)
		b, _ := strconv.ParseInt(v6, 10, 64)
		return proto.DictOf("",
			proto.Pair{K: &k1, V: proto.StrVal(v1)},
			proto.Pair{K: &k2, V: proto.U32(uint32(w))},
			proto.Pair{K: &k6, V: proto.U32(uint32(b))},
		)
	case 6:
		// 无码 ARGLIST 包 KV。
		return proto.Args(nil, kvs...)
	case 7:
		// 扁平字符串对：[k1,v1,k2,v2,...] 的 ARGLIST。
		return proto.Args(nil,
			proto.StrVal(k1), proto.StrVal(v1),
			proto.StrVal(k2), proto.StrVal(v2),
			proto.StrVal(k6), proto.StrVal(v6),
		)
	case 8:
		// 带 DTO 类型名的 DICT。
		return proto.DictOf("epbgnjmmiem",
			proto.Pair{K: &k1, V: proto.StrVal(v1)},
			proto.Pair{K: &k2, V: proto.StrVal(v2)},
			proto.Pair{K: &k6, V: proto.StrVal(v6)},
		)
	case 9:
		// 单个 KV，不套容器。
		return proto.KV(&k2, &v2)
	case 10:
		// STR3：[鱼种, 重量, 空]。
		empty := ""
		return proto.Str3(&v1, &v2, &empty)
	case 11:
		// args("61") 装 KV。
		code := "61"
		return proto.Args(&code, kvs...)
	case 12:
		// 裸数值：f4 直接是重量 U32。
		w, _ := strconv.ParseInt(v2, 10, 64)
		return proto.U32(uint32(w))
	case 20:
		// f4 = 判鱼 27 对象（fgogccpglhb：f1=鱼种key f2=档位 f4=重量）。
		w, _ := strconv.ParseInt(v2, 10, 64)
		return wiregen.MustMakeObj("fgogccpglhb", map[int]*proto.Value{
			1: proto.StrVal(v1),
			2: proto.U32(3),
			4: proto.U32(uint32(w)),
		})
	case 30:
		// 官方 KV 数组，附加榜单类型键 6。
		w, _ := strconv.ParseInt(v2, 10, 64)
		list := fishTVKv(v1, w)
		return &proto.Value{Kind: proto.KindKVList, Items: append(list.Items,
			&proto.Value{Kind: proto.KindKVEntry, Marker: 6, Items: []*proto.Value{proto.StrVal(v6)}})}
	case 40:
		return &proto.Value{Kind: proto.KindKVList}
	case 41:
		w, _ := strconv.ParseInt(v2, 10, 64)
		return proto.KVList(proto.KVEntry{Key: 2, Val: proto.U32(uint32(w))})
	case 42:
		return proto.KVList(proto.KVEntry{Key: 1, Val: proto.StrVal(v1)})
	case 43:
		w, _ := strconv.ParseInt(v2, 10, 64)
		return &proto.Value{Kind: proto.KindKVArr, Items: []*proto.Value{
			{Kind: proto.KindKVEntry, Marker: 1, Items: []*proto.Value{proto.StrVal(v1)}},
			{Kind: proto.KindKVEntry, Marker: 2, Items: []*proto.Value{proto.U32(uint32(w))}},
		}}
	case 44:
		return &proto.Value{Kind: proto.KindTypedArr, TypeName: "System.String"}
	case 45:
		return &proto.Value{Kind: proto.KindTypedArr, TypeName: "dejdolfnooh, Assembly-CSharp"}
	case 50:
		return &proto.Value{Kind: proto.KindTypedArr, TypeName: v1}
	case 51:
		w, _ := strconv.ParseInt(v2, 10, 64)
		return proto.KVListCustom(v1,
			proto.KVEntry{Key: 1, Val: proto.StrVal(v1)},
			proto.KVEntry{Key: 2, Val: proto.U32(uint32(w))})
	}
	return nil
}

// hSocialCurrent 处理 24/3 social/current / 24/2 social/auto：
// 返回当前公共聊天频道。客户端没有频道时聊天输入框不会发送 24/10。
func hSocialCurrent(ctx *Ctx) ([]byte, bool) {
	ch := sessChannel(ctx.Sess)
	srv := ctx.Sess.Srv
	srv.mu.Lock()
	users := int64(0)
	for _, sess := range srv.sessions {
		if sess != nil && !sess.Closed() {
			users++
		}
	}
	srv.mu.Unlock()
	if users < 1 {
		users = 1
	}
	obj := wiregen.MustMakeObj("godhiicbgng", map[int]*proto.Value{
		0: proto.StrVal(ch.Name),
		1: proto.StrVal("公共频道"),
		2: proto.StrVal(ch.Region),
		3: proto.U32(uint32(users)),
		4: proto.U32(0), // 0=Public
		5: proto.Bool(false),
		6: proto.Null(), // 成员列表懒加载
		7: proto.U32(1),
		8: proto.Bool(false),
	})
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// hSocialMove 处理 24/1 social/move：客户端在上船、跨水域时上报目标频道名。
//
// 只记录会话频道，响应回 null。回完整频道对象 godhiicbgng 时客户端会认为移动
// 未生效，以约 16ms 间隔持续重发并最终断开连接；回 null 时只发一次。
func hSocialMove(ctx *Ctx) ([]byte, bool) {
	name := argFirstStr(ctx.Tree)
	ch := sessChannel(ctx.Sess)
	if name == "" {
		name = ch.Name
	}
	ctx.Sess.SetState("channel", map[string]any{
		"name":    name,
		"region":  ch.Region,
		"users":   ch.Users,
		"private": false,
	})
	return []byte{0x00}, true
}

// hSocialPublic 处理 24/10 公聊，命令本身不返回响应。
//
// 普通发言通过 24/27 回显，客户端只接受 27..31 的聊天推送，不能直接推送 24/10。
// 推送由 buildChatEpochPush 构造并经过形状合同校验。空消息不广播；
// GM 指令不回显原文也不广播。
func hSocialPublic(ctx *Ctx) ([]byte, bool) {
	msg := argFirstStr(ctx.Tree)
	if msg == "" {
		return nil, true // 空消息不返回响应也不广播
	}
	if strings.HasPrefix(msg, "/") {
		ctx.Log.Info("24/10 GM 指令（Go 未实现，不回显）", "text", msg)
		return nil, true
	}
	ch := sessChannel(ctx.Sess)
	// 24/27 args[1] 必须与 24/3 godhiicbgng.f0 完全一致；带 [CN] 会被客户端当作别的频道丢弃。
	loc := ch.Name
	users := ch.Users
	if users < 1 {
		users = 1
	}
	// 公聊广播给所有在线会话；只回显发送者会导致多人频道互相看不到。
	srv := ctx.Sess.Srv
	srv.mu.Lock()
	peers := make([]*Session, 0, len(srv.sessions))
	for _, sess := range srv.sessions {
		if sess != nil && !sess.Closed() {
			peers = append(peers, sess)
		}
	}
	srv.mu.Unlock()
	if int64(len(peers)) > users {
		users = int64(len(peers))
	}
	for _, sess := range peers {
		sess.Push(24, 27, buildChatEchoPush(loc, msg, uint64(ctx.Sess.Userid), uint32(users)))
	}
	return nil, true // fire-and-forget，不返回响应
}

// ---------------------------------------------------------------------------
// 容器枚举全表
// ---------------------------------------------------------------------------

// containerEnumName 将容器枚举转换为 item.container 使用的存储名。
//
//	0 背包    1/6 帐篷    2/7 汽车    3/8 船
//	4/9 摩托车   5 鱼护    10/11 房屋
//
// 未知枚举回退到背包。4/1、4/4、4/5 只有背包和鱼护包含物品；其他容器必须
// 返回空，避免同一 GUID 被客户端登记到多个容器后发生索引冲突。
func containerEnumName(v uint64) string {
	switch v {
	case 0:
		return "backpack"
	case 1, 6:
		return "tent"
	case 2, 7:
		return "car"
	case 3, 8:
		return "boat"
	case 4, 9:
		return "scooter"
	case 5:
		return "keepnet"
	case 10, 11:
		return "house"
	default:
		return "backpack"
	}
}

// ---------------------------------------------------------------------------
// 顶层参数读取辅助
// ---------------------------------------------------------------------------

// argFirstStr 返回顶层首个非空字符串，同时接受裸字符串树。
func argFirstStr(v *proto.Value) string {
	for _, s := range argStringsTop(v) {
		if s != "" {
			return s
		}
	}
	return ""
}

// argStringsTop 返回顶层字符串参数，只扫描 args 或裸节点，不递归子对象。
func argStringsTop(v *proto.Value) []string {
	var out []string
	if v == nil {
		return out
	}
	if v.Kind == proto.KindStr {
		if v.Str != nil {
			out = append(out, *v.Str)
		}
		return out
	}
	if v.Kind == proto.KindArgs || v.Kind == proto.KindValList {
		for _, it := range v.Items {
			if it != nil && it.Kind == proto.KindStr && it.Str != nil {
				out = append(out, *it.Str)
			}
		}
	}
	return out
}

// argFirstStrOrInt 按顺序返回首个非空字符串或整数，整数包括 0。
// 返回值和是否命中的标志。
func argFirstStrOrInt(v *proto.Value) (string, bool) {
	if v == nil {
		return "", false
	}
	if v.Kind != proto.KindArgs && v.Kind != proto.KindValList {
		return "", false
	}
	for _, it := range v.Items {
		if it == nil {
			continue
		}
		switch it.Kind {
		case proto.KindStr:
			if it.Str != nil && *it.Str != "" {
				return *it.Str, true
			}
		case proto.KindU32, proto.KindU64:
			return strconv.FormatUint(it.Num, 10), true
		}
	}
	return "", false
}
