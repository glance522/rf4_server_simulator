package gameserver

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// cafeScene 将咖啡厅场景绑定到自己的水域，不能回退到玩家当前水域。
func (s *Server) cafeScene(ctx context.Context, uid int64, tree *proto.Value) (scene, level string) {
	if tree != nil {
		if a := tree.ArgItem(0); a != nil && a.Str != nil {
			scene = strings.TrimSpace(*a.Str)
		}
	}
	if scene == "" {
		level = s.currentLevel(uid)
		if level == "" {
			level = spawnLevelKey
		}
		scene = level
	}
	if strings.HasPrefix(scene, "fmarket_") || strings.HasPrefix(scene, "fishmarket_") || strings.HasPrefix(scene, "level_") {
		parts := strings.Split(scene, "_")
		if len(parts) > 1 {
			n, err := strconv.Atoi(parts[1])
			if err == nil && n > 0 && n < 1000 {
				level = s.DB.ResolveLevelKey(ctx, fmt.Sprintf("loc%d_01", n))
				if level == "" {
					defs, _ := loadMapDefs()
					for key := range defs {
						if strings.HasPrefix(key, fmt.Sprintf("level_%03d_", n)) {
							level = key
							break
						}
					}
				}
			}
		}
	} else {
		level = s.DB.ResolveLevelKey(ctx, scene)
	}
	if level == "" {
		return "", ""
	}
	// 同一水域不同入口共享订单，避免换入口重复领取。
	return level, level
}

func cafeRandom(key string) *rand.Rand {
	h := sha256.Sum256([]byte(key))
	return rand.New(rand.NewSource(int64(binary.LittleEndian.Uint64(h[:8]))))
}

// cafeTemplates 使用已核对的订单组合和奖励。
func (s *Server) cafeTemplates(ctx context.Context, level string, now int64) ([]store.CafeOrderTemplate, error) {
	return s.calibratedCafeTemplates(ctx, level, now)
}

func hCafeLots(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	cctx := ctx.Sess.pushCtx()
	scene, level := srv.cafeScene(cctx, ctx.Sess.Userid, ctx.Tree)
	out := proto.Args(proto.S("110"))
	if scene != "" {
		now := time.Now().Unix()
		templates, err := srv.cafeTemplates(cctx, level, now)
		if err != nil {
			ctx.Log.Warn("咖啡厅生成订单失败", "err", err)
			return nil, false
		}
		if len(templates) == 0 {
			body, err := proto.EncodeValue(out, ctx.Sess.Table())
			return body, err == nil
		}
		orders, err := srv.DB.GetCafeOrders(cctx, ctx.Sess.Userid, scene, level, now, 3600, templates)
		if err != nil {
			ctx.Log.Warn("咖啡厅读取订单失败", "err", err)
			return nil, false
		}
		for _, o := range orders {
			completed := uint32(0)
			if o.Completed {
				completed = 1
			}
			out.Items = append(out.Items, wiregen.MustMakeObj("jbfajcjlfek", map[int]*proto.Value{
				0: proto.Guid(o.ID), 1: proto.StrVal(o.Level), 2: proto.U64(uint64(o.ExpiresAt+11644473600) * 10000000),
				3: proto.U32(completed), 4: proto.StrVal(o.Species), 5: proto.U32(uint32(o.OfferSize)),
				6: proto.U32(proto.F32Bits(float32(o.MinWeightG) / 1000)), 7: proto.U32(uint32(o.Quantity)), 8: proto.U64(uint64(o.RewardCents)),
			}))
		}
	}
	body, err := proto.EncodeValue(out, ctx.Sess.Table())
	return body, err == nil
}

func hCafeSubmit(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	cctx := ctx.Sess.pushCtx()
	scene, _ := srv.cafeScene(cctx, ctx.Sess.Userid, ctx.Tree)
	if scene == "" || ctx.Tree == nil || ctx.Tree.ArgItem(1) == nil || ctx.Tree.ArgItem(2) == nil || ctx.Tree.ArgItem(2).Kind != proto.KindGuidArr {
		return encodeMarketSellOfferResult(ctx, 0, 1)
	}
	// 保留重复GUID交给事务校验，不能去重后冒充有效交付。
	guids := deepGUIDs(ctx.Tree.ArgItem(2))
	res, err := srv.DB.SubmitCafeOrder(cctx, ctx.Sess.Userid, scene, ctx.Tree.ArgItem(1).GuidHex(), guids, time.Now().Unix())
	if err != nil {
		ctx.Log.Warn("咖啡厅交鱼失败", "err", err)
		return encodeMarketSellOfferResult(ctx, 0, 1)
	}
	if !res.Replayed {
		st := srv.stateFor(ctx.Sess.Userid)
		st.mu.Lock()
		kept := st.Keepnet[:0]
		for _, k := range st.Keepnet {
			if !containsGUID(res.RemovedGUIDs, k.FishGUID) {
				kept = append(kept, k)
			}
		}
		st.Keepnet = kept
		st.mu.Unlock()
		ctx.Sess.Push2_1()
		ctx.Sess.Push4_21(res.RemovedGUIDs)
	}
	return encodeMarketSellOfferResult(ctx, res.RewardCents, 0)
}
