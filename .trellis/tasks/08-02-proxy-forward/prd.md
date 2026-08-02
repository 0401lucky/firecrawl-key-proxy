# C3 — 代理转发与故障转移

父任务：`08-02-firecrawl-key-proxy`。请求流程见父任务 `design.md` §6。

## Goal

实现代理的主路径：把客户端请求透明转发到 Firecrawl、失败时自动换 Key 重试、维护异步任务的 Key 粘连、改写响应中的绝对 URL。这是整个项目对外行为的落点，AC1–AC6 全部由本任务承担。

## Requirements

### R3.1 透明转发

- 只处理 `PROXY_PATH_PREFIXES` 覆盖的路径（默认 `/v1/`、`/v2/`），其余返回 404 + `{"error":"not_found"}`。
- 保留原始方法、路径、查询串、请求体，以及除 `Authorization` 外的全部请求头。
- 用选中的上游 Key 替换 `Authorization: Bearer <key>`。
- 上游响应的状态码与响应头原样返回；响应体在不需要重写时**流式**透传，不整体读入内存。

### R3.2 故障转移

- 依据 `keypool.Report()` 的分类结果决定是否换 Key 重试：402 / 401 / 403 / 429 / 408 / 5xx / 网络错误 → 重试；2xx 与其他 4xx → 直接返回给客户端，不重试。
- 每次重试用 `NextExcluding()` 换一个未尝试过的 Key。
- 尝试次数上限为 `MAX_FAILOVER_ATTEMPTS`。
- 无可用 Key → 503 + `{"error":"no_upstream_key_available", ..., "detail":{状态计数}}`。
- 次数耗尽仍失败 → 502 + `{"error":"upstream_failover_exhausted"}`。

### R3.3 请求体缓冲

- 转发前把请求体读入内存以支持重放，上限 `MAX_REQUEST_BUFFER_BYTES`（默认 8 MiB）。
- 超过上限的请求不缓冲，以流式方式单次转发，**不支持故障转移**；失败时原样返回上游响应。

### R3.4 异步任务 Key 粘连

- **写入**：`POST` 到 `/v{1,2}/crawl`、`/v{1,2}/batch/scrape`、`/v{1,2}/extract` 且响应 2xx 时，解析响应 JSON 的 `id` 字段，写入 `job_routes`（`expires_at = now + JOB_ROUTE_TTL_HOURS`）。解析失败只记 warning，不影响响应返回。
- **读取**：`GET` / `DELETE` 命中 `/v{1,2}/crawl/{id}`、`/v{1,2}/crawl/{id}/errors`、`/v{1,2}/batch/scrape/{id}`、`/v{1,2}/batch/scrape/{id}/errors`、`/v{1,2}/extract/{id}` 时，查表取定上游 Key。
- 命中映射的请求**强制使用该 Key，不做故障转移**，即使该 Key 当前为 `exhausted` 或 `invalid` 也照用——换 Key 只会得到 404，而查询已有任务通常不消耗额度。
- 未命中映射则退化为常规轮询。
- 启动时清理一次过期映射，之后每小时清理一次。

### R3.5 响应 URL 重写

- 仅当响应 `Content-Type` 为 JSON 且请求路径命中「需重写集合」时，才缓冲并解析响应体。
- 改写 `url`（提交端点响应）与 `next`（状态查询响应）字段：把 scheme + host 替换为 `PUBLIC_BASE_URL`，保留路径与查询串。
- 响应体超过 32 MiB 时放弃重写、原样透传并记 warning，不得截断。
- 不从请求 `Host` 头推断对外地址。

### R3.6 日志

每个代理请求输出一条结构化日志，含：方法、路径、代理 Key 名称、上游 Key（脱敏）、上游状态码、转移次数、耗时。

## Acceptance Criteria

以下均以 `httptest` 搭建的假上游验证，不依赖真实 Firecrawl 账号。

- [ ] **AC1**：3 个 Key，第一个返回 402，请求最终由第二个 Key 成功完成；第一个 Key 状态变为 `exhausted`。
- [ ] **AC2**：第一个 Key 返回 429 + `Retry-After: 30`，请求由第二个完成；第一个进入冷却且剩余约 30 秒。
- [ ] **AC3**：第一个 Key 返回 500，发生重试，但第一个 Key 状态仍为 `available`。
- [ ] **AC4**：`POST /v2/crawl` 返回 `{"id":"abc"}` 后，连续 5 次 `GET /v2/crawl/abc` 全部命中同一个上游 Key。
- [ ] **AC5**：`POST /v2/crawl` 响应的 `url` 与 `GET /v2/crawl/{id}` 响应的 `next` 均被改写为 `PUBLIC_BASE_URL` 开头，路径与查询串保持不变，响应体中不再出现 `api.firecrawl.dev`。
- [ ] **AC6**：全部 Key 不可用时返回 503，响应体含 `error` 与按状态分类的 `detail` 计数。
- [ ] 上游返回 400（客户端参数错误）时直接透传给客户端，不重试、不改变任何 Key 状态。
- [ ] 转移次数达到 `MAX_FAILOVER_ATTEMPTS` 仍失败时返回 502 且 `error` 为 `upstream_failover_exhausted`。
- [ ] 请求体超过 `MAX_REQUEST_BUFFER_BYTES` 时仍能成功转发（单次，不重试）。
- [ ] 非 JSON 响应（如二进制截图）流式透传，内容逐字节一致，未被解析或改写。
- [ ] 未在 `PROXY_PATH_PREFIXES` 内的路径返回 404 而非转发。
- [ ] job 映射过期后，同一个 job id 的查询退化为轮询（返回上游给什么就是什么）。
- [ ] `go test -race ./internal/proxy/...` 无告警。

## Out of Scope

- 下游代理 Key 的校验——属于 C4。本任务假设请求已通过认证中间件，能拿到调用方身份。
- Key 的选择与状态判断逻辑——属于 C2。本任务只调用 `Next()` / `NextExcluding()` / `Report()`。
- 响应内容的缓存或改写（URL 重写除外）。

## Notes

本任务有两处逻辑与直觉相反，实现时需特别注意，也是评审的重点：

1. **5xx 与网络错误要重试但不惩罚 Key**。重试和惩罚是两件独立的事，不要合并成一个布尔。
2. **命中 job 映射的请求不做故障转移，且无视 Key 的当前状态**。这条分支与常规路径的处置完全相反，不要复用同一段代码。
