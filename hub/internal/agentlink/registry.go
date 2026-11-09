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

// ErrCredentialRevoked 表示连接携带的凭据在握手过程中被撤销。
var ErrCredentialRevoked = errors.New("该服务器的接入凭据已在握手过程中被撤销，请使用新密钥重新连接")

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

// IsOnline 判断某个 Agent 当前是否在线。
func (r *Registry) IsOnline(agentID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.conns[agentID]
	return ok
}

// Status 返回展示用的实时状态。
//
// 为什么需要这个封装：数据库里的 status 只是"最后一次心跳时的快照"，
// 进程重启后那条记录还写着 online，实际早就断了。
// 判断在线必须以连接表为准，数据库值只在离线时用于展示原因。
// 各处分别写这段逻辑很容易漏掉"以 Registry 为准"这条规则，
// 收敛到这里可以保证所有接口的判定一致。
func (r *Registry) Status(agentID, dbStatus string) string {
	if r.IsOnline(agentID) {
		return "online"
	}
	// 不在线时，数据库里的 online 快照是过期信息，一律显示 offline。
	if dbStatus == "online" {
		return "offline"
	}
	return dbStatus
}

// Close 主动断开指定 Agent 的连接，reason 会作为关闭原因发给对方。
//
// 用于管理动作（禁用、删除记录）：握手只在建立连接时发生，
// 不主动踢掉存量连接的话，禁用会"看起来生效了但通道还在"。
//
// 找不到连接时什么都不做——这是正常情况（Agent 本来就没连）。
func (r *Registry) Close(agentID, reason string) {
	r.mu.RLock()
	c := r.conns[agentID]
	r.mu.RUnlock()

	if c == nil {
		return
	}
	c.log.Info("管理员操作导致连接关闭", "reason", reason)
	// 先把原因发给对端，再关连接。
	// 顺序很重要：先关的话对方只能看到"连接断了"，拿不到原因。
	c.sendClose(reason)
	c.close()
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

	timeoutCtx, cancel := timeoutContext(ctx, requestTimeoutFor(method))
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
