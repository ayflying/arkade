# Arkade Agent

Arkade 的执行端：通过反向 WebSocket 连接 Hub，在授权后执行 Docker/Podman 容器与 Compose 操作。Agent 需要连接已有 Hub，本镜像不包含 Hub 或 PostgreSQL。

- **GitHub 源码**：https://github.com/ayflying/arkade
- **部署文档**：https://github.com/ayflying/arkade/blob/master/deploy/docker/README.md
- **问题反馈**：https://github.com/ayflying/arkade/issues
- **对应 Hub**：https://hub.docker.com/r/ayflying/arkade-hub

## 快速部署

先在 Hub 网页添加服务器，保存该服务器专属接入密钥。下载以下文件到服务器同一目录，分别保存为 `compose.yml` 和 `.env`：

- [Agent 独立编排](https://raw.githubusercontent.com/ayflying/arkade/master/deploy/docker/agent-compose.yml)
- [Agent 凭据样例](https://raw.githubusercontent.com/ayflying/arkade/master/deploy/docker/agent.env.example)

将专属密钥填入 `.env` 的 `ARKADE_AGENT_TOKEN`，将编排中的 `ARKADE_HUB_URL` 改为可达的 Hub 地址；跨机器推荐 `wss://域名/agent/connect`。

```sh
chmod 600 .env
docker compose --env-file .env pull
docker compose --env-file .env up -d
docker compose --env-file .env ps
```

## 安全与持久化

- 挂载 Docker socket 等价于授予宿主高权限，仅部署到可信服务器。
- Agent 数据卷保存身份和工作数据，升级保留数据卷，不执行 `down -v`。
- Agent 专属密钥不是 Hub 准入令牌，不要混用；不要把管理员密码或数据库密码传给 Agent。
- 容器自身健康不代表已成功连接 Hub，应从 Hub 页面核对在线状态。
- Podman 套接字、权限和 Compose 兼容性需要按实际宿主验证。

需要同机部署 Hub、Agent 与 PostgreSQL，参见 [合并编排](https://github.com/ayflying/arkade/blob/master/deploy/docker/combined-compose.yml) 与 [对应凭据样例](https://github.com/ayflying/arkade/blob/master/deploy/docker/combined.env.example)。

## 标签与架构

支持 Linux `amd64` 和 `arm64`。`latest` 随 GitHub 默认分支 `master` 更新；生产建议固定 `sha-<完整提交号>` 或镜像摘要。

镜像由 GitHub Actions 构建，同时推送 Docker Hub 和 GHCR；不是 Docker Hub Automated Builds。构建和发布记录见 https://github.com/ayflying/arkade/actions 。
