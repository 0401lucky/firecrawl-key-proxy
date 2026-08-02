# 多阶段构建：前端(Vite) → 后端(Go) → 精简运行镜像。
# 构建产物由阶段 1 在镜像内重建，宿主机上的 internal/webui/dist 被 .dockerignore
# 排除，杜绝「COPY . . 把陈旧前端带进镜像」的问题。
#
# 阶段 2 依赖层与源码层分离：只改 Go 源码时命中 go mod 缓存，只改前端源码时
# 命中 npm ci 缓存，避免每次全量重装依赖。

# ---- 阶段 1：前端构建 ----
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build          # 产物 → /src/internal/webui/dist

# ---- 阶段 2：后端编译 ----
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 用镜像内构建的前端产物覆盖（即使 .dockerignore 已排除，双保险）。
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---- 阶段 3：运行 ----
FROM alpine:3
# ca-certificates 必需：程序以 HTTPS 访问 api.firecrawl.dev，Alpine 默认不含根证书，
# 缺失表现为 x509: certificate signed by unknown authority。
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -u 10001 app \
 && mkdir -p /data \
 && chown app:app /data     # 必须在 USER app 之前，否则无权限
COPY --from=build /out/server /usr/local/bin/server
USER app
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/server"]
