package gameserver

import (
	"math"
	"testing"

	"rf4server/internal/proto"
)

func TestFightCurvesKeepSprintTensionStages(t *testing.T) {
	srv, _ := newRigTestServer(t)
	const pull = 50.0
	curves := srv.buildFightCurves("crucian", pull)
	if curves == nil || len(curves.Items) < 20 {
		t.Fatalf("搏鱼曲线行数=%d，期望完整冲刺阶段池", len(curves.Items))
	}

	stageIDs := map[uint32]bool{}
	minTension, maxTension := math.Inf(1), 0.0
	for _, row := range curves.Items {
		stageField := row.Field(0)
		stage := stageField.U32()
		stageIDs[stage] = true
		for _, idx := range []int{8, 9} {
			v := protoF32Field(row, idx)
			if v <= 0 {
				t.Fatalf("阶段 %d 的 f%d 张力无效: %f", stage, idx, v)
			}
			minTension = math.Min(minTension, v)
			maxTension = math.Max(maxTension, v)
		}
	}
	for _, stage := range []uint32{104, 105, 301, 302, 306} {
		if !stageIDs[stage] {
			t.Errorf("缺少冲刺/恢复阶段 %d", stage)
		}
	}
	if minTension > pull*0.75 || maxTension < pull*1.15 {
		t.Fatalf("曲线张力范围 %.2f..%.2f，未形成可展示的冲刺差异（基准 %.2f）",
			minTension, maxTension, pull)
	}
}

func TestFishFightProfilesUseSpeciesAndWeight(t *testing.T) {
	srv, _ := newRigTestServer(t)
	pikeSmall := srv.fishFightProfileFor(&RolledFish{Species: "n.pike", WeightG: 150, Grade: 1})
	pikeLarge := srv.fishFightProfileFor(&RolledFish{Species: "n.pike", WeightG: 8000, Grade: 4})
	roach := srv.fishFightProfileFor(&RolledFish{Species: "c.roach", WeightG: 100, Grade: 1})
	catfish := srv.fishFightProfileFor(&RolledFish{Species: "w_catfish", WeightG: 20000, Grade: 4})

	if pikeLarge.ForceScale <= pikeSmall.ForceScale {
		t.Fatalf("同鱼种大体型冲刺应更强: small=%f large=%f",
			pikeSmall.ForceScale, pikeLarge.ForceScale)
	}
	if pikeLarge.StaminaScale <= pikeSmall.StaminaScale {
		t.Fatalf("同鱼种大体型耐力应更强: small=%f large=%f",
			pikeSmall.StaminaScale, pikeLarge.StaminaScale)
	}
	if pikeLarge.ForceScale <= roach.ForceScale {
		t.Fatalf("伏击型掠食鱼冲刺应强于小型鱼: pike=%f roach=%f",
			pikeLarge.ForceScale, roach.ForceScale)
	}
	if catfish.StaminaScale <= pikeLarge.StaminaScale {
		t.Fatalf("鲶鱼耐力应高于梭鲈: catfish=%f pike=%f",
			catfish.StaminaScale, pikeLarge.StaminaScale)
	}

	curves := srv.buildFightCurvesForFish(
		&RolledFish{Species: "n.pike", WeightG: 8000},
		20,
		pikeLarge,
	)
	minTension, maxTension, restTension, burstTension := math.Inf(1), 0.0, 0.0, 0.0
	for _, row := range curves.Items {
		stageField := row.Field(0)
		stage := stageField.U32()
		v := protoF32Field(row, 8)
		minTension = math.Min(minTension, v)
		maxTension = math.Max(maxTension, v)
		if fightIsRestPhase(stage) {
			restTension = math.Max(restTension, v)
		}
		if stage == 302 || stage == 101 {
			burstTension = math.Max(burstTension, v)
		}
	}
	if burstTension <= restTension*1.8 {
		t.Fatalf("冲刺/休息张力差不足: burst=%f rest=%f", burstTension, restTension)
	}
	if maxTension <= minTension*2.2 {
		t.Fatalf("完整搏鱼池张力波动不足: %.2f..%.2f", minTension, maxTension)
	}
}

func protoF32Field(row *proto.Value, idx int) float64 {
	if row == nil || idx < 0 || idx >= len(row.Fields) || row.Fields[idx].Num == nil {
		return 0
	}
	return float64(proto.F32FromBits(uint32(u64Of(row.Fields[idx].Num))))
}
