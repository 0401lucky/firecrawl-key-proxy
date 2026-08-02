# 技术设计 — Firecrawl 多 Key 反向代理与管理面板

本文档描述架构、边界与契约。需求与验收标准见 `prd.md`。

## 1. 技术选型

| 层 | 选择 | 说明 |
|---|---|---|
| 语言 | Go 1.22+ | 标准库 `net/http/httputil.ReverseProxy` 直接可用；`ServeMux` 已支持路径参数与方法匹配，无需引入路由库 |
| 存储 | SQLite，驱动 `modernc.org/sqlite` | 纯 Go 实现，`CGO_ENABLED=0` 即可构建，容器基础镜像可用 `alpine` 甚至 `scratch` |
| 日志 | 标准库 `log/slog` | 结构化 JSON 输出，无第三方依赖 |
| 前端 | Vue 3 + Vite + TypeScript + Tailwind CSS | SFC 结构固定；面板状态复杂度低，不引入 Pinia 等状态库 |
| 前端嵌入 | `go:embed` | 构建产物随二进制分发，运行时单进程 |

第三方依赖控制在必要范围内：SQLite 驱动、可能的 `golang.org/x/time/rate`（若需要限流）。不引入 Web 框架、ORM、DI 容器。

## 2. 目录结构

```
firecrawl-proxy/
├── cmd/server/main.go            # 组装依赖、启动 HTTP 服务、优雅关闭
├── internal/
│   ├── config/config.go          # 环境变量加载与校验
│   ├── store/                    # SQLite：schema、迁移、仓储
│   │   ├── db.go
│   │   ├── schema.sql
│   │   ├── upstream_keys.go
│   │   ├── proxy_keys.go
│   │   ├── job_routes.go
│   │   └── sessions.go
│   ├── keypool/                  # 上游 Key 池：状态机 + 轮询选择
│   │   ├── pool.go
│   │   └── state.go
│   ├── proxy/                    # 反向代理
│   │   ├── handler.go            # 主流程：选 Key → 转发 → 转移
│   │   ├── jobroute.go           # 异步任务识别与 job→Key 粘连
│   │   ├── rewrite.go            # 响应体绝对 URL 重写
│   │   └── classify.go           # 上游状态码 → 处置动作
│   ├── auth/
│   │   ├── proxykey.go           # 下游 API Key 签发与校验
│   │   └── session.go            # 面板管理员登录与 session
│   ├── admin/                    # 面板 JSON API handlers
│   │   ├── router.go
│   │   ├── upstream.go
│   │   ├── proxykeys.go
│   │   └── overview.go
│   ├── firecrawl/client.go       # 调用 /team/credit-usage 拉取余额
│   └── webui/
│       ├── embed.go              # //go:embed dist
│       └── dist/                 # Vite 构建输出目录（构建产物，不入库）
├── web/                          # 前端源码
│   ├── src/
│   ├── package.json
│   └── vite.config.ts            # build.outDir 指向 ../internal/webui/dist
├── Dockerfile
├── docker-compose.yml
├── Caddyfile.example
└── README.md
```

`go:embed` 只能嵌入本包目录树内的文件，因此 Vite 的输出目录必须落在 `internal/webui/dist`，不能放在仓库根的 `web/dist`。这是一个容易踩的约束，实现时注意。

## 3. 数据模型

```sql
-- 上游 Firecrawl Key
CREATE TABLE upstream_keys (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    name              TEXT    NOT NULL,
    api_key           TEXT    NOT NULL UNIQUE,
    key_suffix        TEXT    NOT NULL,             -- 末 4 位，展示用
    enabled           INTEGER NOT NULL DEFAULT 1,   -- 管理员手动启停
    state             TEXT    NOT NULL DEFAULT 'available',
                                                    -- available | cooling | exhausted | invalid
    cooldown_until    INTEGER,                      -- unix 秒，state=cooling 时有效
    credits_total     INTEGER,
    credits_remaining INTEGER,
    credits_synced_at INTEGER,
    request_count     INTEGER NOT NULL DEFAULT 0,
    last_error        TEXT,
    last_used_at      INTEGER,
    created_at        INTEGER NOT NULL
);

-- 下游代理 API Key
CREATE TABLE proxy_keys (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT    NOT NULL,
    key_hash      TEXT    NOT NULL UNIQUE,   -- sha256(明文) 的十六进制
    key_prefix    TEXT    NOT NULL,          -- 明文前 12 位，展示用
    revoked       INTEGER NOT NULL DEFAULT 0,
    request_count INTEGER NOT NULL DEFAULT 0,
    last_used_at  INTEGER,
    created_at    INTEGER NOT NULL
);
CREATE INDEX idx_proxy_keys_hash ON proxy_keys(key_hash);

-- 异步任务 → 上游 Key 粘连
CREATE TABLE job_routes (
    job_id          TEXT    PRIMARY KEY,
    upstream_key_id INTEGER NOT NULL REFERENCES upstream_keys(id) ON DELETE CASCADE,
    kind            TEXT    NOT NULL,        -- crawl | batch_scrape | extract
    created_at      INTEGER NOT NULL,
    expires_at      INTEGER NOT NULL
);
CREATE INDEX idx_job_routes_expires ON job_routes(expires_at);

-- 面板会话
CREATE TABLE admin_sessions (
    token_hash TEXT    PRIMARY KEY,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
```

### 存储安全的取舍

上游 Key 必须以明文入库——代理需要用它构造上游请求，任何单向哈希都不可行。防护手段限于：

- 数据库文件仅容器内可读，卷挂载到宿主机受限目录。
- 日志、面板 API 响应一律只输出 `fc-****` + 末 4 位。
- 面板不提供「查看完整上游 Key」的功能，录入后不可回读。

不做应用层加密：主密钥同样要以环境变量注入同一个容器，攻破面等价，只增加复杂度而不增加实际安全性。若日后需要，改用外部 KMS 才有意义。

下游代理 Key 用 SHA-256 而非 bcrypt/argon2：它是程序生成的 32 字节高熵随机串，不存在字典攻击面，而每个代理请求都要校验一次，慢哈希会成为吞吐瓶颈。

## 4. 路由划分

单个 HTTP 服务监听一个端口，按前缀分派到三套完全不同的处理链：

| 路径 | 处理 | 认证 |
|---|---|---|
| `/v1/*`、`/v2/*` | 代理转发（前缀列表可配置） | `Authorization: Bearer <proxy-key>` |
| `/api/admin/*` | 面板 JSON API | HttpOnly session cookie |
| `/healthz` | 健康检查 | 无 |
| 其余 | 嵌入的 SPA 静态资源，未匹配到文件时回退 `index.html` | 无 |

两套认证互不接受对方的凭据：代理链只读 `Authorization` 头，面板链只读 cookie。这满足 AC13。

代理前缀采用配置项而非「一切未匹配路径」，是为了避免与面板路由和静态资源冲突。Firecrawl 未来若发布 `/v3`，改配置即可，无需改代码。

## 5. Key 状态机

```
                   402                          手动禁用
        ┌──────────────────────► exhausted ◄───────────────┐
        │                                                   │
   ┌────┴──────┐   401/403                              ┌───┴─────┐
   │ available ├──────────────────► invalid             │ disabled │
   └────┬──────┘                                        └───▲─────┘
        │                                                   │
        │ 429                                            管理员操作
        ▼
   ┌─────────┐   cooldown_until 到期
   │ cooling ├────────────────────► available
   └─────────┘
```

- `available` — 可被选中。
- `cooling` — 429 触发，携带 `cooldown_until`。到期后自动恢复为 `available`，无需人工介入。判定在选择时惰性完成，不需要后台定时任务。
- `exhausted` — 402 触发。终态，只能由管理员在面板手动重置（例如账号充值后）。
- `invalid` — 401/403 触发。终态，面板高亮告警。
- `disabled` — `enabled=0`，管理员手动关闭，与自动状态正交。

**408/5xx 不进入状态机**。这些是上游自身故障，与 Key 无关。误把它们计入会在 Firecrawl 抖动时把所有 Key 一次性打成不可用，是这类代理最典型的故障模式。

冷却时长优先取 429 响应的 `Retry-After` 头；缺失时用配置的默认值（默认 60 秒）。

### 选择算法

`keypool.Next()` 返回下一个可用 Key：

1. 加锁，遍历内存中的 Key 列表（DB 是持久化副本，内存是运行时权威）。
2. 跳过 `enabled=0`、`exhausted`、`invalid`；对 `cooling` 的，若 `cooldown_until` 已过则就地转回 `available` 并可选中。
3. 在候选集合上按游标轮询（round-robin），游标取模推进。
4. 无候选则返回 `ErrNoKeyAvailable`。

选择过程持有互斥锁，操作是 O(n) 且 n 很小（几十个 Key），无需更复杂的结构。

### 内存与 DB 的一致性

- 状态变更（进入 cooling/exhausted/invalid、冷却恢复）：同步写 DB。频率低。
- `request_count`、`last_used_at`：高频。内存累加，按固定间隔（默认 10 秒）批量刷盘，进程退出前 flush 一次。计数允许在崩溃时丢失最后几秒，这是可接受的精度损失。
- 面板增删 Key：写 DB 后触发内存池重新加载。这是 AC8（新增 Key 立即生效）的实现路径。

## 6. 代理请求流程

```
客户端请求
   │
   ├─ 1. 校验下游 proxy key（sha256 查表，revoked 则 401）
   │
   ├─ 2. 是否携带已知 job id？
   │      │
   │      ├─ 是 → 从 job_routes 取定上游 Key，不参与轮询，不做转移
   │      └─ 否 → keypool.Next()
   │
   ├─ 3. 缓冲请求体（为可能的重放）
   │
   ├─ 4. 构造上游请求，替换 Authorization，转发
   │
   ├─ 5. 按状态码分类处置
   │      ├─ 2xx        → 成功
   │      ├─ 402        → 标记 exhausted，换 Key 重试
   │      ├─ 401/403    → 标记 invalid，换 Key 重试
   │      ├─ 429        → 标记 cooling(Retry-After)，换 Key 重试
   │      ├─ 408/5xx    → 不改 Key 状态，按重试策略处理
   │      └─ 其余 4xx   → 客户端自身的问题，直接透传，不重试不改状态
   │
   ├─ 6. 若为异步提交端点且成功 → 解析响应取 job id，写 job_routes
   │
   ├─ 7. 若响应需重写 → 改写 url / next 字段
   │
   └─ 8. 返回客户端
```

### 6.1 请求体缓冲的约束

故障转移要求把同一个请求重放给另一个 Key，因此请求体必须可重读。Firecrawl 的请求体是 JSON（URL、抓取选项），通常几 KB。

设计：请求体缓冲上限默认 8 MiB（可配置）。超过上限的请求**不缓冲、不支持转移**，直接以当前 Key 单次转发；若失败则原样返回上游响应。这个边界必须在文档中写明，否则会成为一个静默的行为差异。

### 6.2 job id 的识别与粘连

**提交**（需要记录映射）：`POST /v{1,2}/crawl`、`POST /v{1,2}/batch/scrape`、`POST /v{1,2}/extract`。响应 2xx 时解析 JSON 的 `id` 字段，写入 `job_routes`，`expires_at` 取「当前时间 + 配置的保留期」（默认 48 小时，覆盖 Firecrawl 的任务过期时间）。

**查询**（需要按映射路由）：路径匹配
`/v{1,2}/crawl/{id}`、`/v{1,2}/crawl/{id}/errors`、
`/v{1,2}/batch/scrape/{id}`、`/v{1,2}/batch/scrape/{id}/errors`、
`/v{1,2}/extract/{id}`，方法为 `GET` 或 `DELETE`。

查表命中则强制使用该 Key。**命中时不做故障转移**：换 Key 重试只会得到 404，纯属浪费。若该 Key 此时已被标记为 exhausted/invalid，仍然使用它——查询已有任务的状态通常不消耗额度，且这是唯一能拿到结果的途径。这一点与常规请求的处置逻辑相反，实现时不要复用同一条分支。

未命中（例如映射已过期、或客户端拿着别处的 job id）则退化为轮询，返回什么就是什么（大概率 404）。

过期清理：启动时清一次，之后按固定间隔（默认每小时）删除 `expires_at < now` 的记录。

### 6.3 响应 URL 重写

需重写的字段与场景：

| 端点 | 字段 | 值的形态 |
|---|---|---|
| `POST /v{1,2}/crawl` | `url` | `https://api.firecrawl.dev/v2/crawl/{id}` |
| `POST /v{1,2}/batch/scrape` | `url` | `https://api.firecrawl.dev/v2/batch/scrape/{id}` |
| `GET /v{1,2}/crawl/{id}` | `next` | `https://api.firecrawl.dev/v2/crawl/{id}?skip=N` |
| `GET /v{1,2}/batch/scrape/{id}` | `next` | 同上形态 |

重写规则：把 URL 的 scheme + host 替换为配置的 `PUBLIC_BASE_URL`，保留路径与查询串。仅在响应 `Content-Type` 为 JSON 且路径命中上表时才解析响应体；其余响应一律流式透传，不解码。

实现方式：只对上表命中的响应做缓冲解码。`GET /crawl/{id}` 的响应最大约 10 MiB（超过则 Firecrawl 通过 `next` 分页），缓冲上限设 32 MiB 足够。超限时放弃重写、原样透传并记 warning——宁可退化也不要截断用户数据。

`PUBLIC_BASE_URL` 必须由配置显式提供（如 `https://fc.example.com`）。不从请求的 `Host` 头推断：前置网关的转发头可被伪造，且容器内看到的 Host 未必是对外地址。

## 7. 面板 API 契约

所有响应为 JSON。`/api/admin/login` 之外均要求有效 session cookie，否则 401。

```
POST   /api/admin/login              {password}            → 204 + Set-Cookie
POST   /api/admin/logout                                   → 204
GET    /api/admin/session                                  → {authenticated}

GET    /api/admin/overview                                 → 额度池汇总 + Key 状态摘要
GET    /api/admin/upstream-keys                            → [{id,name,masked,state,cooldown_remaining,
                                                              credits_remaining,credits_total,
                                                              request_count,last_error,enabled}]
POST   /api/admin/upstream-keys      {name, api_key}       → 201 + 该条记录
PATCH  /api/admin/upstream-keys/{id} {name?,enabled?,reset?} → 200
DELETE /api/admin/upstream-keys/{id}                       → 204
POST   /api/admin/upstream-keys/{id}/refresh-credits       → 200 + 最新额度

GET    /api/admin/proxy-keys                               → [{id,name,key_prefix,request_count,
                                                              last_used_at,created_at,revoked}]
POST   /api/admin/proxy-keys         {name}                → 201 + {…, plaintext_key}
DELETE /api/admin/proxy-keys/{id}                          → 204
```

`plaintext_key` 只在创建响应中出现一次，之后任何接口都不再返回。`masked` 恒为 `fc-****` + 末 4 位，任何接口都不返回上游 Key 明文。

`PATCH` 的 `reset` 用于把 `exhausted`/`invalid` 的 Key 手动拉回 `available`（账号充值或换绑后）。

额度拉取（`refresh-credits`）是**面板触发的显式动作**与低频后台刷新，不参与调度决策。后台刷新间隔默认 10 分钟，且只刷新 `available` 状态的 Key，避免对已失效的 Key 反复发请求。

## 8. 配置项

全部通过环境变量注入，便于 compose 管理。

| 变量 | 默认值 | 说明 |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | 监听地址 |
| `PUBLIC_BASE_URL` | 必填 | 对外地址，用于响应 URL 重写 |
| `DB_PATH` | `/data/proxy.db` | SQLite 文件路径 |
| `ADMIN_PASSWORD` | 必填 | 面板管理员密码 |
| `UPSTREAM_BASE_URL` | `https://api.firecrawl.dev` | 上游地址 |
| `PROXY_PATH_PREFIXES` | `/v1/,/v2/` | 需转发的路径前缀 |
| `MAX_FAILOVER_ATTEMPTS` | `3` | 单个请求最多尝试的 Key 数 |
| `DEFAULT_COOLDOWN_SECONDS` | `60` | 429 无 `Retry-After` 时的冷却时长 |
| `MAX_REQUEST_BUFFER_BYTES` | `8388608` | 请求体缓冲上限，超过则不支持转移 |
| `JOB_ROUTE_TTL_HOURS` | `48` | job 映射保留时长 |
| `CREDIT_REFRESH_MINUTES` | `10` | 后台额度刷新间隔，0 表示关闭 |
| `SESSION_TTL_HOURS` | `168` | 面板会话有效期 |
| `LOG_LEVEL` | `info` | 日志级别 |

启动时校验必填项，缺失直接退出并给出明确提示，不静默使用不安全的默认值。

## 9. 错误响应契约

代理自身产生的错误（而非上游返回的）统一为：

```json
{ "error": "no_upstream_key_available", "message": "所有上游 Key 均不可用", "detail": {...} }
```

| 场景 | 状态码 | `error` |
|---|---|---|
| 缺失或无效的代理 Key | 401 | `invalid_proxy_key` |
| 所有上游 Key 均不可用 | 503 | `no_upstream_key_available` |
| 转移次数耗尽仍失败 | 502 | `upstream_failover_exhausted` |
| 请求路径不在转发前缀内 | 404 | `not_found` |

`detail` 中带上各 Key 的状态计数（如 `{exhausted: 3, cooling: 1, invalid: 0}`），让调用方能一眼区分「额度用完了」和「Firecrawl 挂了」。这是 AC6 的实现方式。

## 10. 日志

`log/slog` 输出 JSON。每个代理请求一条记录：

```
level=info msg="proxy request" method=POST path=/v2/scrape
  proxy_key=本地脚本 upstream_key=fc-****a1c9 upstream_status=200
  failover_count=1 duration_ms=1243
```

上游 Key 一律以脱敏形式出现。代理 Key 记名称而非值。审计时能回答「这次请求用了哪个账号、是否发生过转移」。

## 11. 测试策略

用 `httptest` 起一个**假 Firecrawl 上游**，按测试用例返回指定状态码与响应体。这是验证故障转移逻辑的核心手段，不依赖真实账号，可在 CI 中运行。

| 测试 | 覆盖 |
|---|---|
| `keypool` 状态转移表驱动测试 | AC1, AC2, AC3 |
| 402 → 换 Key 成功的端到端测试 | AC1 |
| 429 + `Retry-After` → 冷却时长与恢复 | AC2 |
| 500 → Key 状态不变 | AC3 |
| crawl 提交后多次轮询命中同一 Key | AC4 |
| `url` / `next` 字段重写 | AC5 |
| 全部 Key 不可用 → 503 与错误体结构 | AC6 |
| 代理 Key 缺失/吊销 → 401 | AC7 |
| 面板新增 Key 后立即可被选中 | AC8 |
| 代理 Key 明文不入库 | AC9 |
| 重启后状态与 job 映射保留 | AC10 |
| 日志与 API 输出的脱敏检查 | AC11 |
| session cookie 与代理 Key 的互斥 | AC13 |

## 12. 已知取舍

- **请求体超过缓冲上限时不支持故障转移**。这是有意的权衡，避免为极少数大请求把内存占用变成攻击面。行为需在 README 中写明。
- **额度展示可能滞后**。后台刷新间隔默认 10 分钟，面板数字不是实时值。调度不依赖它，因此不影响正确性。
- **`request_count` 允许在崩溃时丢失最后几秒**。换取每请求一次 DB 写入的开销消除。
- **job 映射过期后无法路由**。TTL 默认 48 小时，长于 Firecrawl 自身的任务过期时间，实践中不会构成问题。
- **上游 Key 明文入库**。见 §3 的说明，这是功能本身决定的，不是疏漏。
