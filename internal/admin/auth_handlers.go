package admin

import (
	"encoding/json"
	"net/http"
	"time"

	"firecrawl-proxy/internal/auth"
	"firecrawl-proxy/internal/store"
)

// handleLogin 校验管理员密码并下发会话 cookie。
// 连续失败达阈值后引入递增延迟（防在线爆破，不做永久封禁）。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "请求体必须是 JSON")
		return
	}
	ip := auth.ClientIP(r)

	// 先按该 IP 的失败次数退避，再校验——让爆破者感知到代价。
	if delay := s.session.LoginBackoff(ip); delay > 0 {
		time.Sleep(delay)
	}

	token, err := s.session.Login(body.Password)
	if err != nil {
		s.session.RecordLoginFailure(ip)
		// 失败不下发 cookie。
		writeError(w, http.StatusUnauthorized, "invalid_password", "密码错误")
		return
	}
	s.session.RecordLoginSuccess(ip)
	http.SetCookie(w, auth.SessionCookie(token, s.clock.Now().Add(s.session.SessionTTL())))
	w.WriteHeader(http.StatusNoContent)
}

// handleLogout 删除当前会话并清除 cookie。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("session"); err == nil {
		_ = s.session.Logout(c.Value)
	}
	http.SetCookie(w, auth.ClearSessionCookie())
	w.WriteHeader(http.StatusNoContent)
}

// handleSessionStatus 供前端启动时判断登录态（无需登录即可访问）。
func (s *Server) handleSessionStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": s.session.Authenticated(r)})
}

// ---- 代理 Key 管理 ----

type proxyKeyDTO struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	KeyPrefix    string     `json:"key_prefix"`
	RequestCount int64      `json:"request_count"`
	LastUsedAt   *time.Time `json:"last_used_at"`
	CreatedAt    time.Time  `json:"created_at"`
	Revoked      bool       `json:"revoked"`
}

func toProxyKeyDTO(pk store.ProxyKey) proxyKeyDTO {
	return proxyKeyDTO{
		ID:           pk.ID,
		Name:         pk.Name,
		KeyPrefix:    pk.KeyPrefix,
		RequestCount: pk.RequestCount,
		LastUsedAt:   pk.LastUsedAt,
		CreatedAt:    pk.CreatedAt,
		Revoked:      pk.Revoked,
	}
}

// handleListProxyKeys 返回代理 Key 列表（无明文、无哈希）。
func (s *Server) handleListProxyKeys(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ProxyKeys.List()
	if err != nil {
		s.logger.Warn("查询代理 Key 列表失败", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal_error", "查询失败")
		return
	}
	dtos := make([]proxyKeyDTO, 0, len(list))
	for _, pk := range list {
		dtos = append(dtos, toProxyKeyDTO(pk))
	}
	writeJSON(w, http.StatusOK, dtos)
}

// handleCreateProxyKey 签发代理 Key。plaintext_key 只在本次响应中出现一次，
// 之后任何接口都不再返回（AC9）。
func (s *Server) handleCreateProxyKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "请求体必须是 JSON")
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid_input", "name 不能为空")
		return
	}
	plaintext, rec, err := s.proxyAuth.Issue(body.Name)
	if err != nil {
		s.logger.Warn("签发代理 Key 失败", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal_error", "签发失败")
		return
	}
	dto := toProxyKeyDTO(rec)
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":            dto.ID,
		"name":          dto.Name,
		"key_prefix":    dto.KeyPrefix,
		"request_count": dto.RequestCount,
		"created_at":    dto.CreatedAt,
		"revoked":       dto.Revoked,
		"plaintext_key": plaintext, // 唯一一次明文出现
	})
}

// handleDeleteProxyKey 吊销代理 Key（置 revoked=1，立即失效）。
func (s *Server) handleDeleteProxyKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.proxyAuth.Revoke(id); err != nil {
		s.logger.Warn("吊销代理 Key 失败", "key_id", id, "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal_error", "吊销失败")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
