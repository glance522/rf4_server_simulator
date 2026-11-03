package gameserver

import "math"

// 泄力比例和已放线长必须由调用场景提供，不能从鱼重反推玩家操作。
type tackleLoadContext struct {
	ForceN, PeakN          float64
	DragFraction, LineOutM float64
	Locked                 bool
}

type tackleLoadResult struct {
	TensionN, PeakN, LoadRatio, PeakRatio float64
	ReserveM, RunningLineMassG            float64
	Slipping, LineExhausted               bool
	WeakestPart                           string
}

func tackleShockFactor(spec gearPhysicsSpec) float64 {
	line := effectiveLineBuffer(spec.LineStretch)
	rod := 1.0
	if spec.RodLengthM > 0 && spec.RodTaper > 0 {
		// 只缓冲增量冲击，不抬高额定承力，也不抵消持续负荷。
		rod -= 0.15 * (1 - clampFloat(spec.RodTaper, 0, 1)) * clampFloat(spec.RodLengthM/4, 0, 1)
	}
	return line * rod
}

func evaluateTackleLoad(spec gearPhysicsSpec, in tackleLoadContext) tackleLoadResult {
	force := math.Max(0, in.ForceN)
	peak := force + math.Max(0, in.PeakN-force)*tackleShockFactor(spec)
	out := tackleLoadResult{TensionN: force, PeakN: peak, WeakestPart: gearWeakestPart(spec)}
	if spec.LinePresent || spec.LineLengthM > 0 {
		out.ReserveM = math.Max(0, spec.LineLengthM-math.Max(0, in.LineOutM))
		out.LineExhausted = out.ReserveM == 0
	}
	out.RunningLineMassG = math.Max(0, spec.LineDensityGPM) * math.Max(0, in.LineOutM)
	if spec.ReelPresent && !in.Locked && !out.LineExhausted && spec.ReelBaseDragKg > 0 {
		drag := spec.ReelBaseDragKg * clampFloat(in.DragFraction, 0, 1) * verifiedForcePerGram * 1000
		out.Slipping = peak > drag
		out.TensionN = math.Min(force, drag)
		out.PeakN = math.Min(peak, drag)
	}
	if limit := gearBreakLimitKg(spec); limit > 0 {
		limitN := limit * verifiedForcePerGram * 1000
		out.LoadRatio, out.PeakRatio = out.TensionN/limitN, out.PeakN/limitN
	}
	return out
}
