# syntax=docker/dockerfile:1

# ---------- 构建阶段 ----------
# 与 go.mod 的 go 指令保持一致：本项目依赖 Go 1.21 的 http.ServeMux 前缀路由行为，
# 升到 1.22 会改变路由语义（`{token}` 通配可用），需同步改动 handler。
#
# --platform=$BUILDPLATFORM 让构建阶段始终跑在 runner 的原生架构上，
# 由 Go 自己去交叉编译到 TARGETARCH。这样多架构镜像不需要 QEMU 模拟，
# arm64 构建不会退化成几十倍的模拟执行。
FROM --platform=$BUILDPLATFORM golang:1.21.4-alpine AS builder

# buildx 会注入这两个参数；不带 buildx 直接 docker build 时用默认值回退
ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /src

# 先只复制依赖清单，利用 Docker 层缓存：源码改动不会触发依赖重新下载
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# 版本号由构建参数注入，运行时可从首行日志读到（见 main.go 的 version 变量）
ARG VERSION=dev

# CGO_ENABLED=0 产出全静态二进制，才能运行在没有 libc 的 distroless/static 上
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
        -trimpath \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -o /out/sms-server .

# ---------- 运行阶段 ----------
# distroless/static: 无 shell、无包管理器，攻击面最小；已内置 CA 证书与时区数据
# （时区数据必需：MYSQL_DSN 带 loc=Local，且 ReceivedAt 取本地时间）。
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="sms-server" \
      org.opencontainers.image.description="短信验证码中继服务" \
      org.opencontainers.image.source="https://github.com/AngelSnow1129/sms-server"

COPY --from=builder /out/sms-server /sms-server

# 镜像自带 nonroot 用户（uid 65532），不使用 root 运行
USER nonroot:nonroot

# 与 config.Load() 的 SERVER_ADDR 默认值一致
EXPOSE 53340

# 运行期必需配置（无默认值，缺失即启动失败）：
#   MYSQL_DSN       MySQL 连接串，须带 parseTime=true
#   HMAC_SECRET     手机号 HMAC 密钥
#   WEBHOOK_SECRET  webhook 鉴权密钥
# 注意：本镜像不含 shell/curl，无法用 HEALTHCHECK 做进程内探活；
# 且服务当前未提供健康检查端点（见 README「已知限制」）。
ENTRYPOINT ["/sms-server"]
