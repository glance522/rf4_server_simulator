package gameserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

type craftedMaterial struct {
	row *store.Item
	def *store.ItemDef
}

type craftedLeaderMaterials struct {
	line      craftedMaterial
	connector craftedMaterial
}

var leaderWireMatchAttrs = []string{
	"kngaaefkpka", // 最大拉力
	"ciichedlnjh", // 线径
	"aomkdnjcgji", // 延展/材质参数
	"lgjeahdobch", // 线材等级参数
}

func (s *Server) leaderWireCatalog(ctx context.Context) ([]*store.ItemDef, error) {
	s.leaderWireMu.Lock()
	defer s.leaderWireMu.Unlock()
	if s.leaderWireLoaded {
		return s.leaderWireDefs, s.leaderWireErr
	}
	defs, err := s.DB.ListItemDefsByCategory(ctx, gearCatLeaderWire)
	s.leaderWireLoaded = err == nil
	s.leaderWireDefs = defs
	s.leaderWireErr = err
	return defs, err
}

// leaderWireDefForLine 找到普通主线对应的前导线定义。
// 客户端把前导内线渲染成 haifiodldde 模型，普通主线模型是
// hfblbgmohio；只改协议外壳而保留普通主线的 numeric_id 会在
// 制作结果页触发 InvalidCastException。
func (s *Server) leaderWireDefForLine(ctx context.Context, lineDef *store.ItemDef) (*store.ItemDef, error) {
	if lineDef == nil {
		return nil, errors.New("缺少主线定义")
	}
	if lineDef.Category == gearCatLeaderWire {
		return lineDef, nil
	}
	if lineDef.Category != gearCatLine {
		return nil, fmt.Errorf("线材品类=%d，不是普通主线或前导线", lineDef.Category)
	}

	target := defAttrs(lineDef)
	for _, key := range leaderWireMatchAttrs {
		if _, ok := target[key]; !ok {
			return nil, fmt.Errorf("主线 %s 缺少匹配参数 %s", lineDef.ItemID, key)
		}
	}
	candidates, err := s.leaderWireCatalog(ctx)
	if err != nil {
		return nil, err
	}

	var best *store.ItemDef
	for _, candidate := range candidates {
		attrs := defAttrs(candidate)
		matched := true
		for _, key := range leaderWireMatchAttrs {
			if target[key] != attrs[key] {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		if best == nil ||
			(candidate.Kind == lineDef.Kind && best.Kind != lineDef.Kind) ||
			(candidate.Kind == lineDef.Kind && best.Kind == lineDef.Kind && candidate.ItemID < best.ItemID) {
			best = candidate
		}
	}
	if best == nil {
		return nil, fmt.Errorf("主线 %s 没有参数一致的前导线定义", lineDef.ItemID)
	}
	return best, nil
}

// consumeLeaderCraftMaterialsTx 按配方需求扣除真实材料，并保留线材和连接件
// 的实例定义。客户端提交的临时 GUID 找不到时必须回退到请求里的需求 ID，
// 不能把任意背包物品当材料，也不能只扣一份而忽略 demand 数量。
func (s *Server) consumeLeaderCraftMaterialsTx(
	ctx context.Context,
	tx *sql.Tx,
	uid int64,
	slots []craftSlot,
	demands []craftDemand,
	times int64,
) ([]string, craftedLeaderMaterials, error) {
	var selected craftedLeaderMaterials
	rows, err := listItemsTx(ctx, tx, uid, []string{"backpack", "rig"})
	if err != nil {
		return nil, selected, err
	}
	if times < 1 {
		times = 1
	}

	byGUID := make(map[string]*store.Item, len(rows))
	for _, row := range rows {
		byGUID[row.GUID] = row
	}
	preferred := make(map[string][]string)
	for _, slot := range slots {
		if slot.GUID == "" {
			continue
		}
		preferred[slot.ItemID] = append(preferred[slot.ItemID], slot.GUID)
	}

	consumed := make(map[string]bool)
	var touched []string
	for _, demand := range demands {
		need := demand.need() * times
		if need <= 0 {
			continue
		}
		var pick *craftedMaterial

		tryRow := func(row *store.Item) (bool, error) {
			if row == nil || row.Qty <= 0 || consumed[row.GUID] {
				return false, nil
			}
			match, err := s.craftDemandMatchesTx(ctx, tx, demand, row)
			if err != nil || !match {
				return false, err
			}
			if pick == nil {
				def, err := itemDefByItemIDTx(ctx, tx, row.ItemID)
				if err != nil || def == nil {
					return false, fmt.Errorf("读取材料定义失败: %s", row.ItemID)
				}
				pick = &craftedMaterial{row: row, def: def}
			} else if demand.DemandID == "craft_mat_line" && pick.row.ItemID != row.ItemID {
				return false, fmt.Errorf("同一条引线不能混用不同线材: %s / %s", pick.row.ItemID, row.ItemID)
			}

			have := stackCountTx(ctx, tx, row)
			if have < 1 {
				have = 1
			}
			take := need
			if have < take {
				take = have
			}
			got, err := consumeItemQtyTx(ctx, tx, row, take)
			if err != nil {
				return false, err
			}
			if got > 0 && !consumed[row.GUID] {
				consumed[row.GUID] = true
				touched = append(touched, row.GUID)
			}
			need -= got
			return got > 0, nil
		}

		// 客户端给出的真实实例 GUID 优先，但必须通过需求类目校验。
		for _, guid := range preferred[demand.DemandID] {
			if need <= 0 {
				break
			}
			if _, err := tryRow(byGUID[guid]); err != nil {
				return nil, selected, err
			}
		}
		// 临时 GUID 或指定实例数量不足时，按需求类目从真实背包继续补扣。
		for _, row := range rows {
			if need <= 0 {
				break
			}
			if _, err := tryRow(row); err != nil {
				return nil, selected, err
			}
		}
		if need > 0 {
			return nil, selected, fmt.Errorf("%s 还缺 %d 份", demand.DemandID, need)
		}
		if pick == nil {
			continue
		}
		switch demand.DemandID {
		case "craft_mat_line":
			selected.line = *pick
		case "craft_mat_swivels":
			selected.connector = *pick
		}
	}
	if selected.line.row == nil {
		return nil, selected, errors.New("配方没有扣除到线材")
	}
	return touched, selected, nil
}

func (s *Server) craftDemandMatchesTx(
	ctx context.Context,
	tx *sql.Tx,
	demand craftDemand,
	row *store.Item,
) (bool, error) {
	for _, alt := range demand.Alternatives {
		if alt != "" && alt == row.ItemID {
			return true, nil
		}
	}
	if len(demand.Categories) == 0 {
		return false, nil
	}
	realCat, ok := effectiveCategoryTx(ctx, tx, row.ItemID)
	if !ok {
		return false, nil
	}
	for _, cat := range demand.Categories {
		if cat == realCat {
			return true, nil
		}
	}
	return false, nil
}

// materializeCraftedLeaderTx 将真实线材与连接件写入成品引线的嵌套配置。
// 根物品 ID 保持配方指定的引线家族/长度；子物品使用真实材料 numeric_id，
// 因此模型引用与线径/拉力都不再落到固定模板。
func (s *Server) materializeCraftedLeaderTx(
	ctx context.Context,
	tx *sql.Tx,
	row *store.Item,
	materials craftedLeaderMaterials,
) error {
	if row == nil || materials.line.row == nil || materials.line.def == nil {
		return errors.New("引线缺少线材")
	}
	wireDef, err := s.leaderWireDefForLine(ctx, materials.line.def)
	if err != nil {
		return err
	}
	materials.line.def = wireDef
	def, err := itemDefByItemIDTx(ctx, tx, row.ItemID)
	if err != nil || def == nil {
		return fmt.Errorf("引线成品定义不存在: %s", row.ItemID)
	}
	leaderLengthCM := int64(math.Round(itemDefF32(def, gearAttrLength)))

	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return err
	}
	setSub := func(cs string, sub *proto.Value) {
		idx := fieldIdxByCS(row.WireClass, cs)
		if idx >= 0 && idx < len(fields) && sub != nil {
			fields[idx].Val = sub
		}
	}
	lineObj, err := s.craftedMaterialObject(row, materials.line, gearAttrLeaderIn, leaderLengthCM)
	if err != nil {
		return err
	}
	var connectorObj *proto.Value
	if materials.connector.row != nil && materials.connector.def != nil {
		connectorObj, err = s.craftedMaterialObject(row, materials.connector, "lbadjgllglo", 0)
		if err != nil {
			return err
		}
	}
	setSub(gearAttrLeaderIn, lineObj)
	setSub("lbadjgllglo", connectorObj)

	lineStrength := applyCraftedMaterialState(lineObj, materials.line, leaderLengthCM)
	connectorStrength := applyCraftedMaterialState(connectorObj, materials.connector, 0)
	strength := lineStrength
	if connectorStrength > 0 && (strength <= 0 || connectorStrength < strength) {
		strength = connectorStrength
	}
	if strength <= 0 {
		return errors.New("真实材料的强度都为 0")
	}
	if !setWireFloat(fields, row.WireClass, fieldIdxByCS(row.WireClass, csStrength), strength) {
		return errors.New("成品引线没有可写入的强度字段")
	}

	fj, err := FieldsToJSON(fields)
	if err != nil {
		return err
	}
	row.FieldsJSON = fj
	return nil
}

func (s *Server) craftedMaterialObject(
	host *store.Item,
	material craftedMaterial,
	key string,
	lengthCM int64,
) (*proto.Value, error) {
	if host == nil || material.row == nil || material.def == nil {
		return nil, errors.New("空材料")
	}
	wireCategory := material.def.Category
	if key == gearAttrLeaderIn {
		// f1 的协议类型固定为内嵌前导线(cat15)。原材料即使来自主线(cat11)，
		// 也只能搬运其 numeric_id、强度和线径，不能沿用主线对象类。
		wireCategory = gearCatLeaderWire
	}
	cls, ok := categoryWireClass[wireCategory]
	if !ok {
		cls = wireClassEntry{fallbackWireClass, fallbackTypeID}
	}
	if wiregen.ClassOps[cls.Class] == nil {
		return nil, fmt.Errorf("材料类没有 wire reader: %s", material.def.ItemID)
	}
	obj, err := wiregen.MakeObj(cls.Class, nil)
	if err != nil {
		return nil, err
	}
	s.applyDefAttrs(material.def, cls.Class, obj)
	if idx := fieldIdxByCS(cls.Class, csGUID); idx >= 0 {
		obj.Fields[idx].Hex = uuidLike(host.GUID, key)
	}
	if idx := fieldIdxByCS(cls.Class, csNumericID); idx >= 0 {
		obj.Fields[idx].Num = ptrU64(uint64(material.def.NumericID))
	}
	if idx := fieldIdxByCS(cls.Class, csParent); idx >= 0 {
		obj.Fields[idx].Hex = host.GUID
	}
	if idx := fieldIdxByCS(cls.Class, csSlotEnum); idx >= 0 {
		obj.Fields[idx].Num = ptrU64(4)
	}
	if idx := fieldIdxByCS(cls.Class, csStateEnum); idx >= 0 {
		state := defAttrU64OrZero(material.def, "hgaijieklek")
		obj.Fields[idx].Num = ptrU64(state | subMountFlag)
	}
	applyCraftedMaterialState(obj, material, lengthCM)
	return obj, nil
}

func applyCraftedMaterialState(obj *proto.Value, material craftedMaterial, lengthCM int64) float64 {
	if obj == nil || material.row == nil || material.def == nil {
		return 0
	}
	setWireFloat(obj.Fields, obj.Class, fieldIdxByCS(obj.Class, csWorkmanship), material.row.Workmanship)
	setWireFloat(obj.Fields, obj.Class, fieldIdxByCS(obj.Class, csDurability), material.row.Durability)
	if lengthCM > 0 {
		if idx := fieldIdxByCS(obj.Class, gearAttrLineLen); idx >= 0 && idx < len(obj.Fields) {
			obj.Fields[idx].Num = ptrU64(uint64(lengthCM))
		}
	}
	strength := itemDefF32(material.def, gearAttrMaxLoad) * material.row.Durability * material.row.Workmanship
	if strength > 0 {
		setWireFloat(obj.Fields, obj.Class, fieldIdxByCS(obj.Class, csStrength), strength)
	}
	return strength
}

// repairCraftedLeaderWire 修复旧版本自制引线中内嵌的普通主线对象。
// 返回 invalid 表示该物品没有可映射的前导线定义，必须从背包隔离。
func (s *Server) repairCraftedLeaderWire(
	ctx context.Context,
	it *store.Item,
	fields []proto.Field,
) (changed bool, invalid bool, err error) {
	if it == nil || it.WireClass != "oldfpclaabo" || !strings.HasPrefix(it.ItemID, "leader_crafted_") {
		return false, false, nil
	}
	lineIdx := fieldIdxByCS(it.WireClass, gearAttrLeaderIn)
	if lineIdx < 0 || lineIdx >= len(fields) {
		return false, true, nil
	}
	lineObj := fields[lineIdx].Val
	if lineObj == nil || lineObj.Kind != proto.KindObj {
		return false, true, nil
	}
	numericIdx := fieldIdxByCS(lineObj.Class, csNumericID)
	if numericIdx < 0 || numericIdx >= len(lineObj.Fields) || lineObj.Fields[numericIdx].Num == nil {
		return false, true, nil
	}
	lineDef, err := s.DB.GetItemDef(ctx, int64(u64Of(lineObj.Fields[numericIdx].Num)))
	if err != nil {
		return false, true, err
	}
	if lineDef == nil {
		return false, true, fmt.Errorf("内线定义不存在")
	}
	if lineDef.Category == gearCatLeaderWire {
		return false, false, nil
	}
	wireDef, err := s.leaderWireDefForLine(ctx, lineDef)
	if err != nil {
		return false, true, nil
	}

	readFloat := func(obj *proto.Value, cs string) float64 {
		if obj == nil {
			return 0
		}
		idx := fieldIdxByCS(obj.Class, cs)
		if idx < 0 || idx >= len(obj.Fields) || obj.Fields[idx].Num == nil {
			return 0
		}
		return float64(proto.F32FromBits(uint32(u64Of(obj.Fields[idx].Num))))
	}
	childGUID := ""
	if idx := fieldIdxByCS(lineObj.Class, csGUID); idx >= 0 && idx < len(lineObj.Fields) {
		childGUID = lineObj.Fields[idx].Hex
	}
	material := craftedMaterial{
		row: &store.Item{
			GUID:        childGUID,
			Workmanship: readFloat(lineObj, csWorkmanship),
			Durability:  readFloat(lineObj, csDurability),
		},
		def: wireDef,
	}
	if material.row.Workmanship <= 0 {
		material.row.Workmanship = it.Workmanship
	}
	if material.row.Durability <= 0 {
		material.row.Durability = it.Durability
	}

	rootDef, err := s.DB.GetItemDef(ctx, it.NumericID)
	if err != nil {
		return false, false, err
	}
	lengthCM := int64(0)
	if rootDef != nil {
		lengthCM = int64(math.Round(itemDefF32(rootDef, gearAttrLength)))
	}
	if lengthCM <= 0 {
		if idx := fieldIdxByCS(lineObj.Class, gearAttrLineLen); idx >= 0 && idx < len(lineObj.Fields) &&
			lineObj.Fields[idx].Num != nil {
			lengthCM = int64(u64Of(lineObj.Fields[idx].Num))
		}
	}
	replacement, err := s.craftedMaterialObject(it, material, gearAttrLeaderIn, lengthCM)
	if err != nil {
		return false, false, err
	}
	lineStrength := applyCraftedMaterialState(replacement, material, lengthCM)
	fields[lineIdx].Val = replacement

	strength := lineStrength
	connectorIdx := fieldIdxByCS(it.WireClass, "lbadjgllglo")
	if connectorIdx >= 0 && connectorIdx < len(fields) {
		connector := fields[connectorIdx].Val
		if connector != nil && connector.Kind == proto.KindObj {
			numericIdx := fieldIdxByCS(connector.Class, csNumericID)
			if numericIdx >= 0 && numericIdx < len(connector.Fields) && connector.Fields[numericIdx].Num != nil {
				connectorDef, derr := s.DB.GetItemDef(ctx, int64(u64Of(connector.Fields[numericIdx].Num)))
				if derr == nil && connectorDef != nil {
					value := itemDefF32(connectorDef, gearAttrMaxLoad) *
						readFloat(connector, csDurability) * readFloat(connector, csWorkmanship)
					if value > 0 && (strength <= 0 || value < strength) {
						strength = value
					}
				}
			}
		}
	}
	if strength > 0 {
		setWireFloat(fields, it.WireClass, fieldIdxByCS(it.WireClass, csStrength), strength)
	}
	return true, false, nil
}

func defAttrU64OrZero(def *store.ItemDef, key string) uint64 {
	value, _ := defAttrU64(def, key)
	return value
}
