package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"rf4server/internal/store"

	"gopkg.in/yaml.v3"
	maxAvatarUploadBytes = 5 << 20
	maxAvatarPixels      = 16_000_000

	avatarPathPrefix     = "/avatars/"

	avatarImagePath      = "/avatars/avatar.png"
)

func (a *app) handleAvatarUpload(w http.ResponseWriter, r *http.Request) {
	if !requireLocalPost(w, r, "multipart/form-data") {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAvatarUploadBytes+(64<<10))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "上传数据无效或超过 5 MiB"})
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	file, _, err := r.FormFile("image")
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "请选择要上传的 PNG、JPEG 或 GIF 图片"})
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxAvatarUploadBytes+1))
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "读取上传图片失败"})
		return
	}
	if len(data) == 0 || len(data) > maxAvatarUploadBytes {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "图片为空或超过 5 MiB"})
		return
	}
	pngData, err := normalizeAvatarPNG(data)
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

<<<<<<< HEAD
	username := strings.TrimSpace(r.FormValue("username"))
	if err := validateAvatarUsername(username); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	profile, err := a.findAvatarProfile(username)
	if err != nil {
		status := http.StatusBadRequest
		if !errors.Is(err, store.ErrPlayerNameNotFound) && !errors.Is(err, store.ErrAmbiguousPlayerName) {
			status = http.StatusInternalServerError
		}
		writeJSONStatus(w, status, map[string]string{"error": err.Error()})
		return
	}

	baseURL := strings.TrimSpace(r.FormValue("base_url"))
	if baseURL == "" {
		baseURL = a.suggestedAvatarBaseURL()
	}
	if err := validateAvatarBaseURL(baseURL); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	filename := avatarFileName(profile.UID, pngData)
	avatarURL, err := avatarURLFor(baseURL, filename)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "构造头像地址失败: " + err.Error()})
		return
	}
=======
	avatarURL := strings.TrimSpace(r.FormValue("url"))
	if avatarURL == "" {
		avatarURL = a.suggestedAvatarURL()
	}
	if err := validateAvatarURL(avatarURL); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if a.cfgPath == "" {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "未指定配置文件路径"})
		return
	}

	a.avatarMu.Lock()
	defer a.avatarMu.Unlock()
>>>>>>> f33dc6fbf75f7bed4504748b2ad037ecfc6e8aa4
	dir := a.avatarDir
	if dir == "" {
		dir = "avatars"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "创建头像目录失败: " + err.Error()})
		return
	}
<<<<<<< HEAD
	a.avatarMu.Lock()
	defer a.avatarMu.Unlock()
	if err := replaceAvatarFile(filepath.Join(dir, filename), pngData); err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "保存头像失败: " + err.Error()})
		return
	}
	if err := a.savePlayerAvatar(profile.UID, profile.Name, avatarURL); err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "图片已上传，但保存玩家头像失败: " + err.Error()})
		return
	}
	if err := a.enqueueAvatarRefresh(profile.UID); err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{
			"error": "玩家头像已保存，但在线刷新指令投递失败；重新登录后生效: " + err.Error(),
		})
		return
	}
	writeJSON(w, map[string]any{"url": avatarURL, "username": profile.Name, "uid": profile.UID})
}

func (a *app) findAvatarProfile(username string) (*store.Profile, error) {
	a.adminMu.RLock()
	defer a.adminMu.RUnlock()
	if a.adminDB == nil {
		return nil, fmt.Errorf("管理数据库尚未就绪")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return a.adminDB.FindUniqueProfileByName(ctx, username)
}

func (a *app) savePlayerAvatar(uid int64, username, avatarURL string) error {
	a.adminMu.RLock()
	defer a.adminMu.RUnlock()
	if a.adminDB == nil {
		return fmt.Errorf("管理数据库尚未就绪")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.adminDB.SetPlayerAvatar(ctx, uid, username, avatarURL); err != nil {
		return err
	}
	return nil
}

func (a *app) enqueueAvatarRefresh(uid int64) error {
	a.adminMu.RLock()
	defer a.adminMu.RUnlock()
	if a.adminDB == nil {
		return fmt.Errorf("管理数据库尚未就绪")
	}
	payload, err := json.Marshal(struct {
		Op  string `json:"op"`
		UID int64  `json:"uid"`
	}{Op: "refresh_player", UID: uid})
	if err != nil {
		return fmt.Errorf("编码档案刷新指令: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.adminDB.QueuePush(ctx, string(payload)); err != nil {
		return fmt.Errorf("写入档案刷新队列: %w", err)
	}
	return nil
}

func (a *app) handleAvatarImage(w http.ResponseWriter, r *http.Request) {
	filename := strings.TrimPrefix(r.URL.Path, avatarPathPrefix)
	if !strings.HasPrefix(r.URL.Path, avatarPathPrefix) || !validAvatarFilename(filename) {
=======
	if err := replaceAvatarFile(filepath.Join(dir, "avatar.png"), pngData); err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "保存头像失败: " + err.Error()})
		return
	}
	if err := persistAvatarURL(a.cfgPath, avatarURL); err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "头像已上传，但保存配置失败: " + err.Error()})
		return
	}

	if a.cfg != nil {
		a.cfg.Game.AvatarURL = avatarURL
	}
	writeJSON(w, map[string]string{"url": avatarURL})
}

func (a *app) handleAvatarImage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != avatarImagePath {
>>>>>>> f33dc6fbf75f7bed4504748b2ad037ecfc6e8aa4
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "仅支持 GET 和 HEAD", http.StatusMethodNotAllowed)
		return
	}

	dir := a.avatarDir
	if dir == "" {
		dir = "avatars"
	}
<<<<<<< HEAD
	file, err := os.Open(filepath.Join(dir, filename))
=======
	file, err := os.Open(filepath.Join(dir, "avatar.png"))
>>>>>>> f33dc6fbf75f7bed4504748b2ad037ecfc6e8aa4
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "读取头像失败", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		http.Error(w, "读取头像信息失败", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
<<<<<<< HEAD
	http.ServeContent(w, r, filename, info.ModTime(), file)
}

func (a *app) suggestedAvatarBaseURL() string {
=======
	http.ServeContent(w, r, "avatar.png", info.ModTime(), file)
}

func (a *app) suggestedAvatarURL() string {
>>>>>>> f33dc6fbf75f7bed4504748b2ad037ecfc6e8aa4
	host := ""
	if a.cfg != nil {
		host = strings.Trim(strings.TrimSpace(a.cfg.Gateway.AdvertiseHost), "[]")
	}
	if host == "" {
		host = "127.0.0.1"
	}
	port := a.avatarPort
	if port <= 0 {
		port = 8090
	}
	return (&url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
<<<<<<< HEAD
	}).String()
}

func validateAvatarBaseURL(raw string) error {
	u, err := url.ParseRequestURI(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("图片服务地址必须是完整的 HTTP 地址，例如 http://127.0.0.1:8090")
=======
		Path:   avatarImagePath,
	}).String()
}

func validateAvatarURL(raw string) error {
	u, err := url.ParseRequestURI(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() == "" || u.User != nil ||
		u.Path != avatarImagePath || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("头像地址必须是完整的 HTTP 地址，并以 %s 结尾", avatarImagePath)
>>>>>>> f33dc6fbf75f7bed4504748b2ad037ecfc6e8aa4
	}
	return nil
}

<<<<<<< HEAD
func validateAvatarUsername(username string) error {
	if username == "" || len(username) > 128 {
		return fmt.Errorf("请输入有效的玩家用户名（最多 128 字节）")
	}
	for _, r := range username {
		if unicode.IsControl(r) {
			return fmt.Errorf("用户名不能包含控制字符")
		}
	}
	return nil
}

func avatarFileName(uid int64, data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%d-%s.png", uid, hex.EncodeToString(sum[:]))
}

func validAvatarFilename(filename string) bool {
	if strings.ContainsAny(filename, `/\`) || !strings.HasSuffix(filename, ".png") {
		return false
	}
	stem := strings.TrimSuffix(filename, ".png")
	parts := strings.Split(stem, "-")
	if len(parts) != 2 || len(parts[1]) != sha256.Size*2 {
		return false
	}
	uid, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || uid <= 0 {
		return false
	}
	_, err = hex.DecodeString(parts[1])
	return err == nil && filename == fmt.Sprintf("%d-%s.png", uid, parts[1])
}

func avatarURLFor(baseURL, filename string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("解析图片服务地址: %w", err)
	}
	u.Path = avatarPathPrefix + filename
	return u.String(), nil
}

=======
>>>>>>> f33dc6fbf75f7bed4504748b2ad037ecfc6e8aa4
func normalizeAvatarPNG(data []byte) ([]byte, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg" && format != "gif") {
		return nil, fmt.Errorf("图片格式无效，仅支持 PNG、JPEG 或 GIF")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxAvatarPixels/cfg.Height {
		return nil, fmt.Errorf("图片尺寸无效或超过 1600 万像素")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("图片内容无法解码")
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, fmt.Errorf("转换 PNG 失败: %w", err)
	}
	if out.Len() > maxAvatarUploadBytes {
		return nil, fmt.Errorf("转换后的 PNG 超过 5 MiB")
	}
	return out.Bytes(), nil
}

func replaceAvatarFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".avatar-*.png")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err == nil {
		return nil
	} else if _, statErr := os.Lstat(path); statErr != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

<<<<<<< HEAD
=======
func persistAvatarURL(configPath, avatarURL string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if strings.TrimSpace(string(data)) != "" {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return err
		}
	}
	if doc.Kind == 0 {
		doc.Kind = yaml.DocumentNode
	}
	if len(doc.Content) == 0 {
		doc.Content = append(doc.Content, &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("配置根节点不是 YAML 映射")
	}
	game := mappingValue(root, "game")
	if game == nil {
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "game"},
			&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"},
		)
		game = root.Content[len(root.Content)-1]
	}
	if game.Kind != yaml.MappingNode {
		return fmt.Errorf("配置项 game 不是 YAML 映射")
	}
	setMappingValue(game, "avatar_url", avatarURL)
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, out, info.Mode().Perm())
}

>>>>>>> f33dc6fbf75f7bed4504748b2ad037ecfc6e8aa4
func requireLocalPost(w http.ResponseWriter, r *http.Request, expectedMediaType string) bool {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "仅支持 POST"})
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != expectedMediaType {
		writeJSONStatus(w, http.StatusUnsupportedMediaType, map[string]string{"error": "请求 Content-Type 无效"})
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		writeJSONStatus(w, http.StatusForbidden, map[string]string{"error": "仅允许本机控制中心调用"})
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			writeJSONStatus(w, http.StatusForbidden, map[string]string{"error": "拒绝跨站请求"})
			return false
		}
	}
	return true
}
