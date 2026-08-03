// Package store 提供 SQLite 持久化层：建表、连接管理与四张表的仓储。
//
// 仓储方法只做数据存取，不含业务判断。时间统一以 unix 秒（int64）入库，
// 对上层暴露 time.Time，转换只在仓储内部发生，避免单位在各层间漂移。
package store

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// Open 打开（必要时创建）SQLite 数据库并幂等执行 schema。
//
// 连接串用「裸路径 + 查询参数」，不加 file: 前缀：加了 file: 会进入 SQLite URI
// 解析模式，路径中的 # 会被当作 URI 片段分隔符，数据库会静默落到截断后的
// 错误路径（Go 子测试的临时目录名含 #01 时就触发过，数据落盘位置错误且
// 难以排查）。modernc 自己解析 ? 之后的 _pragma 参数，不依赖 file: 前缀
//（见 modernc.org/sqlite/dsn_test.go）。
//
// 参数：WAL 读写互不阻塞；busy_timeout 让瞬时并发写排队而不是直接报
// database is locked；foreign_keys 让 job_routes 的 ON DELETE CASCADE 生效。
func Open(path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建数据库目录失败: %w", err)
		}
	}
	dsn := fmt.Sprintf(
		"%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)",
		path,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("执行 schema 失败: %w", err)
	}
	return db, nil
}

// NewStore 构造全部仓储。db 的所有权归调用方（main），关闭由调用方负责。
func NewStore(db *sql.DB) *Store {
	return &Store{
		UpstreamKeys: &UpstreamKeyRepo{db: db},
		ProxyKeys:    &ProxyKeyRepo{db: db},
		JobRoutes:    &JobRouteRepo{db: db},
		Sessions:     &SessionRepo{db: db},
		CallStats:    &CallStatsRepo{db: db},
	}
}

// Store 聚合全部仓储，供上层一次注入。
type Store struct {
	UpstreamKeys *UpstreamKeyRepo
	ProxyKeys    *ProxyKeyRepo
	JobRoutes    *JobRouteRepo
	Sessions     *SessionRepo
	CallStats    *CallStatsRepo
}
