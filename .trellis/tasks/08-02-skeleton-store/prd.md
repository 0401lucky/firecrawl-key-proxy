# C1 — 骨架与存储层

父任务：`08-02-firecrawl-key-proxy`。架构与数据模型见父任务 `design.md` §2、§3、§8。

## Goal

搭出可编译运行的 Go 项目骨架，完成配置加载、SQLite 建表与仓储层、结构化日志。此后所有子任务都在这个地基上叠加，因此本任务定下的接口一旦成型，后续改动成本高。

## Requirements

- **R1.1** 建立 `go.mod`（module 名 `firecrawl-proxy`，Go 1.22+）与父任务 `design.md` §2 描述的目录结构。运行时依赖只允许 `modernc.org/sqlite`。
- **R1.2** `internal/config`：从环境变量加载父任务 `design.md` §8 的全部配置项并应用默认值。`PUBLIC_BASE_URL` 与 `ADMIN_PASSWORD` 为必填，缺失时打印明确错误并以非零码退出，不得静默降级为不安全的默认值。
- **R1.3** `internal/store`：打开 SQLite（`modernc.org/sqlite`，`CGO_ENABLED=0` 下可构建），启动时幂等执行 `schema.sql` 建表，四张表见父任务 `design.md` §3。
- **R1.4** 为四张表各提供一个仓储类型，方法只做数据存取，不含业务判断：
  - `UpstreamKeyRepo`：List / Create / Update（name、enabled、state、cooldown_until、credits_*、last_error）/ Delete / IncrementUsage（批量）
  - `ProxyKeyRepo`：List / Create / FindByHash / Revoke / IncrementUsage（批量）
  - `JobRouteRepo`：Upsert / Get / DeleteExpired
  - `SessionRepo`：Create / Get / Delete / DeleteExpired
- **R1.5** `internal/logging`：基于 `log/slog` 的 JSON handler，级别由 `LOG_LEVEL` 控制。提供把上游 Key 转成 `fc-****abcd` 的脱敏函数，全项目复用同一份实现。
- **R1.6** `cmd/server/main.go`：加载配置 → 打开 DB → 建表 → 启动 HTTP 服务（此阶段只需 `/healthz` 返回 200）→ 监听 SIGINT/SIGTERM 优雅关闭（关闭前 flush、关闭 DB）。
- **R1.7** 时间统一以 unix 秒（`int64`）入库；仓储层对上层暴露 `time.Time`，转换只在仓储内部发生，避免单位在各层间漂移。

## Acceptance Criteria

- [ ] `CGO_ENABLED=0 go build ./...` 通过并产出可执行文件。
- [ ] 不设 `PUBLIC_BASE_URL` 时启动失败，stderr 指明缺失的具体变量名；补上后启动成功。
- [ ] 首次启动自动创建 SQLite 文件与四张表；再次启动不报错、不重复建表、已有数据不丢失。
- [ ] `curl localhost:8080/healthz` 返回 200。
- [ ] 每个仓储的增删改查各有一个使用临时 DB 文件的单元测试，`go test ./internal/store/...` 通过。
- [ ] 脱敏函数对 `fc-1234567890abcd` 返回 `fc-****abcd`；对短于 4 字符的输入不 panic，有测试覆盖。
- [ ] 发送 SIGTERM 后进程在 5 秒内退出，无 `database is locked` 类错误输出。

## Out of Scope

- Key 选择逻辑、代理转发、认证、面板——分别属于 C2–C6。
- 数据库迁移框架。当前只需启动时幂等建表；表结构在项目定型前直接改 `schema.sql`。

## Notes

`IncrementUsage` 在本任务只实现存储侧。调用时机与内存缓冲策略由 C2 决定，因此签名要按「一次提交多个 id 的增量」设计（如 `IncrementUsage(map[int64]int64) error`），避免 C2 落地时被迫返工改签名。
