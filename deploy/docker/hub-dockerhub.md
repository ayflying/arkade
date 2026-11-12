# Arkade Hub

Arkade 的控制端：提供 Web 界面、用户与权限管理、Agent 连接和容器编排管理。Hub 不挂载宿主 Docker socket。

- **GitHub 源码**：https://github.com/ayflying/arkade
- **部署文档**：https://github.com/ayflying/arkade/blob/master/deploy/docker/README.md
- **问题反馈**：https://github.com/ayflying/arkade/issues
- **对应 Agent**：https://hub.docker.com/r/ayflying/arkade-agent

## 快速部署（自带 PostgreSQL）

下载以下两个文件到服务器同一目录，分别保存为 `compose.yml` 和 `.env`：

- [Hub + PostgreSQL 完整编排](https://raw.githubusercontent.com/ayflying/arkade/master/deploy/docker/hub-compose.yml)
- [对应凭据样例](https://raw.githubusercontent.com/ayflying/arkade/master/deploy/docker/hub.env.example)

在 `.env` 填写 PostgreSQL 密码、管理员初始密码和 Hub 准入令牌。普通端口、域名等参数直接修改编排，不需要克隆源码或挂载 TOML。

```sh
chmod 600 .env
docker compose --env-file .env pull
docker compose --env-file .env up -d
docker compose --env-file .env ps
```

默认访问 `http://服务器地址:8080`。对外部署前修改 `ARKADE_BASE_URL` 并配置 HTTPS。

## 数据与安全

- 仅支持全新 PostgreSQL；不导入或兼容旧 SQLite 数据。
- PG 空卷首次启动自动创建库和账号；Hub 自动运行内置建表脚本并创建初始管理员，无须手动执行 SQL。
- PG 数据、Hub OIDC 加密主密钥分别持久化；备份时必须同时保存数据库和主密钥。
- PG 默认只供内部网络访问，不公开宿主 5432 端口。
- 日常停止和升级不要执行 `docker compose down -v`，否则会删除数据卷。

## 标签与架构

支持 Linux `amd64` 和 `arm64`。`latest` 随 GitHub 默认分支 `master` 更新；生产建议固定 `sha-<完整提交号>` 或镜像摘要。

镜像由 GitHub Actions 构建，同时推送 Docker Hub 和 GHCR；不是 Docker Hub Automated Builds。完整构建和发布记录见 https://github.com/ayflying/arkade/actions 。
