package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"rf4server/internal/config"
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
