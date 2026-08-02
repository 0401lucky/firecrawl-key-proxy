package store

import (
	"database/sql"
	"time"
)

// 本组函数统一处理 time.Time/指针 与 SQLite 存储格式（unix 秒 int64 / NULL）之间的转换，
// 转换只允许发生在仓储内部，上层一律使用 time.Time。

func int64PtrToTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(v.Int64, 0)
	return &t
}

func timeToInt64Ptr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Unix()
}

func nullInt64ToPtr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	p := v.Int64
	return &p
}

func ptrToNullInt64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullStringToPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	p := v.String
	return &p
}

func ptrToNullString(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}
