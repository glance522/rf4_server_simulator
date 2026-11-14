package gameserver

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"rf4server/internal/proto"
)

const gearAssistantUID int64 = 2147483646
const gearAssistantName = "钓具助手"

func overloadProtectionKey(uid int64) string {
	return fmt.Sprintf("gear-assistant:overload-protection:%d", uid)
}

type overloadProtectionReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readOverloadProtection(ctx context.Context, db overloadProtectionReader, uid int64) (bool, error) {
	return readKVFlag(ctx, db, overloadProtectionKey(uid))
}

// 钓具助手支持完整功能指令，并保留两级数字菜单：
//
//	直接发送功能名称及开/关；保留原有数字菜单和耐力播报。
//	二级（发功能序号后进入）：只响应 开(y) 关(n) 说明(h) 返回(b)；
//	                        未知输入重复二级菜单；b 退回一级。
type gearFeature struct {
	label  string // 回复里显示的名称
	onNew  string // 新开启时的回复
	offNew string // 新关闭时的回复
	desc   string // 「说明」子命令返回的功能描述
}

// gearSep 是菜单行之间的分隔线。长度按客户端聊天气泡宽度实测调过：22 个会折行，21 个刚好一行。
const gearSep = "─────────────────────"

var gearFeatures = map[string]gearFeature{
	"shoes":    {"【五速鞋】", "已开启，跑步速度变为原来的 3 倍。", "已关闭，恢复正常跑步速度。", "【五速鞋】：跑步速度为原来的 3 倍，走路速度不变；沿用游戏的跑步按键设置。需要配套客户端模块。"},
	"overload": {"【满红不爆】", "已开启，对你的所有鱼竿生效。", "已关闭，恢复正常装备损坏规则。", "【满红不爆】：防止断件与锁轮过载烧毁；普通磨损、自然脱钩及主动剪线不变，不修复已有损伤。"},
	"stamina":  {"【耐力播报】", "已开启：搏鱼期间每 5 秒在聊天框显示当前鱼的耐力与超载。", "已关闭。", "【耐力播报】：搏鱼期间每 5 秒在聊天框显示当前鱼的耐力与超载。耐力是这尾鱼还剩多少体力，会一路掉到 0；超载是这尾鱼此刻的拉力超出整根钓组最大拉力的百分比，0% 表示拉得动（小鱼恒为 0%），数字越大越拉不动，鱼乏了它会自己降下来。仅在当前手持竿进入搏鱼时播报。"},
}

// gearCommand 是一级解析结果：mode 为 help/unknown/cmd。
type gearCommand struct {
	mode    string // "help" | "unknown" | "cmd"
	feature string // "overload" | "stamina" | "shoes"
	action  string // ""=进入二级看状态 | "on" | "off" | "desc" | "back"
}

func parseGearCommand(text string) gearCommand {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return gearCommand{mode: "help"}
	}
	head := fields[0]
	switch head {
	case "帮助", "菜单", "help", "Help", "HELP", "?", "h", "0", "说明":
		return gearCommand{mode: "help"}
	case "1", "过载", "满红", "满红不爆", "overload", "Overload", "OVERLOAD":
		head = "overload"
	case "2", "耐力", "耐力播报", "stamina", "Stamina", "STAMINA":
		head = "stamina"
	case "3", "鞋", "五速鞋":
		head = "shoes"
	case "开", "开启", "关", "关闭", "状态":
		// 两级菜单之前的单字指令，语义是满红不爆。保留成一级快捷别名：
		// 老玩家不必先记序号，也不会因为菜单化而失去「开关」这个动作。
		if len(fields) > 1 {
			return gearCommand{mode: "unknown"}
		}
		return gearCommand{mode: "cmd", feature: "overload", action: legacyGearAction(head)}
	default:
		return gearCommand{mode: "unknown"}
	}
	action := ""
	if len(fields) > 1 {
		switch fields[1] {
		case "开", "开启", "on", "enable", "ON", "ENABLE", "k", "K", "y", "Y":
			action = "on"
		case "关", "关闭", "off", "disable", "OFF", "DISABLE", "g", "G", "n", "N":
			action = "off"
		case "状态", "status", "query", "STATUS":
			action = ""
		case "说明", "help", "describe", "info", "?", "HELP", "DESCRIBE", "INFO", "h", "H":
			action = "desc"
		case "返回", "back", "BACK", "b", "B":
			action = "back"
		default:
			return gearCommand{mode: "unknown"}
		}
	}
	return gearCommand{mode: "cmd", feature: head, action: action}
}

// legacyGearAction 把旧单字指令还原成二级动作；「状态」对应空动作（只显示菜单）。
func legacyGearAction(text string) string {
	switch text {
	case "开", "开启":
		return "on"
	case "关", "关闭":
		return "off"
	}
	return ""
}

// gearReply 拼出二级菜单内任意操作的回复，统一带快捷键尾行。
func gearReply(f gearFeature, action string, enabled bool) (next bool, reply string) {
	const footer = "\n" + gearSep + "\n开(y) · 关(n) · 说明(h) · 返回(b)"
	next = enabled
	switch action {
	case "on":
		next = true
		if enabled {
			reply = f.label + "已经开启，无需重复设置。"
		} else {
			reply = f.label + f.onNew
		}
	case "off":
		next = false
		if !enabled {
			reply = f.label + "已经关闭，无需重复设置。"
		} else {
			reply = f.label + f.offNew
		}
	case "desc":
		reply = f.desc
	default:
		status := "关闭"
		if enabled {
			status = "开启"
		}
		reply = "【当前状态】：" + status
	}
	reply += footer
	return next, reply
}

func gearAssistantHelp() string {
	items := []string{"1  【满红不爆】", "2  【耐力播报】", "3  【五速鞋】"}
	// 每行下面都跟一条分隔线（含最后一行），形成下划线效果。
	return strings.Join(items, "\n"+gearSep+"\n") + "\n" + gearSep
}

// ---- 二级菜单状态（per uid）----

func (s *Server) gearMenuGet(uid int64) string {
	s.gearMenuMu.Lock()
	defer s.gearMenuMu.Unlock()
	if s.gearMenuState == nil {
		return ""
	}
	return s.gearMenuState[uid]
}

func (s *Server) gearMenuSet(uid int64, feature string) {
	s.gearMenuMu.Lock()
	defer s.gearMenuMu.Unlock()
	if s.gearMenuState == nil {
		s.gearMenuState = make(map[int64]string)
	}
	s.gearMenuState[uid] = feature
}

func (s *Server) gearMenuClear(uid int64) {
	s.gearMenuMu.Lock()
	defer s.gearMenuMu.Unlock()
	if s.gearMenuState != nil {
		delete(s.gearMenuState, uid)
	}
}

// gearAssistantReplyText 只回一条消息、不改任何设置，用于帮助、未知与二级菜单显示。
func (s *Server) gearAssistantReplyText(ctx *Ctx, uid int64, reply string, silent ...bool) ([]byte, bool) {
	c := ctx.Sess.pushCtx()
	tx, err := beginEconomyTx(c, s.DB)
	if err != nil {
		ctx.ReplyFailure("System.IO.IOException", "钓具设置保存失败")
		return nil, true
	}
	defer func() { _ = tx.Rollback() }()
	var mailID int64
	quiet := len(silent) > 0 && silent[0]
	if quiet {
		reply, err = gearClientSnapshot(c, tx, uid, reply)
	}
	if err == nil {
		mailID, err = insertContactReply(c, tx, uid, gearAssistantUID, reply)
	}
	if err == nil && quiet {
		_, err = tx.ExecContext(c, `UPDATE mail SET read=1 WHERE uid=? AND id=?`, uid, mailID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		ctx.ReplyFailure("System.IO.IOException", "钓具设置保存失败")
		return nil, true
	}
	ctx.AfterResponse(func() {
		if !quiet {
			s.notifyMail(ctx.Sess.pushCtx(), uid, mailID)
			return
		}
		// Deliver the existing snapshot to the bridge without creating unread mail.
		mail, err := s.DB.MailGet(ctx.Sess.pushCtx(), uid, mailID)
		if err != nil || mail == nil {
			s.log.Warn("钓具设置同步读取失败", "uid", uid, "mail_id", mailID, "err", err)
			return
		}
		for _, sess := range s.sessionsForUID(uid) {
			sess.Push(19, 10, proto.Args524(buildMail11062(mail)))
		}
	})
	return []byte{0}, true
}

// receiveGearAssistant 优先处理完整功能指令，其余输入沿用两级菜单。
func (s *Server) receiveGearAssistant(ctx *Ctx, text string) ([]byte, bool) {
	if !ctx.ExpectResponse {
		return nil, true
	}
	uid := ctx.Sess.Userid
	if uid <= 0 {
		ctx.ReplyFailure("System.InvalidOperationException", "请先登录")
		return nil, true
	}
	// The optional local bridge asks once after its contact model is created.
	// This is a read, and must not change the player's current submenu.
	if text == "同步设置" {
		return s.gearAssistantReplyText(ctx, uid, "钓具设置已同步。", true)
	}
	// 完整功能名称可从任意子菜单切换，后续仍走原有菜单处理。
	cmd := parseGearCommand(text)
	fields := strings.Fields(text)
	named := false
	if len(fields) > 0 {
		switch strings.ToLower(fields[0]) {
		case "满红不爆", "过载", "满红", "鞋", "五速鞋", "耐力", "耐力播报", "stamina", "overload":
			named = true
		}
	}
	if named && len(fields) > 2 {
		return s.gearAssistantReplyText(ctx, uid, gearAssistantHelp())
	}
	if !named {
		if state := s.gearMenuGet(uid); state != "" {
			return s.gearSubMenu(ctx, uid, state, text)
		}
	}

	switch cmd.mode {
	case "help":
		return s.gearAssistantReplyText(ctx, uid, gearAssistantHelp())
	case "unknown":
		return s.gearAssistantReplyText(ctx, uid, gearAssistantHelp())
	}
	// cmd.mode == "cmd"：进入对应二级
	s.gearMenuSet(uid, cmd.feature)
	if cmd.action == "back" {
		s.gearMenuClear(uid)
		return s.gearAssistantReplyText(ctx, uid, gearAssistantHelp())
	}
	if cmd.action == "" {
		return s.gearSubShow(ctx, uid, cmd.feature)
	}
	return s.gearSubAction(ctx, uid, cmd.feature, cmd.action)
}

// gearSubMenu 处理二级内的输入：只认 y/n/h/b，未知重显，b 返回一级。
func (s *Server) gearSubMenu(ctx *Ctx, uid int64, feature, text string) ([]byte, bool) {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "y", "k", "开", "开启", "on", "enable":
		return s.gearSubAction(ctx, uid, feature, "on")
	case "n", "g", "关", "关闭", "off", "disable":
		return s.gearSubAction(ctx, uid, feature, "off")
	case "h", "说明", "help", "describe", "info", "?":
		return s.gearSubShow(ctx, uid, feature, "desc")
	case "b", "返回", "back":
		s.gearMenuClear(uid)
		return s.gearAssistantReplyText(ctx, uid, gearAssistantHelp())
	default:
		// 输入未知，重复二级命令
		return s.gearSubShow(ctx, uid, feature)
	}
}

// gearReadFlag 在独立短事务中读取开关，事务在返回前结束，避免与后续回信事务重叠。
func (s *Server) gearReadFlag(c context.Context, uid int64, feature string) (bool, error) {
	tx, err := beginEconomyTx(c, s.DB)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	return readGearFeatureFlag(c, tx, uid, feature)
}

// gearSubShow 显示二级菜单（可选先显示说明），不改设置。
func (s *Server) gearSubShow(ctx *Ctx, uid int64, feature string, prefix ...string) ([]byte, bool) {
	enabled, err := s.gearReadFlag(ctx.Sess.pushCtx(), uid, feature)
	if err != nil {
		ctx.ReplyFailure("System.IO.IOException", "钓具设置读取失败")
		return nil, true
	}
	var reply string
	if len(prefix) > 0 && prefix[0] == "desc" {
		reply = gearFeatures[feature].desc + "\n\n"
	}
	_, body := gearReply(gearFeatures[feature], "", enabled)
	reply += body
	return s.gearAssistantReplyText(ctx, uid, reply)
}

// gearSubAction 在二级内执行开/关，写设置后停留二级（重显二级菜单）。
func (s *Server) gearSubAction(ctx *Ctx, uid int64, feature, action string) ([]byte, bool) {
	// 只有满红不爆要和断件/负载判定串行（同一把锁也在磨损结算里用）。
	// 耐力播报只读写自己那一行 kv，没必要占用它，避免菜单的 DB+邮件写入阻塞搏鱼结算。
	if feature == "overload" {
		s.overloadProtectionMu.Lock()
		defer s.overloadProtectionMu.Unlock()
	}
	c := ctx.Sess.pushCtx()
	tx, err := beginEconomyTx(c, s.DB)
	if err != nil {
		ctx.ReplyFailure("System.IO.IOException", "钓具设置保存失败")
		return nil, true
	}
	defer func() { _ = tx.Rollback() }()
	key := gearFeatureKey(uid, feature)
	enabled, err := readGearFeatureFlag(c, tx, uid, feature)
	if err != nil {
		ctx.ReplyFailure("System.IO.IOException", "钓具设置读取失败，设置未改变")
		return nil, true
	}
	next, reply := gearReply(gearFeatures[feature], action, enabled)
	var mailID int64
	if next != enabled {
		value := "0"
		if next {
			value = "1"
		}
		_, err = tx.ExecContext(c, `INSERT INTO kv(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, key, value)
	}
	if err == nil {
		mailID, err = insertContactReply(c, tx, uid, gearAssistantUID, reply)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		ctx.ReplyFailure("System.IO.IOException", "钓具设置保存失败，设置未改变")
		return nil, true
	}
	if next != enabled {
		switch feature {
		case "overload":
			now := time.Now()
			for _, st := range s.fishingStatesFor(uid) {
				st.mu.Lock()
				// Keep incurred wear; the timestamp only bounds later tick billing.
				st.reelWear.lastLoadAt = now
				st.reelWear.protectionChangedAt = now
				st.mu.Unlock()
			}
		case "stamina":
			// 设置已落库，清掉进程内缓存，下一条 14/8 重新读。
			s.invalidateStaminaFlag(uid)
		}
	}
	ctx.AfterResponse(func() { s.notifyMail(ctx.Sess.pushCtx(), uid, mailID) })
	return []byte{0}, true
}
