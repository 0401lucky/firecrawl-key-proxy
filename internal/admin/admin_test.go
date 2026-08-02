package admin

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"firecrawl-proxy/internal/auth"
	"firecrawl-proxy/internal/firecrawl"
	"firecrawl-proxy/internal/keypool"
	"firecrawl-proxy/internal/proxy"
	"firecrawl-proxy/internal/store"
	"firecrawl-proxy/internal/webui"
)

// fakeClock 是 admin 测试用的可推进时钟。
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

const testPassword = "secret"

// setupAdmin 组装：DB + n 个上游 Key + 池 + 代理 Key 认证 + 会话认证 + 面板 Server。
// upstreamURL 指向假上游（额度接口 /team/credit-usage 由用例自行配置）。
func setupAdmin(t *testing.T, upstreamURL string, n int) (*Server, *store.Store, *fakeClock) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.NewStore(db)
	for i := 1; i <= n; i++ {
		if _, err := st.UpstreamKeys.Create(&store.UpstreamKey{
			Name: "up-" + string(rune('0'+i)), APIKey: "fc-admin-0" + string(rune('0'+i)),
		}); err != nil {
			t.Fatalf("创建测试 Key 失败: %v", err)
		}
	}
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	pool, err := keypool.New(st.UpstreamKeys, clock, keypool.Config{
		DefaultCooldown: time.Minute, FlushInterval: time.Minute,
	})
	if err != nil {
		t.Fatalf("New(pool) 失败: %v", err)
	}
	proxyAuth := auth.NewProxyKeyAuth(st.ProxyKeys)
	session := auth.NewSessionAuth(st.Sessions, testPassword, time.Hour, clock,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	client := firecrawl.NewClient(upstreamURL)
	srv := NewServer(pool, st, proxyAuth, session, client,
		slog.New(slog.NewTextHandler(io.Discard, nil)), clock)
	return srv, st, clock
}

// login 通过面板登录，返回 cookie。
func login(t *testing.T, srv *Server) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/admin/login",
		strings.NewReader(`{"password":"`+testPassword+`"}`))
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Fatalf("登录失败: %d %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("登录未下发 cookie")
	}
	return cookies[0]
}

// authedReq 构造带 session cookie 的请求。
func authedReq(method, path string, body io.Reader, cookie *http.Cookie) *http.Request {
	req := httptest.NewRequest(method, path, body)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return req
}

// ---- 登录与会话 ----

func TestLoginWrongPasswordNoCookie(t *testing.T) {
	srv, _, _ := setupAdmin(t, "http://upstream.invalid", 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/admin/login",
		strings.NewReader(`{"password":"wrong"}`))
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("错误密码期望 401, got %d", rec.Code)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("登录失败不应下发 cookie")
	}
}

func TestLoginBruteForceBackoff(t *testing.T) {
	srv, _, _ := setupAdmin(t, "http://upstream.invalid", 1)
	router := srv.Router()

	// 前 5 次快速失败（阈值内无延迟）。
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/admin/login",
			strings.NewReader(`{"password":"wrong"}`))
		req.RemoteAddr = "10.0.0.1:1234"
		router.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatalf("第 %d 次失败期望 401, got %d", i+1, rec.Code)
		}
	}
	// 第 6 次开始有可观测延迟（约 1 秒起步）。
	start := time.Now()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/admin/login",
		strings.NewReader(`{"password":"wrong"}`))
	req.RemoteAddr = "10.0.0.1:1234"
	router.ServeHTTP(rec, req)
	elapsed := time.Since(start)
	if elapsed < 900*time.Millisecond {
		t.Errorf("第 6 次失败应有递增延迟, 实测 %v", elapsed)
	}
	if rec.Code != 401 {
		t.Errorf("仍应 401, got %d", rec.Code)
	}
}

func TestLoginBackoffUnit(t *testing.T) {
	s, _, _ := setupAdmin(t, "http://upstream.invalid", 1)
	ip := "10.0.0.9"
	if d := s.session.LoginBackoff(ip); d != 0 {
		t.Errorf("无失败记录应无延迟, got %v", d)
	}
	for i := 0; i < 4; i++ {
		s.session.RecordLoginFailure(ip)
	}
	if d := s.session.LoginBackoff(ip); d != 0 {
		t.Errorf("失败 4 次应无延迟, got %v", d)
	}
	s.session.RecordLoginFailure(ip) // 第 5 次
	if d := s.session.LoginBackoff(ip); d != time.Second {
		t.Errorf("失败 5 次应延迟 1 秒, got %v", d)
	}
	s.session.RecordLoginFailure(ip) // 第 6 次
	if d := s.session.LoginBackoff(ip); d != 2*time.Second {
		t.Errorf("失败 6 次应延迟 2 秒, got %v", d)
	}
	// 封顶 30 秒。
	for i := 0; i < 10; i++ {
		s.session.RecordLoginFailure(ip)
	}
	if d := s.session.LoginBackoff(ip); d > 30*time.Second {
		t.Errorf("延迟应封顶 30 秒, got %v", d)
	}
	// 成功登录清零。
	s.session.RecordLoginSuccess(ip)
	if d := s.session.LoginBackoff(ip); d != 0 {
		t.Errorf("成功后应清零, got %v", d)
	}
}

func TestSessionStatus(t *testing.T) {
	srv, _, _ := setupAdmin(t, "http://upstream.invalid", 1)
	router := srv.Router()

	// 未登录。
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/admin/session", nil)
	router.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"authenticated":false`) {
		t.Errorf("未登录应返回 authenticated:false: %s", rec.Body.String())
	}
	// 登录后。
	cookie := login(t, srv)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, authedReq("GET", "/api/admin/session", nil, cookie))
	if !strings.Contains(rec.Body.String(), `"authenticated":true`) {
		t.Errorf("登录后应返回 authenticated:true: %s", rec.Body.String())
	}
}

func TestUnauthenticatedProtectedRoutes(t *testing.T) {
	srv, _, _ := setupAdmin(t, "http://upstream.invalid", 1)
	router := srv.Router()
	paths := []string{
		"GET /api/admin/overview",
		"GET /api/admin/upstream-keys",
		"POST /api/admin/upstream-keys",
		"GET /api/admin/proxy-keys",
		"POST /api/admin/proxy-keys",
	}
	for _, spec := range paths {
		parts := strings.SplitN(spec, " ", 2)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(parts[0], parts[1], nil)
		router.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Errorf("%s 未登录应 401, got %d", spec, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"error":"unauthorized"`) {
			t.Errorf("%s 401 响应体不符合约定: %s", spec, rec.Body.String())
		}
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	srv, _, _ := setupAdmin(t, "http://upstream.invalid", 1)
	router := srv.Router()
	cookie := login(t, srv)

	// 登出。
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, authedReq("POST", "/api/admin/logout", nil, cookie))
	if rec.Code != 204 {
		t.Fatalf("登出期望 204, got %d", rec.Code)
	}
	// 原 cookie 立即失效。
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, authedReq("GET", "/api/admin/overview", nil, cookie))
	if rec.Code != 401 {
		t.Errorf("登出后原 cookie 应 401, got %d", rec.Code)
	}
}

func TestSessionExpiry(t *testing.T) {
	srv, _, clock := setupAdmin(t, "http://upstream.invalid", 1)
	router := srv.Router()
	cookie := login(t, srv)

	clock.Advance(2 * time.Hour) // TTL 1 小时
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, authedReq("GET", "/api/admin/overview", nil, cookie))
	if rec.Code != 401 {
		t.Errorf("过期会话应 401, got %d", rec.Code)
	}
}

// ---- AC8：新增上游 Key 即生效 ----

func TestAC8_CreateUpstreamKeyImmediatelyUsable(t *testing.T) {
	srv, st, _ := setupAdmin(t, "http://upstream.invalid", 1)
	router := srv.Router()
	cookie := login(t, srv)

	rec := httptest.NewRecorder()
	req := authedReq("POST", "/api/admin/upstream-keys",
		strings.NewReader(`{"name":"新账号","api_key":"fc-brand-new-77"}`), cookie)
	router.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("创建期望 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	// 无需重启，池已能选中新 Key。
	key, err := srv.pool.GetByID(2)
	if err != nil || key.APIKey != "fc-brand-new-77" {
		t.Fatalf("新 Key 未进入池: %v", err)
	}
	found := false
	for i := 0; i < 10; i++ {
		if uk, err := srv.pool.Next(); err == nil && uk.ID == 2 {
			found = true
			break
		}
	}
	if !found {
		t.Error("新 Key 从未被选中")
	}
	_ = st
}

// ---- AC11：列表响应无明文 ----

func TestAC11_NoPlaintextInList(t *testing.T) {
	srv, _, _ := setupAdmin(t, "http://upstream.invalid", 2)
	router := srv.Router()
	cookie := login(t, srv)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, authedReq("GET", "/api/admin/upstream-keys", nil, cookie))
	if rec.Code != 200 {
		t.Fatalf("列表期望 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "fc-admin-01") || strings.Contains(body, "fc-admin-02") {
		t.Errorf("列表泄漏上游 Key 明文: %s", body)
	}
	if !strings.Contains(body, "fc-****n-01") || !strings.Contains(body, "fc-****n-02") {
		t.Errorf("列表应含脱敏 Key: %s", body)
	}
}

// ---- PATCH reset ----

func TestPatchResetRecoversExhausted(t *testing.T) {
	srv, st, _ := setupAdmin(t, "http://upstream.invalid", 1)
	router := srv.Router()
	cookie := login(t, srv)

	// 制造 exhausted。
	srv.pool.Report(1, keypool.Outcome{StatusCode: 402})

	rec := httptest.NewRecorder()
	req := authedReq("PATCH", "/api/admin/upstream-keys/1",
		strings.NewReader(`{"reset":true}`), cookie)
	router.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("PATCH 期望 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	uk, _ := st.UpstreamKeys.Get(1)
	if uk.State != store.StateAvailable {
		t.Errorf("reset 后 state = %q, want available", uk.State)
	}
	if uk.LastError != nil {
		t.Errorf("reset 后 last_error 应清空, got %q", *uk.LastError)
	}
}

func TestPatchDisableAndRename(t *testing.T) {
	srv, st, _ := setupAdmin(t, "http://upstream.invalid", 1)
	router := srv.Router()
	cookie := login(t, srv)

	rec := httptest.NewRecorder()
	req := authedReq("PATCH", "/api/admin/upstream-keys/1",
		strings.NewReader(`{"name":"改名","enabled":false}`), cookie)
	router.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("PATCH 期望 200, got %d", rec.Code)
	}
	uk, _ := st.UpstreamKeys.Get(1)
	if uk.Name != "改名" || uk.Enabled {
		t.Errorf("改名/禁用未生效: %+v", uk)
	}
	// 禁用后不再被选中。
	if _, err := srv.pool.Next(); err == nil {
		t.Error("禁用后 Key 不应被选中")
	}
}

// ---- DELETE 级联 job 映射 ----

func TestDeleteUpstreamKeyCascadesJobRoutes(t *testing.T) {
	srv, st, _ := setupAdmin(t, "http://upstream.invalid", 1)
	router := srv.Router()
	cookie := login(t, srv)

	// 手动造一条 job 映射。
	now := time.Now()
	if err := st.JobRoutes.Upsert(&store.JobRoute{
		JobID: "job-1", UpstreamKeyID: 1, Kind: "crawl",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("写入 job 映射失败: %v", err)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, authedReq("DELETE", "/api/admin/upstream-keys/1", nil, cookie))
	if rec.Code != 204 {
		t.Fatalf("DELETE 期望 204, got %d", rec.Code)
	}
	if _, err := st.JobRoutes.Get("job-1"); err == nil {
		t.Error("删除上游 Key 后 job 映射应级联删除")
	}
}

// ---- 代理 Key 接口 ----

func TestProxyKeyCreateThenListNoPlaintext(t *testing.T) {
	srv, _, _ := setupAdmin(t, "http://upstream.invalid", 1)
	router := srv.Router()
	cookie := login(t, srv)

	// 创建：响应含 plaintext_key。
	rec := httptest.NewRecorder()
	req := authedReq("POST", "/api/admin/proxy-keys", strings.NewReader(`{"name":"脚本A"}`), cookie)
	router.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("创建期望 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID           int64  `json:"id"`
		PlaintextKey string `json:"plaintext_key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("解析创建响应失败: %v", err)
	}
	if !strings.HasPrefix(created.PlaintextKey, "fcp_") {
		t.Errorf("明文格式不符: %q", created.PlaintextKey)
	}

	// 列表：不含明文。
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, authedReq("GET", "/api/admin/proxy-keys", nil, cookie))
	if strings.Contains(rec.Body.String(), created.PlaintextKey) {
		t.Errorf("列表泄漏代理 Key 明文: %s", rec.Body.String())
	}

	// 吊销。
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, authedReq("DELETE", "/api/admin/proxy-keys/"+strconv.FormatInt(created.ID, 10), nil, cookie))
	if rec.Code != 204 {
		t.Fatalf("吊销期望 204, got %d", rec.Code)
	}
}

// ---- 额度拉取 ----

func TestRefreshCredits(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/team/credit-usage" {
			t.Errorf("路径应为 /team/credit-usage, got %s", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer fc-admin-") {
			t.Errorf("应带上游 Key 认证, got %q", r.Header.Get("Authorization"))
		}
		io.WriteString(w, `{"credits_total":100,"credits_used":20,"credits_remaining":80}`)
	}))
	defer up.Close()

	srv, st, _ := setupAdmin(t, up.URL, 1)
	router := srv.Router()
	cookie := login(t, srv)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, authedReq("POST", "/api/admin/upstream-keys/1/refresh-credits", nil, cookie))
	if rec.Code != 200 {
		t.Fatalf("刷新期望 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"credits_remaining":80`) {
		t.Errorf("刷新响应应含最新额度: %s", rec.Body.String())
	}
	// 池与 DB 均已写回。
	uk, _ := st.UpstreamKeys.Get(1)
	if uk.CreditsRemaining == nil || *uk.CreditsRemaining != 80 {
		t.Errorf("credits_remaining 未写回: %+v", uk.CreditsRemaining)
	}
	if uk.CreditsTotal == nil || *uk.CreditsTotal != 100 {
		t.Errorf("credits_total 未写回: %+v", uk.CreditsTotal)
	}
}

func TestRefreshCreditsFailureKeepsState(t *testing.T) {
	// 假上游对额度接口返回 401（Key 无效）。
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, `{"error":"unauthorized"}`)
	}))
	defer up.Close()

	srv, st, _ := setupAdmin(t, up.URL, 1)
	router := srv.Router()
	cookie := login(t, srv)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, authedReq("POST", "/api/admin/upstream-keys/1/refresh-credits", nil, cookie))
	if rec.Code != 502 {
		t.Fatalf("上游 401 时刷新应 502, got %d", rec.Code)
	}
	// Key 状态不被改变（仍是 available）。
	uk, _ := st.UpstreamKeys.Get(1)
	if uk.State != store.StateAvailable {
		t.Errorf("额度拉取失败不应改变状态, got %q", uk.State)
	}
}

// ---- overview ----

func TestOverview(t *testing.T) {
	srv, st, _ := setupAdmin(t, "http://upstream.invalid", 3)
	router := srv.Router()
	cookie := login(t, srv)

	// 造数据：1 exhausted，2 有额度，3 禁用。
	srv.pool.Report(1, keypool.Outcome{StatusCode: 402})
	srv.pool.SetCredits(2, 500, 300)
	uk3, _ := st.UpstreamKeys.Get(3)
	uk3.Enabled = false
	st.UpstreamKeys.Update(uk3)
	srv.pool.Reload()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, authedReq("GET", "/api/admin/overview", nil, cookie))
	if rec.Code != 200 {
		t.Fatalf("overview 期望 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"credits_remaining_sum":300`) {
		t.Errorf("剩余额度求和不符: %s", body)
	}
	if !strings.Contains(body, `"credits_total_sum":500`) {
		t.Errorf("总额度求和不符: %s", body)
	}
	if !strings.Contains(body, `"exhausted":1`) {
		t.Errorf("状态计数不符: %s", body)
	}
	if !strings.Contains(body, `"disabled":1`) {
		t.Errorf("禁用计数不符: %s", body)
	}
}

// ---- 全栈：AC13 / AC14 / SPA 兜底 ----

// setupFullStack 按 main.go 的装配方式组装完整 mux：
// healthz + admin + 代理前缀(带代理 Key 认证) + SPA 兜底。
func setupFullStack(t *testing.T) (http.Handler, *httptest.Server, *store.Store) {
	t.Helper()
	// 假 Firecrawl 上游：任意路径都返回 200。
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"success":true}`)
	}))
	t.Cleanup(up.Close)

	db, err := store.Open(filepath.Join(t.TempDir(), "full.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.NewStore(db)
	st.UpstreamKeys.Create(&store.UpstreamKey{Name: "up", APIKey: "fc-full-7777"})

	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	pool, _ := keypool.New(st.UpstreamKeys, clock, keypool.Config{
		DefaultCooldown: time.Minute, FlushInterval: time.Minute,
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	proxyHandler, err := proxy.NewHandler(pool, st.JobRoutes, logger, proxy.Config{
		UpstreamBaseURL: up.URL, PublicBaseURL: "https://fc.example.com",
		PathPrefixes: []string{"/v1/", "/v2/"}, MaxAttempts: 3,
		MaxRequestBuffer: 1 << 20, JobTTL: time.Hour, Clock: clock,
	})
	if err != nil {
		t.Fatalf("New(proxy) 失败: %v", err)
	}
	proxyAuth := auth.NewProxyKeyAuth(st.ProxyKeys)
	session := auth.NewSessionAuth(st.Sessions, testPassword, time.Hour, clock, logger)
	client := firecrawl.NewClient(up.URL)
	srv := NewServer(pool, st, proxyAuth, session, client, logger, clock)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		io.WriteString(w, `{"status":"ok"}`)
	})
	mux.Handle("/api/admin/", srv.Router())
	authedProxy := proxyAuth.Middleware(proxyHandler)
	for _, p := range []string{"/v1/", "/v2/"} {
		mux.Handle(p, authedProxy)
	}
	mux.Handle("/", webui.Handler())
	return mux, up, st
}

func TestAC13_AuthIsolation(t *testing.T) {
	mux, up, st := setupFullStack(t)

	// 签发一个代理 Key。
	proxyToken, _, _ := auth.NewProxyKeyAuth(st.ProxyKeys).Issue("测试")

	// ① 代理 Key 访问面板 → 401。
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/admin/upstream-keys", nil)
	req.Header.Set("Authorization", "Bearer "+proxyToken)
	mux.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("代理 Key 访问面板应 401, got %d", rec.Code)
	}

	// ② 面板 session cookie 访问代理路径 → 401。
	loginRec := httptest.NewRecorder()
	loginReq := httptest.NewRequest("POST", "/api/admin/login",
		strings.NewReader(`{"password":"`+testPassword+`"}`))
	mux.ServeHTTP(loginRec, loginReq)
	cookie := loginRec.Result().Cookies()[0]

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/v2/scrape", strings.NewReader(`{"url":"https://x.com"}`))
	req.AddCookie(cookie)
	mux.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("session cookie 访问代理应 401, got %d", rec.Code)
	}

	// ③ 代理 Key 访问代理 → 200（对照组）。
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/v2/scrape", strings.NewReader(`{"url":"https://x.com"}`))
	req.Header.Set("Authorization", "Bearer "+proxyToken)
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("代理 Key 访问代理应 200, got %d", rec.Code)
	}
	_ = up
}

func TestAC14_HealthzNoAuth(t *testing.T) {
	mux, _, _ := setupFullStack(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/healthz", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("/healthz 应无需认证 200, got %d", rec.Code)
	}
}

func TestSPAFallback(t *testing.T) {
	mux, _, _ := setupFullStack(t)

	// 前端路由路径 → index.html。
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/keys", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "index.html") &&
		!strings.Contains(rec.Body.String(), "Firecrawl 管理面板") {
		t.Errorf("/keys 应回退 index.html, got %d %s", rec.Code, rec.Body.String())
	}

	// 代理前缀未匹配路由 → 404 而非 HTML（SPA 兜底不得吃掉 API 404）。
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/v2/unknown-endpoint", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code == 200 || strings.Contains(rec.Body.String(), "index.html") {
		t.Errorf("/v2/unknown 不应返回 HTML, got %d %s", rec.Code, rec.Body.String())
	}

	// API 前缀未匹配路由 → 404 而非 HTML。
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/unknown", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code == 200 || strings.Contains(rec.Body.String(), "index.html") {
		t.Errorf("/api/unknown 不应返回 HTML, got %d %s", rec.Code, rec.Body.String())
	}
}
