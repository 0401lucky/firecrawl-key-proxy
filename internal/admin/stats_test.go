package admin

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"firecrawl-proxy/internal/store"
)

// TestStatsEndpoint 验证 /api/admin/stats 的响应形状与窗口语义：
// 窗口内总量、成功率、series 补齐空小时（24h 正好 24 点）、按 Key 分布。
func TestStatsEndpoint(t *testing.T) {
	srv, st, clock := setupAdmin(t, "http://upstream.invalid", 2)
	cookie := login(t, srv)

	// 直接播种统计桶（绕过 pool，聚焦 API 层）。
	nowHour := clock.Now().Truncate(time.Hour).Unix()
	if err := st.CallStats.Increment([]store.CallStat{
		{Hour: nowHour, UpstreamKeyID: 1, StatusClass: store.StatusClass2xx, Calls: 8},
		{Hour: nowHour, UpstreamKeyID: 2, StatusClass: store.StatusClass4xx, Calls: 2},
		{Hour: nowHour - 3600, UpstreamKeyID: 1, StatusClass: store.StatusClass2xx, Calls: 5},
	}); err != nil {
		t.Fatalf("播种统计失败: %v", err)
	}

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec,
		authedReq("GET", "/api/admin/stats?window=24h", nil, cookie))
	if rec.Code != 200 {
		t.Fatalf("stats 期望 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		Window      string             `json:"window"`
		TotalCalls  int64              `json:"total_calls"`
		SuccessRate float64            `json:"success_rate"`
		Series      []statsSeriesPoint `json:"series"`
		PerKey      []statsPerKey      `json:"per_key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	if body.Window != "24h" {
		t.Errorf("window = %q, want 24h", body.Window)
	}
	if body.TotalCalls != 15 {
		t.Errorf("total_calls = %d, want 15", body.TotalCalls)
	}
	// 13 个 2xx / 15 总 = 0.8666 → 保留 3 位 0.867。
	if body.SuccessRate != 0.867 {
		t.Errorf("success_rate = %v, want 0.867", body.SuccessRate)
	}
	if len(body.Series) != 24 {
		t.Fatalf("series 点数 = %d, want 24（24h 窗口，空小时补零）", len(body.Series))
	}
	// 最后两点：上一小时 5 次成功，当前小时 10 次（8 成功 + 2 错误）。
	last := body.Series[len(body.Series)-1]
	if last.Calls != 10 || last.Errors != 2 {
		t.Errorf("当前小时点 = calls %d errors %d, want 10/2", last.Calls, last.Errors)
	}
	prev := body.Series[len(body.Series)-2]
	if prev.Calls != 5 || prev.Errors != 0 {
		t.Errorf("上一小时点 = calls %d errors %d, want 5/0", prev.Calls, prev.Errors)
	}

	if len(body.PerKey) != 2 {
		t.Fatalf("per_key 行数 = %d, want 2", len(body.PerKey))
	}
	// 按次数降序：key1=13（share 0.867），key2=2（share 0.133）。
	if body.PerKey[0].KeyID != 1 || body.PerKey[0].Calls != 13 {
		t.Errorf("per_key[0] = key %d calls %d, want key 1 calls 13", body.PerKey[0].KeyID, body.PerKey[0].Calls)
	}
	if body.PerKey[0].Share != 0.867 {
		t.Errorf("per_key[0].share = %v, want 0.867", body.PerKey[0].Share)
	}
	if body.PerKey[1].KeyID != 2 || body.PerKey[1].Share != 0.133 {
		t.Errorf("per_key[1] = key %d share %v, want key 2 share 0.133", body.PerKey[1].KeyID, body.PerKey[1].Share)
	}
}

// 无数据时返回零值而非报错：series 仍为 24 个空点。
func TestStatsEndpointEmpty(t *testing.T) {
	srv, _, _ := setupAdmin(t, "http://upstream.invalid", 1)
	cookie := login(t, srv)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec,
		authedReq("GET", "/api/admin/stats?window=24h", nil, cookie))
	if rec.Code != 200 {
		t.Fatalf("空数据 stats 期望 200, got %d", rec.Code)
	}
	var body struct {
		TotalCalls int64              `json:"total_calls"`
		Series     []statsSeriesPoint `json:"series"`
		PerKey     []statsPerKey      `json:"per_key"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.TotalCalls != 0 {
		t.Errorf("空数据 total_calls = %d, want 0", body.TotalCalls)
	}
	if len(body.Series) != 24 {
		t.Errorf("空数据 series 点数 = %d, want 24", len(body.Series))
	}
	if len(body.PerKey) != 0 {
		t.Errorf("空数据 per_key = %+v, want 空", body.PerKey)
	}
}

// 非法 window 返回 400。
func TestStatsInvalidWindow(t *testing.T) {
	srv, _, _ := setupAdmin(t, "http://upstream.invalid", 1)
	cookie := login(t, srv)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec,
		authedReq("GET", "/api/admin/stats?window=1y", nil, cookie))
	if rec.Code != 400 {
		t.Fatalf("非法 window 期望 400, got %d", rec.Code)
	}
}

// stats 属于受保护路由：无会话 cookie → 401。
func TestStatsRequiresSession(t *testing.T) {
	srv, _, _ := setupAdmin(t, "http://upstream.invalid", 1)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec,
		authedReq("GET", "/api/admin/stats?window=24h", nil, nil))
	if rec.Code != 401 {
		t.Fatalf("无 cookie 期望 401, got %d", rec.Code)
	}
}
