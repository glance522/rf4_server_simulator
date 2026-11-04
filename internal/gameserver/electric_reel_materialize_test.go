package gameserver

import (
	"testing"

	"rf4server/internal/proto"
)

// 电动轮电机装在渔轮 f14（dfjpnambjcj）扩展槽：wire 类必须是
// kbpmmclfipg/1470（DTO odlknjlbooh），功率与最高速度写进 f0/f1。
// 缺失或为 0 时客户端能切电动模式但不收线。
func TestElectricReelMotorMaterialized(t *testing.T) {
	srv, _, ctx := newMiscProbe(t)
	it, err := srv.CreateItem(ctx, 1000001, "elcon_6003", "backpack", 1)
	if err != nil || it == nil {
		t.Fatalf("创建电动轮失败: %v", err)
	}
	fields, err := FieldsFromJSON(it.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	idx := fieldIdxByCS(it.WireClass, "dfjpnambjcj")
	if idx < 0 || idx >= len(fields) || fields[idx].Val == nil || fields[idx].Val.Kind != proto.KindObj {
		t.Fatal("渔轮 f14 电机槽为空")
	}
	motor := fields[idx].Val
	if motor.Class != "kbpmmclfipg" || motor.TypeID != 1470 {
		t.Fatalf("f14 电机类=%s/%d，期望 kbpmmclfipg/1470", motor.Class, motor.TypeID)
	}
	def, err := srv.DB.GetItemDef(ctx, it.NumericID)
	if err != nil || def == nil {
		t.Fatalf("读取电动轮定义失败: %v", err)
	}
	attrs := defAttrs(def)
	motorCfg, _ := attrs["dfjpnambjcj"].(map[string]any)
	if motorCfg == nil {
		t.Fatal("电动轮定义缺少 f14 电机配置")
	}
	for cs, attr := range map[string]string{"npnlhobgfen": "afmboidbbaj", "mbbdpnidheg": "fgkfkgimfgo"} {
		fi := fieldIdxByCS(motor.Class, cs)
		if fi < 0 || fi >= motor.FieldCount() {
			t.Fatalf("电机类缺少字段 %s", cs)
		}
		raw, _ := motorCfg[attr].(float64)
		if raw == 0 {
			t.Fatalf("定义 %s 缺失", attr)
		}
		if got := u64Of(motor.Fields[fi].Num); got != uint64(raw) {
			t.Fatalf("电机 %s=%d，期望定义值 %d", cs, got, uint64(raw))
		}
	}
}
