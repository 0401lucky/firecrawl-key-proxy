# C2 实施计划 — Key 池与状态机

## 前置

C1 完成：`store.UpstreamKeyRepo` 与 `store.KeyState` 可用。

## 步骤

1. **`internal/keypool/state.go`**
   定义 `Outcome`、`Clock`、`fakeClock`，以及纯函数 `classify(Outcome) transition`——输入状态码，输出「变成什么状态 / 冷却多久 / 不变」。把状态判断从 `Pool` 里抽出来做成无副作用的纯函数，是让它可被表驱动测试穷举的关键。
   验证：`classify` 的表驱动测试，覆盖 200/400/401/402/403/408/429(带头)/429(无头)/500/502/网络错误共 10 个用例。

2. **`internal/keypool/pool.go` — 加载与选择**
   `New(repo, clock, cfg)` 加载 Key；实现 `Next()` 与 `NextExcluding()`。
   验证：轮询均匀性测试、`enabled=0` 跳过测试、无候选返回 `ErrNoKeyAvailable` 测试。

3. **状态转移**
   实现 `Report()`，调用 `classify` 后更新内存并同步写 DB。
   验证：402/401/429/500 各一个测试，断言内存状态、DB 状态、`last_error` 三者一致或按预期不变。

4. **冷却恢复**
   在 `Next()` 的候选判定中加入 `cooling` 且已过期的惰性恢复。
   验证：用 `fakeClock` 推进时间，断言恢复前不返回、恢复后返回且状态变回 `available`。

5. **用量累加与刷盘**
   实现内存累加、`Flush()`、ticker 驱动，接入 `main.go` 的关闭路径。
   验证：累加 N 次后 `Flush()`，断言 DB 中 `request_count` 增量正确；重复 `Flush()` 不重复累加。

6. **`Reload()` 与 `Snapshot()`**
   验证：新增一条 DB 记录后 `Reload()`，`Next()` 可返回它；`Snapshot()` 的状态计数与冷却剩余秒数正确。

7. **并发测试**
   验证：`go test -race ./internal/keypool/...` 全绿。

## 验证命令

```bash
go test -race ./internal/keypool/...
go vet ./internal/keypool/...
```

## 风险点

- **把 5xx / 网络错误计入 Key 惩罚**。这是本任务最容易写错的地方，也是后果最严重的——Firecrawl 抖动一次就会把所有 Key 打成不可用。步骤 1 的表驱动测试必须明确覆盖这两类，且断言「状态不变」。
- **`Reload()` 前忘记刷盘**，导致调用计数静默丢失。
- **持锁做 DB 写**。`Report()` 中的同步写 DB 若在锁内执行，高并发下会把 `Next()` 一起卡住。先在锁内改内存并取出待写数据，再在锁外落库。

## 回滚点

本任务只新增 `internal/keypool` 包，不修改 C1 的既有文件（除 `main.go` 中接入 ticker 与 `Flush()` 的几行）。放弃时删包并还原 `main.go` 即可。
