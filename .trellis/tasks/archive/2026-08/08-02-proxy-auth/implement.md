# C4 实施计划 — 下游认证与用量统计

## 前置

C1 完成（`store.ProxyKeyRepo`）。可与 C5 并行，但需在 C3 的集成测试前接线完毕。

## 步骤

1. **`Issue` 与哈希**
   实现明文生成、SHA-256、入库。
   验证：AC9——断言库中无明文、`key_hash` 与手工计算的 SHA-256 一致；两次调用明文不同。

2. **`Middleware`**
   解析 `Authorization`、常量时间比较、注入 context、401 分支。
   验证：AC7 的三种失败情形响应体一致；成功情形 context 中能取到 name。

3. **`Revoke`**
   验证：吊销后下一次请求即 401。

4. **计数与刷盘**
   验证：N 次成功请求后 `Flush()`，`request_count` 增量为 N；`last_used_at` 被更新。

5. **接线**
   在 `main.go` 中把 `Middleware` 包在 C3 的代理 handler 外层，仅覆盖 `PROXY_PATH_PREFIXES`；ticker 与关闭时的 `Flush()` 一并接入。
   验证：`/healthz` 与 `/api/admin/*` 不受影响，无需 token 即可访问（面板自身的认证由 C5 负责）。

## 验证命令

```bash
go test -race ./internal/auth/...
go test ./...
```

## 风险点

- **401 响应体因失败原因不同而不同**，泄露「这个 token 存在但被吊销了」。三个分支必须返回同一个响应体。
- **中间件挂载范围过宽**，把 `/healthz` 或静态资源也保护起来，导致 AC14 与面板无法访问。挂载时按前缀精确限定。
- **明文被记进日志**。`Issue` 的返回值绝不进入任何日志语句；code review 时全局搜一遍 `plaintext`。

## 回滚点

新增 `internal/auth/proxykey.go` 与 `main.go` 中的几行挂载。放弃时移除中间件包装即可，代理路径退回无认证状态。
