package proxy

import "firecrawl-proxy/internal/keypool"

// shouldRetry 决定「要不要换一个 Key 重试同一请求」。
//
// 它与 keypool.classify（决定「这个 Key 该不该被惩罚」）是两个独立的判断，
// 绝不能合并成一个布尔值：
//
//   - 401/402/403/429：要重试，且要惩罚（这个 Key 确实有问题）。
//   - 408/5xx/网络错误：要重试，但不惩罚（Firecrawl 自身故障，不是 Key 的错）。
//     写成同一个函数会必然导致：上游抖动一次，所有 Key 依次被误判为失效。
//
// 2xx 与其他 4xx（400、404 等）是客户端自己的问题，重试毫无意义，直接透传。
func shouldRetry(o keypool.Outcome) bool {
	if o.Err != nil {
		return true
	}
	switch o.StatusCode {
	case 401, 402, 403, 408, 429:
		return true
	default:
		return o.StatusCode >= 500
	}
}
