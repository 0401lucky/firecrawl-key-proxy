package main

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

// freeAddr 返回一个本机空闲端口（监听后立刻关闭，测试内使用，竞态可忽略）。
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("获取空闲端口失败: %v", err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// TestGracefulShutdown 用上下文取消触发优雅关闭路径：
// HTTP 服务能正常响应 /healthz，取消后 run() 在 5 秒内返回，
// 数据库文件被正常关闭、可再次打开。
func TestGracefulShutdown(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shutdown.db")
	addr := freeAddr(t)
	t.Setenv("PUBLIC_BASE_URL", "https://fc.example.com")
	t.Setenv("ADMIN_PASSWORD", "secret")
	t.Setenv("DB_PATH", dbPath)
	t.Setenv("LISTEN_ADDR", addr)
	t.Setenv("LOG_LEVEL", "error")

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx)
	}()

	// 轮询 /healthz 直到服务就绪（最多 5 秒）。
	base := fmt.Sprintf("http://%s", addr)
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("服务 5 秒内未就绪: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 触发优雅关闭。
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run() 优雅关闭后不应报错: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消上下文后 run() 未在 5 秒内退出")
	}

	// 数据库应已干净关闭且数据完好（schema 已建）。
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("重新打开数据库失败: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='upstream_keys'",
	).Scan(&n); err != nil {
		t.Fatalf("查询 schema 失败: %v", err)
	}
	if n != 1 {
		t.Error("优雅关闭后 schema 表应仍存在")
	}
}
