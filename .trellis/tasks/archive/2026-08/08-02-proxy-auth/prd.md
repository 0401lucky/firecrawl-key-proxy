# C4 — 下游认证与用量统计

父任务：`08-02-firecrawl-key-proxy`。存储模型见父任务 `design.md` §3，认证隔离见 §4。

## Goal

实现代理对外签发的 API Key：生成、校验、吊销、调用计数。调用方拿到的是这个 Key，而不是任何一个真实的 Firecrawl Key——这是「像用单个 Key 一样调用」的入口，也是上游 Key 不外泄的前提。

## Requirements

- **R4.1 生成**：`Issue(name string) (plaintext string, record ProxyKey, err error)`。明文格式 `fcp_` + 32 字节 `crypto/rand` 的 base64url 编码。入库只存 `sha256(plaintext)` 的十六进制与前 12 位前缀。明文只通过返回值交给调用方一次，不写日志、不入库。
- **R4.2 校验**：中间件从 `Authorization: Bearer <token>` 取出明文，算 SHA-256 后查 `proxy_keys`。命中且 `revoked=0` 则放行，并把该 Key 的 id 与 name 放入请求上下文；否则返回 401 + `{"error":"invalid_proxy_key"}`。
- **R4.3** 缺失 `Authorization` 头、格式不是 `Bearer <token>`、token 为空——三种情况都返回 401，且响应体一致，不泄露失败原因的差异。
- **R4.4 吊销**：`Revoke(id)` 置 `revoked=1`。吊销后的 Key 立即失效，不依赖缓存过期。
- **R4.5 计数**：每次通过校验的请求为对应的代理 Key 累加 `request_count` 并更新 `last_used_at`。与 C2 一致，内存累加、按间隔批量刷盘、退出前 flush。
- **R4.6** 中间件只挂在代理路径（`PROXY_PATH_PREFIXES`）上。`/api/admin/*`、`/healthz` 与静态资源不经过它。

## Acceptance Criteria

- [ ] **AC7**：不带 `Authorization` 头调用 `/v2/scrape` 返回 401；带一个不存在的 token 返回 401；带已吊销的 Key 返回 401。三者响应体一致。
- [ ] **AC9**：`Issue()` 返回的明文在数据库中查不到；`proxy_keys.key_hash` 等于该明文的 SHA-256 十六进制。
- [ ] 有效 Key 调用后，`request_count` 在刷盘后正确 +1，`last_used_at` 被更新。
- [ ] `Revoke()` 之后的下一个请求即返回 401，无需重启或等待。
- [ ] 连续两次 `Issue()` 产生的明文不同，且长度、前缀格式稳定（`fcp_` 开头）。
- [ ] 代理 Key 的明文不出现在任何日志输出中——日志只记 `name`。
- [ ] **AC13 的一半**：面板 session cookie 不能通过本中间件的校验（它不读 cookie）。
- [ ] 并发校验 + 计数在 `go test -race` 下无告警。

## Out of Scope

- 面板侧的管理员登录与 session——属于 C5。
- 创建/吊销的 HTTP 接口——属于 C5，本任务只提供被调用的函数。
- 按 Key 限流、按 Key 分配额度上限。父任务已列为 Out of Scope。

## Notes

用 SHA-256 而非 bcrypt/argon2 是有意的：token 是 32 字节的高熵随机串，不存在字典攻击面，而每个代理请求都要校验一次，慢哈希会直接变成吞吐瓶颈。这个理由需要写进代码注释，否则后续容易被「安全最佳实践」误改。

比较哈希时用 `crypto/subtle.ConstantTimeCompare`。虽然此处的时序侧信道在高熵 token 面前实际风险极低，但成本为零。
