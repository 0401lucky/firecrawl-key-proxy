package store

import (
	"database/sql"
	"fmt"
)

// CallStat 是一条待刷盘的调用统计增量：小时桶 × 上游 Key × 状态类别。
// Hour 为 unix 秒且对齐到小时起点（由 keypool 计算），statusClass 取值
// 1=2xx 2=3xx 3=4xx 4=5xx（网络错误 v1 不记录）。
type CallStat struct {
	Hour          int64
	UpstreamKeyID int64
	StatusClass   int
	Calls         int64
}

// CallStatRow 是查询窗口内单个桶的聚合结果（calls 为该桶的总次数）。
type CallStatRow struct {
	Hour          int64
	UpstreamKeyID int64
	StatusClass   int
	Calls         int64
}

// KeyCallTotal 是某上游 Key 在窗口内的调用总量（按次数降序）。
type KeyCallTotal struct {
	UpstreamKeyID int64
	Calls         int64
}

// CallStatsRepo 是 call_stats_buckets 表的仓储。
// 写入走批量 upsert（内存缓冲固定间隔刷盘），读取只按窗口下界过滤。
type CallStatsRepo struct {
	db *sql.DB
}

// Increment 批量累加统计桶；桶已存在（同 hour+key+class）时叠加 calls。
// 高频写入路径：keypool 内存缓冲按固定间隔调用，避免每请求一次 DB 写。
func (r *CallStatsRepo) Increment(rows []CallStat) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("开启统计写入事务失败: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(
		`INSERT INTO call_stats_buckets (hour, upstream_key_id, status_class, calls)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(hour, upstream_key_id, status_class)
		 DO UPDATE SET calls = calls + excluded.calls`,
	)
	if err != nil {
		return fmt.Errorf("准备统计 upsert 语句失败: %w", err)
	}
	defer stmt.Close()

	for _, s := range rows {
		if s.Calls <= 0 {
			continue
		}
		if _, err := stmt.Exec(s.Hour, s.UpstreamKeyID, s.StatusClass, s.Calls); err != nil {
			return fmt.Errorf("写入统计桶失败: %w", err)
		}
	}
	return tx.Commit()
}

// QueryWindow 返回 hour >= startHour 的全部桶聚合（按小时、Key、状态类别分组求和）。
// 供 stats API 构造趋势 series 与成功率；上限由调用方按窗口推算。
func (r *CallStatsRepo) QueryWindow(startHour int64) ([]CallStatRow, error) {
	rows, err := r.db.Query(
		`SELECT hour, upstream_key_id, status_class, SUM(calls)
		 FROM call_stats_buckets
		 WHERE hour >= ?
		 GROUP BY hour, upstream_key_id, status_class
		 ORDER BY hour`,
		startHour,
	)
	if err != nil {
		return nil, fmt.Errorf("查询统计窗口失败: %w", err)
	}
	defer rows.Close()

	var out []CallStatRow
	for rows.Next() {
		var s CallStatRow
		if err := rows.Scan(&s.Hour, &s.UpstreamKeyID, &s.StatusClass, &s.Calls); err != nil {
			return nil, fmt.Errorf("读取统计行失败: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// PerKey 返回 hour >= startHour 内每个上游 Key 的调用总量，按次数降序。
// 供 stats API 的「按上游 Key 分布」使用；上游 Key 删除后其桶被级联清除，不会出现孤儿行。
func (r *CallStatsRepo) PerKey(startHour int64) ([]KeyCallTotal, error) {
	rows, err := r.db.Query(
		`SELECT upstream_key_id, SUM(calls) AS total
		 FROM call_stats_buckets
		 WHERE hour >= ?
		 GROUP BY upstream_key_id
		 ORDER BY total DESC`,
		startHour,
	)
	if err != nil {
		return nil, fmt.Errorf("查询按 Key 统计失败: %w", err)
	}
	defer rows.Close()

	var out []KeyCallTotal
	for rows.Next() {
		var k KeyCallTotal
		if err := rows.Scan(&k.UpstreamKeyID, &k.Calls); err != nil {
			return nil, fmt.Errorf("读取按 Key 统计行失败: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// DeleteBefore 删除 hour < cutoffHour 的所有桶（保留策略），返回删除条数。
func (r *CallStatsRepo) DeleteBefore(cutoffHour int64) (int64, error) {
	res, err := r.db.Exec("DELETE FROM call_stats_buckets WHERE hour < ?", cutoffHour)
	if err != nil {
		return 0, fmt.Errorf("清理过期统计桶失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("读取清理条数失败: %w", err)
	}
	return n, nil
}
