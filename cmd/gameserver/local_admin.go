package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"

	"rf4server/internal/admin"
	"rf4server/internal/config"
	"rf4server/internal/store"
)

// 本地管理后台和游戏共用存档，随父进程一起退出。
func startLocalAdmin(ctx context.Context, db *store.DB, log *slog.Logger) (*admin.Server, <-chan error, error) {
	addr := os.Getenv("RF4_LOCAL_ADMIN_ADDR")
	if addr == "" {
		return nil, nil, nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return nil, nil, fmt.Errorf("管理后台只允许监听本机地址")
	}
	password, secret := os.Getenv("RF4_LOCAL_ADMIN_PASSWORD"), os.Getenv("RF4_LOCAL_ADMIN_SECRET")
	if len(password) < 6 || len(secret) < 32 {
		return nil, nil, fmt.Errorf("本地管理凭据未配置")
	}
	if _, err := db.UpsertAdminPassword(ctx, "admin", password); err != nil {
		return nil, nil, err
	}
	srv := admin.NewServer(config.Admin{HTTPAddr: addr, JWTSecret: secret}, db, log, admin.WebFS())
	errors, err := srv.Start()
	return srv, errors, err
}
