# C6 实施计划 — 面板前端 SPA

## 前置

C5 完成并可本地运行——前端需要一个真实后端来联调。

## 步骤

1. **脚手架**
   `npm create vite@latest web -- --template vue-ts`，装 Tailwind 与 vue-router，配置 `build.outDir` 与开发期 `server.proxy`。
   验证：`npm run dev` 能打开空白页；`npm run build` 的产物落在 `internal/webui/dist`。

2. **`api/client.ts` 与 `api/types.ts`**
   按 C5 的契约逐个定义类型与请求函数。
   验证：`npm run build` 无类型错误。

3. **登录流程**
   `LoginView` + 路由守卫 + 401 自动跳转。
   验证：密码错误显示提示；正确后进入总览；刷新保持登录；登出回到登录页。

4. **`OverviewView`**
   额度池进度条、状态卡片、`usePolling`、倒计时。
   验证：冷却中的 Key 显示倒计时并在归零后转为可用；切到后台标签页请求停止。

5. **`UpstreamKeysView`**
   表格、新增表单、行操作、删除确认。
   验证：新增后列表立即出现（AC8 前端侧）；重置按钮仅在 `exhausted`/`invalid` 时可用。

6. **`ApiKeysView`**
   表格、创建流程、`RevealKeyDialog`、吊销确认。
   验证：AC9 前端侧——弹窗展示明文与复制，关闭后无处可查。

7. **主题与 Toast**
   验证：切换暗色后刷新保持；写操作成功与失败均有 Toast。

8. **构建产物接入**
   完整跑一遍 `npm run build && go build ./...`，运行二进制访问面板。
   验证：`go:embed` 无报错；三个页面均可用；`/keys` 直接访问不 404。

## 验证命令

```bash
cd web && npm ci && npm run build
cd .. && go build ./... && go vet ./...
```

## 风险点

- **`build.outDir` 配错**，产物落在 `web/dist`，`go:embed` 报 `no matching files`。这是本任务最常见的失败点，步骤 1 就要验证产物位置。
- **`emptyOutDir` 未开启**，旧产物残留导致嵌入的资源与 `index.html` 引用的哈希文件名对不上，表现为页面白屏。
- **倒计时用客户端时钟计算**，与服务端不同步。按 `design.md` 用服务端返回的剩余秒数。
- **明文 Key 被写入 store 或 URL**，破坏「只展示一次」的保证。
- **非 HTTPS 下 `navigator.clipboard` 不可用**导致复制按钮静默失效——必须有降级路径，否则用户会以为自己复制到了。

## 回滚点

前端全部位于 `web/` 与 `internal/webui/dist`。放弃时保留占位 `index.html`，后端仍可独立运行。
