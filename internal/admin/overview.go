package admin

import (
	"net/http"
	"time"
)

// handleOverview 返回额度池汇总与 Key 状态摘要。
// 数据全部取自 keypool.Snapshot() 与仓储，不额外查上游。
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	keys, counts := s.pool.Snapshot()

	var (
		sumRemaining  int64
		sumTotal      int64
		disabled      int
		lastRefreshed *time.Time
	)
	for _, ks := range keys {
		k := ks.Key
		if !k.Enabled {
			disabled++
			continue // 禁用的 Key 不计入额度池
		}
		if k.CreditsRemaining != nil {
			sumRemaining += *k.CreditsRemaining
		}
		if k.CreditsTotal != nil {
			sumTotal += *k.CreditsTotal
		}
		if k.CreditsSyncedAt != nil &&
			(lastRefreshed == nil || k.CreditsSyncedAt.After(*lastRefreshed)) {
			t := *k.CreditsSyncedAt
			lastRefreshed = &t
		}
	}

	keyCounts := map[string]int{}
	for state, n := range counts {
		keyCounts[string(state)] = n
	}
	keyCounts["disabled"] = disabled

	proxyKeys, err := s.st.ProxyKeys.List()
	if err != nil {
		s.logger.Warn("查询代理 Key 列表失败", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal_error", "查询失败")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"credits_remaining_sum": sumRemaining,
		"credits_total_sum":     sumTotal,
		"key_counts":            keyCounts,
		"proxy_key_count":       len(proxyKeys),
		"last_refreshed_at":     lastRefreshed,
	})
}
