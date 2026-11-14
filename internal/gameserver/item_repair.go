package gameserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"rf4server/internal/proto"
)

// itemPhysicsRepairKey 记录最近一次物理字段修复的版本与统计。
// 每次启动仍会重新扫描，以修复后续导入或回档产生的异常数据。
const itemPhysicsRepairKey = "repair:item_physics:v5"
const itemPhysicsRepairVersion = "11"

const craftedLeaderQuarantineContainer = "craft_quarantine"

// reelFrictionZeroMigrationKey 标记"存量零值制动力"迁移只执行一次。
const reelFrictionZeroMigrationKey = "migration:reel_friction_zero:v1"

// lureHookSlotsMigrationKey 标记"存量拟饵钩槽数组"迁移只执行一次。
// v5：转向官方短缩语义后的存量清理——剥离 v3~v7 实验期间写入的垫底
// 钩（:r 命名空间）与幻影空槽（:e 命名空间）元素，恢复真实占用状态。
const lureHookSlotsMigrationKey = "migration:lure_hook_slots:v5"

// repairLureHookSlots 清理拟饵钩槽数组的历史实验残留。
//
// v3~v7 期间卸钩曾回填「恢复版出厂钩」（lureRestoredHookKey）或「幻影
// 空槽钩」（lurePhantomHookKey）以保数组长度——两者都没有对应物品行，
// 剥掉即恢复该槽真实状态。官方短缩语义下空数组/短数组是合法状态
// （Length==0 时客户端物品状态行报「钩未安装」），不做任何补齐。
// 历史 null/elem_null 损坏形态已由 v1~v4 迁移清干净，此处不再处理。
func (s *Server) repairLureHookSlots(ctx context.Context) (int, error) {
	_, done, err := s.DB.KVGet(ctx, lureHookSlotsMigrationKey)
	if err == nil && done {
		return 0, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("读取拟饵钩槽迁移标记: %w", err)
	}
	rows, err := s.DB.SQL.QueryContext(ctx, `SELECT guid FROM item WHERE wire_class=?`, lureWireClass)
	if err != nil {
		return 0, err
	}
	var guids []string
	for rows.Next() {
		var guid string
		if err := rows.Scan(&guid); err != nil {
			_ = rows.Close()
			return 0, err
		}
		guids = append(guids, guid)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	fixed := 0
	for _, guid := range guids {
		row, rerr := s.DB.GetItemByGUID(ctx, guid)
		if rerr != nil || row == nil {
			continue
		}
		fields, ferr := FieldsFromJSON(row.FieldsJSON)
		if ferr != nil || len(fields) == 0 {
			continue
		}
		idx := lureHookFieldIndex(row.WireClass)
		if idx < 0 || idx >= len(fields) {
			continue
		}
		list := fields[idx].Val
		if list == nil || list.Kind != proto.KindArgs || len(list.Items) == 0 {
			continue
		}
		items := list.Items
		stripped := make([]*proto.Value, 0, len(items))
		keptOld := make([]int, 0, len(items))
		dropped := 0
		for i, it := range items {
			// 与下标无关的全空间匹配（PR#339 评审 P2：短缩后下标漂移）。
			if isLureLegacyArtifactGUID(guid, lureHookItemGUID(items, i)) {
				dropped++
				continue
			}
			stripped = append(stripped, it)
			keptOld = append(keptOld, i)
		}
		if dropped == 0 {
			continue
		}
		list.Items = stripped
		fields[idx] = proto.Field{Op: "VAL", Val: list}
		// 活动钩下标按剥离映射随迁（PR#339 评审 P2）：垫底钩被剥掉时，
		// 仍存在的活动钩不得被机械切回第一颗。
		applyLureActiveOrdinal(row.WireClass, fields,
			lureActiveOrdinalAfterStrip(lureActiveOrdinal(fields, len(items)), keptOld))
		bumpCreatedTicks(fields, row.WireClass)
		fj, jerr := FieldsToJSON(fields)
		if jerr != nil {
			continue
		}
		if uerr := s.DB.UpdateItemFields(ctx, row.GUID, fj, row.Qty, row.Durability, row.Workmanship, row.RigGUID); uerr != nil {
			return fixed, uerr
		}
		fixed++
		s.log.Info("拟饵钩槽数组残留已剥离", "guid", guid, "item", row.ItemID, "dropped", dropped, "kept", len(stripped))
	}
	if err := s.DB.KVSet(ctx, lureHookSlotsMigrationKey, "done"); err != nil {
		return fixed, fmt.Errorf("写入拟饵钩槽迁移标记: %w", err)
	}
	return fixed, nil
}

// migrateLegacyZeroDragForce 处理升级前的存量零值摩擦片。
//
// 旧实现把 csDragForce=0 当成"未初始化"，每次打滑都从零件定义重算，因此
// 升级前存档里留下来的 0 并不代表磨穿；若不处理，新语义会让这些轮子一上线
// 就泄力失效。这里统一补回额定值。迁移完成后 0 是权威的"已磨穿"状态，
// 必须靠 KV 标记保证不再重复执行，否则磨穿的轮子会被复活。
func (s *Server) migrateLegacyZeroDragForce(ctx context.Context) (int, error) {
	_, done, err := s.DB.KVGet(ctx, reelFrictionZeroMigrationKey)
	if err == nil && done {
		return 0, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("读取摩擦片迁移标记: %w", err)
	}
	rows, err := s.DB.SQL.QueryContext(ctx, `SELECT guid FROM item WHERE wire_class=?`, "dgjinoebdop")
	if err != nil {
		return 0, err
	}
	var guids []string
	for rows.Next() {
		var guid string
		if err := rows.Scan(&guid); err != nil {
			_ = rows.Close()
			return 0, err
		}
		guids = append(guids, guid)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	fixed := 0
	for _, guid := range guids {
		row, rerr := s.DB.GetItemByGUID(ctx, guid)
		if rerr != nil || row == nil {
			continue
		}
		fields, ferr := FieldsFromJSON(row.FieldsJSON)
		if ferr != nil {
			continue
		}
		child, def, _, ok := s.selectedReelPart(ctx, fields, 9)
		if !ok || child == nil || def == nil {
			continue
		}
		cur, present, idx := frictionWasherDrag(child)
		if !present || cur != 0 {
			continue
		}
		want := itemDefF32(def, gearAttrDragMax) * row.Workmanship
		if want <= 0 || idx < 0 || idx >= len(child.Fields) {
			continue
		}
		if !setWireFloat(child.Fields, child.Class, idx, want) {
			continue
		}
		bumpCreatedTicks(fields, row.WireClass)
		fj, jerr := FieldsToJSON(fields)
		if jerr != nil {
			continue
		}
		if uerr := s.DB.UpdateItemFields(ctx, row.GUID, fj, row.Qty, row.Durability, row.Workmanship, row.RigGUID); uerr != nil {
			return fixed, uerr
		}
		row.FieldsJSON = fj
		if _, perr := s.syncReelPerformance(ctx, row); perr != nil {
			return fixed, perr
		}
		fixed++
	}
	if err := s.DB.KVSet(ctx, reelFrictionZeroMigrationKey, "done"); err != nil {
		return fixed, fmt.Errorf("写入摩擦片迁移标记: %w", err)
	}
	return fixed, nil
}

// RepairItemPhysics 修复已落库物品的物理字段快照：
//   - 把误落成钓线类的工具、材料和铃铛改写成自己的实例类；
//   - 重新物化缺失的渔轮子件；
//   - 补全线、钩、竿、轮的顶层强度；
//   - 回填零值状态位和容器枚举；
//   - 修正渔轮刹车片成色，只补齐缺失的曳力字段（已有磨损值不再覆盖）；
//   - 将竿的 f5 类型限制在竿型允许范围内；
//   - 修复旧自制引线的内嵌线材槽位，并把旧减震引线迁到 f3。
//
// 兼容性只用于装配推荐，不覆盖玩家显式选择。修复写回 fields_json，
// 并通过 ensureRig 同步 rig/rig_component。成功后写版本和统计标记；
// 失败时保留上一次成功标记。
func (s *Server) RepairItemPhysics(ctx context.Context) error {
	if s.DB == nil {
		return fmt.Errorf("物品物理状态修复：数据库未连接")
	}
	// 必须先于常规扫描：常规扫描只会补齐缺失字段，不会改写已存在的 0。
	if _, err := s.migrateLegacyZeroDragForce(ctx); err != nil {
		return fmt.Errorf("物品物理状态修复：摩擦片存量零值迁移: %w", err)
	}
	// 旧拆钩逻辑缩短数组的存量损坏：常规扫描的拟饵投影只处理 f0 为
	// nil/null 的实例，空数组/长度不足只有这里能修。
	if fixed, err := s.repairLureHookSlots(ctx); err != nil {
		return fmt.Errorf("物品物理状态修复：拟饵钩槽迁移: %w", err)
	} else if fixed > 0 {
		s.log.Info("拟饵钩槽迁移完成", "lures_fixed", fixed)
	}

	// 梦鱼优惠券存量补偿：数量塌陷的券行按发放审计 + 制作流水补回余量。
	// 放在常规扫描之前——常规扫描只对齐字段结构，不会把 qty=1 抬回真实余量。
	if fixed, err := s.repairCouponGrantQuantities(ctx); err != nil {
		return fmt.Errorf("物品物理状态修复：梦鱼券存量补偿: %w", err)
	} else if fixed > 0 {
		s.log.Info("梦鱼券存量补偿完成", "coupons_fixed", fixed)
	}

	// 梦鱼券「一张一个实例」展开：券类没有客户端数量槽，一行多张显示不出张数。
	// 必须在数量补偿之后——先让总张数正确，再按张数铺成多行。
	if fixed, err := s.repairCouponRowLayout(ctx); err != nil {
		return fmt.Errorf("物品物理状态修复：梦鱼券展开: %w", err)
	} else if fixed > 0 {
		s.log.Info("梦鱼券展开完成", "players_fixed", fixed)
	}

	rows, err := s.DB.SQL.QueryContext(ctx, `SELECT guid FROM item ORDER BY guid`)
	if err != nil {
		return fmt.Errorf("物品物理状态修复：列出物品: %w", err)
	}
	var guids []string
	for rows.Next() {
		var guid string
		if err := rows.Scan(&guid); err != nil {
			_ = rows.Close()
			return fmt.Errorf("物品物理状态修复：读取 guid: %w", err)
		}
		guids = append(guids, guid)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("物品物理状态修复：关闭查询: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("物品物理状态修复：遍历物品: %w", err)
	}

	changed := 0
	rigsRepaired := 0
	quarantined := 0
	restored := 0
	for _, guid := range guids {
		it, err := s.DB.GetItemByGUID(ctx, guid)
		if err != nil {
			return fmt.Errorf("物品物理状态修复：读取 %s: %w", guid, err)
		}
		if it == nil {
			continue
		}
		if def, derr := s.DB.GetItemDef(ctx, it.NumericID); derr == nil && def != nil {
			rewritten, rerr := s.reclassifyStoredItem(ctx, it, def)
			if rerr != nil {
				return fmt.Errorf("物品物理状态修复：改写类别 %s: %w", guid, rerr)
			}
			if rewritten {
				changed++
				continue
			}
		}
		fields, err := FieldsFromJSON(it.FieldsJSON)
		if err != nil {
			return fmt.Errorf("物品物理状态修复：解析 %s: %w", guid, err)
		}
		fields = alignFieldsToOps(it, fields)
		leaderChanged, invalidLeader, lerr := s.repairCraftedLeaderWire(ctx, it, fields)
		if lerr != nil {
			return fmt.Errorf("物品物理状态修复：修复引线 %s: %w", guid, lerr)
		}
		if invalidLeader {
			if err := s.DB.UpdateItemContainer(ctx, it.GUID, craftedLeaderQuarantineContainer); err != nil {
				return fmt.Errorf("物品物理状态修复：隔离无效引线 %s: %w", guid, err)
			}
			quarantined++
			s.log.Warn("无效自制引线已隔离", "guid", guid, "item", it.ItemID)
			continue
		}
		if it.Container == craftedLeaderQuarantineContainer {
			// 旧版本把"找不到参数完全一致的前导线定义"的引线关进隔离区，
			// 玩家从此看不见自己做出来的引线。映射放宽后必须把物品放回原容器。
			restore := "backpack"
			if it.RigGUID != "" {
				restore = "rig"
			}
			if err := s.DB.UpdateItemContainer(ctx, it.GUID, restore); err != nil {
				return fmt.Errorf("物品物理状态修复：解除引线隔离 %s: %w", guid, err)
			}
			if restore == "backpack" {
				if slot, serr := s.DB.NextFreeSlot(ctx, it.UID, "backpack"); serr == nil && slot >= 0 {
					if uerr := s.DB.SetItemSlot(ctx, it.GUID, slot); uerr != nil {
						return fmt.Errorf("物品物理状态修复：分配引线背包槽位 %s: %w", guid, uerr)
					}
				}
			}
			restored++
			it.Container = restore
			s.log.Info("自制引线已解除隔离", "guid", guid, "item", it.ItemID, "container", restore)
		}
		before, _ := FieldsToJSON(fields)
		obj := proto.Obj(it.WireClass, uint32(it.TypeID), fields...)
		rowChanged := leaderChanged
		var rodCreated proto.Field
		hasRodCreated := false
		// 与 ItemToTree 的字段修复保持一致：状态零值和缺失子件必须写回数据库。
		if def, derr := s.DB.GetItemDef(ctx, it.NumericID); derr == nil && def != nil {
			stateFlagsZeroFill(it.WireClass, obj, def)
			s.materializeInto(ctx, it.WireClass, def, obj, it.GUID, it.Workmanship, it.Durability)
		}
		// 竿实例的厂件和 f5 也必须持久化，不能只在渲染时重建。
		if it.WireClass == "cnolibojkao" {
			if idx := fieldIdxByCS(it.WireClass, csCreated); idx >= 0 && idx < len(fields) {
				rodCreated = fields[idx]
				hasRodCreated = true
			}
			if err := s.rebuildRod(ctx, it, obj); err != nil {
				return fmt.Errorf("物品物理状态修复：重建鱼竿 %s: %w", guid, err)
			}
			fields = obj.Fields
			// f16 是每次下发都会刷新的动态时间戳，不参与持久化变化判断；
			// f0、f5 等字段仍通过后续 JSON 比较检测。
			if hasRodCreated {
				if idx := fieldIdxByCS(it.WireClass, csCreated); idx >= 0 && idx < len(fields) {
					fields[idx] = rodCreated
				}
			}
			// 挂饵方式可选项缺失时按钓组类型补掩码：f9=0 会让「选择安装方式」
			// 弹窗全空、鱼饵被判定为不适配（2026-11-04 实测）。
			if idx := fieldIdxByCS(it.WireClass, csRigObject); idx >= 0 && idx < len(fields) && fields[idx].Val != nil {
				if _, avail := rigBaitMountsOf(fields[idx].Val); avail == 0 {
					setRigBaitMountAvail(fields[idx].Val, baitMountAvailForRig(s.rigTypeOfItem(ctx, it), 0))
				}
			}
		}
		// 品类 46（成品窝料）存量补单件克重：老实例是照抄定义生成的，
		// 重量恒为 0，卡片永远显示「质量 0 克」。
		if def46, derr := s.DB.GetItemDef(ctx, it.NumericID); derr == nil && def46 != nil && def46.Category == 46 {
			if fillFeedUnitWeight(it.WireClass, fields, def46.ItemID) {
				rowChanged = true
			}
		}
		if s.canonicalizeItemPhysicalFields(ctx, it, fields) {
			rowChanged = true
		}
		if after, aerr := FieldsToJSON(fields); aerr == nil && after != before {
			rowChanged = true
		}
		// 计件物品以 fields_json 的 ljnnpokapig 为准，修正 item.qty。
		// 这里必须用 csQty 而不是 itemQtyFieldIdx：后者对 metaQtyFamily 类
		// （如 cat58 的 bpmmimcmpof）返回的是 dnmfciolgep 槽，那是 attrs 里的
		// 常量语义而不是「还剩多少件」，会把 item.qty 改写成该常量（实测 100）。
		if def, derr := s.DB.GetItemDef(ctx, it.NumericID); derr == nil && def != nil && countableCategories[def.Category] {
			if qi := fieldIdxByCS(it.WireClass, csQty); qi >= 0 && qi < len(fields) {
				if q := int64(u64Of(fields[qi].Num)); q > 0 && q != it.Qty {
					it.Qty = q
					rowChanged = true
				}
			}
		}
		if rowChanged {
			bumpCreatedTicks(fields, it.WireClass)
			fj, err := FieldsToJSON(fields)
			if err != nil {
				return fmt.Errorf("物品物理状态修复：编码 %s: %w", guid, err)
			}
			if err := s.DB.UpdateItemFields(ctx, it.GUID, fj, it.Qty, it.Durability, it.Workmanship, it.RigGUID); err != nil {
				return fmt.Errorf("物品物理状态修复：写回 %s: %w", guid, err)
			}
			changed++
		}
		if it.WireClass == "cnolibojkao" {
			if s.ensureRig(ctx, it.UID, it.GUID) {
				rigsRepaired++
			}
		}
	}

	marker := fmt.Sprintf("done;version=%s;scanned=%d;changed=%d;rods=%d;quarantined=%d;leaders_restored=%d",
		itemPhysicsRepairVersion, len(guids), changed, rigsRepaired, quarantined, restored)
	if err := s.DB.KVSet(ctx, itemPhysicsRepairKey, marker); err != nil {
		return fmt.Errorf("物品物理状态修复：写完成标记: %w", err)
	}
	s.log.Info("物品物理状态修复完成", "version", itemPhysicsRepairVersion,
		"items_changed", changed, "rods_repaired", rigsRepaired,
		"quarantined", quarantined, "leaders_restored", restored, "total", len(guids))
	return nil
}
