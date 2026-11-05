package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/store"
)

func TestManagedAdminMountAndParentExit(t *testing.T) {
	cfg := &config.Config{}
	cfg.SQLite.Path = filepath.Join(t.TempDir(), "game.db")
	cfg.SQLite.StaticPath = filepath.Join("..", "..", "server_data", "static.sqlite3")
	cfg.Admin.Username, cfg.Admin.Password, cfg.Admin.JWTSecret = "admin", "test-password", "test-secret"
	ctx, cancel := context.WithCancel(context.Background())
	a := &app{cfg: cfg, ctx: ctx, cancel: cancel, log: panelLogger{}}
	defer a.closeAdmin()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	parent, writer := io.Pipe()
	defer writer.Close()
	done := make(chan error, 1)
	go func() { done <- a.serveManagedAdmin(parent, addr) }()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	ready := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		response, err := client.Get("http://" + addr + "/admin/api/me")
		if err == nil {
			_ = response.Body.Close()
			ready = response.StatusCode == http.StatusUnauthorized
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready || a.sup != nil {
		t.Fatal("管理入口未就绪或启动了额外游戏服务")
	}
	response, err := client.Get("http://" + addr + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		AdminReady bool `json:"admin_ready"`
	}
	err = json.NewDecoder(response.Body).Decode(&status)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || !status.AdminReady {
		t.Fatalf("管理入口健康检查失败: status=%d ready=%v err=%v", response.StatusCode, status.AdminReady, err)
	}
	_ = writer.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("父进程退出后管理入口未停止")
	}
}

func TestManagedAdminRejectsRemoteListener(t *testing.T) {
	a := &app{}
	if err := a.serveManagedAdmin(nil, "0.0.0.0:9500"); err == nil {
		t.Fatal("不应监听外部地址")
	}
}

func TestManagedAdminPreservesExistingPasswordAcrossStarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureAdmin(t.Context(), "owner", "original-password"); err != nil {
		t.Fatal(err)
	}
	var originalHash string
	if err := db.SQL.QueryRow(`SELECT password_hash FROM admin_user WHERE username='owner'`).Scan(&originalHash); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"owner", "owner", "admin"} {
		cfg := &config.Config{}
		cfg.SQLite.Path, cfg.SQLite.StaticPath = path, filepath.Join("..", "..", "server_data", "static.sqlite3")
		cfg.Admin.Username, cfg.Admin.Password, cfg.Admin.JWTSecret = user, "different-config-password", "synthetic-jwt-secret"
		ctx, cancel := context.WithCancel(t.Context())
		a := &app{cfg: cfg, ctx: ctx, cancel: cancel, log: panelLogger{}}
		if err := a.serveManagedAdmin(strings.NewReader(""), "127.0.0.1:0"); err != nil {
			a.closeAdmin()
			t.Fatal(err)
		}
		server := httptest.NewServer(http.HandlerFunc(a.handleAdmin))
		login := func(username, password string) int {
			t.Helper()
			payload, _ := json.Marshal(map[string]string{"username": username, "password": password})
			response, err := server.Client().Post(server.URL+"/admin/api/login", "application/json", strings.NewReader(string(payload)))
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			return response.StatusCode
		}
		if login("owner", "original-password") != http.StatusOK || login(user, cfg.Admin.Password) != http.StatusUnauthorized {
			t.Error("托管启动覆盖了原口令或绕过了认证")
		}
		var hash string
		var count int
		if err := db.SQL.QueryRow(`SELECT password_hash FROM admin_user WHERE username='owner'`).Scan(&hash); err != nil || hash != originalHash {
			t.Errorf("托管启动改变了原管理员密码: %v", err)
		}
		if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM admin_user`).Scan(&count); err != nil || count != 1 {
			t.Errorf("托管启动新增了配置管理员: count=%d err=%v", count, err)
		}
		server.Close()
		a.closeAdmin()
	}
}
