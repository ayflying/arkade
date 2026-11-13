# Arkade 容器部署：Hub 内置本机执行面

## 选择部署方式

| 编排 | 独立凭据样例 | 场景 |
| --- | --- | --- |
| [hub-compose.yml](<hub-compose.yml>) | [hub.env.example](<hub.env.example>) | 仅 Hub（内置本机），连接用户已有 PostgreSQL |
| [agent-compose.yml](<agent-compose.yml>) | [agent.env.example](<agent.env.example>) | 远程服务器只部署 Agent，连接已有 Hub |
| [combined-compose.yml](<combined-compose.yml>) | [combined.env.example](<combined.env.example>) | Hub（内置本机）+ PostgreSQL，保留旧同机部署项目名和卷名 |

每种部署只需对应一份 YAML 和凭据样例，分别保存为服务器部署目录中的 `compose.yml` 与 `.env`；不需要克隆仓库、挂载 TOML 或拼接其他编排。三份编排均为独立、仅 `image` 的部署方案，不含 `build`、`extends` 或 `include`。Hub 独立编排仅声明 Hub，连接已有 PostgreSQL；combined 才提供 Hub 与 PostgreSQL 两个容器。两者都不额外运行同机 Agent，远程 Agent 仍独立运行。不要在同一宿主同时启动两套同名 Hub，不能同时写相同主密钥/工作卷或业务数据库。

**这是部署安全边界的变化：默认 Hub 现在会挂载宿主 Docker socket，以 root 运行并管理本机。** 本机执行复用 Agent 的能力，但不通过一个额外 Agent 容器或本机 WebSocket/token 接入。只需要控制中心、不管理本机的部署见下文“关闭本机管理”。

## 配置与持久化

- 普通参数直接修改编排：镜像、端口、域名、日志、卷名、本机开关和运行时等。`.env` 仅存 PostgreSQL 密码与初始管理员密码；远程 Agent 的独立 `.env` 仅存后台生成的专属接入密钥。
- `.env` 由 Compose 读取，不会自动注入全部键到容器；命令显式使用 `--env-file .env`。不要公开包含凭据的 `docker compose config` 完整输出，结构检查使用 `config --quiet`。
- 两份 Hub 编排默认 `ARKADE_LOCAL_ENABLED: "true"`、`ARKADE_LOCAL_DATA_DIR: /opt/arkade/agent`、`ARKADE_LOCAL_RUNTIME: auto`、`ARKADE_LOCAL_ENDPOINT: /var/run/docker.sock`、`ARKADE_LOCAL_COMPOSE_BIN: docker`。这些是编排字面量，改 `.env` 中同名变量不会覆盖它们。
- Hub 默认关闭共享准入：`ARKADE_AGENT_TOKEN: ""`。远程服务器在后台创建专属接入密钥，不需要填写全局令牌。本机不再需要 `ARKADE_LOCAL_AGENT_TOKEN`。
- Hub 两套方案保留显式卷名：设置主密钥 `arkade-hub-files-pg` → `/data`；本机身份、归属、Compose 文件与备份 `arkade-agent-data` → `/opt/arkade/agent`。combined 另管理 PostgreSQL `arkade-postgres-data` → `/var/lib/postgresql/data`；仅 Hub 编排不声明、不控制或删除数据库卷。切勿让独立 Agent 与 Hub 同时写同一工作卷。
- Hub 内部监听 `8080`，前端 `/app/web/dist`，设置主密钥 `/data/settings.key`。combined 的 PostgreSQL 只开放内部网络，不映射宿主 5432；仅 Hub 的已有数据库网络由部署者安排。改变外部端口无需改变内部监听或健康检查。
- 仅 Hub 改用项目隔离的默认网络（默认 `arkade-hub_default`），不再声明共享的 `arkade-hub-net`，避免与 combined 网络所有权冲突。旧网络不自动删除；如旧数据库只在该网络可见，必须先为 Hub 配置可达地址或明确的受控网络接入，不能假定跨 Compose 服务名自动解析。
- 本版本仅支持 PostgreSQL，不导入旧 SQLite。已有 PostgreSQL 按版本化迁移升级；不要将未知或 SQLite 文件挂入新库。
- 会话、注册、OIDC 等网页设置保存在数据库；`ARKADE_SESSION_TTL` 仅保留旧配置回退。原生二进制仍可通过 `-c` 显式使用 TOML。

## 镜像版本与权限

使用同时支持 PostgreSQL和内置本机的新 Hub 镜像；本地代码提交不代表远端镜像或服务器已更新。编排默认 Docker Hub 的 `ayflying/arkade-hub:latest` / `ayflying/arkade-agent:latest`；生产直接在 YAML 固定 `sha-<完整提交号>` 或摘要。也可换为 GHCR 同名镜像。PostgreSQL 使用 `postgres:17-alpine`。

新版 Hub 包含运行时执行代码、Docker CLI 与 Compose 插件，**镜像体积会比纯控制面版增加**；镜像只有 Hub 一个入口，不启动第二个 Agent 进程。构建上下文必须为仓库根目录，含 Hub、Agent、protocol 与 Web 源码：

```sh
docker build -f hub/Dockerfile -t arkade-hub:local .
```

Hub 默认 root，用于访问宿主通常为 `root:docker` 的 socket 并复用原 Agent 工作卷。**Docker socket 访问权等价于宿主高权限**；不是容器文件系统的普通只读权限，给 socket 挂载加 `:ro` 也不能把 Docker API 变成只读。只部署到可信主机，限制管理员与 Hub 入口访问。若自定义为非 root，需自行匹配 socket GID、工作卷权限及插件发现路径并验证，不建议通过 `chmod 666` 放开 socket。Podman/rootless socket 路径与权限需另行验证，不把改路径当作已兼容。

CI 发布仍由现有工作流管理；本文不证明某次镜像构建、发布或真实 Docker 操作已经通过。

## 1. 新建 Hub 与本机管理

已有 PostgreSQL 时，把 [仅 Hub 编排](<hub-compose.yml>) 与 [对应凭据样例](<hub.env.example>) 保存为部署目录内的 `compose.yml` 与 `.env`。需要同时部署 PostgreSQL 才选择 [combined 编排](<combined-compose.yml>) 和 [对应样例](<combined.env.example>)；不是通过两份文件叠加部署。

仅 Hub 必须修改 YAML 的 `ARKADE_DB_HOST: 请填写PostgreSQL地址`、端口、用户名、库名，并在 `.env` 填现有数据库密码。该编排不会创建数据库/角色或等待 PG 服务；提前准备专用 PostgreSQL 数据库和账号，Hub 启动执行受版本管理的业务表迁移。地址必须从 **Hub 容器内**可达，`127.0.0.1` 是 Hub 容器自身，不代表宿主；也不能默认用另一个 Compose 的 `postgres` 服务名。根据现有数据库部署安排受限网络、监听和授权，不要为方便无保护地暴露数据库。

`ARKADE_DB_SSLMODE` 沿用 `disable` 示例，仅适用于可信隔离网络；远程数据库应按证书/主机名配置 TLS（例如 `verify-full` 并提供可信 CA）。请根据实际连接和证书验证，不把改端口或网络当作 TLS 已启用。

```sh
chmod 600 .env
# 编辑 .env：填写 ARKADE_DB_PASSWORD、ARKADE_INITIAL_PASS。
# 编辑 compose.yml：仅 Hub 必填可达 PG 地址/账号/库名/TLS；核对 image、ports、BASE_URL 及 socket。
docker compose --env-file .env -f compose.yml config --quiet
docker compose --env-file .env -f compose.yml pull
docker compose --env-file .env -f compose.yml up -d
docker compose --env-file .env -f compose.yml ps
curl -fsS http://127.0.0.1:8080/healthz
```

浏览器访问 `http://服务器地址:8080`，生产设置稳定的 `ARKADE_BASE_URL` 并配 HTTPS。初始管理员密码用于新库创建账号，不会重置已有账号。本机无需先在网页添加服务器或复制接入令牌；启动后检查本机记录、运行时连接、原 Compose 文件和工作目录是否符合预期。健康检查验证 Hub/数据库，不等于本机 Docker/Compose 的端到端验收。

### 关闭本机管理（仅控制中心）

在自己的 YAML 中将 `ARKADE_LOCAL_ENABLED` 改为 `"false"`，同时**删除** Hub 的 `/var/run/docker.sock:/var/run/docker.sock` 挂载，然后重建 Hub 容器。可不挂本机工作卷，但必须保留已有卷，不删除数据。仅关闭开关、仍挂 socket 不构成撤销宿主权限；反过来仅删除 socket、仍启用本机会导致连接失败。远程 Agent 连接与 PostgreSQL 配置不受此开关替代。

## 2. 远程 Agent 独立部署

在 Hub 后台“添加服务器”获取该服务器的专属接入密钥，将 [Agent 编排](<agent-compose.yml>) 和 [Agent 凭据样例](<agent.env.example>) 放在远程服务器独立目录，保存为 `compose.yml`、`.env`，不要覆盖 Hub 的凭据。

```sh
chmod 600 .env
# .env 填 ARKADE_AGENT_TOKEN（后台生成的该节点专属密钥）。
# compose.yml 修改 ARKADE_HUB_URL、名称和 image。
docker compose --env-file .env -f compose.yml config --quiet
docker compose --env-file .env -f compose.yml pull
docker compose --env-file .env -f compose.yml up -d
```

跨机器使用 `wss://域名/agent/connect`，可信内网可用 `ws://Hub地址:8080/agent/connect`。远程 Agent 容器的 `127.0.0.1` 是它自己；不要照搬旧 combined 的 `ws://hub:8080/agent/connect` 到另一台机器。Agent 只获得自己的密钥，不获得数据库或管理员密码。远程编排的 socket 和数据路径保持不变。

## 3. 从旧 Hub + PG 编排改为仅 Hub（数据库保留）

**这不是删除数据库的迁移。** 先备份数据库、设置主密钥及本机工作卷，保留旧编排和凭据。记录旧 PostgreSQL 容器、项目、网络和真实卷名，保持该数据库运行；仅 Hub 编排没有管理它的权限或生命周期声明。

旧 Hub 编排曾使用显式 `arkade-hub-net`，新仅 Hub 使用项目默认网络。切换前先安排 Hub 到原数据库的可达连接（受控外部地址或明确配置的已有网络），核对 PG 用户、库名、密码和 TLS，不能简单把服务删掉后仍填 `postgres` 并期待自动连通。不要在此步骤拆除旧 PG 所依赖的网络。

使用原 project name、原 Hub/Agent 工作卷，仅更新 Hub：

```sh
# 备份并保留旧编排；新版仅 Hub 已设置好原数据库的可达地址与凭据。
docker compose --env-file .env -f compose.yml config --quiet
docker compose --env-file .env -f compose.yml pull hub
docker compose --env-file .env -f compose.yml up -d --no-deps hub
```

**禁止在此切换中使用 `--remove-orphans`、`down` / `down -v`、卷清理或自动删除旧 PostgreSQL 容器**。旧 PG 可能仍属于同项目，会被误当 orphan 移除；即使不删卷，停掉 PG 也会中断服务。仅 Hub `up` 不声明旧数据库、不会更新或删除其容器/卷；应另外妥善保管旧数据库的运维配置。后续数据库生命周期仍由原部署或专门的数据库管理方案负责。不要同时启动两套 `arkade-hub` 容器来“切换”。

## 4. 旧 combined 同机 Agent 迁移到内置本机（保留 combined 的 PG）

本节仅适用于 **旧 combined → 新 combined**，不能用于上一节仅 Hub 替换场景。先核对正在运行的旧 project name、Hub/数据库版本、工作卷名和身份。默认旧 combined 项目名为 `arkade`，卷为上文三个显式名称；新版 combined 保留这些名称。若原部署改过卷名或使用 bind mount，必须在新版指向**原目录/原卷**，不要创建空工作卷后声称已迁移身份。保留原身份与归属文件，不手工改写 ID。

1. 备份数据库、设置主密钥和本机 Agent 工作卷（含身份与 Compose 数据）；保留旧镜像、旧编排及旧凭据用于受控回退。
2. **先用旧编排停止旧 Agent**，防止新 Hub 与它同时操作宿主或写工作卷。不要先换掉编排后再执行 `stop agent`，因为新版已没有该服务。
3. 下载新版 combined，保留原 project name、端口、域名和卷映射。新版 `.env` 只需数据库与初始管理员密码；移除不再使用的本机 token 映射，不删除其他业务凭据。
4. 拉取并用 `--remove-orphans` 更新，清理已停止的旧 Agent 容器，**不删除旧卷**。

```sh
# 仍使用旧文件，且先核对项目归属；只停止这套部署的旧 Agent。
docker compose --env-file .env -f compose.yml stop agent
# 备份旧文件后，将新版保存为 compose.yml，核对原有卷名与项目名。
docker compose --env-file .env -f compose.yml config --quiet
docker compose --env-file .env -f compose.yml pull
docker compose --env-file .env -f compose.yml up -d --remove-orphans
```

`--remove-orphans` 会移除同一 Compose 项目中不再声明的服务容器，执行前确认没有其他刻意保留的 orphan。**不要执行 `down -v`、`docker volume prune` 或删除 `arkade-agent-data`**。如果旧 Agent 由另一项目或手动启动，需要在其所属部署中先停止并移除旧容器；此命令不能代替识别其他项目。检查“本机”记录与原容器归属、Compose 工作目录、运行时操作后再完成升级；失败保留现场，不能同时启动新 Hub 本机执行面与旧 Agent。

原本依赖全局 `ARKADE_AGENT_TOKEN` 的旧远程节点，需要先切换为后台专属密钥。新版默认不会把 `.env` 中旧全局令牌映射给 Hub；若过渡期仍需共享准入，管理员可在自己的 YAML 显式恢复该凭据映射并评估风险，但不要将本机旧 token 当全局令牌。

## 备份、日常更新与验证

- PG 用 `pg_dump` 备份，并在隔离新库演练恢复。数据库必须和 `/data/settings.key` 一起备份，否则 OIDC/通知密文不能解密。另备份本机工作卷，不把数据库快照当作包含全部容器数据。详见 [手动部署与备份](<../../docs/18-manual-goframe-deployment.md>)。
- combined 的数据库密码仅在空 PG 卷初始化时生效；已有 PG 改密先改数据库角色，再同步 Hub，不能只改 `.env`。仅 Hub 的密码始终是连接现有数据库的凭据。
- 日常更新保持原 project、卷和凭据，不随意重启其他业务容器。仅 Hub 停止使用 `stop hub`；从旧含 PG 编排切换后尤其不要 `down` 或清理 orphan。combined 可按其完整运维范围停止，但不得加 `-v`。回退需匹配旧镜像、库结构、密钥与原执行方式，先停新本机执行面再恢复旧 Agent。
- 仓库根运行 `python3 scripts/check-deploy-compose.py`：用临时虚构凭据检查三套编排独立解析、无重复本机 Agent、旧卷名、配置隔离与镜像静态约束，需要 Docker Compose v2；不读真实 `.env`，不启动容器。
- `config --quiet` 只能证明可解析，不能证明远端镜像更新、宿主 socket 可用、身份迁移或业务操作成功。上线需分别验 Hub、数据库、本机执行、远程 Agent 与原数据。
