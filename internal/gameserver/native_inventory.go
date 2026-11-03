package gameserver

import (
	"context"
	"fmt"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func nativeInventoryContainers(kind uint64) []string {
	switch kind {
	case containerBackpack:
		return []string{"backpack", "rig"}
	case 17, 18:
		return []string{fmt.Sprintf("native:%d", kind)}
	}
	return nil
}

// 导入快照保留渔获资料，容器和版本以当前护网为准。
func (s *Server) keepnetItemTree(ctx context.Context, entry *store.KeepnetEntry, level string) *proto.Value {
	row, err := s.DB.GetItemByGUID(ctx, entry.EchoGUID)
	if err == nil && row.UID == entry.UID && row.Container == "keepnet" && row.TypeID == 25 && row.WireClass == "ecknbkpeeji" {
		if fields, err := FieldsFromJSON(row.FieldsJSON); err == nil && len(fields) == 29 {
			fields[16].Hex = entry.EchoGUID
			fields[18].Num = ptrU64(containerKeepnet)
			fields[22].Num = ptrU64(uint64(netTicksNow()))
			return proto.Obj(row.WireClass, uint32(row.TypeID), fields...)
		}
	}
	return buildKeepnet25(entry, level)
}

func inventoryContainerMatches(name string, allowed []string) bool {
	for _, value := range allowed {
		if name == value {
			return true
		}
	}
	return false
}
