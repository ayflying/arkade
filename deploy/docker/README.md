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

凭据只配在编排层（`.env`），程序配置文件里一律留空——配置只有一处来源，不用两头改。

```bash
cd <仓库根目录>

# 1. 准备凭据（只在这一次填写，之后启动不用再管）
cp deploy/docker/.env.example deploy/docker/.env
#    编辑 .env，填 ARKADE_INITIAL_PASS 与 ARKADE_AGENT_TOKEN
chmod 600 deploy/docker/.env

# 2. 构建并启动（compose 自动读取同目录的 .env，不需要 --env-file）
docker compose -f deploy/docker/docker-compose.yml up -d --build

# 3. 看状态
docker compose -f deploy/docker/docker-compose.yml ps
curl http://127.0.0.1:8080/healthz

# 4. 浏览器打开 http://<机器IP>:8080，用 .env 里的管理员账号登录
```

改动 `.env` 后需要重建容器才生效（环境变量在容器创建时注入）：

```bash
docker compose -f deploy/docker/docker-compose.yml up -d --force-recreate
```

## 清理（不留残留）

```bash
docker compose -f deploy/docker/docker-compose.yml down -v --rmi local
```

`-v` 删除命名卷（数据库与 Agent 身份），`--rmi local` 删除本次构建的镜像。
默认卷名是 `arkade-debug-*`、网络名 `arkade-debug-net`，清理后可用下面的命令核验：

```bash
docker ps -a --filter name=arkade
docker volume ls --filter name=arkade
docker network ls --filter name=arkade
docker images --filter reference='arkade-*'
```

## 凭据怎么流转

```
deploy/docker/.env            ← 唯一填写凭据的地方（已被 .gitignore 排除）
        │  compose 读取
        ▼
容器环境变量                   ← 启动时注入
  ARKADE_INITIAL_PASS
  ARKADE_AGENT_TOKEN
        │  程序启动时读取
        ▼
进程内存                       ← 不落盘
```

配置文件（`hub.config.toml` / `agent.config.toml`）里的 `agent_token`、`initial_pass`、
`token` 全部保持空字符串，它们只是占位，实际值由环境变量覆盖。

两个关键保障：

1. **缺凭据直接拒绝启动**：compose 用 `${VAR:?提示}` 语法在宿主侧校验，`.env` 没填就中止；
   Hub 自己也会校验（缺 `ARKADE_INITIAL_PASS` 时拒绝启动），不会带着空令牌跑起来。
2. **`.env` 不入库**：`.gitignore` 拦截 `.env`，只放行 `.env.example`。

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
