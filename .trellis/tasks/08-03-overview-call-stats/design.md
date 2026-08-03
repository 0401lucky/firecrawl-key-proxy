# 总览页调用数据面板 — 技术设计

## 目标边界

在管理面板「总览」页新增「调用数据」区，MVP 内容（已与用户确认）：

1. 汇总卡：总调用数、今日调用、近 24 小时调用、成功率（2xx 占比）
2. 趋势图：近 24 小时逐小时（可切 7 天 / 30 天，日粒度聚合），纯 SVG 手绘，不引图表库
3. 按上游 Key 分布：各 Key 调用占比横向条形

不做：按下游 API Key 分布、错误码明细、耗时统计、网络错误计入、多副本。

## 数据模型

新增一张聚合桶表，不存请求明细（沿用项目「避免每请求一次 DB 写」的既有原则）：

```sql
-- 调用统计：按「小时桶 × 上游 Key × 状态类别」聚合
CREATE TABLE IF NOT EXISTS call_stats_buckets (
    hour            INTEGER NOT NULL,  -- unix 秒，对齐到小时起点
    upstream_key_id INTEGER NOT NULL REFERENCES upstream_keys(id) ON DELETE CASCADE,
    status_class    INTEGER NOT NULL,  -- 1=2xx 2=3xx 3=4xx 4=5xx
    calls           INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (hour, upstream_key_id, status_class)
);
CREATE INDEX IF NOT EXISTS idx_call_stats_hour ON call_stats_buckets(hour);
```

- 存储量级：31 天 ≈ 744 桶 × Key 数 × 4 类，几 K 行，可忽略。
- `ON DELETE CASCADE`：删上游 Key 自动清其统计桶，无孤儿数据。
- schema.sql 幂等执行（CREATE TABLE IF NOT EXISTS），老库启动自动建表，无迁移脚本。

## 记录语义（口径决策）

**记录点与现有 `RecordUsage` 完全一致**（`internal/proxy/handler.go`）：
- `forwardLoop`：`netErr == nil` 时（含故障转移的每次成功传输的上游尝试——与面板「调用数」口径统一，总调用 ≈ Σ request_count）
- `stickyPath`：job 查询路径同样计入（与 request_count 行为一致）

**网络错误不计入**（`netErr != nil` 时不调 RecordUsage 也不调 RecordCall）——与既有计数语义保持一致，v1 不展开，见「风险/遗留」。

状态类别按 `outcome.StatusCode` 映射：200-299→1，300-399→2，400-499→3，其余→4。

## 采集与落库

复用 `keypool.Pool` 既有的「内存缓冲 + 批量刷盘」模式（不新增 goroutine、不增加每请求 DB 写）：

- `Pool` 增加 `stats map[string]int64`（key = `"{hour}|{keyID}|{statusClass}"`，value = 次数）。
- 新方法 `RecordCall(keyID int64, statusCode int)`：锁内算 `hour = truncate(now, 1h)` 与 statusClass，累加 map。
- `Flush()` 扩展：与 usage 同一次锁内取走、锁外 DB 写。upsert：
  ```sql
  INSERT INTO call_stats_buckets (hour, upstream_key_id, status_class, calls)
  VALUES (?, ?, ?, ?)
  ON CONFLICT(hour, upstream_key_id, status_class)
  DO UPDATE SET calls = calls + excluded.calls
  ```
  （modernc.org/sqlite 的 SQLite ≥ 3.24，支持 upsert。）
- `Reload()` 已先调 `Flush()`，usage 与 stats 一起保住，无需额外改动。

## 查询 API

`GET /api/admin/stats?window=24h|7d|30d`（默认 24h，SessionAuth 保护，注册在 `internal/admin/server.go` Router 的 protected mux）：

```json
{
  "window": "24h",
  "total_calls": 123,
  "success_rate": 0.95,
  "series": [
    { "ts": 1722600000, "calls": 10, "errors": 1 }
  ],
  "per_key": [
    { "key_id": 1, "calls": 80, "share": 0.65 }
  ]
}
```

- **series 永远按小时返回**（24h=24 点，30d=720 点，JSON 约 30KB 可接受）；粒度聚合（7d/30d 转日）与「今日调用」（浏览器本地时区，避免容器 UTC 偏差 8 小时的问题）都在前端做。
- `success_rate` = 窗口内 status_class=1 的桶 / 总桶，后端算（与时区无关）。
- `per_key` 只含 `key_id + calls + share`，名称/掩码由前端从已轮询的 `upstreamKeys` 列表 join（不重复传输）。
- 数据源：store 仓储直接读 DB（刷盘延迟 ≤10s，图表无需秒级实时）。
- 仓储方法（`internal/store/call_stats.go`）：
  - `Increment(rows []CallStat)`（Flush 用，单条 Exec 或事务批量）
  - `QueryWindow(startHour int64) ([]CallStatRow, error)`（GROUP BY hour, upstream_key_id）
  - `PerKey(startHour int64) ([]KeyCallTotal, error)`（GROUP BY upstream_key_id）
  - `DeleteBefore(cutoffHour int64) (int64, error)`（保留清理）

## 保留与清理

- 保留 31 天。启动时 + 每小时清一次过期桶（与 job 清理同模式，main 里加一个小 goroutine；硬编码 31 天，暂不开放配置）。

## 前端

`web/src/views/OverviewView.vue` 增加「调用数据」区块（`load()` 里并入 `api.stats(window)`）：

- 窗口切换：24h / 7d / 30d（本地 ref + 切换时重新拉取，轮询沿用 5s）
- 汇总卡 4 张：窗口总调用、今日调用（本地时区由小时 series 求和）、近 24h 调用、成功率
- 趋势图：手绘 SVG 柱状图组件（新组件 `CallTrendChart.vue`），24h 逐小时、7d/30d 聚合为日；每根柱分「成功/错误」两段（series 里有 errors）
- 按 Key 分布：横向条形 + 百分比，名称/掩码 join `keys`（已轮询）
- 类型：`web/src/api/types.ts` 加 `CallStats` / `CallSeriesPoint` / `PerKeyCall`；`client.ts` 加 `stats(window)`

## 兼容 / 迁移 / 部署 / 回滚

- schema 幂等，无数据迁移；上线后统计从零积累（不回填历史 request_count）。
- 部署：`git push` → 服务器 `git pull` → `docker compose build proxy && docker compose up -d proxy`（前端产物在镜像内重建，无需单独步骤）。
- 回滚：切回上一镜像 tag 或 `git revert`。

## 测试计划

- store：`call_stats_buckets` upsert 幂等（重复 Flush 不重复累加）、`QueryWindow`/`PerKey`/`DeleteBefore`、级联删除。
- keypool：`RecordCall` 后 `Flush` 落库正确、跨小时桶归属、并发累加（现有 fake clock + 临时库模式）。
- admin：`/api/admin/stats` 端点用假数据断言窗口过滤与响应形状（现有 admin_test 模式）。
- proxy：handler 在 RecordUsage 点位同步调 RecordCall 的集成断言（现有假上游）。
- 前端：`npm run build` 类型检查；手动验证轮询与窗口切换。
- 注意：`go test -race` 需 gcc（AGENTS.md 已知限制），开发机无则跳过。

## 风险 / 遗留

- 网络错误不计入统计（与 request_count 语义一致）；未来要「上游故障率」可再加 class=0 桶。
- 统计延迟 ≤10s（刷盘间隔）；图表无需秒级实时，接受。
- 窗口数据只在服务运行期间积累；多副本不支持（与既有限制一致）。
