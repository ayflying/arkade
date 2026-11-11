// Package imagetasks implements native typed image-task controllers.
package imagetasks

import (
	"context"
	"log/slog"
	"net/http"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/imagetask"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"github.com/gogf/gf/v2/net/ghttp"
)

type Deps struct {
	DB       *store.DB
	Registry *agentlink.Registry
	Tasks    *imagetask.Manager
	Session  controller.SessionAccessor
	Log      *slog.Logger
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

func failure(status int, code, message string) error {
	return &controller.Error{Status: status, Code: code, Message: message}
}
func badRequest(message string) error { return failure(http.StatusBadRequest, "bad_request", message) }

func (c *Controller) current(ctx context.Context) (user.User, error) {
	if c.deps.Session != nil {
		if u, ok := c.deps.Session.CurrentUser(ctx); ok {
			return u, nil
		}
	}
	return user.User{}, controller.ErrUnauthorized
}

func (c *Controller) allowed(ctx context.Context, t imagetask.Task) error {
	u, err := c.current(ctx)
	if err != nil {
		return err
	}
	if u.Role != user.RoleAdmin && t.OwnerID != u.ID {
		return failure(http.StatusForbidden, "forbidden", "不能访问其他用户的镜像任务")
	}
	if err := c.access.Require(ctx, t.AgentID, user.PermManage); err != nil {
		return err
	}
	if t.TargetAgentID != "" {
		return c.access.Require(ctx, t.TargetAgentID, user.PermManage)
	}
	return nil
}

func (c *Controller) task(ctx context.Context, id string) (imagetask.Task, error) {
	t, err := c.deps.Tasks.Get(id)
	if err != nil {
		return t, failure(http.StatusNotFound, "not_found", "镜像任务不存在或已过期")
	}
	return t, c.allowed(ctx, t)
}

func secret(ctx context.Context) {
	if r := ghttp.RequestFromCtx(ctx); r != nil {
		r.Response.Header().Set("Cache-Control", "no-store")
	}
}
func accepted(ctx context.Context) {
	controller.Status(ctx, http.StatusAccepted)
}
