<!-- TRELLIS:START -->
# Trellis Instructions

These instructions are for AI assistants working in this project.

This project is managed by Trellis. The working knowledge you need lives under `.trellis/`:

- `.trellis/workflow.md` — development phases, when to create tasks, skill routing
- `.trellis/spec/` — package- and layer-scoped coding guidelines (read before writing code in a given layer)
- `.trellis/workspace/` — per-developer journals and session traces
- `.trellis/tasks/` — active and archived tasks (PRDs, research, jsonl context)

If a Trellis command is available on your platform (e.g. `/trellis:finish-work`, `/trellis:continue`), prefer it over manual steps. Not every platform exposes every command.

If you're using Codex or another agent-capable tool, additional project-scoped helpers may live in:
- `.agents/skills/` — reusable Trellis skills
- `.codex/agents/` — optional custom subagents

Managed by Trellis. Edits outside this block are preserved; edits inside may be overwritten by a future `trellis update`.

<!-- TRELLIS:END -->

---

# 项目说明（给接手的 AI 与开发者）

## 这是什么

Go 编写的 HTTP 反向代理，把多个 Firecrawl 账号的 API Key 聚合成一个入口。调用方只持有代理签发的一个 Key，代理负责轮询选 Key、被动感知额度、故障转移，并提供 Vue 3 管理面板。附带一个可选的 MCP 端点，供只支持远程 MCP 的客户端接入。

**规划文档在 `.trellis/tasks/archive/2026-08/`**，父任务 `08-02-firecrawl-key-proxy` 的 `prd.md`（需求与 AC1–AC15）和 `design.md`（架构、数据模型、请求流程、API 契约）是权威依据，改动前先读。

## 技术栈与结构

Go 1.22+ · SQLite（`modernc.org/sqlite`，纯 Go 无 CGO）· `log/slog` · Vue 3 + Vite + TS + Tailwind（`go:embed` 嵌入）

```
cmd/server/main.go        组装依赖、路由装配、优雅关闭
internal/
  config/                 环境变量加载与校验
  store/                  SQLite schema 与四张表的仓储层
  keypool/                上游 Key 池：轮询选择、状态机、冷却恢复
  proxy/                  透明转发、故障转移、URL 重写、job 粘连
  auth/                   下游 API Key 认证 + 面板 session
  admin/                  面板 JSON API
  firecrawl/              代理「作为客户端」调用上游（额度查询）
  webui/dist/             前端构建产物（Vite 的 build.outDir 指向这里）
web/                      前端源码
```

## 不可破坏的约束

这些每一条都对应一个真实踩过的坑，改代码前务必确认没有违反。

**1. 5xx / 408 / 网络错误绝不惩罚 Key。** `proxy/classify.go` 的 `shouldRetry`（要不要换 Key 重试）与 `keypool/state.go` 的 `classify`（这个 Key 该不该被惩罚）是**两个独立判断**，合并成一个布尔就会让 Firecrawl 抖动一次把所有 Key 依次打成不可用。表驱动测试必须显式覆盖这两类并断言状态不变。

**2. 命中 job 映射的请求走独立分支。** `proxy/handler.go` 的 `stickyPath` 不做故障转移、不调 `pool.Report()`，且无视 Key 当前状态照常使用——换 Key 只会得到 404，而查询已有任务通常不消耗额度。不要复用主循环。

**3. 需要解析响应体的路径不得透传客户端的 `Accept-Encoding`。** Go 的 Transport 只有在调用方未显式设置该头时才透明解压。透传会让 `resp.Body` 是 gzip 字节，JSON 解析静默失败 → `url`/`next` 不重写、job 映射不记录，而响应仍是 200。官方 SDK 默认都发 gzip，即生产必然触发。见 `internal/proxy/gzip_test.go`。

**4. `keypool` 的 `Next`/`NextExcluding`/`GetByID` 必须返回副本。** 交出内部指针会让面板的 PATCH 处理器在锁外改动池的权威状态，与持锁的 `Report`/`next`/`Snapshot` 构成数据竞争。见 `internal/keypool/alias_test.go`。

**5. 额度数据绝不参与调度。** `SetCredits` 写入的字段不得出现在 `Next()` 的任何判断中。调度只依赖被动感知，这样额度接口挂掉不影响转发。

**6. 上游 Key 明文不得外泄。** 结构体字段打 `json:"-"`，日志一律经 `logging.MaskKey`。面板不提供「查看完整 Key」功能。

**7. 两套认证互不通用。** 代理路径只读 `Authorization`，面板只读 session cookie。

**8. SPA 兜底不得吞掉 API 的 404。** `internal/webui` 对 `/v1/`、`/v2/`、`/api/` 前缀返回 404 而非 `index.html`，否则客户端拿到 200 + HTML，表现为极难排查的「SDK 解析失败」。

**9. `go:embed` 只能嵌入本包目录树内的文件。** Vite 的 `build.outDir` 必须是 `internal/webui/dist`。仓库保留占位 `index.html`，让未构建前端时 `go build` 仍能通过。

## 上游 API 的实测事实（与官方文档不符之处）

- **额度接口必须带版本前缀**：`/team/credit-usage` 会 404，正确是 `/v2/team/credit-usage`。文档的端点页省略了 base URL 里的版本段。
- **额度响应套了 `data` 外层且字段命名随版本变化**：
  `v2: {"success":true,"data":{"remainingCredits":N,"planCredits":N}}`，
  `v1: {"success":true,"data":{"remaining_credits":N,"plan_credits":N}}`。
  文档描述的扁平 `{credits_total,credits_used,credits_remaining}` 实测不存在。`internal/firecrawl` 三种形状都兼容。
- **错误语义**：401/403 是 Key 终态，402 是额度耗尽（终态），429 是临时冷却（遵守 `Retry-After`），408/5xx 是上游故障。
- **异步任务**：`POST /v{1,2}/crawl`、`/batch/scrape`、`/extract` 返回的 job 只有提交它的 Key 能查。响应里的 `url` 与 `next` 是指向 `api.firecrawl.dev` 的**绝对地址**，不重写客户端会绕过代理直连。
- **`firecrawl-mcp` 在 `HTTP_STREAMABLE_SERVER` 模式下忽略客户端请求头里的 key**（`x-firecrawl-api-key`/`Authorization`/`x-api-key` 均无效），只认服务端环境变量；且默认绑 `localhost`，容器化需显式 `HOST=0.0.0.0`。

## 常用命令

```bash
CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...
cd web && npm ci && npm run build          # 产物落到 internal/webui/dist
docker compose up -d                       # 仅代理
docker compose --profile mcp up -d         # 代理 + MCP 端点
```

测试全部基于 `internal/proxy` 里可编程的假上游，不消耗真实额度。

**注意**：`go test -race` 需要 CGO 与 C 编译器。开发机若无 gcc 则跑不了竞态检测——并发相关改动务必在有 gcc 的环境验证。

## 生产部署现状

已部署在一台 Zeabur 自带节点上（k3s 的 ingress 占着 80/443，不可抢占），因此走 Cloudflare Tunnel：

```
客户端 → Cloudflare 边缘(TLS) → cloudflared 隧道 → 127.0.0.1:8081 → 容器
```

- 面板与代理：`https://firecrawl.lucky0625.us.ci`
- MCP 端点：`https://firecrawl-mcp.lucky0625.us.ci/mcp`（需 `X-MCP-Token` 请求头）
- 容器只绑回环地址，公网无法绕过 Cloudflare 直连

## 已知限制

- 不支持多副本：Key 池状态与轮询游标在进程内存，扩容需把状态外置。
- 请求体超过 8 MiB 不做故障转移（避免大请求体反复重传）。
- 轮询不按剩余额度加权，额度少的 Key 会先撞一次 402 再被移出候选。
- 面板无多用户体系，单管理员密码。
- 程序不处理 TLS 证书。
