package firecrawl

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 回归：额度接口的路径必须带版本前缀。
// 曾经用的是 /team/credit-usage，上游返回 404「Cannot GET /team/credit-usage」，
// 面板「刷新额度」因此恒为 502——官方文档省略了 base URL 中的版本段。
func TestCreditUsagePathHasVersionPrefix(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"data":{"remainingCredits":1025,"planCredits":1000}}`))
	}))
	defer srv.Close()

	u, err := NewClient(srv.URL).GetCreditUsage(context.Background(), "fc-test-key")
	if err != nil {
		t.Fatalf("不该失败: %v", err)
	}
	if gotPath != "/v2/team/credit-usage" {
		t.Errorf("路径应带版本前缀, got %s", gotPath)
	}
	if gotAuth != "Bearer fc-test-key" {
		t.Errorf("Authorization 头不对: %s", gotAuth)
	}
	if u.Remaining != 1025 || u.Total != 1000 {
		t.Errorf("解析错误: %+v", u)
	}
}

// 回归：三种响应形状都要能解析。
// 官方文档写的是扁平结构，但实测 v1/v2 都套了 data 外层且字段命名不同；
// 只认其中一种，换 API 版本就会静默拿不到余额。
func TestCreditUsageParsesAllShapes(t *testing.T) {
	cases := []struct {
		name              string
		body              string
		remaining, total  int64
	}{
		{
			"v2 camelCase（实测）",
			`{"success":true,"data":{"remainingCredits":1025,"planCredits":1000,"billingPeriodEnd":"2026-09-02T15:55:50.971Z"}}`,
			1025, 1000,
		},
		{
			"v1 snake_case（实测）",
			`{"success":true,"data":{"remaining_credits":432,"plan_credits":500}}`,
			432, 500,
		},
		{
			"文档描述的扁平结构",
			`{"credits_total":10000,"credits_used":2500,"credits_remaining":7500}`,
			7500, 10000,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(c.body))
			}))
			defer srv.Close()

			u, err := NewClient(srv.URL).GetCreditUsage(context.Background(), "k")
			if err != nil {
				t.Fatalf("不该失败: %v", err)
			}
			if u.Remaining != c.remaining || u.Total != c.total {
				t.Errorf("期望 remaining=%d total=%d, got %+v", c.remaining, c.total, u)
			}
		})
	}
}

// 非 2xx 必须返回带状态码的 APIError，调用方才能区分
// 「Key 无效」「额度耗尽」「限流」——这三者的处置完全不同。
func TestCreditUsageReturnsTypedAPIError(t *testing.T) {
	for _, code := range []int{401, 402, 429, 500} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			w.Write([]byte(`{"success":false,"error":"nope"}`))
		}))
		_, err := NewClient(srv.URL).GetCreditUsage(context.Background(), "k")
		srv.Close()

		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("状态码 %d 应返回 *APIError, got %T: %v", code, err, err)
		}
		if apiErr.StatusCode != code {
			t.Errorf("状态码应为 %d, got %d", code, apiErr.StatusCode)
		}
	}
}

// 响应里没有余额字段时必须报错，而不是静默当成 0——
// 0 会在面板上表现为「额度耗尽」，是个会误导人的假象。
func TestCreditUsageRejectsUnknownShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"data":{"somethingElse":1}}`))
	}))
	defer srv.Close()

	if _, err := NewClient(srv.URL).GetCreditUsage(context.Background(), "k"); err == nil {
		t.Fatal("未知结构应报错，不能静默返回 0")
	}
}
