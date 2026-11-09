# Arkade Agent（`arkade/agent`）

被管服务器上的代理程序：主动反向连接 Hub，执行本机容器操作；Hub 失联时仍可本地自治管理。
本 README 只覆盖 Agent 侧的代码结构与开发约定，产品整体设计见 `../docs/`。

## 代码结构

```
agent/
├── go.mod                     # module arkade/agent，replace 到 ../protocol
├── cmd/agent/main.go          # 进程入口：只做 os.Args → cli.Main → 退出码
└── internal/
    ├── runtime/               # 运行时抽象层（D-01：Docker + Podman）
    │   ├── runtime.go         # Runtime 接口与 Container/LogOptions 等模型
    │   ├── errors.go          # ErrNone / ErrNotFound / ErrUnsupported 与 Kind
    │   ├── api.go             # Docker 兼容 API 端点常量（禁止在别处手写路径）
    │   ├── client.go          # 手写的最小 HTTP over unix socket 客户端
    │   ├── engine.go          # Docker/Podman 共用的实现（列表/启停/删除/日志）
    │   ├── discover.go        # 套接字候选清单与归一化
    │   ├── probe.go           # 探测 → 判定引擎类型；noneRuntime 空实现
    │   └── factory.go         # runtime.New(cfg) 工厂
    ├── link/                  # 反向连接客户端（gorilla/websocket）
    │   ├── backoff.go         # 1s→2s→4s…60s 退避（纯函数，可测）
    │   ├── dial.go            # Hub 地址归一化、认证请求头
    │   ├── tls.go             # 仅测试用的跳过校验 TLS 配置
    │   ├── session.go         # 单连接的读/写/心跳三个循环与出站队列
    │   ├── agent.go           # 握手、重连循环、快照上报
    │   └── state.go           # agent_id 原子持久化、主机名等小工具
    ├── handler/               # 方法注册表与处理器（在线通道与本地 CLI 共用）
    │   ├── registry.go        # Register / Dispatch / RegisterDefaults
    │   ├── container.go       # container.list / start / stop 与摘要归一化
    │   ├── logs.go            # container.logs 与行数上界裁剪
    │   ├── info.go            # runtime.info
    │   └── errors.go          # 内部错误 → 协议错误码的唯一翻译层
    ├── config/                # TOML 配置、校验、数据目录状态
    │   ├── config.go          # 读取、默认值、人话校验
    │   └── state.go           # agent_id 持久化、容器归属登记表
    ├── cli/                   # 命令行（D-05 离线管理入口）
    │   ├── cli.go             # 子命令分派与本地初始化
    │   ├── args.go            # 位置参数与选项混排解析
    │   ├── run.go             # run：服务模式
    │   ├── local.go           # ps / start / stop / logs / info
    │   └── output.go          # 表格输出与错误呈现
    └── logging/logging.go     # slog 日志器构造（text/json、级别）
```

## 关键设计：为什么在线与离线共用一套处理器

`handler.Registry` 只有一张方法表。Hub 下行请求与本地 CLI 都会构造**同构**的
`envelope.Envelope`（同样的 v/type/method/payload）并调用同一个 `Dispatch`：

```
Hub ──WebSocket──► link.session ──┐
                                  ├──► handler.Registry.Dispatch ──► runtime.Runtime ──► docker/podman
CLI ──callLocal ──────────────────┘
```

这样离线自治（D-05）与在线行为不会分叉；参数校验与错误码映射也只有一处实现。

## 常用命令

```bash
cd agent
go build ./...
go vet ./...
go test ./...
gofmt -l .        # 应无输出
```

交叉编译 Linux 静态二进制（发布用）：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags "-s -w -X arkade/agent/internal/cli.Version=0.1.0" \
  -o arkade-agent ./cmd/agent
```

## 已实现范围与边界

- 已实现：运行时抽象层（Docker/Podman 探测与容器列表/启停/删除/日志）、
  反向连接（握手 / 心跳 / 退避重连 / 快照上报）、本地 CLI、配置、日志、单元测试。
- 未实现（后续阶段）：容器删除方法的协议注册（协议里暂无 `LocalMethodContainerRemove`
  常量，`runtime.Runtime.Remove` 已就绪、等协议补常量后一行注册即可）、
  编排文件同步、镜像/卷传输、自升级、断线期间待办队列持久化。
