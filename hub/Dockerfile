# Arkade Hub 镜像（多阶段构建：前端产物 + Go 二进制）
#
# 构建上下文必须是**仓库根目录**，不是 hub/：
#   - hub 通过 replace 引用了同级的 protocol 模块，只拷 hub/ 会找不到；
#   - 前端产物需要一并打进镜像，由 Hub 静态托管。
#
# 用法（在仓库根目录执行）：
#   docker build -f hub/Dockerfile -t arkade-hub:dev .

# ---------- 阶段一：前端构建 ----------
FROM node:22-alpine AS webbuild

WORKDIR /src/web

# 先装依赖，利用层缓存：只要锁文件没变就不用重新下载。
COPY web/package.json web/pnpm-lock.yaml ./
RUN corepack enable && pnpm install --frozen-lockfile

COPY web/ ./
# build 脚本内部会先跑 vue-tsc --noEmit，类型错误会直接让镜像构建失败。
# 这是刻意的：不允许把类型错误的前端打进镜像。
RUN pnpm build

# ---------- 阶段二：Go 构建 ----------
FROM golang:1.24-alpine AS gopkg

WORKDIR /src

COPY protocol/go.mod protocol/
COPY hub/go.mod hub/go.sum hub/

WORKDIR /src/hub
RUN go mod download

COPY protocol ./../protocol
COPY hub ./

# CGO_ENABLED=0：静态二进制，运行阶段不需要 glibc。
# -trimpath 去掉本机路径，-s -w 去掉调试符号。
ARG VERSION=0.1.0
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/arkade-hub ./cmd/hub

# ---------- 阶段三：运行 ----------
FROM alpine:3.20

# ca-certificates：将来访问外部镜像仓库需要
# tzdata：日志按本地时区显示，排障更直观
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S arkade && adduser -S -G arkade arkade \
    && mkdir -p /data /app/web && chown -R arkade:arkade /data

WORKDIR /app

COPY --from=gopkg /out/arkade-hub /app/arkade-hub
COPY --from=webbuild /src/web/dist /app/web/dist

# Hub 文件目录，持久化 OIDC 加密主密钥；业务数据由独立 PostgreSQL 保存
VOLUME ["/data"]

USER arkade

EXPOSE 8080

# 健康检查打内置 /healthz：它同时验证数据库可读，比单纯端口探测更可信。
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1

ENTRYPOINT ["/app/arkade-hub"]
# 默认使用环境变量；需要 TOML 时可覆盖为 -c <path>。
CMD []
