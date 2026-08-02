package store

import (
	"database/sql"
	"fmt"
	"time"
)

// UpstreamKeyRepo 是 upstream_keys 表的仓储。方法只做数据存取。
type UpstreamKeyRepo struct {
	db *sql.DB
}

const upstreamKeyCols = `id, name, api_key, key_suffix, enabled, state,
	cooldown_until, credits_total, credits_remaining, credits_synced_at,
	request_count, last_error, last_used_at, created_at`

func scanUpstreamKey(row interface{ Scan(...any) error }) (UpstreamKey, error) {
	var (
		uk              UpstreamKey
		enabled         int
		cooldownUntil   sql.NullInt64
		creditsTotal    sql.NullInt64
		creditsRemain   sql.NullInt64
		creditsSyncedAt sql.NullInt64
		lastError       sql.NullString
		lastUsedAt      sql.NullInt64
		createdAt       int64
	)
	err := row.Scan(
		&uk.ID, &uk.Name, &uk.APIKey, &uk.KeySuffix, &enabled, &uk.State,
		&cooldownUntil, &creditsTotal, &creditsRemain, &creditsSyncedAt,
		&uk.RequestCount, &lastError, &lastUsedAt, &createdAt,
	)
	if err != nil {
		return UpstreamKey{}, err
	}
	uk.Enabled = enabled != 0
	uk.CooldownUntil = int64PtrToTime(cooldownUntil)
	uk.CreditsTotal = nullInt64ToPtr(creditsTotal)
	uk.CreditsRemaining = nullInt64ToPtr(creditsRemain)
	uk.CreditsSyncedAt = int64PtrToTime(creditsSyncedAt)
	uk.LastError = nullStringToPtr(lastError)
	uk.LastUsedAt = int64PtrToTime(lastUsedAt)
	uk.CreatedAt = time.Unix(createdAt, 0)
	return uk, nil
}

// List 返回全部上游 Key，按 id 升序。
func (r *UpstreamKeyRepo) List() ([]UpstreamKey, error) {
	rows, err := r.db.Query("SELECT " + upstreamKeyCols + " FROM upstream_keys ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("查询上游 Key 列表失败: %w", err)
	}
	defer rows.Close()

	var out []UpstreamKey
	for rows.Next() {
		uk, err := scanUpstreamKey(rows)
		if err != nil {
			return nil, fmt.Errorf("读取上游 Key 行失败: %w", err)
		}
		out = append(out, uk)
	}
	return out, rows.Err()
}

// Get 按 id 返回单条上游 Key，不存在时返回 sql.ErrNoRows。
func (r *UpstreamKeyRepo) Get(id int64) (*UpstreamKey, error) {
	row := r.db.QueryRow("SELECT "+upstreamKeyCols+" FROM upstream_keys WHERE id = ?", id)
	uk, err := scanUpstreamKey(row)
	if err != nil {
		return nil, err
	}
	return &uk, nil
}

// Create 插入一条上游 Key，返回自增 id。
// key_suffix 若未由调用方设置，则自动取 api_key 末 4 位，保证 NOT NULL 约束在存储边界成立。
func (r *UpstreamKeyRepo) Create(uk *UpstreamKey) (int64, error) {
	if uk.KeySuffix == "" {
		uk.KeySuffix = lastChars(uk.APIKey, 4)
	}
	if uk.State == "" {
		uk.State = StateAvailable
	}
	res, err := r.db.Exec(
		`INSERT INTO upstream_keys (name, api_key, key_suffix, state, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		uk.Name, uk.APIKey, uk.KeySuffix, string(uk.State), time.Now().Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("插入上游 Key 失败: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("读取自增 id 失败: %w", err)
	}
	uk.ID = id
	return id, nil
}

// Update 更新一条上游 Key 的可变字段：
// name、enabled、state、cooldown_until、credits_*、last_error。
// 可空字段以 nil 表示「置空」。request_count 与 last_used_at 走 IncrementUsage。
func (r *UpstreamKeyRepo) Update(uk *UpstreamKey) error {
	enabled := 0
	if uk.Enabled {
		enabled = 1
	}
	_, err := r.db.Exec(
		`UPDATE upstream_keys SET
			name = ?, enabled = ?, state = ?,
			cooldown_until = ?, credits_total = ?, credits_remaining = ?,
			credits_synced_at = ?, last_error = ?
		 WHERE id = ?`,
		uk.Name, enabled, string(uk.State),
		timeToInt64Ptr(uk.CooldownUntil), ptrToNullInt64(uk.CreditsTotal),
		ptrToNullInt64(uk.CreditsRemaining), timeToInt64Ptr(uk.CreditsSyncedAt),
		ptrToNullString(uk.LastError), uk.ID,
	)
	if err != nil {
		return fmt.Errorf("更新上游 Key %d 失败: %w", uk.ID, err)
	}
	return nil
}

// Delete 删除一条上游 Key。job_routes 表通过外键 ON DELETE CASCADE 自动清理。
func (r *UpstreamKeyRepo) Delete(id int64) error {
	_, err := r.db.Exec("DELETE FROM upstream_keys WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("删除上游 Key %d 失败: %w", id, err)
	}
	return nil
}

// IncrementUsage 批量累加 request_count 并把 last_used_at 置为当前时间。
// 批量形式由 C2 的内存缓冲策略驱动（固定间隔刷盘一次），避免每请求一次 DB 写入。
func (r *UpstreamKeyRepo) IncrementUsage(usage map[int64]int64) error {
	if len(usage) == 0 {
		return nil
	}
	now := time.Now().Unix()
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(
		"UPDATE upstream_keys SET request_count = request_count + ?, last_used_at = ? WHERE id = ?",
	)
	if err != nil {
		return fmt.Errorf("准备更新语句失败: %w", err)
	}
	defer stmt.Close()

	for id, delta := range usage {
		if delta <= 0 {
			continue
		}
		if _, err := stmt.Exec(delta, now, id); err != nil {
			return fmt.Errorf("累加上游 Key %d 用量失败: %w", id, err)
		}
	}
	return tx.Commit()
}

// lastChars 返回字符串末 n 个字符；不足 n 个时返回原串。
func lastChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
