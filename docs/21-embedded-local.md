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
- 本机固定 ID 为 `agent-local`。容器模板使用 `/data/local` 工作目录，与 `/data/settings.key` 一起保存在单个 `arkade-hub-data` 卷中。
- Hub 仍依赖 PostgreSQL；内置执行不替代数据库。
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

本机生命周期由启动配置控制，配置改变后重启 Hub 生效，不在网页执行热启动或后台重启。

### 本机记录管理

本机不是一台可以随意重置身份的远程 Agent：

| 操作 | 约束 |
| --- | --- |
| Enable | 不用作本机启动入口；使用本机配置并重启 Hub |
| Disable | 拒绝；需要停用本机时使用启动配置并重启 Hub |
| Delete | 拒绝；保留固定本机身份与关联数据 |
| Reset Token | 拒绝；本机进程内调用不使用接入 Token |

这些约束不应被理解为提供了本机热切换功能。

## Docker、Compose 与信任边界

Hub 镜像增加 Docker CLI 与 Compose，以执行本机容器和编排操作。容器部署启用本机时，Hub 以 root 运行并挂载宿主 Docker socket。此权限**近似宿主 root**，不是普通只读监控权限，也不是隔离沙箱；应限制 Hub 管理员、镜像来源、网络入口及 socket 暴露范围。

源码或二进制部署同样要保证 Hub 所在环境有可用的 Docker/Compose 和运行时访问权限。纯远程控制面关闭本机后，可以去掉不再需要的 socket 挂载；具体编排配置见 [容器部署说明](<../deploy/docker/README.md>)。不要为了让界面显示 online 而放宽宿主权限。

## 在线状态与监控

- `agent-local` 显示 **online** 仅代表 Hub 进程内执行可用，不代表 Docker 正常、socket 可达、Compose 命令可用或容器健康。
- 运行时为 `none` 时，List 可成功返回空集合，但不代表 Docker 零容器或健康；监控采样与实际控制不支持。无指标、采集失败或不完整快照不能解释成零占用、健康或恢复。
- 容器是否健康依靠实际运行状态、健康检查与有效采集样本；没有配置健康检查也不等于 healthy。
- Hub 运行时仍会监控远程 Agent 失联；失联可能由网络或 Agent 故障造成，不等于确定宿主宕机。
- **Hub 停止时无法为自身发通知**，内置执行、采集和后台通知随进程停止。须另配外部可用性监控。此功能不会自动修复容器，也不是 Hub 自愈。
- 已有容器是否在 Hub 停止后继续运行，由运行时和容器重启策略决定；不要将继续运行误认为仍有 Arkade 管理进程在工作。远程 Agent 的独立 CLI 离线管理能力不等于内置执行也有一个独立守护进程。

监控开关、通知凭据与告警抑制规则见 [容器监控与通知](<20-container-monitoring.md>)。

## 安装与持久化

两份 Hub 模板统一使用 `arkade-hub-data` → `/data`，Docker socket 是独立 bind mount，不是第二个数据卷。仅 Hub 连接已有 PostgreSQL；combined 另提供 PostgreSQL 容器和独立数据库卷，两种方案二选一。

备份数据库与整个 `/data`，主密钥须与数据库配套恢复；业务容器数据还需独立备份。具体参数见 [容器部署说明](<../deploy/docker/README.md>)。
