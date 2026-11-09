# Arkade Agent 镜像（多阶段构建）
#
# 构建上下文是仓库根目录（原因同 hub/Dockerfile：依赖共享 protocol 模块）。
#
# 用法（在仓库根目录执行）：
#   docker build -f agent/Dockerfile -t arkade-agent:dev .

# ---------- 构建阶段 ----------
FROM golang:1.24-alpine AS builder

WORKDIR /src

COPY protocol/go.mod protocol/
COPY agent/go.mod agent/go.sum agent/

WORKDIR /src/agent
RUN go mod download

COPY protocol ./../protocol
COPY agent ./

# 静态二进制：Agent 要能直接丢到任意 Linux 服务器运行，不依赖 glibc。
ARG VERSION=0.1.0
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags "-s -w -X arkade/agent/internal/cli.Version=${VERSION}" \
        -o /out/arkade-agent ./cmd/agent

# ---------- 运行阶段 ----------
FROM alpine:3.20

# ca-certificates：Agent 走 wss:// 时需要校验 Hub 证书
# tzdata：日志时区
# docker-cli + docker-cli-compose：编排功能必须走 docker compose 命令行。
#
#   为什么必须装 CLI 而不是只用 Go 直连 Docker API：
#   compose 的语义（依赖顺序、网络创建、profile、健康检查等待）大量存在于
#   CLI 里，用 API 复刻必然漏细节，且会随 compose 版本演进而失效。
#   **alpine 基础镜像里默认没有 docker，不装的话 compose.* 方法全部报
#   "exec: docker: executable file not found"，这一点已在 243 上实测确认。**
#
#   注意 docker-cli-compose 只是 CLI 插件，它依赖 docker-cli 提供 docker 主命令，
#   两个都得装（只装 compose 插件不会有 docker 可执行文件）。
RUN apk add --no-cache ca-certificates tzdata docker-cli docker-cli-compose \
    && addgroup -S arkade && adduser -S -G arkade arkade \
    && mkdir -p /opt/arkade/agent && chown -R arkade:arkade /opt/arkade

# compose 插件发现路径（踩过的坑，务必保留）。
#
# alpine 的 docker-cli-compose 把插件装在 /usr/libexec/docker/cli-plugins/，
# 而 docker CLI 默认只查 ~/.docker/cli-plugins 与 /usr/local/lib/docker/cli-plugins。
# 找不到时 `docker compose` 会报 "not a docker command"，非常误导人。
#
# 双保险：既设环境变量，也建软链——因为环境变量在子进程里可能被
# 清空（例如通过某些 su/sudo 或容器编排器启动），软链不依赖环境。
ENV DOCKER_CLI_PLUGIN_EXTRA_DIRS=/usr/libexec/docker/cli-plugins
RUN mkdir -p /usr/local/lib/docker/cli-plugins /root/.docker/cli-plugins \
    && for p in /usr/libexec/docker/cli-plugins/*; do \
           [ -e "$p" ] || continue; \
           ln -sf "$p" /usr/local/lib/docker/cli-plugins/; \
           ln -sf "$p" /root/.docker/cli-plugins/; \
       done \
    && docker compose version

WORKDIR /app

COPY --from=builder /out/arkade-agent /app/arkade-agent

# Agent 数据目录：存放 Hub 下发的 agent_id、容器归属登记、编排文件与备份
#
# 目录结构（见 internal/compose/workspace.go）：
#   /opt/arkade/agent/stacks/<项目名>/compose.yml
#   /opt/arkade/agent/backups/<项目名>/<时间戳>/
VOLUME ["/opt/arkade/agent"]

# 注意：以 root 运行是刻意的，不是为了省事。
#   - 容器里要读 /var/run/docker.sock，socket 属主是宿主机的 root:docker，
#     非 root 用户默认没有权限；
#   - 更严格的替代方案是把宿主机的 docker 组 GID 映射进来（group_add），
#     但那需要构建时知道宿主 GID，跨机器部署会失效。
# 生产环境若在意这一点，应改用 group_add + 匹配的 GID。
USER root

# 健康检查同时验证 Agent 进程与 compose 可用性。
#
# 只查进程存在是不够的：Agent 可能活着但 compose 插件路径坏了，
# 这种情况下编排功能整体不可用，而 healthcheck 仍显示健康——
# 那是最难排查的状态（"监控说好的，功能就是不工作"）。
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD pgrep -f arkade-agent >/dev/null && docker compose version >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/app/arkade-agent"]
CMD ["run", "-c", "/opt/arkade/agent/agent.toml"]
