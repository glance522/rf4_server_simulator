package gameserver

import (
	"context"
	"testing"
)

func TestProfile307SuppressesEmailBindingPrompt(t *testing.T) {
	db := connectTestDB(t)
	srv := NewServerForGearTest(t, db)
	srv.cfg.AvatarURL = "http://127.0.0.1:8090/avatars/avatar.png"
	ctx := context.Background()
	const uid = int64(1000001)
	profile, err := db.EnsureProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	const avatarURL = "http://127.0.0.1:8090/avatars/1000001.png"
	if err := db.SetPlayerAvatar(ctx, uid, profile.Name, avatarURL); err != nil {
		t.Fatal(err)
	}
	p, err := srv.BuildProfile307(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if flags := u32Of(p.Fields[15].Num); flags&0x100 == 0 {
		t.Fatalf("307.f15 未跳过邮箱绑定提示: 0x%x", flags)
	}
<<<<<<< HEAD
	if p.Fields[9].Str == nil || *p.Fields[9].Str != avatarURL {
		t.Fatalf("307.f9 avatar=%v, want %q", p.Fields[9].Str, avatarURL)
=======
	if p.Fields[9].Str == nil || *p.Fields[9].Str != srv.cfg.AvatarURL {
		t.Fatalf("307.f9 avatar=%v, want %q", p.Fields[9].Str, srv.cfg.AvatarURL)
>>>>>>> f33dc6fbf75f7bed4504748b2ad037ecfc6e8aa4
	}
}
