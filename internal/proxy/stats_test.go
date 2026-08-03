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

// TestStatsRecordedPerAttempt 验证调用统计与 request_count 同位：
//   - 200 响应 → 2xx 桶 +1；
//   - 500 后换 Key 重试成功 → 两次上游尝试各计一次（5xx 桶 +1、2xx 桶 +1）；
//   - Flush 后 DB 落库，类别正确。
//
// 口径对齐：stats 与 request_count 在 netErr==nil 的同一分支记录，
// 因此「窗口总调用 ≈ Σ request_count」恒成立（AC6）。
func TestStatsRecordedPerAttempt(t *testing.T) {
	up := newFakeUpstream(
		planEntry{status: 200, body: `{"ok":true}`},
		planEntry{status: 500, body: `boom`}, // 第一次尝试 500 → 触发故障转移
		planEntry{status: 200, body: `{"ok":true}`},
	)
	defer up.Close()

	db, err := store.Open(filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	defer db.Close()
	st := store.NewStore(db)
	for i, name := range []string{"up-1", "up-2"} {
		if _, err := st.UpstreamKeys.Create(&store.UpstreamKey{
			Name: name, APIKey: "fc-stats-" + string(rune('0'+i+1)),
		}); err != nil {
			t.Fatalf("创建测试 Key 失败: %v", err)
		}
	}

	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	pool, err := keypool.New(st.UpstreamKeys, clock, keypool.Config{
		DefaultCooldown: time.Minute, FlushInterval: time.Minute, StatsRepo: st.CallStats,
	})
	if err != nil {
		t.Fatalf("New(pool) 失败: %v", err)
	}
	h, err := NewHandler(pool, st.JobRoutes,
		slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)), Config{
			UpstreamBaseURL: up.URL(), PublicBaseURL: testPublicBase,
			PathPrefixes: []string{"/v1/", "/v2/"}, MaxAttempts: 3,
			MaxRequestBuffer: 1 << 20, JobTTL: time.Hour, Clock: clock,
		})
	if err != nil {
		t.Fatalf("New(handler) 失败: %v", err)
	}
	pa := auth.NewProxyKeyAuth(st.ProxyKeys)
	proxyKey, _, _ := pa.Issue("stats-test")
	stack := pa.Middleware(h)

	send := func() int {
		req := httptest.NewRequest("POST", "/v2/scrape", strings.NewReader(`{"url":"https://x.com"}`))
		req.Header.Set("Authorization", "Bearer "+proxyKey)
		rec := httptest.NewRecorder()
		stack.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := send(); code != 200 {
		t.Fatalf("请求 1 期望 200, got %d", code)
	}
	if code := send(); code != 200 {
		t.Fatalf("请求 2（500→重试→200）期望 200, got %d", code)
	}
	// 共 3 次上游尝试（200 + 500 + 200）。
	if n := up.callCount(); n != 3 {
		t.Fatalf("上游调用次数 = %d, want 3", n)
	}

	if err := pool.Flush(); err != nil {
		t.Fatalf("Flush() 失败: %v", err)
	}
	hour := clock.Now().Truncate(time.Hour).Unix()
	rows, err := st.CallStats.QueryWindow(hour)
	if err != nil {
		t.Fatalf("QueryWindow 失败: %v", err)
	}
	var calls2xx, calls5xx int64
	for _, r := range rows {
		switch r.StatusClass {
		case store.StatusClass2xx:
			calls2xx += r.Calls
		case store.StatusClass5xx:
			calls5xx += r.Calls
		}
	}
	if calls2xx != 2 {
		t.Errorf("2xx 统计 = %d, want 2", calls2xx)
	}
	if calls5xx != 1 {
		t.Errorf("5xx 统计 = %d, want 1（故障转移的失败尝试同样计入）", calls5xx)
	}

	// 口径一致性：Σ request_count == Σ stats（同一批请求，Flush 后比对）。
	var reqCount int64
	for _, uk := range mustList(t, st) {
		reqCount += uk.RequestCount
	}
	if reqCount != calls2xx+calls5xx {
		t.Errorf("Σ request_count = %d, 统计总数 = %d, 应一致（AC6）", reqCount, calls2xx+calls5xx)
	}
}

func mustList(t *testing.T, st *store.Store) []store.UpstreamKey {
	t.Helper()
	list, err := st.UpstreamKeys.List()
	if err != nil {
		t.Fatalf("List() 失败: %v", err)
	}
	return list
}
