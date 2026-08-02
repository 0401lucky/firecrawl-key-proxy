# C1 设计 — 骨架与存储层

架构全貌见父任务 `design.md`。本文只记录本子任务范围内的实现决策。

## 边界

本任务交付「地基」：配置、存储、日志、进程生命周期。不含任何业务判断——判断 Key 该不该用是 C2 的事，决定请求怎么走是 C3 的事。仓储方法只做 SQL 读写，不含 if 分支。

## 契约

各仓储对上层暴露领域结构体（`UpstreamKey`、`ProxyKey`、`JobRoute`、`Session`），字段用 Go 原生类型（`time.Time`、`*time.Time` 表示可空时间）。unix 秒的转换封在仓储内部。

`state` 在 Go 侧定义为具名字符串类型：

```go
type KeyState string
const (
    StateAvailable KeyState = "available"
    StateCooling   KeyState = "cooling"
    StateExhausted KeyState = "exhausted"
    StateInvalid   KeyState = "invalid"
)
```

类型放在 `internal/store`，C2 直接复用，不重复定义一套。

## SQLite 连接配置

`modernc.org/sqlite` 是纯 Go 实现，但 SQLite 本身的写并发限制仍在。连接串需带：

```
file:{path}?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)
```

- WAL：读写不互相阻塞，代理路径上的读（查 job_routes、查 proxy_keys）不会被面板的写卡住。
- busy_timeout：避免瞬时并发写直接返回 `database is locked`。
- foreign_keys：让 `job_routes` 的 `ON DELETE CASCADE` 真正生效，删除上游 Key 时自动清掉它的 job 映射。

`db.SetMaxOpenConns(1)` 不采用——WAL 下多读一写是安全的，限制单连接会白白牺牲读并发。写操作靠 busy_timeout 排队。

## 优雅关闭

`main.go` 中用 `signal.NotifyContext` 捕获信号，`http.Server.Shutdown(ctx)` 等待在途请求完成（超时 5 秒），随后依次：flush 用量计数（C2 接入后生效，本任务先留出 hook）、`db.Close()`。顺序不能颠倒，否则 flush 会写到已关闭的连接。

## 测试

仓储测试用 `t.TempDir()` 下的真实 SQLite 文件，不用 mock。每个测试独立建库，互不干扰。这类测试跑得足够快，且能真正验证 SQL 与约束。
