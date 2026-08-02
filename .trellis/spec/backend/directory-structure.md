# Directory Structure — Backend

## 项目布局

单仓库模式，Go 后端 + Vue 前端（前端源码在 `web/`，构建产物嵌入二进制）。

```
cmd/server/main.go            # 服务入口：组装依赖、启动 HTTP、优雅关闭
internal/
  config/config.go            # 环境变量加载与校验（唯一样式来源）
  store/                      # SQLite：schema、连接、仓储
    db.go  schema.sql  models.go  convert.go
    upstream_keys.go  proxy_keys.go  job_routes.go  sessions.go
  keypool/                    # 上游 Key 池：状态机 + 轮询选择（C2）
  proxy/                      # 反向代理：转发、故障转移、URL 重写、job 粘连（C3）
  auth/                       # 下游代理 Key + 面板 session（C4）
  admin/                      # 面板 JSON API（C5）
  firecrawl/                  # 调用上游 /team/credit-usage 拉余额（C5）
  webui/                      # go:embed 前端产物
    embed.go  dist/           # dist 由 Vite 构建产出
web/                          # Vue 3 前端源码
```

## 关键约束

- **`go:embed` 只能嵌入包目录树内文件**：Vite 的 `build.outDir` 必须指向
  `internal/webui/dist`，不能放仓库根的 `web/dist`。见 `web/vite.config.ts`。
- **第三方运行时依赖只允许 `modernc.org/sqlite`**。不引入 Web 框架、ORM、DI 容器。
  前端无 Pinia 等状态库。
- 模块名 `firecrawl-proxy`。

## 分层边界

- `internal/config`：无依赖（纯环境变量读取）。
- `internal/store`：只依赖 `database/sql`，仓储方法不做业务判断。
- `internal/keypool` → 依赖 store（领域类型复用）。
- `internal/proxy` → 依赖 keypool（选 Key/报告状态）。
- `cmd/server/main.go`：组装依赖，不写业务逻辑。
