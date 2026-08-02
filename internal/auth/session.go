package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"firecrawl-proxy/internal/keypool"
	"firecrawl-proxy/internal/store"
)

// 面板管理员会话（HttpOnly cookie）。session token 存 DB 而非签名 cookie：
// 需要支持登出即失效，签名 cookie 做不到无状态吊销。单管理员，数量极小。
//
// 与代理 Key 认证（Authorization）彻底隔离：本中间件只读 cookie，代理中间件
// 只读 Authorization（AC13 的另一半）。

const sessionCookieName = "session"

// SessionAuth 管理面板管理员登录与会话。并发安全。
type SessionAuth struct {
	repo     *store.SessionRepo
	password string
	ttl      time.Duration
	clock    keypool.Clock
	logger   *slog.Logger

	mu       sync.Mutex
	failures map[string]int // 来源 IP → 连续登录失败次数（成功即清零）
}

// NewSessionAuth 构造会话管理器。
func NewSessionAuth(repo *store.SessionRepo, password string, ttl time.Duration,
	clock keypool.Clock, logger *slog.Logger) *SessionAuth {
	return &SessionAuth{
		repo:     repo,
		password: password,
		ttl:      ttl,
		clock:    clock,
		logger:   logger,
		failures: make(map[string]int),
	}
}

// Login 校验密码并签发会话，返回明文 session token（只此一次，交给调用方下 cookie）。
// 密码用常量时间比较：管理员密码可能熵不高，时序侧信道比代理 Key 场景更现实。
func (s *SessionAuth) Login(password string) (string, error) {
	if subtle.ConstantTimeCompare([]byte(s.password), []byte(password)) != 1 {
		return "", ErrInvalidPassword
	}
	token, err := s.newSession()
	if err != nil {
		return "", err
	}
	return token, nil
}

// NewSessionToken 供登录 handler 在密码校验后调用：生成 token、入库、返回明文。
func (s *SessionAuth) newSession() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	now := s.clock.Now()
	sess := &store.Session{
		TokenHash: sha256Hex(token),
		CreatedAt: now,
		ExpiresAt: now.Add(s.ttl),
	}
	if err := s.repo.Create(sess); err != nil {
		return "", err
	}
	return token, nil
}

// ErrInvalidPassword 表示密码错误。
var ErrInvalidPassword = &LoginError{}

// LoginError 用于标识登录失败（供上层区分「密码错」与「系统错」）。
type LoginError struct{}

func (*LoginError) Error() string { return "密码错误" }

// SessionTTL 返回会话有效期（cookie 过期时间与 DB 会话一致）。
func (s *SessionAuth) SessionTTL() time.Duration {
	return s.ttl
}

// Logout 删除会话并返回待清除的 token（由 handler 清 cookie）。
func (s *SessionAuth) Logout(token string) error {
	if token == "" {
		return nil
	}
	return s.repo.Delete(sha256Hex(token))
}

// Authenticated 判断请求 cookie 是否对应一个未过期会话。
func (s *SessionAuth) Authenticated(r *http.Request) bool {
	token := sessionToken(r)
	if token == "" {
		return false
	}
	sess, err := s.repo.Get(sha256Hex(token))
	if err != nil || sess.ExpiresAt.Before(s.clock.Now()) {
		return false
	}
	return true
}

// Middleware 保护面板 API：无有效会话一律 401，不泄露「会话不存在/已过期」的差异。
func (s *SessionAuth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.Authenticated(r) {
			writeSessionUnauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RecordLoginFailure 记录一次登录失败；返回当前连续失败次数。
// 成功登录时调用 RecordLoginSuccess 清零。
func (s *SessionAuth) RecordLoginFailure(ip string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[ip]++
	return s.failures[ip]
}

// RecordLoginSuccess 清零该 IP 的失败计数。
func (s *SessionAuth) RecordLoginSuccess(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.failures, ip)
}

// LoginBackoff 返回该 IP 当前应等待的延迟（连续失败 n>=5 后递增，上限 30 秒）。
// 不做永久封禁：单管理员场景把自己锁在门外的风险高于被爆破的风险。
func (s *SessionAuth) LoginBackoff(ip string) time.Duration {
	s.mu.Lock()
	n := s.failures[ip]
	s.mu.Unlock()
	if n < 5 {
		return 0
	}
	delay := time.Duration(1<<uint(n-5)) * time.Second
	if delay > 30*time.Second {
		return 30 * time.Second
	}
	return delay
}

// ClientIP 取来源 IP（RemoteAddr 的 host 部分）。
// 注意：前置网关场景下这是网关地址，X-Forwarded-For 可伪造故不采用——
// 面板仅对公网地址暴露时风险可接受，README 中说明。
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func sessionToken(r *http.Request) string {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return ""
	}
	return c.Value
}

// SessionCookie 构造会话 cookie（HttpOnly + SameSite=Lax + Path=/）。
// 不设 Secure：代理自身只监听 HTTP，TLS 由前置网关终止，Secure 会直接
// 让 cookie 永远无法下发。
func SessionCookie(token string, expiresAt time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt,
	}
}

// ClearSessionCookie 返回一个立即过期的同名 cookie（登出时清除）。
func ClearSessionCookie() *http.Cookie {
	return SessionCookie("", time.Unix(0, 0))
}

func writeSessionUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   "unauthorized",
		"message": "未登录或会话已过期",
	})
}

// StartSessionCleanup 定期清理过期会话，直到 ctx 取消。
func (s *SessionAuth) StartSessionCleanup(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			n, err := s.repo.DeleteExpired(s.clock.Now())
			if err != nil {
				s.logger.Warn("清理过期会话失败", "error", err.Error())
				continue
			}
			if n > 0 {
				s.logger.Info("清理过期会话", "count", n)
			}
		case <-ctx.Done():
			return
		}
	}
}
