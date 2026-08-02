# C1 实施计划 — 骨架与存储层

## 前置

无。这是第一个执行的子任务。

## 步骤

1. **初始化模块与目录**
   `go mod init firecrawl-proxy`，创建 `design.md` §2 的目录骨架，`go get modernc.org/sqlite`。
   验证：`go build ./...` 通过（此时无实质代码）。

2. **`internal/config`**
   定义 `Config` 结构体，`Load()` 从环境变量读取并校验。必填项缺失返回错误列表而非只报第一个——一次告诉用户全部缺什么。
   验证：`go test ./internal/config/...`，覆盖默认值填充、必填缺失、数值项解析失败三种情况。

3. **`internal/store/schema.sql` + `db.go`**
   写入父任务 `design.md` §3 的四张表 DDL（用 `CREATE TABLE IF NOT EXISTS`）。`Open(path)` 按本任务 `design.md` 的连接串打开并执行 DDL。
   验证：临时目录下调用两次 `Open`，第二次不报错。

4. **四个仓储**
   逐个实现并配单测。顺序：`upstream_keys` → `proxy_keys` → `job_routes` → `sessions`。
   验证：`go test ./internal/store/...` 全绿。

5. **`internal/logging`**
   `New(level string) *slog.Logger` + `MaskKey(string) string`。
   验证：`MaskKey` 的表驱动测试，含空串、3 字符、正常长度三类输入。

6. **`cmd/server/main.go`**
   串起来：Load → Open → mux（仅 `/healthz`）→ ListenAndServe → 信号处理 → Shutdown → Close。
   验证：`CGO_ENABLED=0 go build -o bin/server ./cmd/server`；设好必填环境变量后运行，`curl /healthz` 得 200；Ctrl-C 后干净退出。

## 验证命令

```bash
CGO_ENABLED=0 go build ./...
go test ./...
go vet ./...
```

## 风险点

- **`modernc.org/sqlite` 的连接串 pragma 语法与 `mattn/go-sqlite3` 不同**。若 `_pragma=journal_mode(WAL)` 写法报错，改用打开后显式执行 `PRAGMA journal_mode=WAL;`。不要因此换回 CGO 驱动——那会破坏 C7 的镜像精简目标。
- **`IncrementUsage` 的签名**。按 C1 `prd.md` Notes 的批量形式定义，避免 C2 返工。

## 回滚点

本任务无既有代码可破坏。若中途放弃，删除新增文件即可。
