# Component Guidelines — Frontend

## 组件模式

- `<script setup lang="ts">`，props 用 `defineProps<{...}>()`，事件用
  `defineEmits<{ confirm: [] }>()` 精确声明，不用运行时 props 声明。
- 弹窗类组件用 `Teleport to="body"` + `<Transition name="dlg">` + `role="dialog"`
  `aria-modal="true"`；点击遮罩（`@click.self`）触发 cancel。
- 状态展示组件（如 `StateBadge`）只做「状态 → 文案/颜色」映射，不持有业务状态。
- 组件内不直接 `fetch`——统一走 `api/client.ts` 封装，便于 401 全局处理与类型推导。

## 本项目组件清单（复用优先）

| 组件 | 用途 | 关键点 |
|------|------|--------|
| `StateBadge` | Key 状态灯 | 可用=绿 / 冷却=琥珀 / 耗尽=灰 / 失效=红 / 禁用=灰（`disabled` prop 覆盖） |
| `ConfirmDialog` | 危险操作二次确认 | `danger` prop 切换红/琥珀；文案必须说明后果 |
| `RevealKeyDialog` | 明文 Key 一次性展示 | 明文只作为 prop 传入，关闭即销毁；内含复制降级 |

## 禁止模式

- 禁止把明文 Key 写入组件模块级变量、store、URL 或 localStorage——只允许
  `RevealKeyDialog` 的 props 短暂持有（AC9）。
- 禁止用 `v-html` 渲染任何后端返回内容。
- 禁止 inline style 写死主题色——统一用 Tailwind 的 `dark:` 变体与主题 token。
