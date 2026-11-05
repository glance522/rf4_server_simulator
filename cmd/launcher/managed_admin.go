package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// 本地桥接共用 launcher 的管理 handler，不启动另一套管理服务。
func (a *app) serveManagedAdmin(parent io.Reader, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("管理入口只允许监听本机地址")
	}
	if err := a.initAdminWithPolicy(a.ctx, true); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/", a.handleAdmin)
	mux.HandleFunc("/api/status", a.handleStatus)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		_, _ = io.Copy(io.Discard, parent)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
