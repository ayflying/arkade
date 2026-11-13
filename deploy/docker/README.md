# Arkade 容器部署

## 选择部署方式

| 编排 | 凭据样例 | 场景 |
| --- | --- | --- |
| [仅 Hub](<hub-compose.yml>) | [Hub 凭据](<hub.env.example>) | 一个 Hub 容器，连接已有 PostgreSQL |
| [combined](<combined-compose.yml>) | [combined 凭据](<combined.env.example>) | Hub + PostgreSQL 两个容器 |
| [远程 Agent](<agent-compose.yml>) | [Agent 凭据](<agent.env.example>) | 在其他受管服务器连接已有 Hub |

Hub 默认内置本机执行面，无需额外同机 Agent。两份 Hub 方案**二选一**，不叠加启动同名 Hub，也不要让多个执行端同时写同一工作目录。每种部署只需对应 YAML 和凭据样例，分别保存为服务器部署目录中的 `compose.yml`、`.env`；不需克隆源码、挂载 TOML 或引用其他编排。模板只使用 `image`，没有 `build`。

## 单卷布局与安全边界

两份 Hub 模板都只使用一个 Hub 命名数据卷 **`arkade-hub-data` → `/data`**：

- `/data/settings.key`：OIDC、通知等设置凭据的加密主密钥。
- `/data/local`：本机执行身份、归属、Compose 文件与备份。

Docker socket `/var/run/docker.sock` 是宿主路径 **bind mount，不是第二个数据卷**。combined 另有 PostgreSQL 数据卷 `arkade-postgres-data`；仅 Hub 不创建、管理或删除数据库服务和卷。远程 Agent 独立使用 `arkade-agent-data` → `/opt/arkade/agent`。

Hub 默认 root，安装 Docker CLI 和 Compose 插件，镜像体积包含本机执行依赖。**socket 权限等价于宿主高权限**；给挂载加 `:ro` 不会把 Docker API 变成只读。仅授予可信 Hub 和管理员；非 root/socket GID、rootless 或 Podman 需要部署者单独验证权限与兼容性，不建议 `chmod 666` 放开 socket。

仅控制中心部署：将 YAML 的 `ARKADE_LOCAL_ENABLED` 改为 `"false"`，**同时移除 socket bind mount**，保留 `/data` 以保存设置主密钥。只关闭开关而继续挂 socket 不能撤销宿主权限。

## 普通配置与凭据

- 镜像、端口、数据库地址/用户/库名、TLS、本机开关及日志直接修改 YAML。`.env` 仅填写对应密码/接入密钥，由 Compose 读取，程序不会自动加载它。
- 两份 Hub 的本机默认配置为 `ARKADE_LOCAL_ENABLED: "true"`、`ARKADE_LOCAL_DATA_DIR: /data/local`、`ARKADE_LOCAL_RUNTIME: auto`、`ARKADE_LOCAL_ENDPOINT: /var/run/docker.sock`、`ARKADE_LOCAL_COMPOSE_BIN: docker`。YAML 字面量不会被 `.env` 同名项自动覆盖。
- 本机不使用网络 token；远程节点在后台创建专属接入密钥。不要把管理员/数据库密码交给远程 Agent。
- Hub 内部监听 `8080`，前端 `/app/web/dist`。修改外部端口无需修改内部监听或健康检查。生产设置稳定的 `ARKADE_BASE_URL` 并使用 HTTPS。
- 会话期限、注册等系统策略由后台数据库设置管理。容器部署无需 TOML。

## 1. 仅 Hub：使用已有 PostgreSQL

保存 [Hub 编排](<hub-compose.yml>) 与 [凭据样例](<hub.env.example>)。提前准备专用 PostgreSQL 数据库与账号；Hub 启动执行受版本管理的业务表迁移，不替你创建外部数据库服务或角色。

必须将 YAML 的 `ARKADE_DB_HOST: 请填写PostgreSQL地址` 改为 **Hub 容器内可达**的地址，核对端口、用户名、库名，在 `.env` 填现有密码。容器内 `127.0.0.1` 是自身；另一 Compose 的 `postgres` 名称不会自动可见。默认网络为项目隔离的 `arkade-hub_default`，不共享 combined 网络，需按数据库部署安排受限网络、监听与授权。

`ARKADE_DB_SSLMODE: disable` 只适用于可信隔离网络。远程 PG 按证书和主机名配置 TLS，例如 `verify-full` 并提供可信 CA；不要为连接方便无保护公开数据库。

```sh
chmod 600 .env
# 填密码，修改 YAML 数据库地址/账号/库名/TLS、image、BASE_URL。
docker compose --env-file .env -f compose.yml config --quiet
docker compose --env-file .env -f compose.yml pull hub
docker compose --env-file .env -f compose.yml up -d --no-deps hub
docker compose --env-file .env -f compose.yml ps
curl -fsS http://127.0.0.1:8080/healthz
```

## 2. combined：同时提供 PostgreSQL

保存 [combined 编排](<combined-compose.yml>) 和 [对应凭据](<combined.env.example>)，填写 PostgreSQL 密码及初始管理员密码，核对公开地址、镜像和 socket 后启动：

```sh
chmod 600 .env
docker compose --env-file .env -f compose.yml config --quiet
docker compose --env-file .env -f compose.yml pull
docker compose --env-file .env -f compose.yml up -d
docker compose --env-file .env -f compose.yml ps
```

PostgreSQL 仅在该项目内部网络提供服务，不映射宿主 5432。空数据库卷初始化数据库/账号，Hub 等数据库健康后执行建表迁移并初始化管理员。已有账号不会因初始密码变量变化被重置。数据库角色密码只在空卷初始化时设置；后续改密须先受控修改数据库角色，再同步 Hub，不能只改 `.env`。

## 3. 远程 Agent

在 Hub 后台添加服务器并保存其专属密钥。在远程服务器独立目录保存 [Agent 编排](<agent-compose.yml>) 与 [凭据样例](<agent.env.example>)，填写 token，修改 YAML 的 Hub 地址、节点名称和镜像：

```sh
chmod 600 .env
docker compose --env-file .env -f compose.yml config --quiet
docker compose --env-file .env -f compose.yml pull
docker compose --env-file .env -f compose.yml up -d
```

跨机器使用 `wss://域名/agent/connect`，可信内网可用 `ws://Hub地址:8080/agent/connect`。Agent 容器的 `127.0.0.1` 是自己，不可代替 Hub 地址。只部署到可信宿主，socket 访问同样是高权限。

## 镜像、备份与验证

默认使用 Docker Hub 的 `ayflying/arkade-hub:latest`、`ayflying/arkade-agent:latest`，也可直接改 YAML 为 GHCR 镜像。生产固定 `sha-<完整提交号>` 或摘要；本地代码与文档不代表远端镜像已发布。开发者构建上下文为仓库根目录：

```sh
docker build -f hub/Dockerfile -t arkade-hub:local .
```

数据库使用 `pg_dump` 并在隔离库演练恢复；同时备份整个 `arkade-hub-data` 卷。数据库与设置主密钥必须配套恢复，否则 OIDC/通知密文不能解密。卷中本机工作文件不等于业务容器的数据，业务卷也需独立备份。详见 [PostgreSQL 部署与备份](<../../docs/18-manual-goframe-deployment.md>)。

日常停止仅 Hub 可用 `stop hub`；不要用 `down -v`、卷清理或删除需要保留的数据。不要公开完整 `docker compose config` 输出（可能含密码），检查结构用 `config --quiet`。

仓库根运行 `python3 scripts/check-deploy-compose.py`，以虚构凭据检查三份模板和数据路径，需要 Docker Compose v2，不读真实 `.env`、不启动容器。配置可解析不等于数据库可达、socket 可用或本机操作成功；上线分别验证 Hub/PG 健康、本机执行与远程节点接入。
