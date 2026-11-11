# Hub 分阶段迁移到 GoFrame

## 已确认的技术路线

- HTTP：GoFrame v2，保持现有 API、错误状态、会话和 Agent WebSocket 契约。
- 数据访问：GoFrame 官方 SQLite 驱动、gdb 以及由 `gf gen dao` 生成的 DAO / DO / Entity。生成文件不得手写。
- 表结构版本：golang-migrate；不将 DAO 生成器误当作建表工具。
- 数据库继续使用 SQLite，原地升级，不重建业务数据。
- 锁定 GoFrame 与官方 SQLite 驱动 v2.9.6、golang-migrate v4.19.1；兼容项目 Go 1.24。

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

以上是本地阶段验证，不等于镜像 CI、测试服务器发布或跨引擎 Compose 迁移端到端已经完成。后续提交、镜像构建与备份后测试服部署应单独记录结果。

## 改造前已发现但尚未闭环的事项

编排迁移的单元/HTTP 测试通过，不等于真实启动装配完整。目前已发现 Agent 侧临时区未传入并完成修复；Hub `NewApp` 仍未注入 `StackMigrate`，且生产 Backend 适配尚需核查。因此不宣称跨服务器端到端迁移已经通过，也不以测试服健康检查代替该证据。

本次 GoFrame 改造不触碰业务容器，不根据未验证的后台汇报直接发布。
