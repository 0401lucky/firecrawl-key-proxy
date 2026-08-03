# Firecrawl 自动注册模块 — 执行计划

## 实施清单（顺序执行）

### 阶段 A：Go 侧接收接口（先做，接口先定稿）

- [ ] A1. `internal/config/config.go` 新增 `REGISTER_TOKEN`（可选 env，测试补 case）
- [ ] A2. `internal/admin/register.go` 新增 `POST /api/register/keys` handler：
  - `X-Register-Token` 校验（常量时间比较），未配置 token → 503
  - body `{"name","api_key"}` 校验 → `store.UpstreamKeys.Create` + `pool.Reload()`（复用 `handleCreateUpstreamKey` 逻辑，抽公共函数避免重复）
  - 响应 201 + masked DTO；409 重复；401 未授权
- [ ] A3. `internal/admin/server.go` 路由挂载（不经过 SessionAuth，独立 token 中间件）
- [ ] A4. 测试：`internal/admin/register_test.go` — 未配置 token 503 / 错 token 401 / 正确 token 201 + 入池 / 重复 409 / masked 不泄明文

### 阶段 B：registerer/ Python 注册器（移植 + 适配）

- [ ] B1. `registerer/requirements.txt`（camoufox、requests）、`.env.example`、`README.md`
- [ ] B2. `registerer/config.py`：env 加载（TEMP_MAIL_API_URL / TEMP_MAIL_ADMIN_PASSWORD / TEMP_MAIL_DOMAIN / REGISTER_API_URL / REGISTER_API_TOKEN / PROXY_FILE / 超时与并发默认值）
- [ ] B3. `registerer/mail.py`：
  - `create_email()`：`/admin/new_address` + enableRandomSubdomain + 地址 JWT 缓存
  - `wait_verification_link(email, timeout)`：轮询 `/admin/mails?address=` → `email.parser` 解析 raw → 提取验证链接（移植参考项目 `_extract_verification_link` 启发式，主 hint：clerk/firecrawl/auth/verify）
- [ ] B4. `registerer/proxies.py`：加载文件（每行一条，支持 `http://`/`socks5://`，可带 user:pass），round-robin 选择 + 失败剔除列表（进程内）
- [ ] B5. `registerer/firecrawl.py`：移植 `firecrawl_browser_solver.py` 核心：
  - 注册页导航/表单填写/提交、`wait_for_signup_result`（blocked/exists/weak_password/sent/stalled）
  - 验证链接访问、自动登录、API Key 提取（页面正则 + API Keys 导航 + 创建）
  - Camoufox context 注入 proxy；单次注册内失败换代理重试（≤2 次）
- [ ] B6. `registerer/uploader.py`：`POST {REGISTER_API_URL}/api/register/keys` + `X-Register-Token`；401/409 语义日志
- [ ] B7. `registerer/accounts.py`：CSV 落盘（线程锁追加）
- [ ] B8. `registerer/cli.py`：argparse — `--count --concurrency --delay --headed --no-upload --proxy-file`
- [ ] B9. `registerer/server.py`：`--server` 模式（http.server 线程版，`POST /register` 触发批量注册，单 worker 串行，防重入锁）
- [ ] B10. `registerer/__init__.py`

### 阶段 C：验证

- [ ] C1. `go build ./... && go vet ./... && go test ./...`（Go 侧全绿）
- [ ] C2. Python 单测（轻量）：`proxies.py` 轮换、`mail.py` RFC822 解析样例、`config.py` 加载（pytest 或纯函数断言）
- [ ] C3. **真实端到端 1 次**：本地配置 .env（用户提供 temp-mail 地址/密码、代理列表）→ 注册 1 个账号 → 确认：邮箱创建（随机子域名）、验证邮件收到、key 提取、上传后面板可见
- [ ] C4. `go test -race`（若有 gcc）确认新 handler 无并发问题

## 验证命令

```bash
# Go
CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...

# Python（registerer 目录内）
python -m pytest -q            # 若有单测
python -m registerer --count 1 --headed   # 冒烟（真实注册）

# 手动验证上传接口
curl -X POST https://<proxy>/api/register/keys \
  -H "X-Register-Token: <token>" \
  -H "Content-Type: application/json" \
  -d '{"name":"manual-test","api_key":"fc-test-xxx"}'
```

## 风险文件 / 回滚点

- `internal/admin/server.go`（路由挂载）：改动小，回归靠现有 admin_test.go
- `registerer/firecrawl.py`：选择器脆弱，移植时保留多选择器回退；失败路径必须有日志
- 回滚：Go 侧删接口/不配 token 即恢复原状；registerer/ 独立目录，删除无副作用

## 部署步骤（用户操作，任务完成后整理进 README）

1. 服务器：`.env` 加 `REGISTER_TOKEN=xxx` → `docker compose up -d proxy` 重启
2. 本地：`registerer/.env` 填 temp-mail 地址/密码、`REGISTER_API_URL=https://firecrawl.lucky0625.us.ci`、token、`PROXY_FILE`
3. `python -m registerer --count 3`

## 前置依赖（用户提供）

- Temp Mail worker 地址 + admin 密码（.env）
- 代理列表文件（至少 1 条可用 http/socks5 代理）
- 服务器端 REGISTER_TOKEN（自行生成，如 `openssl rand -hex 16`）
