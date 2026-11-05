package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"rf4server/internal/config"
)

func TestHandleAvatarUploadAndServe(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("# preserve this comment\ngateway:\n  advertise_host: 127.0.0.1\ngame:\n  obs_addr: ':9502'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &app{
		cfg:        &config.Config{Gateway: config.Gateway{AdvertiseHost: "127.0.0.1"}},
		cfgPath:    configPath,
		avatarDir:  filepath.Join(dir, "avatars"),
		avatarPort: 8090,
	}

	req := avatarUploadRequest(t, "http://127.0.0.1:8090/avatars/avatar.png", testAvatarPNG(t))
	rr := httptest.NewRecorder()
	a.handleAvatarUpload(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", rr.Code, rr.Body.String())
	}
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
}

func TestHandleAvatarUploadRejectsInvalidImageAndCrossOrigin(t *testing.T) {
	a := &app{
		cfg:       &config.Config{Gateway: config.Gateway{AdvertiseHost: "127.0.0.1"}},
		cfgPath:   filepath.Join(t.TempDir(), "config.yaml"),
		avatarDir: filepath.Join(t.TempDir(), "avatars"),
	}
	if err := os.WriteFile(a.cfgPath, []byte("game: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := avatarUploadRequest(t, "", []byte("not an image"))
	rr := httptest.NewRecorder()
	a.handleAvatarUpload(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid image status=%d body=%s", rr.Code, rr.Body.String())
	}

	req = avatarUploadRequest(t, "", testAvatarPNG(t))
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

func avatarUploadRequest(t *testing.T, avatarURL string, data []byte) *http.Request {
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
	if avatarURL != "" {
		if err := writer.WriteField("url", avatarURL); err != nil {
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

func testAvatarPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}
