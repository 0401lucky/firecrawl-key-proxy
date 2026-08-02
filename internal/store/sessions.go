package store

import (
	"database/sql"
	"fmt"
	"time"
)

// SessionRepo 是 admin_sessions 表的仓储：面板管理员会话。
type SessionRepo struct {
	db *sql.DB
}

const sessionCols = `token_hash, created_at, expires_at`

func scanSession(row interface{ Scan(...any) error }) (Session, error) {
	var (
		s         Session
		createdAt int64
		expiresAt int64
	)
	err := row.Scan(&s.TokenHash, &createdAt, &expiresAt)
	if err != nil {
		return Session{}, err
	}
	s.CreatedAt = time.Unix(createdAt, 0)
	s.ExpiresAt = time.Unix(expiresAt, 0)
	return s, nil
}

// Create 写入一条会话。
func (r *SessionRepo) Create(s *Session) error {
	_, err := r.db.Exec(
		"INSERT INTO admin_sessions (token_hash, created_at, expires_at) VALUES (?, ?, ?)",
		s.TokenHash, s.CreatedAt.Unix(), s.ExpiresAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("写入会话失败: %w", err)
	}
	return nil
}

// Get 按哈希查询会话，不存在时返回 sql.ErrNoRows。
func (r *SessionRepo) Get(tokenHash string) (*Session, error) {
	row := r.db.QueryRow("SELECT "+sessionCols+" FROM admin_sessions WHERE token_hash = ?", tokenHash)
	s, err := scanSession(row)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// Delete 删除一条会话（登出时调用）。
func (r *SessionRepo) Delete(tokenHash string) error {
	_, err := r.db.Exec("DELETE FROM admin_sessions WHERE token_hash = ?", tokenHash)
	if err != nil {
		return fmt.Errorf("删除会话失败: %w", err)
	}
	return nil
}

// DeleteExpired 删除所有已过期会话，返回删除条数。
func (r *SessionRepo) DeleteExpired(now time.Time) (int64, error) {
	res, err := r.db.Exec("DELETE FROM admin_sessions WHERE expires_at < ?", now.Unix())
	if err != nil {
		return 0, fmt.Errorf("清理过期会话失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("读取删除条数失败: %w", err)
	}
	return n, nil
}
