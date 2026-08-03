# 总览页新增调用数据面板

## Goal

在管理面板「总览」页新增「调用数据」区，展示代理请求统计（调用量、趋势、成功率、按上游 Key 分布），管理员不离开总览页即可看清代理使用情况。

## Background（仓库事实）

- 总览页（`web/src/views/OverviewView.vue`）现有：额度池卡片、状态计数条、上游 Key 卡片（含累计调用数），无图表。
- `/api/admin/overview`（`internal/admin/overview.go`）只返回额度与状态摘要。
- 无按时间维度的请求记录：schema 仅 `upstream_keys.request_count` / `proxy_keys.request_count` 累计值。
- 记录点（`internal/proxy/handler.go`）：`forwardLoop`（`netErr==nil` 时 `RecordUsage`）与 `stickyPath`（无条件 `RecordUsage`）；计数为内存累加 + 10 秒批量刷盘（`keypool.Pool`，刻意避免每请求一次 DB 写）。
- 前端 5 秒轮询（`usePolling`），无图表库（纯 Tailwind + Vue 3）。
- 部署：VPS 15.235.208.226，docker compose，更新 = git pull + build/up proxy。

## Requirements

- R1 汇总卡：窗口内总调用数、今日调用（浏览器本地时区）、近 24h 调用、成功率（2xx 占比）。
- R2 趋势图：24h 逐小时；7d / 30d 聚合为日粒度；纯 SVG 手绘，不引入图表库依赖。
- R3 按上游 Key 分布：各 Key 调用数与占比条形（名称/掩码沿用既有列表数据）。
- R4 窗口切换：24h / 7d / 30d，切换后重新拉取，仍按 5s 轮询。
- R5 采集：新增小时桶聚合表（`call_stats_buckets`：hour × upstream_key_id × status_class，calls 计数），内存累加 + 批量 upsert 刷盘，**不引入每请求一次 DB 写**。
- R6 记录口径：与现有 `request_count` 完全一致——`forwardLoop` 的 `netErr==nil` 分支（含故障转移的每次上游尝试）与 `stickyPath`；网络错误不计入。
- R7 保留：小时桶保留 31 天，启动 + 每小时清理过期桶。
- R8 新增 `GET /api/admin/stats?window=24h|7d|30d`（SessionAuth 保护），series 按小时返回，粒度聚合在前端做。

## Acceptance Criteria

- [ ] AC1 总览页出现「调用数据」区：4 张汇总卡、趋势图、按 Key 分布、窗口切换（24h/7d/30d）。
- [ ] AC2 趋势图在 24h 窗口显示 24 个数据点（按小时），7d/30d 按日聚合；纯 SVG 无外部图表库。
- [ ] AC3 成功率 = 窗口内 2xx 请求 / 总请求，随轮询刷新。
- [ ] AC4 「今日调用」按浏览器本地时区计算（容器 UTC 不导致 8 小时偏差）。
- [ ] AC5 记录点与 `RecordUsage` 同位：普通路径 + job 查询路径均计入；网络错误不计入。
- [ ] AC6 刷盘后总调用数 ≈ Σ `request_count`（同一批请求，量级一致）。
- [ ] AC7 删上游 Key 后其统计桶级联清除，stats 不报错。
- [ ] AC8 超过 31 天的桶被清理（`DeleteBefore` 生效）。
- [ ] AC9 `go build`、`go vet`、`go test ./...`、`npm run build` 全绿。
- [ ] AC10 部署后面板可见新区块，5s 轮询正常，无后端报错日志。

## Out of Scope

- 按下游 API Key 分布、请求明细/错误码明细、耗时统计（p50/p95）
- 网络错误计入统计（保留语义与 request_count 一致；如需上游故障率另开）
- 历史数据回填（上线后从零积累）
- 统计配置项（31 天保留硬编码）、多副本支持

## Key Decisions

- 口径：统计 = 每次成功传输的上游尝试（与 request_count 一致），job 查询路径计入。
- 存储：小时桶聚合而非明细；内存缓冲 + 10s 批量 upsert（复用 Pool.Flush），零每请求 DB 写。
- series 永远按小时返回；日粒度聚合与「今日」边界都在前端（浏览器时区）。
- status_class：1=2xx 2=3xx 3=4xx 4=5xx；网络错误 v1 不记录。

## 验收口径备注

「总调用数 ≈ Σ request_count」：两者记录点相同，但 stats 有 10s 刷盘延迟且保留 31 天，长窗口对比应看同一时间点落库值；AC6 以「同一批请求后 Flush 再比对」为准。
