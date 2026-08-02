# Hook Guidelines — Frontend

本项目把可复用逻辑放 `src/composables/`，命名 `useXxx.ts`，返回 ref 或纯函数。

## usePolling(fn, intervalMs) — 面板轮询唯一入口

- `document.hidden` 时**暂停**（后台标签页不浪费请求）；切回页面立即拉一次再恢复。
- 上一次请求未返回时跳过本次 tick（`inflight` 防堆积）。
- 组件卸载时清理定时器与 `visibilitychange` 监听。
- 单次失败不中断轮询（错误由调用方 Toast），避免一个 5xx 把面板打停。

## useTheme() — 暗色主题

- 初始值：`localStorage['fc-proxy-theme']` 优先，其次 `prefers-color-scheme`。
- `watchEffect` 同步 `document.documentElement.classList.toggle('dark', ...)`
  与 localStorage（Tailwind `darkMode: 'class'`）。
- 手动切换会覆盖系统偏好（持久化后以手动为准）。

## useToast() — 写操作反馈

- 模块级单例（`toasts` ref + `seq`），任何组件 `push('success'|'error', text)`。
- 3.6 秒自动消失，可手动关闭；容器固定在右上角（App.vue 内）。

## 约定

- 倒计时类逻辑（冷却剩余、N 秒前更新）用「后端快照秒数 + 本地每秒递减」，
  轮询结果会校正漂移；不要单独用客户端时钟算绝对值。
- composable 内部不直接 import 视图组件；错误信息一律以 `(e as Error).message`
  上抛给调用方展示。
