# Database Guidelines — SQLite

## 驱动与连接串

驱动固定为 `modernc.org/sqlite`（纯 Go，`CGO_ENABLED=0` 可构建）。连接串：

```go
dsn := fmt.Sprintf(
    "file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)",
    path,
)
```

- WAL：读写互不阻塞，代理路径的读不会被面板的写卡住。
- busy_timeout：瞬时并发写排队，不直接抛 `database is locked`。
- foreign_keys：`job_routes` 的 `ON DELETE CASCADE` 才真正生效。

**不要** `db.SetMaxOpenConns(1)`——WAL 下多读一写安全，限制单连接牺牲读并发。
写操作靠 busy_timeout 排队。

> **Warning**：modernc 驱动与 mattn/go-sqlite3 的 pragma 语法不同。若 `_pragma=`
> 写法报错，改为打开后显式 `PRAGMA journal_mode=WAL;`。不要因此换回 CGO 驱动，
> 那会破坏 Docker 精简镜像目标。

## 时间表示

**规则：unix 秒（int64）入库，上层只见 `time.Time`。**

- 转换只发生在仓储内部（`internal/store/convert.go` 集中实现）。
- 可空时间用 `*time.Time`；入库转 `nil`/`int64`。
- 亚秒精度会被截断，测试比较基准需 `time.Now().Truncate(time.Second)`。

## 领域类型

`KeyState` 具名字符串类型定义在 `internal/store`（不是 keypool），全项目唯一：

```go
type KeyState string
const (
    StateAvailable KeyState = "available"
    StateCooling   KeyState = "cooling"
    StateExhausted KeyState = "exhausted"
    StateInvalid   KeyState = "invalid"
)
```

## 仓储契约

- 方法只做数据存取，不做业务判断（不含 if 分支判断状态语义）。
- 查不到记录返回 `sql.ErrNoRows`（不包装成自定义错误），上层用 `errors.Is` 判断。
- 高频计数写入必须走批量形式，避免每请求一次 DB 写：

```go
// 一次提交多个 id 的增量（id → delta）。C2 的内存缓冲策略固定间隔刷盘。
func (r *UpstreamKeyRepo) IncrementUsage(usage map[int64]int64) error
```

- 面板增删 Key：写 DB 后触发内存池重载（keypool 是运行时权威，DB 是持久化副本）。
