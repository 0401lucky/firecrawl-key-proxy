# Journal - lucky0401 (Part 1)

> AI development session journal
> Started: 2026-08-02

---

## 2026-08-02 — C6 面板前端 SPA 完成

- `web/` 新建 Vue 3 + Vite + TS + Tailwind 工程,产物出到 `internal/webui/dist`(go:embed)。
- 三个视图:总览(额度池/状态卡/轮询/倒计时)、上游 Key(CRUD)、API Key(创建即示明文一次)。
- 登录/401 自动跳转、暗色主题持久化、Toast、确认弹窗全部按 PRD 完成,浏览器 E2E 逐条验证。
- 过程中修复:keypool `Snapshot()` 不做惰性恢复导致冷却归零后面板不转可用——补上与 `next()` 一致
  的惰性恢复 + 新测试;`TestSPAFallback` 断言依赖旧占位页文案,改为兼容占位/真实 SPA 两态。
- 提交前需 `git checkout -- internal/webui/dist/index.html` 恢复占位页(assets 不入库,真实产物由
  C7 Dockerfile 在 go build 前构建)。

## 2026-08-02 — 归档 C1–C5 + C7 容器化与部署完成

- 归档 C1–C5(实现早已提交,状态未收尾);go vet + go test 全绿后逐个 archive。
- C7 落地:Dockerfile(三阶段 node→go→alpine,非 root uid 10001,HEALTHCHECK,
  Go 镜像用 1.25 匹配 go.mod,不是 design 里旧的 1.22)、.dockerignore、docker-compose.yml
  (proxy 仅 expose,内置 Caddy 用 profile tls)、.env.example、Caddyfile.example、README.md。
- 验证全部实测:镜像 32MB;健康检查 healthy;/healthz 200;容器内面板与 session API 正常;
  AC12 数据跨 down/up 保留;缺失必填变量聚合报错退出;层缓存命中(npm/go mod 不重装)。
- 坑:wget 在容器里可用但 busybox 版不支持 cookie,AC12 测试改用带端口映射的一次性容器+宿主 curl。

