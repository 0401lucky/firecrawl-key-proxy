# Firecrawl 自动注册模块（CF 邮箱 + 代理池）

## Goal

为 Firecrawl 反向代理项目增加一个**自动注册模块**：批量自动注册 Firecrawl 账号、自动完成邮箱验证、提取可用 API Key 并接入现有 Key 池。解决手动注册太慢的问题（当前用量不大，但注册一次成本高）。参考 [tavily-key-generator](https://github.com/skernelx/tavily-key-generator) 的 Firecrawl 注册实现。

## Background（已确认事实）

### 参考项目调研（tavily-key-generator）

- 注册流程：Camoufox 反检测浏览器打开 firecrawl.dev → Sign Up → 填邮箱+强密码（≥12 位，大小写/数字/特殊字符）→ 提交 → 智能检测注册结果（blocked 风控 / exists 已注册 / weak_password / sent 已发验证邮件）→ 轮询邮箱 API 获取**验证链接**（Firecrawl 用链接非验证码）→ 浏览器访问链接 → 自动登录 → 提取 `fc-` API Key（页面 HTML 正则 `fc-[a-zA-Z0-9_-]{20,}` 或导航 API Keys 页）→ 真实调用 `POST https://api.firecrawl.dev/v2/scrape` 验证 → 保存 `email,password,api_key`
- 关键文件：`firecrawl_browser_solver.py`（核心注册，495 行）、`mail_provider.py`（邮箱抽象，400 行）、`config.py`（env 加载）
- 上传机制：`POST {SERVER_URL}/api/keys` + `Authorization: Bearer {SERVER_ADMIN_PASSWORD}`，body `{"key","email","service"}`
- 强密码生成：`Tv{6位随机小写数字}!A` 之类（满足 ≥12 位含大小写数字特殊字符）

### 用户自建邮箱系统 = dreamhunter2333/cloudflare_temp_email（Temp Mail）

用户后台截图（lucky0506.shop：创建账号/启用随机子域名/AI 提取/Webhook）与官方文档吻合。API 已确认：

- 创建邮箱：`POST https://<worker域名>/admin/new_address`，header `x-admin-auth: <admin密码>`，body `{"enablePrefix": true, "name": "<前缀>", "domain": "lucky0506.shop", "enableRandomSubdomain": true}` → 返回 `{"jwt", "address", "address_id"}`
- 随机子域名：需 DNS 通配 MX 记录（用户已配置），文档推荐用于收件隔离；`RANDOM_SUBDOMAIN_DOMAINS` 需在 worker 变量中配置
- 查邮件：`GET /api/mails?limit=&offset=`，header `Authorization: Bearer <地址JWT>`；或 `GET /admin/mails?address=`，header `x-admin-auth`。**返回原始 RFC822 raw，需客户端解析**（Python `email` 标准库可解析）
- 注意：地址 JWT（`Authorization: Bearer`）与用户 JWT（`x-user-token`）是两套认证，勿混淆

### 现有 Go 代理对接点

- 面板已有 `POST /api/admin/upstream-keys`（session cookie 认证，body `{"name","api_key"}`，写库 + `pool.Reload()` 立即参与调度，AC8）
- 配置加载：`internal/config/config.go`，env 驱动（`ADMIN_PASSWORD` 等），缺必填项聚合报错
- 项目约束 #6：上游 Key 明文不落日志、DTO 只回 masked

## Requirements

- R1: 自动注册 Firecrawl 账号，流程对齐参考项目（Camoufox 反检测浏览器 + 注册结果智能检测 + 验证链接提取 + API Key 提取 + 真实调用验证）
- R2: 邮箱来源接入用户自建 Temp Mail（lucky0506.shop），创建邮箱走 `/admin/new_address`，**启用随机子域名**
- R3: 代理池支持：静态列表文件（每行一条 `http://user:pass@host:port` 或 `socks5://host:port`），每个注册任务轮换使用；代理失效时自动换下一个
- R4: 注册成功的 API Key 自动上传到 Go 代理 Key 池（走新接口，见 design）
- R5: 触发方式：CLI 为主（`--count --concurrency --delay`），另提供 `--server` 模式暴露轻量 HTTP 接口（预留面板/定时触发）
- R6: 账号信息（email,password,api_key）本地落盘保存，密码明文仅存本地文件，不上传

## Acceptance Criteria

- [ ] 单条命令可注册 N 个 Firecrawl 账号（N 可配，并发可配），全程无需人工介入
- [ ] 邮箱自动通过 Temp Mail admin API 创建，地址形如 `fc-xxx@<随机子域名>.lucky0506.shop`
- [ ] 验证链接自动提取并完成邮箱验证
- [ ] API Key 自动提取并真实调用 `/v2/scrape` 验证；验证结果区分「可用/不可用/网络不确定」
- [ ] 代理列表文件配置生效：http 与 socks5 均可用，注册失败可换代理重试
- [ ] 可用 Key 自动上传到 Go 代理 Key 池，面板可见（masked），参与调度
- [ ] 失败场景有明确日志：风控 blocked / 邮箱已存在 / 验证邮件超时 / 代理不可用，不静默吞掉

## Out of Scope

- Tavily / Exa 等其他服务注册
- Turnstile solver 服务（Firecrawl 注册不需要）
- 注册器 Web UI / 面板注册按钮（预留 HTTP 接口，本轮不做界面）
- 上传 key 的额度查询/初始化（沿用现有 refresh-credits 机制）

## Key Decisions（已与用户确认）

1. **形态**：独立 Python 注册器（`registerer/` 目录放本仓库），Go 侧加接收接口；不嵌 Go 进程
2. **触发**：CLI 为主 + `--server` 轻量 HTTP 接口；本轮 Go 侧不加注册界面
3. **部署**：本地 Windows 跑注册器（住宅 IP 注册成功率更高），Go 代理继续在 VPS；注册器通过公网调用面板接口上传
4. **代理池**：静态列表文件，支持 http/socks5，轮换使用

## Technical Notes

- Temp Mail 邮件为原始 RFC822，需用 Python `email.parser` 解析 subject/text/html 后提取验证链接
- Camoufox 基于 Playwright，context 支持 `proxy` 参数（http/https/socks5）
- 参考项目的 CF provider（`GET /messages?address=`）与 temp-mail API 不同，`mail_provider.py` 需重写适配，不能直接复用
- Firecrawl 验证邮件为 Clerk 体系（链接含 clerk/auth 关键字），提取逻辑可移植参考项目 `_extract_verification_link`

## Deployment Config（用户侧提供，写入 .env，不进代码）

- `TEMP_MAIL_API_URL`：Temp Mail worker 地址（用户后台域名）
- `TEMP_MAIL_ADMIN_PASSWORD`：admin 密码（`x-admin-auth`）
- `TEMP_MAIL_DOMAIN`：lucky0506.shop
- `REGISTER_API_URL` + `REGISTER_API_TOKEN`：Go 代理上传接口地址与 token
- `PROXY_FILE`：代理列表文件路径
