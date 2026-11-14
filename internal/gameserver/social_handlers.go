package gameserver

import (
	"database/sql"
	"encoding/json"
	"strings"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

func registerSocialHandlers(d *Dispatch) {
	d.Register(24, 4, hSocialMembers)
	d.RegisterResponseContract(24, 4, ResponseContractRule{Kind: proto.KindU32Arr})
	d.Register(24, 18, hSocialUserProfile)
	d.Register(24, 19, hSocialUserCard)
	d.Register(24, 21, hSocialAbsoluteRecords)
	d.Register(24, 22, hSocialRecordFish)
	d.Register(24, 24, hSocialNamedFish)
}

func firstRequestUID(ctx *Ctx) int64 {
	if v, ok := ctx.ArgInt(0); ok {
		return int64(v)
	}
	if ints := deepInts(ctx.Tree); len(ints) > 0 {
		return ints[0]
	}
	return 0
}

func encodeSocialTree(ctx *Ctx, v *proto.Value) ([]byte, bool) {
	body, err := proto.EncodeValue(v, ctx.Sess.Table())
	if err != nil {
		ctx.Log.Error("24/x 响应编码失败", "cmd", ctx.Key(), "err", err)
		return nil, false
	}
	return body, true
}

func bytesWrappedObject(inner *proto.Value, table *proto.OpsTable) (*proto.Value, error) {
	raw, err := proto.EncodeValue(inner, table)
	if err != nil {
		return nil, err
	}
	return proto.BytesOf(raw), nil
}

func hSocialUserProfile(ctx *Ctx) ([]byte, bool) {
	uid := firstRequestUID(ctx)
	decks := tradingDeckList()
	if s := ctx.Sess.Srv; s != nil && s.DB != nil {
		var err error
		decks, err = s.playerTradingDecks(ctx.Sess.pushCtx(), uid)
		if err != nil {
			ctx.ReplyFailure("System.IO.IOException", "卡册记录读取失败")
			return nil, true
		}
	}
	inner := wiregen.MustMakeObj("feaidppnpch", map[int]*proto.Value{
		0: proto.U32(uint32(uid)),
		9: decks,
	})
	outer, err := bytesWrappedObject(inner, ctx.Sess.Table())
	if err != nil {
		ctx.Log.Error("24/18 档案编码失败", "uid", uid, "err", err)
		return []byte{0x00}, true
	}
	return encodeSocialTree(ctx, outer)
}

func hSocialUserCard(ctx *Ctx) ([]byte, bool) {
	uid := firstRequestUID(ctx)
	if isVirtualContact(uid) {
		name := fishQueryContactName
		if uid == gearAssistantUID {
			name = gearAssistantName
		}
		outer, err := bytesWrappedObject(virtualContactCard(uid, name), ctx.Sess.Table())
		if err != nil {
			return nil, false
		}
		return encodeSocialTree(ctx, outer)
	}
	var p *store.Profile
	names := ""
	level := int64(1)
	role := int64(0)
	region := int64(0)
	trophies := int64(0)
	if ctx.Sess.Srv != nil && ctx.Sess.Srv.DB != nil {
		var err error
		p, err = ctx.Sess.Srv.DB.GetProfile(ctx.Sess.Srv.dbCtx(), uid)
		if err != nil && err != sql.ErrNoRows {
			ctx.Log.Error("24/19 档案查询失败", "uid", uid, "err", err)
		}
		if p != nil {
			names = p.Name
			level = p.Level
			role = p.Role
			region = socialRegionCode(p.Region)
			// f5 与 profile f16 同落客户端 ifbfdldbana.gebeadddmlp（等级旁
			// 星号同一字段），官服按鱼种去重计，不按稀有渔获条数。
			if n, err := ctx.Sess.Srv.DB.RareSpeciesCount(ctx.Sess.Srv.dbCtx(), uid, 3); err == nil {
				trophies = n
			}
		}
	}
	inner := wiregen.MustMakeObj("dgbmlekfpbg", map[int]*proto.Value{
		0: proto.U32(uint32(uid)),
		1: proto.StrVal(names),
		2: proto.Str(nil, 0x15),
		3: proto.U32(uint32(role)),
		4: proto.U32(uint32(level)),
		5: proto.U32(uint32(trophies)),
		6: proto.U32(0),
		7: proto.U32(uint32(region)),
		8: proto.U32(0),
	})
	outer, err := bytesWrappedObject(inner, ctx.Sess.Table())
	if err != nil {
		ctx.Log.Error("24/19 资料卡编码失败", "uid", uid, "err", err)
		return []byte{0x00}, true
	}
	return encodeSocialTree(ctx, outer)
}

func socialRegionCode(region string) int64 {
	switch region {
	case "GL":
		return 0
	case "RU":
		return 1
	case "DE":
		return 2
	case "EN":
		return 3
	case "CN":
		return 4
	case "FR":
		return 5
	case "PL":
		return 6
	case "US":
		return 7
	case "JP":
		return 8
	case "KR":
		return 9
	default:
		return 0
	}
}

func hSocialAbsoluteRecords(ctx *Ctx) ([]byte, bool) {
	strs := argStringsTop(ctx.Tree)
	pick := func(i int, def string) string {
		if i < len(strs) && strs[i] != "" {
			return strs[i]
		}
		return def
	}
	payload := struct {
		Success  bool              `json:"success"`
		ID       string            `json:"id"`
		Type     string            `json:"type"`
		Locale   string            `json:"locale"`
		Region   string            `json:"region"`
		Modified int64             `json:"modified"`
		Records  []json.RawMessage `json:"records"`
	}{
		Success: true, ID: pick(0, "absolute"), Type: pick(1, "DEF"),
		Locale: pick(2, "zh-CN"), Region: "GL", Records: []json.RawMessage{},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		ctx.Log.Error("24/21 JSON 编码失败", "err", err)
		return []byte{0x00}, true
	}
	return encodeSocialTree(ctx, proto.BytesOf(raw))
}

func hSocialRecordFish(ctx *Ctx) ([]byte, bool) {
	water := argFirstStr(ctx.Tree)
	if ctx.Sess.Srv == nil || ctx.Sess.Srv.DB == nil {
		return encodeSocialTree(ctx, proto.Null())
	}
	row, err := ctx.Sess.Srv.DB.BestCatchByWater(ctx.Sess.Srv.dbCtx(), ctx.Sess.Userid, water)
	if err != nil {
		ctx.Log.Error("24/22 纪录鱼查询失败", "water", water, "err", err)
		return encodeSocialTree(ctx, proto.Null())
	}
	if row == nil {
		return encodeSocialTree(ctx, proto.Null())
	}
	angler := ""
	if p, err := ctx.Sess.Srv.DB.GetProfile(ctx.Sess.Srv.dbCtx(), ctx.Sess.Userid); err == nil {
		angler = p.Name
	}
	loc := row.LocLevel
	if loc == "" {
		loc = row.Loc
	}
	// f11（oajeeklppia）同样是模型族参数：留空就退回鱼种默认族（老模型），
	// 纪录页的预览会跟渔护/溜鱼对不上。这里用 catch_guid 推导同一个族名。
	modelKey := ctx.Sess.Srv.fishModelVariantForFish(row.FishKey, row.WeightG, row.LengthM,
		row.Grade, row.GUID)
	v := wiregen.MustMakeObj("kijhfkcpngc", map[int]*proto.Value{
		0:  proto.StrVal(row.FishKey),
		1:  proto.U32(uint32(row.WeightG)),
		2:  proto.F32(float32(row.LengthM)),
		3:  proto.U32(uint32(maxInt64(row.Grade, 1))),
		4:  proto.U32(uint32(row.XP)),
		5:  proto.U64(unixToFiletime100ns(row.TS)),
		6:  proto.U32(0),
		7:  proto.StrVal(angler),
		8:  proto.StrVal(""),
		9:  proto.StrVal(loc),
		10: proto.ArrayOf(nil),
		11: proto.StrVal(modelKey),
	})
	return encodeSocialTree(ctx, v)
}

// namedFishGUID 把 32 位十六进制鱼标识格式化成客户端使用的 GUID。
func namedFishGUID(id string) string {
	if len(id) == 32 && !strings.Contains(id, "-") {
		return id[0:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:32]
	}
	return id
}

// namedFishFallbackID 在没有结算记录时生成一个稳定的 history id。
func namedFishFallbackID(id string) int64 {
	var h int64
	for i := 0; i < len(id) && i < 8; i++ {
		h *= 16
		switch c := id[i]; {
		case c >= '0' && c <= '9':
			h += int64(c - '0')
		case c >= 'a' && c <= 'f':
			h += int64(c-'a') + 10
		case c >= 'A' && c <= 'F':
			h += int64(c-'A') + 10
		}
	}
	return h
}

// namedFishWire 按官方 24/24 JSON 结构编码一条命名鱼：
// {"id","systemID","name","history":[{"id","catched","weight","length","baitID"}]}
// weight 是公斤、length 是米、catched 是 Unix 秒。
func namedFishWire(nf store.NamedFish) map[string]any {
	histID := nf.CatchID
	if histID == 0 {
		histID = namedFishFallbackID(nf.FishID)
	}
	return map[string]any{
		"id":       namedFishGUID(nf.FishID),
		"systemID": nf.Species,
		"name":     nf.Name,
		"history": []any{map[string]any{
			"id":      histID,
			"catched": int64(nf.TS),
			"weight":  float64(nf.WeightG) / 1000,
			"length":  nf.LengthM,
			"baitID":  nf.Bait,
		}},
	}
}

func hSocialNamedFish(ctx *Ctx) ([]byte, bool) {
	key := argFirstStr(ctx.Tree)
	entries := make([]any, 0)
	if s := ctx.Sess.Srv; s != nil && s.DB != nil {
		rows, err := s.DB.NamedFishList(s.dbCtx(), key)
		if err != nil {
			ctx.Log.Error("24/24 命名鱼查询失败", "place", key, "err", err)
		}
		for _, nf := range rows {
			entries = append(entries, namedFishWire(nf))
		}
		ctx.Log.Info("24/24 明星居民", "place", key, "count", len(rows))
	}
	raw, err := json.Marshal(map[string]any{"namedfish": entries})
	if err != nil {
		ctx.Log.Error("24/24 JSON 编码失败", "err", err)
		return []byte{0x00}, true
	}
	return encodeSocialTree(ctx, proto.BytesOf(raw))
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
