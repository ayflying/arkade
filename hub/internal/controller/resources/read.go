package resources

import (
	"context"
	"strings"

	api "arkade/hub/api/resources/v1"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
	"github.com/gogf/gf/v2/net/ghttp"
)

func (c *Controller) ListContainers(ctx context.Context, req *api.ListContainersReq) (*api.ListContainersRes, error) {
	if err := c.access.Require(ctx, req.AgentID, user.PermView); err != nil {
		return nil, err
	}
	out, err := c.deps.Ops.ListContainers(ctx, req.AgentID)
	if err != nil {
		return nil, controller.ConnectionError(err)
	}
	return &api.ListContainersRes{AgentID: req.AgentID, Containers: out.Containers}, nil
}
func (c *Controller) ContainerDetail(ctx context.Context, req *api.ContainerDetailReq) (*api.ContainerDetailRes, error) {
	if err := c.access.Require(ctx, req.AgentID, user.PermManage); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.ContainerID) == "" || len(req.ContainerID) > 256 {
		return nil, controller.Failure("bad_request", "容器标识无效")
	}
	out, err := c.deps.Ops.InspectContainer(ctx, req.AgentID, req.ContainerID)
	if err != nil {
		return nil, controller.ConnectionError(err)
	}
	if r := ghttp.RequestFromCtx(ctx); r != nil {
		r.Response.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		r.Response.Header().Set("Pragma", "no-cache")
		r.Response.Header().Set("Expires", "0")
	}
	return &api.ContainerDetailRes{Container: out}, nil
}
func (c *Controller) ListImages(ctx context.Context, req *api.ListImagesReq) (*api.ListImagesRes, error) {
	if err := c.access.Require(ctx, req.AgentID, user.PermView); err != nil {
		return nil, err
	}
	out, err := c.deps.Ops.ListImages(ctx, req.AgentID)
	if err != nil {
		return nil, controller.ConnectionError(err)
	}
	if out.Images == nil {
		out.Images = []envelope.ImageSummary{}
	}
	return &api.ListImagesRes{AgentID: req.AgentID, Images: out.Images}, nil
}
func (c *Controller) ListNetworks(ctx context.Context, req *api.ListNetworksReq) (*api.ListNetworksRes, error) {
	if err := c.access.Require(ctx, req.AgentID, user.PermView); err != nil {
		return nil, err
	}
	out, err := c.deps.Ops.ListNetworks(ctx, req.AgentID)
	if err != nil {
		return nil, controller.ConnectionError(err)
	}
	if out.Networks == nil {
		out.Networks = []envelope.NetworkSummary{}
	}
	return &api.ListNetworksRes{AgentID: req.AgentID, Networks: out.Networks}, nil
}
func (c *Controller) ListVolumes(ctx context.Context, req *api.ListVolumesReq) (*api.ListVolumesRes, error) {
	if err := c.access.Require(ctx, req.AgentID, user.PermView); err != nil {
		return nil, err
	}
	out, err := c.deps.Ops.ListVolumes(ctx, req.AgentID)
	if err != nil {
		return nil, controller.ConnectionError(err)
	}
	if out.Volumes == nil {
		out.Volumes = []envelope.VolumeSummary{}
	}
	return &api.ListVolumesRes{AgentID: req.AgentID, Volumes: out.Volumes}, nil
}
