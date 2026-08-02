// Package firecrawl 是代理「作为客户端」主动调用 Firecrawl 的 API 客户端。
//
// 与 internal/proxy 的转发逻辑完全独立：转发是替用户请求经 Key 池调度，
// 这里是面板额度展示的主动拉取。两边的超时、重试、错误处理各不相同，
// 共用代码只会把逻辑纠缠在一起。当前只提供额度查询，未来需要时可以扩展。
package firecrawl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client 调用 Firecrawl 的只读接口。
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient 构造客户端。
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// CreditUsage 是额度查询的结果。
type CreditUsage struct {
	Total     int64 // 套餐额度
	Remaining int64 // 剩余额度
}

// APIError 表示上游返回了非 2xx。带状态码，供调用方给出可操作的提示
// （401=Key 无效、402=额度耗尽、429=限流，语义完全不同）。
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("额度接口返回 %d: %s", e.StatusCode, e.Body)
}

// creditUsageResponse 覆盖 Firecrawl 实际返回的几种形状。
//
// 官方文档写的是扁平的 {credits_total, credits_used, credits_remaining}，
// 但实测 v1 与 v2 都套了 data 外层，且字段命名不一致：
//
//	v2: {"success":true,"data":{"remainingCredits":1025,"planCredits":1000,...}}
//	v1: {"success":true,"data":{"remaining_credits":1025,"plan_credits":1000,...}}
//
// 三种形状全部兼容，避免上游改版或换 API 版本时又静默失效。
type creditUsageResponse struct {
	Data *struct {
		RemainingCredits     *int64 `json:"remainingCredits"`
		PlanCredits          *int64 `json:"planCredits"`
		RemainingCreditsSnake *int64 `json:"remaining_credits"`
		PlanCreditsSnake     *int64 `json:"plan_credits"`
	} `json:"data"`
	// 文档描述的扁平形状
	CreditsTotal     *int64 `json:"credits_total"`
	CreditsRemaining *int64 `json:"credits_remaining"`
}

func (r creditUsageResponse) toUsage() (CreditUsage, bool) {
	pick := func(vals ...*int64) (int64, bool) {
		for _, v := range vals {
			if v != nil {
				return *v, true
			}
		}
		return 0, false
	}
	if r.Data != nil {
		remaining, ok := pick(r.Data.RemainingCredits, r.Data.RemainingCreditsSnake)
		if ok {
			total, _ := pick(r.Data.PlanCredits, r.Data.PlanCreditsSnake)
			return CreditUsage{Total: total, Remaining: remaining}, true
		}
	}
	if remaining, ok := pick(r.CreditsRemaining); ok {
		total, _ := pick(r.CreditsTotal)
		return CreditUsage{Total: total, Remaining: remaining}, true
	}
	return CreditUsage{}, false
}

// GetCreditUsage 查询指定上游 Key 的额度。
//
// 路径必须带版本前缀：不带前缀的 /team/credit-usage 会 404
// （Cannot GET /team/credit-usage），官方文档省略了 base URL 中的版本段。
//
// 这个调用不消耗 credits，因此也可以安全地当作「这个 Key 还能不能用」的探测手段——
// 比发一次真实 scrape 去试要省一个额度。仅用于面板展示与探测，绝不参与调度决策。
func (c *Client) GetCreditUsage(ctx context.Context, apiKey string) (CreditUsage, error) {
	var usage CreditUsage
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v2/team/credit-usage", nil)
	if err != nil {
		return usage, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return usage, fmt.Errorf("请求额度接口失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return usage, fmt.Errorf("读取额度响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		preview := string(body)
		if len(preview) > 200 {
			preview = preview[:200]
		}
		return usage, &APIError{StatusCode: resp.StatusCode, Body: preview}
	}

	var parsed creditUsageResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return usage, fmt.Errorf("解析额度响应失败: %w", err)
	}
	usage, ok := parsed.toUsage()
	if !ok {
		return usage, fmt.Errorf("额度响应中找不到剩余额度字段: %s", string(body))
	}
	return usage, nil
}
