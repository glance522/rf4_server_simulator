package gameserver

import (
	"context"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func TestBreakReportCannotDamageUnrelatedRod(t *testing.T) {
	for _, name := range []string{"unknown", "foreign line", "unmounted line", "mounted reel", "other rod", "nonrod target"} {
		t.Run(name, func(t *testing.T) {
			srv, db := newRigTestServer(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)
			const uid = int64(1000001)
			rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
			if err != nil {
				t.Fatal(err)
			}
			line, err := srv.CreateItem(ctx, uid, "mono_40853", "backpack", 1)
			if err != nil {
				t.Fatal(err)
			}
			sess := newProbeSess(srv)
			mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
			beforeRod := mustItem(t, db, rod.GUID)
			beforeLength := lineLengthOfTestItem(t, srv, line.GUID)
			target, broken := rod.GUID, "ffffffffffffffffffffffffffffffff"
			switch name {
			case "foreign line", "unmounted line":
				owner := uid
				if name == "foreign line" {
					owner++
				}
				extra, err := srv.CreateItem(ctx, owner, "mono_40853", "backpack", 1)
				if err != nil {
					t.Fatal(err)
				}
				broken = extra.GUID
			case "mounted reel":
				key := "clas_20163"
				if name == "mounted reel" {
					key = "spin_5820"
				}
				extra, err := srv.CreateItem(ctx, uid, key, "backpack", 1)
				if err != nil {
					t.Fatal(err)
				}
				result := mountGearForTest(t, srv, sess, rod.GUID, extra.GUID)
				broken = result.MountedGUID
			case "other rod":
				extra, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
				if err != nil {
					t.Fatal(err)
				}
				broken = extra.GUID
			case "nonrod target":
				target, broken = line.GUID, line.GUID
			}
			if _, err := srv.breakLineOrRod(ctx, uid, target, broken); err == nil {
				t.Error("invalid or unsupported broken part accepted")
			}
			afterRod := mustItem(t, db, rod.GUID)
			if afterRod.Durability != beforeRod.Durability || lineLengthOfTestItem(t, srv, line.GUID) != beforeLength {
				t.Error("invalid report damaged rod or line")
			}
			logs, err := db.ListItemDamageLogs(ctx, uid, 100)
			if err != nil || len(logs) != 0 {
				t.Fatalf("damage history changed: %v %v", logs, err)
			}
		})
	}
}

func TestInvalidBreakReplyPreservesPendingCatch(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	sess := newProbeSess(srv)
	rod, err := srv.CreateItem(ctx, sess.Userid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	st := srv.stateFor(sess.Userid)
	fish := &RolledFish{FishGUID: "11111111111111111111111111111111", Species: "perch", WeightG: 321}
	st.mu.Lock()
	st.GearGuid, st.Pending, st.CastActive, st.Hooked = rod.GUID, fish, true, true
	st.mu.Unlock()
	srv.dispatch.Register(17, 9, hDynamicRodBreak)
	request := &Ctx{Sess: sess, Main: 17, Sub: 9, CallID: 73, ExpectResponse: true, DecodeOK: true,
		Tree: rigRequestTree(t, proto.Guid(rod.GUID), proto.Guid("ffffffffffffffffffffffffffffffff"), proto.U32(1), proto.Null()), Log: logging.Discard()}
	result := srv.dispatch.executeRequest(request, true)
	if !result.nativeFailure {
		t.Error("invalid break returned synthetic success")
	}
	st.mu.Lock()
	same := st.Pending == fish && st.CastActive && st.Hooked && st.GearGuid == rod.GUID
	st.mu.Unlock()
	if !same || mustItem(t, db, rod.GUID).Durability <= 0 {
		t.Fatal("invalid break discarded catch or broke rod")
	}
}

func TestStructuralRodLimitRemainsAfterDragFix(t *testing.T) {
	spec := gearPhysicsSpec{RodPowerKg: 5, LineStrengthKg: 25, LeaderStrengthKg: 20, ReelDragKg: 1}
	// 10kg * 0.35 = 3.5kg: above drag, below rod strength.
	if ok, part := canLandGear(spec, 10, .35); !ok || part != "" {
		t.Fatalf("drag treated as fracture: %v %s", ok, part)
	}
	// 20kg * 0.35 = 7kg: exceeds the 5kg rod's structural limit.
	if ok, part := canLandGear(spec, 20, .35); ok || part != "rod" {
		t.Fatalf("rod overload suppressed: %v %s", ok, part)
	}
}

func TestBrokenMountedHookDamagesOnlyHook(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	sess := newProbeSess(srv)
	rod, err := srv.CreateItem(ctx, sess.Userid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, sess.Userid, "mono_40853", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := srv.CreateItem(ctx, sess.Userid, "clas_20163", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	mounted := mountGearForTest(t, srv, sess, rod.GUID, hook.GUID)
	hook = mustItem(t, db, mounted.MountedGUID)
	beforeRod, beforeLine := mustItem(t, db, rod.GUID), mustItem(t, db, line.GUID)
	st := srv.stateFor(sess.Userid)
	st.mu.Lock()
	st.GearGuid, st.CastActive, st.Hooked = rod.GUID, true, true
	st.Pending = &RolledFish{FishGUID: "11111111111111111111111111111111", Species: "perch", WeightG: 321}
	st.mu.Unlock()
	body, ok := hDynamicRodBreak(&Ctx{Sess: sess, Main: 17, Sub: 9, ExpectResponse: true, DecodeOK: true,
		Tree: rigRequestTree(t, proto.Guid(rod.GUID), proto.Guid(hook.GUID), proto.Null()), Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatal("hook break did not return a native result")
	}
	decoded, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if u64Of(decoded.Fields[0].Num) != 0 || decoded.Fields[1].GuidHex() != hook.GUID {
		t.Fatal("hook break masqueraded as line or rod")
	}
	afterHook := mustItem(t, db, hook.GUID)
	if afterHook.Durability != 0 || afterHook.RigGUID != rod.GUID || afterHook.Container != "rig" {
		t.Fatal("broken hook did not persist in its original mount")
	}
	if *mustItem(t, db, rod.GUID) != *beforeRod || *mustItem(t, db, line.GUID) != *beforeLine {
		t.Fatal("hook break changed rod or line")
	}
	tree, err := srv.ItemToTree(ctx, afterHook)
	if err != nil {
		t.Fatal(err)
	}
	if u64Of(tree.Fields[fieldIdxByCS(afterHook.WireClass, csStrength)].Num) != 0 || !srv.itemBrokenForMount(ctx, afterHook) {
		t.Fatal("broken hook remained usable")
	}
	st.mu.Lock()
	preserved := st.GearGuid == rod.GUID && st.Pending == nil && !st.CastActive && !st.Hooked
	st.mu.Unlock()
	if !preserved {
		t.Fatal("hook break discarded the held rod or retained the fish")
	}
	logs, err := db.ListItemDamageLogs(ctx, sess.Userid, 100)
	if err != nil || len(logs) != 1 || logs[0].Event != damageEventHookBreak || logs[0].ItemGUID != hook.GUID {
		t.Fatalf("wrong damage history: %+v %v", logs, err)
	}
	var rodBreaks int64
	if err := db.SQL.QueryRow(`SELECT COALESCE(SUM(value),0) FROM player_counters WHERE uid=? AND key='rod_breaks'`, sess.Userid).Scan(&rodBreaks); err != nil || rodBreaks != 0 {
		t.Fatal("hook break counted as rod break")
	}
}

func TestHookDamageRollbackPreservesTackleAndFish(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	sess := newProbeSess(srv)
	rod, err := srv.CreateItem(ctx, sess.Userid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := srv.CreateItem(ctx, sess.Userid, "clas_20163", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	result := mountGearForTest(t, srv, sess, rod.GUID, hook.GUID)
	hook = mustItem(t, db, result.MountedGUID)
	beforeRod, beforeHook := *mustItem(t, db, rod.GUID), *hook
	st := srv.stateFor(sess.Userid)
	fish := &RolledFish{FishGUID: "22222222222222222222222222222222", Species: "perch", WeightG: 321}
	st.mu.Lock()
	st.GearGuid, st.Pending, st.CastActive, st.Hooked = rod.GUID, fish, true, true
	st.mu.Unlock()
	if _, err := db.SQL.Exec(`CREATE TRIGGER reject_hook_damage BEFORE INSERT ON item_damage_log BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	srv.dispatch.Register(17, 9, hDynamicRodBreak)
	reply := srv.dispatch.executeRequest(&Ctx{Sess: sess, Main: 17, Sub: 9, CallID: 71, ExpectResponse: true, DecodeOK: true,
		Tree: rigRequestTree(t, proto.Guid(rod.GUID), proto.Guid(hook.GUID), proto.Null()), Log: logging.Discard()}, true)
	if !reply.nativeFailure {
		t.Fatal("failed damage write returned success")
	}
	if *mustItem(t, db, rod.GUID) != beforeRod || *mustItem(t, db, hook.GUID) != beforeHook {
		t.Fatal("failed damage transaction changed tackle")
	}
	st.mu.Lock()
	same := st.GearGuid == rod.GUID && st.Pending == fish && st.CastActive && st.Hooked
	st.mu.Unlock()
	if !same {
		t.Fatal("failed hook damage discarded fishing state")
	}
}
