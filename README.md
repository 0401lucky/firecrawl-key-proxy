# Firecrawl 多 Key 反向代理与管理面板

把多个 Firecrawl 账号的 API Key 聚合到一个入口，自动做**负载均衡、故障转移、冷却与额度耗尽感知**，并提供 Web 管理面板完成 Key 管理与调用方授权。

## 解决什么问题

Firecrawl 官方按账号发放额度，单账号额度用完、被限流（429）或失效（401/402）时，调用方只能干等或手动换 Key。本项目把这些 Key 收进一个 Key 池：

- **透明转发**：客户端只面对一个地址、一个 Key，无需感知背后有几个上游账号。
- **故障转移**：上游 429/402/401 时自动换下一个 Key 重试（可配重试上限），5xx/网络错误不惩罚 Key。
- **冷却感知**：429 且带 `Retry-After` 时按上游指示冷却，冷却中的 Key 自动跳过，到期自动恢复。
- **异步任务粘连**：`/v2/crawl` 提交的任务，后续轮询 `/v2/crawl/{id}` 始终命中同一个上游 Key，不会在账号间跳来跳去导致任务丢失。
- **管理面板**：总览额度池、录入/禁用/删除上游 Key、刷新额度、签发与吊销下游调用 Key。

## 架构

```
调用方 (SDK / curl / MCP 客户端)
      │  Authorization: Bearer <代理 Key>
      ▼
┌──────────────────────┐    ┌─────────────────────┐
│  本服务 (单二进制)     │───▶│ api.firecrawl.dev   │  多个上游账号
│  /v1 /v2 代理转发      │    └─────────────────────┘
│  /api/admin 面板 API  │       ▲
│  /  SPA 静态资源      │       │ 轮询选 Key · 402/401 换下一个
└──────────────────────┘       │ 429 冷却 · 5xx 不惩罚
      ▲
      │ 可选（--profile mcp）
┌─────┴──────────────────┐
│ mcp-gate (校验 token)  │
│   └▶ firecrawl-mcp     │  给只支持远程 MCP 的客户端用
└────────────────────────┘
```

单进程单端口：代理转发、面板 API、前端页面都由一个二进制提供。**TLS 与域名由前置网关（内置 Caddy 示例、你自己的 Nginx，或 Cloudflare Tunnel）处理，程序自身只监听 HTTP、不碰证书。**

## 快速开始（Docker）

前置：已安装 Docker（含 compose 插件）。

```bash
# 1. 准备配置
cp .env.example .env
#    编辑 .env：
#      PUBLIC_BASE_URL=http://127.0.0.1:8080   ← 对外访问地址
#      ADMIN_PASSWORD=换成强密码

# 2. 构建并启动
docker compose up -d
docker compose ps          # 等 proxy 变 healthy

# 3. 打开面板
#    浏览器访问 http://127.0.0.1:8080，用 ADMIN_PASSWORD 登录
```

首次面板使用：

1. **上游 Key 页** →「录入新 Key」→ 粘贴你的 Firecrawl API Key（`fc-...`），起个备注名。
2. 点该行的「**测试**」确认这个 Key 能用——它调的是额度接口，**不消耗 credits**，成功时直接显示剩余额度；失败时会区分「Key 无效」「额度耗尽」「触发限流」「连不上上游」。
3. **API Key 页** →「创建 API Key」→ 给调用方起名 → **弹窗里的一次性明文就是调用方要用的 Key**，关闭后无法再查看，请立即保存。
4. 把明文 Key 发给调用方，用下面的方式接入。

启用内置 Caddy TLS 网关（有域名时）：

```bash
# 编辑 Caddyfile.example，把 your-domain.com 换成真实域名并解析到本机
docker compose --profile tls up -d
```

不想用内置 Caddy：proxy 已把端口绑在宿主机的 `127.0.0.1:8080`，直接让你的
Nginx/Caddy 反代到它即可。端口号可用 `HOST_PORT` 环境变量改。

默认只绑回环地址、不绑 `0.0.0.0`，是为了防止面板登录的明文密码裸奔在公网。
确需局域网内其他机器直连时，把 `docker-compose.yml` 里的映射改成
`"0.0.0.0:8080:8080"`——但那样面板走的是明文 HTTP，只在可信内网这么做。

### 80/443 已被占用时：Cloudflare Tunnel

如果目标机器上 80/443 已被别的服务占着（k3s ingress、宝塔、其他 PaaS 节点等），
不要去抢端口。用 Cloudflare Tunnel 从内部反向连出去，零端口冲突且不用管证书：

```bash
# 一次性：安装 cloudflared 并登录，选中你的域名所在 zone
cloudflared tunnel login
cloudflared tunnel create <隧道名>

# ~/.cloudflared/config.yml
# tunnel: <隧道ID>
# credentials-file: /home/<user>/.cloudflared/<隧道ID>.json
# ingress:
#   - hostname: firecrawl.your-domain.com
#     service: http://localhost:8080
#   - service: http_status:404

cloudflared tunnel route dns <隧道ID> firecrawl.your-domain.com
sudo systemctl restart cloudflared
```

注意 `cloudflared tunnel route dns` 的 hostname 必须落在**登录时选中的那个 zone**里，
否则它会把整个域名当成该 zone 的子域，建出一条类似
`firecrawl.a.com.b.com` 的错误记录。要管多个 zone 就用 `--origincert` 指定
对应的证书文件。

## 客户端接入

统一把 SDK 的 base URL 指向 `PUBLIC_BASE_URL`，`api_key` 填面板签发的代理 Key。

### curl 示例

```bash
curl -X POST "$PUBLIC_BASE_URL/v2/scrape" \
  -H "Authorization: Bearer <面板签发的代理 Key>" \
  -H "Content-Type: application/json" \
  -d '{"url":"https://example.com","formats":["markdown"]}'
```

### Python SDK 示例

```python
from firecrawl import Firecrawl

app = Firecrawl(
    api_key="<面板签发的代理 Key>",
    api_url="https://your-domain.com",   # 换成你的 PUBLIC_BASE_URL
)

# 同步端点
r = app.scrape("https://example.com", formats=["markdown"])
print(r.markdown)

# 异步端点：提交 + 轮询，代理会保证轮询始终命中同一个上游 Key
job = app.start_crawl("https://example.com", limit=2)
status = app.get_crawl_status(job.id)
print(status.status, status.total)
```

Node / Go / Ruby / .NET SDK 同理，构造客户端时把 `apiUrl`（或等价参数）指向 `PUBLIC_BASE_URL`。

### MCP 接入

有些客户端（尤其手机端）不提供「自定义 Firecrawl 地址」的输入框，但支持 MCP。

**能起本地进程的客户端**（Claude Code、Codex、Cursor 等）直接用 stdio：

```json
{
  "mcpServers": {
    "firecrawl": {
      "command": "npx",
      "args": ["-y", "firecrawl-mcp"],
      "env": {
        "FIRECRAWL_API_URL": "https://your-domain.com",
        "FIRECRAWL_API_KEY": "<面板签发的代理 Key>"
      }
    }
  }
}
```

**只支持远程 MCP 的客户端**（如 Android 上的 RikkaHub，传输类型只有 Streamable HTTP / SSE）需要本项目自带的 MCP 端点：

```bash
# .env 里补三项后启动
#   MCP_PROXY_KEY=fcp_...            面板创建的调用 Key
#   MCP_ACCESS_TOKEN=$(openssl rand -hex 32)
#   MCP_HOST_PORT=8082
docker compose --profile mcp up -d
```

客户端填：传输类型 `Streamable HTTP`、地址 `https://your-mcp-domain/mcp`（末尾 `/mcp` 不能省）、自定义请求头 `X-MCP-Token: <MCP_ACCESS_TOKEN>`。

> **这道 token 校验不是可选的。** `firecrawl-mcp` 在 HTTP 模式下不接受客户端自带的 key（`x-firecrawl-api-key` / `Authorization` / `x-api-key` 实测全部被忽略），只认服务端环境变量里的 Key。裸暴露的 MCP 端点等于把额度池开放给任何知道 URL 的人，所以 compose 里在它前面加了一个校验 `X-MCP-Token` 的网关。

## 环境变量

| 变量 | 必填 | 默认值 | 说明 |
|---|---|---|---|
| `PUBLIC_BASE_URL` | ✅ | — | 对外可访问地址（含协议），用于响应体 URL 重写；SDK 的 `api_url` 也填它 |
| `ADMIN_PASSWORD` | ✅ | — | 面板管理员密码（无用户体系，单管理员） |
| `DB_PATH` | — | `/data/proxy.db` | SQLite 数据文件路径（compose 已指向命名卷） |
| `LISTEN_ADDR` | — | `:8080` | 监听地址（compose 场景一般不需要改） |
| `UPSTREAM_BASE_URL` | — | `https://api.firecrawl.dev` | 上游 Firecrawl 地址 |
| `PROXY_PATH_PREFIXES` | — | `/v1/,/v2/` | 代理转发的路径前缀（逗号分隔） |
| `MAX_FAILOVER_ATTEMPTS` | — | `3` | 单请求最多换 Key 重试次数（>0） |
| `DEFAULT_COOLDOWN_SECONDS` | — | `60` | 429 且无 `Retry-After` 时的冷却时长（秒） |
| `MAX_REQUEST_BUFFER_BYTES` | — | `8388608` | 请求体缓冲上限（8 MiB）。**超限请求单次转发、不重试、不做故障转移** |
| `JOB_ROUTE_TTL_HOURS` | — | `48` | 异步任务 → 上游 Key 的映射保留时长（小时） |
| `CREDIT_REFRESH_MINUTES` | — | `10` | 后台刷新上游额度的间隔（分钟），`0` 关闭。只刷新可用状态的 Key |
| `SESSION_TTL_HOURS` | — | `168` | 面板登录会话有效期（小时） |
| `LOG_LEVEL` | — | `info` | `debug` / `info` / `warn` / `error` |
| `HOST_PORT` | — | `8080` | 代理在宿主机回环地址上的端口 |
| `MCP_PROXY_KEY` | MCP 必填 | — | MCP 端点使用的调用 Key（面板创建） |
| `MCP_ACCESS_TOKEN` | MCP 必填 | — | 访问 MCP 端点必须携带的 `X-MCP-Token` |
| `MCP_HOST_PORT` | — | `8082` | MCP 网关在宿主机回环地址上的端口 |

必填项缺失时进程启动即退出，日志一次性列出全部缺失项。

## 数据与备份

全部状态（上游 Key、代理 Key 及其哈希、Key 状态、job 映射、会话）都在 SQLite 单文件 `/data/proxy.db` 中。备份即备份该卷：

```bash
docker compose stop proxy     # 先停服务，保证一致性
docker run --rm -v <project>_proxy-data:/data -v "$PWD":/backup alpine \
  sh -c 'cp /data/proxy.db /backup/proxy-backup.db'
docker compose start proxy
```

## 已知限制

- **不支持多副本部署**：Key 池状态与 job 映射都在本机 SQLite 与进程内存中，横向扩容需改造存储层（超出当前范围）。
- **请求体超限不重试**：超过 `MAX_REQUEST_BUFFER_BYTES` 的请求走单次转发路径，不做故障转移——避免大请求体在多 Key 间反复重传放大带宽。
- **上游额度为被动感知**：不做主动轮询额度做调度，Key 的状态由真实请求结果驱动（429/402/401 触发冷却或耗尽）。面板上的额度数字仅供展示，不参与选 Key。
- **轮询不按剩余额度加权**：可用 Key 之间均匀轮转。若各 Key 余额悬殊，余额少的会先撞一次 402 才被移出候选——结果正确，只是那一次请求会多花一跳。
- **面板无多用户/角色体系**：单管理员，`ADMIN_PASSWORD` 即全部。
- **程序不处理 TLS**：证书与域名由前置网关或 Cloudflare Tunnel 负责。
- **`go test -race` 需要 CGO**：无 C 编译器的开发机跑不了竞态检测，并发相关改动请在有 gcc 的环境验证。

## 本地开发

```bash
# 后端
go build ./... && go test ./...
# 前端（产物输出到 internal/webui/dist，由 go:embed 嵌入）
cd web && npm ci && npm run dev     # 开发热更新，/api 自动代理到 :8080
# 或生产构建后跑二进制
cd web && npm run build && cd .. && go run ./cmd/server
```
