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
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S arkade && adduser -S -G arkade arkade \
    && mkdir -p /opt/arkade/agent && chown -R arkade:arkade /opt/arkade

WORKDIR /app

COPY --from=builder /out/arkade-agent /app/arkade-agent

# Agent 数据目录：存放 Hub 下发的 agent_id、容器归属登记、后续编排文件
VOLUME ["/opt/arkade/agent"]

# 注意：以 root 运行是刻意的，不是为了省事。
#   - 容器里要读 /var/run/docker.sock，socket 属主是宿主机的 root:docker，
#     非 root 用户默认没有权限；
#   - 更严格的替代方案是把宿主机的 docker 组 GID 映射进来（group_add），
#     但那需要构建时知道宿主 GID，跨机器部署会失效。
# 生产环境若在意这一点，应改用 group_add + 匹配的 GID。
USER root

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD pgrep -f arkade-agent >/dev/null || exit 1

ENTRYPOINT ["/app/arkade-agent"]
CMD ["run", "-c", "/opt/arkade/agent/agent.toml"]
