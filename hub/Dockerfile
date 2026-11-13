# Arkade Hub 镜像（多阶段构建：前端产物 + Go 二进制）
#
# 构建上下文必须是**仓库根目录**，不是 hub/：
#   - hub 通过 replace 引用了同级 protocol 和 agent 模块，只拷 hub/ 会找不到；
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
COPY agent/go.mod agent/go.sum agent/

WORKDIR /src/hub
RUN go mod download
WORKDIR /src/agent
RUN go mod download
WORKDIR /src/hub

COPY protocol ./../protocol
COPY agent ./../agent
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

# 内置本机执行面通过运行时 API 和 docker compose 操作宿主容器。
# 与 Agent 镜像保持相同 CLI 插件发现路径；镜像因此比纯控制面版本更大。
RUN apk add --no-cache ca-certificates tzdata docker-cli docker-cli-compose \
    && mkdir -p /data/local /app/web
ENV DOCKER_CLI_PLUGIN_EXTRA_DIRS=/usr/libexec/docker/cli-plugins
RUN mkdir -p /usr/local/lib/docker/cli-plugins /root/.docker/cli-plugins \
    && for p in /usr/libexec/docker/cli-plugins/*; do \
           [ -e "$p" ] || continue; \
           ln -sf "$p" /usr/local/lib/docker/cli-plugins/; \
           ln -sf "$p" /root/.docker/cli-plugins/; \
       done \
    && docker compose version

WORKDIR /app

COPY --from=gopkg /out/arkade-hub /app/arkade-hub
COPY --from=webbuild /src/web/dist /app/web/dist

# 单一文件数据目录：加密主密钥及本机执行工作区；业务数据由 PostgreSQL 保存
VOLUME ["/data"]

# 默认 root 是访问宿主 root:docker socket 与复用 Agent 工作卷的刻意选择。
# socket 权限等价于宿主高权限；仅控制中心部署应关闭 local 并不挂 socket。
USER root

EXPOSE 8080

# 健康检查打内置 /healthz：它同时验证数据库可读，比单纯端口探测更可信。
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1

ENTRYPOINT ["/app/arkade-hub"]
# 默认使用环境变量；需要 TOML 时可覆盖为 -c <path>。
CMD []
