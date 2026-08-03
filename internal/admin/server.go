// Package admin 实现面板 JSON API（父任务 design §7 契约）。
//
// 认证隔离：/api/admin/* 走 HttpOnly session cookie（C5 的 SessionAuth），
// 代理路径走 Authorization（C4 的 ProxyKeyAuth），两套互不通用（AC13）。
package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"firecrawl-proxy/internal/auth"
	"firecrawl-proxy/internal/firecrawl"
	"firecrawl-proxy/internal/keypool"
	"firecrawl-proxy/internal/logging"
	"firecrawl-proxy/internal/store"
)

// Server 聚合面板 API 的全部依赖。所有字段只读，并发安全。
type Server struct {
	pool          *keypool.Pool
	st            *store.Store
	proxyAuth     *auth.ProxyKeyAuth
	session       *auth.SessionAuth
	client        *firecrawl.Client
	logger        *slog.Logger
	clock         keypool.Clock
	registerToken string // 自动注册器上传 Key 的共享 token，空表示未启用
}

// NewServer 构造面板 API 服务。
// registerToken 为空时 /api/register/keys 返回 503（注册接入未启用）。
func NewServer(
	pool *keypool.Pool,
	st *store.Store,
	proxyAuth *auth.ProxyKeyAuth,
	session *auth.SessionAuth,
	client *firecrawl.Client,
	logger *slog.Logger,
	clock keypool.Clock,
	registerToken string,
) *Server {
	return &Server{
		pool: pool, st: st, proxyAuth: proxyAuth, session: session,
		client: client, logger: logger, clock: clock,
		registerToken: registerToken,
	}
}

// Router 装配面板路由。login 与 session 状态检查不需要会话，
// 其余全部经过 SessionAuth.Middleware。
func (s *Server) Router() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/admin/login", s.handleLogin)
	mux.HandleFunc("GET /api/admin/session", s.handleSessionStatus)
	// 注册器接入接口：独立 token 认证（register.go），不经过 SessionAuth。
	mux.HandleFunc("POST /api/register/keys", s.handleRegisterCreateKey)

	protected := http.NewServeMux()
	protected.HandleFunc("POST /api/admin/logout", s.handleLogout)
	protected.HandleFunc("GET /api/admin/overview", s.handleOverview)
	protected.HandleFunc("GET /api/admin/stats", s.handleStats)
	protected.HandleFunc("GET /api/admin/upstream-keys", s.handleListUpstreamKeys)
	protected.HandleFunc("POST /api/admin/upstream-keys", s.handleCreateUpstreamKey)
	protected.HandleFunc("PATCH /api/admin/upstream-keys/{id}", s.handlePatchUpstreamKey)
	protected.HandleFunc("DELETE /api/admin/upstream-keys/{id}", s.handleDeleteUpstreamKey)
	protected.HandleFunc("POST /api/admin/upstream-keys/{id}/refresh-credits", s.handleRefreshCredits)
	protected.HandleFunc("GET /api/admin/proxy-keys", s.handleListProxyKeys)
	protected.HandleFunc("POST /api/admin/proxy-keys", s.handleCreateProxyKey)
	protected.HandleFunc("DELETE /api/admin/proxy-keys/{id}", s.handleDeleteProxyKey)

	mux.Handle("/", s.session.Middleware(protected))
	return mux
}

// StartCreditRefresh 按间隔刷新可用 Key 的额度（后台展示数据）。
// 间隔 <= 0 时关闭。独立 goroutine + 每轮 recover：刷新任务出错或被关闭
// 绝不影响代理转发与故障转移（额度数据不参与调度，见 keypool.SetCredits）。
func (s *Server) StartCreditRefresh(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.refreshCreditsOnce()
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) refreshCreditsOnce() {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("额度刷新 panic，已恢复", "panic", r)
		}
	}()
	keys, _ := s.pool.Snapshot()
	for _, ks := range keys {
		k := ks.Key
		// 只刷新可用且启用的 Key：对已失效的 Key 反复发请求没有意义。
		if k.State != store.StateAvailable || !k.Enabled {
			continue
		}
		usage, err := s.client.GetCreditUsage(context.Background(), k.APIKey)
		if err != nil {
			// 拉取失败只记 warning，保留上次的值，不改变 Key 状态。
			s.logger.Warn("额度拉取失败",
				"key", logging.MaskKey(k.APIKey), "error", err.Error())
			continue
		}
		s.pool.SetCredits(k.ID, usage.Total, usage.Remaining)
	}
}

// ---- 响应辅助 ----

// writeJSON 输出 2xx JSON 响应。
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeError 输出统一的错误响应（父任务 design §9 契约风格）。
func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": code, "message": msg})
}
