# registerer — Firecrawl 自动注册器

自动批量注册 Firecrawl 账号：自动创建 Temp Mail 随机子域名邮箱、自动完成邮箱验证、
提取 API Key 并上传到 Firecrawl 代理的 Key 池。流程移植自
[tavily-key-generator](https://github.com/skernelx/tavily-key-generator)。

## 工作原理

```
创建随机子域名邮箱          Camoufox 反检测浏览器            Temp Mail admin API
(Temp Mail admin API)  ──▶  firecrawl.dev 注册/验证    ◀──  轮询收验证链接
                                │
                                ▼
                    提取 fc- API Key → 真实调用 /v2/scrape 验证
                                │
                                ▼
              POST /api/register/keys（Go 代理，X-Register-Token）→ 进入 Key 池
```

- 邮箱：自建 [cloudflare_temp_email](https://github.com/dreamhunter2333/cloudflare_temp_email)
  的 `/admin/new_address` 接口，`enableRandomSubdomain: true` 生成随机子域名地址
  （需 DNS 通配 MX 记录，见项目文档「配置子域名邮箱」）
- 浏览器：Camoufox（Playwright 封装的反检测浏览器），Firecrawl 注册无需 Turnstile solver
- 代理：静态列表文件，每行一条 `http://user:pass@host:port` 或 `socks5://host:port`，
  轮换使用；风控/连接失败自动换代理重试（最多 3 次尝试）

## 安装

```bash
cd registerer
python -m venv .venv
.venv\Scripts\activate        # Windows；Linux/macOS: source .venv/bin/activate
pip install -r requirements.txt
```

首次运行 Camoufox 会自动下载浏览器（需可访问 GitHub 的网络）。

## 配置

```bash
cp .env.example .env
# 编辑 .env：
#   TEMP_MAIL_API_URL         Temp Mail worker 地址
#   TEMP_MAIL_ADMIN_PASSWORD  管理后台 admin 密码
#   TEMP_MAIL_DOMAIN          收件域名（lucky0506.shop）
#   REGISTER_API_URL          Go 代理地址（https://firecrawl.lucky0625.us.ci）
#   REGISTER_API_TOKEN        服务器 .env 的 REGISTER_TOKEN
#   PROXY_FILE                代理列表文件路径
```

Go 代理侧需在服务器 `.env` 配置 `REGISTER_TOKEN`（如 `openssl rand -hex 16`），
重启后 `POST /api/register/keys` 接口生效（未配置时接口返回 503）。

## 使用

```bash
# 注册 3 个账号，并发 2
python -m registerer --count 3 --concurrency 2

# 前台浏览器（排查风控，能看到页面）
python -m registerer --count 1 --headed

# 不上传，仅本地保存
python -m registerer --count 2 --no-upload

# HTTP 接口模式（供面板/定时触发）
python -m registerer --server --port 8899
curl -X POST http://127.0.0.1:8899/register -d '{"count": 3, "concurrency": 2}'
```

账号落盘 `accounts.csv`（email,password,api_key,status,time）。

## 结果状态

| status | 含义 | 是否换代理重试 |
|---|---|---|
| ok | 注册成功，Key 已验证/已上传 | - |
| blocked | Firecrawl 风控（Security check failed） | ✅ |
| stalled | 提交后停留在注册页 | ✅ |
| error | 浏览器/网络异常 | ✅（连接类错误会把该代理列入黑名单） |
| exists | 邮箱已被注册过 | ❌ |
| weak_password | 密码强度不足（理论不会发生） | ❌ |
| mail_timeout | 验证邮件超时 | ❌（检查通配 MX） |
| no_key | 页面提取不到 API Key | ❌ |

## 测试

```bash
python -m unittest discover -s tests -v
```

## 备注

- 注册器设计在本地 Windows 跑（住宅 IP 注册成功率更高）；VPS 上 headless 也可运行
- Firecrawl 页面选择器可能随改版失效：失败日志会给出具体状态，必要时 `--headed`
  前台观察后调整 `registerer/firecrawl.py` 的选择器
- 密码明文只存本地 `accounts.csv`，不上传（Go 代理侧只收 Key）
