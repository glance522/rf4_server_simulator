package gameserver

import (
	"strings"
	"testing"
)

func TestGearNamedCommandsOverrideNumericSubmenu(t *testing.T) {
	env := newDLineTestEnv(t, 1000001)
	if _, err := env.db.EnsureProfile(t.Context(), env.sess.Userid); err != nil {
		t.Fatal(err)
	}
	sendGearAssistantForTest(t, env.srv, env.sess, "2")
	if env.srv.gearMenuGet(env.sess.Userid) != "stamina" {
		t.Fatal("numeric submenu missing")
	}
	sendGearAssistantForTest(t, env.srv, env.sess, "满红不爆 开")
	on, err := readOverloadProtection(t.Context(), env.db.SQL, env.sess.Userid)
	if err != nil || !on {
		t.Fatalf("named command ignored in submenu: %v %v", on, err)
	}
	if env.srv.gearMenuGet(env.sess.Userid) != "overload" {
		t.Fatal("named command did not switch to its submenu")
	}
	sendGearAssistantForTest(t, env.srv, env.sess, "五速鞋 开 多余")
	on, err = readGearFeatureFlag(t.Context(), env.db.SQL, env.sess.Userid, "shoes")
	if err != nil || on {
		t.Fatalf("malformed command changed shoes: %v %v", on, err)
	}
	sendGearAssistantForTest(t, env.srv, env.sess, "耐力播报 开")
	on, err = readGearFeatureFlag(t.Context(), env.db.SQL, env.sess.Userid, "stamina")
	if err != nil || !on {
		t.Fatalf("full stamina name was not recognized: %v %v", on, err)
	}
	sendGearAssistantForTest(t, env.srv, env.sess, "五速鞋")
	if env.srv.gearMenuGet(env.sess.Userid) != "shoes" {
		t.Fatal("named query did not use the feature submenu")
	}
}

func TestGearShoesSavedSnapshotAndSilentSync(t *testing.T) {
	env := newDLineTestEnv(t, 1000001)
	if _, err := env.db.EnsureProfile(t.Context(), env.sess.Userid); err != nil {
		t.Fatal(err)
	}
	on, err := readGearFeatureFlag(t.Context(), env.db.SQL, env.sess.Userid, "shoes")
	if err != nil || on {
		t.Fatalf("shoes must default off: %v %v", on, err)
	}
	sendGearAssistantForTest(t, env.srv, env.sess, "五速鞋 开")
	sendGearAssistantForTest(t, env.srv, env.sess, "五速鞋 开")
	var normalReply string
	if err := env.db.SQL.QueryRow(`SELECT text FROM mail WHERE uid=? AND sender=? ORDER BY id DESC LIMIT 1`, env.sess.Userid, gearAssistantUID).Scan(&normalReply); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(normalReply, "\n五速鞋：") || !strings.Contains(normalReply, "已经开启") || !strings.Contains(normalReply, "开(y)") {
		t.Fatalf("normal acknowledgement changed framework format: %q", normalReply)
	}
	sendGearAssistantForTest(t, env.srv, env.sess, "b")
	if err := env.db.SQL.QueryRow(`SELECT text FROM mail WHERE uid=? AND sender=? ORDER BY id DESC LIMIT 1`, env.sess.Userid, gearAssistantUID).Scan(&normalReply); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(normalReply, "\n五速鞋：") || !strings.Contains(normalReply, gearSep) {
		t.Fatalf("menu gained a settings footer or lost original separators: %q", normalReply)
	}
	on, err = readGearFeatureFlag(t.Context(), env.db.SQL, env.sess.Userid, "shoes")
	if err != nil || !on {
		t.Fatalf("setting not persisted: %v %v", on, err)
	}
	other, err := readGearFeatureFlag(t.Context(), env.db.SQL, env.sess.Userid+1, "shoes")
	if err != nil || other {
		t.Fatalf("setting leaked to another player: %v %v", other, err)
	}
	if overload, err := readOverloadProtection(t.Context(), env.db.SQL, env.sess.Userid); err != nil || overload {
		t.Fatalf("shoes changed overload: %v %v", overload, err)
	}
	sendGearAssistantForTest(t, env.srv, env.sess, "1")
	sendGearAssistantForTest(t, env.srv, env.sess, "同步设置")
	if env.srv.gearMenuGet(env.sess.Userid) != "overload" {
		t.Fatal("sync changed numeric submenu")
	}
	var text string
	var read int
	if err := env.db.SQL.QueryRow(`SELECT text, read FROM mail WHERE uid=? AND sender=? ORDER BY id DESC LIMIT 1`, env.sess.Userid, gearAssistantUID).Scan(&text, &read); err != nil {
		t.Fatal(err)
	}
	if read != 1 || !strings.HasPrefix(text, "钓具设置已同步。\n") || !strings.HasSuffix(text, "\n五速鞋：开启") {
		t.Fatalf("invalid silent snapshot: read=%d text=%q", read, text)
	}
	sendGearAssistantForTest(t, env.srv, env.sess, "五速鞋 关")
	on, err = readGearFeatureFlag(t.Context(), env.db.SQL, env.sess.Userid, "shoes")
	if err != nil || on {
		t.Fatalf("shoes remained on: %v %v", on, err)
	}
}
