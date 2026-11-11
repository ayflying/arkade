package resources

import (
	"context"
	"strings"

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
	out, err := c.deps.Ops.RemoveContainer(ctx, req.AgentID, req.ContainerID, force, volumes)
	if err != nil {
		return nil, controller.ConnectionError(err)
	}
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
