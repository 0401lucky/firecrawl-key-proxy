# C3 设计 — 代理转发与故障转移

流程图见父任务 `design.md` §6。本文记录实现结构与易错点。

## 为什么不用 `httputil.ReverseProxy`

`ReverseProxy` 假定「一个请求对应一次上游调用」。故障转移需要在拿到上游响应后决定是否换个目标重发，这在 `ReverseProxy` 的 `Director` / `ModifyResponse` 钩子里表达不出来——`ModifyResponse` 返回错误只能让整个请求失败，不能重来一次。

因此直接用 `http.Client` 手写转发循环。代价是要自己处理 hop-by-hop 头，收益是控制流清晰。

需要剔除的 hop-by-hop 请求头：`Connection`、`Proxy-Connection`、`Keep-Alive`、`Transfer-Encoding`、`TE`、`Trailer`、`Upgrade`、`Proxy-Authorization`、`Proxy-Authenticate`。响应侧同理。

## 主循环骨架

```go
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // 1. 路径前缀检查
    // 2. job 粘连查表 → 若命中，走 stickyPath 并 return
    // 3. 缓冲请求体（或标记 unbuffered）
    // 4. failover 循环
    for attempt := 0; attempt < maxAttempts; attempt++ {
        key, err := pool.NextExcluding(tried)
        if err != nil { return respond503(w, pool.Snapshot()) }
        tried = append(tried, key.ID)

        resp, netErr := h.forward(r, body, key)
        outcome := keypool.Outcome{StatusCode: statusOf(resp), Err: netErr,
                                   RetryAfter: parseRetryAfter(resp)}
        pool.Report(key.ID, outcome)          // 惩罚与否由 keypool 决定

        if !shouldRetry(outcome) || !buffered {
            return h.deliver(w, r, resp, key) // 含 job 记录与 URL 重写
        }
        drainAndClose(resp)                    // 复用连接
    }
    respond502(w)
}
```

`shouldRetry` 与 `keypool.classify` 是**两个独立判断**：前者决定「要不要换个 Key 再试」，后者决定「这个 Key 该不该被惩罚」。5xx 的答案是「要重试，不惩罚」。把它们写成同一个函数会必然导致 AC3 失败。

## 请求体处理

```go
const unbuffered = -1
```

读请求体用 `io.LimitReader(r.Body, max+1)`，读到 `max+1` 字节说明超限：此时把已读部分与剩余流用 `io.MultiReader` 拼回去，走单次转发路径，`buffered = false`。这样既不用预先知道 `Content-Length`（可能缺失或撒谎），也不会把大请求整个读进内存。

## job 粘连

路径匹配用预编译正则，而非字符串切分——`/v2/crawl/{id}/errors` 与 `/v2/crawl/{id}` 需要区分，且 `{id}` 本身可能含连字符。

```go
var jobPathRe = regexp.MustCompile(
    `^/v[12]/(crawl|batch/scrape|extract)/([^/]+)(/errors)?$`)
var jobSubmitRe = regexp.MustCompile(
    `^/v[12]/(crawl|batch/scrape|extract)$`)
```

`stickyPath` 与主循环完全分开的一个函数：取定 Key、转发一次、`deliver`。不调用 `pool.Report()`——一个 exhausted 的 Key 查自己的任务返回 402 是没有意义的信号，会把已经正确的状态搅乱。

写入映射的时机在 `deliver` 中：若请求路径命中 `jobSubmitRe` 且响应 2xx，则本就要缓冲响应体（因为 `url` 字段需要重写），顺便取出 `id` 写表。两件事共用同一次解析，不重复读。

## URL 重写

只对「需重写集合」的响应缓冲。判定：请求路径命中 `jobSubmitRe`（改 `url`）或 `jobPathRe` 且无 `/errors` 后缀（改 `next`），且响应 `Content-Type` 含 `application/json`。

改写用 `url.Parse` 后替换 `Scheme` 与 `Host`，不要用字符串替换——查询串里可能出现同样的 host 字面量。

解析用 `map[string]any` 而非定义结构体：Firecrawl 的响应字段会随版本增减，用结构体会在序列化回去时静默丢字段。改完后 `json.Marshal` 回写，并更新 `Content-Length`。

## 假上游

`internal/proxy/testdata` 或测试文件内提供一个可编程的 `httptest.Server`：按「第 N 次请求 / 按 Authorization 头 / 按路径」返回预设的状态码与响应体。C3 的全部 AC 都靠它验证，因此它值得写得好用一点——后续 C4、C5 的集成测试也会复用。
