//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

const localHealth = "http://127.0.0.1:19214/api/health"

func backendReady(client *http.Client, url string) (bool, error) {
	response, err := client.Get(url)
	if err != nil {
		return false, nil
	}
	defer response.Body.Close()
	var health struct {
		App      string `json:"app"`
		Status   string `json:"status"`
		Protocol string `json:"gameProtocol"`
	}
	if json.NewDecoder(response.Body).Decode(&health) != nil || health.App != "rf4-local-prototype-v1" {
		return false, errors.New("19214端口由其他程序使用")
	}
	if response.StatusCode != http.StatusOK || health.Status != "ready" {
		return false, errors.New("本地后台未就绪，请重启本地服务")
	}
	if health.Protocol != "reference-experimental" {
		return false, errors.New("请先停止旧版本地后台，再启动游戏")
	}
	return health.Status == "ready", nil
}

func ensureBackend(root string) error {
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	ready, err := backendReady(client, localHealth)
	if err != nil || ready {
		return err
	}
	python := filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Python", "Python312", "pythonw.exe")
	script := filepath.Join(root, "local-service", "local_server.py")
	command := exec.Command(python, script, "--managed-backend")
	command.Dir = filepath.Dir(script)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	if err := command.Start(); err != nil {
		return fmt.Errorf("本地后台启动失败: %w", err)
	}
	defer command.Process.Release()
	for deadline := time.Now().Add(45 * time.Second); time.Now().Before(deadline); {
		ready, err = backendReady(client, localHealth)
		if err != nil || ready {
			return err
		}
		time.Sleep(250 * time.Millisecond)
	}
	_ = command.Process.Kill()
	return errors.New("本地后台未就绪，请检查角色导入和业务程序")
}

func run(root string) error {
	if err := ensureBackend(root); err != nil {
		return err
	}
	game := filepath.Join(root, "Game")
	command := exec.Command(filepath.Join(game, "rf4_x64.exe"), "-region", "CN", "-logFile", filepath.Join(root, "local-service", "data", "client.log"), "-screen-width", "1024", "-screen-height", "768", "-screen-fullscreen", "0")
	command.Dir = game
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

func main() {
	self, err := os.Executable()
	if err != nil {
		return
	}
	root := filepath.Dir(self)
	if err := run(root); err != nil {
		_ = os.WriteFile(filepath.Join(root, "local-service", "data", "start-error.txt"), []byte(err.Error()), 0600)
	}
}
