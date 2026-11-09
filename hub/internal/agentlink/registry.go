package agentlink

// registry.go 是在线 Agent 表。
//
// 关键约束：在线表以内存为准。数据库里的 status 只是最后一次上报的快照，
// 进程重启后所有 Agent 都视为离线，直到重新握手——这样才不会出现
// "重启后 Hub 认为 Agent 都在线，实际发请求全超时"的假在线。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"arkade/protocol/envelope"
)

// Registry 保存在线连接。
type Registry struct {
	mu    sync.RWMutex
	conns map[string]*connection
}

// NewRegistry 构造空的在线表。
func NewRegistry() *Registry {
	return &Registry{conns: make(map[string]*connection)}
}

// Add 把一个新连接登记为在线。
//
// 同一 agent_id 重复上线时（旧连接还没被检测掉线），新连接直接顶替旧连接：
// 让最新的连接服务请求，旧连接强制关闭，避免同一个 Agent 有两条活跃通道导致消息错配。
func (r *Registry) Add(c *connection) {
	r.mu.Lock()
	old := r.conns[c.agentID]
	r.conns[c.agentID] = c
	r.mu.Unlock()

	if old != nil && old != c {
		old.log.Info("同一 Agent 重新上线，关闭旧连接")
		old.close()
	}
}

// Remove 移除连接。只有登记的仍是同一条连接时才移除，
// 防止旧连接关闭时误删刚上线的新连接。
func (r *Registry) Remove(c *connection) {
	r.mu.Lock()
	if cur, ok := r.conns[c.agentID]; ok && cur == c {
		delete(r.conns, c.agentID)
	}
	r.mu.Unlock()
}

// Get 取在线连接。
func (r *Registry) Get(agentID string) (*connection, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.conns[agentID]
	return c, ok
}

// OnlineIDs 返回当前在线 Agent 的 ID 集合，供列表接口标注实时状态。
func (r *Registry) OnlineIDs() map[string]struct{} {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]struct{}, len(r.conns))
	for id := range r.conns {
		out[id] = struct{}{}
	}
	return out
}

// Count 返回在线数量。
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.conns)
}

// Call 向指定 Agent 下发请求并等待响应。
//
// 这是所有容器操作的唯一入口。离线时立刻返回 agent_offline，不排队、不重试：
// 危险操作排队在重连后执行是极其危险的行为（02-features.md 第 1 节明确禁止）。
func (r *Registry) Call(ctx context.Context, agentID, method string, payload any) ([]byte, *ConnError) {
	c, ok := r.Get(agentID)
	if !ok {
		return nil, newOfflineError(agentID)
	}

	id := newRequestID()
	ch := c.registerPending(id)

	env, err := envelope.NewRequest(id, method, "agent:"+agentID, payload)
	if err != nil {
		c.removePending(id)
		return nil, &ConnError{Code: envelope.CodeBadRequest, Message: "构造请求失败", Wrapped: err}
	}
	if err := c.send(env); err != nil {
		c.removePending(id)
		return nil, &ConnError{Code: envelope.CodeAgentOffline, Message: "消息发送失败，Agent 可能已断开", Wrapped: err}
	}

	timeoutCtx, cancel := timeoutContext(ctx, DefaultRequestTimeout)
	defer cancel()

	return c.waitResponse(timeoutCtx, id, ch, method)
}

// newRequestID 生成请求 ID，用于与响应配对。
func newRequestID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// 随机数失败属于系统级异常，这里退化为时间戳方案保证可用，
		// 因为请求 ID 只需在单进程内唯一且不可预测性要求不高。
		return fmt.Sprintf("req-%d", nowUnixNano())
	}
	return hex.EncodeToString(buf)
}

// ErrNoConnection 表示找不到连接，供上层判断。
var ErrNoConnection = errors.New("连接不存在")
