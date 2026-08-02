# C7 设计 — 容器化与部署

## 三阶段构建

```dockerfile
# ---- 阶段 1：前端 ----
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build          # 产物 → /src/internal/webui/dist

# ---- 阶段 2：后端 ----
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---- 阶段 3：运行 ----
FROM alpine:3
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -u 10001 app \
 && mkdir -p /data && chown app:app /data
COPY --from=build /out/server /usr/local/bin/server
USER app
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/server"]
```

两处顺序不能颠倒：

- 阶段 2 的 `COPY . .` 会把宿主机的 `internal/webui/dist`（可能是陈旧的占位文件）带进来，因此**必须**紧接着用 `COPY --from=web` 覆盖。`.dockerignore` 排除该目录也能达到同样效果，两者都做更稳。
- `chown /data` 必须在 `USER app` 之前，否则没有权限。

`ca-certificates` 是必需的——程序要以 HTTPS 访问 `api.firecrawl.dev`，Alpine 基础镜像默认不含根证书，缺了会表现为 `x509: certificate signed by unknown authority`。

## compose

```yaml
services:
  proxy:
    build: .
    restart: unless-stopped
    environment:
      PUBLIC_BASE_URL: ${PUBLIC_BASE_URL}
      ADMIN_PASSWORD: ${ADMIN_PASSWORD}
      DB_PATH: /data/proxy.db
      # 其余项按需覆盖，默认值见 README
    volumes:
      - proxy-data:/data
    expose:
      - "8080"

  caddy:
    image: caddy:2-alpine
    restart: unless-stopped
    profiles: ["tls"]
    ports: ["80:80", "443:443"]
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy-data:/data
      - caddy-config:/config

volumes:
  proxy-data:
  caddy-data:
  caddy-config:
```

`proxy` 用 `expose` 而非 `ports`，默认不向宿主机暴露。启用 TLS 网关：`docker compose --profile tls up -d`。不想用内置 Caddy 的用户，改成 `ports: ["127.0.0.1:8080:8080"]` 并自行配置外部网关——README 中给出这段替换说明。

## 镜像体积

静态 Go 二进制约 15–20 MB（含 SQLite 纯 Go 实现），Alpine 基础约 8 MB，前端产物约 200 KB。`-ldflags="-s -w"` 去掉符号表与调试信息可省几 MB。总计落在 50 MB 目标内。

不用 `scratch`：需要 `ca-certificates` 与 `wget`（HEALTHCHECK 用），且出问题时能 `docker exec` 进去看一眼的价值，高于省下的 8 MB。

## 数据与备份

全部状态在 `/data/proxy.db` 一个文件里。备份即备份 `proxy-data` 卷：

```bash
docker compose exec proxy sh -c 'cat /data/proxy.db' > backup.db
```

WAL 模式下热备份可能不完整。README 中建议先 `docker compose stop proxy` 再复制，或直接接受「丢失最后几秒计数」的风险——Key 与配置这类关键数据是同步写的，不会丢。
