# 21 Hub 内置本机执行

> 2026-11-13 确认的新架构：**Hub 默认内置本机，远程才部署 Agent**。本文覆盖历史架构中「Hub 不能操作本机 Docker」「同机也必须独立 Agent」的旧约束，不表示所有路线图能力已实现。

## 拓扑与调用边界

```text
浏览器 → Hub（鉴权、业务、PostgreSQL、监控）
           ├─ 内部 Registry → agent/embedded → 本机 Docker / Compose
           └─ 远程 WebSocket ← 独立 Agent → 远程容器运行时
```

- 本机复用 `agent/embedded` 公开包的执行能力，不复制另一套运行时实现。
- 本机请求经内部 Registry 调用，**不启动本地 WebSocket、不创建本机接入 Token**；远程 Agent 的反向连接和专属凭据机制保持独立。
- 本机固定 ID 为 `agent-local`，沿用已有本机 identity 与 workspace，不为切换执行方式创建第二台“本机”。默认工作目录为 `/opt/arkade/agent`。
- Hub 仍依赖 PostgreSQL；内置执行不替代数据库，也不重新引入 SQLite 或旧数据导入。
- Hub 仍是单控制端。进程内执行不等于多 Hub 高可用，不提供主备切换或自愈保证。

## 开关与启动

默认启用本机执行。只做远程控制面时，可以二选一：

```sh
ARKADE_LOCAL_ENABLED=false
```

```toml
[local]
enabled = false
```

变更启动配置后须重启 Hub。关闭本机执行不等于删除本机记录、编排目录、容器或数据卷；不要以“关闭”为由清空已有 identity/workspace。

已有 `agent-local` 记录若处于 disabled 状态，必须保留禁用状态，**不启动内置执行**，不能在升级时悄悄恢复执行权限。

### 本机记录管理

本机不是一台可以随意重置身份的远程 Agent：

| 操作 | 约束 |
| --- | --- |
| Enable（计划后台入口） | 只解除旧记录的 disabled 标记；不能热启动执行器，仍须启用本机配置并重启 Hub |
| Disable | 拒绝；需要停用本机时使用启动配置并重启 Hub |
| Delete | 拒绝；保留固定本机身份与关联数据 |
| Reset Token | 拒绝；本机进程内调用不使用接入 Token |

计划入口不应被理解为已提供的热切换功能。旧 disabled 记录解除后，在重启前仍不可执行。

## Docker、Compose 与信任边界

Hub 镜像增加 Docker CLI 与 Compose，以执行本机容器和编排操作。容器部署启用本机时，Hub 以 root 运行并挂载宿主 Docker socket。此权限**近似宿主 root**，不是普通只读监控权限，也不是隔离沙箱；应限制 Hub 管理员、镜像来源、网络入口及 socket 暴露范围。

源码或二进制部署同样要保证 Hub 所在环境有可用的 Docker/Compose 和运行时访问权限。纯远程控制面关闭本机后，可以去掉不再需要的 socket 挂载；具体编排配置见 [容器部署说明](<../deploy/docker/README.md>)。不要为了让界面显示 online 而放宽宿主权限。

## 在线状态与监控

- `agent-local` 显示 **online** 仅代表 Hub 进程内执行可用，不代表 Docker 正常、socket 可达、Compose 命令可用或容器健康。
- 运行时为 `none` 时，List 为兼容可成功返回空集合，但不代表 Docker 零容器或健康；监控采样与实际控制不支持。无指标、采集失败或不完整快照不能解释成零占用、健康或恢复。
- 容器是否健康依靠实际运行状态、健康检查与有效采集样本；没有配置健康检查也不等于 healthy。
- Hub 运行时仍会监控远程 Agent 失联；失联可能由网络或 Agent 故障造成，不等于确定宿主宕机。
- **Hub 停止时无法为自身发通知**，内置执行、采集和后台通知随进程停止。须另配外部可用性监控。此功能不会自动修复容器，也不是 Hub 自愈。
- 已有容器是否在 Hub 停止后继续运行，由运行时和容器重启策略决定；不要将继续运行误认为仍有 Arkade 管理进程在工作。远程 Agent 的独立 CLI 离线管理能力不等于内置执行也有一个独立守护进程。

监控开关、通知凭据与告警抑制规则见 [容器监控与通知](<20-container-monitoring.md>)。

## 从旧同机 combined 部署升级

新版 combined 删除独立 Agent 服务，但必须**保持原 Compose project 和 `arkade-agent-data` 卷**，让 Hub 内置执行沿用本机 identity/workspace。这里只说明经确认的操作次序，不会替用户操作生产环境。

1. 维护前核对现有 Compose project、卷名、挂载路径、本机身份与 workspace，记录旧 Agent 是否 disabled。
2. 备份 PostgreSQL、原设置加密主密钥和本机工作卷，并确认备份可用于恢复。数据库与原主密钥须配套保留；不能靠生成新密钥恢复旧密文。
3. **先停止旧独立 Agent**，确认它不再写本机 workspace。不要让旧 Agent 与 Hub 内置执行同时使用同一工作目录。
4. 更新镜像与 combined 编排，保持原 project 和既有数据卷；使用新版编排执行：

   ```sh
   # 在原 project 下执行；若原来显式指定了 -p，继续使用相同值。
   docker compose -f combined-compose.yml up -d --remove-orphans
   ```

5. 核对 `agent-local` 沿用原记录、workspace 内容仍在、PG 与原主密钥挂载正确，再检查运行时和监控结果。原记录 disabled 时不应自动启动；需按前述规则解除并重启。

**禁止使用 `down -v`、`docker volume prune` 或删除工作卷作为升级步骤。** `--remove-orphans` 用来移除新编排不再声明的旧服务容器，不是删除数据卷的授权。不要改 project 名后意外创建空卷，也不要删除旧运行环境文件或盲目初始化新 identity。

若升级异常，应先停止新执行器，再根据已核实的镜像、编排、数据库、主密钥和工作卷备份决定恢复方案，始终避免两个执行器同时写同一 workspace。部署参数以 [容器部署说明](<../deploy/docker/README.md>) 为准，本文不替代实际备份与回退验证。
