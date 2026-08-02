package proxy

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
)

// 本文件实现响应体绝对 URL 重写（父任务 R1.4）与 job id 提取。
//
// 改写规则：把 url/next 字段中 URL 的 scheme+host 替换为 PUBLIC_BASE_URL，
// 保留路径与查询串。必须用 url.Parse 而非字符串替换——查询串里可能出现
// 同样的 host 字面量，字符串替换会误伤。
//
// 解析用 map[string]any 而非结构体：Firecrawl 的响应字段随版本增减，
// 用结构体会在序列化回去时静默丢字段。

// respNeedsRewrite 判断该请求路径的响应是否需要缓冲与改写：
//   - 提交端点（jobSubmitRe）：响应含 url 字段（轮询地址）。
//   - 状态查询端点无 /errors 后缀：响应含 next 字段（分页游标）。
//   - /errors 与其余路径的响应不含这些字段，一律流式透传。
func respNeedsRewrite(path string) bool {
	if jobSubmitRe.MatchString(path) {
		return true
	}
	m := jobPathRe.FindStringSubmatch(path)
	return m != nil && m[3] == ""
}

// isJSONResponse 判断响应是否为 JSON（Content-Type 含 application/json）。
func isJSONResponse(resp *http.Response) bool {
	mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return false
	}
	return mt == "application/json"
}

// bufferResponse 把响应体读入内存（上限 maxResponseBuffer）。
// 超限返回 false，调用方应放弃处理、原样透传，不得截断。
func (h *Handler) bufferResponse(resp *http.Response) ([]byte, bool) {
	buf, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBuffer+1))
	if err != nil {
		return nil, false
	}
	if len(buf) > maxResponseBuffer {
		return nil, false
	}
	return buf, true
}

// processBody 解析 JSON 响应体，改写 url/next 字段并返回：
//   - rewritten：改写后的完整响应体
//   - jobID：提交端点响应中的任务 id（非提交端点或解析失败时为空串）
//
// 解析失败时不改写、不提取 job，原样返回原 body。
func (h *Handler) processBody(path string, body []byte, statusCode int) ([]byte, string) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body, ""
	}

	if jobSubmitRe.MatchString(path) {
		h.rewriteField(m, "url")
	} else if sub := jobPathRe.FindStringSubmatch(path); sub != nil && sub[3] == "" {
		h.rewriteField(m, "next")
	}

	// job 提取：仅提交端点且响应成功（记录时机由调用方判定）。
	var jobID string
	if statusCode >= 200 && statusCode < 300 {
		if id, ok := m["id"].(string); ok {
			jobID = id
		}
	}

	rewritten, err := json.Marshal(m)
	if err != nil {
		return body, jobID
	}
	return rewritten, jobID
}

// rewriteField 把 map 中指定字段的绝对 URL 替换为对外地址。
// 字段不是字符串、或不是绝对 URL 时保持原样。
func (h *Handler) rewriteField(m map[string]any, field string) {
	v, ok := m[field].(string)
	if !ok || v == "" {
		return
	}
	u, err := url.Parse(v)
	if err != nil || !u.IsAbs() {
		return
	}
	u.Scheme = h.publicBase.Scheme
	u.Host = h.publicBase.Host
	m[field] = u.String()
}
