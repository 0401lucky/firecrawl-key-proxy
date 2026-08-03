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

	"firecrawl-proxy/internal/admin"
	"firecrawl-proxy/internal/auth"
	"firecrawl-proxy/internal/config"
	"firecrawl-proxy/internal/firecrawl"
	"firecrawl-proxy/internal/keypool"
	"firecrawl-proxy/internal/logging"
	"firecrawl-proxy/internal/proxy"
	"firecrawl-proxy/internal/store"
	"firecrawl-proxy/internal/webui"
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

	pool, err := keypool.New(st.UpstreamKeys, keypool.RealClock{}, keypool.Config{
		DefaultCooldown: time.Duration(cfg.DefaultCooldownSec) * time.Second,
		FlushInterval:   10 * time.Second,
		StatsRepo:       st.CallStats,
	})
	if err != nil {
		return err
	}
	go pool.Run(ctx)
	go startStatsCleanup(ctx, st.CallStats, logger)
	logger.Info("上游 Key 池就绪")

	proxyHandler, err := proxy.NewHandler(pool, st.JobRoutes, logger, proxy.Config{
		UpstreamBaseURL:  cfg.UpstreamBaseURL,
		PublicBaseURL:    cfg.PublicBaseURL,
		PathPrefixes:     cfg.ProxyPathPrefixes,
		MaxAttempts:      cfg.MaxFailoverAttempts,
		MaxRequestBuffer: cfg.MaxRequestBufferSize,
		JobTTL:           time.Duration(cfg.JobRouteTTLHours) * time.Hour,
		Clock:            keypool.RealClock{},
	})
	if err != nil {
		return err
	}
	go proxyHandler.StartCleanup(ctx)

	proxyAuth := auth.NewProxyKeyAuth(st.ProxyKeys)
	go proxyAuth.Run(ctx)

	// 面板会话与 API。
	sessionAuth := auth.NewSessionAuth(st.Sessions, cfg.AdminPassword,
		time.Duration(cfg.SessionTTLHours)*time.Hour, keypool.RealClock{}, logger)
	go sessionAuth.StartSessionCleanup(ctx)
	adminServer := admin.NewServer(pool, st, proxyAuth, sessionAuth,
		firecrawl.NewClient(cfg.UpstreamBaseURL), logger, keypool.RealClock{})
	// 后台额度刷新：独立 goroutine，出错/关闭不影响代理调度。
	go adminServer.StartCreditRefresh(ctx, time.Duration(cfg.CreditRefreshMinutes)*time.Minute)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})
	// 面板 JSON API（session cookie 认证，与代理认证互斥）。
	mux.Handle("/api/admin/", adminServer.Router())
	// 代理转发路径：按前缀挂载，外层包代理 Key 认证中间件（AC7/AC13）。
	// 中间件只覆盖代理前缀，/healthz 与 /api/admin/* 不受影响。
	authedProxy := proxyAuth.Middleware(proxyHandler)
	for _, p := range cfg.ProxyPathPrefixes {
		mux.Handle(p, authedProxy)
	}
	// SPA 静态资源兜底（最低优先级，最长前缀匹配保证它最后命中）。
	mux.Handle("/", webui.Handler())

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

	// 先冲刷内存中的用量计数（上游 Key 池 + 代理 Key），再关闭数据库（顺序不可颠倒）。
	if err := pool.Flush(); err != nil {
		logger.Error("退出前刷盘失败", "error", err.Error())
	}
	if err := proxyAuth.Flush(); err != nil {
		logger.Error("代理 Key 退出前刷盘失败", "error", err.Error())
	}
	logger.Info("服务已退出")
	return nil
}

// callStatsRetention 是调用统计桶的保留时长：31 天，硬编码（MVP 不开放配置）。
const callStatsRetention = 31 * 24 * time.Hour

// startStatsCleanup 定期清理过期的调用统计桶（保留 31 天）。
// 启动时清一次，之后每小时一次；与 job 映射清理同模式，ctx 取消即退出。
// 统计桶数量级极小（≈744×Key 数×4 类），清理不会造成明显写压力。
func startStatsCleanup(ctx context.Context, repo *store.CallStatsRepo, logger *slog.Logger) {
	run := func() {
		cutoff := time.Now().Add(-callStatsRetention).Truncate(time.Hour).Unix()
		n, err := repo.DeleteBefore(cutoff)
		if err != nil {
			logger.Warn("清理过期调用统计失败", "error", err.Error())
			return
		}
		if n > 0 {
			logger.Info("清理过期调用统计桶", "count", n)
		}
	}
	run()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			run()
		case <-ctx.Done():
			return
		}
	}
}
