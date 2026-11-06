package gameserver

import (
	"math"
	"strings"
)

// fishFightProfile 是一次搏鱼中鱼种与体重共同决定的动态参数。
// forceScale 作用于冲刺峰值；duration/rest 作用于 27.f21 的阶段时长；
// staminaScale 作用于 27.f5 的体力池。
type fishFightProfile struct {
	ForceScale    float64
	DurationScale float64
	RestScale     float64
	StaminaScale  float64
	SizeRatio     float64
}

type fishFightArchetype struct {
	Burst     float64
	Duration  float64
	Rest      float64
	Stamina   float64
	RestPower float64
	CurvePool string
}

var fishFightArchetypes = map[string]fishFightArchetype{
	"default":  {Burst: 1.10, Duration: 0.95, Rest: 0.85, Stamina: 1.00, RestPower: 0.55, CurvePool: "crucian"},
	"ambush":   {Burst: 1.38, Duration: 0.72, Rest: 0.68, Stamina: 0.95, RestPower: 0.48, CurvePool: "perch"},
	"salmonid": {Burst: 1.30, Duration: 1.18, Rest: 0.92, Stamina: 1.28, RestPower: 0.52, CurvePool: "e.chub"},
	"cyprinid": {Burst: 1.06, Duration: 1.22, Rest: 1.10, Stamina: 1.38, RestPower: 0.58, CurvePool: "c.carp"},
	"catfish":  {Burst: 1.48, Duration: 1.20, Rest: 1.18, Stamina: 1.65, RestPower: 0.60, CurvePool: "e.chub"},
	"sturgeon": {Burst: 1.34, Duration: 1.42, Rest: 1.22, Stamina: 1.75, RestPower: 0.62, CurvePool: "e.chub"},
	"small":    {Burst: 0.96, Duration: 0.62, Rest: 0.58, Stamina: 0.72, RestPower: 0.45, CurvePool: "c.roach"},
	"marine":   {Burst: 1.22, Duration: 1.02, Rest: 0.90, Stamina: 1.18, RestPower: 0.52, CurvePool: "perch"},
	"eel":      {Burst: 1.16, Duration: 0.78, Rest: 0.68, Stamina: 0.92, RestPower: 0.46, CurvePool: "a.sleeper"},
	"apex":     {Burst: 1.58, Duration: 1.05, Rest: 1.05, Stamina: 1.55, RestPower: 0.56, CurvePool: "e.chub"},
}

func fishFightArchetypeFor(species string) fishFightArchetype {
	sp := strings.ToLower(strings.TrimSpace(species))
	has := func(parts ...string) bool {
		for _, part := range parts {
			if strings.Contains(sp, part) {
				return true
			}
		}
		return false
	}
	switch {
	case has("shark", "katran", "lamna", "cetorhinus", "somniosus"):
		return fishFightArchetypes["apex"]
	case has("catfish", "burbot", "bullhead", "wels", "som"):
		return fishFightArchetypes["catfish"]
	case has("sturgeon", "beluga", "kaluga", "sterlet", "osetr"):
		return fishFightArchetypes["sturgeon"]
	case has("salmon", "trout", "char", "taimen", "lenok", "nelma", "omul",
		"muksun", "peled", "ripus", "vendace", "smelt", "keta", "nerka", "golets", "grayling"):
		return fishFightArchetypes["salmonid"]
	case has("pike", "zander", "walleye", "perch", "barsch", "orfe", "asp",
		"ruffe", "piksha", "sander"):
		return fishFightArchetypes["ambush"]
	case has("eel", "lamprey", "conger"):
		return fishFightArchetypes["eel"]
	case has("carp", "bream", "tench", "barbel", "buffalo", "karasekarp"):
		return fishFightArchetypes["cyprinid"]
	case has("roach", "rudd", "dace", "bleak", "gudgeon", "minnow",
		"vendace", "sabrefish", "shemaya", "zubatka", "vobla", "stickleback"):
		return fishFightArchetypes["small"]
	case has("cod", "herring", "mackerel", "makrel", "tuna", "thynnus",
		"halibut", "kambala", "coalfish", "pollachius", "merluza", "molva",
		"hake", "saithe", "turbot", "flounder", "scorpius", "zoarces"):
		return fishFightArchetypes["marine"]
	default:
		return fishFightArchetypes["default"]
	}
}

func (s *Server) fishFightProfileFor(f *RolledFish) fishFightProfile {
	arch := fishFightArchetypeFor("")
	if f != nil {
		arch = fishFightArchetypeFor(f.Species)
	}
	ratio := 0.5
	if f != nil {
		switch f.Grade {
		case 1:
			ratio = 0.25
		case 2:
			ratio = 0.50
		case 3:
			ratio = 0.75
		case 4:
			ratio = 1.0
		}
	}
	if f != nil && f.WeightG > 0 && s != nil && s.DB != nil {
		var maxG int64
		if err := s.DB.SQL.QueryRowContext(s.dbCtx(),
			`SELECT COALESCE(max_g,0) FROM species_weight WHERE fish_key=?`,
			f.Species).Scan(&maxG); err == nil && maxG > 0 {
			ratio = float64(f.WeightG) / float64(maxG)
		}
	}
	ratio = math.Max(0.03, math.Min(1.0, ratio))
	root := math.Sqrt(ratio)
	return fishFightProfile{
		ForceScale:    arch.Burst * (0.84 + 0.28*root),
		DurationScale: arch.Duration * (0.72 + 0.58*ratio),
		RestScale:     arch.Rest * (0.82 + 0.30*root),
		StaminaScale:  arch.Stamina * (0.66 + 0.58*root),
		SizeRatio:     ratio,
	}
}

func fightPhaseTensionScale(stage uint32) float64 {
	switch stage {
	case 300:
		return 1.36
	case 301:
		return 1.20
	case 302:
		return 1.78
	case 306:
		return 0.42
	case 101:
		return 1.66
	case 102:
		return 1.42
	case 103:
		return 1.16
	case 104:
		return 0.62
	case 105:
		return 0.30
	default:
		return 0.92
	}
}

func fightPhaseDurationScale(stage uint32) float64 {
	switch stage {
	case 302:
		return 0.82
	case 101:
		return 0.72
	case 102:
		return 0.78
	case 103:
		return 1.18
	case 104:
		return 1.35
	case 105, 306:
		return 1.55
	default:
		return 1.0
	}
}

func fightIsRestPhase(stage uint32) bool {
	return stage == 104 || stage == 105 || stage == 306
}
