# C2 — Key 池与状态机

父任务：`08-02-firecrawl-key-proxy`。状态机与选择算法见父任务 `design.md` §5。

## Goal

实现上游 Key 的运行时池：从 DB 加载、在可用 Key 间轮询、依据上游状态码做状态转移、冷却到期自动恢复。这是整个代理「智能调度」的全部逻辑所在，代理转发层（C3）只负责调用它并把结果告诉它。

## Requirements

- **R2.1** `keypool.Pool` 启动时从 `UpstreamKeyRepo` 加载全部 Key 到内存。内存是运行时权威，DB 是持久化副本。
- **R2.2** `Next() (*Key, error)`：在候选集合中轮询返回下一个可用 Key。候选 = `enabled=1` 且状态为 `available`，或状态为 `cooling` 但 `cooldown_until` 已过（此时就地转回 `available`）。无候选返回 `ErrNoKeyAvailable`。
- **R2.3** `NextExcluding(tried []int64) (*Key, error)`：同上，但排除本次请求已尝试过的 Key。供 C3 做故障转移时使用，避免重复撞同一个。
- **R2.4** `Report(keyID int64, outcome Outcome)`：依据上游响应更新状态。
  - `402` → `exhausted`，记 `last_error`
  - `401` / `403` → `invalid`，记 `last_error`
  - `429` → `cooling`，`cooldown_until = now + Retry-After`；`Retry-After` 缺失或无法解析时用 `DEFAULT_COOLDOWN_SECONDS`
  - `408` / `5xx` → **不改变状态**，也不记 `last_error`（这是上游故障，不是 Key 的问题）
  - `2xx` 及其他 `4xx` → 不改变状态
- **R2.5** 状态变更同步写 DB；`request_count` 与 `last_used_at` 在内存累加，按 `10 秒` 间隔批量刷盘，进程退出前 `Flush()` 一次。
- **R2.6** `Reload()`：重新从 DB 加载。供面板增删改 Key 后调用，使变更立即生效而无需重启。
- **R2.7** `Snapshot()`：返回各 Key 的当前状态快照（含冷却剩余秒数）与按状态分类的计数。供面板展示与 C3 构造 503 错误体使用。
- **R2.8** `SetCredits(keyID, total, remaining)`：由额度刷新逻辑写入，仅用于展示，**不参与 `Next()` 的候选判断**。
- **R2.9** 所有公开方法并发安全。

## Acceptance Criteria

- [ ] 3 个可用 Key 连续调用 `Next()` 6 次，返回序列为 1,2,3,1,2,3——轮询均匀，无偏袒。
- [ ] `Report(id, 402)` 后该 Key 不再被 `Next()` 返回，且 DB 中 `state='exhausted'`。
- [ ] `Report(id, 429)` 带 `Retry-After: 30` 后，`Snapshot()` 中该 Key 冷却剩余约 30 秒；把时钟推进 31 秒后 `Next()` 重新返回它，且状态已回到 `available`。
- [ ] `Report(id, 500)` 与 `Report(id, 408)` 后，该 Key 状态仍为 `available`，`last_error` 未被写入。
- [ ] `Report(id, 429)` 无 `Retry-After` 头时，冷却时长等于配置的默认值。
- [ ] 全部 Key 被标记为 `exhausted` 后，`Next()` 返回 `ErrNoKeyAvailable`；`Snapshot()` 的计数为 `{exhausted: N}`。
- [ ] `NextExcluding([]int64{1,2})` 在 3 个可用 Key 时只可能返回 3；排除全部时返回 `ErrNoKeyAvailable`。
- [ ] 面板新增一个 Key 后调用 `Reload()`，`Next()` 可能返回新 Key（AC8 的下半段）。
- [ ] `enabled=0` 的 Key 永不被 `Next()` 返回，无论其 `state` 为何。
- [ ] 并发 100 goroutine 各调用 100 次 `Next()` + `Report()`，`go test -race` 无告警，总调用计数准确。
- [ ] 进程重启后，此前 `exhausted` 的 Key 仍为 `exhausted`（AC10 的上半段）。

## Out of Scope

- HTTP 转发与重试循环——属于 C3。本任务只回答「该用哪个 Key」和「这个结果说明 Key 怎么了」。
- 主动向上游查询额度——`SetCredits` 只是写入口，拉取动作在 C5。
- 加权、优先级、按剩余额度排序等策略。父任务已定为均匀轮询。

## Notes

冷却恢复采用**惰性判定**：在 `Next()` 中检查 `cooldown_until` 是否已过，不起后台定时任务。少一个 goroutine、少一处生命周期管理，行为等价。

时间必须通过一个可注入的时钟接口获取（`type Clock interface{ Now() time.Time }`），否则冷却与恢复无法在单元测试中确定性地验证——AC 中「把时钟推进 31 秒」依赖这一点。
