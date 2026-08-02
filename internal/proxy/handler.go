// Package proxy 实现代理主路径：透明转发、故障转移重试、异步任务 Key 粘连、
// 响应体绝对 URL 重写。AC1–AC6 全部由本包承担。
//
// 不用 httputil.ReverseProxy：故障转移需要在拿到上游响应后决定是否换个目标
// 重发，ReverseProxy 的 Director/ModifyResponse 钩子表达不出「重来一次」。
// 因此直接用 http.Client 手写转发循环，代价是自行处理 hop-by-hop 头。
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"firecrawl-proxy/internal/keypool"
	"firecrawl-proxy/internal/logging"
	"firecrawl-proxy/internal/store"
)

// 需要剔除的 hop-by-hop 头（RFC 7230 §6.1）。请求侧与响应侧都要剔。
var hopByHopHeaders = map[string]struct{}{
	"Connection":          {},
	"Proxy-Connection":    {},
	"Keep-Alive":          {},
	"Transfer-Encoding":   {},
	"TE":                  {},
	"Trailer":             {},
	"Upgrade":             {},
	"Proxy-Authorization": {},
	"Proxy-Authenticate":  {},
}

// 响应体缓冲上限（URL 重写 / job 提取用），超限则放弃处理、原样透传。
const maxResponseBuffer = 32 << 20 // 32 MiB

// Handler 是代理转发处理器。所有字段只读，并发安全。
type Handler struct {
	pool         *keypool.Pool
	jobs         *store.JobRouteRepo
	client       *http.Client
	upstreamBase *url.URL
	publicBase   *url.URL
	pathPrefixes []string
	maxAttempts  int
	maxReqBuf    int64
	jobTTL       time.Duration
	logger       *slog.Logger
	clock        keypool.Clock
}

// Config 是代理处理器需要的运行时参数（由 main 从 config.Config 组装）。
type Config struct {
	UpstreamBaseURL  string
	PublicBaseURL    string
	PathPrefixes     []string
	MaxAttempts      int
	MaxRequestBuffer int64
	JobTTL           time.Duration
	Clock            keypool.Clock
}

// NewHandler 构造代理处理器。
func NewHandler(
	pool *keypool.Pool,
	jobs *store.JobRouteRepo,
	logger *slog.Logger,
	cfg Config,
) (*Handler, error) {
	up, err := url.Parse(cfg.UpstreamBaseURL)
	if err != nil {
		return nil, err
	}
	pub, err := url.Parse(cfg.PublicBaseURL)
	if err != nil {
		return nil, err
	}
	return &Handler{
		pool:         pool,
		jobs:         jobs,
		upstreamBase: up,
		publicBase:   pub,
		pathPrefixes: cfg.PathPrefixes,
		maxAttempts:  cfg.MaxAttempts,
		maxReqBuf:    cfg.MaxRequestBuffer,
		jobTTL:       cfg.JobTTL,
		logger:       logger,
		clock:        cfg.Clock,
		client: &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					Timeout:   30 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				MaxIdleConns:          100,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 60 * time.Second,
			},
			// 不设总超时：流式响应必须能持续读取。
		},
	}, nil
}

// ServeHTTP 是代理主入口。认证（C4）由外层中间件完成，这里假设调用方身份已可用。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. 前缀检查：范围外返回 JSON 404，不转发。
	if !h.matchesPrefix(r.URL.Path) {
		writeJSONError(w, http.StatusNotFound, "not_found", "请求路径不在转发范围内", nil)
		return
	}

	// 2. 异步任务粘连：GET/DELETE 命中已知 job 路径且映射存在 → 走独立分支。
	if (r.Method == http.MethodGet || r.Method == http.MethodDelete) &&
		isJobQueryPath(r.URL.Path) {
		if jr, err := h.jobs.Get(jobIDFromPath(r.URL.Path)); err == nil {
			if jr.ExpiresAt.After(h.clock.Now()) {
				h.stickyPath(w, r, jr)
				return
			}
			// 映射已过期：顺带清一次，退化为常规轮询。
			_, _ = h.jobs.DeleteExpired(h.clock.Now())
		}
	}

	// 3-4. 常规路径：缓冲请求体 + 故障转移循环。
	h.forwardLoop(w, r)
}

// forwardLoop 是常规请求的主循环：选 Key → 转发 → 判定 → 重试。
func (h *Handler) forwardLoop(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	buf, buffered := h.bufferRequestBody(r)

	var tried []int64
	for attempt := 0; attempt < h.maxAttempts; attempt++ {
		key, err := h.pool.NextExcluding(tried)
		if err != nil {
			// 无候选 Key。区分两种情况：
			//  - attempt==0：一开始就没有可用 Key（额度耗尽/全部冷却/已禁用）→ 503；
			//  - attempt>0：已经尝试过但中途耗尽候选 → 502（试过了，全失败）。
			// 两种情况都带状态计数，调用方仍能区分「没额度了」vs「上游挂了」。
			_, counts := h.pool.Snapshot()
			if attempt == 0 {
				writeJSONError(w, http.StatusServiceUnavailable,
					"no_upstream_key_available", "所有上游 Key 均不可用", counts)
			} else {
				writeJSONError(w, http.StatusBadGateway,
					"upstream_failover_exhausted", "多次换 Key 重试后仍失败", counts)
			}
			return
		}
		tried = append(tried, key.ID)

		// 缓冲体每次尝试都要新建 reader：同一 io.Reader 被消费一次后不可重读，
		// 重试时会以「ContentLength>0 但 body 为 EOF」在客户端直接失败。
		var body io.Reader = r.Body // 未缓冲：单次转发，直接流式读原 body
		var bodyLen int64 = -1
		if buffered {
			body = bytes.NewReader(buf)
			bodyLen = int64(len(buf))
		}
		resp, netErr := h.forward(r, body, bodyLen, key)
		var statusCode int
		if resp != nil {
			statusCode = resp.StatusCode
		}
		outcome := keypool.Outcome{
			StatusCode: statusCode,
			RetryAfter: parseRetryAfter(resp),
			Err:        netErr,
		}
		// 惩罚与否完全交给 keypool 判定（5xx/网络错误不惩罚）。
		h.pool.Report(key.ID, outcome)
		if netErr == nil {
			h.pool.RecordUsage(key.ID)
		}

		if !shouldRetry(outcome) || !buffered {
			h.deliver(w, r, resp, key, outcome)
			h.logRequest(r, key, statusCode, attempt, start, netErr)
			return
		}
		// 要重试：先放掉未消费的响应体，复用连接。
		drainAndClose(resp)
	}

	// 次数耗尽仍失败（此处必是 buffered=true 且都是可重试错误）。
	_, counts := h.pool.Snapshot()
	writeJSONError(w, http.StatusBadGateway,
		"upstream_failover_exhausted", "多次换 Key 重试后仍失败", counts)
}

// matchesPrefix 判断路径是否命中任一转发前缀。
func (h *Handler) matchesPrefix(path string) bool {
	for _, p := range h.pathPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// bufferRequestBody 读取请求体到内存以支持重放。
//
// 返回：buf 为缓冲的请求体（buffered=true 时有效，调用方每次重试需新建 reader）；
// buffered=false 表示超限或读取失败——此时 r.Body 已被替换为重建的完整流，
// 只能单次转发，不支持故障转移。
// 用 LimitReader 读到 max+1 字节判定超限，已读部分与剩余流用 MultiReader
// 拼回，不整体读进内存。
func (h *Handler) bufferRequestBody(r *http.Request) ([]byte, bool) {
	if h.maxReqBuf <= 0 {
		return nil, false
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, h.maxReqBuf+1))
	if err != nil || int64(len(buf)) > h.maxReqBuf {
		// 超限：已读部分 + 剩余流拼回，单次转发。
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(buf), r.Body))
		return nil, false
	}
	return buf, true
}

// forward 构造上游请求并发送：保留方法/路径/查询串/请求头（除认证与 hop-by-hop），
// 用选中的上游 Key 替换 Authorization。contentLength<0 表示未知（走 chunked）。
func (h *Handler) forward(r *http.Request, body io.Reader, contentLength int64, key *store.UpstreamKey) (*http.Response, error) {
	u := *h.upstreamBase
	u.Path = r.URL.Path
	u.RawQuery = r.URL.RawQuery

	req, err := http.NewRequestWithContext(r.Context(), r.Method, u.String(), body)
	if err != nil {
		return nil, err
	}
	for k, vs := range r.Header {
		if isHopByHop(k) || k == "Authorization" || k == "Host" {
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Authorization", "Bearer "+key.APIKey)
	if contentLength > 0 {
		req.ContentLength = contentLength
	}
	return h.client.Do(req)
}

// deliver 把上游响应原样交还客户端；仅在需要改写/提取 job 时缓冲响应体。
func (h *Handler) deliver(
	w http.ResponseWriter,
	r *http.Request,
	resp *http.Response,
	key *store.UpstreamKey,
	outcome keypool.Outcome,
) {
	if resp == nil {
		// 网络错误且不可重试（超限请求）：给出可读的错误。
		writeJSONError(w, http.StatusBadGateway, "upstream_error",
			"转发上游失败："+outcome.Err.Error(), nil)
		return
	}
	defer resp.Body.Close()

	// 需要缓冲响应体的判定：JSON + 命中需重写集合。
	if respNeedsRewrite(r.URL.Path) && isJSONResponse(resp) {
		body, ok := h.bufferResponse(resp)
		if ok {
			rewritten, jobID := h.processBody(r.URL.Path, body, resp.StatusCode)
			if jobID != "" && resp.StatusCode >= 200 && resp.StatusCode < 300 {
				h.recordJobRoute(jobID, r.URL.Path, key.ID)
			}
			copyResponseHeaders(w.Header(), resp.Header)
			w.Header().Set("Content-Length", strconv.Itoa(len(rewritten)))
			w.WriteHeader(resp.StatusCode)
			w.Write(rewritten)
			return
		}
		h.logger.Warn("响应体超限，放弃重写与 job 提取，原样透传",
			"path", r.URL.Path, "status", resp.StatusCode)
	}

	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	// 流式透传：不整体读入内存。
	_, _ = io.Copy(w, resp.Body)
}

// stickyPath 是命中 job 映射的专用转发路径：强制使用提交任务时的那个 Key，
// 不做故障转移，也不调用 pool.Report()——查询已有任务通常不消耗额度，
// 换 Key 重试只会得到 404；一个 exhausted 的 Key 查自己的任务若返回 402
// 也只是无用信号，不应把已经正确的状态搅乱。
func (h *Handler) stickyPath(w http.ResponseWriter, r *http.Request, jr *store.JobRoute) {
	key, err := h.pool.GetByID(jr.UpstreamKeyID)
	if err != nil {
		// 上游 Key 已被删除（job 映射本应级联删除，防御性兜底）。
		h.logger.Warn("job 映射指向的 Key 不存在，删除映射并退化为常规转发",
			"job_id", jr.JobID, "key_id", jr.UpstreamKeyID)
		_ = h.jobs.Delete(jr.JobID)
		h.forwardLoop(w, r)
		return
	}
	start := time.Now()
	resp, netErr := h.forward(r, r.Body, -1, key)
	if netErr != nil {
		writeJSONError(w, http.StatusBadGateway, "upstream_error",
			"转发上游失败："+netErr.Error(), nil)
		return
	}
	// 不调用 pool.Report()：见函数注释。
	h.deliver(w, r, resp, key, keypool.Outcome{StatusCode: resp.StatusCode})
	h.logRequest(r, key, resp.StatusCode, 0, start, nil)
}

// recordJobRoute 把异步任务与提交它的上游 Key 写入持久化映射。
func (h *Handler) recordJobRoute(jobID, path string, keyID int64) {
	now := h.clock.Now()
	jr := &store.JobRoute{
		JobID:         jobID,
		UpstreamKeyID: keyID,
		Kind:          jobKindFromPath(path),
		CreatedAt:     now,
		ExpiresAt:     now.Add(h.jobTTL),
	}
	if err := h.jobs.Upsert(jr); err != nil {
		h.logger.Warn("写入 job 映射失败", "job_id", jobID, "error", err.Error())
	}
}

// StartCleanup 清理过期 job 映射：启动时一次，之后每小时一次。
// 由 main 以 goroutine 方式调用，ctx 取消即退出。
func (h *Handler) StartCleanup(ctx context.Context) {
	h.cleanupExpired()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			h.cleanupExpired()
		case <-ctx.Done():
			return
		}
	}
}

func (h *Handler) cleanupExpired() {
	n, err := h.jobs.DeleteExpired(h.clock.Now())
	if err != nil {
		h.logger.Warn("清理过期 job 映射失败", "error", err.Error())
		return
	}
	if n > 0 {
		h.logger.Info("清理过期 job 映射", "count", n)
	}
}

// logRequest 输出每个代理请求的结构化日志。上游 Key 一律脱敏。
func (h *Handler) logRequest(
	r *http.Request, key *store.UpstreamKey, status int, attempt int, start time.Time, netErr error,
) {
	attrs := []any{
		"method", r.Method,
		"path", r.URL.Path,
		"upstream_key", logging.MaskKey(key.APIKey),
		"upstream_status", status,
		"failover_count", attempt,
		"duration_ms", time.Since(start).Milliseconds(),
	}
	if netErr != nil {
		attrs = append(attrs, "error", netErr.Error())
	}
	h.logger.Info("proxy request", attrs...)
}

// ---- 工具函数 ----

func isHopByHop(header string) bool {
	_, ok := hopByHopHeaders[http.CanonicalHeaderKey(header)]
	return ok
}

// copyResponseHeaders 拷贝响应头并剔除 hop-by-hop 头与 Content-Length
// （重写路径会显式设置，流式路径由 Go 处理）。
func copyResponseHeaders(dst, src http.Header) {
	for k, vs := range src {
		if isHopByHop(k) || k == "Content-Length" {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// drainAndClose 放掉未消费的响应体以复用连接。
func drainAndClose(resp *http.Response) {
	if resp == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
}

// parseRetryAfter 解析 Retry-After 头：HTTP 日期或秒数；缺失/无法解析返回 0。
func parseRetryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// writeJSONError 输出统一的 JSON 错误体（父任务 design §9 契约）。
func writeJSONError(w http.ResponseWriter, status int, code, msg string, detail any) {
	body := map[string]any{"error": code, "message": msg}
	if detail != nil {
		d := map[string]int{}
		for k, v := range detail.(map[store.KeyState]int) {
			d[string(k)] = v
		}
		body["detail"] = d
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
