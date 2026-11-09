// Package agentlink 管理 Hub 侧的 Agent 连接与消息路由。
//
// 这是整个系统的咽喉：所有对服务器的操作都必须经过这里才能到达 Agent。
//
// 设计要点：
//   - 在线表只认活跃连接：连接断开立刻移出在线表，状态以内存为准，
//     数据库里的 status 只是最后一次快照，不作为"是否能操作"的依据；
//   - 请求/响应按 ID 关联，每个请求登记一个等待通道，超时或断连都会立即唤醒调用方，
//     不允许出现"发出去就永远等不到回应"的悬空请求；
//   - 危险操作不进队列：Agent 离线时直接返回 AgentOffline，不排队、不延后执行
//     （见 02-features.md 第 1 节）。
package agentlink

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"arkade/protocol/envelope"
)

// 连接相关的超时与限额常量。集中在此，避免各处硬编码不一致。
const (
	// DefaultRequestTimeout 是 Hub 等待 Agent 响应的默认上限。
	// 设得比 Agent 侧常见操作耗时略长，避免正常操作被误判超时。
	//
	// 注意：这个值只适用于「快操作」（列容器、启停、查日志）。
	// 编排类操作会拉镜像、重建容器，动辄几分钟，必须用下面的
	// methodRequestTimeout 单独放宽，否则会出现最尴尬的结果——
	// Hub 已经超时报错，Agent 那边却还在正常执行并最终成功，
	// 用户看到"失败"但服务其实起来了。
	DefaultRequestTimeout = 15 * time.Second
	// writeQueueSize 是单连接出站队列长度。队列满说明 Agent 消费不过来，
	// 此时应主动断开而不是无限堆积内存。
	writeQueueSize = 256
	// heartbeatTimeout 是判定离线的阈值：超过该时长未收到 Agent 任何消息即视为离线。
	// 慢操作期间 Agent 仍在正常发心跳（compose 命令是子进程，不阻塞消息循环），
	// 因此放宽请求超时不会影响离线判定。
	heartbeatTimeout = 60 * time.Second
)

// slowMethodTimeouts 为耗时明显长于普通操作的方法单独放宽超时。
//
// 为什么必须区分：docker compose pull / up / rebuild 会联网拉镜像、下载层、
// 重建容器，首次部署一个多层镜像很容易超过一分钟。统一用 15 秒会稳定误报超时。
// 反过来把默认值整体调大同样有害——真正的卡死会拖上十分钟才报错。
//
// 数值取 Agent 侧命令超时（pull/up 给 10 分钟）再留一点余量，
// 保证 Agent 先把明确的错误（比如 YAML 写错）报回来，而不是 Hub 先斩后奏。
var slowMethodTimeouts = map[string]time.Duration{
	envelope.MethodComposeUp:      11 * time.Minute,
	envelope.MethodComposePull:    11 * time.Minute,
	envelope.MethodComposeRebuild: 12 * time.Minute,
	envelope.MethodComposeDown:    2 * time.Minute,
	envelope.MethodComposeRemove:  2 * time.Minute,
	envelope.MethodComposeBackup:  5 * time.Minute,
}

// requestTimeoutFor 返回某个方法的等待上限。
func requestTimeoutFor(method string) time.Duration {
	if d, ok := slowMethodTimeouts[method]; ok {
		return d
	}
	return DefaultRequestTimeout
}

// ErrAgentOffline 表示目标 Agent 当前不在线。
var ErrAgentOffline = errors.New("目标 Agent 不在线")

// ErrClosed 表示连接已关闭。
var ErrClosed = errors.New("连接已关闭")

// ConnError 携带协议错误码，方便 HTTP 层直接映射成响应。
type ConnError struct {
	Code    string
	Message string
	Wrapped error
}

func (e *ConnError) Error() string {
	if e.Wrapped != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Wrapped)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *ConnError) Unwrap() error { return e.Wrapped }

// newOfflineError 构造标准的离线错误。
func newOfflineError(agentID string) *ConnError {
	return &ConnError{
		Code:    "agent_offline",
		Message: "目标 Agent 不在线，无法下发操作",
		Wrapped: fmt.Errorf("%w: %s", ErrAgentOffline, agentID),
	}
}

// newTimeoutError 构造超时错误。
func newTimeoutError(agentID, method string) *ConnError {
	return &ConnError{
		Code:    "timeout",
		Message: "等待 Agent 响应超时",
		Wrapped: fmt.Errorf("等待 %s 的 %s 响应超时", agentID, method),
	}
}

// pending 是一个在途请求的等待槽位。
type pending struct {
	ch chan result
}

// result 是请求的最终结果。
type result struct {
	payload []byte
	err     *ConnError
}

// lock 是便捷的互斥辅助，减少 defer unlock 的样板代码。
type lock struct{ mu sync.Mutex }

func (l *lock) do(fn func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fn()
}
