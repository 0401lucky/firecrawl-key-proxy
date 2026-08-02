# Error Handling — Backend

## 配置错误：聚合报错

必填项/非法值一次性列出全部问题，不报第一个就停（`errors.Join`）：

```go
var errs []error
if cfg.PublicBaseURL == "" {
    errs = append(errs, errors.New("缺少必填环境变量 PUBLIC_BASE_URL（对外地址，用于响应 URL 重写）"))
}
// ...
if len(errs) > 0 {
    return nil, errors.Join(errs...)
}
```

配置错误直接终止启动（非零码），绝不静默降级为不安全的默认值。

## 存储错误

- 查不到 → 返回 `sql.ErrNoRows`，上层 `errors.Is(err, sql.ErrNoRows)` 判断。
- 仓储方法把错误用中文 `fmt.Errorf("操作 X 失败: %w", err)` 包装，保留根因链。
- 批量写用事务（`Begin` + `defer Rollback` + 显式 `Commit`）。

## 错误响应契约（HTTP 层，C3/C5）

代理自身产生的错误统一 JSON 结构，`detail` 携带状态计数让调用方区分
「额度用完了」vs「Firecrawl 挂了」：

```json
{ "error": "no_upstream_key_available", "message": "所有上游 Key 均不可用", "detail": {"exhausted":3,"cooling":1,"invalid":0} }
```

| 场景 | 状态码 | error |
|---|---|---|
| 缺失或无效的代理 Key | 401 | `invalid_proxy_key` |
| 所有上游 Key 均不可用 | 503 | `no_upstream_key_available` |
| 转移次数耗尽仍失败 | 502 | `upstream_failover_exhausted` |
| 路径不在转发前缀内 | 404 | `not_found` |
