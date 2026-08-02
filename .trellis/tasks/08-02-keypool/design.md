# C2 设计 — Key 池与状态机

状态机图与转移规则见父任务 `design.md` §5。本文记录实现结构。

## 类型

```go
package keypool

type Outcome struct {
    StatusCode int
    RetryAfter time.Duration // 从 Retry-After 头解析，0 表示缺失
    Err        error          // 网络层错误（连接失败/超时），非 HTTP 响应
}

type Pool struct {
    mu     sync.Mutex
    keys   []*entry      // 按 id 升序，保证轮询顺序稳定
    cursor int
    repo   store.UpstreamKeyRepo
    clock  Clock
    cfg    Config
    usage  map[int64]int64 // 待刷盘的调用增量
}
```

`entry` 包裹 `store.UpstreamKey` 并附加内存态字段（待刷盘的计数、最后使用时间）。

## 为什么单个互斥锁就够

Key 数量是几十级别，`Next()` 是一次 O(n) 线性扫描，n 小到分支预测都能吃下。用读写锁反而更慢——`Next()` 本身就要改 `cursor`，是写操作，读写锁拿不到好处。用原子计数器分片则会让状态转移的一致性变复杂。

## 网络层错误的处置

`Outcome.Err != nil`（连接被拒、超时、DNS 失败）等同于 5xx：**不改变 Key 状态**。理由与 5xx 相同——连不上 Firecrawl 不是这个 Key 的错。C3 应把它计入重试次数，但不计入 Key 惩罚。

这一点容易写错：直觉上「用这个 Key 失败了就换一个」，但那会在网络抖动时把所有 Key 依次标记一遍。

## 用量刷盘

```go
func (p *Pool) Flush() error   // 把 usage map 批量提交给 repo，然后清空
```

由一个 `time.Ticker`（间隔取配置）驱动，`main.go` 的优雅关闭路径中再显式调用一次。刷盘时先在锁内取走并置空 map，再在锁外执行 DB 写——不要持锁做 I/O。

## 时钟注入

```go
type Clock interface{ Now() time.Time }
type realClock struct{}
type fakeClock struct{ mu sync.Mutex; t time.Time }  // 测试用，支持 Advance()
```

生产用 `realClock`，测试用 `fakeClock`。这是 AC 中冷却时长与自动恢复能被确定性验证的前提；用 `time.Sleep` 写这类测试会既慢又不稳。

## Reload 的并发安全

`Reload()` 在锁外读 DB，拿到新列表后在锁内整体替换 `keys`，并把 `cursor` 归零。替换前需把当前的 `usage` 增量刷盘，否则会丢失。已在途的请求持有的是旧 `*entry` 指针，其 `Report()` 会落到已被替换的对象上——因此 `Report()` 按 `keyID` 而非指针查找当前列表，找不到就丢弃（该 Key 已被删除）。
