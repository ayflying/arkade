// Package link 是 Agent → Hub 的反向长连接客户端。
//
// 职责边界（这是它能独立成包的原因）：
//   - 建连、握手（agent.hello / HelloAck）、心跳、断线退避重连、信封收发；
//   - 它**不知道任何业务方法**：收到请求后只交给注入的 envelope 分发函数，
//     分发函数指向 handler.Registry，与本地 CLI 用的是同一张表（决策 D-05）。
//
// 这样做的好处是 link 可以被单测用 httptest 的 WebSocket 服务端完整覆盖，
// 不需要拉起真实的 Hub；反过来 handler 也可以脱离网络单测。
package link

import "time"

// 退避参数。按 01 架构 3.3：1s → 2s → 4s … 上限 60s。
//
// 为什么必须指数退避而不是固定间隔：Hub 重启或网络大范围中断时，成百上千个
// Agent 会同时掉线，固定间隔重连会在 Hub 刚恢复的瞬间形成惊群，把 Hub 打死。
const (
	// MinBackoff 是首次重连的等待时间。
	MinBackoff = time.Second
	// MaxBackoff 是退避上限，超过后保持该间隔持续重试。
	MaxBackoff = 60 * time.Second
	// backoffFactor 是每次失败后的倍数。取 2 即文档要求的 1s→2s→4s。
	backoffFactor = 2
)

// nextBackoff 计算下一次重连等待时间。
//
// 纯函数（无随机抖动）是刻意的：文档明确写了 1s→2s→4s…60s 的序列，
// 需要可预测、可测试。抖动在单机 Agent 数量不大时收益有限，而引入随机化
// 会让运维在日志里对不上节奏。若后续 Hub 侧压力大，再在此处统一加抖动。
func nextBackoff(current time.Duration) time.Duration {
	if current <= 0 {
		return MinBackoff
	}
	next := current * backoffFactor
	if next > MaxBackoff {
		return MaxBackoff
	}
	return next
}
