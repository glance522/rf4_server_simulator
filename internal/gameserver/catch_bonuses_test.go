package gameserver

import (
	"context"
	"math"
	"rf4server/internal/proto"
	"testing"
	"time"
)

func TestRodXPUsesDefinitionAndSavedReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, item string
		kind       uint32
		rate       float64
	}{
		{"UL", "cast_10055_CSM68UL", 1, 1},
		{"L", "bolo_10071_BL500", 2, .5},
		{"ML", "bolo_10022_LB400", 3, .25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, db := newProgressSrv(t)
			importStaticForProbe(t, db)
			ctx := context.Background()
			const uid = int64(1000001)
			rod, err := srv.CreateItem(ctx, uid, tc.item, "backpack", 1)
			if err != nil {
				t.Fatal(err)
			}
			st := srv.stateFor(uid)
			st.mu.Lock()
			st.GearGuid = rod.GUID
			st.mu.Unlock()
			if _, err := db.EnsureProfile(ctx, uid); err != nil {
				t.Fatal(err)
			}
			if _, err := db.SQL.Exec(`UPDATE profile SET xp=0,level=1,membership=1,membership_exp=? WHERE uid=?`, time.Now().Unix()+3600, uid); err != nil {
				t.Fatal(err)
			}
			fish := &RolledFish{FishGUID: newFishGUID(), Species: "perch", Grade: 2, WeightG: 642}
			result := srv.settleCatchXP(ctx, uid, fish, 1)
			if result == nil || len(result.bonuses) != 1 {
				t.Fatalf("missing rod bonus: %+v", result)
			}
			want := int64(math.Round(float64(result.baseXP) * tc.rate))
			b := result.bonuses[0]
			if b.Kind != tc.kind || b.GUID != rod.GUID || b.Multiplier != tc.rate || b.XP != want || result.totalXP != result.baseXP*2+want {
				t.Fatalf("incorrect breakdown: result=%+v bonus=%+v", result, b)
			}
			p, err := db.GetProfile(ctx, uid)
			if err != nil || p.XP != result.totalXP {
				t.Fatalf("credited XP differs: %+v %v", p, err)
			}
			block := srv.buildSettle813(ctx, uid, fish, result).Field(8).Val
			list := block.Field(6).Val
			if list.ArgCount() != 2 {
				t.Fatalf("expected rod and premium entries: %+v", list)
			}
			entry := list.ArgItem(0)
			amount, category, kind, guid := entry.Field(2), entry.Field(3), entry.Field(4), entry.Field(1)
			if amount.U64() != uint64(want) || category.U32() != 9 || kind.U32() != tc.kind || guid.GuidHex() != rod.GUID {
				t.Fatalf("incorrect native bonus entry: %+v", entry)
			}
			// 新会话中没有原手持竿，仍必须重放原奖励，而非用当前装备重算。
			fresh := newFishingRecoveryServer(t, db)
			replay := fresh.settleCatchXP(ctx, uid, fish, 1)
			if replay == nil || !replay.replayed || len(replay.bonuses) != 1 || replay.bonuses[0] != b || replay.totalXP != result.totalXP {
				t.Fatalf("receipt changed after reconnect: %+v", replay)
			}
			p, err = db.GetProfile(ctx, uid)
			if err != nil || p.XP != result.totalXP {
				t.Fatal("replay credited XP twice")
			}
		})
	}
}

func TestRodXPRejectsForeignOrBrokenEquipment(t *testing.T) {
	srv, db := newProgressSrv(t)
	importStaticForProbe(t, db)
	ctx := context.Background()
	rod, err := srv.CreateItem(ctx, 1000001, "bolo_10022_LB400", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.rodXPBonuses(ctx, 1000002, rod.GUID, 101); err == nil {
		t.Fatal("foreign rod accepted")
	}
	if _, err := db.SQL.Exec(`UPDATE item SET durability=0 WHERE guid=?`, rod.GUID); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.rodXPBonuses(ctx, 1000001, rod.GUID, 101); err == nil {
		t.Fatal("broken rod accepted")
	}
	b, err := srv.rodXPBonuses(ctx, 1000001, "", 101)
	if err != nil || len(b) != 0 {
		t.Fatal("missing rod must not invent a bonus")
	}
}

func TestCatchSkillUsesRigAndReportsRemainingGain(t *testing.T) {
	for _, tc := range []struct {
		name             string
		rig, skill       int64
		bait             string
		before, wantGain float64
	}{
		{"float", 1, 1, "spinner_wrong_hint", 20, .2},
		{"lure", 5, 2, "worm", 30, .2},
		{"bottom", 12, 3, "spinner_wrong_hint", 40, .2},
		{"marine_pilker", 50, 11, "worm", 50, .2},
		{"marine_bottom", 55, 11, "worm", 99.9, .1},
		{"capped", 12, 3, "worm", 100, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, db := newProgressSrv(t)
			ctx := context.Background()
			const uid = int64(1000001)
			if _, err := db.EnsureProfile(ctx, uid); err != nil {
				t.Fatal(err)
			}
			if _, err := db.SQL.Exec(`INSERT INTO skill(uid,skill_id,points) VALUES(?,?,?)`, uid, tc.skill, tc.before); err != nil {
				t.Fatal(err)
			}
			st := srv.stateFor(uid)
			st.mu.Lock()
			st.RigType = tc.rig
			st.mu.Unlock()
			fish := &RolledFish{FishGUID: newFishGUID(), Species: "perch", Grade: 2, WeightG: 642}
			result := srv.settleCatchXP(ctx, uid, fish, tc.rig)
			if result == nil || result.skillID != tc.skill || result.skillGain < 0 || result.skillGain > tc.wantGain+1e-6 {
				t.Fatalf("wrong skill result: %+v", result)
			}
			var points float64
			if err := db.SQL.QueryRow(`SELECT points FROM skill WHERE uid=? AND skill_id=?`, uid, tc.skill).Scan(&points); err != nil || math.Abs(points-(tc.before+result.skillGain)) > 1e-6 {
				t.Fatalf("points=%v err=%v", points, err)
			}
			block := srv.buildSettle813(ctx, uid, fish, result).Field(8).Val
			field := block.Field(3)
			if math.Abs(float64(proto.F32FromBits(field.U32()))-result.skillGain) > 1e-6 {
				t.Fatal("displayed skill gain differs from saved gain")
			}
		})
	}
}
