# syntax=docker/dockerfile:1
# PortTunnel 服务端镜像（Web 管理台已预构建并嵌入二进制）

# ---------- 构建阶段 ----------
FROM golang:1.21-alpine AS builder

WORKDIR /src
ENV CGO_ENABLED=0

# 先拷依赖清单，利用 Docker 层缓存
COPY go.mod go.sum ./
RUN go mod download

# 只拷编译所需目录（internal/server/web/dist 已包含内嵌的 Web 资源）
COPY cmd ./cmd
COPY internal ./internal

# 版本号可通过 --build-arg VERSION=0.4.0 注入
ARG VERSION=dev
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/ptunnel-server ./cmd/ptunnel-server

# ---------- 运行阶段 ----------
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata curl \
    && addgroup -S ptunnel \
    && adduser -S -D -H -G ptunnel -h /opt/ptunnel -s /sbin/nologin ptunnel \
    && mkdir -p /opt/ptunnel/data \
    && chown -R ptunnel:ptunnel /opt/ptunnel

WORKDIR /opt/ptunnel

COPY --from=builder /out/ptunnel-server /usr/local/bin/ptunnel-server
COPY --chown=ptunnel:ptunnel server.yaml /opt/ptunnel/server.yaml

USER ptunnel
ENV TZ=Asia/Shanghai

# 隧道端口、管理端口、代理端口范围
EXPOSE 8443 9443 20000-30000

# CA、证书、SQLite、SQL 审计文件都落在 data 目录，务必挂载持久卷
VOLUME ["/opt/ptunnel/data"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -fsSk https://127.0.0.1:9443/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/ptunnel-server"]
CMD ["-c", "/opt/ptunnel/server.yaml"]