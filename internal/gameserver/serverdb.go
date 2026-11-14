package gameserver

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

//go:embed assets/starter_items.json
var starterItemsJSON []byte

//go:embed assets/fight_curves_sets.json
var fightCurvesJSON []byte

// attachDB 给主服接上数据库；nil 时降级到模板兜底。
func (s *Server) attachDB(db *store.DB, log *slog.Logger) {
	s.DB = db
	if db == nil {
		return
	}
	s.loadFightCurves()
	if n, err := s.ensureFishEcology(context.Background()); err != nil {
		s.log.Warn("鱼种生态档案初始化失败", "err", err)
	} else if n > 0 {
		s.log.Info("鱼种生态档案初始化", "inserted", n)
	}
	if err := s.reloadFishEcologyCache(context.Background()); err != nil {
		s.log.Warn("鱼种生态缓存加载失败", "err", err)
	}
	if n, err := s.ensureFishSkillEffects(context.Background()); err != nil {
		s.log.Warn("鱼口技能效果初始化失败", "err", err)
	} else if n > 0 {
		s.log.Info("鱼口技能效果初始化", "inserted", n)
	}
	if err := s.reloadHookBiteProfiles(context.Background()); err != nil {
		s.log.Warn("鱼钩型号咬口缓存加载失败", "err", err)
	}
}

// loadFightCurves 解析搏鱼曲线池，把 f0==100 的基础行放在首位。
func (s *Server) loadFightCurves() {
	var parsed struct {
		Pools map[string]struct {
			Rows [][19]any `json:"rows"`
		} `json:"pools"`
	}
	if err := json.Unmarshal(fightCurvesJSON, &parsed); err != nil {
		s.log.Warn("fight_curves_sets.json 解析失败", "err", err)
		return
	}
	s.curveMu.Lock()
	defer s.curveMu.Unlock()
	s.curves = map[string][][19]float64{}
	for sp, pool := range parsed.Pools {
		rows := make([][19]float64, 0, len(pool.Rows))
		var base [19]float64
		for _, raw := range pool.Rows {
			var r [19]float64
			for i, v := range raw {
				switch tv := v.(type) {
				case float64:
					r[i] = tv
				case string:
					fmt.Sscanf(tv, "%g", &r[i])
				}
			}
			if int(r[0]) == 100 {
				base = r
				continue
			}
			rows = append(rows, r)
		}
		// 基础行钉首位（无则保持原序）
		out := make([][19]float64, 0, len(rows)+1)
		if base[0] == 100 {
			out = append(out, base)
		}
		out = append(out, rows...)
		s.curves[sp] = out
	}
}

// buildFightCurves 构造 27.f21 曲线池：鱼种池缺省借同习性池，张力列按
// pull/池基准 uniform 缩放，其余列保持池原值（官方口径，见 spec）。
func (s *Server) buildFightCurves(species string, pull float64) *proto.Value {
	s.curveMu.Lock()
	rows := s.curves[species]
	if len(rows) == 0 {
		rows = s.curves[fishFightArchetypeFor(species).CurvePool]
		if len(rows) == 0 {
			rows = s.curves["crucian"]
		}
	}
	s.curveMu.Unlock()

	out := proto.Args(proto.S("27"))
	if len(rows) == 0 {
		return out
	}
	selected := make([][19]float64, 0, len(rows))
	if s.tunableF("fight_curves_fixture", 0) != 0 {
		selected = append(selected, knownRealFightCurveRows()...)
	} else {
		selected = rows
	}
	// 官方 14/14 抓包证实：每鱼曲线 = 鱼种池 × 单一 uniform scale（张力列逐行
	// 精确成立，见 specs/changes/fish-pull-k-table/spec.md），duration 与其余列
	// 不随体重/阶段缩放。任何逐阶段乘数都是自造层，直接破坏 105 主搏/302 下潜
	// 的官方强度比例。
	baseTension := selected[0][8]
	scale := 1.0
	if baseTension > 0 {
		scale = pull / baseTension
	}
	for _, r := range selected {
		items := map[int]*proto.Value{0: proto.U32(uint32(r[0]))}
		for i := 1; i <= 14; i++ {
			v := r[i]
			if i == 8 || i == 9 {
				v *= scale
			}
			items[i] = proto.F32(float32(v))
		}
		for i := 15; i <= 18; i++ {
			items[i] = proto.U32(uint32(r[i])) // 必须 int（阶段重复次数区间）
		}
		out.Items = append(out.Items, wiregen.MustMakeObj("oiikpeaajln", items))
	}
	return out
}

// knownRealFightCurveRows 是 2026-10-31 真服 14/14 抓包中 27.f21 的原始两行。
// 仅用于对照验证客户端的鱼状态机；正式曲线仍应从表/鱼种映射生成。
func knownRealFightCurveRows() [][19]float64 {
	return [][19]float64{
		{105, 1, 0.2, 0.4, 0.25774, 0.42957, 0.75, 1.25, 0.12994, 0.38981, -0.5, 0.5, 0, 0, math.Inf(1), 0, 0, 0, 0},
		{301, 348, 2, 2, 0.23323, 0.46645, 1, 2, 1.55926, 1.55926, 0, 0, 0, 0, 0, 4500, 5000, 0, 0},
	}
}

// buildFightItems 构造 4/22 物品同步元素：full 返回竿和线，非 full 返回钩。
// 14/11 以钩强 f0 作为负载比基准，因此非 full 分支不能返回竿树。
func (s *Server) buildFightItems(ctx context.Context, uid int64, full bool, gears ...string) ([]*proto.Value, error) {
	st := s.stateFor(uid)
	st.mu.Lock()
	gear := st.GearGuid
	st.mu.Unlock()
	if len(gears) > 0 {
		gear = gears[0]
	}
	if gear == "" {
		// 无手持竿：取背包第一件竿
		guids, _ := s.DB.ItemGUIDsByContainers(ctx, uid, []string{"backpack", "rig"})
		for _, g := range guids {
			row, err := s.DB.GetItemByGUID(ctx, g)
			if err == nil && row.WireClass == "cnolibojkao" {
				gear = g
				break
			}
		}
	}
	if gear == "" {
		return nil, nil
	}
	if !full {
		if hook := s.buildMountedHookTree(ctx, gear); hook != nil {
			return []*proto.Value{hook}, nil
		}
		return nil, nil
	}
	var out []*proto.Value
	rodRow, err := s.DB.GetItemByGUID(ctx, gear)
	if err != nil {
		return nil, err
	}
	rodTree, err := s.ItemToTree(ctx, rodRow)
	if err != nil {
		return nil, err
	}
	out = append(out, rodTree)
	// 线 = 挂装槽 11 的物品树
	if lineGUID, err := s.DB.GetItemGUIDByRigSlot(ctx, gear, 11); err == nil && lineGUID != "" {
		if lineRow, err := s.DB.GetItemByGUID(ctx, lineGUID); err == nil {
			lineTree, err := s.ItemToTree(ctx, lineRow)
			if err == nil {
				out = append(out, lineTree)
			}
		}
	}
	return out, nil
}

// buildMountedHookTree 返回线末端钩的完整实例树。
// 优先使用钩物品行；拟饵出厂钩没有独立行时，从拟饵子对象中取同 GUID 的树。
func (s *Server) buildMountedHookTree(ctx context.Context, gearGUID string) *proto.Value {
	hookGUID := s.mountedHookGuid(ctx, gearGUID)
	if hookGUID == "" {
		return nil
	}
	if row, err := s.DB.GetItemByGUID(ctx, hookGUID); err == nil && row != nil {
		if tree, terr := s.ItemToTree(ctx, row); terr == nil {
			return tree
		}
	}
	lureGUID := s.mountedLureGUID(ctx, gearGUID)
	if lureGUID == "" {
		return nil
	}
	lureRow, err := s.DB.GetItemByGUID(ctx, lureGUID)
	if err != nil || lureRow == nil {
		return nil
	}
	lureTree, err := s.ItemToTree(ctx, lureRow)
	if err != nil || len(lureTree.Fields) == 0 || lureTree.Fields[0].Val == nil {
		return nil
	}
	for _, hook := range lureTree.Fields[0].Val.Items {
		if hook == nil {
			continue
		}
		for i := range hook.Fields {
			if hook.Fields[i].Op == "GUID16" && strings.EqualFold(hook.Fields[i].Hex, hookGUID) {
				return hook
			}
		}
	}
	return nil
}

// buildHookTreeByGUID 返回指定钩实例的物品树（裸钩形状）。
// 多钩钓组按钩位逐枚挂鱼时用，与 buildMountedHookTree 取“线末端钩”不同。
func (s *Server) buildHookTreeByGUID(ctx context.Context, hookGUID string) *proto.Value {
	if s == nil || s.DB == nil || hookGUID == "" {
		return nil
	}
	row, err := s.DB.GetItemByGUID(ctx, hookGUID)
	if err != nil || row == nil {
		return nil
	}
	tree, terr := s.ItemToTree(ctx, row)
	if terr != nil {
		return nil
	}
	return tree
}

// ---- 新玩家初始化 ----

// EnsurePlayer 为首次登录的玩家创建档案、初始物品和快捷栏。
// starter_items.json 只保存物品 ID 和数量；实例 GUID、属性和时间在运行时生成。
func (s *Server) EnsurePlayer(ctx context.Context, uid int64) error {
	if s.DB == nil {
		return nil
	}
	if _, err := s.DB.EnsureProfile(ctx, uid); err != nil {
		return err
	}
	if _, _, err := s.removeItemsAndRepairRigs(ctx, uid, nil); err != nil {
		return fmt.Errorf("修复物品装配引用: %w", err)
	}
	var parsed struct {
		Items []struct {
			ItemID string `json:"item_id"`
			Qty    int64  `json:"qty"`
		} `json:"items"`
		Shortcuts []struct {
			Slot   int64  `json:"slot"`
			ItemID string `json:"item_id"`
		} `json:"shortcuts"`
	}
	if err := json.Unmarshal(starterItemsJSON, &parsed); err != nil {
		return fmt.Errorf("starter_items.json 解析: %w", err)
	}
	if len(parsed.Items) == 0 {
		return fmt.Errorf("starter_items.json 不含物品")
	}

	items := make([]*store.Item, 0, len(parsed.Items))
	firstByItem := make(map[string]string, len(parsed.Items))
	for i, si := range parsed.Items {
		if si.ItemID == "" {
			return fmt.Errorf("starter 第 %d 项缺少 item_id", i)
		}
		if si.Qty < 0 {
			return fmt.Errorf("starter %s 数量非法: %d", si.ItemID, si.Qty)
		}
		it, err := s.prepareNewItem(ctx, uid, si.ItemID, "backpack", 1, "")
		if err != nil {
			return fmt.Errorf("构造 starter %s: %w", si.ItemID, err)
		}
		it.Slot = int64(i)
		if si.Qty > 0 {
			fields, err := FieldsFromJSON(it.FieldsJSON)
			if err != nil {
				return fmt.Errorf("解析 starter %s 字段: %w", si.ItemID, err)
			}
			if !setItemQty(it.WireClass, fields, uint64(si.Qty)) {
				return fmt.Errorf("starter %s 不支持数量 %d", si.ItemID, si.Qty)
			}
			fj, err := FieldsToJSON(fields)
			if err != nil {
				return fmt.Errorf("编码 starter %s 字段: %w", si.ItemID, err)
			}
			it.FieldsJSON = fj
			it.Qty = si.Qty
		}
		items = append(items, it)
		if _, ok := firstByItem[si.ItemID]; !ok {
			firstByItem[si.ItemID] = it.GUID
		}
	}

	shortcuts := make(map[int64]string, len(parsed.Shortcuts))
	for _, sc := range parsed.Shortcuts {
		guid := firstByItem[sc.ItemID]
		if guid == "" {
			return fmt.Errorf("starter 快捷栏引用不存在: slot=%d item=%s", sc.Slot, sc.ItemID)
		}
		shortcuts[sc.Slot] = guid
	}
	seeded, err := s.DB.SeedStarterBundle(ctx, uid, items, shortcuts)
	if err != nil {
		return fmt.Errorf("播种 starter: %w", err)
	}
	if !seeded {
		return nil
	}
	s.log.Info("新玩家播种完成", "uid", uid, "items", len(items))
	return nil
}

// ensureRig 幂等建立钓组索引，并返回是否新写入竿实例尖梢。
// 顺序为建档、补齐尖梢、矫正非法钓组类型；尖梢必须先写，避免后续字段更新覆盖结果。
// 调用方在尖梢新写入后应推送 4/22，让客户端在抛竿校验前拿到完整物品。
func (s *Server) ensureRig(ctx context.Context, uid int64, gearGUID string) bool {
	if s.DB == nil || gearGUID == "" {
		return false
	}
	it, err := s.DB.GetItemByGUID(ctx, gearGUID)
	if err != nil || it == nil {
		return false
	}
	// 确保竿实例存在 153 钓组对象；缺失时构造默认值。
	fields, err := FieldsFromJSON(it.FieldsJSON)
	if err != nil || len(fields) <= 5 {
		return false
	}
	if fields[5].Val == nil || fields[5].Val.Kind == proto.KindNull {
		fields[5].Val = s.defaultRig153(ctx, it)
		fj, _ := FieldsToJSON(fields)
		_ = s.DB.UpdateItemFields(ctx, it.GUID, fj, it.Qty, it.Durability, it.Workmanship, it.RigGUID)
	}
	// 钓组类型以实例 f5.f1 为准；缺失时按竿型派生。
	rigType := s.rigTypeOfItem(ctx, it)
	_ = s.DB.UpsertRig(ctx, gearGUID, uid, rigType, "", "{}")
	// 组件登记（竿 f5 内的 152 列表）
	if fields[5].Val != nil && fields[5].Val.FieldCount() > 2 {
		if comps := fields[5].Val.Field(2).Val; comps != nil {
			for _, c := range comps.Items {
				// 152: f0=槽位品类 f1=组件guid f2=规范索引
				f0, f1, f2 := c.Field(0), c.Field(1), c.Field(2)
				slot := f0.U32()
				g := f1.GuidHex()
				extra := f2.U32()
				_ = s.DB.UpsertRigComponent(ctx, gearGUID, int64(slot), g, int64(extra))
			}
		}
	}
	// 所有竿都要检查尖梢；f8 为空时客户端会拒绝抛竿。
	// 尖梢必须在钓组类型矫正前写入，避免已有字段快照覆盖矫正结果。
	tipRepaired := s.ensureRodTip(ctx, it)
	// 钓组类型与竿型或已装组件不兼容时进行矫正。
	s.correctStaleRigType(ctx, gearGUID)
	return tipRepaired
}

// sortStringsBy 按升序排序字符串集合。
var _ = sort.Strings
var _ = sync.Mutex{}
