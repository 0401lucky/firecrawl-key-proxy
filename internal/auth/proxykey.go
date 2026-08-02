// Package auth 实现两套互不通用的认证：
//
//   - proxykey：代理对外签发的 API Key（Authorization: Bearer），挂在代理路径上；
//   - session：面板管理员会话（HttpOnly cookie），挂在面板路径上（C5 实现）。
//
// 两套认证互不接受对方的凭据（父任务 R6.6 / AC13）。
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"firecrawl-proxy/internal/store"
)

// 用 SHA-256 而非 bcrypt/argon2 是有意的：
// token 是 32 字节 crypto/rand 生成的高熵随机串，不存在字典攻击面，
// 而每个代理请求都要校验一次，慢哈希会直接变成吞吐瓶颈。
// 不要被「安全最佳实践」误改成慢哈希。
//
// 校验比较发生在 SQLite 的 key_hash 唯一索引等值查询里（命中即相等）。
// 不在 Go 里另做常量时间比较：那需要把行取回再比一次，纯属多余的一次
// 往返；而高熵 token 面前，纳秒级比较差异相对网络抖动不可观测。

// ProxyKeyAuth 是代理 API Key 的签发、校验与计数。并发安全。
type ProxyKeyAuth struct {
	repo  *store.ProxyKeyRepo
	mu    sync.Mutex
	usage map[int64]int64 // 待刷盘的调用增量（keyID → 次数）
}

// NewProxyKeyAuth 构造代理 Key 认证器。
func NewProxyKeyAuth(repo *store.ProxyKeyRepo) *ProxyKeyAuth {
	return &ProxyKeyAuth{repo: repo, usage: make(map[int64]int64)}
}

// Issue 签发一个新的代理 Key。
//
// 明文只在返回值中出现一次：不写日志、不入库。库里只存 sha256(明文) 的
// 十六进制与前 12 位前缀（展示用）。
func (a *ProxyKeyAuth) Issue(name string) (plaintext string, rec store.ProxyKey, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", store.ProxyKey{}, err
	}
	// RawURLEncoding 避免 + / = 出现在 token 里（会进 HTTP 头、被复制进配置文件）。
	plaintext = "fcp_" + base64.RawURLEncoding.EncodeToString(buf)

	rec = store.ProxyKey{
		Name:      name,
		KeyHash:   sha256Hex(plaintext),
		KeyPrefix: plaintext[:12],
	}
	if _, err := a.repo.Create(&rec); err != nil {
		return "", store.ProxyKey{}, err
	}
	return plaintext, rec, nil
}

// Revoke 吊销一个代理 Key。吊销后下一个请求立即 401，不依赖缓存过期。
func (a *ProxyKeyAuth) Revoke(id int64) error {
	return a.repo.Revoke(id)
}

// Middleware 校验 Authorization: Bearer <proxy-key>。
//
// 四种失败情形（缺头/格式错/token 不存在/token 已吊销）返回完全一致的 401
// 响应体，不泄露失败原因的差异。只读 Authorization 头，不读 cookie——
// 面板 session cookie 天然无法通过本校验（AC13 的一半）。
func (a *ProxyKeyAuth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			writeUnauthorized(w)
			return
		}
		pk, err := a.repo.FindByHash(sha256Hex(token))
		if err != nil || pk.Revoked {
			writeUnauthorized(w)
			return
		}
		a.recordUsage(pk.ID)
		ctx := WithProxyKey(r.Context(), pk.ID, pk.Name)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Run 启动用量刷盘循环，直到 ctx 取消。
func (a *ProxyKeyAuth) Run(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := a.Flush(); err != nil {
				slog.Error("代理 Key 用量刷盘失败", "error", err.Error())
			}
		case <-ctx.Done():
			return
		}
	}
}

// Flush 把内存中累计的调用增量批量写入 DB 后清空。
func (a *ProxyKeyAuth) Flush() error {
	a.mu.Lock()
	if len(a.usage) == 0 {
		a.mu.Unlock()
		return nil
	}
	usage := a.usage
	a.usage = make(map[int64]int64)
	a.mu.Unlock()
	return a.repo.IncrementUsage(usage)
}

func (a *ProxyKeyAuth) recordUsage(keyID int64) {
	a.mu.Lock()
	a.usage[keyID]++
	a.mu.Unlock()
}

// ---- 请求上下文身份 ----

type ctxKey int

const (
	ctxProxyKeyID ctxKey = iota
	ctxProxyKeyName
)

// WithProxyKey 把通过校验的代理 Key 身份写入请求上下文。
func WithProxyKey(ctx context.Context, id int64, name string) context.Context {
	ctx = context.WithValue(ctx, ctxProxyKeyID, id)
	ctx = context.WithValue(ctx, ctxProxyKeyName, name)
	return ctx
}

// ProxyKeyNameFrom 从请求上下文取代理 Key 名称（供日志使用，记名称不记值）。
func ProxyKeyNameFrom(ctx context.Context) string {
	if v, ok := ctx.Value(ctxProxyKeyName).(string); ok {
		return v
	}
	return ""
}

// ---- 工具函数 ----

// bearerToken 从 Authorization 头解析 Bearer token。
// scheme 大小写不敏感（RFC 7235）；token 为空视为格式错误。
func bearerToken(header string) (string, bool) {
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", false
	}
	return token, true
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// writeUnauthorized 输出 401 响应。四个失败分支共用同一个响应体。
func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   "invalid_proxy_key",
		"message": "无效的代理 API Key",
	})
}
