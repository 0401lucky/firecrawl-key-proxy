# C5 设计 — 面板后端 API

契约见父任务 `design.md` §7。本文记录实现结构。

## 路由装配

Go 1.22 的 `http.ServeMux` 已支持方法与路径参数（`"PATCH /api/admin/upstream-keys/{id}"`），足够本项目使用，不引入路由库。

`main.go` 中的装配顺序：

```go
mux := http.NewServeMux()
mux.Handle("GET /healthz", healthHandler)
mux.Handle("/api/admin/", sessionMW(adminRouter))
for _, p := range cfg.ProxyPathPrefixes {
    mux.Handle(p, proxyKeyMW(proxyHandler))
}
mux.Handle("/", spaHandler)          // 最低优先级，兜底
```

`ServeMux` 按最长前缀匹配，`"/"` 自然成为兜底。SPA handler 内部再判断：若路径以 `/v1/`、`/v2/`、`/api/` 开头（说明前面的 handler 没匹配上具体路由），返回 404 而非 `index.html`——否则客户端会拿到一个 200 的 HTML 页面而误以为调用成功。

## Session

存 DB 而非签名 cookie。理由：需要支持登出即失效，签名 cookie 做不到无状态吊销。Session 数量极小（单管理员），DB 查询开销可忽略。

过期清理与 job 映射共用同一个后台清理 ticker。

## 登录暴力破解防护

按来源 IP 记录连续失败次数（内存 map，成功即清零）。失败次数 n 达到阈值后，在返回 401 前 `time.Sleep(min(2^(n-5) 秒, 30 秒))`。

不做永久封禁——单管理员场景下，把自己锁在门外的风险高于被爆破的风险。递增延迟已足以让在线爆破不可行。

## 额度拉取客户端

```go
package firecrawl
type Client struct{ baseURL string; http *http.Client }
func (c *Client) GetCreditUsage(ctx context.Context, apiKey string) (CreditUsage, error)
```

超时 10 秒。这个 client 独立于 C3 的转发逻辑——它是代理**作为客户端**主动发起的调用，与转发用户请求是两件事，共用代码只会把两边的超时、重试、错误处理纠缠在一起。

后台刷新 goroutine：

```go
for range ticker.C {
    for _, k := range pool.Snapshot() {
        if k.State != available || !k.Enabled { continue }
        usage, err := client.GetCreditUsage(ctx, k.APIKey)
        if err != nil { log.Warn(...); continue }   // 不改状态
        pool.SetCredits(k.ID, usage.Total, usage.Remaining)
    }
}
```

逐个串行拉取，不并发。Key 数量小，且并发拉取可能自己触发上游限流——那会让展示逻辑反过来污染调度状态，是本设计要极力避免的耦合。

## 响应约定

- 成功：2xx + JSON 主体（204 无主体）。
- 失败：对应状态码 + `{"error": "<code>", "message": "<中文说明>"}`。
- 所有 handler 统一经过一个把 `error` 转成该结构的辅助函数，避免各处手写 JSON 拼装出格式不一致的响应。

## 与 C6 的契约冻结

本任务完成后，`design.md` §7 的接口形状即为前端可依赖的契约。C6 若需要新字段，应回到本任务追加，而不是在前端做推导或拼接。
