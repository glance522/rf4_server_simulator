package gameserver

import (
	"context"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// ensureAwardsInHouse 把玩家背包/鱼护里的奖赏物品移进房屋容器（容器 10）。
//
// 房屋里「奖赏」展示位按 E 后，客户端用 4/1(10)/4/4(10)/4/5(10) 拉房屋
// 容器；只有 `item.container='house'` 的物品才会出现在这个列表里。奖赏
// 物品若留在背包，3/16 奖赏面板仍能显示（它读 house/backpack/keepnet），
// 但展示位列表永远是空的，客户端显示「未找到一件合适的物件」。
//
// 因此首次请求背包或房屋库存时做一次幂等搬迁：只搬 enikeammfen(2015)
// 奖赏，受房屋容量限制，失败不改动。奖赏本身是纪念品，放进房屋后仍由
// 3/16 面板展示，也才能被摆到展示位上。
func (s *Server) ensureAwardsInHouse(ctx context.Context, uid int64) (int, error) {
	if s == nil || s.DB == nil {
		return 0, nil
	}
	rows, err := s.playerRewardItems(ctx, uid)
	if err != nil {
		return 0, err
	}
	pending := make([]*store.Item, 0, len(rows))
	for _, it := range rows {
		if it.Container != "house" {
			pending = append(pending, it)
		}
	}
	if len(pending) == 0 {
		return 0, nil
	}
	used, err := s.DB.CountItemsByContainers(ctx, uid, []string{"house"})
	if err != nil {
		return 0, err
	}
	capacity := int64(houseContainerCapacity)
	capacity, _ = s.importedContainerCapacity(ctx, uid, 10, capacity, 0)
	room := capacity - used
	if room <= 0 {
		return 0, nil
	}
	if int64(len(pending)) > room {
		pending = pending[:room]
	}
	tx, err := beginEconomyTx(ctx, s.DB)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, it := range pending {
		if _, err := tx.ExecContext(ctx,
			`UPDATE item SET container='house' WHERE guid=? AND uid=? AND container<>'house'`,
			it.GUID, uid); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(pending), nil
}

// syncAwardGroupOnce 把玩家全部奖赏按 f4=14（客户端奖赏专用分组）推给
// 本会话一次。客户端「选择物件」的奖赏标签读的是物品管理器分组 14，
// 不是 3/16 面板数组，也不会自动从 4/4 房屋列表推断；不推就会出现
// 「3/16 面板有、摆放列表空」。每会话只推一次，避免每次开背包刷屏。
func (s *Session) syncAwardGroupOnce() {
	if s == nil || s.Srv == nil || s.Srv.DB == nil {
		return
	}
	if !s.awardsSynced.CompareAndSwap(false, true) {
		return
	}
	ctx := s.pushCtx()
	rows, err := s.Srv.playerRewardItems(ctx, s.Userid)
	if err != nil {
		return
	}
	trees := make([]*proto.Value, 0, len(rows))
	for _, it := range rows {
		tree, terr := s.Srv.awardItemTree(ctx, it)
		if terr != nil || tree == nil {
			continue
		}
		trees = append(trees, tree)
	}
	if len(trees) > 0 {
		s.Push(4, 22, proto.Args(proto.S("61"), trees...))
	}
}
