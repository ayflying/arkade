// Package external implements explicit, configuration-only external Compose flows.
package external

import (
	"context"
	"errors"
	"log/slog"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/controller"
	compose "arkade/hub/internal/service/compose"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
	"github.com/gogf/gf/v2/net/ghttp"
)

// Operations deliberately excludes lifecycle methods: inspection/import/copy
// must never start, stop, rebuild or delete running services.
type Operations interface {
	ComposeList(context.Context, string) (envelope.ComposeListResult, *agentlink.ConnError)
	ListContainers(context.Context, string) (envelope.ContainerListResult, *agentlink.ConnError)
	ComposeSourceRaw(context.Context, string, string) (envelope.ComposeSourceResult, *agentlink.ConnError)
	ComposePreview(context.Context, string, envelope.ComposePreviewPayload) (envelope.ComposePreviewResult, *agentlink.ConnError)
	ComposeAdopt(context.Context, string, envelope.ComposeAdoptPayload) (envelope.ComposeAdoptResult, *agentlink.ConnError)
}
type Deps struct {
	DB             *store.DB
	Ops            Operations
	Session        controller.SessionAccessor
	Log            *slog.Logger
	AutoAdoptState func(agent, name string) (status, reason string)
}
type Controller struct {
	deps   Deps
	access controller.Access
}

func New(d Deps) *Controller {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Controller{deps: d, access: controller.Access{DB: d.DB, Session: d.Session, Log: d.Log}}
}
func (c *Controller) requireExternal(ctx context.Context, agent, name string, need user.Permission) (*envelope.ComposeProjectSummary, error) {
	if err := c.access.Require(ctx, agent, need); err != nil {
		return nil, err
	}
	if err := compose.ValidateName(name); err != nil {
		return nil, controller.Failure("bad_request", err.Error())
	}
	list, cerr := c.deps.Ops.ComposeList(ctx, agent)
	if cerr != nil {
		return nil, controller.ConnectionError(cerr)
	}
	for _, p := range append(list.External, list.Projects...) {
		if p.Name == name {
			return &p, nil
		}
	}
	return nil, controller.Failure("not_found", "引擎中未找到该编排项目")
}
func (c *Controller) storeError(err error, notFound string) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return controller.Failure("not_found", notFound)
	case errors.Is(err, store.ErrConflict):
		return controller.Failure("conflict", "名称已被占用，请换一个")
	default:
		c.deps.Log.Error("管理接口数据库操作失败", "err", err)
		return controller.Failure("internal", "数据库操作失败").WithErr(err)
	}
}
func secret(ctx context.Context) {
	if req := ghttp.RequestFromCtx(ctx); req != nil && req.Response != nil {
		req.Response.Header().Set("Cache-Control", "no-store")
		req.Response.Header().Set("Pragma", "no-cache")
		req.Response.Header().Set("Expires", "0")
	}
}
