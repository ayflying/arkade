// Package compose 在 Agent 侧执行 docker compose 编排。
//
// 设计边界（重要，改代码前必读）：
//   - **必须走 docker compose 命令行**，不直接调 Docker API。这是用户拍板的决策：
//     compose 的语义（依赖顺序、网络创建、profile、健康检查等待）大量存在于
//     CLI 里，用 API 复刻必然漏细节，且会随 compose 版本演进而失效。
//   - 本包只管「把文件落到本机 + 执行命令 + 汇报状态」，**不负责和 Hub 同步**。
//     编排内容由 Hub 随请求下发，Agent 只上行状态与哈希——同步是单向的 Hub→Agent，
//     不存在双向冲突合并，这是整个设计能简单的前提。
//   - 项目名会拼进文件路径与命令行参数，因此校验必须严格（见 validate.go）。
//
// 文件职责划分：
//   - validate.go  项目名与服名白名单（安全边界）
//   - hash.go     内容哈希（与 Hub 算法必须一致）
//   - runner.go   命令执行封装（超时、输出截断、DOCKER_HOST 传递）
//   - workspace.go 编排文件落盘/读取/列表/删除
//   - status.go   项目状态探测与服务名解析
//   - backup.go   备份编排文件与数据卷
package compose

// hashHexLen 是内容哈希取 sha256 十六进制的前 16 位。
//
// 这个值与 Hub 侧 composeConfigHash 必须逐位相同：两边哈希是 drift 判定的唯一依据，
// 长度或算法差一点，界面上就会永远显示"配置已偏离"，把真正的偏离淹没在噪声里。
const hashHexLen = 16
