package proxy

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

// 回归：客户端发 Accept-Encoding: gzip（官方 Python/Node SDK 的默认行为）时，
// url/next 重写与 job 映射记录必须照常工作。
//
// 曾经的缺陷：Accept-Encoding 被原样透传给上游，Go 的 Transport 因此关闭
// 透明解压，resp.Body 是 gzip 字节，JSON 解析静默失败——响应仍是 200，
// 但 url 不被重写（SDK 绕过代理直连上游 401）、job 映射不被记录（轮询 404）。
func TestGzipClientStillRewritesAndRecordsJob(t *testing.T) {
	submitBody := `{"success":true,"id":"job-abc","url":"https://api.firecrawl.dev/v2/crawl/job-abc"}`
	up := newFakeUpstream(planEntry{status: 200, body: submitBody})
	defer up.Close()

	h, st, _, _ := setupProxy(t, up.URL(), 2)

	req := httptest.NewRequest("POST", "/v2/crawl", strings.NewReader(`{"url":"https://x.com"}`))
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("期望 200, got %d", rec.Code)
	}

	// 上游不应收到客户端的 Accept-Encoding（否则 Go 不会透明解压）。
	// 这里只断言最终行为正确即可。
	raw := decodeBody(t, rec.Body.Bytes(), rec.Header().Get("Content-Encoding"))

	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("响应体不是合法 JSON: %v", err)
	}
	gotURL, _ := m["url"].(string)
	if strings.Contains(gotURL, "api.firecrawl.dev") {
		t.Errorf("url 未被重写，仍指向上游: %s", gotURL)
	}
	if !strings.HasPrefix(gotURL, testPublicBase) {
		t.Errorf("url 应指向对外地址 %s, got %s", testPublicBase, gotURL)
	}
	if m["id"] != "job-abc" {
		t.Errorf("重写不应丢失其他字段, got %v", m)
	}

	if jr, err := st.JobRoutes.Get("job-abc"); err != nil || jr == nil {
		t.Fatalf("job 映射未记录，后续轮询将随机分发并 404 (err=%v)", err)
	}
}

// 回归：改写后的响应若客户端接受 gzip，应重新压缩以省带宽，
// 且解压后内容与改写结果一致。
func TestGzipResponseRecompressedForClient(t *testing.T) {
	// 造一个大于 gzipMinSize 的响应，触发重新压缩。
	filler := strings.Repeat("x", 4096)
	body := `{"success":true,"id":"job-big","url":"https://api.firecrawl.dev/v2/crawl/job-big","pad":"` + filler + `"}`
	up := newFakeUpstream(planEntry{status: 200, body: body})
	defer up.Close()

	h, _, _, _ := setupProxy(t, up.URL(), 1)
	req := httptest.NewRequest("POST", "/v2/crawl", strings.NewReader(`{}`))
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if enc := rec.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("客户端接受 gzip 且响应够大时应重新压缩, Content-Encoding=%q", enc)
	}
	if rec.Body.Len() >= len(body) {
		t.Errorf("压缩后应显著小于原文: %d vs %d", rec.Body.Len(), len(body))
	}
	raw := decodeBody(t, rec.Body.Bytes(), "gzip")
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("解压后不是合法 JSON: %v", err)
	}
	if u, _ := m["url"].(string); !strings.HasPrefix(u, testPublicBase) {
		t.Errorf("url 应已重写, got %s", u)
	}
}

// 回归：客户端不接受 gzip 时，响应必须是明文，且不带 Content-Encoding。
func TestNoGzipWhenClientDoesNotAccept(t *testing.T) {
	body := `{"success":true,"id":"job-plain","url":"https://api.firecrawl.dev/v2/crawl/job-plain"}`
	up := newFakeUpstream(planEntry{status: 200, body: body})
	defer up.Close()

	h, _, _, _ := setupProxy(t, up.URL(), 1)
	req := httptest.NewRequest("POST", "/v2/crawl", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("客户端未请求压缩时不应带 Content-Encoding, got %q", enc)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("响应体应为明文 JSON: %v", err)
	}
}

// decodeBody 按 Content-Encoding 解码响应体。
func decodeBody(t *testing.T, raw []byte, encoding string) []byte {
	t.Helper()
	if encoding != "gzip" {
		return raw
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("响应声明 gzip 但无法解压: %v", err)
	}
	defer zr.Close()
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("解压失败: %v", err)
	}
	return out
}
