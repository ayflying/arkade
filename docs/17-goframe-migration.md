# Hub GoFrame 架构

当前 Hub 使用 GoFrame v2 HTTP、官方 PostgreSQL 驱动与 gdb。本文说明框架分层，不是历史数据库接管或部署兼容指南。实际安装、版本化建表和备份见 [PostgreSQL 部署与备份](<18-manual-goframe-deployment.md>)。

## HTTP 分层

- API 请求/响应类型通过 typed controller 与 `Bind` 注册。
- API/controller/service 按功能域与读取、写入、任务、上传、下载、成员、授权拆分；公共权限决策集中管理。
- 框架仅绑定允许的路径/query；写接口按业务边界严格解码 JSON，不接受 query/form 替代正文。
- 认证、审计、权限与正文校验的顺序应有真实 HTTP 回归，不能只检查函数返回值。
- WebSocket、流式上传下载、取消、空响应、状态码和 SPA fallback 分别验证，不用普通 JSON 包装破坏协议。

## 数据访问与版本化建表

- 使用 GoFrame 官方 PostgreSQL 驱动；DAO/DO/Entity 由 GF 生成器从真实迁移后的数据库生成，不手写生成文件。
- golang-migrate 管理表结构版本，DAO 生成不替代建表迁移。
- 迁移连接与业务池分别管理，关闭迁移器不得误关业务连接。
- 启动检查版本、dirty 状态、脚本校验和与结构漂移；不得自动 force 跳过失败。
- 跨表事务保留锁、条件更新及影响行数判断；拒绝因零行更新而报告成功。
- 测试使用隔离 PostgreSQL 数据库，必须显式配置 `ARKADE_TEST_PG_DSN`，不可将测试指向业务账号。

## 验证边界

框架单测和 HTTP 回归不等于镜像发布、服务器部署或跨服务器编排迁移验收。构建、发布、实际连接与业务验收应分别报告。跨服务器 Compose 迁移的功能验收见 [编排迁移验收](<19-compose-migration-completion.md>)。
