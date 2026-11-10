# 自动构建与发布镜像（GitHub Packages）

工作流：`.github/workflows/images.yml`。使用 GitHub Actions 构建，镜像发布到 GitHub Container Registry（GHCR），可以在 GitHub 的 Packages 页面查看，不把镜像二进制提交进 Git 仓库。

## 镜像地址

本仓库 `ayflying/arkade` 会发布：

- Hub（包含 Web 前端）：`ghcr.io/ayflying/arkade-hub`
- Agent：`ghcr.io/ayflying/arkade-agent`

镜像名根据当前仓库名生成，统一转换为小写；fork 仓库不会误推到原仓库。两个镜像均构建 `linux/amd64`、`linux/arm64` 多架构清单。

## 自动触发与标签

| 事件 | 行为 |
| --- | --- |
| 推送到 `master` | 测试、构建，发布 `latest` 和 `sha-<完整提交号>` |
| 推送 `v*` 标签 | 测试、构建，发布原标签和提交号；符合语义版本的标签额外发布版本号与主次版本号 |
| PR 到 `master` | 测试与镜像构建，不登录 GHCR、不推送镜像 |
| Actions → Run workflow | 对选择的分支/标签执行构建发布；只有默认分支更新 `latest` |

例如 `v1.2.3` 会生成 `v1.2.3`、`1.2.3`、`1.2` 和 `sha-...`，不会用版本标签覆盖开发分支的 `latest`。预发布标签遵循 docker/metadata-action 的 SemVer 规则，不建议使用浮动标签部署生产。

每次运行先执行三套 Go 模块测试与 vet、Web 构建及全部契约测试，全部通过后才并行构建 Hub、Agent。测试容器不提供 Docker CLI 和 socket，以免既有“Docker 不可用”的测试假设被 GitHub Runner 内置 Docker 干扰；实际编排生命周期由测试里的假 Docker 执行器验证。镜像构建使用 Buildx/QEMU 和 GitHub Actions 缓存。

## GitHub 权限（通常不需要额外 Secret）

发布使用本次运行自带的 `GITHUB_TOKEN`，发布 job 显式申请 `packages: write`，其他 job 只有 `contents: read`。没有 PAT、密码或服务器部署密钥写在仓库里。不使用 `pull_request_target` 执行 PR 内容，不会把发布权限交给外部 PR。

首次发布后检查：

1. 仓库 **Actions** 页面启用了工作流。组织策略如果禁用 GitHub Actions、第三方 Actions 或 Packages 写入，需要管理员放行。
2. 若已有同名 Package 并出现 `permission_denied: write_package`，到该 Package 的 **Package settings → Manage Actions access**，给此仓库写入权限。OCI `source` 标签会关联仓库，但不能绕过已有包的权限设置。
3. Package 可能默认是 private。若希望未登录也能拉取，需要在 Package 设置中明确改为 public；CI 不会擅自更改公开性。

## 使用镜像

公开包可以直接拉取：

```sh
docker pull ghcr.io/ayflying/arkade-hub:latest
docker pull ghcr.io/ayflying/arkade-agent:latest
```

生产建议固定 `sha-<完整提交号>` 或摘要，避免下一次推送 `master` 后浮动标签变化。私有包需要拥有 `read:packages` 的账号凭据，使用 `docker login ghcr.io --password-stdin`，不要把 Token 放在命令行、Compose 文件或 Git 中。

现有本地调试部署方式不变。本次 CI 只构建并发布镜像，不连接测试服、不重启任何业务容器，也不会自动升级现有运行实例。更换镜像前仍应备份配置和数据并确认发布版本。

## 查看产物与排障

工作流 Summary 会展示两个镜像的标签和 digest。构建未通过或权限失败时不会把失败说成发布成功，具体错误在对应 job 日志中。Hub 与 Agent 是两个独立发布 job，一个失败时另一个可能已成功；部署时请确认目标提交的两张镜像都存在。
