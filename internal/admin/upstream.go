package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"firecrawl-proxy/internal/firecrawl"
	"firecrawl-proxy/internal/logging"
	"firecrawl-proxy/internal/store"
)

// upstreamKeyDTO 是上游 Key 的对外表示。任何接口都不返回 api_key 明文，
// 只有 Masked（fc-**** + 末 4 位）——AC11 的守门人就是本 DTO。
type upstreamKeyDTO struct {
	ID               int64        `json:"id"`
	Name             string       `json:"name"`
	Masked           string       `json:"masked"`
	State            store.KeyState `json:"state"`
	CooldownRemaining int64       `json:"cooldown_remaining"`
	CreditsTotal     *int64       `json:"credits_total"`
	CreditsRemaining *int64       `json:"credits_remaining"`
	CreditsSyncedAt  *time.Time   `json:"credits_synced_at"`
	RequestCount     int64        `json:"request_count"`
	LastError        *string      `json:"last_error"`
	Enabled          bool         `json:"enabled"`
	CreatedAt        time.Time    `json:"created_at"`
}

func toUpstreamDTO(uk *store.UpstreamKey, cooldownRemaining int64) upstreamKeyDTO {
	return upstreamKeyDTO{
		ID:                uk.ID,
		Name:              uk.Name,
		Masked:            logging.MaskKey(uk.APIKey),
		State:             uk.State,
		CooldownRemaining: cooldownRemaining,
		CreditsTotal:      uk.CreditsTotal,
		CreditsRemaining:  uk.CreditsRemaining,
		CreditsSyncedAt:   uk.CreditsSyncedAt,
		RequestCount:      uk.RequestCount,
		LastError:         uk.LastError,
		Enabled:           uk.Enabled,
		CreatedAt:         uk.CreatedAt,
	}
}

// handleListUpstreamKeys 返回全部上游 Key 的当前状态（数据源为 keypool 内存权威）。
func (s *Server) handleListUpstreamKeys(w http.ResponseWriter, r *http.Request) {
	keys, _ := s.pool.Snapshot()
	dtos := make([]upstreamKeyDTO, 0, len(keys))
	for _, ks := range keys {
		dtos = append(dtos, toUpstreamDTO(&ks.Key, ks.CooldownRemaining))
	}
	writeJSON(w, http.StatusOK, dtos)
}

// handleCreateUpstreamKey 录入新 Key，写库后 Reload 使其立即参与调度（AC8）。
func (s *Server) handleCreateUpstreamKey(w http.ResponseWriter, r *http.Request) {
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
		s.logger.Warn("创建上游 Key 失败", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal_error", "创建失败")
		return
	}
	writeJSON(w, http.StatusCreated, toUpstreamDTO(uk, 0))
}

// handlePatchUpstreamKey 支持改名、启停、reset（手动拉回 available）。
func (s *Server) handlePatchUpstreamKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body struct {
		Name    *string `json:"name"`
		Enabled *bool   `json:"enabled"`
		Reset   *bool   `json:"reset"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "请求体必须是 JSON")
		return
	}
	uk, err := s.pool.GetByID(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "上游 Key 不存在")
		return
	}
	if body.Name != nil {
		uk.Name = *body.Name
	}
	if body.Enabled != nil {
		uk.Enabled = *body.Enabled
	}
	if body.Reset != nil && *body.Reset {
		// 手动把 exhausted/invalid 的 Key 拉回可用（账号充值/换绑后），
		// 并清空 last_error 与冷却。
		uk.State = store.StateAvailable
		uk.LastError = nil
		uk.CooldownUntil = nil
	}
	if err := s.st.UpstreamKeys.Update(uk); err != nil {
		s.logger.Warn("更新上游 Key 失败", "key_id", id, "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal_error", "更新失败")
		return
	}
	if err := s.pool.Reload(); err != nil {
		s.logger.Warn("更新后 Reload 失败", "error", err.Error())
	}
	writeJSON(w, http.StatusOK, toUpstreamDTO(uk, 0))
}

// handleDeleteUpstreamKey 删除 Key。job 映射经外键级联删除。
func (s *Server) handleDeleteUpstreamKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.st.UpstreamKeys.Delete(id); err != nil {
		s.logger.Warn("删除上游 Key 失败", "key_id", id, "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal_error", "删除失败")
		return
	}
	if err := s.pool.Reload(); err != nil {
		s.logger.Warn("删除后 Reload 失败", "error", err.Error())
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRefreshCredits 拉取该 Key 的额度并写回，同时充当「这个 Key 还能不能用」的探测。
//
// 用 credit-usage 而非发一次真实 scrape 来探测：前者不消耗 credits，
// 对免费账号更友好，而且直接给出余额。
//
// 失败时按上游状态码给出可操作的提示，但**不改变 Key 状态**——额度展示与调度
// 保持解耦（见父任务 design §7），Key 状态只由真实转发结果驱动。
func (s *Server) handleRefreshCredits(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	uk, err := s.pool.GetByID(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "上游 Key 不存在")
		return
	}
	usage, err := s.client.GetCreditUsage(r.Context(), uk.APIKey)
	if err != nil {
		s.logger.Warn("额度拉取失败",
			"key", logging.MaskKey(uk.APIKey), "key_id", id, "error", err.Error())
		code, msg := describeUpstreamFailure(err)
		writeError(w, http.StatusBadGateway, code, msg)
		return
	}
	s.pool.SetCredits(id, usage.Total, usage.Remaining)
	writeJSON(w, http.StatusOK, map[string]any{
		"credits_total":     usage.Total,
		"credits_remaining": usage.Remaining,
		"credits_synced_at": s.clock.Now(),
	})
}

// describeUpstreamFailure 把上游错误翻译成调用方能据此行动的提示。
// 401/402/429 的语义完全不同，混成一句「拉取失败」等于没说。
func describeUpstreamFailure(err error) (code, msg string) {
	var apiErr *firecrawl.APIError
	if !errors.As(err, &apiErr) {
		return "upstream_unreachable", "连不上 Firecrawl，请检查服务器网络：" + err.Error()
	}
	switch apiErr.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "key_invalid", "这个 Key 无效或已被吊销，请核对后重新录入"
	case http.StatusPaymentRequired:
		return "key_exhausted", "这个 Key 的额度已耗尽"
	case http.StatusTooManyRequests:
		return "key_rate_limited", "触发上游限流，稍后再试（Key 本身可能仍然可用）"
	default:
		return "credit_refresh_failed", apiErr.Error()
	}
}

// ---- 工具 ----

// pathID 解析路径参数 {id}，解析失败时已写出 400 并返回 false。
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_id", "路径中的 id 必须是正整数")
		return 0, false
	}
	return id, true
}

// isUniqueViolation 判断错误是否为 SQLite 的 UNIQUE 约束冲突。
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
