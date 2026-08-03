package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// registerServer 组装带注册 token 的面板 Server（复用 setupAdmin 的公共装配）。
func registerServer(t *testing.T, upstreamURL string, token string) *Server {
	t.Helper()
	srv, _, _ := setupAdmin(t, upstreamURL, 1)
	srv.registerToken = token
	return srv
}

func registerReq(method, path, body, token string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	if token != "" {
		r.Header.Set("X-Register-Token", token)
	}
	return r
}

// 未配置 REGISTER_TOKEN：接口返回 503，不落库。
func TestRegisterKeysNotEnabled(t *testing.T) {
	srv := registerServer(t, "", "")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, registerReq("POST", "/api/register/keys",
		`{"name":"auto-a","api_key":"fc-register-test-001"}`, "any-token"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("未配置 token 应返回 503, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if resp["error"] != "register_not_enabled" {
		t.Errorf("error 应为 register_not_enabled, got %q", resp["error"])
	}
}

// token 缺失或错误：401。
func TestRegisterKeysUnauthorized(t *testing.T) {
	srv := registerServer(t, "", "correct-token")
	for name, token := range map[string]string{
		"缺失":   "",
		"错误":   "wrong-token",
		"部分匹配": "correct-tok",
	} {
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, registerReq("POST", "/api/register/keys",
			`{"name":"auto-a","api_key":"fc-register-test-001"}`, token))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s token 应返回 401, got %d", name, rec.Code)
		}
	}
}

// 正确 token：201 + masked 响应 + Key 已入池（池快照可见）。
func TestRegisterKeysCreated(t *testing.T) {
	srv, st, _ := setupAdmin(t, "", 1)
	srv.registerToken = "correct-token"

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, registerReq("POST", "/api/register/keys",
		`{"name":"auto-abc","api_key":"fc-register-test-001"}`, "correct-token"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("正确 token 应返回 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	// 响应是 masked DTO，绝不回明文 key。
	body := rec.Body.String()
	if strings.Contains(body, "fc-register-test-001") {
		t.Errorf("响应泄漏了上游 Key 明文: %s", body)
	}
	if !strings.Contains(body, "fc-****") {
		t.Errorf("响应应包含 masked key, got %s", body)
	}

	// Key 已入库并参与调度（池快照可见，名字对得上）。
	keys, _ := srv.pool.Snapshot()
	found := false
	for _, ks := range keys {
		if ks.Key.APIKey == "fc-register-test-001" && ks.Key.Name == "auto-abc" {
			found = true
		}
	}
	if !found {
		t.Error("新 Key 未出现在池快照中（未入池或未 Reload）")
	}
	_ = st
}

// 重复 Key：409，且不覆盖已有记录。
func TestRegisterKeysDuplicate(t *testing.T) {
	srv, _, _ := setupAdmin(t, "", 1)
	srv.registerToken = "correct-token"

	// 先经注册接口录入一次。
	srv.Router().ServeHTTP(httptest.NewRecorder(), registerReq("POST", "/api/register/keys",
		`{"name":"auto-abc","api_key":"fc-register-test-002"}`, "correct-token"))

	// 再录同一个 key：409。
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, registerReq("POST", "/api/register/keys",
		`{"name":"auto-abc","api_key":"fc-register-test-002"}`, "correct-token"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("重复 key 应返回 409, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// 空 body / 缺字段：400。
func TestRegisterKeysInvalidBody(t *testing.T) {
	srv := registerServer(t, "", "correct-token")
	for name, body := range map[string]string{
		"空 body":  "",
		"缺 api_key": `{"name":"auto-a"}`,
		"缺 name":  `{"api_key":"fc-register-test-003"}`,
	} {
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, registerReq("POST", "/api/register/keys", body, "correct-token"))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s 应返回 400, got %d", name, rec.Code)
		}
	}
}

// 注册接口不经过 SessionAuth：无 cookie 也能访问（token 有效时）。
func TestRegisterKeysNoSessionNeeded(t *testing.T) {
	srv := registerServer(t, "", "correct-token")
	rec := httptest.NewRecorder()
	req := registerReq("POST", "/api/register/keys",
		`{"name":"auto-a","api_key":"fc-register-test-004"}`, "correct-token")
	// 显式不带任何 cookie。
	req.Header.Del("Cookie")
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("注册接口不应依赖 session cookie, got %d body=%s", rec.Code, rec.Body.String())
	}
}
