# C5 — 面板后端 API

父任务：`08-02-firecrawl-key-proxy`。API 契约见父任务 `design.md` §7，路由与认证隔离见 §4。

## Goal

为管理面板提供 JSON API：管理员登录、上游 Key 的增删改查与状态展示、额度拉取、代理 API Key 的签发与吊销。C6 的前端完全依赖本任务定下的契约，因此接口形状一旦发布就应稳定。

## Requirements

### R5.1 管理员登录

- `POST /api/admin/login` 接收 `{password}`，与 `ADMIN_PASSWORD` 常量时间比较。成功则生成 32 字节随机 session token，存 `sha256` 到 `admin_sessions`（`expires_at = now + SESSION_TTL_HOURS`），并以 `HttpOnly`、`SameSite=Lax`、`Path=/` 的 cookie 下发。失败返回 401。
- `POST /api/admin/logout` 删除当前 session 并清除 cookie。
- `GET /api/admin/session` 返回 `{authenticated: bool}`，供前端启动时判断是否需要跳登录页。
- 其余 `/api/admin/*` 均需有效 session，否则 401。
- 登录接口需要有基础的暴力破解防护：同一来源 IP 连续失败达阈值后引入递增延迟。

### R5.2 上游 Key 管理

- `GET /api/admin/upstream-keys` → 列表，每项含 `id`、`name`、`masked`、`state`、`cooldown_remaining`（秒）、`credits_total`、`credits_remaining`、`credits_synced_at`、`request_count`、`last_error`、`enabled`、`created_at`。
- `POST /api/admin/upstream-keys` `{name, api_key}` → 201。写库后调用 `keypool.Reload()`，使新 Key 立即可被选中。
- `PATCH /api/admin/upstream-keys/{id}` `{name?, enabled?, reset?}` → 200。`reset=true` 把 `exhausted`/`invalid` 的 Key 拉回 `available` 并清空 `last_error`。写库后 `Reload()`。
- `DELETE /api/admin/upstream-keys/{id}` → 204。级联删除其 job 映射。写库后 `Reload()`。
- **`masked` 恒为 `fc-****` + 末 4 位。任何接口、任何情况都不返回上游 Key 明文，也不提供「查看完整 Key」的功能。**

### R5.3 额度拉取

- `internal/firecrawl/client.go` 提供 `GetCreditUsage(ctx, apiKey)`，调用上游 `GET /team/credit-usage`，返回 `{credits_total, credits_used, credits_remaining}`。
- `POST /api/admin/upstream-keys/{id}/refresh-credits` 立即拉取并写回，返回最新值。
- 后台按 `CREDIT_REFRESH_MINUTES` 间隔刷新，**只刷新 `available` 状态且 `enabled=1` 的 Key**——对已失效的 Key 反复发请求没有意义。间隔为 0 时关闭后台刷新。
- 额度数据仅用于展示，任何情况下都不参与 `keypool.Next()` 的候选判断。
- 拉取失败只记 warning 并保留上次的值，不改变 Key 状态。

### R5.4 代理 API Key 管理

- `GET /api/admin/proxy-keys` → 列表，含 `id`、`name`、`key_prefix`、`request_count`、`last_used_at`、`created_at`、`revoked`。
- `POST /api/admin/proxy-keys` `{name}` → 201，响应体含 `plaintext_key`。**这是明文唯一一次出现的地方**，其余任何接口都不再返回。
- `DELETE /api/admin/proxy-keys/{id}` → 204，调用 C4 的 `Revoke`。

### R5.5 总览

- `GET /api/admin/overview` → `{credits_remaining_sum, credits_total_sum, key_counts: {available, cooling, exhausted, invalid, disabled}, proxy_key_count, last_refreshed_at}`。数据取自 `keypool.Snapshot()` 与仓储，不额外查上游。

### R5.6 静态资源与路由

- `/api/admin/*` 走 session 中间件。
- `/healthz` 无认证。
- 其余未匹配路径服务 `internal/webui` 中嵌入的 SPA：命中文件则返回文件，否则回退 `index.html`（前端路由需要）。回退逻辑不得覆盖 `/v1/`、`/v2/`、`/api/` 前缀。

## Acceptance Criteria

- [ ] **AC8**：通过 `POST /api/admin/upstream-keys` 新增一个 Key 后，无需重启，`keypool.Next()` 即可能返回它。
- [ ] **AC11**：`GET /api/admin/upstream-keys` 的响应中不含上游 Key 明文，只有 `fc-****` + 末 4 位；grep 整个响应体无完整 Key。
- [ ] **AC13**：用代理 API Key 作为 `Authorization` 头访问 `/api/admin/upstream-keys` 返回 401；用面板 session cookie 访问 `/v2/scrape` 返回 401。
- [ ] **AC14**：`/healthz` 无需 cookie 即返回 200。
- [ ] `POST /api/admin/proxy-keys` 的响应含 `plaintext_key`；随后 `GET /api/admin/proxy-keys` 的响应不含它。
- [ ] 密码错误时 `POST /api/admin/login` 返回 401 且不下发 cookie；连续失败 5 次后第 6 次请求出现可观测的延迟。
- [ ] `PATCH` 带 `reset=true` 后，原本 `exhausted` 的 Key 状态变为 `available` 且 `last_error` 被清空。
- [ ] `DELETE` 一个有 job 映射的上游 Key 后，`job_routes` 中对应记录被级联删除。
- [ ] `refresh-credits` 在上游返回 401 时不改变该 Key 的状态，只记 warning。
- [ ] 未登录访问任意 `/api/admin/*`（除 `login`、`session`）返回 401。
- [ ] SPA 回退：访问 `/keys` 返回 `index.html`；访问 `/v2/unknown` 不返回 `index.html`。

## Out of Scope

- 前端页面——属于 C6。
- 请求日志检索、用量图表、告警阈值。父任务已明确排除。
- 多管理员、角色权限、密码修改接口。单管理员密码由环境变量提供。

## Notes

`keypool.Reload()` 在每次写操作后调用。Key 数量是几十级，重载成本可忽略，比维护增量同步的正确性划算得多。

后台额度刷新是唯一一处代理自身主动向 Firecrawl 发请求的地方。它必须与调度解耦——若这个刷新任务出错或被关闭，代理的转发与故障转移能力应完全不受影响。实现时用独立 goroutine，panic 需 recover 且不影响主流程。
