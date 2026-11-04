package gameserver

import (
	"context"
	"testing"
)

func TestCatchSettlementReplayUsesCapturedGear(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rodA, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	rodB, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.GearGuid = rodA.GUID
	st.RigType = 16
	st.mu.Unlock()

	fish := &RolledFish{FishGUID: "10101010101010101010101010101010", GearGUID: rodA.GUID, RigType: 16,
		Species: "perch", Grade: 2, WeightG: 642, LengthM: .28, LocLevel: "level_001_lake"}
	first := srv.settleCatchXP(ctx, uid, fish, 16)
	if first == nil || first.replayed || fish.XP <= 0 {
		t.Fatalf("first settlement failed: result=%+v fish=%+v", first, fish)
	}
	profileA, _ := db.GetProfile(ctx, uid)
	rodAAfter, _ := db.GetItemByGUID(ctx, rodA.GUID)
	rodBAfter, _ := db.GetItemByGUID(ctx, rodB.GUID)

	st.mu.Lock()
	st.GearGuid = rodB.GUID
	st.RigType = 1
	st.mu.Unlock()
	second := srv.settleCatchXP(ctx, uid, fish, 1)
	if second == nil || !second.replayed || second.totalXP != first.totalXP {
		t.Fatalf("settlement replay changed receipt: first=%+v second=%+v", first, second)
	}
	profileB, _ := db.GetProfile(ctx, uid)
	rodAAfterReplay, _ := db.GetItemByGUID(ctx, rodA.GUID)
	rodBAfterReplay, _ := db.GetItemByGUID(ctx, rodB.GUID)
	if profileB.XP != profileA.XP || rodAAfterReplay.Durability != rodAAfter.Durability || rodBAfterReplay.Durability != rodBAfter.Durability {
		t.Fatalf("replay changed XP/wear: xp=%d/%d rodA=%g/%g rodB=%g/%g",
			profileA.XP, profileB.XP, rodAAfter.Durability, rodAAfterReplay.Durability,
			rodBAfter.Durability, rodBAfterReplay.Durability)
	}
}

func TestCatchSettlementRollsBackOnReceiptFailure(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	before, _ := db.GetProfile(ctx, uid)
	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`CREATE TRIGGER fail_settlement_receipt BEFORE INSERT ON kv
		WHEN NEW.k LIKE 'catch-settlement:%' BEGIN SELECT RAISE(ABORT,'receipt failure'); END`); err != nil {
		t.Fatal(err)
	}
	fish := &RolledFish{FishGUID: "20202020202020202020202020202020", GearGUID: rod.GUID, RigType: 1,
		Species: "perch", Grade: 2, WeightG: 642}
	if result := srv.settleCatchXP(ctx, uid, fish, 1); result != nil {
		t.Fatal("receipt failure must not report success")
	}
	after, _ := db.GetProfile(ctx, uid)
	rodAfter, _ := db.GetItemByGUID(ctx, rod.GUID)
	var skills, transactions, damage int
	_ = db.SQL.QueryRow(`SELECT COUNT(*) FROM skill WHERE uid=?`, uid).Scan(&skills)
	_ = db.SQL.QueryRow(`SELECT COUNT(*) FROM txn WHERE uid=?`, uid).Scan(&transactions)
	_ = db.SQL.QueryRow(`SELECT COUNT(*) FROM item_damage_log WHERE uid=?`, uid).Scan(&damage)
	if after.XP != before.XP || skills != 0 || transactions != 0 || damage != 0 || fish.XP != 0 || rodAfter.Durability != rod.Durability {
		t.Fatalf("settlement was not rolled back: xp=%d skills=%d txns=%d damage=%d fishXP=%d", after.XP, skills, transactions, damage, fish.XP)
	}
}
