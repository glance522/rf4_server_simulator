package gameserver

import (
	"context"
	"encoding/json"
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

// Only capacity is imported; occupied counts always come from current rows.
func (s *Server) importedContainerCapacity(ctx context.Context, uid int64, kind uint64, normal, member int64) (int64, int64) {
	raw, found, err := s.DB.KVGet(ctx, fmt.Sprintf("native-import:%d:inventory", uid))
	if err != nil || !found {
		return normal, member
	}
	var snapshot struct {
		Counts map[string]struct{ A, B int64 } `json:"countsByKind"`
	}
	if json.Unmarshal([]byte(raw), &snapshot) != nil {
		return normal, member
	}
	if counts, ok := snapshot.Counts[fmt.Sprint(kind)]; ok && counts.A >= 0 && counts.A <= 2147483647 && counts.B >= 0 && counts.B <= 2147483647 {
		return counts.A, counts.B
	}
	return normal, member
}
