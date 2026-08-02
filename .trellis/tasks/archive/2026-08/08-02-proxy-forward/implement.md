# C3 实施计划 — 代理转发与故障转移

## 前置

C1、C2 完成。`keypool.Pool` 与 `store.JobRouteRepo` 可用。

## 步骤

1. **假上游测试工具**
   先写测试基础设施：可编程的 `httptest.Server`，支持按调用序号 / Authorization / 路径返回预设响应，并记录每次收到的 Authorization。没有它，后面每一步都无法验证。
   验证：自测——设定「第一次返回 402，第二次返回 200」，断言两次调用的行为符合预期。

2. **`classify.go`**
   `shouldRetry(Outcome) bool`。独立于 `keypool.classify`。
   验证：表驱动测试，明确断言 500 与网络错误为「重试」，且此处不涉及任何 Key 状态。

3. **`handler.go` — 单次转发**
   `forward()`：构造上游请求、剔除 hop-by-hop 头、替换 Authorization、发起调用。先不做重试。
   验证：假上游收到的路径、方法、查询串、body、非认证头与原请求一致；Authorization 为上游 Key。

4. **失败转移循环**
   接入 `Next` / `NextExcluding` / `Report`，实现重试与 503 / 502 兜底。
   验证：AC1（402 转移）、AC3（500 重试不惩罚）、AC6（503 结构）、502 次数耗尽、400 直接透传。

5. **`rewrite.go`**
   URL 重写与响应缓冲判定。
   验证：AC5；另加非 JSON 响应流式透传的逐字节比对测试。

6. **`jobroute.go`**
   正则匹配、映射写入、`stickyPath` 转发、过期清理。
   验证：AC4；映射过期后退化为轮询；命中映射时不调用 `Report()`（用 mock pool 断言）。

7. **请求体缓冲上限**
   `io.LimitReader` + `io.MultiReader` 的超限降级路径。
   验证：构造超限请求，断言仍能成功转发且未发生重试。

8. **日志与接线**
   结构化日志字段补齐；在 `main.go` 中把 handler 挂到路径前缀上。
   验证：`go test -race ./internal/proxy/...` 全绿；手工用假上游跑一遍确认日志字段完整且无明文 Key。

## 验证命令

```bash
go test -race ./internal/proxy/...
go test ./...
go vet ./...
```

## 风险点

按后果排序：

1. **把 5xx 计入 Key 惩罚**（AC3 失败）。步骤 2 与步骤 4 的测试必须显式覆盖。
2. **命中 job 映射的请求走了故障转移**（AC4 失败）。`stickyPath` 必须是独立函数，不复用主循环。
3. **响应 URL 用字符串替换**，误伤查询串中的 host 字面量。必须走 `url.Parse`。
4. **响应体解析用结构体**，导致 Firecrawl 新增字段被静默丢弃。必须用 `map[string]any`。
5. **忘记 drain 未使用的响应体**，导致连接无法复用、fd 泄漏。重试前必须 `io.Copy(io.Discard, resp.Body)` 再 `Close()`。
6. **hop-by-hop 头未剔除**，导致上游或客户端出现协议层错误。

## 回滚点

本任务集中在 `internal/proxy` 包内，对外只在 `main.go` 增加路由挂载。放弃时删包并还原 `main.go`。
