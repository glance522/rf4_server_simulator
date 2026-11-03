package gameserver

import "math"

// 负载比是锁轮直传时的潜在负荷，不等于客户端实际力度条。
type fishFightAnalysis struct {
	ForceN, PeakForceN           float64
	InitialStamina               float64
	EndurancePerNewton           float64
	GearLimitKg                  float64
	WaterDragN, PressurePa       float64
	WaterAlongN, WaterCrossN     float64
	CurrentDirectionKnown        bool
	DepthBlocked                 bool
	PotentialLoad, PotentialPeak float64
}

func (s *Server) analyzeFishFight(f *RolledFish, spec gearPhysicsSpec) fishFightAnalysis {
	force := s.fishForceFor(f)
	peakRatio := 1.0
	if f != nil {
		peakRatio = s.fishCurvePeakRatio(f.Species)
	}
	out := fishFightAnalysis{
		ForceN: force, PeakForceN: force * peakRatio,
		InitialStamina: s.fishStaminaFor(f) * s.fishInitialStaminaRatio(),
		GearLimitKg:    gearBreakLimitKg(spec),
	}
	if force > 0 {
		out.EndurancePerNewton = out.InitialStamina / force
	}
	tension, peakTension := force, out.PeakForceN
	if f != nil {
		water := evaluateFightWater(spec, f.Species, float64(f.WeightG), f.LengthM)
		out.WaterDragN, out.PressurePa, out.DepthBlocked = water.DragN, water.PressurePa, water.DepthBlocked
		out.WaterAlongN, out.WaterCrossN, out.CurrentDirectionKnown = water.AlongPullN, water.CrossPullN, water.DirectionKnown
		tension, peakTension = waterCombinedForce(force, water, true), waterCombinedForce(out.PeakForceN, water, true)
		if tension > 0 {
			out.EndurancePerNewton = out.InitialStamina / tension
		}
	}
	load := evaluateTackleLoad(spec, tackleLoadContext{ForceN: tension, PeakN: peakTension, LineOutM: spec.Water.FishDepthM, Locked: true})
	out.PotentialLoad, out.PotentialPeak = load.LoadRatio, load.PeakRatio

	return out
}

func (s *Server) fishCurvePeakRatio(species string) float64 {
	peak := 1.0
	rows := s.fightCurveRows(species)
	if len(rows) > 0 && rows[0][8] > 0 {
		for _, row := range rows {
			peak = math.Max(peak, math.Max(row[8], row[9])/rows[0][8])
		}
	}
	return peak
}
