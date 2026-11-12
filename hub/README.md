# Arkade Hub（主程序）

控制面服务。负责 Web 界面、用户鉴权、Agent 连接管理、容器操作下发。**不直连任何 Docker 守护进程**（架构红线，见 `docs/01-architecture.md`）。

## 当前数据库与运行方式

默认且唯一业务数据库为 PostgreSQL，使用 GoFrame 官方 pgsql 驱动与 gdb。此版本是全新数据库，不兼容/导入 SQLite；原 SQLite 专用备份工具已移除。运行时通过环境变量配置连接，容器不要求 TOML；普通参数直接写编排，只有密码和令牌引用对应 `.env`。

服务器仅需一份完整编排和对应凭据文件，详见 [独立容器部署](../deploy/docker/README.md) 和 [PG 新部署/备份](../docs/18-manual-goframe-deployment.md)。Hub OIDC 主密钥独立持久化，不再从数据库文件路径推导。

## 目录结构

```
hub/
├── cmd/hub/main.go              # 进程入口：解析参数、加载配置、优雅退出
└── internal/
    ├── config/                  # TOML 配置加载与校验（未知字段直接拒绝）
    ├── store/                   # GoFrame PostgreSQL 数据访问层（版本化建表在 dbmigrate）
    │   ├── store.go             # 连接池与事务封装（单写连接 + WAL）
    │   ├── migrate.go           # 迁移执行器（事务内执行、失败整体回滚）
    │   ├── migrate_sql.go       # 迁移脚本（只增不改）
    │   ├── users.go             # 用户、用户组授权查询
    │   ├── sessions.go          # 会话（只存令牌哈希）
    │   └── agents.go            # Agent 记录
    ├── user/                    # 用户模型、权限档位、密码哈希（bcrypt）
    ├── auth/                    # 登录、会话签发与校验
    ├── agentlink/               # Agent 连接管理与消息路由（核心）
    │   ├── registry.go          # 在线表与 Call（离线立即失败，不排队）
    │   ├── connection.go        # 单连接读/写/心跳三循环
    │   ├── server.go            # 建连、鉴权、握手、事件分发
    │   ├── ops.go               # 容器操作高层接口（HTTP 层只调这里）
    │   ├── identity.go          # Agent 身份生成、恒定时间比较
    │   └── handler.go           # 向业务层回调的接口定义
    └── httpx/                   # HTTP 层
        ├── router.go            # 路由装配 + SPA 回落
        ├── middleware.go        # 日志/恢复/安全头/认证（透传 Hijacker）
        ├── authz.go             # 权限校验
        ├── respond.go           # 统一错误响应与状态码映射
        ├── handlers_auth.go     # 登录、退出、当前用户
        ├── handlers_agents.go   # 服务器列表与容器接口
        ├── agent_sink.go        # Agent 事件落库
        └── runtime.go           # 启动装配
```

## 关键设计

### 在线状态以内存为准

数据库里的 `status` 只是最后一次上报的**快照**。进程重启后所有 Agent 都视为离线，直到重新握手。这样可以避免「重启后 Hub 认为 Agent 都在线、实际发请求全超时」的假在线。

### 离线立即拒绝，不排队

Agent 不在线时，`Registry.Call` 立刻返回 `agent_offline`（HTTP 503），不发、不排队、不重试。危险操作在重连后补执行是极其危险的行为（`docs/02-features.md` 第 1 节明确禁止）。

### 握手顺序：先登记在线表，再回 HelloAck

反过来写会产生真实竞态——Agent 收到 ack 后立刻下发首个请求，而连接尚未登记，会偶发「刚连上就报离线」。该问题由测试 `TestAgentConnectHandshake` 锁死。

### 中间件必须透传 Hijacker

日志中间件的 `statusWriter` 如果不实现 `http.Hijacker`，WebSocket 升级会稳定失败并返回 500（`websocket: response does not implement http.Hijacker`）。这是踩过的真实缺陷，已在 `middleware.go` 修复并由连接测试覆盖。

### 错误响应契约

所有错误统一为 `{"error":{"code":"...","message":"..."}}`：

| 错误码 | HTTP | 前端行为 |
| --- | --- | --- |
| `unauthorized` | 401 | 跳登录页 |
| `forbidden` | 403 | 提示无权限 |
| `not_found` | 404 | 提示目标不存在 |
| `agent_offline` / `timeout` / `runtime_failed` | 503 | 提示稍后重试 |
| `unsupported` | 501 | 展示 Agent 返回的真实原因（如未安装运行时） |

只有 `internal` 才用兜底文案掩盖细节；业务错误必须原样透出，否则运维看不到真实原因。

## 开发

数据库测试必须设置 `ARKADE_TEST_PG_DSN`，使用具有 CREATEDB 权限的专用隔离测试 PostgreSQL，不可指向生产账号。测试创建独立库并清理，未配置时明确失败。

```bash
go build -o bin/arkade-hub ./cmd/hub
go test ./... -count=1
go vet ./...
gofmt -l .
```

## 测试覆盖

- `internal/config`：配置校验、未知字段拒绝、时长解析边界
- `internal/httpx`：鉴权 401 契约、登录成败、账号枚举防护、退出失效、空列表序列化、未知 API 返回 JSON，以及通过真实 WebSocket 的 Agent 握手、请求往返、心跳、离线回调、Token 校验

`store` 已有真实 PostgreSQL 仓储及跨连接并发回归；`dbmigrate` 验证新库初始化、重复启动、脏位、校验和、结构漂移及锁等待。`agentlink` 另有独立协议测试。测试通过不代表生产部署或数据库恢复演练已完成。
