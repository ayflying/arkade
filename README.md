# Arkade（容器方舟）

> 一个不受 Portainer 局限的容器管理平台：主从架构、多引擎兼容、离线自治、迁移与镜像同步。

## 这是什么

Arkade（中文名：容器方舟）是一个用于管理多台服务器上容器的平台。它参考 Portainer 的使用方式，但不绑定 Docker，也不要求每台机器都能连外网。

它采用「Hub 默认内置本机执行，远程服务器部署 Agent」的架构：

- **主程序（Hub）**：提供 Web 界面、用户鉴权、服务器管理、编排下发、镜像仓库凭据集中配置与迁移任务编排；默认还在进程内管理本机容器。内置执行复用 Agent 的公开 embedded 包，经内部 Registry 调用，不另起本地 WebSocket 连接、不创建本机接入 Token。
- **代理程序（Agent）**：只需部署在远程受管服务器上，主动反向连接 Hub，执行该服务器的容器操作、编排文件落盘及镜像与数据卷搬运。同机不必再部署一个独立 Agent。

本机固定标识为 `agent-local`，保留已有本机 identity 与 workspace；默认工作目录为 `/opt/arkade/agent`。只想运行控制面时，可设置 `ARKADE_LOCAL_ENABLED=false` 或配置 `local.enabled=false` 后重启 Hub。Hub 仍必须连接 PostgreSQL。详见 [内置本机执行与升级](<docs/21-embedded-local.md>)。

## 核心特性

| 特性 | 说明 |
| --- | --- |
| 本机内置、远程代理 | Hub 默认复用 Agent 执行能力管理本机；远程服务器由独立 Agent 执行 |
| 远程反向连接 | 远程 Agent 主动连接 Hub，服务器不需要暴露入站端口，内网/NAT 后可管 |
| 远程离线自治 | Hub 失联时，远程 Agent 仍可通过本地 CLI 操作所在服务器的容器与编排；内置执行随 Hub 停止 |
| 编排双份 | 编排文件在主程序与 Agent 本地各存一份，主程序挂了服务器仍能自跑 |
| 接管既有容器 | 支持接管宝塔等外部工具部署的容器与编排，并保留来源标记 |
| 容器迁移 | 配置 + 镜像 + 数据卷一起迁移，支持预检、断点续传、校验与回滚 |
| 本地镜像同步 | 无外网仓库的服务器之间，可手动点对点同步本地镜像 |
| 仓库统一授权 | 主程序集中配置镜像仓库凭据，各 Agent 无需逐台登录 |
| 多引擎兼容 | 设计上兼容 Docker / Podman / containerd 等运行时（分期接入） |

## 技术栈

| 层 | 选型 | 理由 |
| --- | --- | --- |
| 主程序 Hub | Go | 容器生态 SDK 成熟，单二进制部署，流式传输顺手 |
| 代理程序 Agent | Go | 静态交叉编译，丢到任意服务器即可运行，无 glibc 依赖 |
| 前端 | Vue 3 + Vite + TypeScript + Pinia | 与既有项目栈一致，开发效率高 |
| 通信 | 本机进程内 Registry；远程 WebSocket + 消息信封 | 本机不绕网络；远程反向连接穿 NAT/反代友好 |

### 为什么不是 Rust

这个工程的核心负载是 I/O（转发 Docker API、搬运镜像层、读写数据卷），语言层的性能不构成瓶颈；而容器生态的现成 SDK、静态单二进制分发、流式管道写法都是 Go 的强项。Rust 更适合局部替换（高频采集、加密校验类 sidecar），不适合作主载体。详见 [docs/01-architecture.md](docs/01-architecture.md)。

## 文档索引

- [01 架构设计](docs/01-architecture.md) — 主从拓扑、连接协议、状态模型、安全边界
- [02 功能规格](docs/02-features.md) — 编排双份、接管、迁移、镜像同步、仓库授权
- [03 技术选型](docs/03-tech-stack.md) — Go + Vue 的取舍与落地约定
- [04 路线图](docs/04-roadmap.md) — 分期计划与验收口径
- [05 已决策项](docs/05-decisions.md) — 已拍板的技术与范围决策（开发以此为准）
- [20 容器监控与通知](<docs/20-container-monitoring.md>) — 后台启用步骤、钉钉/SMTP、告警边界与主密钥备份
- [21 内置本机执行](<docs/21-embedded-local.md>) — 本机/远程拓扑、运行权限、开关及同机升级边界

## 目录结构

```
arkade/
├── README.md
├── docs/              # 设计文档
├── protocol/          # 共享协议模块（信封、方法名、载荷定义，Hub 与 Agent 共用）
├── hub/               # 主程序（Go，控制面）
├── agent/             # 代理程序（Go，部署在被管服务器）
├── web/               # 前端（Vue 3）
└── deploy/            # 配置模板与 systemd 单元
```

## 容器部署

提供三套仅使用 `image` 的编排，不包含 `build`；普通参数直接写在编排中，只有密码、令牌使用 `.env`，不再要求挂载 TOML。Hub 镜像包含 Docker CLI 与 Compose；启用本机执行时，Hub 以 root 运行并挂载宿主 Docker socket，该权限近似宿主 root，必须只授予受信管理员。

- [Hub + PostgreSQL](<deploy/docker/hub-compose.yml>)：控制端、默认内置本机执行及独立数据库；纯控制面可关闭本机执行并移除不需要的 socket 挂载。
- [仅 Agent](<deploy/docker/agent-compose.yml>)：部署在远程受管服务器。
- [Hub 本机执行 + PostgreSQL 合并编排](<deploy/docker/combined-compose.yml>)：不再启动独立 Agent，保留原 `arkade-agent-data` 卷与 Compose project，以沿用本机工作目录。

旧版同机部署升级必须先备份数据库、设置主密钥和本机工作卷，停止旧 Agent，再在原 project 下用新版编排执行 `up -d --remove-orphans`。不得使用 `down -v` 或删除数据卷；不要让旧 Agent 与内置执行同时写同一 workspace。这里是升级指引，不会自动操作生产环境。

数据库仅支持 PostgreSQL，使用 GoFrame 官方驱动；本版本采用全新库，不导入或兼容 SQLite。数据库密码放对应凭据样例，PG 地址、用户名、库名直接写编排；PostgreSQL 数据与 Hub 的 OIDC 加密主密钥分别持久化。

配置样例、首次创建 Agent 专属密钥的步骤及升级边界见 [容器部署说明](deploy/docker/README.md)。使用支持无 TOML 启动的新镜像，生产固定镜像版本，升级保留原数据卷并先备份。框架重构记录见 [GoFrame 迁移说明](docs/17-goframe-migration.md)，手动升级见 [部署与回退](docs/18-manual-goframe-deployment.md)。

## 源码快速开始

前置：Go 1.24+、Node 20+、pnpm。

```bash
# 1. 构建前端
cd web && pnpm install && pnpm build

# 2. 准备 Hub 配置
cd ../hub && go build -o bin/arkade-hub ./cmd/hub
./bin/arkade-hub -init-config -c config.toml   # 生成模板后修改

# 3. 配置可用的 PostgreSQL DSN 后启动 Hub
# 默认内置本机执行；需本机 Docker/Compose 与相应权限
# 初始管理员密码必须显式提供，代码内无默认密码
ARKADE_INITIAL_PASS='你的密码至少8位' ./bin/arkade-hub -c config.toml

# 4. 可选：仅在远程被管服务器构建并启动 Agent（Hub 本机不需要）
cd ../agent && go build -o bin/arkade-agent ./cmd/agent
./bin/arkade-agent run -c agent.toml

# 5. 浏览器打开 http://127.0.0.1:8080 ，用配置里的管理员账号登录
```

远程 Agent 与 Hub 失联时仍可通过该服务器的本地 CLI 管理（决策 D-05）。Hub 内置执行不等于一个独立离线守护进程：Hub 停止后其管理、采集与通知也会停止，已有容器是否继续运行由容器运行时与自身重启策略决定。远程 Agent 的命令示例：

```bash
./arkade-agent ps                 # 列出本机容器
./arkade-agent start <id|name>
./arkade-agent stop <id|name>
./arkade-agent logs <id|name> -n 100
```

## 项目状态

**阶段一骨架已打通**（Hub + Agent + 前端三层能连上、能看能管）：

- Hub：登录鉴权（bcrypt + 会话 Cookie）、内置本机执行、远程 Agent 连接管理、容器操作下发、PostgreSQL 版本化建表迁移器、SPA 托管
- 本机边界：`online` 仅表示进程内执行可用，不代表 Docker 健康；运行时为 `none` 时，各类资源列表为兼容可成功返回空集合，但不代表 Docker 没有资源或健康；监控、日志与实际控制不支持
- Agent：Docker / Podman 运行时抽象、反向连接 + 指数退避重连、心跳、本地 CLI 离线自治
- 前端：登录页、服务器列表、容器列表（启停二次确认 + 日志抽屉）
- 端到端验证：真实 Hub 与真实 Agent 通过 WebSocket 完成握手、心跳保活、请求往返与离线拒绝

**尚未实现**（按路线图推进）：编排双份存储、接管、镜像与仓库、迁移、审计与权限管理界面、WebSocket 实时推送。

**已知限制**：运行时层只用假引擎做过单元测试，尚未在真实 Docker / Podman 守护进程上验证；`go test -race` 因本机缺少 CGO 工具链暂不可运行。

## 命名说明

- **Arkade**：中文直译「方舟」，取其收纳万物、可托底之意。
- **容器方舟**：中文正式名，日常简称「方舟」。

## 许可证

本项目采用 [Apache License 2.0](LICENSE) 开源协议。

选择它的原因：Arkade 的目标是让人自己部署、自己改造、长期自托管，
Apache 2.0 既允许商用与闭源二次开发，也自带专利授权与商标条款，
比 MIT 更适合"要接进企业环境"的基础设施类项目。

再分发本项目时，请按协议要求保留 [NOTICE](NOTICE) 文件中的署名信息。

Copyright 2026 Arkade Authors

## 安全

发现安全问题请勿公开提交 Issue，请按 [SECURITY.md](SECURITY.md) 中的渠道私下报告。
