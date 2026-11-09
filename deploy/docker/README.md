# Arkade 容器化调试部署

在一台装有 Docker 的机器上，用 `docker compose` 把 Hub 与 Agent 一起跑起来，验证从
「浏览器 → Hub → Agent → 本机容器」的完整链路。

> **这不是生产部署模板。** 生产形态是：Hub 单独一台，Agent 部署在每台被管服务器上
> （见 `deploy/arkade-agent.service`）。这里的 Agent 挂载宿主 `docker.sock`，只用于本机自测。

## 目录

| 文件 | 说明 |
| --- | --- |
| `docker-compose.yml` | Hub + Agent 两个服务，含健康检查与依赖顺序 |
| `hub.config.toml` | Hub 容器内配置（路径均为容器内路径） |
| `agent.config.toml` | Agent 容器内配置（`hub_url` 用服务名 `hub`） |
| `.env.example` | 端口、密码、令牌等环境变量样例 |
| `../../hub/Dockerfile` | Hub 镜像：先构建前端产物，再构建 Go 二进制 |
| `../../agent/Dockerfile` | Agent 镜像：静态 Go 二进制 |

## 使用

```bash
cd <仓库根目录>

# 1. 准备环境变量（必须改掉两个凭据）
cp deploy/docker/.env.example deploy/docker/.env

# 2. 构建并启动
docker compose -f deploy/docker/docker-compose.yml --env-file deploy/docker/.env up -d --build

# 3. 看状态
docker compose -f deploy/docker/docker-compose.yml --env-file deploy/docker/.env ps
curl http://127.0.0.1:8080/healthz

# 4. 浏览器打开 http://<机器IP>:8080，用 .env 里的管理员账号登录
```

## 清理（不留残留）

```bash
docker compose -f deploy/docker/docker-compose.yml --env-file deploy/docker/.env down -v --rmi local
```

`-v` 删除命名卷（数据库与 Agent 身份），`--rmi local` 删除本次构建的镜像。
默认卷名是 `arkade-debug-*`、网络名 `arkade-debug-net`，清理后可用下面的命令核验：

```bash
docker ps -a --filter name=arkade
docker volume ls --filter name=arkade
docker network ls --filter name=arkade
docker images --filter reference='arkade-*'
```

## 关键设计说明

### 构建上下文必须传仓库根目录

`docker-compose.yml` 里的 `context: ../..` 不是笔误：`hub/` 与 `agent/` 都通过
`replace` 引用了同级的 `protocol/` 模块，只把子目录拷进镜像会找不到该模块。

### 密码与令牌只走环境变量

配置文件会被挂载进容器、随卷备份，明文写进去等于把凭据永久留在磁盘。
`hub.config.toml` 与 `agent.config.toml` 里的 `agent_token`、`initial_pass` 都留空，
由 `ARKADE_AGENT_TOKEN` / `ARKADE_INITIAL_PASS` 注入。

### Agent 以 root 运行

容器里要读宿主 `/var/run/docker.sock`，该 socket 属主是宿主 `root:docker`，
非 root 用户默认无权限。更严格的做法是用 `group_add` 映射宿主 docker 组 GID，
但那要求部署时知道宿主 GID，跨机器会失效，因此调试环境先用 root。

### agent 服务依赖 hub 健康

`depends_on: condition: service_healthy` 让 Agent 等 Hub 真正健康后再启动，
避免开局进入重连退避循环、日志被刷满（重连退避是 1s→2s→4s…，刷起来很快）。
