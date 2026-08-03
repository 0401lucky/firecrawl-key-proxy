# 注册器接入契约（registerer ↔ 代理）

## 1. Scope / Trigger

Firecrawl 自动注册模块（registerer/，Python）与 Go 代理之间的上传接口。
涉及新 API、新 env、跨层数据流（Python 脚本 → HTTP → SQLite → keypool），
按 code-spec 深度记录。

## 2. Signatures

```
POST /api/register/keys
Header: X-Register-Token: <REGISTER_TOKEN>
Body:   {"name": string, "api_key": string}
响应:   201 + upstreamKeyDTO（masked） / 401 / 409 / 400 / 503
```

Go 侧实现：`internal/admin/register.go` 的 `handleRegisterCreateKey`，
写库与入池复用 `createUpstreamKey`（与面板 `POST /api/admin/upstream-keys` 共用）。

## 3. Contracts

### 环境变量

| env | 必填 | 说明 |
|---|---|---|
| `REGISTER_TOKEN` | 否 | 注册器共享 token；为空时接口 503（功能未启用） |

**compose gotcha**：docker-compose.yml 的 proxy 服务 `environment` 是**逐个列举**
的，新增 env 必须同步在 compose 加一行 `REGISTER_TOKEN: ${REGISTER_TOKEN:-}`，
否则容器内拿不到（表现为生产 503、本地正常）。

### 路由挂载（mux 前缀）

`adminServer.Router()` 挂在 **`/api/`** 前缀（不是 `/api/admin/`）：
- `/api/admin/*` 走 SessionAuth（面板，session cookie）
- `/api/register/*` 走 X-Register-Token（无人值守脚本，不维护 session）
两套认证互不通用（约束 #7 的延伸）。

### 认证与幂等

- token 用 `crypto/subtle.ConstantTimeCompare` 比较，防时序侧信道
- 重复上传同一 api_key → 409（SQLite 唯一约束），注册器视为成功（已存在）

## 4. Validation & Error Matrix

| 条件 | 状态码 | error |
|---|---|---|
| REGISTER_TOKEN 未配置 | 503 | `register_not_enabled` |
| token 缺失/错误 | 401 | `invalid_register_token` |
| body 非 JSON / 缺 name 或 api_key | 400 | `invalid_body` / `invalid_input` |
| api_key 已存在 | 409 | `duplicate_api_key` |
| 写库失败（其他） | 500 | `internal_error` |
| 成功 | 201 | upstreamKeyDTO（**masked**，`fc-****末4位`） |

响应 DTO 与面板共用 `upstreamKeyDTO`，任何路径都不回明文（约束 #6）。

## 5. Good / Base / Bad Cases

- Good：注册器注册成功 → POST key → 201 → 面板可见、立即参与调度
- Base：重复上传（注册器重试/并发撞库）→ 409 → 注册器判「已存在」继续
- Bad：明文 key 出现在日志或响应 → 违反约束 #6（`logging.MaskKey` 是唯一出口）

## 6. Tests Required

`internal/admin/register_test.go`：
- 未配置 token → 503（`register_not_enabled`）
- 错 token / 空 token / 部分匹配 → 401
- 正确 token → 201 + 响应含 masked 不含明文 + `pool.Snapshot()` 可见
- 重复 → 409
- 缺字段 → 400
- 不带 session cookie → 201（证明不走 SessionAuth）

## 7. Wrong vs Correct

### Wrong

```go
// 直接把 token 放进 SessionAuth 或复用面板密码——注册器是脚本，
// 拿不到 session；面板密码泄漏面也更大。
```

### Correct

```go
// 独立 token 中间件：未配置即 503，配置后常量时间比较。
// 注册器 .env 与服务器 .env 各存一份，服务器侧可单独吊销。
```

## 8. 注册器侧要点（Python）

- Temp Mail API（自建 cloudflare_temp_email）：
  - 创建邮箱：`POST /admin/new_address`（`x-admin-auth`），body 带
    `enableRandomSubdomain: true` → 返回 `{"address","jwt","address_id"}`
  - 查邮件：`GET /admin/mails?address=` → **`{"results": [...]}`** 容器，
    raw 是 RFC822，需 `email.parser` 解析 subject/html/text
  - 域名列表：`GET /open_api/settings`（免认证）→ `randomSubdomainDomains`，
    注册器自动拉取并 round-robin 轮询，无需手配
- 上传成功判定：201 或 409 都算成功（已存在跳过）
- 本地敏感文件（accounts.csv 含密码明文、proxies.txt）已在 .gitignore 排除，
  新增敏感输出文件时记得同样处理
