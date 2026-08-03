package admin

import (
	"math"
	"net/http"
	"time"

	"firecrawl-proxy/internal/store"
)

// 支持的统计窗口。series 永远按小时返回，日粒度聚合在前端做。
var statsWindows = map[string]time.Duration{
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

// statsSeriesPoint 是趋势图的一个数据点：某个小时桶的总调用与非 2xx 数。
type statsSeriesPoint struct {
	TS     int64 `json:"ts"`     // unix 秒，小时起点
	Calls  int64 `json:"calls"`  // 该小时总调用
	Errors int64 `json:"errors"` // 该小时非 2xx 调用
}

// statsPerKey 是「按上游 Key 分布」的一行。
type statsPerKey struct {
	KeyID int64   `json:"key_id"`
	Calls int64   `json:"calls"`
	Share float64 `json:"share"` // 0~1，占窗口总调用比例（保留 3 位小数）
}

// handleStats 返回调用统计：窗口内总调用、成功率、逐小时趋势、按上游 Key 分布。
// 数据源为 call_stats_buckets（刷盘延迟 ≤10s，图表无需秒级实时）。
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	window := r.URL.Query().Get("window")
	if window == "" {
		window = "24h"
	}
	dur, ok := statsWindows[window]
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_window", "window 仅支持 24h / 7d / 30d")
		return
	}
	// 窗口语义：从「当前小时往前 windowHours-1 小时」到「当前小时（进行中的部分桶）」，
	// 正好 windowHours 个点——24h=24 点、7d=168 点、30d=720 点。
	// 查询下界与 series 起点必须一致，否则 total/success_rate 会多算一个桶。
	nowHour := s.clock.Now().Truncate(time.Hour).Unix()
	windowHours := int64(dur / time.Hour)
	start := nowHour - (windowHours-1)*int64(time.Hour/time.Second)

	rows, err := s.st.CallStats.QueryWindow(start)
	if err != nil {
		s.logger.Warn("查询调用统计失败", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal_error", "统计查询失败")
		return
	}
	perKey, err := s.st.CallStats.PerKey(start)
	if err != nil {
		s.logger.Warn("查询按 Key 统计失败", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal_error", "统计查询失败")
		return
	}

	// 按小时聚合，同时统计窗口总量与 2xx 量（成功率）。
	var total, success int64
	byHour := map[int64]*statsSeriesPoint{}
	for _, row := range rows {
		total += row.Calls
		if row.StatusClass == store.StatusClass2xx {
			success += row.Calls
		}
		pt, ok := byHour[row.Hour]
		if !ok {
			pt = &statsSeriesPoint{TS: row.Hour}
			byHour[row.Hour] = pt
		}
		pt.Calls += row.Calls
		if row.StatusClass != store.StatusClass2xx {
			pt.Errors += row.Calls
		}
	}

	// 补齐无数据的空小时，保证 series 连续（前端画图无需自己补零）。
	series := make([]statsSeriesPoint, 0, windowHours)
	for h := start; h <= nowHour; h += int64(time.Hour / time.Second) {
		if pt, ok := byHour[h]; ok {
			series = append(series, *pt)
		} else {
			series = append(series, statsSeriesPoint{TS: h})
		}
	}

	successRate := 0.0
	if total > 0 {
		successRate = round3(float64(success) / float64(total))
	}
	dist := make([]statsPerKey, 0, len(perKey))
	for _, k := range perKey {
		sh := 0.0
		if total > 0 {
			sh = round3(float64(k.Calls) / float64(total))
		}
		dist = append(dist, statsPerKey{KeyID: k.UpstreamKeyID, Calls: k.Calls, Share: sh})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"window":       window,
		"total_calls":  total,
		"success_rate": successRate,
		"series":       series,
		"per_key":      dist,
	})
}

// round3 保留 3 位小数（0.333333 → 0.333）。
func round3(f float64) float64 {
	return math.Round(f*1000) / 1000
}
