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

// CreditUsage 是 /team/credit-usage 的响应。
type CreditUsage struct {
	Total     int64 `json:"credits_total"`
	Used      int64 `json:"credits_used"`
	Remaining int64 `json:"credits_remaining"`
}

// GetCreditUsage 查询指定上游 Key 的额度。仅用于面板展示，绝不参与调度决策。
func (c *Client) GetCreditUsage(ctx context.Context, apiKey string) (CreditUsage, error) {
	var usage CreditUsage
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/team/credit-usage", nil)
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
		return usage, fmt.Errorf("额度接口返回 %d: %s", resp.StatusCode, string(body))
	}
	if err := json.Unmarshal(body, &usage); err != nil {
		return usage, fmt.Errorf("解析额度响应失败: %w", err)
	}
	return usage, nil
}
