package agentlink

import (
	"context"

	"arkade/protocol/envelope"
)

// SelfUpdate 让某台服务器更新自己所在容器的镜像。
//
// ============================ 为什么挂在 Registry 上 ============================
//
// 其余 ops_*.go 都是"对某个资源做操作"（容器、编排、卷、镜像），
// 而自更新的对象是**这台服务器本身**，属于 Agent 生命周期的一部分，
// 与 IsOnline / Close / OnlineIDs 同类。服务器控制器手里只有 Registry，
// 因此放在这里可以直接用，不必为了一个方法再扩一次依赖注入。
//
// 返回的是**受理结果**：真正重建由目标机器上的一次性容器异步完成。
func (r *Registry) SelfUpdate(ctx context.Context, agentID string, p envelope.SelfUpdatePayload) (envelope.SelfUpdateResult, *ConnError) {
	var out envelope.SelfUpdateResult
	raw, cerr := r.Call(ctx, agentID, envelope.MethodSelfUpdate, p)
	if cerr != nil {
		return out, cerr
	}
	if err := decodeInto(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}
