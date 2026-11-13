# Arkade Hub

Arkade 控制端，提供 Web、权限、远程 Agent 连接与编排管理。Hub **内置本机执行面**，默认挂载宿主 Docker socket 管理本机，不再需要同机 Agent 容器；其他服务器仍部署独立 Agent。

- **GitHub 源码**：https://github.com/ayflying/arkade
- **部署与迁移**：https://github.com/ayflying/arkade/blob/master/deploy/docker/README.md
- **问题反馈**：https://github.com/ayflying/arkade/issues
- **远程 Agent**：https://hub.docker.com/r/ayflying/arkade-agent

## 快速部署（仅 Hub，连接已有 PostgreSQL）

下载两个文件到同一目录，保存为 `compose.yml`、`.env`：

- [仅 Hub 编排](https://raw.githubusercontent.com/ayflying/arkade/master/deploy/docker/hub-compose.yml)
- [凭据样例](https://raw.githubusercontent.com/ayflying/arkade/master/deploy/docker/hub.env.example)

该编排**只创建 Hub，不创建、控制或删除 PostgreSQL 容器/数据卷**。先准备可达的 PostgreSQL 数据库/角色，在 YAML 把 `ARKADE_DB_HOST: 请填写PostgreSQL地址` 改成 Hub 容器可达地址，核对端口、用户名和库名；`.env` 填现有 PG 密码及初始管理员密码。容器内 `127.0.0.1` 是自身，另一个 Compose 的 `postgres` 名称不会自动可见。默认项目隔离网络不共享 combined 的显式 `arkade-hub-net`，网络接入需按实际数据库部署配置。

`ARKADE_DB_SSLMODE: disable` 仅适用于可信隔离网络；远程 PG 应配置匹配证书/主机名的 TLS 策略（例如 `verify-full` 并提供可信 CA），不要无保护开放数据库。普通参数改 YAML，不需克隆源码或 TOML。本机无需令牌，远程节点在后台创建专属密钥，全局共享准入默认关闭。

```sh
chmod 600 .env
docker compose --env-file .env config --quiet
docker compose --env-file .env pull hub
docker compose --env-file .env up -d --no-deps hub
docker compose --env-file .env ps
```

访问 `http://服务器地址:8080`，对外部署前修改 `ARKADE_BASE_URL` 并配 HTTPS。需要一并部署 PostgreSQL 时，改用 [combined 编排](https://raw.githubusercontent.com/ayflying/arkade/master/deploy/docker/combined-compose.yml) 与其对应凭据样例，不要同时运行两套同名 Hub。

## 单卷布局与权限

- 仅 Hub 为新安装提供 **一个命名数据卷** `arkade-hub-data`，挂载 `/data`；其中 `/data/settings.key` 保存设置主密钥，`/data/local` 保存本机身份、归属、Compose 文件和备份。`ARKADE_LOCAL_DATA_DIR` 为 `/data/local`，不再单独挂 Agent 数据卷。
- `/var/run/docker.sock` 是宿主路径 bind mount，**不是第二个数据卷**。默认 Hub 以 root 访问 socket，权限等价于宿主高权限；`:ro` 不能把 Docker API 变为只读。镜像包含 Docker CLI、Compose 插件与本机执行代码，体积比纯控制面版增加。
- 仅控制中心：把 YAML 的 `ARKADE_LOCAL_ENABLED` 改为 `"false"`，同时移除 socket 挂载；保留 `/data` 以保存设置主密钥。
- combined 同样使用单个 Hub 数据卷，另提供独立 PostgreSQL 数据卷；与仅 Hub 二选一，不叠加部署。

## 数据与标签

仅支持 PostgreSQL。Hub 对配置的业务库执行版本化迁移；已有账号不因初始密码变量改变而重置。备份现有 PostgreSQL 和整个 `arkade-hub-data` 卷，后者包含设置主密钥与本机工作文件；数据库与主密钥必须配套恢复，业务容器数据另行备份。

支持 Linux `amd64` / `arm64`。`latest` 随默认分支 `master` 更新，生产固定 `sha-<完整提交号>` 或摘要。镜像由 GitHub Actions 构建并发布 Docker Hub 与 GHCR；构建记录见 https://github.com/ayflying/arkade/actions 。配置检查不代表镜像已发布、数据库可达或服务器已升级。
