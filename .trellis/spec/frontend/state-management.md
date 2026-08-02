# State Management — Frontend

**不引入 Pinia/Vuex**。本项目只有三种状态形态：

## 1. 全局登录态 — `api/client.ts` 的模块级 reactive 单例

```ts
export const authState = reactive({ authenticated: false, ready: true })
```

- `router.ts` 守卫在 `!ready` 时先调 `GET /api/admin/session` 确认登录态（仅一次）。
- 任何 API 返回 401 → `authState.authenticated = false` + `location.assign('/login')`
  （整页跳转重置内存态）；登录接口的 401 用 `silent401` 豁免，避免身在登录页时
  被全局逻辑二次跳转。

## 2. 组件局部状态 — `ref`/`reactive`

- 每个 view 自持列表数据（`const keys = ref<...>([])`）与表单态、busy 标记。
- 列表在写操作成功后**就地更新**（`unshift`/按 id 替换/过滤），不等下一次轮询，
  满足 AC8「新增立即出现」。

## 3. Toast / 主题 — composable 模块级单例

`useToast`、`useTheme` 内部是模块级 ref，跨组件共享；用法见 hook-guidelines.md。

## 约定

- 服务器状态（列表、概览）一律轮询拉取，不在前端做二次缓存或推导。
- 明文 Key 不进入任何状态容器（见 component-guidelines.md 禁止模式）。
