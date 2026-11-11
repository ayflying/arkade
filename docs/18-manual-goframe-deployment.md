# GoFrame 完整迁移后的手动部署

本文仅供操作员手动执行。源码迁移测试已通过，不等于对应镜像已发布或远端已升级。

## 1. 确认版本

先将完成迁移的本地 master 推送到自己的远端，服务器更新后运行 `git log -1 --oneline`。不要把 GHCR 的 latest 当成本阶段版本；此前曾因 GitHub 凭据失效推送失败。

以下命令均在 Linux 服务器的仓库根目录执行。仅更新 Hub；Agent 协议兼容，本次不要求重建 Agent，更不应重启业务容器。

## 2. 新装：构建自己的固定标签

```sh
VERSION="gf-$(git rev-parse --short HEAD)"
docker build -f hub/Dockerfile --build-arg VERSION="$VERSION" -t "arkade-hub:$VERSION" .
```

构建包含前端类型检查、前端产物、Linux 静态 Hub 和数据库工具。

```sh
# 仅首次创建；已有凭据文件不要覆盖。
cp deploy/docker/.env.example .env
chmod 600 .env
```

编辑根目录 `.env`：管理员初始密码至少 8 位；准入令牌至少 16 位，可用 `openssl rand -hex 24` 生成。已有数据库不会因初始密码配置而重置账号。

写一个只覆盖镜像的本地文件，保持原有配置结构：

```sh
cat > deploy/docker/hub-local.yml <<EOF
services:
  hub:
    image: arkade-hub:$VERSION
EOF

docker compose --project-directory "$PWD" --env-file .env \
  -f deploy/docker/hub-compose.yml -f deploy/docker/hub-local.yml \
  up -d --no-build --pull never hub
```

显式指定项目目录与凭据文件，避免 Compose 不同调用目录影响 `${...}` 插值。

```sh
docker logs --tail 100 arkade-hub
curl -fsS http://127.0.0.1:8080/healthz
```

健康响应应含预期版本、数据库健康、在线数。首次 Agent 数为 0 正常。登录后通过页面新增服务器并取得各机器专属接入密钥；不要把共享令牌当成专属令牌。

## 3. 升级：禁止直接换数据卷

先检查原容器镜像、卷、配置和 Compose 归属：

```sh
docker inspect 原Hub容器 --format '{{.Config.Image}}'
docker inspect 原Hub容器 --format '{{json .Mounts}}'
docker inspect 原Hub容器 --format '{{json .Config.Labels}}'
```

保留原端口、卷、配置、网络别名和凭据。旧测试服是 `arkade-hub-demo`、18080 端口及 `arkade-demo-hub-data` 卷；它与标准编排的 `arkade-hub-data` 不是同一份数据。不能直接套新装步骤而误用空卷。

### 先备份

以下数据库路径仅适用于确认原库确为 `/data/hub.db` 的环境：

```sh
OLD=原Hub容器
BACKUP="hub-before-gf-$(date +%Y%m%d-%H%M%S).db"

docker run --rm --user 0 --volumes-from "$OLD" \
  --entrypoint /app/arkade-dbtool "arkade-hub:$VERSION" \
  backup -source /data/hub.db -destination "/data/$BACKUP"

docker run --rm --user 0 --volumes-from "$OLD" \
  --entrypoint /app/arkade-dbtool "arkade-hub:$VERSION" \
  check -source "/data/$BACKUP"

mkdir -p backups
chmod 700 backups
docker cp "$OLD:/data/$BACKUP" "backups/$BACKUP"
chmod 600 "backups/$BACKUP"
```

两步工具必须返回 `ok`。不得直接复制运行中的 SQLite 主文件而忽略 WAL；备份不启动第二个 Hub，也不迁移源数据库。

### 更新原有编排

在原有编排中仅将 Hub 镜像改为刚构建的固定标签，然后使用原来的 project name、Compose 文件和服务名更新：

```sh
docker compose -p 原项目名 -f 原编排文件.yml \
  up -d --no-deps --no-build --pull never 原Hub服务名
```

原测试服上曾手动保留旧容器并替换 Hub，不一定能由原 Compose 直接接管；应按 inspect 得到的真实归属处理，先不要盲目执行 `up`。

不要执行 `down -v`，不要删除源卷，不要重建 Agent/业务容器。

## 4. 验收和回退

- 健康版本正确，无 dirty、checksum、结构不匹配或迁移失败。
- 原账号、用户组和授权仍在，Agent 重新在线。
- 服务器、容器、资源、镜像任务和编排页面能读取；审计有新操作记录。
- 原业务容器 ID、启动时间、重启次数不变。
- 日志无 `panic serving`、`superfluous response.WriteHeader`。

Hub 启动自动运行数据库迁移，不需手动执行 SQL，禁止为绕过失败而 force 版本。

失败时停止新 Hub，保留新库和日志。数据库 restore 只能输出到**新文件**，校验后调整配置指向恢复库；不要覆盖活动主文件或带旧 WAL 的同一路径。旧镜像需要与升级前备份配套恢复，不默认保证可直接读取已升级库。

## 5. 能力边界

全部 HTTP 和数据访问架构已迁移到 GoFrame；这不代表跨服务器 Compose 迁移业务的生产 backend 和真实双引擎验收也已完成。不要通过手动配置臆造启用项，未装配时的明确失败是安全边界。
