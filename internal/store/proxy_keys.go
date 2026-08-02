package store

import (
	"database/sql"
	"fmt"
	"time"
)

// ProxyKeyRepo 是 proxy_keys 表的仓储。明文只在创建时返回一次，库里只存哈希与前缀。
type ProxyKeyRepo struct {
	db *sql.DB
}

const proxyKeyCols = `id, name, key_hash, key_prefix, revoked, request_count, last_used_at, created_at`

func scanProxyKey(row interface{ Scan(...any) error }) (ProxyKey, error) {
	var (
		pk        ProxyKey
		revoked   int
		lastUsed  sql.NullInt64
		createdAt int64
	)
	err := row.Scan(
		&pk.ID, &pk.Name, &pk.KeyHash, &pk.KeyPrefix, &revoked,
		&pk.RequestCount, &lastUsed, &createdAt,
	)
	if err != nil {
		return ProxyKey{}, err
	}
	pk.Revoked = revoked != 0
	pk.LastUsedAt = int64PtrToTime(lastUsed)
	pk.CreatedAt = time.Unix(createdAt, 0)
	return pk, nil
}

// List 返回全部下游代理 Key，按 id 升序。
func (r *ProxyKeyRepo) List() ([]ProxyKey, error) {
	rows, err := r.db.Query("SELECT " + proxyKeyCols + " FROM proxy_keys ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("查询代理 Key 列表失败: %w", err)
	}
	defer rows.Close()

	var out []ProxyKey
	for rows.Next() {
		pk, err := scanProxyKey(rows)
		if err != nil {
			return nil, fmt.Errorf("读取代理 Key 行失败: %w", err)
		}
		out = append(out, pk)
	}
	return out, rows.Err()
}

// Create 插入一条下游代理 Key，返回自增 id。
func (r *ProxyKeyRepo) Create(pk *ProxyKey) (int64, error) {
	res, err := r.db.Exec(
		`INSERT INTO proxy_keys (name, key_hash, key_prefix, created_at)
		 VALUES (?, ?, ?, ?)`,
		pk.Name, pk.KeyHash, pk.KeyPrefix, time.Now().Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("插入代理 Key 失败: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("读取自增 id 失败: %w", err)
	}
	pk.ID = id
	return id, nil
}

// FindByHash 按哈希查找代理 Key，不存在时返回 sql.ErrNoRows。
func (r *ProxyKeyRepo) FindByHash(hash string) (*ProxyKey, error) {
	row := r.db.QueryRow("SELECT "+proxyKeyCols+" FROM proxy_keys WHERE key_hash = ?", hash)
	pk, err := scanProxyKey(row)
	if err != nil {
		return nil, err
	}
	return &pk, nil
}

// Revoke 吊销一条代理 Key（revoked=1）。吊销后该 Key 立即不可用。
func (r *ProxyKeyRepo) Revoke(id int64) error {
	_, err := r.db.Exec("UPDATE proxy_keys SET revoked = 1 WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("吊销代理 Key %d 失败: %w", id, err)
	}
	return nil
}

// IncrementUsage 批量累加 request_count 并把 last_used_at 置为当前时间。
func (r *ProxyKeyRepo) IncrementUsage(usage map[int64]int64) error {
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
		"UPDATE proxy_keys SET request_count = request_count + ?, last_used_at = ? WHERE id = ?",
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
			return fmt.Errorf("累加代理 Key %d 用量失败: %w", id, err)
		}
	}
	return tx.Commit()
}
