# Arkade 容器部署：普通配置写编排，仅凭据使用 .env

## 选择部署方式

| 编排 | 独立凭据样例 | 场景 |
| --- | --- | --- |
| [hub-compose.yml](hub-compose.yml) | [hub.env.example](hub.env.example) | Hub + PostgreSQL，两个容器 |
| [agent-compose.yml](agent-compose.yml) | [agent.env.example](agent.env.example) | 只部署 Agent，连接已有 Hub |
| [combined-compose.yml](combined-compose.yml) | [combined.env.example](combined.env.example) | Hub + Agent + PostgreSQL，三个独立容器 |

服务器不需要克隆仓库或保留 `deploy/docker` 目录。每种部署只拿对应的一份 YAML 和一份凭据样例，将其分别保存为服务器任意部署目录中的 `compose.yml` 和 `.env`，即可独立部署。不要拼接三份样例或覆盖已有凭据。下文命令均在这个服务器部署目录中执行。

三套编排各自包含完整服务配置，不使用 extends 引用其他编排。合并编排直接声明 Hub 和 Agent 的镜像，服务间使用 `ws://hub:8080/agent/connect`。只有 Agent 挂载 Docker socket，Hub 不接触宿主 Docker。三套是替代方案，不要同时启动同名容器。合并部署并不意味着把两个进程塞进一个容器。

## 配置约定

- 镜像、端口、对外地址、日志、数据卷名、Agent 名等普通参数直接写在编排 YAML 中，只有密码、令牌通过 `.env` 注入。不再挂载或要求 TOML 配置文件。三套编排只拉取 `image`，不包含 `build`。
- `.env` 由 Docker Compose 读取，程序接收环境变量，不会自行加载磁盘上的 `.env`。下文显式使用 `--env-file`，不依赖 Compose 自动查找目录。
- 编排只映射服务需要的变量，不使用 `env_file` 将整份文件灌入容器：Agent 不应拿到管理员密码或 Hub 准入令牌。
- 容器内部协议和持久化布局固定：Hub 监听 `8080`，前端 `/app/web/dist`，OIDC 主密钥 `/data/settings.key`；PostgreSQL 数据 `/var/lib/postgresql/data`，仅内部网络开放 5432；Agent 数据 `/opt/arkade/agent`。改宿主端口不需要改内部监听或健康检查。
- 新数据库卷为 `arkade-postgres-data`，Hub 文件卷为 `arkade-hub-files-pg`，Agent 卷为 `arkade-agent-data`。此版本只支持全新 PostgreSQL，不读取或迁移旧 SQLite；不挂载旧 Hub 数据卷，不擅自删除旧文件。
- 会话期限、注册策略、OIDC 等网页设置保存在数据库。`ARKADE_SESSION_TTL` 仅保留旧配置回退，不能覆盖网页系统设置。
- 原生二进制仍可显式指定 `-c 路径.toml`；这只是兼容旧部署，不是新容器部署的必需项。

## 镜像版本：先确认，再部署

CI 在推送到 `master` 后构建 Hub、Agent 的 amd64 / arm64 镜像，同一次构建同时发布到 GHCR（`ghcr.io/ayflying/arkade-hub`、`ghcr.io/ayflying/arkade-agent`）和 Docker Hub（`docker.io/ayflying/arkade-hub`、`docker.io/ayflying/arkade-agent`）。两处标签规则一致，包含 `latest` 和 `sha-<完整提交号>`，生产应固定标签或摘要。三套编排默认使用 Docker Hub 的 `ayflying/arkade-hub:latest`、`ayflying/arkade-agent:latest`；需要改用 GHCR 时直接修改 image 地址，其他参数不变。PostgreSQL 仍使用官方 `postgres:17-alpine`。

GitHub Actions 发布需要仓库 Secret `DOCKERHUB_TOKEN`，以及 Variable 或 Secret `DOCKERHUB_USERNAME`（当前为 `ayflying`）。若发布到组织命名空间，另设 Variable `DOCKERHUB_NAMESPACE`；默认使用登录用户名。PR 只构建、不登录或推送镜像。缺少发布凭据会明确失败，不假称双仓库发布完成。

**这套编排必须配合支持 PostgreSQL 的新 Hub 镜像。** 旧镜像的启动参数仍指向 TOML，不能直接删除挂载后继续用旧镜像。代码提交/本地验证不代表远端 CI、GHCR 或服务器已更新。

## 1. Hub 独立部署

将 hub-compose.yml 保存为服务器的 compose.yml，将 hub.env.example 保存为同目录 .env；已有凭据不要覆盖：

```sh
chmod 600 .env
# 编辑 .env：填写 ARKADE_DB_PASSWORD、ARKADE_INITIAL_PASS 和 ARKADE_AGENT_TOKEN。
# 编辑 compose.yml：核对 image、ports 和 ARKADE_BASE_URL。
docker compose --env-file .env -f compose.yml pull
docker compose --env-file .env -f compose.yml up -d
docker compose --env-file .env -f compose.yml ps
curl -fsS http://127.0.0.1:8080/healthz
```

默认浏览器地址 `http://服务器地址:8080`，改端口直接修改 compose.yml 中的 `ports` 和 `ARKADE_BASE_URL`。管理员初始密码不能为空，允许短密码，登录后请尽快修改；Hub 准入令牌仍至少 16 位。已有数据库不会因初始密码变量改变而重置账号。

编排不提供源码构建。开发者需要本地构建时，在仓库根目录单独执行，再直接修改编排中的 `image`：

```sh
docker build -f hub/Dockerfile -t arkade-hub:local .
```

## 2. Agent 独立部署

先在 Hub 网页点击“添加服务器”，保存该服务器专属接入密钥。将 agent-compose.yml 保存为服务器的 compose.yml，将 agent.env.example 保存为同目录 .env（不要覆盖 Hub 的凭据）：

```sh
chmod 600 .env
# 编辑 .env：填写 ARKADE_AGENT_TOKEN。
# 编辑 compose.yml：将 ARKADE_HUB_URL 占位地址改为可达的 Hub 地址，并核对 image。
docker compose --env-file .env -f compose.yml pull
docker compose --env-file .env -f compose.yml up -d
docker compose --env-file .env -f compose.yml ps
```

跨机器使用 `wss://域名/agent/connect`，内网可用 `ws://Hub地址:8080/agent/connect`。独立 Agent 容器的 `127.0.0.1` 是它自己，不能用来连接宿主 Hub；服务名 `hub` 也只在合并编排网络内有效。

Agent 的 Docker socket 权限等价于宿主高权限，仅部署到可信服务器。需要不同的宿主套接字路径时，直接修改编排中的 socket 挂载源路径。Podman 需单独验证权限和运行时兼容性，不把修改路径视为验证通过。

## 3. Hub + Agent 合并部署

将 combined-compose.yml 保存为服务器的 compose.yml，将 combined.env.example 保存为同目录 .env；其中 **Hub 准入令牌 `ARKADE_AGENT_TOKEN` 和本机 Agent 专属密钥 `ARKADE_LOCAL_AGENT_TOKEN` 必须分开**。

首次部署不能事先拿到网页生成的专属密钥，因此分两步启动：

```sh
chmod 600 .env
# 在 .env 填好 PostgreSQL 密码、Hub 密码、准入令牌，ARKADE_LOCAL_AGENT_TOKEN 暂留空。
docker compose --env-file .env -f compose.yml pull
docker compose --env-file .env -f compose.yml up -d hub
```

登录 Hub，添加本机服务器，把生成的专属密钥写入 `.env` 的 `ARKADE_LOCAL_AGENT_TOKEN`。再启动整个项目：

```sh
docker compose --env-file .env -f compose.yml up -d
docker compose --env-file .env -f compose.yml ps
```

Agent 等 Hub 健康后启动，通过 Compose 服务名连接，不需要填写宿主地址。专属密钥留空时 Agent 会拒绝启动，不会自动使用共享准入令牌降级注册。先起 Hub 的命令不启动 Agent。

只改 Agent 密钥或参数时：

```sh
docker compose --env-file .env -f compose.yml up -d --no-deps --force-recreate agent
```

## 升级、备份与停止

- 本次是全新 PostgreSQL 部署，不提供 SQLite 数据导入或旧库兼容。旧实例、数据文件和卷应由操作员另行保管，新版不尝试接管。
- PG 已有数据升级前使用 `pg_dump`，并在独立新库演练 `pg_restore`。OIDC 主密钥位于 Hub 文件卷，必须和数据库一起备份；丢失密钥将无法解密第三方登录凭据。
- 数据库密码仅初始化 PG 数据卷时生效。已有 PG 修改密码需要先执行受控的数据库角色改密，再同步 Hub 凭据；只改 .env 不会自动修改库中密码。
- 从独立编排切换到合并编排时，先核对项目归属、卷和凭据，不同时运行两个不同版本 Hub。不执行 `down -v`，不擅自接管未知服务。
- 配置变更后用相应编排 `up -d`，必要时只对目标服务 `--force-recreate`；不要无故重启其他业务容器。
- 停止用 `docker compose --env-file .env -f 对应编排.yml down`。**日常停止/升级不要加 `-v`，否则数据库和 Agent 身份数据会被删除。**
- `.env` 含密钥，设置权限并保留在部署机，不提交 Git，不分享展开密钥后的 `docker compose config` 输出；检查结构用 `config --quiet`。

## 验证与边界

在仓库根目录运行 `python3 scripts/check-deploy-compose.py` 可检查三套编排的解析、密钥隔离、数据路径、健康依赖、首次只启动 Hub、仅凭据插值和禁止 build；需要 Python 3 与 Docker Compose v2，使用临时虚构凭据，不启动容器、不读取真实 `.env`。

`docker compose config --quiet` 只能证明编排可解析，不能证明镜像已发布、Docker socket 可用、Agent 已接入或迁移业务端到端通过。上线还需核对健康版本、原账号、Agent 在线、原数据和日志。Agent 进程健康不等于已和 Hub 成功握手。
