package resources

import (
	"context"
	"strings"
	"time"

	api "arkade/hub/api/resources/v1"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
)

func (c *Controller) ContainerStart(ctx context.Context, req *api.ContainerStartReq) (*api.ContainerStartRes, error) {
	if err := c.access.Require(ctx, req.AgentID, user.PermOperate); err != nil {
		return nil, err
	}
	out, err := c.deps.Ops.StartContainer(ctx, req.AgentID, req.ContainerID)
	if err != nil {
		return nil, controller.ConnectionError(err)
	}
	return &api.ContainerOpRes{ContainerID: out.ContainerID, State: out.State}, nil
}
func (c *Controller) ContainerStop(ctx context.Context, req *api.ContainerStopReq) (*api.ContainerStopRes, error) {
	if err := c.access.Require(ctx, req.AgentID, user.PermOperate); err != nil {
		return nil, err
	}
	if c.deps.SuppressMonitor != nil {
		c.deps.SuppressMonitor(req.AgentID, req.ContainerID, 5*time.Minute)
	}
	out, err := c.deps.Ops.StopContainer(ctx, req.AgentID, req.ContainerID)
	if err != nil {
		return nil, controller.ConnectionError(err)
	}
	return &api.ContainerOpRes{ContainerID: out.ContainerID, State: out.State}, nil
}
func (c *Controller) ContainerRestart(ctx context.Context, req *api.ContainerRestartReq) (*api.ContainerRestartRes, error) {
	if err := c.access.Require(ctx, req.AgentID, user.PermOperate); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.ContainerID) == "" {
		return nil, controller.Failure("bad_request", "容器 ID 不能为空")
	}
	if c.deps.SuppressMonitor != nil {
		c.deps.SuppressMonitor(req.AgentID, req.ContainerID, 5*time.Minute)
	}
	out, err := c.deps.Ops.RestartContainer(ctx, req.AgentID, req.ContainerID)
	if err != nil {
		return nil, controller.ConnectionError(err)
	}
	return &api.ContainerOpRes{ContainerID: out.ContainerID, State: out.State}, nil
}
func (c *Controller) ContainerRemove(ctx context.Context, req *api.ContainerRemoveReq) (*api.ContainerRemoveRes, error) {
	if err := c.access.Require(ctx, req.AgentID, user.PermManage); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.ContainerID) == "" {
		return nil, controller.Failure("bad_request", "容器 ID 不能为空")
	}
	force, ok := parseBoolQuery(req.Force)
	if !ok {
		return nil, controller.Failure("bad_request", "force 只能是 true/false/1/0")
	}
	volumes, ok := parseBoolQuery(req.RemoveVolumes)
	if !ok {
		return nil, controller.Failure("bad_request", "remove_volumes 只能是 true/false/1/0")
	}
	if c.deps.SuppressMonitor != nil {
		c.deps.SuppressMonitor(req.AgentID, req.ContainerID, 5*time.Minute)
	}
	out, err := c.deps.Ops.RemoveContainer(ctx, req.AgentID, req.ContainerID, force, volumes)
	if err != nil {
		return nil, controller.ConnectionError(err)
	}
	// 容器没了，它的资源历史就是没有任何入口能看到的残留。
	// 请求里的引用可能是完整 ID、短 ID 或容器名，清理时三种都匹配。
	if c.deps.DB != nil {
		if e := c.deps.DB.DeleteMonitorSamplesForContainer(ctx, req.AgentID, req.ContainerID); e != nil && c.deps.Log != nil {
			c.deps.Log.Error("删除容器后清理资源历史失败，将在保留期到期后自动清理", "agent", req.AgentID, "container", req.ContainerID, "err", e)
		}
	}
	return &api.ContainerOpRes{ContainerID: out.ContainerID, State: out.State}, nil
}

// ContainerRecreate 按原配置重建容器。
//
// 为什么要求 manage 而不是 operate：重建会**删除并新建**容器对象，
// 属于"改变资源本身"的动作。给 operate 就等于让人用低权限
// 达到了删除+创建的效果，权限模型会被绕开。
func (c *Controller) ContainerRecreate(ctx context.Context, req *api.ContainerRecreateReq) (*api.ContainerRecreateRes, error) {
	if err := c.access.Require(ctx, req.AgentID, user.PermManage); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.ContainerID) == "" {
		return nil, controller.Failure("bad_request", "容器 ID 不能为空")
	}
	pull, ok := parseBoolQuery(req.Pull)
	if !ok {
		return nil, controller.Failure("bad_request", "pull 只能是 true/false/1/0")
	}
	// 重建期间容器会短暂消失再出现，监控采样必然取不到数据。
	// 抑制窗口比启停更宽：重建包含"停止 + 拉镜像 + 创建 + 启动"，
	// 勾选拉镜像时可能花几分钟，采样不到就刷出一串告警没有意义。
	if c.deps.SuppressMonitor != nil {
		window := 5 * time.Minute
		if pull {
			window = 15 * time.Minute
		}
		c.deps.SuppressMonitor(req.AgentID, req.ContainerID, window)
	}
	out, err := c.deps.Ops.RecreateContainer(ctx, req.AgentID, req.ContainerID, pull)
	if err != nil {
		return nil, controller.ConnectionError(err)
	}
	c.deps.Log.Info("重建容器", "agent", req.AgentID, "container", req.ContainerID,
		"pull", pull, "new_container", out.ContainerID)
	return &api.ContainerOpRes{ContainerID: out.ContainerID, State: out.State}, nil
}

func parseBoolQuery(raw string) (bool, bool) {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "", "0", "false", "f", "no", "n":
		return false, true
	case "1", "true", "t", "yes", "y":
		return true, true
	}
	return false, false
}
func (c *Controller) RemoveImage(ctx context.Context, req *api.RemoveImageReq) (*api.RemoveImageRes, error) {
	if err := c.access.Require(ctx, req.AgentID, user.PermManage); err != nil {
		return nil, err
	}
	if req.ImageID == "" {
		return nil, controller.Failure("bad_request", "镜像 ID 不能为空")
	}
	force := req.Force == "1" || req.Force == "true"
	if err := c.deps.Ops.RemoveImage(ctx, req.AgentID, envelope.ImageRemovePayload{ImageID: req.ImageID, Force: force}); err != nil {
		return nil, controller.ConnectionError(err)
	}
	c.deps.Log.Info("删除镜像", "agent", req.AgentID, "image", req.ImageID, "force", force)
	return nil, controller.ErrNoContent
}
func (c *Controller) RemoveNetwork(ctx context.Context, req *api.RemoveNetworkReq) (*api.RemoveNetworkRes, error) {
	if err := c.access.Require(ctx, req.AgentID, user.PermManage); err != nil {
		return nil, err
	}
	if req.NetworkID == "" {
		return nil, controller.Failure("bad_request", "网络 ID 不能为空")
	}
	if err := c.deps.Ops.RemoveNetwork(ctx, req.AgentID, envelope.NetworkRemovePayload{NetworkID: req.NetworkID}); err != nil {
		return nil, controller.ConnectionError(err)
	}
	c.deps.Log.Info("删除网络", "agent", req.AgentID, "network", req.NetworkID)
	return nil, controller.ErrNoContent
}
func (c *Controller) RemoveVolume(ctx context.Context, req *api.RemoveVolumeReq) (*api.RemoveVolumeRes, error) {
	if err := c.access.Require(ctx, req.AgentID, user.PermManage); err != nil {
		return nil, err
	}
	if req.Name == "" {
		return nil, controller.Failure("bad_request", "存储卷名称不能为空")
	}
	if err := c.deps.Ops.RemoveVolume(ctx, req.AgentID, envelope.VolumeRemovePayload{Name: req.Name}); err != nil {
		return nil, controller.ConnectionError(err)
	}
	c.deps.Log.Info("删除存储卷", "agent", req.AgentID, "volume", req.Name)
	return nil, controller.ErrNoContent
}
