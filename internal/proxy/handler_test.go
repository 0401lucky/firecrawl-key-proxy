package proxy

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"firecrawl-proxy/internal/keypool"
	"firecrawl-proxy/internal/store"
)

// fakeClock 是 proxy 测试用的可推进时钟（与 keypool 测试中的实现等价，
// 测试工具不跨包导出，各自维护一份）。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{t: t} }
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

const testPublicBase = "https://fc.example.com"

// setupProxy 组装：临时 DB + n 个上游 Key + Key 池 + 代理 Handler。
// upstreamURL 为假上游地址；maxAttempts 默认 3。
func setupProxy(t *testing.T, upstreamURL string, n int) (*Handler, *store.Store, *fakeClock, *keypool.Pool) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "proxy.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.NewStore(db)
	for i := 1; i <= n; i++ {
		if _, err := st.UpstreamKeys.Create(&store.UpstreamKey{
			Name:   "key-" + string(rune('0'+i)),
			APIKey: "fc-test-0" + string(rune('0'+i)),
		}); err != nil {
			t.Fatalf("创建测试 Key %d 失败: %v", i, err)
		}
	}
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	pool, err := keypool.New(st.UpstreamKeys, clock, keypool.Config{
		DefaultCooldown: 60 * time.Second,
		FlushInterval:   10 * time.Second,
	})
	if err != nil {
		t.Fatalf("New(pool) 失败: %v", err)
	}
	h, err := NewHandler(pool, st.JobRoutes,
		slog.New(slog.NewTextHandler(io.Discard, nil)), Config{
			UpstreamBaseURL:  upstreamURL,
			PublicBaseURL:    testPublicBase,
			PathPrefixes:     []string{"/v1/", "/v2/"},
			MaxAttempts:      3,
			MaxRequestBuffer: 8 << 20,
			JobTTL:           48 * time.Hour,
			Clock:            clock,
		})
	if err != nil {
		t.Fatalf("New(handler) 失败: %v", err)
	}
	return h, st, clock, pool
}

// do 通过代理处理器发送一次请求并返回响应。
func do(t *testing.T, h *Handler, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// apiKey 返回第 i 个上游 Key 的明文（测试内使用）。
func apiKey(st *store.Store, i int64) string {
	uk, err := st.UpstreamKeys.Get(i)
	if err != nil {
		return ""
	}
	return uk.APIKey
}

// ---- 重试判定（独立于 keypool 的惩罚判定）----

func TestShouldRetry(t *testing.T) {
	netErr := errors.New("连接被拒")
	cases := []struct {
		name    string
		outcome keypool.Outcome
		want    bool
	}{
		{"2xx 成功不重试", keypool.Outcome{StatusCode: 200}, false},
		{"400 客户端错误不重试", keypool.Outcome{StatusCode: 400}, false},
		{"401 重试", keypool.Outcome{StatusCode: 401}, true},
		{"402 重试", keypool.Outcome{StatusCode: 402}, true},
		{"403 重试", keypool.Outcome{StatusCode: 403}, true},
		{"408 重试（不惩罚由 keypool 管）", keypool.Outcome{StatusCode: 408}, true},
		{"429 重试", keypool.Outcome{StatusCode: 429}, true},
		{"500 重试（不惩罚由 keypool 管）", keypool.Outcome{StatusCode: 500}, true},
		{"502 重试", keypool.Outcome{StatusCode: 502}, true},
		{"网络错误重试", keypool.Outcome{Err: netErr}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldRetry(c.outcome); got != c.want {
				t.Errorf("shouldRetry(%+v) = %v, want %v", c.outcome, got, c.want)
			}
		})
	}
}

// ---- AC1：402 转移 ----

func TestAC1_FailoverOn402(t *testing.T) {
	up := newFakeUpstream(
		planEntry{status: 402, body: `{"success":false}`},
		planEntry{status: 200, body: `{"success":true}`},
	)
	defer up.Close()
	h, st, _, _ := setupProxy(t, up.URL(), 3)

	rec := do(t, h, "POST", "/v2/scrape", []byte(`{"url":"https://x.com"}`))

	if rec.Code != 200 {
		t.Fatalf("期望 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	calls := up.Calls()
	if len(calls) != 2 {
		t.Fatalf("应重试一次共 2 次上游调用, got %d", len(calls))
	}
	// 第一次用 key-1，第二次必须是另一个未尝试过的 Key（轮询语义：游标取模候选集）。
	if calls[0].auth != "Bearer "+apiKey(st, 1) {
		t.Errorf("第一次应使用 key-1, got %q", calls[0].auth)
	}
	if calls[1].auth == calls[0].auth {
		t.Errorf("第二次重复使用了同一个 Key: %q", calls[1].auth)
	}
	if calls[1].auth != "Bearer "+apiKey(st, 2) && calls[1].auth != "Bearer "+apiKey(st, 3) {
		t.Errorf("第二次应使用 key-2 或 key-3, got %q", calls[1].auth)
	}
	// key-1 被标记 exhausted。
	uk, _ := st.UpstreamKeys.Get(1)
	if uk.State != store.StateExhausted {
		t.Errorf("key-1 state = %q, want exhausted", uk.State)
	}
	// key-2 不受影响。
	uk2, _ := st.UpstreamKeys.Get(2)
	if uk2.State != store.StateAvailable {
		t.Errorf("key-2 state = %q, want available", uk2.State)
	}
}

// ---- AC2：429 + Retry-After 冷却 ----

func TestAC2_CoolingOn429(t *testing.T) {
	up := newFakeUpstream(
		planEntry{status: 429, body: `{"error":"rate limit"}`,
			headers: map[string]string{"Retry-After": "30"}},
		planEntry{status: 200, body: `{"success":true}`},
	)
	defer up.Close()
	h, st, clock, _ := setupProxy(t, up.URL(), 3)

	rec := do(t, h, "POST", "/v2/scrape", []byte(`{"url":"https://x.com"}`))
	if rec.Code != 200 {
		t.Fatalf("期望 200, got %d", rec.Code)
	}

	uk, _ := st.UpstreamKeys.Get(1)
	if uk.State != store.StateCooling {
		t.Fatalf("key-1 state = %q, want cooling", uk.State)
	}
	if uk.CooldownUntil == nil {
		t.Fatal("key-1 应记录 cooldown_until")
	}
	got := uk.CooldownUntil.Unix() - clock.Now().Unix()
	if got < 29 || got > 30 {
		t.Errorf("冷却时长 = %d 秒, want 约 30", got)
	}
}

// ---- AC3：500 重试但不惩罚 ----

func TestAC3_NoPenaltyOn500(t *testing.T) {
	up := newFakeUpstream(
		planEntry{status: 500, body: `{"error":"boom"}`},
		planEntry{status: 200, body: `{"success":true}`},
	)
	defer up.Close()
	h, st, _, _ := setupProxy(t, up.URL(), 3)

	rec := do(t, h, "POST", "/v2/scrape", []byte(`{"url":"https://x.com"}`))
	if rec.Code != 200 {
		t.Fatalf("期望 200, got %d", rec.Code)
	}
	if len(up.Calls()) != 2 {
		t.Fatalf("应重试一次, got %d 次调用", len(up.Calls()))
	}
	// 核心断言：500 后 key-1 状态必须保持 available，且不写 last_error。
	uk, _ := st.UpstreamKeys.Get(1)
	if uk.State != store.StateAvailable {
		t.Errorf("500 后 key-1 state = %q, want available（不得惩罚）", uk.State)
	}
	if uk.LastError != nil {
		t.Errorf("500 后不应写 last_error, got %q", *uk.LastError)
	}
}

// ---- AC4：job 粘连 ----

func TestAC4_JobStickiness(t *testing.T) {
	up := newFakeUpstream(
		planEntry{status: 200, body: `{"success":true,"id":"abc","url":"https://api.firecrawl.dev/v2/crawl/abc"}`},
		planEntry{status: 200, body: `{"success":true,"status":"completed","data":[],"next":null}`},
		planEntry{status: 200, body: `{"success":true,"status":"completed","data":[],"next":null}`},
		planEntry{status: 200, body: `{"success":true,"status":"completed","data":[],"next":null}`},
		planEntry{status: 200, body: `{"success":true,"status":"completed","data":[],"next":null}`},
		planEntry{status: 200, body: `{"success":true,"status":"completed","data":[],"next":null}`},
	)
	defer up.Close()
	h, st, _, _ := setupProxy(t, up.URL(), 3)

	rec := do(t, h, "POST", "/v2/crawl", []byte(`{"url":"https://x.com"}`))
	if rec.Code != 200 {
		t.Fatalf("提交期望 200, got %d", rec.Code)
	}
	for i := 0; i < 5; i++ {
		rec := do(t, h, "GET", "/v2/crawl/abc", nil)
		if rec.Code != 200 {
			t.Fatalf("第 %d 次查询期望 200, got %d body=%s", i+1, rec.Code, rec.Body.String())
		}
	}

	// 全部 6 次调用必须命中同一个上游 Key。
	auths := up.callAuths()
	first := auths[0]
	for i, a := range auths {
		if a != first {
			t.Errorf("第 %d 次调用 Authorization=%s, 与提交时 %s 不一致（job 粘连失效）", i, a, first)
		}
	}
	// job 映射已持久化。
	jr, err := st.JobRoutes.Get("abc")
	if err != nil || jr.Kind != "crawl" {
		t.Fatalf("job 映射应存在: %+v, err=%v", jr, err)
	}
}

// ---- AC5：URL 重写 ----

func TestAC5_URewrite(t *testing.T) {
	up := newFakeUpstream(
		planEntry{status: 200, body: `{"success":true,"id":"abc","url":"https://api.firecrawl.dev/v2/crawl/abc"}`},
		planEntry{status: 200, body: `{"success":true,"data":[],"next":"https://api.firecrawl.dev/v2/crawl/abc?skip=10"}`},
	)
	defer up.Close()
	h, _, _, _ := setupProxy(t, up.URL(), 1)

	// 提交响应的 url 字段。
	rec := do(t, h, "POST", "/v2/crawl", []byte(`{"url":"https://x.com"}`))
	body := rec.Body.String()
	if !strings.Contains(body, `"url":"`+testPublicBase+"/v2/crawl/abc") {
		t.Errorf("提交响应 url 未改写: %s", body)
	}
	if strings.Contains(body, "api.firecrawl.dev") {
		t.Errorf("提交响应仍含 api.firecrawl.dev: %s", body)
	}

	// 查询响应的 next 字段。
	rec = do(t, h, "GET", "/v2/crawl/abc", nil)
	body = rec.Body.String()
	if !strings.Contains(body, `"next":"`+testPublicBase+"/v2/crawl/abc?skip=10") {
		t.Errorf("查询响应 next 未改写: %s", body)
	}
	if strings.Contains(body, "api.firecrawl.dev") {
		t.Errorf("查询响应仍含 api.firecrawl.dev: %s", body)
	}
	// 查询串保持不变（skip=10 应原样保留）。
	if !strings.Contains(body, "skip=10") {
		t.Errorf("查询串丢失: %s", body)
	}
}

// ---- AC6：全部不可用 → 503 ----

func TestAC6_AllUnavailable503(t *testing.T) {
	up := newFakeUpstream()
	defer up.Close()
	h, st, _, pool := setupProxy(t, up.URL(), 3)

	// 三个 Key 全部标记 exhausted。
	for i := int64(1); i <= 3; i++ {
		pool.Report(i, keypool.Outcome{StatusCode: 402})
	}
	_ = st

	rec := do(t, h, "POST", "/v2/scrape", []byte(`{"url":"https://x.com"}`))
	if rec.Code != 503 {
		t.Fatalf("期望 503, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"error":"no_upstream_key_available"`) {
		t.Errorf("错误码不符: %s", body)
	}
	if !strings.Contains(body, `"exhausted":3`) {
		t.Errorf("detail 状态计数不符: %s", body)
	}
	if up.callCount() != 0 {
		t.Errorf("不可用时不应请求上游, got %d 次", up.callCount())
	}
}

// ---- 其他 4xx 直接透传 ----

func Test400Passthrough(t *testing.T) {
	up := newFakeUpstream(
		planEntry{status: 400, body: `{"error":"invalid_url","message":"URL 无效"}`},
	)
	defer up.Close()
	h, st, _, _ := setupProxy(t, up.URL(), 3)

	rec := do(t, h, "POST", "/v2/scrape", []byte(`{"url":"bad"}`))
	if rec.Code != 400 {
		t.Fatalf("期望 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid_url") {
		t.Errorf("400 响应应原样透传: %s", rec.Body.String())
	}
	// 不重试、不改状态。
	if up.callCount() != 1 {
		t.Errorf("400 不应重试, got %d 次调用", up.callCount())
	}
	uk, _ := st.UpstreamKeys.Get(1)
	if uk.State != store.StateAvailable {
		t.Errorf("400 后 state = %q, want available", uk.State)
	}
}

// ---- 次数耗尽 → 502 ----

func Test502_AttemptsExhausted(t *testing.T) {
	up := newFakeUpstream(
		planEntry{status: 500, body: `{"error":"a"}`},
		planEntry{status: 500, body: `{"error":"b"}`},
		planEntry{status: 500, body: `{"error":"c"}`},
	)
	defer up.Close()
	h, _, _, _ := setupProxy(t, up.URL(), 3)

	rec := do(t, h, "POST", "/v2/scrape", []byte(`{"url":"https://x.com"}`))
	if rec.Code != 502 {
		t.Fatalf("期望 502, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"upstream_failover_exhausted"`) {
		t.Errorf("错误码不符: %s", rec.Body.String())
	}
	// maxAttempts=3，三次全部失败。
	if up.callCount() != 3 {
		t.Errorf("应尝试 3 次, got %d", up.callCount())
	}
}

// ---- 请求体超限：单次转发，不重试（AC15）----

func TestRequestBodyOverLimit(t *testing.T) {
	big := strings.Repeat("x", 100)

	// 超限且上游 500：单次转发，原样返回 500，不重试、不 502。
	up := newFakeUpstream(planEntry{status: 500, body: `{"error":"boom"}`})
	h, _, _, _ := setupProxy(t, up.URL(), 3)
	h.maxReqBuf = 16 // 覆盖为极小缓冲
	rec := do(t, h, "POST", "/v2/scrape", []byte(big))
	if rec.Code != 500 {
		t.Fatalf("超限+500 应原样透传 500, got %d", rec.Code)
	}
	if up.callCount() != 1 {
		t.Errorf("超限请求不应重试, got %d 次调用", up.callCount())
	}
	up.Close()

	// 超限且上游 200：仍能成功转发，body 完整。
	up2 := newFakeUpstream(planEntry{status: 200, body: `{"ok":true}`})
	defer up2.Close()
	h2, _, _, _ := setupProxy(t, up2.URL(), 3)
	h2.maxReqBuf = 16
	rec = do(t, h2, "POST", "/v2/scrape", []byte(big))
	if rec.Code != 200 {
		t.Fatalf("超限请求应仍能成功, got %d", rec.Code)
	}
	if len(up2.Calls()) != 1 || string(up2.Calls()[0].body) != big {
		t.Errorf("上游应收到完整请求体（逐字节一致）")
	}
}

// ---- 非 JSON 响应流式透传 ----

func TestNonJSONStreamed(t *testing.T) {
	img := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x01, 0x02, 0x03}
	up := newFakeUpstream(planEntry{status: 200, body: string(img),
		headers: map[string]string{"Content-Type": "image/png"}})
	defer up.Close()
	h, st, _, _ := setupProxy(t, up.URL(), 1)

	// 提交端点但返回非 JSON：不解析、不写 job、逐字节透传。
	rec := do(t, h, "POST", "/v2/crawl", []byte(`{"url":"https://x.com"}`))
	if rec.Code != 200 {
		t.Fatalf("期望 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if !bytes.Equal(rec.Body.Bytes(), img) {
		t.Errorf("二进制内容逐字节不一致")
	}
	// 未写 job 映射。
	if _, err := st.JobRoutes.Get("any"); err == nil {
		t.Error("非 JSON 响应不应写 job 映射")
	}
}

// ---- 前缀外路径 404 ----

func TestPathOutsidePrefix404(t *testing.T) {
	up := newFakeUpstream()
	defer up.Close()
	h, _, _, _ := setupProxy(t, up.URL(), 1)

	rec := do(t, h, "GET", "/api/admin/whatever", nil)
	if rec.Code != 404 {
		t.Fatalf("期望 404, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"error":"not_found"`) {
		t.Errorf("错误码不符: %s", rec.Body.String())
	}
	if up.callCount() != 0 {
		t.Errorf("不应转发到上游, got %d 次", up.callCount())
	}
}

// ---- job 映射过期退化为轮询 ----

func TestJobExpiryDegrades(t *testing.T) {
	up := newFakeUpstream(
		planEntry{status: 200, body: `{"success":true,"id":"abc","url":"https://api.firecrawl.dev/v2/crawl/abc"}`},
		planEntry{status: 404, body: `{"success":false,"error":"job not found"}`},
	)
	defer up.Close()
	h, st, clock, _ := setupProxy(t, up.URL(), 2)

	do(t, h, "POST", "/v2/crawl", []byte(`{"url":"https://x.com"}`))
	// 推进超过 TTL（48h）。
	clock.Advance(49 * time.Hour)

	rec := do(t, h, "GET", "/v2/crawl/abc", nil)
	// 映射过期后不粘连：退化为轮询，上游给什么就是什么（这里是 404）。
	if rec.Code != 404 {
		t.Fatalf("过期后应透传上游结果, got %d", rec.Code)
	}
	// 且不再命中提交时的 Key（round-robin 换到 key-2）。
	calls := up.Calls()
	if len(calls) == 2 && calls[1].auth == calls[0].auth {
		t.Error("过期映射仍被用于粘连")
	}
	// 过期映射应已被清理。
	if _, err := st.JobRoutes.Get("abc"); err == nil {
		t.Error("过期映射应被删除")
	}
}

// ---- 命中映射时无视 Key 状态、不 Report、不转移 ----

func TestStickyIgnoresKeyState(t *testing.T) {
	up := newFakeUpstream(
		planEntry{status: 200, body: `{"success":true,"id":"abc","url":"https://api.firecrawl.dev/v2/crawl/abc"}`},
		planEntry{status: 200, body: `{"success":true,"status":"running"}`},
	)
	defer up.Close()
	h, st, _, pool := setupProxy(t, up.URL(), 1)

	do(t, h, "POST", "/v2/crawl", []byte(`{"url":"https://x.com"}`))
	// 提交后把唯一 Key 打成 exhausted。
	pool.Report(1, keypool.Outcome{StatusCode: 402})
	before := mustState(t, st, 1)

	// 查询仍必须命中同一个（已耗尽）Key，且不报 503、不重试。
	rec := do(t, h, "GET", "/v2/crawl/abc", nil)
	if rec.Code != 200 {
		t.Fatalf("粘滞查询应成功, got %d body=%s", rec.Code, rec.Body.String())
	}
	if up.callCount() != 2 {
		t.Fatalf("粘滞查询不应触发重试, got %d 次调用", up.callCount())
	}
	// 状态未被搅乱。
	after := mustState(t, st, 1)
	if after != before {
		t.Errorf("粘滞查询后 Key 状态被改动: %q → %q", before, after)
	}
}

func mustState(t *testing.T, st *store.Store, id int64) store.KeyState {
	t.Helper()
	uk, err := st.UpstreamKeys.Get(id)
	if err != nil {
		t.Fatalf("读取 Key %d 失败: %v", id, err)
	}
	return uk.State
}

// ---- 转发保真：方法/路径/查询串/头/body/认证替换 ----

func TestForwardPreservesRequest(t *testing.T) {
	up := newFakeUpstream(planEntry{status: 200, body: `{"ok":true}`})
	defer up.Close()
	h, st, _, _ := setupProxy(t, up.URL(), 1)

	req := httptest.NewRequest("POST", "/v2/scrape?foo=bar&baz=1", strings.NewReader(`{"url":"https://x.com"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Custom", "hello")
	req.Header.Set("Authorization", "Bearer client-token") // 客户端自带的认证，必须被替换
	req.Header.Set("Connection", "keep-alive")            // hop-by-hop，必须剔除
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("期望 200, got %d", rec.Code)
	}
	calls := up.Calls()
	if len(calls) != 1 {
		t.Fatalf("调用次数 = %d, want 1", len(calls))
	}
	c := calls[0]
	if c.method != "POST" || c.path != "/v2/scrape" || c.rawQuery != "foo=bar&baz=1" {
		t.Errorf("方法/路径/查询串不符: %s %s?%s", c.method, c.path, c.rawQuery)
	}
	if c.auth != "Bearer "+apiKey(st, 1) {
		t.Errorf("Authorization 应为上游 Key, got %q", c.auth)
	}
	if string(c.body) != `{"url":"https://x.com"}` {
		t.Errorf("请求体不符: %q", c.body)
	}
	if c.ct != "application/json" {
		t.Errorf("Content-Type 应保留, got %q", c.ct)
	}
	// 假上游的 r.Header 里不应有 X-Custom 丢失问题——需要单独断言。
}

// TestHopByHopStripped 直接断言 hop-by-hop 头不会到达上游。
func TestHopByHopStripped(t *testing.T) {
	var gotHeader http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		w.WriteHeader(200)
		io.WriteString(w, `{"ok":true}`)
	}))
	defer up.Close()
	h, _, _, _ := setupProxy(t, up.URL, 1)

	req := httptest.NewRequest("GET", "/v2/scrape", nil)
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Keep-Alive", "timeout=5")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("X-Keep", "yes")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	for _, hdr := range []string{"Connection", "Keep-Alive", "Upgrade"} {
		if gotHeader.Get(hdr) != "" {
			t.Errorf("hop-by-hop 头 %s 不应到达上游: %q", hdr, gotHeader.Get(hdr))
		}
	}
	if gotHeader.Get("X-Keep") != "yes" {
		t.Errorf("普通头 X-Keep 应保留")
	}
}

// ---- 并发安全 ----

func TestConcurrentRequests(t *testing.T) {
	plan := make([]planEntry, 100)
	for i := range plan {
		plan[i] = planEntry{status: 200, body: `{"ok":true}`}
	}
	up := newFakeUpstream(plan...)
	defer up.Close()
	h, _, _, _ := setupProxy(t, up.URL(), 3)

	var wg sync.WaitGroup
	for g := 0; g < 20; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest("POST", "/v2/scrape", strings.NewReader(`{"url":"https://x.com"}`))
				h.ServeHTTP(rec, req)
				if rec.Code != 200 {
					t.Errorf("并发请求期望 200, got %d", rec.Code)
				}
			}
		}()
	}
	wg.Wait()
}

// ---- 网络错误：重试但不惩罚 ----

func TestNetworkErrorRetriesWithoutPenalty(t *testing.T) {
	// 第一个「上游」直接拒绝连接（关闭的 server），第二个正常。
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := dead.URL
	dead.Close() // 端口随即不可达

	up := newFakeUpstream(planEntry{status: 200, body: `{"ok":true}`})
	defer up.Close()

	// 用 dead 地址构造 handler：转发必然网络错误。
	db, _ := store.Open(filepath.Join(t.TempDir(), "x.db"))
	defer db.Close()
	st := store.NewStore(db)
	st.UpstreamKeys.Create(&store.UpstreamKey{Name: "k1", APIKey: "fc-net-01"})
	st.UpstreamKeys.Create(&store.UpstreamKey{Name: "k2", APIKey: "fc-net-02"})
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	pool, _ := keypool.New(st.UpstreamKeys, clock, keypool.Config{DefaultCooldown: time.Minute, FlushInterval: time.Minute})
	h, err := NewHandler(pool, st.JobRoutes,
		slog.New(slog.NewTextHandler(io.Discard, nil)), Config{
			UpstreamBaseURL: addr, PublicBaseURL: testPublicBase,
			PathPrefixes: []string{"/v1/", "/v2/"}, MaxAttempts: 2,
			MaxRequestBuffer: 1 << 20, JobTTL: time.Hour, Clock: clock,
		})
	if err != nil {
		t.Fatalf("New(handler) 失败: %v", err)
	}

	rec := do(t, h, "POST", "/v2/scrape", []byte(`{"url":"https://x.com"}`))
	// 两个 Key 都网络错误 → 502（attempt>0），且 Key 状态必须保持 available。
	if rec.Code != 502 {
		t.Fatalf("期望 502, got %d body=%s", rec.Code, rec.Body.String())
	}
	for i := int64(1); i <= 2; i++ {
		uk, _ := st.UpstreamKeys.Get(i)
		if uk.State != store.StateAvailable {
			t.Errorf("网络错误后 key-%d state = %q, want available（不得惩罚）", i, uk.State)
		}
		if uk.LastError != nil {
			t.Errorf("网络错误后 key-%d 不应写 last_error", i)
		}
	}
}

// TestLogHasMaskedKey 断言请求日志中只出现脱敏后的上游 Key（AC11 的代理侧一半）。
func TestLogHasMaskedKey(t *testing.T) {
	up := newFakeUpstream(planEntry{status: 200, body: `{"ok":true}`})
	defer up.Close()

	var logBuf bytes.Buffer
	// 单独构造 handler：注入可捕获的 logger。
	db, _ := store.Open(filepath.Join(t.TempDir(), "log.db"))
	defer db.Close()
	st := store.NewStore(db)
	st.UpstreamKeys.Create(&store.UpstreamKey{Name: "k1", APIKey: "fc-secret-1234"})
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	pool, _ := keypool.New(st.UpstreamKeys, clock, keypool.Config{DefaultCooldown: time.Minute, FlushInterval: time.Minute})
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	h, err := NewHandler(pool, st.JobRoutes, logger, Config{
		UpstreamBaseURL: up.URL(), PublicBaseURL: testPublicBase,
		PathPrefixes: []string{"/v1/", "/v2/"}, MaxAttempts: 3,
		MaxRequestBuffer: 1 << 20, JobTTL: time.Hour, Clock: clock,
	})
	if err != nil {
		t.Fatalf("New(handler) 失败: %v", err)
	}

	do(t, h, "POST", "/v2/scrape", []byte(`{"url":"https://x.com"}`))

	logText := logBuf.String()
	if !strings.Contains(logText, "fc-****1234") {
		t.Errorf("日志应含脱敏 Key fc-****1234: %s", logText)
	}
	if strings.Contains(logText, "fc-secret-1234") {
		t.Errorf("日志泄漏上游 Key 明文: %s", logText)
	}
}
