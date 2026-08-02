# C7 — 容器化与部署

父任务：`08-02-firecrawl-key-proxy`。交付要求见父任务 `prd.md` R8。

## Goal

把项目打成可用一条命令部署与升级的 Docker 交付物，并写清运维文档。TLS 由前置网关处理，程序自身不碰证书。

## Requirements

### R7.1 Dockerfile

- 多阶段构建，三阶段：
  1. `node:22-alpine` — `cd web && npm ci && npm run build`，产物落到 `internal/webui/dist`
  2. `golang:1.22-alpine` — `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"`，复制上一阶段的前端产物
  3. `alpine:3` — 只含二进制、`ca-certificates`、时区数据
- 依赖安装层与源码层分离（先 `COPY package*.json` 再 `npm ci`，先 `COPY go.mod go.sum` 再 `go mod download`），使源码改动不触发依赖重装。
- 最终镜像以非 root 用户运行；`/data` 目录预置为该用户可写。
- 镜像内含 `HEALTHCHECK`，指向 `/healthz`。

### R7.2 docker-compose.yml

- `proxy` 服务：构建本镜像，环境变量注入配置，`/data` 挂载命名卷，`restart: unless-stopped`。
- 可选的 `caddy` 服务（用 profile 或注释形式提供），反代到 `proxy:8080`，自动签发证书。
- 敏感项（`ADMIN_PASSWORD`）从 `.env` 读取，提供 `.env.example` 且不提交真实 `.env`。
- 不把 `proxy` 的端口直接暴露到宿主机 `0.0.0.0`——默认只在 compose 网络内可达，由网关转发。若用户不用 compose 内的 Caddy，文档说明如何改为绑定 `127.0.0.1`。

### R7.3 Caddyfile.example

- 一份最小可用的 Caddy 配置：域名、`reverse_proxy proxy:8080`、自动 HTTPS。
- 注释说明需要把域名解析指向本机、80/443 需可达。

### R7.4 README.md

至少覆盖：

- 这是什么、解决什么问题。
- 快速开始：复制 `.env.example`、填 `ADMIN_PASSWORD` 与 `PUBLIC_BASE_URL`、`docker compose up -d`、打开面板、录入上游 Key、创建调用用的 API Key。
- 客户端如何接入：把 SDK 的 `api_url` 指向 `PUBLIC_BASE_URL`、`api_key` 填代理签发的 Key，给出 curl 与 Python SDK 两个例子。
- 全部环境变量表（同父任务 `design.md` §8）。
- 行为说明：故障转移规则、Key 状态含义、**请求体超过缓冲上限时不支持故障转移**这一已知限制。
- 升级方式：`docker compose pull/build && docker compose up -d`，数据在卷中不受影响。
- 备份：备份哪个卷、如何恢复。

### R7.5 `.dockerignore` 与 `.gitignore`

- `.dockerignore` 排除 `node_modules`、`internal/webui/dist`、`.git`、`*.db`，避免把宿主机产物带进构建上下文造成层缓存失效或产物污染。
- `.gitignore` 排除 `internal/webui/dist`（保留 `.gitkeep` 与占位 `index.html`）、`.env`、`*.db`、`bin/`。

## Acceptance Criteria

- [ ] **AC12**：`docker compose up -d` 后服务可用，面板可通过浏览器访问。
- [ ] **AC12 续**：录入一个上游 Key 与一个代理 Key 后，`docker compose down && docker compose up -d`，两者及 Key 状态完整保留。
- [ ] **AC14**：容器的 `HEALTHCHECK` 在启动后转为 `healthy`。
- [ ] 镜像最终大小在 50 MB 以内（Alpine + 静态二进制 + 前端产物的合理范围）。
- [ ] `docker compose config` 校验通过，无未定义变量告警。
- [ ] 只改 Go 源码后重新 `docker compose build`，前端与依赖层命中缓存，构建时间显著短于首次。
- [ ] 容器内 `whoami` 不是 root；`/data` 可写。
- [ ] 未设置 `ADMIN_PASSWORD` 时容器启动失败并在日志中指明缺失变量（而非以空密码启动）。
- [ ] 按 README 的快速开始逐步操作，一个未接触过本项目的人能在十分钟内跑通并用 curl 成功调用一次 `/v2/scrape`。
- [ ] `.env` 与 `*.db` 不会被 git 跟踪。

## Out of Scope

- CI/CD 流水线、镜像仓库推送。
- Kubernetes 清单。
- 多副本部署。当前设计基于本地 SQLite 与内存 Key 池，**不支持水平扩容**——这一点需在 README 中明确写出，避免有人误以为可以起多个副本。

## Notes

多副本的限制值得单独说明：Key 状态与轮询游标在内存中，多个副本各自维护一份，会导致同一个 Key 被并发超用、429 冷却状态不共享。若日后确有扩容需求，需要把 Key 池状态外置（Redis 或 SQLite 换成共享数据库并加锁），那是另一个项目级改造，不是配置调整。
