# Arkade Hub

Arkade 控制端，提供 Web、权限、远程 Agent 连接与编排管理。新版 Hub **内置本机执行面**，默认部署挂载宿主 Docker socket 管理本机，不再需要同机 Agent 容器；其他服务器仍部署独立 Agent。

- **GitHub 源码**：https://github.com/ayflying/arkade
- **部署与旧版本迁移**：https://github.com/ayflying/arkade/blob/master/deploy/docker/README.md
- **问题反馈**：https://github.com/ayflying/arkade/issues
- **远程 Agent**：https://hub.docker.com/r/ayflying/arkade-agent

## 快速部署（Hub + PostgreSQL）

下载两个文件到同一部署目录，保存为 `compose.yml`、`.env`：

- [完整编排](https://raw.githubusercontent.com/ayflying/arkade/master/deploy/docker/hub-compose.yml)
- [凭据样例](https://raw.githubusercontent.com/ayflying/arkade/master/deploy/docker/hub.env.example)

在 `.env` 填 PostgreSQL 密码和初始管理员密码；普通参数直接改 YAML，不需要克隆源码或挂载 TOML。本机无需令牌，远程服务器在后台创建专属密钥；全局共享准入默认关闭。

```sh
chmod 600 .env
docker compose --env-file .env config --quiet
docker compose --env-file .env pull
docker compose --env-file .env up -d
docker compose --env-file .env ps
```

访问 `http://服务器地址:8080`，对外部署前修改 `ARKADE_BASE_URL` 并配 HTTPS。新版镜像包含 Docker CLI、Compose 插件与本机执行代码，体积比旧纯控制面镜像增加。

## 本机权限与旧部署升级

- 默认 Hub 以 **root** 运行，挂载 `/var/run/docker.sock` 和 `/opt/arkade/agent` 工作卷。socket 访问等价于宿主高权限，只交给可信 Hub；`:ro` 不能把 Docker API 变为只读。
- 仅控制中心部署：把 YAML 中 `ARKADE_LOCAL_ENABLED` 改为 `"false"`，**同时移除 socket 挂载**。不是只在 `.env` 添加变量；普通配置是 YAML 字面量。
- 旧同机部署先用旧编排 `stop agent`，再换新版 combined 并 `up -d --remove-orphans`。新版保留原 `arkade-agent-data` 工作卷、`arkade-hub-files-pg` 密钥卷及 `arkade-postgres-data` 数据库卷；保留身份和原自定义卷映射。
- 新旧执行面不得同时运行并写同一工作卷。不要执行 `down -v` 或删除旧卷；`--remove-orphans` 前核对同项目中是否有其他需要保留的服务。完整迁移步骤见部署文档。

## 数据与标签

仅支持 PostgreSQL，不导入或兼容旧 SQLite。空 PG 卷创建库/账号，Hub 执行版本化迁移并初始化管理员。PG 不映射宿主 5432。数据库与设置加密主密钥必须配套备份，另备份本机工作卷。

支持 Linux `amd64` / `arm64`。`latest` 随默认分支 `master` 更新；生产固定 `sha-<完整提交号>` 或摘要。镜像由 GitHub Actions 构建并发布 Docker Hub 与 GHCR，不是 Docker Hub Automated Builds；构建发布记录见 https://github.com/ayflying/arkade/actions 。文档或本地配置检查不代表镜像已经发布或服务器完成升级。
