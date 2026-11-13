# 自动构建与发布镜像（GitHub Packages）

工作流：`.github/workflows/images.yml`。使用 GitHub Actions 构建，镜像发布到 GitHub Container Registry（GHCR），可以在 GitHub 的 Packages 页面查看，不把镜像二进制提交进 Git 仓库。

**部署链路就是这个闭环**：本地改代码 → 推送 `master` → CI 跑测试并构建发布镜像 → 部署机 `docker compose pull && up -d`。
部署机不编译、不需要源码，镜像来源统一，生产固定提交标签或摘要。

## 镜像地址

本仓库 `ayflying/arkade` 会发布：

- Hub（包含 Web 前端）：`ghcr.io/ayflying/arkade-hub`
- Agent：`ghcr.io/ayflying/arkade-agent`

镜像名根据当前仓库名生成，统一转换为小写；fork 仓库不会误推到原仓库。两个镜像均构建 `linux/amd64`、`linux/arm64` 多架构清单。

## 本地版本号

根目录 [VERSION](<../VERSION>) 是 Hub、Agent 的统一发布版本，首次为 `0.0.1`。每轮发布改动提交前执行一次 `python scripts/bump-version.py`，仅补丁号加一；同轮修正或 CI 重试不再次加号。使用 `python scripts/bump-version.py --check` 只检查版本，不修改文件。规则记录在 [AGENTS.md](<../AGENTS.md>)，CI 不回写版本。

CI 将该版本注入两个二进制；从仓库根目录执行 Docker 构建时，未传 `VERSION` 构建参数也会读取版本文件。默认分支还会发布同名版本镜像标签。直接 `go run` 的开发版本不代表发布镜像版本。

## 自动触发与标签

| 事件 | 行为 |
| --- | --- |
| 推送到 `master` | 测试、构建，发布 `latest`、`VERSION` 中的版本标签和 `sha-<完整提交号>` |
| 推送 `v*` 标签 | 测试、构建，发布原标签和提交号；符合语义版本的标签额外发布版本号与主次版本号 |
| PR 到 `master` | 测试与镜像构建，不登录 GHCR、不推送镜像 |
| Actions → Run workflow | 对选择的分支/标签执行构建发布；只有默认分支更新 `latest` |

例如 `v1.2.3` 会生成 `v1.2.3`、`1.2.3`、`1.2` 和 `sha-...`，不会用版本标签覆盖开发分支的 `latest`。发布 Git 标签必须为 `v` 加上版本文件的完整值（例如文件为 `1.2.3` 时标签为 `v1.2.3`），不一致时拒绝发布。不建议使用浮动标签部署生产。

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

### 部署默认就用 CI 镜像

`deploy/docker/hub-compose.yml` 与 `deploy/docker/agent-compose.yml` 的 `image:` 直接指向 GHCR：

```yaml
  hub:                                   # hub-compose.yml
    image: ghcr.io/ayflying/arkade-hub:latest
  agent:                                 # agent-compose.yml（部署到被管服务器）
    image: ghcr.io/ayflying/arkade-agent:latest
```

部署流程就是「拉取 + 起容器」，不需要在服务器上装 Go/Node，也不需要源码：

```sh
docker compose -f deploy/docker/hub-compose.yml pull
docker compose -f deploy/docker/hub-compose.yml up -d
# 被管节点在 Agent 部署目录：
docker compose -f agent-compose.yml pull && docker compose -f agent-compose.yml up -d
```

固定发布版本（生产推荐）可修改该镜像字符串：

```sh
# 先取得摘要（推荐固定摘要，而不是浮动标签）
docker pull ghcr.io/ayflying/arkade-hub:latest
docker image inspect ghcr.io/ayflying/arkade-hub:latest --format '{{index .RepoDigests 0}}'
# ghcr.io/ayflying/arkade-hub@sha256:...
```

```yaml
    image: ghcr.io/ayflying/arkade-hub@sha256:<digest>
```

然后 `docker compose -f deploy/docker/hub-compose.yml up -d`（**不要加 `--build`**）。

部署模板仅包含 `image`，不含 `build`，不需要源码或 TOML。两种 Hub 模板使用 `arkade-hub-data` → `/data`（含 `settings.key` 与 `local`）；combined 另使用 `arkade-postgres-data`。远程 Agent 的 `arkade-agent-data` 与 Hub 单卷布局相互独立。更新镜像不应删除数据卷；数据库结构与镜像版本仍需分别验证。

## 查看产物与排障

工作流 Summary 会展示两个镜像的标签和 digest。构建未通过或权限失败时不会把失败说成发布成功，具体错误在对应 job 日志中。Hub 与 Agent 是两个独立发布 job，一个失败时另一个可能已成功；部署时请确认目标提交的两张镜像都存在。

## 读取外部编排（宝塔等）的原始配置

Agent 是容器，看不到宿主机路径；而外部编排的配置路径来自容器标签
`com.docker.compose.project.config_files`，是**宿主机绝对路径**。宝塔面板创建的编排
就在 `/www/server/panel/data/compose/<项目>/` 下，目录权限是 `0700`。

这种情况下 Arkade 能看到容器、能看日志，但读不到原始 compose 文件，Agent 日志里反复出现
`读取编排配置失败`。**这不是权限问题**：Agent 的 helper 已经带 `DAC_READ_SEARCH`，
容器内实测能读 0700 目录和 0600 文件；缺的是"宿主路径没有被挂进 Agent 容器"。

配置方式（在被管服务器的 Agent 部署目录，`agent-compose.yml` 里给 agent 服务加一段只读挂载）：

```yaml
    volumes:
      - type: bind
        source: /www/server/panel/data/compose
        target: /arkade/external
        read_only: true
```

然后重建 Agent：

```sh
cd <Agent 部署目录>
docker compose -f agent-compose.yml up -d --force-recreate
```

机制与边界：

- 目录以**只读**方式挂进容器，Arkade 不会修改这里的内容，宝塔可以继续照常管理；
- 只挂一个目录而不是 `/`：挂根目录等于把整台宿主机文件系统交给 Agent；
- Agent 收到容器标签里的宿主路径后，先按原有规则做安全校验（单配置文件、规范路径、
  非系统顶层目录、配置在工程目录内），校验通过才换算成容器内路径去读；
- 路径不在挂载目录之下时给出明确原因（"该目录没有挂载进 Agent 容器"），
  不会去猜一个相近目录，避免把别的项目当成这个项目；
- 不挂载 = 保持旧行为（原生部署的 Agent 本来就能直接读宿主路径）。
- **主节点不需要这项**：Hub 不读宿主机路径，只接收 Agent 上报的内容。

只读挂载的一个注意点：如果一个编排文件同时被 Arkade 和宝塔改，后保存的会覆盖先保存的。
之所以只读挂载，是为了让 Arkade 不会在你不知情的时候改动宝塔的配置；编辑请以"谁的事实为准"决定在哪边改。
