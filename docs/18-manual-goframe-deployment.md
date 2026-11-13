# GoFrame + PostgreSQL 新部署与备份

## 当前数据库边界

本版本仅支持 PostgreSQL，采用 GoFrame 官方 `pgsql/v2` 驱动。按用户决定使用全新数据库，不导入旧 SQLite，不提供 SQLite 兼容模式，原 SQLite 专用 `arkade-dbtool` 已移除。旧数据文件和旧运行实例不会被新版自动删除或接管。

本地代码/测试通过不等于 GHCR 或远端已经更新。部署必须确认 Hub 镜像包含 PostgreSQL 改造，生产建议直接在编排中固定镜像标签或摘要，不跟随未知 `latest`。

## 服务器独立部署

服务器不需要源码仓库：从 [部署目录](../deploy/docker/README.md) 选择一份完整编排和对应凭据样例，分别保存为 `compose.yml`、`.env`。

- Hub 独立编排：只有 Hub（内置本机执行面），连接已有 PostgreSQL；必须修改 PG 地址，并确保从 Hub 容器可达，不重复创建数据库。
- Agent 独立编排：远程服务器一个 Agent，连接已有 Hub。
- 历史合并编排：现在同样为 Hub + PostgreSQL 两个容器，保留旧项目名与卷名，不再启动同机 Agent。

普通参数直接写编排，仅密码和令牌用变量；不包含 build、extends 或外部 TOML 挂载。合并编排的 PostgreSQL 不暴露宿主 5432 端口；仅 Hub 模板须连接实际可达的已有数据库，容器内 `127.0.0.1` 不是宿主或其他项目的 PG。Hub 文件卷保存 `/data/settings.key`，数据库由自身部署持久化。

```sh
chmod 600 .env
# 填数据库密码、管理员初始密码；本机不再需要令牌。
docker compose --env-file .env -f compose.yml config --quiet
docker compose --env-file .env -f compose.yml pull
docker compose --env-file .env -f compose.yml up -d hub
curl -fsS http://127.0.0.1:8080/healthz
```

两份 Hub 编排默认 `ARKADE_LOCAL_ENABLED: "true"`，直接在 Hub 内执行本机操作，不再依赖本机 token 或额外 Agent 容器。Hub 镜像包含 Docker CLI/Compose，默认 root，挂载宿主 `/var/run/docker.sock`，这等价于宿主高权限；镜像体积比旧纯控制面版本增加。工作目录 `/opt/arkade/agent` 使用原 `arkade-agent-data` 卷，必须保留身份、Compose 文件与归属记录，不能和旧 Agent 并行写入。仅控制中心部署应把 YAML 开关改为 `"false"` 并删除 socket 挂载，而非只停用网页入口。

远程 Agent 独立编排不变，在后台生成各节点专属接入密钥；Hub 全局共享 `ARKADE_AGENT_TOKEN` 默认空。旧 `.env` 中存在令牌不会自动映射到新 Hub，如仍需过渡期共享准入须在自己的 YAML 显式恢复并评估风险。

新库启动自动执行版本化 PostgreSQL 建表迁移。dirty、脚本校验和不符、结构漂移或未知更高版本应拒绝启动，禁止自动 force 跳过。已经有未受版本管理业务表的数据库不可冒充新库使用。

## PostgreSQL 备份和独立恢复演练

在安全目录执行，备份文件含账号密码哈希和业务凭据密文，限制文件权限。以下 `exec postgres` 命令仅适用于包含 PG 的合并编排；仅 Hub 部署需在已有数据库自身的部署目录执行对应备份/恢复，不能对仅 Hub 的 compose 执行这些命令。以下命令通过容器内部本地连接备份，不把密码放命令行，不公开数据库端口。

```sh
umask 077
docker compose --env-file .env -f compose.yml exec -T postgres \
  pg_dump -U arkade -d arkade -Fc > arkade-backup.dump
```

OIDC 主密钥必须和数据库一起备份：

```sh
docker cp arkade-hub:/data/settings.key ./settings.key.backup
chmod 600 settings.key.backup
```

还应备份本机 `arkade-agent-data` 工作卷；数据库快照不包含该卷内身份、Compose 文件、归属与备份。不能只有 PG 备份而没有主密钥，否则第三方登录的 Client Secret 与监控通知凭据无法解密。备份后应在隔离环境/新数据库进行恢复演练，不覆盖活动业务库：

```sh
docker compose --env-file .env -f compose.yml exec -T postgres \
  createdb -U arkade arkade_restore_check
docker compose --env-file .env -f compose.yml exec -T postgres \
  pg_restore -U arkade -d arkade_restore_check --exit-on-error < arkade-backup.dump
```

恢复演练还需用相同版本 Hub 和备份密钥指向恢复库验证登录、权限和编排读取；`pg_restore` 成功不等于业务验收完成。不要让演练 Hub 连接生产 Agent。

## 旧同机 Agent 切换到内置本机

以下命令仅用于继续采用 combined（Hub + PG）的同机升级。改用仅 Hub 模板时必须保留已有 PG 的运行、网络和卷，不能使用 `--remove-orphans` 或 `down`；须先配置 Hub 可达的已有 PG 地址，再只更新 Hub，见部署说明中的“旧 Hub + PG 改为仅 Hub”章节。

升级前备份数据库、设置主密钥和原 Agent 工作卷，保留旧编排、镜像及凭据。先用旧文件停止 Agent，再替换编排；新版已无 `agent` 服务，不能在换文件后才运行 `stop agent`。

```sh
# 原部署目录与旧文件；先确认 project 与服务归属。
docker compose --env-file .env -f compose.yml stop agent
# 备份旧文件后替换为新版 combined，保留原项目名与显式卷名。
docker compose --env-file .env -f compose.yml config --quiet
docker compose --env-file .env -f compose.yml pull
docker compose --env-file .env -f compose.yml up -d --remove-orphans
```

新版 combined 保留项目名 `arkade`，复用 `arkade-agent-data`、`arkade-hub-files-pg` 和 `arkade-postgres-data`。原自定义卷名或 bind mount 必须对应保留，不能用空卷替代后声称保留身份。`--remove-orphans` 会移除同项目未声明的旧服务容器，执行前核对是否存在其他应保留 orphan；不会替代停止另一 Compose 项目的 Agent。**不执行 `down -v`、卷清理或删除旧卷**。检查本机记录、原容器归属与 Compose 工作目录，再验证操作；不能同时运行新 Hub 本机执行面和旧 Agent。

详见 [部署迁移步骤](<../deploy/docker/README.md>)。如需回退，先关闭/停止新本机执行面，再用配套旧镜像、库备份及工作卷恢复旧部署，避免并行执行。

## 更新与回退

- 新库不含旧账号、旧服务器或旧编排，重新初始化管理员并登记 Agent；不宣称保留 SQLite 数据。
- PG 数据卷 `arkade-postgres-data` 与 Hub 文件卷 `arkade-hub-files-pg` 必须保留，升级前备份两者。
- `.env` 中数据库密码在空卷初始化时设置角色密码，修改已有库密码须先数据库改密并同步 Hub；仅改 `.env` 不会自动修改既有角色。
- 更新时直接修改编排的 image，用原 project name、卷和凭据执行目标服务更新；不使用 `down -v`，不重启无关业务容器。
- 回退需要旧镜像及对应版本 PG 备份和密钥配套，不能假定旧版本能读取新结构；在独立库恢复，保留失败现场。
- 原 SQLite 版本只能独立保管和运行；不能将其数据库文件挂入新 PG 版 Hub。

## 开发与 CI

真实数据库回归需要管理测试库的独立 PostgreSQL 实例，通过 `ARKADE_TEST_PG_DSN` 指定。测试每例创建隔离库，结束删除；缺环境变量会明确失败，不使用 SQLite 或静默跳过。

```sh
# 仅隔离测试环境，禁止指向业务数据库账号。
export ARKADE_TEST_PG_DSN='postgres://arkade_test:测试密码@127.0.0.1:15432/postgres?sslmode=disable'
cd hub
go test ./... -count=1
go vet ./...
```

CI 启动 PostgreSQL 服务运行真实数据库测试。镜像仍为纯 Go 构建，不需要 CGO。此文档不证明某台服务器已经部署。
