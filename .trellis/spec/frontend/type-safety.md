# Type Safety — Frontend

## 类型来源唯一性

`src/api/types.ts` 是后端响应契约的唯一类型来源。每个类型与
`internal/admin/*.go` 的 DTO/json tag 一一对应，字段名、可空性、枚举值完全一致：

```ts
export type KeyState = 'available' | 'cooling' | 'exhausted' | 'invalid'
// 与 store.KeyState 的字符串值一致；'disabled' 不在此枚举——
// 前端用 enabled:boolean 表示禁用，StateBadge 通过 disabled prop 渲染。
```

可空字段用 `| null`（如 `credits_total: number | null`），禁止用 `any` 或
`as unknown as T` 绕过检查。

## 后端结构变更联动

1. 改后端 DTO 的 json tag / 新增字段 → 同步改 `types.ts`。
2. 跑 `npm run build`（vue-tsc）验证所有消费点类型正确。
3. 跑 `go test ./internal/admin/` 验证端到端响应形状。

## 创建类响应的明文特例

`ProxyKeyCreated extends ProxyKey { plaintext_key: string }`——明文只存在于
创建接口的返回类型里；列表接口的类型 `ProxyKey` 没有该字段，从类型层面杜绝
「顺手把明文渲染出来」的写法。

## fetch 封装

- 所有请求走 `api/client.ts` 的 `request<T>`：`credentials: 'same-origin'`，
  401 全局跳登录（`silent401` 豁免登录接口），错误体取 `message` 抛 `ApiError`。
- 调用侧用 `(e as Error).message` 展示，不自行拼错误文案。
