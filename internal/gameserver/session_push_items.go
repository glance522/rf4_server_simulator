package gameserver

import (
	"rf4server/internal/proto"
)

// Push4_22Items 搏鱼全量同步并附上指定物品的树。
func (s *Session) Push4_22Items(extraGUIDs []string) {
	pctx := s.pushCtx()
	items, err := s.Srv.buildFightItems(pctx, s.Userid, true)
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, item := range items {
		if idx := fieldIdxByCS(item.Class, csGUID); idx >= 0 && idx < len(item.Fields) {
			seen[item.Fields[idx].GuidHex()] = true
		}
	}
	for _, guid := range extraGUIDs {
		if guid == "" || seen[guid] {
			continue
		}
		seen[guid] = true
		row, err := s.Srv.DB.GetItemByGUID(pctx, guid)
		if err != nil || row == nil || row.UID != s.Userid {
			continue
		}
		tree, err := s.Srv.ItemToTree(pctx, row)
		if err != nil || tree == nil {
			continue
		}
		items = append(items, tree)
	}
	if len(items) > 0 {
		s.Push(4, 22, proto.Args(proto.S("61"), items...))
	}
}

// Push4_22ItemGUIDs 只推指定背包物品的最新树。
func (s *Session) Push4_22ItemGUIDs(guids []string) {
	pctx := s.pushCtx()
	var trees []*proto.Value
	seen := map[string]bool{}
	for _, guid := range guids {
		if guid == "" || seen[guid] {
			continue
		}
		seen[guid] = true
		row, err := s.Srv.DB.GetItemByGUID(pctx, guid)
		if err != nil || row == nil || row.UID != s.Userid {
			continue
		}
		tree, err := s.Srv.ItemToTree(pctx, row)
		if err == nil && tree != nil {
			trees = append(trees, tree)
		}
	}
	if len(trees) > 0 {
		s.Push(4, 22, proto.Args(proto.S("61"), trees...))
	}
}
