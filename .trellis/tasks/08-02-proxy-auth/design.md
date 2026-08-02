# C4 设计 — 下游认证与用量统计

## 位置

`internal/auth/proxykey.go`。对外暴露：

```go
type ProxyKeyAuth struct { /* repo, usage buffer, mu */ }

func (a *ProxyKeyAuth) Issue(name string) (plaintext string, rec store.ProxyKey, err error)
func (a *ProxyKeyAuth) Revoke(id int64) error
func (a *ProxyKeyAuth) Middleware(next http.Handler) http.Handler
func (a *ProxyKeyAuth) Flush() error
```

`Middleware` 把通过校验的 Key 身份写入 `context`，C3 的日志从 context 取 `name`。

## 是否要缓存

不缓存。每请求一次 `SELECT ... WHERE key_hash = ?`，在 `key_hash` 上有唯一索引，SQLite 走 WAL 读，这个开销远低于一次上游 HTTP 调用（数百毫秒起）。加缓存会引入吊销延迟——AC 明确要求吊销立即生效，缓存就得额外做失效通知，复杂度换来的性能收益在这个场景下不成比例。

若日后确实成为瓶颈，正确的做法是加带失效广播的缓存，而不是加 TTL 缓存。

## 明文格式

```go
buf := make([]byte, 32)
rand.Read(buf)                                  // crypto/rand
plaintext := "fcp_" + base64.RawURLEncoding.EncodeToString(buf)
prefix := plaintext[:12]                        // 展示用，如 "fcp_9k3Xa"
```

`RawURLEncoding` 避免 `+` `/` `=` 出现在 token 里——它会被放进 HTTP 头，也会被用户复制粘贴进各种配置文件。

## 计数缓冲

与 `keypool` 的用量刷盘同构：内存 `map[int64]int64` 累加，ticker 驱动批量提交，退出前 flush。两处可以各自实现，不必强行抽公共组件——总共十几行，抽象的成本高于重复的成本。

## 与 C5 的边界

本任务提供 `Issue` / `Revoke` 函数，C5 负责把它们包成 HTTP 接口并处理「明文只返回一次」的响应构造。本任务不感知 HTTP 层的面板逻辑。
