# 多阶段构建：编译阶段用完整 Go 工具链，运行阶段只留二进制。
#
# 好处：最终镜像不含 Go 工具链与源码，体积从 ~800MB 降到 ~20MB，
# 攻击面也小得多（容器里没有编译器、没有 shell 工具链）。

# ---- 编译阶段 ----
# 版本必须 >= go.mod 里声明的 go 指令，否则 go mod download 直接报错。
FROM golang:1.26-alpine AS builder

WORKDIR /build

# 国内网络下 proxy.golang.org 不可达，切到 goproxy.cn。
# 用 ARG 而非硬编码：海外构建时可覆盖为默认源。
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}

# 先只拷依赖清单并下载：依赖不变时这层可复用缓存，
# 改业务代码不会触发重新下载依赖。
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 生成静态链接二进制，才能跑在 scratch/alpine 上。
# -trimpath 去掉构建机的绝对路径（避免泄露目录结构，也让构建可复现）。
# -ldflags "-s -w" 去掉符号表与调试信息，二进制更小。
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /build/api \
    ./cmd/api

# ---- 运行阶段 ----
FROM alpine:3.20

# ca-certificates：HTTPS 出站请求需要（如后续接入第三方服务）。
# tzdata：日志时间戳按本地时区显示。
RUN apk add --no-cache ca-certificates tzdata

# 非 root 运行：容器被攻破时限制影响范围。
RUN adduser -D -u 10001 appuser

WORKDIR /app

COPY --from=builder /build/api /app/api

# 头像目录默认落在工作目录下（AVATAR_DIR=data/avatars），
# 需把 /app 属主交给 appuser，否则非 root 运行时会因无写权限启动失败。
RUN mkdir -p /app/data/avatars && chown -R appuser:appuser /app

USER appuser

EXPOSE 8080

# 用 /healthz 做容器级健康检查（不碰数据库，只判断进程存活）。
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/app/api"]
