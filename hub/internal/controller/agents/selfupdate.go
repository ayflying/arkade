package agents

// selfupdate.go：更新某台服务器上的 Arkade 组件镜像。
//
// ============================ 这个接口只受理，不等待 ============================
//
// 更新会把目标容器重建掉，因此同步等不到结果——尤其当目标是「本机」时，
// 重建的正是 Hub 自己，当前这个 HTTP 请求的应答根本发不出去。
//
// 所以这里只做三件事：校验权限、确认在线、把请求派发下去。
// 成败由前端"它是否带着新版本重新上线"来判断，而不是由这个接口返回。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	api "arkade/hub/api/agents/v1"
	"arkade/hub/internal/controller"
	"arkade/protocol/envelope"
)

// SelfUpdate 让某台服务器更新自己所在容器的镜像。
//
// 只允许管理员：这个操作会重启目标机器上的 Arkade 组件，
// 普通查看者不应能触发。
func (c *Controller) SelfUpdate(ctx context.Context, req *api.SelfUpdateReq) (*api.SelfUpdateRes, error) {
	if err := c.requireAdmin(ctx); err != nil {
		return nil, err
	}
	rec, err := c.d.DB.GetAgent(ctx, req.AgentID)
	if err != nil {
		return nil, c.storeError(err, "服务器不存在")
	}
	if rec.Disabled {
		return nil, badRequest("该服务器已被禁用，请先启用后再更新")
	}
	// 离线服务器直接拒绝：请求会落到 Registry.Call 的 agent_offline。
	// 在这里先判一次是为了给出更准确的话术（"恢复在线后再试"），
	// 而不是让用户看到底层错误码。
	if !c.d.Registry.IsOnline(req.AgentID) {
		return nil, &controller.Error{
			Status:  http.StatusConflict,
			Code:    "agent_offline",
			Message: "服务器当前离线，无法执行更新。恢复在线后可以再试",
		}
	}

	requestID := newSelfUpdateRequestID()
	out, cerr := c.d.Registry.SelfUpdate(ctx, req.AgentID, envelope.SelfUpdatePayload{
		RequestID: requestID,
	})
	if cerr != nil {
		return nil, controller.ConnectionError(cerr)
	}

	res := &api.SelfUpdateRes{
		Accepted:            out.Accepted,
		Blockers:            out.Blockers,
		RequestID:           requestID,
		CurrentImage:        out.CurrentImage,
		HelperContainerName: out.HelperContainerName,
		Message:             out.Message,
	}
	if len(out.Blockers) > 0 {
		// 有阻塞原因时不要给出"已提交"的假象。
		res.Accepted = false
		res.Message = "当前部署方式不支持自更新"
	}
	if v, verr := c.viewByID(ctx, req.AgentID); verr == nil {
		res.Agent = v
	}
	return res, nil
}

// newSelfUpdateRequestID 生成一次更新的幂等键。
//
// 用随机值而不是自增/时间戳：它同时会出现在目标机器上的一次性容器名里，
// 用时间戳会在同一秒内连点时撞名，导致"第二次更新创建容器失败"。
func newSelfUpdateRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 熵源不可用时退化为纳秒时间戳：这里不需要密码学强度，
		// 只需要"两次点击不撞名"。
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
