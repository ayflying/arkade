# GoFrame + PostgreSQL 新部署与备份

## 当前数据库边界

本版本仅支持 PostgreSQL，采用 GoFrame 官方 `pgsql/v2` 驱动。按用户决定使用全新数据库，不导入旧 SQLite，不提供 SQLite 兼容模式，原 SQLite 专用 `arkade-dbtool` 已移除。旧数据文件和旧运行实例不会被新版自动删除或接管。

本地代码/测试通过不等于 GHCR 或远端已经更新。部署必须确认 Hub 镜像包含 PostgreSQL 改造，生产建议直接在编排中固定镜像标签或摘要，不跟随未知 `latest`。

## 服务器独立部署

服务器不需要源码仓库：从 [部署目录](../deploy/docker/README.md) 选择一份完整编排和对应凭据样例，分别保存为 `compose.yml`、`.env`。

- Hub 独立编排：Hub + PostgreSQL 两个容器。
- Agent 独立编排：一个 Agent，连接已有 Hub。
- 合并编排：Hub + Agent + PostgreSQL 三个容器。

普通参数直接写编排，仅密码和令牌用变量；不包含 build、extends 或外部 TOML 挂载。PostgreSQL 不暴露宿主 5432 端口。Hub 文件卷保存 `/data/settings.key`，PG 独立卷保存业务数据。

```sh
chmod 600 .env
# 填数据库密码、管理员初始密码、Hub准入令牌。
docker compose --env-file .env -f compose.yml config --quiet
docker compose --env-file .env -f compose.yml pull
docker compose --env-file .env -f compose.yml up -d hub
curl -fsS http://127.0.0.1:8080/healthz
```

合并编排在 `.env` 预设独立随机的 `ARKADE_LOCAL_AGENT_TOKEN`（至少16位），直接 `up -d` 启动全部服务。Hub 自动创建“本机”并绑定该专属密钥，Agent 等 Hub 健康后自动连接，无须网页添加或复制密钥。Agent 专属密钥与 Hub 准入令牌不可混用；重启保留密钥，已有身份或密钥冲突不自动覆盖。

新库启动自动执行版本化 PostgreSQL 建表迁移。dirty、脚本校验和不符、结构漂移或未知更高版本应拒绝启动，禁止自动 force 跳过。已经有未受版本管理业务表的数据库不可冒充新库使用。

## PostgreSQL 备份和独立恢复演练

在安全目录执行，备份文件含账号密码哈希和业务凭据密文，限制文件权限。以下命令通过容器内部本地连接备份，不把密码放命令行，不公开数据库端口。

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

不能只有 PG 备份而没有主密钥，否则第三方登录的 Client Secret 无法解密。备份后应在隔离环境/新数据库进行恢复演练，不覆盖活动业务库：

```sh
docker compose --env-file .env -f compose.yml exec -T postgres \
  createdb -U arkade arkade_restore_check
docker compose --env-file .env -f compose.yml exec -T postgres \
  pg_restore -U arkade -d arkade_restore_check --exit-on-error < arkade-backup.dump
```

恢复演练还需用相同版本 Hub 和备份密钥指向恢复库验证登录、权限和编排读取；`pg_restore` 成功不等于业务验收完成。不要让演练 Hub 连接生产 Agent。

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
