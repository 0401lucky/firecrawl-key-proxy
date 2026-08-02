# C5 实施计划 — 面板后端 API

## 前置

C1、C2 完成；C4 的 `Issue` / `Revoke` 可用（可与 C4 并行开发，接线时汇合）。

## 步骤

1. **`internal/auth/session.go`**
   登录、登出、session 中间件、递增延迟防护。
   验证：密码正确/错误、cookie 有效/过期/伪造、连续失败后的延迟——各一个测试。

2. **`internal/admin/router.go` + 错误响应辅助**
   装配 `ServeMux` 子路由与统一错误结构。
   验证：未登录访问受保护路由返回 401 且响应体符合约定格式。

3. **上游 Key 的 CRUD**
   四个 handler，写操作后 `Reload()`。
   验证：AC8（新增即生效）、AC11（脱敏）、`reset` 语义、级联删除 job 映射。

4. **`internal/firecrawl/client.go` 与额度拉取**
   `GetCreditUsage` + `refresh-credits` handler + 后台 ticker。
   验证：用假上游返回额度 JSON，断言写回正确；返回 401 时不改 Key 状态、只记 warning。

5. **代理 Key 的接口**
   包装 C4 的 `Issue` / `Revoke`。
   验证：创建响应含 `plaintext_key`，列表响应不含。

6. **`GET /api/admin/overview`**
   验证：额度求和与状态计数与 `Snapshot()` 一致。

7. **SPA 静态服务与路由兜底**
   `internal/webui/embed.go` + `spaHandler`。此时 `dist` 目录可先放一个占位 `index.html`，真实产物由 C6 提供。
   验证：`/keys` 返回 `index.html`；`/v2/unknown` 返回 404 而非 HTML；`/healthz` 不受 session 影响。

8. **认证隔离验证**
   验证：AC13 双向断言——代理 Key 访问面板 401，session cookie 访问代理 401。

## 验证命令

```bash
go test -race ./internal/admin/... ./internal/auth/... ./internal/firecrawl/...
go test ./...
go vet ./...
```

## 风险点

- **SPA 兜底吃掉了 API 的 404**。客户端调用一个不存在的 `/v2/xxx` 拿到 200 + HTML，会以极难排查的方式表现为「SDK 解析失败」。步骤 7 的测试必须显式覆盖。
- **上游 Key 明文从某个接口漏出**。最容易漏的是 `POST` 创建后的回显响应与错误信息中的 `detail`。写完后 grep 一遍所有 JSON 序列化路径，确认 `api_key` 字段永远不参与序列化（结构体上打 `json:"-"`）。
- **后台额度刷新阻塞或 panic 影响主流程**。独立 goroutine + recover，且循环内的错误一律 continue。
- **额度值被误用于调度**。`SetCredits` 写入的字段不得出现在 `keypool.Next()` 的任何判断中。评审时确认。

## 回滚点

新增 `internal/admin`、`internal/auth/session.go`、`internal/firecrawl`。放弃时移除路由挂载，代理转发功能不受影响。
