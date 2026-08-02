package store

import (
	"database/sql"
	"fmt"
	"time"
)

// JobRouteRepo 是 job_routes 表的仓储：异步任务 → 上游 Key 的粘连关系。
type JobRouteRepo struct {
	db *sql.DB
}

const jobRouteCols = `job_id, upstream_key_id, kind, created_at, expires_at`

func scanJobRoute(row interface{ Scan(...any) error }) (JobRoute, error) {
	var (
		jr        JobRoute
		createdAt int64
		expiresAt int64
	)
	err := row.Scan(&jr.JobID, &jr.UpstreamKeyID, &jr.Kind, &createdAt, &expiresAt)
	if err != nil {
		return JobRoute{}, err
	}
	jr.CreatedAt = time.Unix(createdAt, 0)
	jr.ExpiresAt = time.Unix(expiresAt, 0)
	return jr, nil
}

// Upsert 写入一条 job 映射；job_id 已存在时更新（覆盖上游 Key 与过期时间）。
func (r *JobRouteRepo) Upsert(jr *JobRoute) error {
	_, err := r.db.Exec(
		`INSERT INTO job_routes (job_id, upstream_key_id, kind, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(job_id) DO UPDATE SET
		   upstream_key_id = excluded.upstream_key_id,
		   kind = excluded.kind,
		   created_at = excluded.created_at,
		   expires_at = excluded.expires_at`,
		jr.JobID, jr.UpstreamKeyID, jr.Kind,
		jr.CreatedAt.Unix(), jr.ExpiresAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("写入 job 映射 %q 失败: %w", jr.JobID, err)
	}
	return nil
}

// Get 按 job id 查询映射，不存在时返回 sql.ErrNoRows。
func (r *JobRouteRepo) Get(jobID string) (*JobRoute, error) {
	row := r.db.QueryRow("SELECT "+jobRouteCols+" FROM job_routes WHERE job_id = ?", jobID)
	jr, err := scanJobRoute(row)
	if err != nil {
		return nil, err
	}
	return &jr, nil
}

// Delete 按 job id 删除一条映射（例如映射指向的上游 Key 已被删除时的清理）。
func (r *JobRouteRepo) Delete(jobID string) error {
	_, err := r.db.Exec("DELETE FROM job_routes WHERE job_id = ?", jobID)
	if err != nil {
		return fmt.Errorf("删除 job 映射 %q 失败: %w", jobID, err)
	}
	return nil
}

// DeleteExpired 删除所有已过期的映射，返回删除条数。
func (r *JobRouteRepo) DeleteExpired(now time.Time) (int64, error) {
	res, err := r.db.Exec("DELETE FROM job_routes WHERE expires_at < ?", now.Unix())
	if err != nil {
		return 0, fmt.Errorf("清理过期 job 映射失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("读取删除条数失败: %w", err)
	}
	return n, nil
}
