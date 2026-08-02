# C6 设计 — 面板前端 SPA

## 目录

```
web/
├── package.json
├── vite.config.ts          # build.outDir: '../internal/webui/dist'
├── tailwind.config.js
├── index.html
└── src/
    ├── main.ts
    ├── App.vue
    ├── router.ts           # vue-router：/ /keys /api-keys /login
    ├── api/
    │   ├── client.ts       # fetch 封装：统一 401 处理、JSON 解析、错误结构
    │   └── types.ts        # 与 C5 响应一一对应的 TS 类型
    ├── composables/
    │   ├── usePolling.ts   # 可见性感知的轮询
    │   └── useTheme.ts
    ├── components/
    │   ├── StateBadge.vue
    │   ├── ConfirmDialog.vue
    │   ├── RevealKeyDialog.vue
    │   └── Toast.vue
    └── views/
        ├── LoginView.vue
        ├── OverviewView.vue
        ├── UpstreamKeysView.vue
        └── ApiKeysView.vue
```

依赖限于 `vue`、`vue-router`、`tailwindcss` 及其构建插件。不引入 Pinia、Axios、组件库——面板只有三个页面、十来个接口，全局状态就是「登录与否」和「主题」，用 `ref` + `provide/inject` 足够；`fetch` 封装二十行就能覆盖需求。

## API 封装

```ts
async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, { ...init, credentials: 'same-origin' })
  if (res.status === 401) { redirectToLogin(); throw new UnauthorizedError() }
  if (!res.ok) throw new ApiError(await res.json())
  return res.status === 204 ? (undefined as T) : res.json()
}
```

401 的处理集中在这一处。各页面不重复判断登录态。

## 轮询

```ts
usePolling(fn, intervalMs)   // document.hidden 时暂停，可见时立即执行一次再恢复
```

监听 `visibilitychange`。切回页面时先立刻拉一次，避免用户看到最多 5 秒的陈旧数据。组件卸载时清理定时器。

## 冷却倒计时

后端返回的是 `cooldown_remaining`（秒）这个快照值。前端本地每秒递减做倒计时显示，每次轮询用服务端值校正。不在前端根据 `cooldown_until` 时间戳计算——客户端时钟未必与服务器一致，会出现倒计时为负或恢复不同步。

## 明文展示弹窗

`RevealKeyDialog.vue` 只接收一次性的 prop，关闭即销毁组件、清空引用。不写进任何 store、不放进 URL、不落 `localStorage`。复制按钮用 `navigator.clipboard.writeText`，失败时降级为选中文本并提示手动复制（非 HTTPS 环境下 clipboard API 不可用——面板可能先在 HTTP 下试跑）。

## 主题

Tailwind 的 `darkMode: 'class'`。初始值取 `localStorage` → 无则取 `prefers-color-scheme`。切换时改 `<html>` 的 class 并写回 `localStorage`。

## 构建顺序约束

`go:embed` 在编译期读取文件，因此 `npm run build` 必须早于 `go build`。开发时若尚未构建前端，`internal/webui/dist` 为空会导致 `go build` 直接失败。

处理方式：仓库中提交一个占位的 `internal/webui/dist/.gitkeep` 与最小 `index.html`，让 `go build` 在未构建前端时也能通过（C5 步骤 7 已依赖这一点）。真实产物在 C7 的 Dockerfile 中按正确顺序生成。
