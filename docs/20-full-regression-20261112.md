# 重构后全量回归（2026-11-12）

本轮从提交 `4e37bda` 重新执行，不复用上一轮通过结果。结论不是“没有任何问题”：发现并修复了一个后台编排轮询退出生命周期缺陷；其余已执行测试没有发现新增业务失败。

## 已发现并修复

生产 `controller/stacks` watcher 原来使用独立 Background context，HTTP server 停止后仍会轮询 Agent，最长持续 12 分钟；没有退出等待，可能在依赖关闭后继续访问 Agent/数据库。

先用可控轮询入口复现“关闭 HTTP 后仍派发 RPC”，测试在旧生命周期失败；随后为同一 watcher 实现实例级取消、等待、拒绝新任务及超时重试。NewApp 注册路由所属 controller 的关闭 hook：HTTP 停止后，先等待 watcher，再关闭迁移 manager、Agent 连接和数据库。使用包级 Close 函数，避免 GoFrame 将 controller 导出方法误识别为路由。

新增覆盖：活动轮询取消并等待、等待超时保留依赖及重试、并发 start/close、关闭后拒新任务，Linux race 重复三次通过；Windows watcher 回归连续五十次通过。测试以 join 完成作为禁止后续调用的边界，不将合法的关闭前已派发调用误判为退出后调用。

## 本轮覆盖

### 后端

- Windows Go 1.24.13，Hub 全包测试及 vet；最终 819 个测试/子测试通过、0 失败，33 个有测试包通过。
- Windows Agent 全包测试及 vet；532 个测试/子测试通过、0 失败，11 个有测试包通过。
- protocol 编译测试及 vet 通过；该模块没有独立 Test 函数，不能计为协议行为全覆盖。
- 使用 `-coverpkg=./... -covermode=atomic` 汇总跨包执行语句：Hub 77.6%，Agent 60.6%。包含命令入口、代码生成工具、生成文件等；是语句覆盖率，不是分支覆盖率或业务完成率，更不是 100%。普通单包 cover 会漏计 HTTP 测试对 controller/agentlink 的调用，因此不与跨包结果混用。
- Linux 对 Hub、Agent 全包重新执行：Hub 819、Agent 602 个测试/子测试通过，均零失败（平台专用测试及子场景不同，不与 Windows 数量混算）；另对 stackmigrate、stackmigration、agentlink、Agent compose/migrate、compose、handler、imagetransfer 执行真实 race。新增 stacks controller 和 runtime watcher 关闭回归各以 race 连续运行三次。

### 本轮补上的盲点

- 镜像任务列表及取消：真实 HTTP typed 路由、任务所有者、同权限不同用户、源/目标撤权、taskID query 别名遮蔽、空数组过滤、真实 fakeAgent WebSocket RPC 取消。
- 外部编排 apply：在线成功、path 与 body/query 名称对抗、错误 hash 无 DB/adopt 副作用、成功 sync 状态与记录。
- 在线资源映射：容器详情、日志、启停、重启、删除；编排 ps/logs/up/down/stop/restart/pull/rebuild/backup/remove。验证 RPC payload/响应/标志及必要 DB 副作用，不仅测试离线 503。

### 前端

- 全部 14 份测试脚本新跑通过，共 1158 次断言/自计数检查，不包装成 1158 个独立业务场景。
- typecheck、生产 build 重新通过。使用项目现有依赖，不改配置或 lockfile；初次 pnpm 自动依赖核验有跨盘 store/无 TTY 警告，随后以仅命令级关闭自动核验重新 typecheck/build，无该警告。
- 测试混合真实 TypeScript API/模型/composable 执行、SFC 编译与源码契约；有网络/计时桩，不等同所有组件挂载与浏览器操作通过。

### 真实隔离链路

本轮创建全新临时 Hub、两个 Agent、两个独立 Docker/containerd 引擎及 network/mount namespace。基线和修复后二进制分别用全新项目执行真实 HTTP → Hub → WebSocket Agent → Docker 链路：

- 默认配置/镜像迁移成功，目标不启动、不迁卷。
- 显式迁卷并启动成功，目标 sentinel 一致，源容器 ID/StartedAt/RestartCount 及源数据不变。
- 托管同名项目、已有目标卷、外部同名项目拒绝，原目标 marker 和生命周期不变，残留记录符合实际。
- 在线真实 containers、stacks ps/logs、image tasks list 读取成功。
- 临时应用、测试容器/卷及私有 daemon 已清理，日志证据保留。清理输出 ORIGINAL_HOST_CONTAINERS_UNCHANGED 和 ISOLATED_CLEANUP_COMPLETE_HOST_UNCHANGED；未替换既有 Hub/Agent，未操作业务容器。

## 仍未覆盖或不能作出的承诺

- 浏览器曾实际打开隔离实例登录页，但登录交互被浏览器插件的受限 extension frame/页面响应错误阻断，已关闭会话；没有拿 API 成功冒充浏览器登录成功。浏览器内完整菜单、弹窗、上传下载、响应式布局与可访问性仍未完成实测。
- 不是 100% 语句/分支覆盖，未遍历所有网络中断时序、任意大归档/磁盘耗尽/断电组合。
- Docker 双引擎通过不等于 Podman 真机或跨 CPU 平台通过；数据库运行中卷备份不保证应用一致性。
- 本轮是源码与隔离环境验收，不代表现有部署已升级，也不证明远端仓库或 latest 镜像包含本轮改动。

## 证据与复跑

本机完整 JSON 测试事件、cover profile、逐函数覆盖、race 日志及前端逐文件日志保存在 `C:\temp\arkade-regression-20261112`。远端隔离证据保存在 `/tmp/arkade-migration-e2e-20261112-regression`，包含权限为私有的测试配置；不要公开整个目录或凭据。

Windows 使用直接 Go 可执行文件而非 `go.cmd`，避免 shell 包装丢失带点号参数导致 JSON/coverage 产物无效；最终覆盖率必须由非空 profile 和 JSON 事件核实。复跑测试：

```powershell
$env:GOTOOLCHAIN='go1.24.13'
$env:CGO_ENABLED='0'
# 分别在 hub / agent 运行；替换输出目录与真实 go.exe 路径。
& 'D:\gopath\go1.26.3\bin\go.exe' test ./... -count=1 '-coverpkg=./...' '-coverprofile=coverage.out' -covermode=atomic -json -timeout=8m
& 'D:\gopath\go1.26.3\bin\go.exe' vet ./...
```
