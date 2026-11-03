package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"rf4server/internal/store"
)

// 账号管理：游戏登录用户名/密码绑定 player id。
// 账号只在管理后台创建（客户端登录仅上传 SHA-1，复杂度策略只能在这里强制）。

type accountReq struct {
	Username string `json:"username"`
	Password string `json:"password"` // 明文；服务端校验复杂度后哈希存储
	Userid   int64  `json:"userid"`   // 0 = 自动分配下一个
}

func (s *Server) handleAccountList(w http.ResponseWriter, r *http.Request) {
	list, err := s.db.AccountList(r.Context())
	if err != nil {
		httpError(w, http.StatusInternalServerError, "读取账号失败: "+err.Error())
		return
	}
	now := time.Now().Unix()
	for i := range list {
		list[i].PassSHA1 = ""
		if list[i].CreatedAt <= 0 {
			list[i].CreatedAt = now
		}
	}
	// 附加已有存档但未绑定账号的玩家（如历史 1000001），灰显展示
	unbound, err := s.db.UnboundProfiles(r.Context())
	if err == nil {
		for _, u := range unbound {
			list = append(list, store.Account{
				Username:  u.Name,
				Userid:    u.Userid,
				CreatedAt: 0,
			})
		}
	}
	writeJSON(w, list)
}

func (s *Server) handleAccountCreate(w http.ResponseWriter, r *http.Request) {
	var req accountReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	username := store.NormalizeUsername(req.Username)
	if username == "" {
		httpError(w, http.StatusBadRequest, "用户名不能为空")
		return
	}
	if err := store.ValidatePassword(req.Password); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	userid := req.Userid
	if userid <= 0 {
		uid, err := s.db.NextUseridSeq(r.Context())
		if err != nil {
			httpError(w, http.StatusInternalServerError, "分配 userid 失败: "+err.Error())
			return
		}
		userid = uid
	}
	if err := s.db.AccountCreate(r.Context(), username, store.PasswordSHA1(req.Password), userid); err != nil {
		switch {
		case errors.Is(err, store.ErrAccountExists):
			httpError(w, http.StatusConflict, "用户名已存在")
		case errors.Is(err, store.ErrUseridTaken):
			httpError(w, http.StatusConflict, "该 userid 已绑定其他账号")
		default:
			httpError(w, http.StatusInternalServerError, "建号失败: "+err.Error())
		}
		return
	}
	// 注册即写入游戏内显示名：profile.name = 用户名（游戏自动显示）
	if err := s.db.SyncProfileName(r.Context(), userid, username); err != nil {
		httpError(w, http.StatusInternalServerError, "写入游戏内显示名失败: "+err.Error())
		return
	}
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "account_create", username, strconv.FormatInt(userid, 10))
	writeJSON(w, map[string]any{"ok": true, "username": username, "userid": userid})
}

func (s *Server) handleAccountPassword(w http.ResponseWriter, r *http.Request) {
	var req accountReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if strings.TrimSpace(req.Username) == "" {
		httpError(w, http.StatusBadRequest, "用户名不能为空")
		return
	}
	if err := store.ValidatePassword(req.Password); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.db.AccountSetPassword(r.Context(), req.Username, store.PasswordSHA1(req.Password)); err != nil {
		httpError(w, http.StatusInternalServerError, "改密失败: "+err.Error())
		return
	}
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "account_password", store.NormalizeUsername(req.Username), "")
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleAccountDelete(w http.ResponseWriter, r *http.Request) {
	var req accountReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if strings.TrimSpace(req.Username) == "" {
		httpError(w, http.StatusBadRequest, "用户名不能为空")
		return
	}
	if err := s.db.AccountDelete(r.Context(), req.Username); err != nil {
		httpError(w, http.StatusInternalServerError, "删除失败: "+err.Error())
		return
	}
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "account_delete", store.NormalizeUsername(req.Username), "")
	writeJSON(w, map[string]any{"ok": true})
}
