# C7 实施计划 — 容器化与部署

## 前置

C1–C6 全部完成。前端可构建、后端可运行、所有测试通过。

## 步骤

1. **`.dockerignore` 与 `.gitignore`**
   先写这两个，否则首次构建会把 `node_modules` 整个塞进构建上下文，慢且可能污染产物。
   验证：`docker build` 的 "Sending build context" 大小在几 MB 量级。

2. **Dockerfile**
   按 `design.md` 的三阶段写。
   验证：`docker build -t fc-proxy .` 成功；`docker run --rm fc-proxy --help` 或直接带环境变量运行能起来；`docker image ls` 确认体积。

3. **非 root 与权限**
   验证：`docker run --rm -it --entrypoint sh fc-proxy -c 'whoami; touch /data/x'` — 输出 `app` 且 touch 成功。

4. **`.env.example` 与 `docker-compose.yml`**
   验证：`docker compose config` 无告警；`docker compose up -d` 后 `docker compose ps` 显示 healthy。

5. **持久化验证**
   起服务 → 面板录入一个上游 Key 与一个代理 Key → `docker compose down` → `up -d` → 确认数据仍在（AC12）。

6. **`Caddyfile.example` 与 TLS profile**
   验证：`docker compose --profile tls config` 通过。若有可用域名则实测一次自动签发；否则至少验证 Caddy 容器能启动并反代到 proxy。

7. **缺失必填变量的行为**
   验证：清空 `.env` 中的 `ADMIN_PASSWORD` 后 `up`，容器退出且日志指明缺失变量。

8. **README.md**
   按 `prd.md` R7.4 的六个部分写。**特别写清**：请求体超限不支持故障转移、不支持多副本部署这两条限制。
   验证：找一台干净机器（或清空本地所有卷与镜像），完全照 README 操作一遍，能跑通并用 curl 成功调用 `/v2/scrape`。这一步不能靠想象，必须实测。

9. **层缓存验证**
   改一行 Go 代码后重新 build。
   验证：npm 与 go mod 层命中缓存，构建时间显著短于首次。

## 验证命令

```bash
docker compose config
docker build -t fc-proxy .
docker image ls fc-proxy
docker compose up -d && docker compose ps
git status --porcelain          # 确认 .env / *.db / dist 未被跟踪
```

## 风险点

- **陈旧的 `internal/webui/dist` 被 `COPY . .` 带进镜像**，覆盖不及时会打包出旧前端。`.dockerignore` 排除 + `COPY --from=web` 覆盖，两道保险都要有。
- **缺 `ca-certificates`**，表现为访问 Firecrawl 时 `x509: certificate signed by unknown authority`。容易到部署后才发现。
- **`chown` 写在 `USER` 之后**，导致构建失败或 `/data` 不可写。
- **README 未实测**。文档里的命令跑不通是这类交付物最常见也最影响体验的缺陷，步骤 8 的实测不可跳过。
- **把 `proxy` 端口直接绑到 `0.0.0.0`**，绕过 TLS 网关，明文暴露面板登录。默认配置必须是不暴露。

## 回滚点

本任务只新增部署相关文件，不改动应用代码。放弃时删除这些文件，项目仍可用 `go build` + 手工运行的方式部署。
