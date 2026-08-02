# Quality Guidelines — Frontend

## 门禁

- `npm run build` = `vue-tsc --noEmit -p tsconfig.json && vite build`，类型错误即失败。
- 前端改动后必须同时跑 `cd web && npm run build` 与 `cd .. && go build ./...`——
  前者验证产物、后者验证 `go:embed` 未报 `no matching files`。
- 面板相关 Go 测试：`go test ./internal/admin/`（含 SPA 回退、认证隔离、AC 用例）。

## 契约对齐（最高优先级）

- `src/api/types.ts` 与后端 `internal/admin/*.go` 的 JSON tag **一一对应**。
  后端少返回字段时回 C5 补充，**不要**在前端做推导或拼接。
- 改后端响应结构后，必须先跑前端 build（类型检查会兜住字段漂移）。

## 明文 Key 红线（AC9）

- `plaintext_key` 只出现在创建接口的一次性响应里；前端只允许 `RevealKeyDialog`
  的 props 持有，关闭即销毁。列表永远只显示 `key_prefix`。
- 复制必须处理非 HTTPS 降级：`navigator.clipboard` 不可用时 Toast 提示手动选中复制，
  不得静默失败。

## 易错点（踩过的坑）

- **`build.outDir` 配错** → 产物落 `web/dist`，embed 报 `no matching files`。
- **`emptyOutDir` 未开** → 旧哈希资源残留，白屏。
- **倒计时用客户端时钟** → 与后端漂移；统一「后端快照 + 本地递减 + 轮询校正」。
- **后端无请求时冷却不恢复** → keypool `Snapshot()` 必须与 `next()` 一样做惰性恢复，
  否则面板永远显示冷却中（`internal/keypool/pool.go` 已修复并加测试）。
- **SPA 兜底吃掉 API 404** → `webui.Handler()` 对 `/v1/ /v2/ /api/` 前缀直接 404，
  绝不回退 index.html。
- 测试断言别依赖旧占位页文案：占位 `index.html` 与真实 SPA 产物并存，断言应同时
  接受 `<div id="app"></div>` 或占位文案（见 `TestSPAFallback`）。

## 暗色主题

- 全部用 Tailwind `dark:` 变体 + 主题 token（`ink`/`amber` 等），禁止硬编码色值。
- 手动切换持久化到 localStorage，刷新保持（AC）。
