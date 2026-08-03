# 总览页调用数据面板 — 执行计划

## 实施清单（顺序执行）

1. **schema**：`internal/store/schema.sql` 追加 `call_stats_buckets` 表 + hour 索引。
2. **store 仓储**：新建 `internal/store/call_stats.go`——`Increment`（upsert 批量）、`QueryWindow`、`PerKey`、`DeleteBefore`；`internal/store/db.go` 的 `NewStore`/`Store` 挂上新仓储。
3. **keypool 采集**：`internal/keypool/pool.go`——`stats map` + `RecordCall(keyID, statusCode)`（算小时桶 + statusClass），`Flush()` 扩展为 usage + stats 一起刷盘；`store.CallStats` 新仓储注入 `Pool`（构造参数或字段）。
4. **proxy 记录点**：`internal/proxy/handler.go`——`forwardLoop` 的 `netErr == nil` 分支与 `stickyPath` 处调用 `RecordCall(key.ID, statusCode)`（与 RecordUsage 同位）。
5. **main 装配**：`cmd/server/main.go`——`Pool` 构造传 stats 仓储；统计保留清理 goroutine（启动一次 + 每小时，`DeleteBefore(now-31d)`）。
6. **admin API**：新建 `internal/admin/stats.go`——`handleStats`（window 参数校验、QueryWindow/PerKey 聚合、success_rate 计算）；`internal/admin/server.go` 的 protected mux 注册 `GET /api/admin/stats`。
7. **前端类型与 client**：`web/src/api/types.ts` 加 `CallStats` 等；`web/src/api/client.ts` 加 `stats(window)`。
8. **前端图表组件**：`web/src/components/CallTrendChart.vue`（纯 SVG 柱状图，成功/错误两段）。
9. **前端总览页**：`web/src/views/OverviewView.vue` 加「调用数据」区块（窗口切换、4 张汇总卡、趋势图、按 Key 分布）。
10. **测试**：store upsert/查询/清理/级联；keypool RecordCall+Flush；admin stats 端点；proxy 记录点集成断言。
11. **验证**：`CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...`；`cd web && npm run build`。
12. **提交 + 部署**：commit → push → 服务器 `git pull` → `docker compose build proxy && docker compose up -d proxy` → 健康检查。

## 验证命令

```bash
CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...
cd web && npm ci && npm run build
# 部署后
docker compose ps  # proxy healthy
curl -s http://127.0.0.1:8081/api/admin/stats?window=24h  # 需登录，至少确认非 404
```

## 风险文件 / 回滚点

- `internal/keypool/pool.go`（并发锁内新增 map，注意 Flush/Reload 一致性）——每步后跑 `go test ./internal/keypool/`
- `internal/store/schema.sql`（表结构，幂等）
- `cmd/server/main.go`（装配）
- 回滚：`git revert` + 重新 build/up；统计表存在但不写入时无副作用（空表）。

## start 前复查

- [ ] prd/design/implement 已就位，无阻塞开放问题
- [ ] 用户已批准最终规划摘要
- [ ] schema 幂等、记录点与 RecordUsage 同位、Flush 双 map 一致性确认
