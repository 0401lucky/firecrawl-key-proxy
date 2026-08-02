# 实施计划（父任务）— Firecrawl 多 Key 反向代理与管理面板

本文件是跨子任务的执行编排与集成验收。各子任务的详细步骤在自己的 `implement.md` 中。

## 执行顺序

```
C1 骨架与存储层
      │
      ▼
C2 Key 池与状态机
      │
      ▼
C3 代理转发与故障转移
      │
      ├──────────────┐
      ▼              ▼
C4 下游认证      C5 面板后端 API      （可并行）
      └──────┬───────┘
             ▼
      C6 面板前端 SPA
             │
             ▼
      C7 容器化与部署
```

依赖理由：

- **C2 依赖 C1** — 需要 `UpstreamKeyRepo` 与 `KeyState` 类型。
- **C3 依赖 C2** — 转发循环调用 `Next` / `NextExcluding` / `Report`。
- **C4、C5 依赖 C1/C2**，彼此独立，可并行；C5 步骤 5 需要 C4 的 `Issue` / `Revoke`，两者在接线时汇合。
- **C6 依赖 C5** — 前端需要真实后端联调，且 API 契约必须先冻结。
- **C7 依赖全部** — Dockerfile 要能构建前端与后端。

## 集成检查点

每个检查点在对应子任务完成后立即执行。检查点失败时不进入下一个子任务——这类问题越往后越难定位。

### 检查点 A（C1 后）

```bash
CGO_ENABLED=0 go build ./... && go test ./... && go vet ./...
```
外加：设好必填环境变量运行二进制，`curl localhost:8080/healthz` 得 200；SIGTERM 后干净退出。

### 检查点 B（C2 后）

```bash
go test -race ./internal/keypool/...
```
**重点确认**：500 与网络错误的用例断言了「Key 状态不变」。这是全项目最容易写错、后果最严重的一条，必须在这里挡住。

### 检查点 C（C3 后）

```bash
go test -race ./internal/proxy/...
```
此时 AC1–AC6 应全部通过（用假上游）。逐条对照父任务 `prd.md` 的验收清单确认，不要只看测试是否全绿——测试可能没覆盖到某条 AC。

### 检查点 D（C4 + C5 后）

```bash
go test -race ./...
```
外加手工验证认证隔离（AC13）：

```bash
# 代理 Key 访问面板 → 期望 401
curl -H "Authorization: Bearer fcp_xxx" localhost:8080/api/admin/upstream-keys
# session cookie 访问代理 → 期望 401
curl -b "session=xxx" -X POST localhost:8080/v2/scrape
```

### 检查点 E（C6 后）

```bash
cd web && npm ci && npm run build && cd .. && go build ./...
```
外加：运行二进制，浏览器走一遍登录 → 录入上游 Key → 创建代理 Key → 用该 Key curl 一次 `/v2/scrape` 的完整链路。**这是第一次端到端跑通全链路**，如果这里不通，问题大概率在前面某个检查点被放过了。

### 检查点 F（C7 后）—— 最终验收

对照父任务 `prd.md` 的 AC1–AC14 逐条走查。AC10（重启保留）与 AC12（compose 持久化）在此处一并验证：

```bash
docker compose up -d
# 面板录入上游 Key 与代理 Key，制造一个 exhausted 状态
docker compose down && docker compose up -d
# 确认 Key、状态、job 映射均保留
```

## 真实账号验证（可选但建议）

自动化测试全部基于假上游，不消耗真实额度。全部完成后，建议用 2–3 个真实 Firecrawl 免费账号做一次冒烟：

1. 面板录入真实 Key，确认额度能正确拉取显示。
2. 用代理 Key 调一次 `POST /v2/scrape`，确认返回真实内容。
3. 用官方 Python SDK 把 `api_url` 指向代理，跑一次 `crawl` 并轮询到完成——这一步同时验证 job 粘连与 URL 重写在真实 SDK 下确实生效。第 3 步是假上游无法完全替代的。

## 全局风险

按后果排序，跨子任务的共性风险：

1. **5xx / 网络错误被计入 Key 惩罚**（C2、C3）。Firecrawl 抖动一次即全部 Key 不可用。检查点 B 与 C 必须显式挡住。
2. **job 粘连失效**（C3）。表现为 crawl 提交成功但永远查不到结果，且难以归因。
3. **响应 URL 未重写**（C3）。SDK 绕过代理直连上游，报 401，用户会以为是代理认证坏了。
4. **上游 Key 明文泄漏**（C1 日志、C5 API 响应）。上线前全局 grep 一遍，确认结构体上的 `api_key` 字段带 `json:"-"`。
5. **SPA 兜底吃掉 API 的 404**（C5）。客户端拿到 200 + HTML，表现为 SDK 解析异常。

## 完成定义

以下全部满足，父任务方可归档：

- [ ] 七个子任务全部完成并各自验收通过。
- [ ] 父任务 `prd.md` 的 AC1–AC14 逐条确认。
- [ ] `go test -race ./...` 全绿，`go vet ./...` 无输出。
- [ ] 按 README 在干净环境实测跑通一次完整流程。
- [ ] 已知限制（请求体超限不转移、不支持多副本）写入 README。
