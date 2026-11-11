# Hub 分阶段迁移到 GoFrame

## 已确认的技术路线

- HTTP：GoFrame v2，保持现有 API、错误状态、会话和 Agent WebSocket 契约。
- 数据访问：GoFrame 官方 SQLite 驱动、gdb 以及由 `gf gen dao` 生成的 DAO / DO / Entity。生成文件不得手写。
- 表结构版本：golang-migrate；不将 DAO 生成器误当作建表工具。
- 数据库继续使用 SQLite，原地升级，不重建业务数据。
- 锁定 GoFrame 与官方 SQLite 驱动 v2.9.6、golang-migrate v4.19.1；兼容项目 Go 1.24。

## 全部 HTTP 原生迁移（第三阶段，当前源码状态）

用户要求完成后自行配置部署，本阶段不修改服务器环境。

- 新增 51 条业务原生路由：服务器管理 7、容器及资源 14、镜像任务 8、核心编排 14、外部编排及导入 8。与前两阶段 19 条合计 **70 条 HTTP API**；另将 Agent WebSocket 握手独立接入 typed controller，总计 71 条 g.Meta 路由。
- 全部业务入口使用 `(ctx,*Req)(*Res,error)` + `Bind`，删除剩余 19 个旧 handler/权限适配文件。仅静态资源及未知路由使用 BindHandler；旧 wrapToGF 只供隔离测试探针，没有生产业务调用。
- API/controller/service 按功能域和读取、写入、任务、上传、下载、成员、授权拆文件；实现文件均小于 300 行。公共权限决策集中在 controller.Access，Compose 校验、哈希、脱敏与视图集中于 service/compose，避免多模块复制。
- 新模块仅让 GF 绑定路径/query；所有可能匹配路径名的大小写/下划线 query 别名被排除。原 body 通过 context 临时隐藏，controller 在权限/项目存在性检查后再严格解码 typed Input，保持旧错误优先级及各自长度/未知字段策略。
- 归档上传下载维持流式、不读全量内存；202、空 411、204、WebSocket hijack 使用明确响应策略，不通过 Recorder 包装旧 handler。
- 此处“全部迁移”指 GoFrame HTTP 与数据访问架构迁移。随后完成了跨服务器 Compose 迁移生产 backend 装配、Agent 计划与安全传输，以及隔离双引擎验收，详见 [跨服务器编排迁移验收](19-compose-migration-completion.md)。框架迁移与业务验收分别记录，不混作一项证据。
- 验证：Go 1.24.13、CGO=0，Windows Hub 全量测试及 vet 通过（HTTP 104.210 秒），Linux 完整 HTTP 实跑通过；Agent 全量测试和 protocol 测试通过。真实网络补测 raw JSON Content-Type 上传、tar 下载、空 411 和慢上传取消释放。Windows/Linux 日志均未发现 `panic serving` / 重复 WriteHeader。
- 本地源码状态与远端镜像/测试服状态分开：本阶段未部署，不宣称 GHCR latest 含新改动；配置前必须确认部署源码包含本阶段提交。第二阶段的测试服状态是历史记录，不代表本阶段已部署。

## 管理接口模块化并发迁移（第二阶段，历史）

三名子智能体分别独占 users、groups、audit 的 API 和 controller 子目录；Lead 独占路由、中间件、严格解码与集成，不并发修改同一共享文件。

- 新增原生 Bind：用户 5 条、用户组 8 条、审计查询 2 条；加上首阶段 4 条，共 19 条。容器、服务器、镜像和编排等其他接口仍保留桥接，并未宣称全部迁移。
- 按读取/写入、成员、授权、筛选、视图拆文件；新的 controller 实现文件均低于 200 个物理行。路由入口由 525 行降为约 330 行，桥接与 SPA 回退各独立文件。
- 删除旧 users/groups/audit 三个 handler 实现，测试仅保留 API 视图类型别名，不保留第二套业务逻辑。
- 写接口严格 JSON 解码结果按请求存入 typed context，补偿 GF 通用转换跳过显式 null，以及大小写重复键的解码差异；路径身份仍来自 GF 路由参数，不信任 JSON/query 的同名字段。GF 只接收首个有效 JSON 值，保持旧 decoder 允许尾随内容的既有行为，不借本次迁移悄悄收紧契约。
- GET 审计只读取 query，重复键保留首值，丢弃 body；写接口不接受 query/form 代替 JSON。管理接口保留旧 decoder 精确错误和 EOF 行为，认证 → 审计 → admin → 解码的顺序不变。
- 独立模块单测通过；Go 1.24.13、CGO=0 下 Windows Hub 全量测试通过（最终冻结节点 HTTP 64.494 秒）及 vet 通过；Linux 完整 HTTP 实际执行通过。两平台输出均未出现 `panic serving` / `superfluous response.WriteHeader`。
- 本阶段尚未部署，测试服仍为第一阶段 `gf-12a2af8`；不把本地测试当作远端验收。

## 已实测的驱动组合

GoFrame 官方驱动与 migrate 的 `database/sqlite` 都注册 SQL 驱动名 `sqlite`，同进程导入会在 init 阶段 panic，传 `WithInstance` 不能绕过。采用 migrate 官方 `database/sqlite3.WithInstance` 接受 GF 驱动创建的纯 Go `*sql.DB`；只借用其迁移执行器，不调用 mattn 的 `Open`。探针在 Windows 与 Linux 的 `CGO_ENABLED=0` 下实际运行通过。依赖树中的 `modernc.org/sqlite/lib` 是底层库，不等于注册冲突；必须禁止的是第二个顶层驱动注册 import。

迁移器与业务池分别建立和关闭连接，防止 migrate.Close 关闭仍在使用的业务连接。生产接入还需验证并发、旧库接管和真实启动装配，探针通过不代表主项目改造已完成。

Lead 已在 Go 1.24.13、Windows amd64、CGO_ENABLED=0 条件下独立复跑 SQLite 探针通过；主项目官方驱动替换后，实际 NewApp 装配的健康检查、登录、Cookie、会话和 Agent 独立认证回归测试通过。主项目沿用已有依赖约束时底层 SQLite 与独立探针可能不是相同版本，必须以主项目回归结果为准，不能把探针的底层版本当作发布版本。

## 阶段与放行条件

1. **基线与兼容性验证**：保存改造前改动及测试基线；独立模块实测 GoFrame HTTP、官方 SQLite 驱动与 golang-migrate 的组合。未完成真实端到端的编排迁移不能作为已验收功能。
2. **迁移工具接管**：将既有 1–10 版 SQL 原样提取为受版本管理的文件；新空库执行全部版本，旧库校验版本连续性及实际结构后接管。不能直接取最大版本号 force。旧迁移历史保留，新工具使用独立版本表。
3. **数据库访问迁移**：从按迁移创建的临时库生成 DAO；分模块替换，保留事务边界、条件更新及影响行数判断。避免全局数据库组污染并行测试。
4. **HTTP 迁移**：先健康检查和认证，再按资源迁移；过渡适配只能临时使用，最终移除 chi，不能只换一层服务器外壳。
5. **收尾发布**：删除旧实现，更新文档、构建和 CI，完整回归后在测试环境发布并验证恢复路径。

## 必须验证的边界

- 官方 SQLite 驱动底层与 golang-migrate 驱动是否重复注册；不得同时盲目导入多套注册同名的驱动。
- Go 版本约束、纯 Go 构建（CGO_ENABLED=0）、Windows 运行与 Linux 构建/运行。
- WAL、外键、busy_timeout、单写连接池；迁移连接的所有权明确，关闭 migrate 不得误关业务连接。
- SQLite 迁移器的锁不等于跨进程数据库锁；迁移必须在开始服务前运行，并防止多进程并行升级。
- 迁移 dirty、结构不匹配、版本断档或未知更高版本：拒绝启动，不自动 force，不覆盖。
- 迁移文件校验和需另行维护，golang-migrate 的版本/dirty 表不提供完整脚本防篡改校验。
- 备份必须是一致性 SQLite 备份；WAL 写入期间不能只拷贝主文件。恢复要实测，down 不等于无损恢复。
- 框架自动时间字段不得改变既有 Unix 秒和零值语义；不新增软删除语义。
- 管理员保护、Agent 凭据撤销、授权、编排乐观锁、幂等和审计不可因 ORM 改造而弱化。
- JSON 格式、HTTP 状态、Cookie、安全属性、WebSocket Upgrade、下载/流式输出、取消、超时及 SPA 回退必须保持兼容。
- ghttp v2.9.6 需要真实 `Start` 初始化 sessionManager，不能仅把未启动的实例当作 http.Handler。外层 net/http 在真实 TCP 请求中可能恢复清理 panic，客户端已收到 200 不构成生命周期通过的证据。生产采用原生启动/关闭；测试也初始化完整生命周期并验证请求结束无 panic。此前探针“不需 Start”的结论已撤回。
- GoFrame v2.9.6 的 Shutdown 不接受 context，使用其 GracefulShutdownTimeout。生产保持现有信号入口并配置框架退出期限；不通过反射初始化私有字段。
- 既有测试可以调整框架装配，但不能放宽业务与安全断言来掩盖回归。

## 当前阶段的实现边界

- 业务 store 已迁到实例隔离的 gdb，生产 `DB` 不再持有独立 `*sql.DB` 字段或旧 `Tx/sqlTx`。生成 DAO 提供表名和列元数据，DO 用于结构化写入，Entity 用于读取映射；复杂 JOIN、原子 `UPDATE … RETURNING` 仍使用 gdb 的参数化 SQL，不宣称所有写入都已变为 DO。
- SQLite 单连接池下，ORM 首次字段处理可能绕过事务查询表元数据，与已占用的事务连接形成等待环。启动时在事务外预热业务表元数据；冷启动事务回归带短期限，禁止通过增加连接数掩盖问题。
- HTTP 生命周期和路由已由 GoFrame 接管，chi 依赖已移除。健康检查及 login/logout/me 四条路由使用 `g.Meta + group.Bind`；其余路由仍使用原生 `BindHandler` 调用已有 net/http 业务 handler，是明确的分阶段过渡，不等于全部 controller 迁移完成。
- 原生 Bind 的响应中间件保留顶层 JSON 和既有错误信封，同时更新 GoFrame 状态及实际写入计数；不能启用默认响应包装改变前端契约。认证请求保留仅从 JSON body 读取凭据的既有语义。
- HTTP Shutdown 不等待 hijack 后的 WebSocket。运行时另行跟踪 socket 和处理函数，关闭时包含未完成 Hello 的连接并等待退出，再关闭数据库。若等待超时，不关闭仍被处理函数使用的业务池。审计清理任务也必须在关闭池之前 join；目前其最长等待为 30 秒，不能宣称所有退出动作都严格受调用方 context 限制。

## SQLite 一致性备份与恢复工具

在 `hub` 目录运行（或先构建 `./cmd/dbtool`）：

```sh
go run ./cmd/dbtool backup -source /data/arkade.db -destination /backup/arkade-before-gf.db
go run ./cmd/dbtool check -source /backup/arkade-before-gf.db
go run ./cmd/dbtool restore -source /backup/arkade-before-gf.db -destination /data/arkade-restored.db
```

Hub 镜像内提供 `/app/arkade-dbtool`，可用 `docker exec <hub-container> /app/arkade-dbtool backup -source /data/arkade.db -destination /data/arkade-before-gf.db` 在已挂载目录创建快照；先按实际配置确认源路径，再将备份另存到容器外的安全位置。旧版镜像没有此工具时，使用单独构建的工具在宿主挂载目录执行；不要为备份而启动第二个 Hub。

备份使用 SQLite `VACUUM INTO` 的一致性快照，包含已提交但尚未 checkpoint 的 WAL 数据；不运行迁移。Lead 已独立在 Windows 和 WSL Linux 上实际运行 WAL 快照、独立恢复、取消清理及拒绝覆盖测试通过（Go 1.24.13、CGO=0）；另外从生产 NewApp 装配验证恢复文件通过迁移校验且原账号可登录。此结果不替代最终版本的全量认证回归。目标必须不存在，工具不会覆盖主库或旧备份。恢复也写入**新路径**：先停止 Hub，校验恢复文件，再将数据库配置指向新文件并启动；不要把备份直接盖到仍有连接和 WAL/SHM 的原库上。`check` 执行 `PRAGMA integrity_check`，并不证明业务数据或迁移脚本版本一定匹配。恢复演练还必须实际启动对应 Hub 版本验证业务契约。

## 本阶段最终独立验证

Lead 已在冻结代码后以 Go 1.24.13、CGO_ENABLED=0 执行：

- Windows Hub `go test ./... -count=1` 全通过，HTTP 包 64.016 秒；随后 `go vet ./...` 通过。
- Linux amd64（WSL）实际执行完整 HTTP 测试二进制通过。两平台均检查输出，未发现 `panic serving` 或 `superfluous response.WriteHeader`；不只依赖 exit code。
- Windows store / dbmigrate / dbbackup 重复回归通过，Linux store 与 dbmigrate、备份工具实际运行通过。
- Agent 与 protocol 全量测试及 vet 通过；前端 typecheck、build 和所有契约测试通过。pnpm 自动依赖校验出现本机 store/无 TTY 警告，但脚本实际执行成功；不将警告描述为无告警安装成功。
- Linux 纯 Go Hub / dbtool 构建通过；驱动依赖检查未发现第二个注册 `sqlite` 的顶层包。

### 测试服发布记录（2026-11-11）

- 阶段提交 `12a2af8`，测试服镜像 `arkade-hub:gf-12a2af8`，地址 `http://192.168.50.243:18080`。
- 升级前以 dbtool VACUUM INTO 生成 `/opt/arkade-demo/backup-gf-12a2af8/hub-before-gf.db`，完整性检查通过；恢复到独立新路径 `hub-restored-rehearsal.db` 并再次 check 通过，没有覆盖活动数据库。
- 仅替换 `arkade-hub-demo`；旧容器停止并保留为 `arkade-hub-demo-before-gf-12a2af8`，Agent 未重建。旧 Hub 镜像保留以供回滚。
- health 返回 `version=gf-12a2af8, agents_online=1`；旧账号 login/me 与 logout 204 空体通过，原用户 ID/名称/密码哈希与备份逐行一致；cursor 为 `(10,0)`，10 条校验和，integrity_check 为 ok。
- 浏览器沿用既有会话进入概览及服务器容器页，能够显示在线 Agent、容器和新 Hub 镜像，无需重新登录。
- 对 lanet-node、portainer_agent、act-runner、两个 adinsight PostgreSQL 业务容器，发布前后容器 ID、RestartCount、StartedAt 全部未变。
- 新 Hub 日志未发现 `panic serving` 或重复 WriteHeader。发布结果记录在测试服备份目录的 `activation-result.txt`。
- GitHub HTTPS 推送先遇到 TLS EOF / connection aborted；网络恢复后的最终回执为 `Invalid username or token / Authentication failed`。需用户恢复 Git 凭据后再推送，未绕过认证。这与测试服本地源码构建发布独立，未宣称远端 CI 或 GHCR 镜像已通过。

跨引擎 Compose 迁移端到端仍未完成；生产 Backend 装配仍需后续阶段实现，不属于本次框架迁移发布验收。

## 改造前已发现但尚未闭环的事项

编排迁移的单元/HTTP 测试通过，不等于真实启动装配完整。目前已发现 Agent 侧临时区未传入并完成修复；Hub `NewApp` 仍未注入 `StackMigrate`，且生产 Backend 适配尚需核查。因此不宣称跨服务器端到端迁移已经通过，也不以测试服健康检查代替该证据。

本次 GoFrame 改造不触碰业务容器，不根据未验证的后台汇报直接发布。
