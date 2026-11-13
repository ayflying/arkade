// Package agents implements native GoFrame agent-management controllers.
package agents

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
)

type Deps struct {
	DB          *store.DB
	Registry    *agentlink.Registry
	Guard       *agentlink.EnrollmentGuard
	SharedToken string
	Session     controller.SessionAccessor
	Log         *slog.Logger
}
type Controller struct{ d Deps }

func New(d Deps) *Controller {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Controller{d: d}
}
func (c *Controller) currentUser(ctx context.Context) (user.User, error) {
	if c.d.Session != nil {
		if u, ok := c.d.Session.CurrentUser(ctx); ok {
			return u, nil
		}
	}
	return user.User{}, &controller.Error{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "登录已失效，请重新登录"}
}
func (c *Controller) requireAdmin(ctx context.Context) error {
	if c.d.Session == nil {
		_, err := c.currentUser(ctx)
		return err
	}
	_, err := c.access().Admin(ctx)
	return err
}
func (c *Controller) access() controller.Access {
	return controller.Access{DB: c.d.DB, Session: c.d.Session, Log: c.d.Log}
}

// 本机身份即使暂时离线也保留；Enable 仅解除历史禁用，启动仍由 Hub 配置管理。
func (c *Controller) rejectReservedLocal(id string) error {
	if c.d.Registry != nil && c.d.Registry.IsReservedLocal(id) {
		return badRequest("本机由 Hub 配置 local.enabled 管理，不支持删除、重置密钥或禁用")
	}
	return nil
}
func badRequest(message string) error {
	return &controller.Error{Status: 400, Code: "bad_request", Message: message}
}
func internal(message string, err error) error {
	return (&controller.Error{Status: 500, Code: "internal", Message: message}).WithErr(err)
}
func (c *Controller) storeError(err error, missing string) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return &controller.Error{Status: 404, Code: "not_found", Message: missing}
	case errors.Is(err, store.ErrConflict):
		return &controller.Error{Status: 409, Code: "conflict", Message: "名称已被占用，请换一个"}
	default:
		c.d.Log.Error("管理接口数据库操作失败", "err", err)
		return internal("数据库操作失败", err)
	}
}
func (c *Controller) revoke(ctx context.Context, fn func() error) error {
	if c.d.Guard == nil {
		return fn()
	}
	return c.d.Guard.RunRevocation(ctx, fn)
}
func (c *Controller) revocationError(err error, missing string) error {
	var apiErr *controller.Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	if errors.Is(err, agentlink.ErrGuardBusy) {
		return internal("系统繁忙，请稍后重试", err)
	}
	return c.storeError(err, missing)
}
