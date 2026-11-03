package gameserver

import (
	"context"
	"testing"
)

func TestDebugBiteIntervalIncludesNormalAndLureRigs(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	for key, value := range map[string]string{"debug_mode": "1", "bite_delay_fixed": "99", "bite_delay_scale": "0.01"} {
		if err := db.TunableSet(ctx, key, value, "test"); err != nil {
			t.Fatal(err)
		}
	}
	rigs := []int64{1}
	for rig := range rigTypeLureFamily {
		rigs = append(rigs, rig)
	}
	for _, rig := range rigs {
		lo, hi := 5.0, 3.0
		for range 128 {
			delay := srv.biteDelay(&FishState{RigType: rig}, fishingSkillBonus{BiteSpeed: 0.1})
			if delay < 3 || delay > 5 {
				t.Fatalf("rig=%d delay=%v outside debug range", rig, delay)
			}
			if delay < lo {
				lo = delay
			}
			if delay > hi {
				hi = delay
			}
		}
		if hi-lo < 1 {
			t.Fatalf("rig=%d debug interval unexpectedly fixed: %v..%v", rig, lo, hi)
		}
	}
}

func TestNormalBiteSettingsRemainEffectiveOutsideDebugMode(t *testing.T) {
	srv, db := newDetachTestServer(t)
	if err := db.TunableSet(context.Background(), "bite_delay_fixed", "17", "test"); err != nil {
		t.Fatal(err)
	}
	if got := srv.biteDelay(&FishState{RigType: 1}); got != 17 {
		t.Fatalf("normal configured delay=%v", got)
	}
}
