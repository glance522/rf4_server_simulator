package gameserver

import (
	"context"
	"fmt"
)

func gearFeatureKey(uid int64, feature string) string {
	switch feature {
	case "shoes":
		return fmt.Sprintf("gear-assistant:shoes:%d", uid)
	case "stamina":
		return staminaReportKey(uid)
	default:
		return overloadProtectionKey(uid)
	}
}

func readGearFeatureFlag(ctx context.Context, db overloadProtectionReader, uid int64, feature string) (bool, error) {
	return readKVFlag(ctx, db, gearFeatureKey(uid, feature))
}

// Only the bridge's explicit startup read needs a private settings snapshot.
func gearClientSnapshot(ctx context.Context, db overloadProtectionReader, uid int64, reply string) (string, error) {
	on, err := readGearFeatureFlag(ctx, db, uid, "shoes")
	if err != nil {
		return "", err
	}
	value := "关闭"
	if on {
		value = "开启"
	}
	return reply + "\n\n五速鞋：" + value, nil
}
