# Quality Guidelines — Backend

## 语言与注释

- 代码注释、提交信息、文档一律**简体中文**。
- 注释解释「为什么」而非「做了什么」。

## 测试策略

- 存储层：`t.TempDir()` 下开真实 SQLite 文件，不用 mock。每个测试独立建库。
- 代理层（C3 起）：`httptest` 起假 Firecrawl 上游，按用例返回指定状态码与
  响应体——不消耗真实额度，可在 CI 运行。
- 时间断言注意 unix 秒截断：`time.Now().Truncate(time.Second)` 后再比较。

### 必须显式覆盖的关键用例（防回归）

> **5xx / 网络错误绝不能计入 Key 惩罚。**「要不要换 Key 重试」和「要不要惩罚
> 这个 Key」是两个独立判断。表驱动测试必须断言 500/408 后 `Key 状态不变`。
> 写错后果：Firecrawl 抖动一次把所有 Key 依次打成不可用，表现为
> 「代理跑一段时间后突然全挂」。

## 进程生命周期

- 优雅关闭结构：`main()` 用 `signal.NotifyContext` 把信号转成 ctx；
  `run(ctx)` 只依赖 `ctx.Done()`。关闭顺序：HTTP Shutdown → flush 计数 → db.Close。
- 顺序不可颠倒：flush 在 db.Close 之后会写到已关闭连接。

## 环境坑（Windows 开发机）

- **MSYS/Git Bash 的 `kill -TERM`/`-INT` 无法向原生 Windows 进程投递可捕获信号**
  （SIGTERM → TerminateProcess 强制终止）。验证优雅关闭用集成测试取消 ctx，
  不要依赖手动 kill。
- **`go test -race` 在 Windows 需要 CGO 且本机无 C 编译器**。race 检查放到
  Docker `golang` 镜像里跑（`docker run --rm -v %cd%:/app -w /app golang:1.25 go test -race ./...`），
  或 CI。生产构建保持 `CGO_ENABLED=0`。
