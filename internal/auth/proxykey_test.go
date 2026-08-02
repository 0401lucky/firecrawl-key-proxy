package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"firecrawl-proxy/internal/store"
)

func setupAuth(t *testing.T) (*ProxyKeyAuth, *store.Store) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.NewStore(db)
	return NewProxyKeyAuth(st.ProxyKeys), st
}

// serve 通过中间件发送一次请求，返回响应。
func serve(t *testing.T, mw *ProxyKeyAuth, token string) *httptest.ResponseRecorder {
	t.Helper()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := ProxyKeyNameFrom(r.Context())
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "ok:"+name)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v2/scrape", nil)
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	mw.Middleware(next).ServeHTTP(rec, req)
	return rec
}

// ---- AC9：明文不入库，哈希正确 ----

func TestIssueHashesAndStoresHashOnly(t *testing.T) {
	a, st := setupAuth(t)

	plaintext, rec, err := a.Issue("调用方 A")
	if err != nil {
		t.Fatalf("Issue() 失败: %v", err)
	}
	if !strings.HasPrefix(plaintext, "fcp_") {
		t.Errorf("明文应以 fcp_ 开头: %q", plaintext)
	}
	if len(plaintext) != 4+43 { // fcp_ + 32 字节 base64url
		t.Errorf("明文长度 = %d, want %d", len(plaintext), 4+43)
	}
	if rec.KeyPrefix != plaintext[:12] {
		t.Errorf("key_prefix = %q, want %q", rec.KeyPrefix, plaintext[:12])
	}

	// 库中不得出现明文。
	list, _ := st.ProxyKeys.List()
	for _, pk := range list {
		if strings.Contains(pk.Name, plaintext) || strings.Contains(pk.KeyHash, plaintext) {
			t.Errorf("库中出现明文相关数据")
		}
		if strings.Contains(plaintext, pk.KeyHash) {
			t.Errorf("key_hash 不应是明文子串")
		}
	}
	// key_hash 必须等于手工计算的 SHA-256 十六进制。
	want := sha256.Sum256([]byte(plaintext))
	if rec.KeyHash != hex.EncodeToString(want[:]) {
		t.Errorf("key_hash 与 SHA-256 不符: %q", rec.KeyHash)
	}
	// 用哈希能查回记录。
	got, err := st.ProxyKeys.FindByHash(rec.KeyHash)
	if err != nil || got.Name != "调用方 A" {
		t.Errorf("按哈希查回失败: %v %+v", err, got)
	}
	// 明文本身不是任何一条记录的 key_hash（明文未被当成哈希入库）。
	if _, err := st.ProxyKeys.FindByHash(plaintext); err == nil {
		t.Error("明文不应能被当作 key_hash 查到")
	}
}

func TestIssueProducesDistinctTokens(t *testing.T) {
	a, _ := setupAuth(t)
	p1, _, _ := a.Issue("a")
	p2, _, _ := a.Issue("b")
	if p1 == p2 {
		t.Error("两次 Issue() 产生相同明文")
	}
}

// ---- AC7：三种失败情形响应体一致 ----

func TestMiddlewareUnauthorized(t *testing.T) {
	a, _ := setupAuth(t)
	// 签发一个后吊销，作为第四种情形（用真实明文验证「已吊销」分支）。
	revokedToken, rec, _ := a.Issue("待吊销")
	a.Revoke(rec.ID)

	cases := []struct {
		name  string
		token string // 空 = 不带 Authorization 头
	}{
		{"无 Authorization 头", ""},
		{"格式错误（非 Bearer）", "Token abc"},
		{"token 不存在", "Bearer fcp_doesnotexist"},
		{"token 已吊销", "Bearer " + revokedToken},
	}
	var bodies []string
	for _, c := range cases {
		resp := serve(t, a, c.token)
		if resp.Code != 401 {
			t.Errorf("%s: 期望 401, got %d", c.name, resp.Code)
		}
		bodies = append(bodies, resp.Body.String())
	}
	// 四个分支的响应体必须完全一致，不泄露失败原因差异。
	for i := 1; i < len(bodies); i++ {
		if bodies[i] != bodies[0] {
			t.Errorf("失败响应体不一致:\n[0]=%s\n[%d]=%s", bodies[0], i, bodies[i])
		}
	}
}

// TestMiddlewareRevokedTokenMatchesAny 用真正的吊销 token 验证 401。
func TestMiddlewareRevokedToken(t *testing.T) {
	a, _ := setupAuth(t)
	plaintext, rec, _ := a.Issue("吊销测试")
	a.Revoke(rec.ID)

	resp := serve(t, a, "Bearer "+plaintext)
	if resp.Code != 401 {
		t.Fatalf("吊销后期望 401, got %d", resp.Code)
	}
}

// ---- 成功路径与 context 注入 ----

func TestMiddlewareSuccess(t *testing.T) {
	a, _ := setupAuth(t)
	plaintext, _, _ := a.Issue("成功调用方")

	resp := serve(t, a, "Bearer "+plaintext)
	if resp.Code != 200 {
		t.Fatalf("期望 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	// context 中应能取到名称（日志用）。
	if resp.Body.String() != "ok:成功调用方" {
		t.Errorf("context 身份未注入: %q", resp.Body.String())
	}
}

// ---- cookie 无法通过校验（AC13 的一半）----

func TestMiddlewareIgnoresCookie(t *testing.T) {
	a, _ := setupAuth(t)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v2/scrape", nil)
	req.Header.Set("Cookie", "session=anything")
	// 只有 cookie、无 Authorization → 401（session cookie 不经过本中间件校验）。
	a.Middleware(next).ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("仅带 cookie 的请求应 401, got %d", rec.Code)
	}
}

// ---- Revoke 立即生效 ----

func TestRevokeImmediate(t *testing.T) {
	a, _ := setupAuth(t)
	plaintext, rec, _ := a.Issue("立即生效测试")

	if resp := serve(t, a, "Bearer "+plaintext); resp.Code != 200 {
		t.Fatalf("吊销前应 200, got %d", resp.Code)
	}
	if err := a.Revoke(rec.ID); err != nil {
		t.Fatalf("Revoke() 失败: %v", err)
	}
	if resp := serve(t, a, "Bearer "+plaintext); resp.Code != 401 {
		t.Fatalf("吊销后应立即 401, got %d", resp.Code)
	}
}

// ---- 计数与刷盘 ----

func TestUsageCounting(t *testing.T) {
	a, st := setupAuth(t)
	plaintext, rec, _ := a.Issue("计数测试")

	for i := 0; i < 5; i++ {
		if resp := serve(t, a, "Bearer "+plaintext); resp.Code != 200 {
			t.Fatalf("第 %d 次请求应 200, got %d", i, resp.Code)
		}
	}
	// 刷盘前 DB 尚未更新（内存缓冲）。
	got, _ := st.ProxyKeys.FindByHash(rec.KeyHash)
	if got.RequestCount != 0 {
		t.Errorf("刷盘前 request_count 应为 0（内存缓冲）, got %d", got.RequestCount)
	}
	if err := a.Flush(); err != nil {
		t.Fatalf("Flush() 失败: %v", err)
	}
	got, _ = st.ProxyKeys.FindByHash(rec.KeyHash)
	if got.RequestCount != 5 {
		t.Errorf("刷盘后 request_count = %d, want 5", got.RequestCount)
	}
	if got.LastUsedAt == nil {
		t.Error("last_used_at 应被更新")
	}
}

// ---- 并发 ----

func TestConcurrentAuthAndUsage(t *testing.T) {
	a, st := setupAuth(t)
	plaintext, rec, _ := a.Issue("并发")

	var wg sync.WaitGroup
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				resp := serve(t, a, "Bearer "+plaintext)
				if resp.Code != 200 {
					t.Errorf("并发请求应 200, got %d", resp.Code)
				}
			}
		}()
	}
	wg.Wait()

	if err := a.Flush(); err != nil {
		t.Fatalf("Flush() 失败: %v", err)
	}
	got, _ := st.ProxyKeys.FindByHash(rec.KeyHash)
	if got.RequestCount != 500 {
		t.Errorf("总计数 = %d, want 500", got.RequestCount)
	}
}
