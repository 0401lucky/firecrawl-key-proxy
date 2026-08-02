package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
)

// 可编程的假 Firecrawl 上游：按调用序号消费 plan 预设响应，
// 并记录每次收到的请求（方法/路径/查询串/Authorization/请求体）。
// 全部 AC 的验证都依赖它，后续 C4/C5 集成测试也会复用。

type planEntry struct {
	status  int
	body    string
	headers map[string]string
}

// callRecord 是假上游收到的一次请求。
type callRecord struct {
	method  string
	path    string // 不含查询串
	rawQuery string
	auth    string // Authorization 头的值
	body    []byte
	ct      string // Content-Type
}

type fakeUpstream struct {
	mu      sync.Mutex
	srv     *httptest.Server
	plan    []planEntry // 按调用序号消费；耗尽后用默认 200
	calls   []callRecord
}

// newFakeUpstream 启动一个按 plan 响应的假上游。
func newFakeUpstream(plan ...planEntry) *fakeUpstream {
	f := &fakeUpstream{plan: plan}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *fakeUpstream) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rec := callRecord{
		method:   r.Method,
		path:     r.URL.Path,
		rawQuery: r.URL.RawQuery,
		auth:     r.Header.Get("Authorization"),
		body:     body,
		ct:       r.Header.Get("Content-Type"),
	}
	f.mu.Lock()
	var entry planEntry
	if len(f.plan) > 0 {
		entry = f.plan[0]
		f.plan = f.plan[1:]
	} else {
		entry = planEntry{status: http.StatusOK, body: `{"ok":true}`}
	}
	f.calls = append(f.calls, rec)
	f.mu.Unlock()

	// 默认按 JSON 响应处理（代理只在 Content-Type 为 JSON 时解析/改写响应体）；
	// 用例可显式覆盖（如二进制响应指定 image/png）。
	if _, ok := entry.headers["Content-Type"]; !ok {
		w.Header().Set("Content-Type", "application/json")
	}
	for k, v := range entry.headers {
		w.Header().Set(k, v)
	}
	w.WriteHeader(entry.status)
	io.WriteString(w, entry.body)
}

// URL 返回假上游的地址。
func (f *fakeUpstream) URL() string { return f.srv.URL }

// Close 关闭假上游。
func (f *fakeUpstream) Close() { f.srv.Close() }

// Calls 返回收到请求的副本。
func (f *fakeUpstream) Calls() []callRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]callRecord, len(f.calls))
	copy(out, f.calls)
	return out
}

// callAuths 返回第 n 次调用起的 Authorization 序列。
func (f *fakeUpstream) callAuths() []string {
	var out []string
	for _, c := range f.Calls() {
		out = append(out, c.auth)
	}
	return out
}

// callCount 返回收到的请求总数。
func (f *fakeUpstream) callCount() int {
	return len(f.Calls())
}
