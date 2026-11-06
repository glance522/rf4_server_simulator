package main

import (
	"bytes"
<<<<<<< HEAD
	"context"
=======
>>>>>>> f33dc6fbf75f7bed4504748b2ad037ecfc6e8aa4
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
<<<<<<< HEAD
	"net/url"
=======
>>>>>>> f33dc6fbf75f7bed4504748b2ad037ecfc6e8aa4
	"os"
	"path/filepath"
	"strings"
	"testing"

<<<<<<< HEAD
	"rf4server/internal/config"
	"rf4server/internal/store"
=======
	"gopkg.in/yaml.v3"
	"rf4server/internal/config"
>>>>>>> f33dc6fbf75f7bed4504748b2ad037ecfc6e8aa4
)

func TestHandleAvatarUploadAndServe(t *testing.T) {
	dir := t.TempDir()
<<<<<<< HEAD
	db, err := store.Open(filepath.Join(dir, "avatar.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	const firstUID = int64(1000001)
	firstProfile, err := db.EnsureProfile(context.Background(), firstUID)
	if err != nil {
		t.Fatal(err)
	}
	const secondUID = int64(1000002)
	if _, err := db.EnsureProfile(context.Background(), secondUID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET name='angler2' WHERE uid=?`, secondUID); err != nil {
=======
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("# preserve this comment\ngateway:\n  advertise_host: 127.0.0.1\ngame:\n  obs_addr: ':9502'\n"), 0o644); err != nil {
>>>>>>> f33dc6fbf75f7bed4504748b2ad037ecfc6e8aa4
		t.Fatal(err)
	}
	a := &app{
		cfg:        &config.Config{Gateway: config.Gateway{AdvertiseHost: "127.0.0.1"}},
<<<<<<< HEAD
		avatarDir:  filepath.Join(dir, "avatars"),
		avatarPort: 8090,
		adminDB:    db,
	}

	req := avatarUploadRequest(t, firstProfile.Name, "http://127.0.0.1:8090", testAvatarPNGWithColor(t, color.RGBA{R: 255, A: 255}))
=======
		cfgPath:    configPath,
		avatarDir:  filepath.Join(dir, "avatars"),
		avatarPort: 8090,
	}

	req := avatarUploadRequest(t, "http://127.0.0.1:8090/avatars/avatar.png", testAvatarPNG(t))
>>>>>>> f33dc6fbf75f7bed4504748b2ad037ecfc6e8aa4
	rr := httptest.NewRecorder()
	a.handleAvatarUpload(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", rr.Code, rr.Body.String())
	}
<<<<<<< HEAD
	var response struct {
		URL      string `json:"url"`
		Username string `json:"username"`
		UID      int64  `json:"uid"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	firstURL, err := url.Parse(response.URL)
	if err != nil || !strings.HasPrefix(firstURL.Path, avatarPathPrefix+"1000001-") || !strings.HasSuffix(firstURL.Path, ".png") {
		t.Fatalf("avatar URL=%q", response.URL)
	}
	if response.Username != firstProfile.Name || response.UID != firstUID {
		t.Fatalf("upload response=%v", response)
	}
	avatarURL, err := db.PlayerAvatarURL(context.Background(), firstUID)
	if err != nil || avatarURL != response.URL {
		t.Fatalf("stored first avatar=%q err=%v, want %q", avatarURL, err, response.URL)
	}
	assertAvatarUpdateQueued(t, db, firstUID)

	rr = httptest.NewRecorder()
	a.handleAvatarImage(rr, httptest.NewRequest(http.MethodGet, firstURL.Path, nil))
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("image response status=%d content-type=%q", rr.Code, rr.Header().Get("Content-Type"))
	}
	served, err := png.Decode(bytes.NewReader(rr.Body.Bytes()))
	if err != nil {
		t.Fatalf("served avatar is not a PNG: %v", err)
	}
	r, g, b, _ := served.At(0, 0).RGBA()
	if r != 0xffff || g != 0 || b != 0 {
		t.Fatalf("served first avatar pixel=(%x,%x,%x), want red", r, g, b)
	}

	req = avatarUploadRequest(t, firstProfile.Name, "http://127.0.0.1:8090", testAvatarPNGWithColor(t, color.RGBA{B: 255, A: 255}))
	rr = httptest.NewRecorder()
	a.handleAvatarUpload(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("second upload status=%d body=%s", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.URL == firstURL.String() {
		t.Fatalf("replaced avatar kept cached URL %q", response.URL)
	}
	firstBlueURL := response.URL
	assertAvatarUpdateQueued(t, db, firstUID)
	rr = httptest.NewRecorder()
	a.handleAvatarImage(rr, httptest.NewRequest(http.MethodGet, mustURLPath(t, firstBlueURL), nil))
	served, err = png.Decode(bytes.NewReader(rr.Body.Bytes()))
	if rr.Code != http.StatusOK || err != nil {
		t.Fatalf("second served avatar status=%d decode err=%v", rr.Code, err)
	}
	r, g, b, _ = served.At(0, 0).RGBA()
	if b != 0xffff || r != 0 || g != 0 {
		t.Fatalf("served second avatar pixel=(%x,%x,%x), want blue", r, g, b)
	}

	req = avatarUploadRequest(t, "angler2", "http://127.0.0.1:8090", testAvatarPNGWithColor(t, color.RGBA{G: 255, A: 255}))
	rr = httptest.NewRecorder()
	a.handleAvatarUpload(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("second player upload status=%d body=%s", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.UID != secondUID || response.URL == firstBlueURL {
		t.Fatalf("second player avatar response=%v", response)
	}
	assertAvatarUpdateQueued(t, db, secondUID)
	firstAvatarURL, err := db.PlayerAvatarURL(context.Background(), firstUID)
	if err != nil || firstAvatarURL != firstBlueURL {
		t.Fatalf("upload for second player changed first player's avatar: url=%q err=%v", firstAvatarURL, err)
	}
}

func assertAvatarUpdateQueued(t *testing.T, db *store.DB, wantUID int64) {
	t.Helper()
	raw, ok, err := db.QueuePop(context.Background())
	if err != nil || !ok {
		t.Fatalf("avatar update command missing: ok=%v err=%v", ok, err)
	}
	var cmd struct {
		Op  string `json:"op"`
		UID int64  `json:"uid"`
	}
	if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.Op != "refresh_player" || cmd.UID != wantUID {
		t.Fatalf("avatar refresh command=%+v, want UID %d", cmd, wantUID)
	}
=======
	var response map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["url"] != "http://127.0.0.1:8090/avatars/avatar.png" {
		t.Fatalf("avatar URL=%q", response["url"])
	}

	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(configData), "# preserve this comment") {
		t.Fatalf("config comment not preserved:\n%s", configData)
	}
	var cfg config.Config
	if err := yaml.Unmarshal(configData, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Game.AvatarURL != response["url"] || a.cfg.Game.AvatarURL != response["url"] {
		t.Fatalf("persisted/in-memory avatar URL mismatch: config=%q app=%q", cfg.Game.AvatarURL, a.cfg.Game.AvatarURL)
	}

	rr = httptest.NewRecorder()
	a.handleAvatarImage(rr, httptest.NewRequest(http.MethodGet, avatarImagePath, nil))
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("image response status=%d content-type=%q", rr.Code, rr.Header().Get("Content-Type"))
	}
	if _, err := png.DecodeConfig(bytes.NewReader(rr.Body.Bytes())); err != nil {
		t.Fatalf("served avatar is not a PNG: %v", err)
	}
>>>>>>> f33dc6fbf75f7bed4504748b2ad037ecfc6e8aa4
}

func TestHandleAvatarUploadRejectsInvalidImageAndCrossOrigin(t *testing.T) {
	a := &app{
		cfg:       &config.Config{Gateway: config.Gateway{AdvertiseHost: "127.0.0.1"}},
<<<<<<< HEAD
		avatarDir: filepath.Join(t.TempDir(), "avatars"),
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "avatar.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnsureProfile(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	a.adminDB = db

	req := avatarUploadRequest(t, "player", "", []byte("not an image"))
=======
		cfgPath:   filepath.Join(t.TempDir(), "config.yaml"),
		avatarDir: filepath.Join(t.TempDir(), "avatars"),
	}
	if err := os.WriteFile(a.cfgPath, []byte("game: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := avatarUploadRequest(t, "", []byte("not an image"))
>>>>>>> f33dc6fbf75f7bed4504748b2ad037ecfc6e8aa4
	rr := httptest.NewRecorder()
	a.handleAvatarUpload(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid image status=%d body=%s", rr.Code, rr.Body.String())
	}

<<<<<<< HEAD
	req = avatarUploadRequest(t, "player", "", testAvatarPNG(t))
=======
	req = avatarUploadRequest(t, "", testAvatarPNG(t))
>>>>>>> f33dc6fbf75f7bed4504748b2ad037ecfc6e8aa4
	req.Header.Set("Origin", "http://evil.example")
	rr = httptest.NewRecorder()
	a.handleAvatarUpload(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(a.avatarDir, "avatar.png")); !os.IsNotExist(err) {
		t.Fatalf("rejected upload created an avatar: err=%v", err)
	}
}

<<<<<<< HEAD
func avatarUploadRequest(t *testing.T, username, baseURL string, data []byte) *http.Request {
=======
func avatarUploadRequest(t *testing.T, avatarURL string, data []byte) *http.Request {
>>>>>>> f33dc6fbf75f7bed4504748b2ad037ecfc6e8aa4
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("image", "avatar.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
<<<<<<< HEAD
	if username != "" {
		if err := writer.WriteField("username", username); err != nil {
			t.Fatal(err)
		}
	}
	if baseURL != "" {
		if err := writer.WriteField("base_url", baseURL); err != nil {
=======
	if avatarURL != "" {
		if err := writer.WriteField("url", avatarURL); err != nil {
>>>>>>> f33dc6fbf75f7bed4504748b2ad037ecfc6e8aa4
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := panelPOSTRequest("/api/avatar/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

<<<<<<< HEAD
func mustURLPath(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Path
}

func testAvatarPNG(t *testing.T) []byte {
	return testAvatarPNGWithColor(t, color.RGBA{R: 255, A: 255})
}

func testAvatarPNGWithColor(t *testing.T, pixel color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, pixel)
=======
func testAvatarPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
>>>>>>> f33dc6fbf75f7bed4504748b2ad037ecfc6e8aa4
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}
