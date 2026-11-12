# Arkade 容器部署：普通配置写编排，仅凭据使用 .env

## 选择部署方式

| 编排 | 独立凭据样例 | 场景 |
| --- | --- | --- |
| [hub-compose.yml](hub-compose.yml) | [hub.env.example](hub.env.example) | 只部署 Hub |
| [agent-compose.yml](agent-compose.yml) | [agent.env.example](agent.env.example) | 只部署 Agent，连接已有 Hub |
| [combined-compose.yml](combined-compose.yml) | [combined.env.example](combined.env.example) | Hub + Agent 同机部署，两个独立容器 |

每种部署只复制对应样例为自己的 `.env`，不要拼接三份样例或覆盖已有凭据。

三套编排各自包含完整服务配置，不使用 extends 引用其他编排。合并编排直接声明 Hub 和 Agent 的镜像，服务间使用 `ws://hub:8080/agent/connect`。只有 Agent 挂载 Docker socket，Hub 不接触宿主 Docker。三套是替代方案，不要同时启动同名容器。合并部署并不意味着把两个进程塞进一个容器。

## 配置约定

- 镜像、端口、对外地址、日志、数据卷名、Agent 名等普通参数直接写在编排 YAML 中，只有密码、令牌通过 `.env` 注入。不再挂载或要求 TOML 配置文件。三套编排只拉取 `image`，不包含 `build`。
- `.env` 由 Docker Compose 读取，程序接收环境变量，不会自行加载磁盘上的 `.env`。下文显式使用 `--env-file`，不依赖 Compose 自动查找目录。
- 编排只映射服务需要的变量，不使用 `env_file` 将整份文件灌入容器：Agent 不应拿到管理员密码或 Hub 准入令牌。
- 容器内部协议和持久化布局固定：Hub 监听 `8080`，数据库 `/data/hub.db`、前端 `/app/web/dist`；Agent 数据 `/opt/arkade/agent`。改宿主端口不需要改内部监听或健康检查。
- 默认卷名保持 `arkade-hub-data`、`arkade-agent-data`。卷名属于数据定位，不可随意修改。已有自定义数据库路径、挂载或项目名的部署，必须先检查 `docker inspect`，不能直接套新装命令。
- 会话期限、注册策略、OIDC 等网页设置保存在数据库。`ARKADE_SESSION_TTL` 仅保留旧配置回退，不能覆盖网页系统设置。
- 原生二进制仍可显式指定 `-c 路径.toml`；这只是兼容旧部署，不是新容器部署的必需项。

## 镜像版本：先确认，再部署

CI 在推送到 `master` 后构建 `ghcr.io/ayflying/arkade-hub`、`ghcr.io/ayflying/arkade-agent`，包括 amd64 / arm64。标签包含 `latest` 和 `sha-<完整提交号>`，生产应固定标签或摘要。

**这套无 TOML 编排必须配合支持纯环境变量启动的新镜像。** 旧镜像的启动参数仍指向 TOML，不能直接删除挂载后继续用旧镜像。代码提交/本地验证不代表远端 CI、GHCR 或服务器已更新。

## 1. Hub 独立部署

以下在仓库根目录执行；首次创建配置，已有 `.env` 不要覆盖：

```sh
cp deploy/docker/hub.env.example .env
chmod 600 .env
# 编辑 .env：填写 ARKADE_INITIAL_PASS 和 ARKADE_AGENT_TOKEN。
# 编辑 hub-compose.yml：核对 image、ports 和 ARKADE_BASE_URL。
docker compose --env-file .env -f deploy/docker/hub-compose.yml pull
docker compose --env-file .env -f deploy/docker/hub-compose.yml up -d
docker compose --env-file .env -f deploy/docker/hub-compose.yml ps
curl -fsS http://127.0.0.1:8080/healthz
```

默认浏览器地址 `http://服务器地址:8080`，改端口直接修改 Hub 编排中的 `ports` 和 `ARKADE_BASE_URL`。管理员初始密码至少 8 位，Hub 准入令牌至少 16 位。已有数据库不会因初始密码变量改变而重置账号。

编排不提供源码构建。开发者需要本地构建时，在仓库根目录单独执行，再直接修改编排中的 `image`：

```sh
docker build -f hub/Dockerfile -t arkade-hub:local .
```

## 2. Agent 独立部署

先在 Hub 网页点击“添加服务器”，保存该服务器专属接入密钥。将下列两个样例复制到独立目录（不要复用并覆盖 Hub 的 `.env`）：

```sh
mkdir -p agent-node
cp deploy/docker/agent-compose.yml agent-node/
cp deploy/docker/agent.env.example agent-node/.env
cd agent-node
chmod 600 .env
# 编辑 .env：填写 ARKADE_AGENT_TOKEN。
# 编辑 agent-compose.yml：将 ARKADE_HUB_URL 占位地址改为可达的 Hub 地址，并核对 image。
docker compose --env-file .env -f agent-compose.yml pull
docker compose --env-file .env -f agent-compose.yml up -d
docker compose --env-file .env -f agent-compose.yml ps
```

跨机器使用 `wss://域名/agent/connect`，内网可用 `ws://Hub地址:8080/agent/connect`。独立 Agent 容器的 `127.0.0.1` 是它自己，不能用来连接宿主 Hub；服务名 `hub` 也只在合并编排网络内有效。

Agent 的 Docker socket 权限等价于宿主高权限，仅部署到可信服务器。需要不同的宿主套接字路径时，直接修改编排中的 socket 挂载源路径。Podman 需单独验证权限和运行时兼容性，不把修改路径视为验证通过。

## 3. Hub + Agent 合并部署

在仓库根目录使用同一份 `.env`，其中 **Hub 准入令牌 `ARKADE_AGENT_TOKEN` 和本机 Agent 专属密钥 `ARKADE_LOCAL_AGENT_TOKEN` 必须分开**。

首次部署不能事先拿到网页生成的专属密钥，因此分两步启动：

```sh
cp deploy/docker/combined.env.example .env
chmod 600 .env
# 在 .env 填好 Hub 密码、准入令牌，ARKADE_LOCAL_AGENT_TOKEN 暂留空。
docker compose --env-file .env -f deploy/docker/combined-compose.yml pull
docker compose --env-file .env -f deploy/docker/combined-compose.yml up -d hub
```

登录 Hub，添加本机服务器，把生成的专属密钥写入 `.env` 的 `ARKADE_LOCAL_AGENT_TOKEN`。再启动整个项目：

```sh
docker compose --env-file .env -f deploy/docker/combined-compose.yml up -d
docker compose --env-file .env -f deploy/docker/combined-compose.yml ps
```

Agent 等 Hub 健康后启动，通过 Compose 服务名连接，不需要填写宿主地址。专属密钥留空时 Agent 会拒绝启动，不会自动使用共享准入令牌降级注册。先起 Hub 的命令不启动 Agent。

只改 Agent 密钥或参数时：

```sh
docker compose --env-file .env -f deploy/docker/combined-compose.yml up -d --no-deps --force-recreate agent
```

## 升级、备份与停止

- 升级前核对当前容器、镜像、挂载、数据库真实路径，执行 SQLite 一致性备份和恢复演练。参见 [重构后部署步骤](../../docs/18-manual-goframe-deployment.md)。不要直接复制运行中的 SQLite 主文件而漏掉 WAL。
- 从旧 TOML 迁移时，把普通自定义值转到编排的 environment，密码和令牌转到 `.env`，确认数据库仍指向原数据。新编排默认 `/data/hub.db`；若原库不是它，不可盲目启动。OIDC 主密钥默认在数据库旁，必须随数据库卷持久化并一起备份。
- 从独立编排切换到合并编排时，虽默认卷名相同，Compose 项目归属不同；需先备份并停止旧项目、移除旧容器（不删卷），核对挂载再启新项目。不能同时运行两个 Hub 访问同一库。不要自动接管未知部署。
- 配置变更后用相应编排 `up -d`，必要时只对目标服务 `--force-recreate`；不要无故重启其他业务容器。
- 停止用 `docker compose --env-file .env -f 对应编排.yml down`。**日常停止/升级不要加 `-v`，否则数据库和 Agent 身份数据会被删除。**
- `.env` 含密钥，设置权限并保留在部署机，不提交 Git，不分享展开密钥后的 `docker compose config` 输出；检查结构用 `config --quiet`。

## 验证与边界

在仓库根目录运行 `python3 scripts/check-deploy-compose.py` 可检查三套编排的解析、密钥隔离、数据路径、健康依赖、首次只启动 Hub、仅凭据插值和禁止 build；需要 Python 3 与 Docker Compose v2，使用临时虚构凭据，不启动容器、不读取真实 `.env`。

`docker compose config --quiet` 只能证明编排可解析，不能证明镜像已发布、Docker socket 可用、Agent 已接入或迁移业务端到端通过。上线还需核对健康版本、原账号、Agent 在线、原数据和日志。Agent 进程健康不等于已和 Hub 成功握手。
