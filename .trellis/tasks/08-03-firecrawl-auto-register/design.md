# Firecrawl 自动注册模块 — 技术设计

## 1. 架构总览

```
┌─────────────── 本地 Windows ───────────────┐      ┌──────────── VPS ────────────┐
│  registerer/（Python + Camoufox）           │      │  Go 代理（不变）             │
│                                             │      │                              │
│  CLI: python -m registerer --count 5        │      │  POST /api/register/keys     │
│  ┌──────────────┐   ┌──────────────────┐    │ HTTP │  (X-Register-Token 认证)     │
│  │ 邮箱创建      │──▶│ Firecrawl 注册    │────┼─────▶│  写库 + pool.Reload          │
│  │ temp-mail API│   │ Camoufox 自动化   │    │      │                              │
│  └──────────────┘   │ 验证链接/API Key  │    │      │  SQLite upstream_keys        │
│                     └──────────────────┘    │      │                              │
│  代理池: proxies.txt (http/socks5 轮换)      │      │                              │
│  账号落盘: accounts.csv                      │      │                              │
└─────────────────────────────────────────────┘      └──────────────────────────────┘
```

边界：
- 注册器是**独立进程**，不依赖 Go 代理也能跑（`--upload` 可关）
- Go 侧**只新增一个上传接口**，不感知注册逻辑
- 注册器通过公网 HTTPS 调用 Go 接口（走 Cloudflare Tunnel 域名或直连）

## 2. registerer/ 目录结构（本仓库新增）

```
registerer/
  __init__.py
  cli.py              # argparse 入口：register / server 子命令
  config.py           # env 加载（参考项目 config.py 简化版）
  mail.py             # Temp Mail provider：创建邮箱 + 轮询邮件 + RFC822 解析
  firecrawl.py        # Firecrawl 注册流程（移植 firecrawl_browser_solver.py）
  proxies.py          # 代理列表加载 + 轮换选择
  uploader.py         # 上传 Key 到 Go 代理
  accounts.py         # 账号落盘（CSV，线程安全追加）
  server.py           # --server 模式：HTTP 接口触发注册
  requirements.txt
  README.md
  .env.example
```

## 3. 数据流（一次注册）

1. **邮箱**：`mail.create_email()` → `POST {TEMP_MAIL_API_URL}/admin/new_address`，body `{"enablePrefix": true, "name": "fc-<rand8>", "domain": "{TEMP_MAIL_DOMAIN}", "enableRandomSubdomain": true}`，header `x-admin-auth` → 返回 `address`（形如 `fc-xxxx@ab12cd34.lucky0506.shop`）与地址 JWT
2. **代理**：`proxies.next()` 从列表轮换选一条（round-robin + 失败剔除重试）
3. **注册**：Camoufox context 带 `proxy={server, username, password}` → 打开 firecrawl.dev → Sign Up → 填邮箱/密码 → 提交 → `wait_for_signup_result`（blocked/exists/weak_password/sent 判定，移植参考项目）
4. **验证**：轮询 `GET {TEMP_MAIL_API_URL}/admin/mails?address={email}`（x-admin-auth）→ Python `email.parser` 解析 raw → 提取 firecrawl/clerk 验证链接（移植 `_extract_verification_link` 启发式）
5. **激活**：Camoufox 访问验证链接 → 若跳登录页则自动填邮箱密码登录 → 提取 `fc-` API Key（页面正则 + API Keys 页导航 + 必要时创建）
6. **验证 Key**：`POST https://api.firecrawl.dev/v2/scrape`（Bearer key）→ 200 可用；区分 401/403（不可用）、网络异常（不确定但保存）
7. **上传**（可选）：`POST {REGISTER_API_URL}/api/register/keys`，body `{"name": "auto-<email前缀>", "api_key": "fc-..."}`，header `X-Register-Token`
8. **落盘**：`accounts.csv` 追加 `email,password,api_key,status,timestamp`（线程锁）

## 4. Go 侧新增接口

### `POST /api/register/keys`

- 认证：`X-Register-Token: <REGISTER_TOKEN>`（新 env，**非必填**；未配置则接口 503「未启用注册接入」）
- body：`{"name": string, "api_key": string}`（与现有 `handleCreateUpstreamKey` 相同结构）
- 行为：写 `store.UpstreamKeys` + `pool.Reload()`（复用现有逻辑，抽公共函数或独立 handler）
- 响应：201 + upstreamKeyDTO（masked）；409 重复 key；401 未授权
- 路由：挂 admin.Server 的 mux（不经过 SessionAuth；token 认证独立中间件）
- 日志：api_key 经 `logging.MaskKey`，符合约束 #6

### 配置（internal/config/config.go）

- 新增 `REGISTER_TOKEN`（可选字符串），为空时接口返回 503

## 5. 并发与代理轮换

- CLI 支持 `--concurrency N`（默认 2）：ThreadPoolExecutor，每个任务独立邮箱、独立代理
- 代理选择：round-robin 游标（线程锁）；单次注册内代理失败（连接失败/风控 blocked）→ 换下一个代理重试同一邮箱，最多重试 2 次
- 风控 blocked 与「代理不可用」分开处理：blocked 换代理重试；代理连接错误换代理重试；exists/weak_password 不重试（记日志）

## 6. 兼容性与回滚

- Go 侧新增接口与 env 均为可选，不配置 REGISTER_TOKEN 时行为与现在完全一致，零回滚风险
- registerer/ 是新增目录，不影响 `go build` / `go test`（Go 构建不扫描 .py）
- 上传接口与现有面板 API 并列，面板行为不变

## 7. 风险与缓解

| 风险 | 缓解 |
|------|------|
| Firecrawl 页面结构变化导致选择器失效 | 移植参考项目的多选择器回退策略；注册失败日志明确（不静默） |
| 随机子域名收不到信（MX/路由问题） | 部署前用一封测试邮件验证；验证邮件超时日志提示检查通配 MX |
| Camoufox 在 Windows 首次运行需下载浏览器 | 首次运行自动下载（camoufox 包机制），README 注明网络要求 |
| temp-mail admin API 返回 raw RFC822 格式变化 | 解析层独立成函数，字段缺失时日志提示 |
| 数据中心/代理 IP 被风控 | 代理轮换 + 失败换代理；headless 可关（--headed 调试） |
