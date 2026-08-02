// Package webui 把前端构建产物（internal/webui/dist）嵌入二进制并对外服务。
//
// go:embed 只能嵌入本包目录树内的文件，因此 Vite 的 build.outDir 指向
// ../internal/webui/dist（见 web/vite.config.ts）。dist 由 C6 构建产出，
// 当前存放占位 index.html 保证 embed 与构建可用。
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Handler 服务 SPA：命中文件返回文件，未命中回退 index.html（前端路由）。
//
// 回退不覆盖 /v1/、/v2/、/api/ 前缀——这些路径的 handler 若没匹配上具体
// 路由，应返回 404 而非 200 + HTML，否则客户端 SDK 会拿到 HTML 而误以为
// 调用成功（表现为极难排查的「解析失败」）。
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("webui: dist 嵌入失败: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		// 代理与 API 前缀绝不兜底到 index.html。
		if strings.HasPrefix(p, "/v1/") || strings.HasPrefix(p, "/v2/") ||
			strings.HasPrefix(p, "/api/") {
			http.NotFound(w, r)
			return
		}
		// 命中真实文件（含 /）直接返回。
		name := strings.TrimPrefix(p, "/")
		if name == "" {
			name = "index.html"
		}
		if _, err := sub.Open(name); err == nil {
			// 直接请求 /index.html 时 FileServer 会 301 到 "./"（Go 的规范化行为），
			// 这里对 index.html 显式服务；其余命中文件走 FileServer。
			if name == "index.html" {
				http.ServeFileFS(w, r, sub, "index.html")
				return
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		// 前端路由回退（ServeFileFS 避免 index.html 被 FileServer 重定向）。
		http.ServeFileFS(w, r, sub, "index.html")
	})
}
