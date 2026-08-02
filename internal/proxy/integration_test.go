package proxy

import (
	"bytes"
	"log/slog"

	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"firecrawl-proxy/internal/auth"
	"firecrawl-proxy/internal/keypool"
	"firecrawl-proxy/internal/store"
)

// TestEndToEndAuthToProxy 验证「代理 Key 认证 → 代理转发 → 假上游」全链路：
// 无 token 401，有 token 200 且上游收到调用，日志记调用方名称且无明文。
func TestEndToEndAuthToProxy(t *testing.T) {
	up := newFakeUpstream(planEntry{status: 200, body: `{"success":true}`})
	defer up.Close()

	db, err := store.Open(filepath.Join(t.TempDir(), "e2e.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	defer db.Close()
	st := store.NewStore(db)
	st.UpstreamKeys.Create(&store.UpstreamKey{Name: "up", APIKey: "fc-e2e-9999"})

	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	pool, _ := keypool.New(st.UpstreamKeys, clock, keypool.Config{
		DefaultCooldown: time.Minute, FlushInterval: time.Minute,
	})
	var logBuf bytes.Buffer
	h, err := NewHandler(pool, st.JobRoutes,
		slog.New(slog.NewJSONHandler(&logBuf, nil)), Config{
			UpstreamBaseURL: up.URL(), PublicBaseURL: testPublicBase,
			PathPrefixes: []string{"/v1/", "/v2/"}, MaxAttempts: 3,
			MaxRequestBuffer: 1 << 20, JobTTL: time.Hour, Clock: clock,
		})
	if err != nil {
		t.Fatalf("New(handler) 失败: %v", err)
	}
	pa := auth.NewProxyKeyAuth(st.ProxyKeys)
	proxyKey, _, _ := pa.Issue("本地脚本")

	stack := pa.Middleware(h)

	// 无 token → 401，且上游零调用。
	req := httptest.NewRequest("POST", "/v2/scrape", strings.NewReader(`{"url":"https://x.com"}`))
	rec := httptest.NewRecorder()
	stack.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("无 token 期望 401, got %d", rec.Code)
	}
	if up.callCount() != 0 {
		t.Fatal("未认证请求不应到达上游")
	}

	// 有 token → 200，上游收到调用，且请求体原样到达。
	req = httptest.NewRequest("POST", "/v2/scrape", strings.NewReader(`{"url":"https://x.com"}`))
	req.Header.Set("Authorization", "Bearer "+proxyKey)
	rec = httptest.NewRecorder()
	stack.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("有效 token 期望 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	calls := up.Calls()
	if len(calls) != 1 || calls[0].auth != "Bearer fc-e2e-9999" {
		t.Fatalf("上游应收到一次带上游 Key 的调用: %+v", calls)
	}

	// 日志：记调用方名称、上游 Key 脱敏，无任何明文。
	logText := logBuf.String()
	if !strings.Contains(logText, `"proxy_key":"本地脚本"`) {
		t.Errorf("日志应记调用方名称: %s", logText)
	}
	if !strings.Contains(logText, "fc-****9999") {
		t.Errorf("日志应含脱敏上游 Key: %s", logText)
	}
	if strings.Contains(logText, proxyKey) {
		t.Errorf("日志泄漏代理 Key 明文: %s", logText)
	}
	if strings.Contains(logText, "fc-e2e-9999") {
		t.Errorf("日志泄漏上游 Key 明文: %s", logText)
	}

	// 计数：刷盘后代理 Key 的 request_count == 1。
	if err := pa.Flush(); err != nil {
		t.Fatalf("Flush() 失败: %v", err)
	}
	pk, _ := st.ProxyKeys.List()
	if len(pk) != 1 || pk[0].RequestCount != 1 {
		t.Errorf("代理 Key 计数 = %+v, want 1", pk)
	}
}
