package gameserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// 邮箱 19/x。
//
// 协议字段 f5 恒为 0（Message）；数据库中的 kind 仅用于内部区分文本和附件，
// 不能作为协议 type 下发。邮件 GUID 是数字 id 的 32 位十六进制形式。
//
// 领取失败反馈不用 kOperationResult_* 枚举：该枚举没有"邮件附件发不出来"
// 这一语义，实机验证码 65 会显示成"没有足够的技能点来升级能力"（误导玩家）。
// 改用普通异常 + 中文 message，见 notifyMailClaimFailed。

func mailIDToGUID(id int64) string { return fmt.Sprintf("%032x", uint64(id)) }

func mailGUIDToID(g string) int64 {
	v, err := strconv.ParseInt(g, 16, 64)
	if err != nil {
		return 0
	}
	return v
}

// parseMailIDArg 解析管理指令里的邮件 id 参数，返回 0 表示非法。
//
// 必须区分两种形式：协议字段是 32 位十六进制 GUID（mailIDToGUID 的产物），
// 而管理后台按十进制入队（handlePlayerMailDelete）。若一律按十六进制解析，
// 十进制 "10" 会被读成 16，导致删错邮件、且后台仍显示"已排队"。
func parseMailIDArg(raw string) int64 {
	if raw == "" {
		return 0
	}
	// 32 位纯十六进制 = GUID 形式；其余按十进制。
	if len(raw) == 32 && isHex32(raw) {
		return mailGUIDToID(raw)
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// hMailList 19/6：guidarr（分页 offset/limit）。
func hMailList(ctx *Ctx) ([]byte, bool) {
	if !ctx.Sess.Srv.requireDB(ctx) {
		return nil, false
	}
	ints := treeInts(ctx.Tree)
	offset, limit := int64(0), int64(-1)
	if len(ints) > 0 {
		offset = int64(ints[0])
	}
	if len(ints) > 1 {
		limit = int64(ints[1])
	}
	mails, err := ctx.Sess.Srv.DB.MailList(ctx.Sess.pushCtx(), ctx.Sess.Userid, true)
	if err != nil {
		return nil, false
	}
	var guids []string
	for i, m := range mails {
		if int64(i) < offset {
			continue
		}
		if limit >= 0 && int64(len(guids)) >= limit {
			break
		}
		guids = append(guids, mailIDToGUID(m.ID))
	}
	body, err := proto.EncodeValue(proto.GuidArr(guids), ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// buildMail11062 读信详情（f0 text、f1 附件 args("308")[11063]、f2 guid、f3 FILETIME、f4 sender、f5 type=0）。
func buildMail11062(m *store.MailRow) *proto.Value {
	items := proto.Args(proto.S("308"))
	if m.ItemsJSON != "" {
		var list []store.MailItem
		if err := json.Unmarshal([]byte(m.ItemsJSON), &list); err == nil {
			// items_json 保存附件实例快照；没有快照的附件仍使用
			// GUID、item_id 和 qty 三个基础字段。
			for _, it := range list {
				g := it.GUID
				if g == "" {
					g = NewItemGUID()
				}
				items.Items = append(items.Items, wiregen.MustMakeObj("fhobhhbebdg", map[int]*proto.Value{
					0: proto.Guid(g),
					1: proto.U32(0),
					2: proto.StrVal(it.ItemID),
					3: proto.StrVal(""),
					4: proto.StrVal(""),
					5: proto.StrVal(""),
					6: proto.U32(uint32(it.Qty)),
				}))
			}
		}
	}
	var text *string
	if m.Text != "" {
		text = &m.Text
	}
	return wiregen.MustMakeObj("hoabkabpoom", map[int]*proto.Value{
		0: proto.Str(text),
		1: items,
		2: proto.Guid(mailIDToGUID(m.ID)),
		3: proto.U64(uint64(proto.UnixToFiletime(int64(m.CreatedTS)))),
		4: proto.U32(uint32(m.Sender)),
		5: proto.U32(0), // MailboxItemTypes.Message
	})
}

// notifyMail 在邮件事务提交后向收件人的全部在线会话发送完整邮件快照。
// 空参数不会给客户端提供新条目；附件仍须通过领取接口进入背包。
func (s *Server) notifyMail(ctx context.Context, uid, id int64) {
	mail, err := s.DB.MailGet(ctx, uid, id)
	if err != nil {
		s.log.Warn("邮件在线通知读取失败", "uid", uid, "mail_id", id, "err", err)
		return
	}
	if mail.Read != 0 {
		return
	}
	body := proto.Args524(buildMail11062(mail))
	var sent int
	for _, sess := range s.sessionsForUID(uid) {
		if sess.Push(19, 10, body) != 0 {
			sent++
		} else {
			s.log.Warn("邮件在线通知发送失败", "uid", uid, "mail_id", id, "sid", sess.ID)
		}
	}
	s.log.Info("邮件在线通知", "uid", uid, "mail_id", id, "sessions", sent)
}

// hMailGet 19/7：args("524")[11062...]。
func hMailGet(ctx *Ctx) ([]byte, bool) {
	if !ctx.Sess.Srv.requireDB(ctx) {
		return nil, false
	}
	out := proto.Args524()
	for _, g := range deepGUIDs(ctx.Tree) {
		id := mailGUIDToID(g)
		if id == 0 {
			continue
		}
		if m, err := ctx.Sess.Srv.DB.MailGet(ctx.Sess.pushCtx(), ctx.Sess.Userid, id); err == nil && m != nil {
			out.Items = append(out.Items, buildMail11062(m))
		}
	}
	body, err := proto.EncodeValue(out, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// hMailSendMessage 19/1：args = [收件人 uid, 正文]，响应 null。
func hMailSendMessage(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	ints := treeInts(ctx.Tree)
	texts := treeStrs(ctx.Tree)
	toUID := int64(0)
	if len(ints) > 0 {
		toUID = int64(ints[0])
	}
	text := ""
	if len(texts) > 0 {
		text = texts[0]
	}
	if toUID == fishQueryContactUID {
		return srv.receiveFishQuery(ctx, text)
	}
	if toUID == gearAssistantUID {
		return srv.receiveGearAssistant(ctx, text)
	}
	if toUID > 0 && toUID != ctx.Sess.Userid {
		cctx := ctx.Sess.pushCtx()
		tx, err := beginEconomyTx(cctx, srv.DB)
		var mailID int64
		if err == nil {
			defer func() { _ = tx.Rollback() }()
			var result sql.Result
			result, err = tx.ExecContext(cctx, `
				INSERT INTO mail(uid, sender, kind, text, items_json, created_ts, read)
				VALUES(?,?,0,?,NULL,?,0)`, toUID, ctx.Sess.Userid, text, float64(time.Now().Unix()))
			if err == nil {
				mailID, err = result.LastInsertId()
			}
			if err == nil {
				err = tx.Commit()
			}
		}
		if err != nil {
			ctx.Log.Warn("19/1 发送文本邮件失败", "to", toUID, "err", err)
		} else {
			srv.notifyMail(cctx, toUID, mailID)
		}
	}
	return []byte{0x00}, true
}

// setTreeQty 修改物品树中的数量字段（仅用于 19/2 的回执快照）。
func setTreeQty(tree *proto.Value, qty int64) {
	if tree == nil || tree.Kind != proto.KindObj {
		return
	}
	setItemQty(tree.Class, tree.Fields, uint64(qty))
}

const mailItemSnapshotVersion int64 = 1

// mailItemSnapshotFromRow 在寄件事务内复制源 item 行的完整实例状态。
// 计件物品只寄出 count，因此 fields_json 内的数量也同步为本次寄出的数量；
// 非计件物品保持字段快照不变。GUID 仍是源实例 GUID，领取时再按既有规则
// 决定保留（非计件）还是重分配（计件）。
func mailItemSnapshotFromRow(row *store.Item, qty int64, countable bool) (store.MailItem, error) {
	if row == nil {
		return store.MailItem{}, fmt.Errorf("空物品快照")
	}
	if row.GUID == "" || row.ItemID == "" || row.WireClass == "" || row.FieldsJSON == "" {
		return store.MailItem{}, fmt.Errorf("物品实例数据不完整: %s", row.GUID)
	}
	if qty < 1 {
		qty = 1
	}
	fieldsJSON := row.FieldsJSON
	if countable {
		fields, err := FieldsFromJSON(row.FieldsJSON)
		if err != nil {
			return store.MailItem{}, err
		}
		if setItemQty(row.WireClass, fields, uint64(qty)) {
			fieldsJSON, err = FieldsToJSON(fields)
			if err != nil {
				return store.MailItem{}, err
			}
		}
	}
	return store.MailItem{
		GUID:            row.GUID,
		ItemID:          row.ItemID,
		Qty:             qty,
		SnapshotVersion: mailItemSnapshotVersion,
		UID:             row.UID,
		NumericID:       row.NumericID,
		WireClass:       row.WireClass,
		TypeID:          row.TypeID,
		Container:       row.Container,
		Slot:            row.Slot,
		RigGUID:         row.RigGUID,
		BuyPrice:        row.BuyPrice,
		FieldsJSON:      fieldsJSON,
		Durability:      row.Durability,
		Workmanship:     row.Workmanship,
		CreatedTS:       row.CreatedTS,
	}, nil
}

// prepareMailSnapshotItem 用附件快照重建待入库物品。
// 没有快照的附件由 prepareNewItem 按定义生成。领取时计件附件分配新 GUID，
// 非计件附件保留源 GUID。
func (s *Server) prepareMailSnapshotItem(ctx context.Context, uid int64, att store.MailItem) (*store.Item, error) {
	if !att.HasItemSnapshot() {
		return nil, fmt.Errorf("邮件附件快照不完整: %s", att.ItemID)
	}
	def, err := s.DB.GetItemDefByItemID(ctx, att.ItemID)
	if err != nil {
		return nil, fmt.Errorf("附件定义不存在: %s: %w", att.ItemID, err)
	}
	if def == nil {
		return nil, fmt.Errorf("附件定义不存在: %s", att.ItemID)
	}
	countable := countableCategories[def.Category]
	guid := NewItemGUID()
	if !countable && isHex32(att.GUID) {
		guid = strings.ToLower(att.GUID)
	}
	fields, err := FieldsFromJSON(att.FieldsJSON)
	if err != nil {
		return nil, fmt.Errorf("附件字段快照损坏: %s: %w", att.ItemID, err)
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("附件字段快照为空: %s", att.ItemID)
	}
	fields = alignFieldsToOps(&store.Item{
		GUID: guid, NumericID: def.NumericID, WireClass: att.WireClass,
		Durability: att.Durability, Workmanship: att.Workmanship,
	}, fields)
	if q := fieldIdxByCS(att.WireClass, csGUID); q >= 0 && q < len(fields) {
		fields[q].Hex = guid
	}
	if q := fieldIdxByCS(att.WireClass, csNumericID); q >= 0 && q < len(fields) {
		fields[q].Num = ptrU64(uint64(def.NumericID))
	}
	qty := att.Qty
	if qty < 1 {
		qty = 1
	}
	if countable {
		setItemQty(att.WireClass, fields, uint64(qty))
	}
	fieldsJSON, err := FieldsToJSON(fields)
	if err != nil {
		return nil, err
	}
	numericID := att.NumericID
	if numericID <= 0 {
		numericID = def.NumericID
	}
	typeID := att.TypeID
	if typeID <= 0 {
		typeID = fallbackTypeID
		if cls, ok := categoryWireClass[def.Category]; ok {
			typeID = cls.TypeID
		}
	}
	return &store.Item{
		GUID:        guid,
		UID:         uid,
		ItemID:      def.ItemID,
		NumericID:   numericID,
		WireClass:   att.WireClass,
		TypeID:      typeID,
		Container:   "backpack",
		Slot:        0,
		RigGUID:     "",
		Qty:         qty,
		BuyPrice:    att.BuyPrice,
		FieldsJSON:  fieldsJSON,
		Durability:  att.Durability,
		Workmanship: att.Workmanship,
		CreatedTS:   att.CreatedTS,
	}, nil
}

// hMailSendItem 19/2：args 整数 [收件人 uid, 数量]，深扫描首个 Guid。
// 扣件、创建收件箱附件在同一事务中；响应 args(61)[寄出物品快照]。
func hMailSendItem(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	uid := ctx.Sess.Userid
	ints := treeInts(ctx.Tree)
	guids := deepGUIDs(ctx.Tree)
	out := proto.Args(proto.S("61"))
	if len(ints) == 0 || len(guids) == 0 {
		body, _ := proto.EncodeValue(out, ctx.Sess.Table())
		return body, true
	}
	toUID := int64(ints[0])
	count := int64(1)
	if len(ints) > 1 {
		count = int64(ints[1])
	}
	if toUID <= 0 || toUID == uid || isVirtualContact(toUID) || guids[0] == "" {
		body, _ := proto.EncodeValue(out, ctx.Sess.Table())
		return body, true
	}
	if count < 1 {
		count = 1
	}
	cctx := ctx.Sess.pushCtx()
	row, err := srv.DB.GetItemByGUID(cctx, guids[0])
	if err != nil || row == nil || row.UID != uid || row.Container != "backpack" {
		body, _ := proto.EncodeValue(out, ctx.Sess.Table())
		return body, true
	}
	have := srv.stackCountOf(cctx, row)
	if have < 1 {
		have = 1
	}
	if count > have {
		count = have
	}
	snapshot, _ := srv.ItemToTree(cctx, row)
	if snapshot != nil {
		setTreeQty(snapshot, count)
	}

	tx, err := beginEconomyTx(cctx, srv.DB)
	if err != nil {
		ctx.Log.Warn("19/2 开启事务失败", "err", err)
		return []byte{0x0F}, true
	}
	defer func() { _ = tx.Rollback() }()
	locked, err := getOwnedItemTx(cctx, tx, uid, guids[0])
	if err != nil || locked.Container != "backpack" {
		body, _ := proto.EncodeValue(out, ctx.Sess.Table())
		return body, true
	}
	reallyHave := stackCountTx(cctx, tx, locked)
	if reallyHave < 1 {
		reallyHave = 1
	}
	if count > reallyHave {
		count = reallyHave
	}
	def, err := itemDefByItemIDTx(cctx, tx, locked.ItemID)
	if err != nil {
		ctx.Log.Warn("19/2 读取寄件定义失败", "item", locked.ItemID, "err", err)
		return []byte{0x0F}, true
	}
	mailItem, err := mailItemSnapshotFromRow(locked, count, countableCategories[def.Category])
	if err != nil {
		ctx.Log.Warn("19/2 抓取物品快照失败", "guid", locked.GUID, "err", err)
		return []byte{0x0F}, true
	}
	got, err := consumeItemQtyTx(cctx, tx, locked, count)
	if err != nil || got != count {
		ctx.Log.Warn("19/2 扣除寄件失败", "guid", guids[0], "err", err, "got", got)
		return []byte{0x0F}, true
	}
	raw, err := json.Marshal([]store.MailItem{mailItem})
	if err != nil {
		return []byte{0x0F}, true
	}
	result, err := tx.ExecContext(cctx, `
		INSERT INTO mail(uid, sender, kind, text, items_json, attachment_snapshot_version, created_ts, read)
		VALUES(?,?,1,'',?,?,?,0)`, toUID, uid, string(raw), mailItemSnapshotVersion, float64(time.Now().Unix()))
	if err != nil {
		ctx.Log.Warn("19/2 创建附件邮件失败", "to", toUID, "err", err)
		return []byte{0x0F}, true
	}
	mailID, err := result.LastInsertId()
	if err != nil {
		ctx.Log.Warn("19/2 读取邮件编号失败", "err", err)
		return []byte{0x0F}, true
	}
	if err := tx.Commit(); err != nil {
		ctx.Log.Warn("19/2 事务提交失败", "err", err)
		return []byte{0x0F}, true
	}
	srv.notifyMail(cctx, toUID, mailID)
	if locked.Qty == 0 {
		ctx.Sess.Push4_21([]string{locked.GUID})
	}
	if snapshot != nil {
		out.Items = append(out.Items, snapshot)
	}
	body, err := proto.EncodeValue(out, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	ctx.Log.Info("19/2 寄出物品", "to", toUID, "guid", shortGUID(guids[0]), "qty", count)
	return body, true
}

// isHex32 判断字符串是否为 32 位十六进制 GUID。
func isHex32(v string) bool {
	if len(v) != 32 {
		return false
	}
	for _, c := range v {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// acceptMails 领取附件：先在事务外构造全部物品，再在事务内发放并标记已读。
// 任一件物品失败都会回滚并保持邮件未读。可堆叠物品合并到已有堆，
// 领取成功后通过 4/22 推送更新客户端背包缓存。
//
// 返回的 error 非 nil 表示本次领取存在玩家可见的失败：调用方必须回成业务错误，
// 不能回 null 成功——否则玩家点了没反应、物品也不进包，只能靠服主查日志才能发现
// （见 specs/changes/mail-claim-visible-failure）。
//
// 失败分两类，处理方式不同：
//   - 单封邮件自身的校验失败（附件定义缺失、快照损坏、数量超限等）：只跳过这封，
//     其余邮件照常发放。否则一封坏邮件会让"一键领取"永久卡死，玩家连好邮件都领不出来。
//   - 事务/数据库级失败：整体回滚，保证不出现"扣了一半"的中间态。
//
// (0, nil, nil) 表示没有可领取的未读邮件，属于正常空操作。
func (s *Server) acceptMails(ctx *Ctx, ids []int64) (int, []*store.Item, error) {
	cctx := ctx.Sess.pushCtx()
	uid := ctx.Sess.Userid
	type claimPlan struct {
		id              int64
		itemsJSON       string
		snapshotVersion int64
		prepared        []*store.Item
		// invalid 表示这封邮件自身校验不通过，准备阶段已跳过，不参与发放。
		invalid bool
	}
	var plans []claimPlan
	// skipped 记录"这封邮件本身领不出来"的原因，逐封隔离，不拖累其它邮件。
	var skipped []error
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		m, err := s.DB.MailGet(cctx, uid, id)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && m == nil) {
			// 邮件确实不存在（或已被并发删除）：正常空操作。
			continue
		}
		if err != nil {
			// 读库失败不能当作"没有邮件"吞掉——那会让本次领取悄悄变成空操作。
			ctx.Log.Warn("19/3 读取邮件失败", "mail", id, "err", err)
			return 0, nil, fmt.Errorf("读取邮件 %d 失败: %w", id, err)
		}
		if m.Read != 0 {
			continue
		}
		plan := claimPlan{id: id, itemsJSON: m.ItemsJSON, snapshotVersion: m.AttachmentSnapshotVersion}
		if m.ItemsJSON != "" {
			var attachments []store.MailItem
			if err := json.Unmarshal([]byte(m.ItemsJSON), &attachments); err != nil {
				ctx.Log.Warn("19/3 附件 JSON 损坏", "mail", id, "err", err)
				skipped = append(skipped, fmt.Errorf("邮件 %d 附件数据损坏: %w", id, err))
				plan.invalid = true
				break
			}
			for _, att := range attachments {
				if m.AttachmentSnapshotVersion > 0 && !att.HasItemSnapshot() {
					ctx.Log.Warn("19/3 附件快照不完整", "mail", id, "item", att.ItemID)
					skipped = append(skipped, fmt.Errorf("邮件 %d 附件快照不完整: %s", id, att.ItemID))
					plan.invalid = true
					break
				}
				def, derr := s.DB.GetItemDefByItemID(cctx, att.ItemID)
				if derr != nil || def == nil {
					ctx.Log.Warn("19/3 附件定义不存在", "mail", id, "item", att.ItemID, "err", derr)
					skipped = append(skipped, fmt.Errorf("邮件 %d 附件物品不存在: %s", id, att.ItemID))
					plan.invalid = true
					break
				}
				if def.Category == bundleCategory {
					openable := bundleOpenable(def)
					if att.BuyPrice != 0 && !openable {
						ctx.Log.Warn("19/3 组合包有购买价值，需人工处理", "mail", id, "item", att.ItemID)
						skipped = append(skipped, fmt.Errorf("邮件 %d 组合包需人工处理: %s", id, att.ItemID))
						plan.invalid = true
						break
					}
					if openable {
						// 礼盒附件以本体发放，一包一件（非计件）；玩家 4/8
						// 使用时拆开。饵料盒维持展开发放。
						packs := att.Qty
						if packs < 1 {
							packs = 1
						}
						if packs > maxBundlePreparedItems {
							ctx.Log.Warn("19/3 礼盒附件数量超过上限", "mail", id,
								"item", att.ItemID, "qty", packs, "limit", maxBundlePreparedItems)
							skipped = append(skipped, fmt.Errorf("礼盒附件数量 %d 超过上限 %d: %s",
								packs, maxBundlePreparedItems, att.ItemID))
							plan.invalid = true
							break
						}
						for i := int64(0); i < packs; i++ {
							it, berr := s.prepareNewItem(cctx, uid, att.ItemID, "backpack", 1, "")
							if berr != nil {
								ctx.Log.Warn("19/3 礼盒附件构造失败", "mail", id, "item", att.ItemID, "err", berr)
								skipped = append(skipped, fmt.Errorf("邮件 %d 礼盒附件构造失败: %s: %w", id, att.ItemID, berr))
								plan.invalid = true
								break
							}
							plan.prepared = append(plan.prepared, it)
						}
						continue
					}
					items, berr := s.prepareBundleItems(cctx, uid, att.ItemID, "backpack", att.Qty)
					if berr != nil {
						ctx.Log.Warn("19/3 组合包展开失败", "mail", id, "item", att.ItemID, "err", berr)
						skipped = append(skipped, fmt.Errorf("邮件 %d 组合包展开失败: %s: %w", id, att.ItemID, berr))
						plan.invalid = true
						break
					}
					plan.prepared = append(plan.prepared, items...)
					continue
				}
				var it *store.Item
				if m.AttachmentSnapshotVersion > 0 {
					it, err = s.prepareMailSnapshotItem(cctx, uid, att)
					if err != nil {
						ctx.Log.Warn("19/3 附件快照重建失败", "mail", id, "item", att.ItemID, "err", err)
						skipped = append(skipped, fmt.Errorf("邮件 %d 附件快照重建失败: %s: %w", id, att.ItemID, err))
						plan.invalid = true
						break
					}
				} else {
					// 没有实例快照时，按 GUID、item_id 和 qty 从定义重建。
					qty := att.Qty
					if qty < 1 {
						qty = 1
					}
					guid := ""
					if !countableCategories[def.Category] && isHex32(att.GUID) {
						guid = strings.ToLower(att.GUID)
					}
					it, err = s.prepareNewItem(cctx, uid, att.ItemID, "backpack", 1, guid)
					if err != nil {
						ctx.Log.Warn("19/3 附件构造失败", "mail", id, "item", att.ItemID, "err", err)
						skipped = append(skipped, fmt.Errorf("邮件 %d 附件构造失败: %s: %w", id, att.ItemID, err))
						plan.invalid = true
						break
					}
					if countableCategories[def.Category] {
						it.Qty = qty
						if fields, ferr := FieldsFromJSON(it.FieldsJSON); ferr == nil && setItemQty(it.WireClass, fields, uint64(qty)) {
							if fj, jerr := FieldsToJSON(fields); jerr == nil {
								it.FieldsJSON = fj
							}
						}
					}
				}
				plan.prepared = append(plan.prepared, it)
			}
		}
		if plan.invalid {
			continue
		}
		plans = append(plans, plan)
	}
	if len(plans) == 0 {
		// 没有可发放的邮件：若原因是坏邮件，仍要报错让玩家看到，而不是假装成功。
		return 0, nil, errors.Join(skipped...)
	}

	tx, err := beginEconomyTx(cctx, s.DB)
	if err != nil {
		ctx.Log.Warn("19/3 开启事务失败", "err", err)
		return 0, nil, fmt.Errorf("领取附件开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var granted []*store.Item
	for _, plan := range plans {
		var currentJSON string
		var currentSnapshotVersion int64
		var read int64
		err := tx.QueryRowContext(cctx, `
			SELECT COALESCE(items_json,''), COALESCE(attachment_snapshot_version,0), read
			FROM mail WHERE uid=? AND id=?`,
			uid, plan.id).Scan(&currentJSON, &currentSnapshotVersion, &read)
		if err == sql.ErrNoRows || read != 0 {
			continue
		}
		if err != nil || currentJSON != plan.itemsJSON || currentSnapshotVersion != plan.snapshotVersion {
			ctx.Log.Warn("19/3 邮件在领取期间发生变化", "mail", plan.id, "err", err)
			return 0, nil, fmt.Errorf("邮件 %d 在领取期间发生变化，请重试", plan.id)
		}
		for _, prepared := range plan.prepared {
			row, _, gerr := grantPreparedItemTx(cctx, tx, prepared, true)
			if gerr != nil {
				ctx.Log.Warn("19/3 附件入包失败", "mail", plan.id, "item", prepared.ItemID, "err", gerr)
				return 0, nil, fmt.Errorf("邮件 %d 附件入包失败: %s: %w", plan.id, prepared.ItemID, gerr)
			}
			granted = append(granted, row)
		}
		if _, err := tx.ExecContext(cctx, `UPDATE mail SET items_json=NULL, attachment_snapshot_version=0, read=1 WHERE uid=? AND id=? AND read=0`,
			uid, plan.id); err != nil {
			ctx.Log.Warn("19/3 标记邮件已读失败", "mail", plan.id, "err", err)
			return 0, nil, fmt.Errorf("邮件 %d 标记已读失败: %w", plan.id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		ctx.Log.Warn("19/3 提交失败", "err", err)
		return 0, nil, fmt.Errorf("领取附件提交失败: %w", err)
	}
	if len(granted) > 0 {
		trees := make([]*proto.Value, 0, len(granted))
		for _, row := range granted {
			if tree, terr := s.ItemToTree(cctx, row); terr == nil {
				trees = append(trees, tree)
			}
		}
		if len(trees) > 0 {
			ctx.Sess.Push(4, 22, proto.Args(proto.S("61"), trees...))
		}
	}
	ctx.Log.Info("领取附件", "count", len(granted), "skipped", len(skipped))
	// 好邮件已入包并置读；坏邮件的原因仍要回给玩家，让他知道还有东西没领出来。
	return len(granted), granted, errors.Join(skipped...)
}

// notifyMailClaimFailed 在领取失败时给出一条玩家可读的原因。
//
// 为什么不走 kOperationResult_* 枚举：abadjfbfdjb 的每个码都是为别的场景
// 设计的（"生产失败""技能点不足"等），没有一个是"这封邮件的附件发不出来"。
// 2026-11-10 实机验证：码 65 在客户端显示成"没有足够的技能点来升级能力"，
// 把玩家引向完全错误的方向（去点技能），比原来的静默失败更糟。
//
// 因此改用普通异常 + 中文 message（与 campfire/econ/fish_query 等既有路径一致，
// 客户端会把 message 直接显示出来）：既不依赖枚举取值，也不必改动客户端。
func (s *Server) notifyMailClaimFailed(ctx *Ctx, err error) {
	if ctx == nil || ctx.Sess == nil || err == nil {
		return
	}
	ctx.ReplyFailure("System.InvalidOperationException",
		"邮件附件领取失败："+err.Error()+"。邮件已保留，请联系服主处理。")
}

// hMailAccept 19/3：领取单封附件。
//
// 成功回 null。存在无法领取的附件（数量超限、定义缺失、快照损坏等）时
// 邮件保持未读，并回失败信封说明原因；不再静默回 null。
func hMailAccept(ctx *Ctx) ([]byte, bool) {
	if !ctx.Sess.Srv.requireDB(ctx) {
		return nil, false
	}
	var ids []int64
	for _, g := range deepGUIDs(ctx.Tree) {
		ids = append(ids, mailGUIDToID(g))
	}
	if _, _, err := ctx.Sess.Srv.acceptMails(ctx, ids); err != nil {
		ctx.Log.Warn("19/3 领取附件失败", "err", err)
		ctx.Sess.Srv.notifyMailClaimFailed(ctx, err)
		return nil, true
	}
	return []byte{0x00}, true
}

// hMailAcceptAll 19/4：一键领取全部未读附件。
//
// 与 19/3 一致：任何一封领不出来都回可读错误，可领的部分已入包并置读。
func hMailAcceptAll(ctx *Ctx) ([]byte, bool) {
	if !ctx.Sess.Srv.requireDB(ctx) {
		return nil, false
	}
	mails, err := ctx.Sess.Srv.DB.MailList(ctx.Sess.pushCtx(), ctx.Sess.Userid, true)
	if err != nil {
		// 读不到未读列表时不能回 null 成功：玩家会以为"已经全部领完"。
		ctx.Log.Warn("19/4 读取未读邮件失败", "err", err)
		ctx.ReplyFailure("System.InvalidOperationException",
			"读取邮箱失败，请稍后重试。")
		return nil, true
	}
	var ids []int64
	for _, m := range mails {
		ids = append(ids, m.ID)
	}
	if _, _, err := ctx.Sess.Srv.acceptMails(ctx, ids); err != nil {
		ctx.Log.Warn("19/4 一键领取附件失败", "err", err)
		ctx.Sess.Srv.notifyMailClaimFailed(ctx, err)
		return nil, true
	}
	return []byte{0x00}, true
}

// hMailRemove / hMailRemoveAll。
func hMailRemove(ctx *Ctx) ([]byte, bool) {
	if !ctx.Sess.Srv.requireDB(ctx) {
		return nil, false
	}
	for _, g := range deepGUIDs(ctx.Tree) {
		if id := mailGUIDToID(g); id > 0 {
			_ = ctx.Sess.Srv.DB.MailDelete(ctx.Sess.pushCtx(), ctx.Sess.Userid, id)
		}
	}
	return []byte{0x00}, true
}

func hMailRemoveAll(ctx *Ctx) ([]byte, bool) {
	if !ctx.Sess.Srv.requireDB(ctx) {
		return nil, false
	}
	_ = ctx.Sess.Srv.DB.MailDeleteAll(ctx.Sess.pushCtx(), ctx.Sess.Userid)
	return []byte{0x00}, true
}

// hMailInfo 19/5：pbndmldbfai(133){f0=未领取邮件数}。
func hMailInfo(ctx *Ctx) ([]byte, bool) {
	if !ctx.Sess.Srv.requireDB(ctx) {
		return nil, false
	}
	mails, err := ctx.Sess.Srv.DB.MailList(ctx.Sess.pushCtx(), ctx.Sess.Userid, true)
	if err != nil {
		return nil, false
	}
	obj := wiregen.MustMakeObj("pbndmldbfai", map[int]*proto.Value{
		0: proto.U32(uint32(len(mails))),
		1: proto.U32(0),
		2: proto.U32(0),
	})
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

func registerMailHandlers(d *Dispatch) {
	d.Register(19, 1, hMailSendMessage)
	d.Register(19, 2, hMailSendItem)
	d.Register(19, 3, hMailAccept)
	d.Register(19, 4, hMailAcceptAll)
	d.Register(19, 5, hMailInfo)
	d.Register(19, 6, hMailList)
	d.Register(19, 7, hMailGet)
	d.Register(19, 8, hMailRemove)
	d.Register(19, 9, hMailRemoveAll)
}

// ---- 管理指令队列 ----
//
// 指令：{"op":"kick","uid":N} / {"op":"grant_item","uid":N,"item_id":"x","qty":1}
//       {"op":"send_mail","uid":N,"text":"..","items":[{item_id,qty}]}
//       {"op":"broadcast","text":".."}

// AdminCommand 表示管理后台投递的指令。
type AdminCommand struct {
	Op           string              `json:"op"`
	UID          int64               `json:"uid,omitempty"`
	ItemID       string              `json:"item_id,omitempty"`
	Qty          int64               `json:"qty,omitempty"`
	Text         string              `json:"text,omitempty"`
	Items        []store.MailItem    `json:"items,omitempty"`
	GUIDs        []string            `json:"guids,omitempty"`
	Keepnet      *store.KeepnetEntry `json:"keepnet,omitempty"`
	KeepnetGUIDs []string            `json:"keepnet_guids,omitempty"`
	MailIDs      []string            `json:"mail_ids,omitempty"`    // delete_mail：要删除的邮件 id（十进制字符串或 GUID）
	Species      string              `json:"species,omitempty"`     // arm_fish：鱼种（空=默认池随机）
	WeightG      int64               `json:"weight_g,omitempty"`    // arm_fish：克重（空=默认池范围内）
	Grade        int64               `json:"grade,omitempty"`       // 兼容旧指令；实际档位按结算尺和重量计算。
	Channel      int64               `json:"channel,omitempty"`     // fish_tv_test：聊天推送通道 24..31
	Kind         int64               `json:"kind,omitempty"`        // fish_tv_test：401.f0 消息类型；负值为参数位探测
	KVShape      int64               `json:"kv_shape,omitempty"`    // fish_tv_test：401.f4 模板 KV 形状或外层参数探测编号
	AchID        int64               `json:"ach_id,omitempty"`      // achievement_push_test：成就 id
	NotifyType   int64               `json:"notify_type,omitempty"` // achievement_push_test：2/9 的 306.f0 取值
}

// StartAdminSubscriber 轮询共享 SQLite 队列，每 2 秒处理一批管理指令。
func (s *Server) StartAdminSubscriber(db *store.DB, stop <-chan struct{}) {
	if db == nil {
		return
	}
	go func() {
		ctx := context.Background()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				for {
					raw, ok, err := db.QueuePop(ctx)
					if err != nil || !ok {
						break
					}
					var cmd AdminCommand
					if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
						s.log.Warn("管理指令解析失败", "err", err)
						continue
					}
					s.execAdminCmd(&cmd)
				}
			}
		}
	}()
}

// execAdminCmd 执行管理指令。
func (s *Server) execAdminCmd(cmd *AdminCommand) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	switch cmd.Op {
	case "kick":
		for _, sess := range s.sessionsForUID(cmd.UID) {
			sess.Close("admin_kick")
			s.log.Info("管理指令：踢下线", "uid", cmd.UID)
		}
	case "grant_item":
		row, _, err := s.AddToStack(ctx, cmd.UID, cmd.ItemID, "backpack", cmd.Qty)
		if err != nil {
			s.log.Warn("管理指令：发物品失败", "uid", cmd.UID, "item", cmd.ItemID, "err", err)
		} else {
			s.pushItemUpdate(cmd.UID, row)
			s.log.Info("管理指令：发物品", "uid", cmd.UID, "item", cmd.ItemID, "qty", cmd.Qty)
		}
	case "delete_item":
		deleted := make([]string, 0, len(cmd.GUIDs))
		for _, guid := range cmd.GUIDs {
			row, err := s.DB.GetItemByGUID(ctx, guid)
			if err != nil || row == nil || row.UID != cmd.UID {
				continue
			}
			if row.RigGUID != "" || row.Container == "rig" {
				s.rigRemoveItemComponent(ctx, cmd.UID, row.RigGUID, guid)
				row, _ = s.DB.GetItemByGUID(ctx, guid)
			}
			if row == nil || row.UID != cmd.UID {
				deleted = append(deleted, guid)
				continue
			}
			if err := s.DB.DeleteItemCascade(ctx, guid); err != nil {
				s.log.Warn("管理指令：删除物品失败", "uid", cmd.UID, "guid", guid, "err", err)
				continue
			}
			deleted = append(deleted, guid)
		}
		if len(deleted) > 0 {
			for _, sess := range s.sessionsForUID(cmd.UID) {
				sess.Push4_21(deleted)
				sess.Push2_1()
			}
		}
		s.log.Info("管理指令：删除物品", "uid", cmd.UID, "count", len(deleted))
	case "add_keepnet":
		if cmd.Keepnet == nil {
			s.log.Warn("管理指令：增加护网鱼缺少数据", "uid", cmd.UID)
			break
		}
		entry := *cmd.Keepnet
		entry.UID = cmd.UID
		entry.Cat = s.rollGradeDB(ctx, entry.Species, entry.WeightG)
		if entry.EchoGUID == "" {
			entry.EchoGUID = newFishGUID()
		}
		if entry.TS <= 0 {
			entry.TS = float64(time.Now().UnixNano()) / 1e9
		}
		if entry.LengthM <= 0 && entry.WeightG > 0 {
			lp := lengthSeed[entry.Species]
			if lp[0] == 0 {
				lp = [2]float64{defaultSpeciesLengthK, defaultSpeciesLengthP}
			}
			lp[0], lp[1] = calibratedLengthKP(entry.Species, lp[0], lp[1])
			entry.LengthM = math.Round(speciesLengthM(entry.Species, lp[0], lp[1], float64(entry.WeightG))*100000) / 100000
		}
		if err := s.DB.KeepnetInsert(ctx, &entry); err != nil {
			s.log.Warn("管理指令：增加护网鱼失败", "uid", cmd.UID, "species", entry.Species, "err", err)
		} else {
			s.log.Info("管理指令：增加护网鱼", "uid", cmd.UID, "guid", shortGUID(entry.EchoGUID),
				"species", entry.Species, "weight", entry.WeightG)
		}
	case "delete_keepnet":
		if err := s.DB.KeepnetDelete(ctx, cmd.UID, cmd.KeepnetGUIDs); err != nil {
			s.log.Warn("管理指令：删除护网鱼失败", "uid", cmd.UID, "err", err)
		} else {
			s.log.Info("管理指令：删除护网鱼", "uid", cmd.UID, "count", len(cmd.KeepnetGUIDs))
		}
	case "send_mail":
		if id, err := s.DB.MailInsert(ctx, cmd.UID, 0, 0, cmd.Text, cmd.Items); err != nil {
			s.log.Warn("管理指令：发邮件失败", "uid", cmd.UID, "err", err)
		} else {
			s.notifyMail(ctx, cmd.UID, id)
		}
	case "delete_mail":
		// 服主清理由后台发件校验漏洞留下的坏邮件（领取侧会回业务错误，
		// 但玩家自己删不掉，只能由服主处理）。
		deleted := 0
		for _, raw := range cmd.MailIDs {
			id := parseMailIDArg(strings.TrimSpace(raw))
			if id <= 0 {
				s.log.Warn("管理指令：删除邮件跳过非法 id", "uid", cmd.UID, "raw", raw)
				continue
			}
			if err := s.DB.MailDelete(ctx, cmd.UID, id); err != nil {
				s.log.Warn("管理指令：删除邮件失败", "uid", cmd.UID, "mail", id, "err", err)
				continue
			}
			deleted++
		}
		s.log.Info("管理指令：删除邮件", "uid", cmd.UID, "count", deleted)
	case "arm_fish":
		st := s.stateFor(cmd.UID)
		f := &RolledFish{FishGUID: newFishGUID(), Species: cmd.Species, WeightG: cmd.WeightG, Grade: cmd.Grade, LocLevel: s.currentLevel(cmd.UID)}
		if f.Species == "" {
			r := s.rollFish(st.Loc, st.Bait)
			if r == nil {
				s.log.Warn("管理指令：当前没有匹配鱼，请指定鱼种", "uid", cmd.UID)
				return
			}
			f.Species = r.Species
			if f.WeightG == 0 {
				f.WeightG = r.WeightG
			}
		}
		if f.WeightG == 0 {
			f.WeightG = 500
		}
		f.Grade = s.rollGradeDB(ctx, f.Species, f.WeightG)
		if f.LengthM == 0 {
			lp := lengthSeed[f.Species]
			if lp[0] == 0 {
				lp = [2]float64{defaultSpeciesLengthK, defaultSpeciesLengthP}
			}
			lp[0], lp[1] = calibratedLengthKP(f.Species, lp[0], lp[1])
			f.LengthM = math.Round(speciesLengthM(f.Species, lp[0], lp[1], float64(f.WeightG))*100000) / 100000
		}
		st.mu.Lock()
		st.Armed = f
		st.mu.Unlock()
		s.log.Info("管理指令：下一杆指定鱼", "uid", cmd.UID, "species", f.Species, "weight", f.WeightG, "grade", f.Grade)
	case "broadcast":
		s.log.Info("管理指令：广播（占位）", "text", cmd.Text)
	case "reload_tunables":
		s.tunMu.Lock()
		s.tunCache = nil // 强制下次读取刷新
		s.tunMu.Unlock()
		if err := s.reloadFishEcologyCache(s.dbCtx()); err != nil {
			s.log.Warn("鱼种生态缓存刷新失败", "err", err)
		}
		if err := s.reloadHookBiteProfiles(s.dbCtx()); err != nil {
			s.log.Warn("鱼钩型号咬口缓存刷新失败", "err", err)
		}
		s.log.Info("管理指令：运行参数已刷新")
	case "reload_bait_rules":
		s.invalidateBaitOverrides()
		s.log.Info("管理指令：鱼饵规则覆盖已刷新")
	case "reload_fish_zones":
		s.fishZoneMu.Lock()
		s.fishZoneCache = nil
		s.fishZoneMu.Unlock()
		s.log.Info("管理指令：区域鱼种名册已刷新")
	case "fish_tv_test":
		// 鱼汛通道探测：向指定玩家（0=全部在线会话）推送一条测试消息。
		var targets []*Session
		if cmd.UID != 0 {
			targets = s.sessionsForUID(cmd.UID)
		} else {
			s.mu.Lock()
			for _, sess := range s.sessions {
				if sess != nil && !sess.Closed() {
					targets = append(targets, sess)
				}
			}
			s.mu.Unlock()
		}
		text := cmd.Text
		if text == "" {
			text = fmt.Sprintf("鱼汛通道测试 ch=%d kind=%d", cmd.Channel, cmd.Kind)
		}
		kv := fishTVKvShape(cmd.KVShape, cmd.Species, cmd.WeightG)
		if cmd.KVShape >= 100 {
			// 外层参数探测：绕过推送形状守卫。
			rawVariant := int(cmd.KVShape)
			for _, sess := range targets {
				sess.pushChatRaw(rawVariant, int(cmd.Kind), text)
			}
			s.log.Info("管理指令：鱼汛外层参数探测", "uid", cmd.UID,
				"variant", rawVariant, "kind", cmd.Kind, "targets", len(targets))
			return
		}
		for _, sess := range targets {
			sess.pushChatProbe(int(cmd.Channel), int(cmd.Kind), text, kv)
		}
		s.log.Info("管理指令：鱼汛通道测试", "uid", cmd.UID,
			"channel", cmd.Channel, "kind", cmd.Kind, "kv_shape", cmd.KVShape, "targets", len(targets))
	case "achievement_push_test":
		// 成就解锁弹窗探测：向指定玩家（0=全部在线）发一种候选推送。
		variant := cmd.Text
		if variant == "" {
			variant = "3_3"
		}
		achID := cmd.AchID
		if achID == 0 {
			achID = 101
		}
		var targets []*Session
		if cmd.UID != 0 {
			targets = s.sessionsForUID(cmd.UID)
		} else {
			s.mu.Lock()
			for _, sess := range s.sessions {
				if sess != nil && !sess.Closed() {
					targets = append(targets, sess)
				}
			}
			s.mu.Unlock()
		}
		sent := 0
		for _, sess := range targets {
			if sess.pushAchievementProbe(variant, achID, cmd.NotifyType) {
				sent++
			}
		}
		s.log.Info("管理指令：成就推送探针", "uid", cmd.UID, "variant", variant,
			"ach_id", achID, "notify_type", cmd.NotifyType, "targets", len(targets), "sent", sent)
	case "refresh_player":
		for _, sess := range s.sessionsForUID(cmd.UID) {
			sess.Push2_1()
		}
		s.log.Info("管理指令：刷新玩家档案", "uid", cmd.UID)
	case "refresh_skills":
		for _, sess := range s.sessionsForUID(cmd.UID) {
			sess.Push2_4SkillTree("admin refresh")
		}
		s.log.Info("管理指令：刷新技能面板", "uid", cmd.UID)
	default:
		s.log.Warn("未知管理指令", "op", cmd.Op)
	}
}

func (s *Server) pushItemUpdate(uid int64, row *store.Item) {
	if row == nil {
		return
	}
	tree, err := s.ItemToTree(s.dbCtx(), row)
	if err != nil {
		return
	}
	for _, sess := range s.sessionsForUID(uid) {
		sess.Push(4, 22, proto.Args(proto.S("61"), tree))
	}
}

// sessionsForUID 取 uid 全部存活会话。
func (s *Server) sessionsForUID(uid int64) []*Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Session
	for _, sess := range s.sessions {
		if sess.Userid == uid && !sess.Closed() {
			out = append(out, sess)
		}
	}
	return out
}

// CheckBanned 会话开启时校验封禁（踢掉封禁玩家）。
func (s *Server) CheckBanned(sess *Session) {
	if s.DB == nil {
		return
	}
	banned, err := s.DB.IsBanned(s.dbCtx(), sess.Userid)
	if err == nil && banned {
		sess.log.Info("封禁玩家拒绝接入")
		sess.Close("banned")
	}
}

var _ = slog.Default
