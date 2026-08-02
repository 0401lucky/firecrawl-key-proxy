# Logging Guidelines — Backend

## 基础设施

- 标准库 `log/slog`，JSON handler，输出到 stdout。级别由 `LOG_LEVEL` 控制。
- `internal/logging.New(level) (*slog.Logger, error)`：非法级别返回错误，
  由调用方决定是否终止，不静默降级。
- `main()` 里 `slog.SetDefault(logger)`，全项目用包级 `slog` 函数即可。

## 上游 Key 脱敏（AC11 的守门人）

全项目只有 `internal/logging.MaskKey` 一份实现，任何日志/API 输出都不得
直接打印上游 Key 明文：

```go
func MaskKey(key string) string {
    if len(key) <= 4 {
        return "fc-****"
    }
    return "fc-****" + key[len(key)-4:]
}
// MaskKey("fc-1234567890abcd") -> "fc-****abcd"
```

- 代理 Key 记**名称**而非值。
- 领域结构体上的敏感字段带 `json:"-"`，防止误序列化泄漏（上线前全局 grep）。

## 代理请求日志（每请求一条）

```go
slog.Info("proxy request",
    "method", r.Method, "path", r.URL.Path,
    "proxy_key", proxyKeyName,
    "upstream_key", logging.MaskKey(uk.APIKey),
    "upstream_status", upstreamStatus,
    "failover_count", failoverCount,
    "duration_ms", durationMs,
)
```

能回答审计问题：「这次请求用了哪个账号、是否发生过转移」。
