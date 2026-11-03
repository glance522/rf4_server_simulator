package gameserver

import (
	"context"
	"database/sql"
)

func terminalBreakPart(category int64) string {
	switch category {
	case catLine:
		return "line"
	case catHook:
		return "hook"
	case catLeaderLock:
		return "leader_lock"
	default:
		return "leader"
	}
}

// 出厂钩没有独立物品行，只移除断钩，保留拟饵和其他钩。
func (s *Server) removeLureHookTx(ctx context.Context, tx *sql.Tx, rodGUID, hookGUID string) (string, error) {
	lureGUID := ""
	_ = tx.QueryRowContext(ctx, `SELECT COALESCE(guid,'') FROM rig_component WHERE rig_guid=? AND slot=? ORDER BY ordinal LIMIT 1`, rodGUID, catLure).Scan(&lureGUID)
	if lureGUID == "" {
		rod, err := s.DB.GetItemByGUIDTx(ctx, tx, rodGUID)
		if err != nil {
			return "", err
		}
		fields, err := FieldsFromJSON(rod.FieldsJSON)
		if err != nil {
			return "", err
		}
		lureGUID = rigCompsOf(protoFieldAt(fields, rod.WireClass, csRigObject))[catLure].GUID
	}
	row, err := s.DB.GetItemByGUIDTx(ctx, tx, lureGUID)
	if err != nil || row == nil {
		return "", sql.ErrNoRows
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return "", err
	}
	if len(fields) == 0 || fields[0].Val == nil {
		return "", sql.ErrNoRows
	}
	hooks := fields[0].Val
	for i, hook := range hooks.Items {
		if hook == nil {
			continue
		}
		idx := fieldIdxByCS(hook.Class, csGUID)
		if idx >= 0 && hook.Fields[idx].GuidHex() == hookGUID {
			hooks.Items = append(hooks.Items[:i], hooks.Items[i+1:]...)
			if len(fields) > 1 && fields[1].Num != nil {
				fields[1].Num = ptrU64(0)
			}
			if err := updateItemFieldsTx(ctx, s.DB, tx, row, fields); err != nil {
				return "", err
			}
			return lureGUID, nil
		}
	}
	return "", sql.ErrNoRows
}
