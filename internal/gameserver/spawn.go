package gameserver

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

//go:embed assets/map_defs.json
var mapDefsJSON []byte

// ---- 1/13 出生点定位 ----
//
// 响应为 206 cgbaffjpbkg{f0: 205 出生地图树, f1: 起始场景}。
// 地图树来自 map_defs.json；下发前会把逐日预报和逐小时天气计划重锚到当前时刻。
// 客户端直接按 206 对象解析响应，因此不能返回空 args。

// 游戏时间换算常量。
const (
	worldDayEpochUnix   = 1438387200 // 2015-08-01 00:00:00 UTC（游戏历元）
	gameHoursPerRealHr  = 24         // 1 真实小时 = 24 游戏小时
	realSecPerGameHour  = 150        // 150 真实秒 = 1 游戏小时
	spawnLevelKey       = "level_001_lake"
	spawnScene          = "loc1_01"
	dobClassID          = 205
	spawnLocatorClassID = 206
	forecastClass       = "bleinjfhebp" // 逐日预报，f0=日号
	hourPlanClass       = "odnlikfnhnl" // 逐小时计划，f0=日号 f1=小时
)

var (
	mapOnce sync.Once
	mapDefs map[string]json.RawMessage
	mapErr  error
)

func loadMapDefs() (map[string]json.RawMessage, error) {
	mapOnce.Do(func() {
		var raw struct {
			Maps map[string]json.RawMessage `json:"maps"`
		}
		if err := json.Unmarshal(mapDefsJSON, &raw); err != nil {
			mapErr = fmt.Errorf("map_defs.json 解析: %w", err)
			return
		}
		mapDefs = raw.Maps
	})
	return mapDefs, mapErr
}

// worldDayAndHour 当前游戏日号（自 2015-08-01 起的真实小时数）与游戏小时。
func worldDayAndHour(nowUnix int64) (day, hour int64) {
	secs := nowUnix - worldDayEpochUnix
	day = secs / 3600
	hour = (secs / realSecPerGameHour) % 24
	return
}

// irField 表示地图数据中的一个字段节点。
type irField struct {
	F   string          `json:"f"` // byte/opt/str/u32/u64/u16/guid/optguid/val/…
	V   json.RawMessage `json:"v"` // 标量原样
	Sub *irNode         `json:"-"` // val 内嵌树（物化后，供原位改写）
}

// irNode IR 树节点（obj / args / null）。
type irNode struct {
	T      string    `json:"t"`
	Class  string    `json:"class,omitempty"`
	TypeID *uint32   `json:"type_id,omitempty"`
	Code   *string   `json:"code,omitempty"`
	Fields []irField `json:"fields,omitempty"`
	Items  []irNode  `json:"items,omitempty"`
}

func (n *irNode) nullish() bool { return n == nil || n.T == "null" || (n.T == "" && n.Class == "") }

// irToValue 将地图 IR 转换为 proto.Value。
// 仅支持 map_defs 使用的节点形态；对象按类 ops 的位置编码。
func irToValue(n *irNode) (*proto.Value, error) {
	if n.nullish() {
		return &proto.Value{Kind: proto.KindNull}, nil
	}
	switch n.T {
	case "obj":
		if n.Class == "" || n.TypeID == nil {
			return nil, fmt.Errorf("obj 节点缺 class/type_id")
		}
		ops := wiregen.ClassOps[n.Class]
		if len(ops) == 0 {
			return nil, fmt.Errorf("类 %s 不在 ops 表", n.Class)
		}
		if len(n.Fields) != len(ops) {
			return nil, fmt.Errorf("类 %s 字段数 %d ≠ ops %d", n.Class, len(n.Fields), len(ops))
		}
		fields := make([]proto.Field, len(n.Fields))
		for i, f := range n.Fields {
			pf, err := irFieldToProto(f, ops[i])
			if err != nil {
				return nil, fmt.Errorf("类 %s 字段 %d: %w", n.Class, i, err)
			}
			fields[i] = pf
		}
		return proto.Obj(n.Class, *n.TypeID, fields...), nil
	case "args":
		items := make([]*proto.Value, 0, len(n.Items))
		for _, it := range n.Items {
			v, err := irToValue(&it)
			if err != nil {
				return nil, err
			}
			items = append(items, v)
		}
		return proto.Args(n.Code, items...), nil
	default:
		return nil, fmt.Errorf("未知 IR 节点 t=%q", n.T)
	}
}

func irFieldToProto(f irField, op string) (proto.Field, error) {
	pf := proto.Field{Op: op}
	switch f.F {
	case "u32", "byte", "u16", "u64", "opt":
		var v uint64
		if err := json.Unmarshal(f.V, &v); err != nil {
			return pf, fmt.Errorf("数值字段解析: %w", err)
		}
		pf.Num = ptrU64(v)
	case "str":
		var s *string
		if err := json.Unmarshal(f.V, &s); err != nil {
			return pf, fmt.Errorf("字符串字段解析: %w", err)
		}
		pf.Str = s
	case "guid":
		var g string
		if err := json.Unmarshal(f.V, &g); err != nil {
			return pf, fmt.Errorf("guid 字段解析: %w", err)
		}
		pf.Hex = g
	case "optguid":
		var g *string
		if err := json.Unmarshal(f.V, &g); err != nil {
			return pf, fmt.Errorf("optguid 字段解析: %w", err)
		}
		if g != nil {
			pf.Hex = *g
		}
	case "val":
		if f.Sub == nil {
			pf.Val = &proto.Value{Kind: proto.KindNull}
			return pf, nil
		}
		v, err := irToValue(f.Sub)
		if err != nil {
			return pf, err
		}
		pf.Val = v
	default:
		return pf, fmt.Errorf("暂不支持的 IR 字段类型 %q", f.F)
	}
	return pf, nil
}

// materializeIR 将字段 val 的内嵌 JSON 树展开为 Sub 指针。
// 后续遍历和重锚都修改同一份内存，避免惰性解析产生副本而丢失改写。
func materializeIR(n *irNode) error {
	if n == nil {
		return nil
	}
	for i := range n.Fields {
		if n.Fields[i].F != "val" || n.Fields[i].Sub != nil {
			continue
		}
		var sub *irNode
		if err := json.Unmarshal(n.Fields[i].V, &sub); err != nil {
			return fmt.Errorf("val 内嵌树解析: %w", err)
		}
		n.Fields[i].Sub = sub
		n.Fields[i].V = nil
		if err := materializeIR(sub); err != nil {
			return err
		}
	}
	for i := range n.Items {
		if err := materializeIR(&n.Items[i]); err != nil {
			return err
		}
	}
	return nil
}

// irWalkObj 深度遍历 IR 树（含字段 val 内嵌树）。
func irWalkObj(n *irNode, fn func(*irNode)) {
	if n == nil {
		return
	}
	fn(n)
	for i := range n.Fields {
		if n.Fields[i].Sub != nil {
			irWalkObj(n.Fields[i].Sub, fn)
		}
	}
	for i := range n.Items {
		irWalkObj(&n.Items[i], fn)
	}
}

// irFieldInt 取 obj 节点第 idx 个整数字段值（非整数返回 false）。
func irFieldInt(n *irNode, idx int) (int64, bool) {
	if n == nil || idx >= len(n.Fields) {
		return 0, false
	}
	var v int64
	if json.Unmarshal(n.Fields[idx].V, &v) != nil {
		return 0, false
	}
	return v, true
}

// buildSpawnLocatorBody 1/13 响应体：206 cgbaffjpbkg{dob, scene}。
// levelKey 是玩家上次所在地图；scene 为空时按地图选择已知默认场景。
func (s *Server) buildSpawnLocatorBody(levelKey, scene string, nowUnix int64) ([]byte, error) {
	levelKey = travelLevelKey(levelKey)
	if scene == "" {
		scene = sceneForLevel(levelKey)
	}
	maps, err := loadMapDefs()
	if err != nil {
		return nil, err
	}
	raw, ok := maps[levelKey]
	if !ok {
		return nil, fmt.Errorf("地图表缺 %s", levelKey)
	}
	var dob irNode
	if err := json.Unmarshal(raw, &dob); err != nil {
		return nil, fmt.Errorf("dob 解析: %w", err)
	}
	if err := materializeIR(&dob); err != nil {
		return nil, err
	}
	reanchorWeather(&dob, nowUnix)
	dobVal, err := irToValue(&dob)
	if err != nil {
		return nil, err
	}
	loc := proto.Obj("cgbaffjpbkg", spawnLocatorClassID,
		proto.Field{Op: "VAL", Val: dobVal},
		proto.Field{Op: "STR", Str: &scene},
	)
	return proto.EncodeValue(loc, s.Table())
}

// buildMapDobBody 1/12 place：指定地图 key 的 dob(205 dobfogbcggd)。
func (s *Server) buildMapDobBody(key string, nowUnix int64) ([]byte, error) {
	maps, err := loadMapDefs()
	if err != nil {
		return nil, err
	}
	raw, ok := maps[key]
	if !ok {
		return nil, fmt.Errorf("地图表缺 %s", key)
	}
	var dob irNode
	if err := json.Unmarshal(raw, &dob); err != nil {
		return nil, fmt.Errorf("dob 解析: %w", err)
	}
	if err := materializeIR(&dob); err != nil {
		return nil, err
	}
	reanchorWeather(&dob, nowUnix)
	dobVal, err := irToValue(&dob)
	if err != nil {
		return nil, err
	}
	return proto.EncodeValue(dobVal, s.Table())
}

// buildTravelBody 构造 1/14 旅行响应：
// vallist[205 地图树, 800 护网快照, 307 玩家档案, null]。
// 外层容器是 vallist，不是 args。
func (s *Server) buildTravelBody(sess *Session, key string) ([]byte, error) {
	return s.buildTravelBodyAt(sess, key, time.Now().Unix())
}

func (s *Server) buildTravelBodyAt(sess *Session, key string, nowUnix int64) ([]byte, error) {
	dobBody, err := s.buildMapDobBody(key, nowUnix)
	if err != nil {
		return nil, err
	}
	dobVal, _, err := proto.DecodeValue(dobBody, 0, s.Table())
	if err != nil {
		return nil, fmt.Errorf("dob 解码: %w", err)
	}
	// 护网快照 800：args(61)[护网鱼...] + null + null
	ctx := sess.pushCtx()
	list, err := s.DB.KeepnetList(ctx, sess.Userid)
	if err != nil {
		list = nil
	}
	kn := proto.Args(proto.S("61"))
	for _, e := range list {
		kn.Items = append(kn.Items, buildKeepnet25(e, key))
	}
	snap := proto.Obj("dlbahppbdad", 800,
		proto.Field{Op: "VAL", Val: kn},
		proto.Field{Op: "VAL", Val: nullVal()},
		proto.Field{Op: "VAL", Val: nullVal()},
	)
	prof, err := s.BuildProfile307(ctx, sess.Userid)
	if err != nil {
		return nil, fmt.Errorf("profile 307: %w", err)
	}
	vl := &proto.Value{Kind: proto.KindValList, Items: []*proto.Value{
		dobVal, snap, prof, nullVal(),
	}}
	return proto.EncodeValue(vl, s.Table())
}

// ---- 地图解锁 ----
//
// 3/37 使用金币买断地点并写入 place_access；档案 307.f22 暴露已买断列表。
// 价格必须与地点静态表一致，避免客户端改价。
// 1/14 只接受地图定义表中的目标，否则退回出生地图。

// placeBuyoutTiers 保存各地图三档买断价，单位为金币百分位。
var placeBuyoutTiers = map[string][3]int64{
	"level_001_lake":            {0, 0, 0},
	"level_002_kirzah_river":    {0, 0, 0},
	"level_003_viiveri_lake":    {690, 0, 0},
	"level_004_torfyanoe_ozero": {290, 0, 0},
	"level_005_volhov":          {1190, 0, 0},
	"level_006_ladoga":          {1690, 0, 0},
	"level_007_grass_lake":      {990, 0, 0},
	"level_008_sura_river":      {1490, 0, 0},
	"level_009_ahtuba":          {1990, 0, 0},
	"level_010_belaya":          {290, 0, 0},
	"level_011_ladoga_02":       {1890, 0, 0},
	"level_012_north_sea":       {3290, 15990, 17990},
	"level_013_sev_don":         {1490, 0, 0},
	"level_014_carp_lake":       {1690, 0, 0},
	"level_015_tunguska":        {2490, 0, 0},
	"level_016_yama":            {2990, 0, 0},
	"level_018_moskitnoe":       {2290, 6990, 8990},
	"level_019_american_pond":   {0, 0, 0},
	"level_ban":                 {0, 0, 0},
	"level_tutorial":            {0, 0, 0},
}

// placeBuyoutPrice 该地点第 packet(1/2/3) 档买断价（无此地点/该档为 0 → 0）。
func placeBuyoutPrice(place string, packet int64) int64 {
	tiers, ok := placeBuyoutTiers[place]
	if !ok || packet < 1 || packet > 3 {
		return 0
	}
	return tiers[packet-1]
}

// placeOwned 返回玩家是否已买断该地点。
func (s *Server) placeOwned(ctx context.Context, uid int64, place string) bool {
	if place == "" {
		return false
	}
	places, err := s.DB.PlaceAccessList(ctx, uid)
	if err != nil {
		return false
	}
	for _, p := range places {
		if p == place {
			return true
		}
	}
	return false
}

// travelLevelKey 校验 1/12 和 1/14 的目标地图；无效 key 退回出生地图。
func travelLevelKey(want string) string {
	if !validLevelKey(want) {
		return spawnLevelKey
	}
	return want
}

// validLevelKey 只接受世界地图静态表中存在的地图 key。
func validLevelKey(key string) bool {
	if key == "" {
		return false
	}
	maps, err := loadMapDefs()
	if err != nil {
		return false
	}
	_, ok := maps[key]
	return ok
}

// sceneForLevel 返回地图对应的已知场景名；未登记地图返回空字符串。
func sceneForLevel(levelKey string) string {
	switch levelKey {
	case "level_001_lake":
		return "loc1_01"
	case "level_002_kirzah_river":
		return "lvl2_01"
	default:
		return ""
	}
}
