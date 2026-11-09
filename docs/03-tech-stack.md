# 03 技术选型

## 结论

**Go（主程序 + 代理程序）+ Vue 3（前端）**

## 为什么 Go 胜出

| 维度 | Go | Rust | 本项目权重 |
| --- | --- | --- | --- |
| 负载性质 | I/O 密集转发、搬运 | 同为 I/O | 相当，语言性能不构成瓶颈 |
| 容器生态 SDK | docker/docker、containerd client、podman REST、client-go 均为一等公民 | 需自研或薄封装 | Go 明显胜出 |
| 单二进制分发 | 交叉编译静态二进制，无 glibc 依赖 | musl 可行但配置成本更高 | Go 胜出 |
| 流式管道 | `io.Pipe` + `io.Copy` 天然贴合镜像/卷搬运 | 需手写流处理 | Go 胜出 |
| 开发效率 | 高 | 中低（借用检查、生命周期耗时） | Go 胜出 |
| 团队熟悉度 | 用户既有栈 | 本机未装 Rust 工具链 | Go 胜出 |
| 内存安全 | GC 保证 | 编译期保证更强 | 本场景差异可接受 |

Rust 的最佳用武之地：未来的高频指标采集 sidecar、加密与内容校验小工具。可作为局部组件引入，不作主载体。

## 主程序框架

**建议：标准库 `net/http` + `chi` 路由 + `gorilla/websocket`（或 `nhooyr/websocket`），不用 GoFrame。**

理由：

- 本项目是**网关型控制面**，核心是长连接管理与消息路由，不是 CRUD 业务系统。
- GoFrame 的价值在 ORM、代码生成、规范的业务分层，对这类控制面收益不大，反而让请求链路变厚、排障变难。
- 但若用户坚持沿用 GoFrame（熟悉度/既有经验），**需明确：只在业务 CRUD 层使用，WebSocket 与 Agent 连接层保持标准库**。两者可共存。

## 代理程序

- 单静态二进制，无外部依赖。
- 通过 unix socket 或 TCP 访问本机运行时（`/var/run/docker.sock`、podman socket 等）。
- 内置本地编排文件存储目录，建议 `/opt/arkade/stacks/`。
- 自带自升级能力（从 Hub 拉取新版二进制并热替换），配合版本协商。

## 前端

- Vue 3 + `<script setup>` + TypeScript
- Vite 构建
- Pinia 状态管理
- 组件库待定（Element Plus / Naive UI / 自研，后续对话确认）
- 实时数据经 WebSocket 推送，不靠轮询

## 通信协议

- WebSocket，双向
- 消息信封统一（见 01 架构文档 3.2）
- 大文件（镜像、卷）不走 WebSocket，走**独立的流式 HTTP/2 通道**，WebSocket 只传控制信令与进度
  - 原因：把 2GB 镜像塞进 WebSocket 消息队列会拖垮整条连接，必须分离数据面与控制面

## 项目结构约定

```
arkade/
├── hub/               # 主程序（Go module: arkade/hub）
│   ├── cmd/hub/
│   ├── internal/
│   │   ├── agentlink/     # Agent 连接与消息路由
│   │   ├── stack/         # 编排仓库与双份同步
│   │   ├── registry/      # 镜像仓库凭据中心
│   │   ├── migration/     # 迁移任务编排
│   │   ├── auth/          # 用户与权限
│   │   └── audit/         # 审计
│   └── ...
├── agent/             # 代理程序（Go module: arkade/agent）
│   ├── cmd/agent/
│   ├── internal/
│   │   ├── runtime/       # 运行时抽象（docker/podman/containerd）
│   │   ├── stack/         # 本地编排文件管理
│   │   ├── transfer/      # 镜像/卷传输
│   │   └── upgrade/       # 自升级
│   └── ...
├── web/               # 前端（Vue 3）
├── docs/
└── deploy/
```

## 代码规范（沿用用户既有要求）

- 按职责拆成短小独立模块，禁止单文件过长。
- 共用逻辑统一复用，禁止复制实现。
- 关键逻辑、设计原因与边界必须有清晰注释。
