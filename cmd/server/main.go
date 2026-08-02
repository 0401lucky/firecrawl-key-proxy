// Firecrawl 多 Key 反向代理与管理面板 —— 服务入口。
//
// 启动流程：加载配置 → 初始化日志 → 打开数据库并建表 → 启动 HTTP 服务。
// 关闭流程（信号或上下文取消）：优雅关闭 HTTP → 冲刷用量计数（C2 接入）→ 关闭数据库。
// 顺序不可颠倒：flush 若发生在数据库关闭之后会写到已关闭的连接。
//
// 信号 → 上下文的桥接放在 main()：signal.NotifyContext 把 SIGINT/SIGTERM
// 转成 context 取消，run(ctx) 只依赖 ctx.Done()。这样优雅关闭路径可以在
// 任何平台上用集成测试直接验证，不必依赖真实信号投递（Windows/MSYS 的
// kill -TERM 会强制终止进程，信号处理器收不到）。
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"firecrawl-proxy/internal/config"
	"firecrawl-proxy/internal/logging"
	"firecrawl-proxy/internal/store"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		slog.Error("服务启动失败", "error", err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		// 配置错误必须让用户一次看到全部缺项/错项，直接输出到 stderr。
		slog.New(slog.NewTextHandler(os.Stderr, nil)).Error("配置加载失败", "error", err.Error())
		return err
	}

	logger, err := logging.New(cfg.LogLevel)
	if err != nil {
		return err
	}
	slog.SetDefault(logger)

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	st := store.NewStore(db)
	logger.Info("数据库就绪", "path", cfg.DBPath)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	// 后续子任务（C3 代理、C5 面板、C6 静态资源）在此注册路由。
	_ = st

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("HTTP 服务启动", "addr", cfg.ListenAddr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		logger.Info("收到退出信号，开始优雅关闭")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP 服务关闭超时", "error", err.Error())
	}

	// C2 接入后在此冲刷内存中的用量计数（先于 db.Close）。
	logger.Info("服务已退出")
	return nil
}
