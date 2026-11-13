# GoFrame + PostgreSQL 部署与备份

## 数据库与安装

Hub 使用 GoFrame 官方 PostgreSQL 驱动。准备专用业务库和角色；启动时执行版本化建表迁移。dirty、脚本校验和不符、结构漂移或未知更高版本应拒绝启动，不自动 force 跳过。未受版本管理的现有业务表不能冒充空库。

从 [部署目录](<../deploy/docker/README.md>) 选择一份编排和凭据样例，保存为 `compose.yml`、`.env`：

- 仅 Hub：一个 Hub 容器，连接已有 PG；填写容器可达地址、账号、库名与 TLS 策略。
- combined：Hub 与 PostgreSQL 两个容器，数据库仅内部网络可见，不映射宿主 5432。
- 远程 Agent：部署在其他受管服务器，用后台生成的专属密钥连接 Hub。

两种 Hub 方案二选一，均使用一个命名卷 `arkade-hub-data` → `/data`，含 `/data/settings.key` 与 `/data/local` 本机工作目录。combined 另使用 `arkade-postgres-data`。Docker socket 是 bind mount，不是第二个 Hub 数据卷。

```sh
chmod 600 .env
# 填密码，核对 YAML 镜像、PG 地址/TLS、BASE_URL 与 socket。
docker compose --env-file .env -f compose.yml config --quiet
docker compose --env-file .env -f compose.yml pull
docker compose --env-file .env -f compose.yml up -d
curl -fsS http://127.0.0.1:8080/healthz
```

Hub 默认内置本机执行，镜像包含 Docker CLI/Compose，默认 root 并挂宿主 socket，等价于宿主高权限。仅控制中心设置 `ARKADE_LOCAL_ENABLED: "false"` 并移除 socket，保留 `/data`。远程节点只使用各自的接入密钥，不需要数据库或管理员密码。

仅 Hub 默认网络不共享其他 Compose 网络。容器内 `127.0.0.1` 不是宿主或另一个 PG 容器；应安排受控可达地址和授权。`sslmode=disable` 仅适用于可信隔离网络，远程连接配置证书与 TLS。镜像固定提交标签或摘要，源码/测试通过不代表镜像已发布。

## PostgreSQL 备份和独立恢复演练

备份含密码哈希和业务凭据密文，限制文件权限。下列命令仅适用于包含 PG 的 combined 编排；仅 Hub 需在已有 PG 自身的管理环境执行备份，不对 Hub 编排运行 `exec postgres`。

```sh
umask 077
docker compose --env-file .env -f compose.yml exec -T postgres \
  pg_dump -U arkade -d arkade -Fc > arkade-backup.dump
```

同时备份整个 `arkade-hub-data` 卷，包含设置主密钥与本机身份、归属、Compose 文件、备份目录。数据库与主密钥必须配套，只有 PG 备份无法解密 OIDC/通知凭据。业务容器数据卷独立备份；工作目录副本不能代替业务数据备份。可另外安全保存主密钥：

```sh
docker cp arkade-hub:/data/settings.key ./settings.key.backup
chmod 600 settings.key.backup
```

在隔离环境/新数据库演练恢复，不覆盖活动业务库：

```sh
docker compose --env-file .env -f compose.yml exec -T postgres \
  createdb -U arkade arkade_restore_check
docker compose --env-file .env -f compose.yml exec -T postgres \
  pg_restore -U arkade -d arkade_restore_check --exit-on-error < arkade-backup.dump
```

使用对应版本 Hub 和备份密钥连接恢复库，验证登录、权限和编排读取；`pg_restore` 成功不等于业务验收。演练 Hub 不应连接生产 Agent、宿主 socket 或工作目录。

## 日常维护

- 更新前备份 PG 与 `/data`，固定并确认目标镜像。数据库结构版本检查与镜像发布独立。
- 仅 Hub 不管理已有 PG 生命周期；停止 Hub 可用 `stop hub`。不要执行 `down -v` 或清理需要保留的数据卷。
- combined 数据库密码仅在空卷初始化角色时应用；改密须先数据库角色修改，再同步 Hub，不可只改 `.env`。
- 不同时运行多个 Hub 写相同业务库或工作目录；不把内置执行误当作多 Hub 高可用。

## 开发与 CI

真实数据库回归通过 `ARKADE_TEST_PG_DSN` 指定隔离 PostgreSQL。测试每例创建独立库并清理，缺环境变量或连接失败应明确失败，不跳过。

```sh
# 仅隔离测试环境，禁止指向业务数据库账号。
export ARKADE_TEST_PG_DSN='postgres://arkade_test:测试密码@127.0.0.1:15432/postgres?sslmode=disable'
cd hub
go test ./... -count=1
go vet ./...
```

CI 启动 PG 服务执行真实数据库测试。镜像为纯 Go 构建，不需要 CGO。本说明不证明任何服务器已完成部署。
