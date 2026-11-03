// Package portal serves the player page using the gateway's existing device identity.
package portal

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"rf4server/internal/store"
)

//go:embed web
var assets embed.FS

type session struct {
	uid     int64
	host    string
	expires time.Time
}

type Server struct {
	db       *store.DB
	resolve  func(string) int64
	mu       sync.Mutex
	sessions map[string]session
	mux      *http.ServeMux
}

// New uses the same source-address identity as game login; callers cannot choose a UID.
func New(db *store.DB, resolve func(string) int64) *Server {
	s := &Server{db: db, resolve: resolve, sessions: map[string]session{}, mux: http.NewServeMux()}
	s.mux.HandleFunc("POST /portal/api/session", s.login)
	s.mux.HandleFunc("DELETE /portal/api/session", s.logout)
	s.mux.HandleFunc("GET /portal/api/profile", s.profile)
	s.mux.HandleFunc("GET /portal/api/membership", s.membership)
	s.mux.HandleFunc("POST /portal/api/membership/purchase", s.purchase)
	s.mux.HandleFunc("/portal/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, 404, "接口不存在") })
	files, _ := fs.Sub(assets, "web")
	s.mux.Handle("/portal/", http.StripPrefix("/portal/", http.FileServer(http.FS(files))))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host || (u.Scheme != "https" && u.Scheme != "http") {
			fail(w, 403, "请求来源无效")
			return
		}
	}
	s.mux.ServeHTTP(w, r)
}

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
func write(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	uid := s.resolve(r.RemoteAddr)
	if _, err := s.db.GetProfile(r.Context(), uid); err != nil {
		fail(w, 404, "请先从当前设备进入游戏，创建角色后再打开个人中心")
		return
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		fail(w, 500, "会话创建失败")
		return
	}
	token := hex.EncodeToString(b[:])
	s.mu.Lock()
	for key, value := range s.sessions {
		if !time.Now().Before(value.expires) {
			delete(s.sessions, key)
		}
	}
	if len(s.sessions) >= 1024 {
		s.mu.Unlock()
		fail(w, 503, "会话较多，请稍后重试")
		return
	}
	s.sessions[token] = session{uid: uid, host: remoteHost(r), expires: time.Now().Add(time.Hour)}
	s.mu.Unlock()
	write(w, map[string]any{"token": token})
}

func (s *Server) identity(w http.ResponseWriter, r *http.Request) (int64, bool) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	value, ok := s.sessions[token]
	if ok && !time.Now().Before(value.expires) {
		delete(s.sessions, token)
		ok = false
	}
	s.mu.Unlock()
	if !ok || value.host != remoteHost(r) {
		fail(w, 401, "请重新进入当前设备的个人中心")
		return 0, false
	}
	return value.uid, true
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.identity(w, r); !ok {
		return
	}
	s.mu.Lock()
	delete(s.sessions, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	s.mu.Unlock()
	write(w, map[string]bool{"ok": true})
}

func (s *Server) profile(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.identity(w, r)
	if !ok {
		return
	}
	p, err := s.db.GetProfile(r.Context(), uid)
	if err != nil {
		fail(w, 500, "角色资料未能读取，请重试")
		return
	}
	m := store.MembershipAt(p.Membership, p.MembershipExp, time.Now().Unix())
	var until any
	if m.ExpiresAt > 0 {
		until = m.ExpiresAt * 1000
	}
	write(w, map[string]any{"userId": uid, "username": p.Name, "nickname": p.Name, "level": p.Level, "stars": p.Stars, "region": p.Region, "premium": m.Active, "premiumUntil": until, "goldMinor": p.Gold, "silverMinor": p.Silver})
}

func (s *Server) membership(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.identity(w, r)
	if !ok {
		return
	}
	state, err := s.db.GetMembershipStatus(r.Context(), uid, time.Now().Unix())
	if err != nil {
		fail(w, 500, "会员状态未能读取，请重试")
		return
	}
	write(w, state)
}

func (s *Server) purchase(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.identity(w, r)
	if !ok {
		return
	}
	var req struct {
		PlanID    string `json:"planID"`
		RequestID string `json:"requestID"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil {
		fail(w, 400, "购买参数无效")
		return
	}
	purchase, err := s.db.PurchaseMembership(r.Context(), uid, req.PlanID, req.RequestID, time.Now().Unix())
	if err != nil {
		var business *store.MembershipError
		if errors.As(err, &business) {
			fail(w, 400, business.Message)
		} else {
			fail(w, 500, "购买未能确认，请重试本次订单")
		}
		return
	}
	state, err := s.db.GetMembershipStatus(r.Context(), uid, time.Now().Unix())
	if err != nil {
		fail(w, 500, "购买已保存，请重试本次订单查询结果")
		return
	}
	write(w, map[string]any{"membership": state.Membership, "balance": state.Balance, "plans": state.Plans, "purchase": purchase})
}
