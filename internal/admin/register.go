// register.go — 自动注册器上传 Key 的接入接口。
//
// 认证隔离设计：面板路径走 SessionAuth（session cookie），注册器是无人值守
// 脚本，不适合维护 session，因此本接口走独立共享 token（X-Register-Token）。
// token 未配置（REGISTER_TOKEN 为空）时接口返回 503，即功能未启用，
// 保证默认部署行为与未加此接口时完全一致。
package admin

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"

	"firecrawl-proxy/internal/store"
)

// createUpstreamKey 是「录入上游 Key」的公共逻辑：写库 + Reload 使其立即
// 参与调度（AC8）。面板 handler 与注册器 handler 共用，错误由调用方映射。
func (s *Server) createUpstreamKey(name, apiKey string) (*store.UpstreamKey, error) {
	uk := &store.UpstreamKey{Name: name, APIKey: apiKey}
	if _, err := s.st.UpstreamKeys.Create(uk); err != nil {
		return nil, err
	}
	if err := s.pool.Reload(); err != nil {
		s.logger.Warn("创建后 Reload 失败", "error", err.Error())
	}
	return uk, nil
}

// handleRegisterCreateKey 供自动注册器（registerer/）上传注册成功的上游 Key。
//
//	POST /api/register/keys
//	X-Register-Token: <REGISTER_TOKEN>
//	{"name": "...", "api_key": "fc-..."}
//
// 响应与面板的创建接口一致：201 + masked DTO；401 token 无效；409 重复。
func (s *Server) handleRegisterCreateKey(w http.ResponseWriter, r *http.Request) {
	if s.registerToken == "" {
		writeError(w, http.StatusServiceUnavailable, "register_not_enabled",
			"未配置 REGISTER_TOKEN，注册接入未启用")
		return
	}
	// 常量时间比较，避免时序侧信道；空 token 一律视为不匹配。
	provided := r.Header.Get("X-Register-Token")
	if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(s.registerToken)) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid_register_token", "注册 token 无效")
		return
	}

	var body struct {
		Name   string `json:"name"`
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "请求体必须是 JSON")
		return
	}
	if body.Name == "" || body.APIKey == "" {
		writeError(w, http.StatusBadRequest, "invalid_input", "name 与 api_key 都不能为空")
		return
	}

	uk, err := s.createUpstreamKey(body.Name, body.APIKey)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "duplicate_api_key", "该上游 Key 已存在")
			return
		}
		s.logger.Warn("注册器创建上游 Key 失败", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal_error", "创建失败")
		return
	}
	writeJSON(w, http.StatusCreated, toUpstreamDTO(uk, 0))
}
