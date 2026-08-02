# Directory Structure — Frontend

## 项目布局

```
web/
  index.html                # Vite 入口（title 为「Firecrawl 代理 · 管理面板」）
  vite.config.ts            # build.outDir → ../internal/webui/dist；dev /api 代理到 :8080
  tailwind.config.js        # 仪表台主题：ink 深色系、amber 高亮、.num 等宽数据
  src/
    main.ts                 # createApp + router + mount('#app')
    style.css               # Tailwind 指令 + .bg-instrument 网格背景
    router.ts               # createWebHistory；登录守卫 + 401 逻辑在此
    App.vue                 # 仪表台骨架（侧栏/Toast 容器），登录页不套骨架
    api/
      client.ts             # fetch 封装、authState 单例、401 自动跳登录
      types.ts              # 与后端 JSON 契约一一对应的类型（见 type-safety.md）
    composables/
      usePolling.ts         # 可见性感知轮询（document.hidden 暂停）
      useTheme.ts           # 暗色主题：系统偏好 + 手动切换 + localStorage
      useToast.ts           # 全局 Toast（模块级单例）
    components/
      StateBadge.vue        # 状态徽标（可用/冷却/耗尽/失效/禁用）
      ConfirmDialog.vue     # 通用二次确认弹窗（Teleport + 危险色）
      RevealKeyDialog.vue   # API Key 明文一次性展示弹窗
    views/
      LoginView.vue  OverviewView.vue  UpstreamKeysView.vue  ApiKeysView.vue
```

构建产物由 Vite 输出到 `internal/webui/dist`（`emptyOutDir: true`），
**不入版本库**——.gitignore 只放行占位 `index.html` 与 `.gitkeep`，保证全新
checkout 时 `go build` 的 embed 仍可用；真实产物由 C7 Dockerfile 在 `go build`
之前构建。

## 关键约束

- **`build.outDir` 必须指向 `../internal/webui/dist`**：`go:embed` 只能嵌入
  包目录树内文件，产物放 `web/dist` 会让 embed 报 `no matching files`。
- **`emptyOutDir` 必须开启**：旧产物残留会导致 `index.html` 引用的哈希文件名
  与 assets 目录对不上，表现为白屏。
- **开发期跨域**：`vite.config.ts` 的 `server.proxy` 把 `/api` 转发到本地
  Go 服务（127.0.0.1:8080），前端请求全部走相对路径 `/api/...`。
- 前端不引入状态库（无 Pinia）。运行时零 UI 框架依赖，Tailwind 即样式方案。
