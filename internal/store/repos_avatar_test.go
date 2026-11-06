package store

import (
	"context"
	"errors"
	"testing"
)

func TestPlayerAvatarLookupAndPersistence(t *testing.T) {
	db := openIntegrityTestDB(t)
	ctx := context.Background()

	first, err := db.EnsureProfile(ctx, 1000001)
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.EnsureProfile(ctx, 1000002)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.ExecContext(ctx, `UPDATE profile SET name='angler2' WHERE uid=?`, second.UID); err != nil {
		t.Fatal(err)
	}

	got, err := db.FindUniqueProfileByName(ctx, first.Name)
	if err != nil || got.UID != first.UID {
		t.Fatalf("FindUniqueProfileByName(%q)=%+v, err=%v", first.Name, got, err)
	}
	if _, err := db.FindUniqueProfileByName(ctx, "missing"); !errors.Is(err, ErrPlayerNameNotFound) {
		t.Fatalf("missing username error=%v, want ErrPlayerNameNotFound", err)
	}
	if err := db.SetPlayerAvatar(ctx, 9999999, "missing", "http://example/avatar.png"); !errors.Is(err, ErrPlayerNameNotFound) {
		t.Fatalf("unknown UID error=%v, want ErrPlayerNameNotFound", err)
	}

	firstURL := "http://example/1000001-avatar.png"
	secondURL := "http://example/1000002-avatar.png"
	if err := db.SetPlayerAvatar(ctx, first.UID, first.Name, firstURL); err != nil {
		t.Fatal(err)
	}
	if err := db.SetPlayerAvatar(ctx, second.UID, "angler2", secondURL); err != nil {
		t.Fatal(err)
	}
	for uid, want := range map[int64]string{first.UID: firstURL, second.UID: secondURL} {
		got, err := db.PlayerAvatarURL(ctx, uid)
		if err != nil || got != want {
			t.Errorf("PlayerAvatarURL(%d)=%q, err=%v; want %q", uid, got, err, want)
		}
	}

	if _, err := db.EnsureProfile(ctx, 1000003); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FindUniqueProfileByName(ctx, "PLAYER"); !errors.Is(err, ErrAmbiguousPlayerName) {
		t.Fatalf("duplicate username error=%v, want ErrAmbiguousPlayerName", err)
	}
}
