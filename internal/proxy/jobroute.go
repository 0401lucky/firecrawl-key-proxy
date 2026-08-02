package proxy

import "regexp"

// 本文件实现异步任务（job）的路径识别与 Key 粘连（父任务 R4）。
//
// 路径匹配用预编译正则而非字符串切分：/v2/crawl/{id}/errors 与 /v2/crawl/{id}
// 需要区分，且 {id} 本身可能含连字符。硬编码 v1/v2（Firecrawl 的异步端点
// 只存在于这两个版本前缀下，见父任务 Background）。

var (
	// jobSubmitRe 匹配异步任务提交端点（响应含 {success, id, url}）。
	jobSubmitRe = regexp.MustCompile(`^/v[12]/(crawl|batch/scrape|extract)$`)
	// jobPathRe 匹配异步任务查询/取消端点，{id} 后可选 /errors 后缀。
	jobPathRe = regexp.MustCompile(`^/v[12]/(crawl|batch/scrape|extract)/([^/]+)(/errors)?$`)
)

// isJobQueryPath 判断路径是否为任务查询/取消端点。
func isJobQueryPath(path string) bool {
	return jobPathRe.MatchString(path)
}

// jobIDFromPath 从任务查询路径中取出 job id。
// 调用方必须先确认 isJobQueryPath(path) 为真。
func jobIDFromPath(path string) string {
	m := jobPathRe.FindStringSubmatch(path)
	if m == nil {
		return ""
	}
	return m[2]
}

// jobKindFromPath 返回任务类型：crawl | batch_scrape | extract。
// 提交与查询路径均可识别。
func jobKindFromPath(path string) string {
	if m := jobSubmitRe.FindStringSubmatch(path); m != nil {
		return m[1]
	}
	if m := jobPathRe.FindStringSubmatch(path); m != nil {
		return m[1]
	}
	return ""
}
