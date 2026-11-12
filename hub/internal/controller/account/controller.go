package account

import (
	api "arkade/hub/api/account/v1"
	"arkade/hub/internal/auth"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/httpwire"
	svc "arkade/hub/internal/service/account"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"context"
	"errors"
	"log/slog"
)

type Controller struct {
	service *svc.Service
	session controller.SessionAccessor
	log     *slog.Logger
}

func New(db *store.DB, session controller.SessionAccessor, log *slog.Logger) *Controller {
	return &Controller{service: svc.New(db), session: session, log: log}
}
func view(u user.User) api.UserView {
	return api.UserView{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Role: string(u.Role), MustChangePassword: u.MustChangePassword, LocalPassword: u.PasswordHash != ""}
}
func (c *Controller) actor(ctx context.Context) (user.User, error) {
	u, ok := c.session.CurrentUser(ctx)
	if !ok {
		return user.User{}, controller.ErrUnauthorized
	}
	return u, nil
}
func decode[T any](ctx context.Context, out *T) error {
	r := httpwire.RestoreRawBody(ctx)
	if r == nil {
		return controller.Failure("bad_request", "请求格式错误: EOF")
	}
	if err := httpwire.DecodeJSON(r, out); err != nil {
		return controller.Failure("bad_request", "请求格式错误: "+err.Error())
	}
	return nil
}
func (c *Controller) failure(err error) error {
	switch {
	case errors.Is(err, store.ErrRegistrationDisabled):
		return controller.Failure("forbidden", "当前未开放注册")
	case errors.Is(err, store.ErrRegistrationGroupUnavailable):
		return controller.Failure("conflict", "注册默认用户组不可用，请联系管理员")
	case errors.Is(err, store.ErrPasswordPolicyChanged):
		return controller.Failure("conflict", "安全策略已变化，请刷新后重试")
	case errors.Is(err, store.ErrConflict):
		return controller.Failure("conflict", "名称已被占用或账户已发生变化，请刷新后重试")
	case errors.Is(err, store.ErrNotFound):
		return controller.Failure("not_found", "账户不存在")
	case errors.Is(err, auth.ErrRateLimited):
		return &controller.Error{Status: 429, Code: "rate_limited", Message: "尝试过于频繁，请稍后重试"}
	case errors.Is(err, auth.ErrSessionExpired):
		return controller.ErrUnauthorized
	default:
		var validation *svc.ValidationError
		if errors.As(err, &validation) {
			return controller.Failure("bad_request", validation.Error())
		}
		c.log.Error("个人账户操作失败", "err", err)
		return controller.ErrInternal.WithErr(err)
	}
}
func (c *Controller) Get(ctx context.Context, _ *api.GetReq) (*api.GetRes, error) {
	u, err := c.actor(ctx)
	if err != nil {
		return nil, err
	}
	u, err = c.service.Get(ctx, u.ID)
	if err != nil {
		return nil, c.failure(err)
	}
	return &api.GetRes{User: view(u)}, nil
}
func (c *Controller) Update(ctx context.Context, _ *api.UpdateReq) (*api.UpdateRes, error) {
	u, err := c.actor(ctx)
	if err != nil {
		return nil, err
	}
	var p api.UpdateInput
	if err := decode(ctx, &p); err != nil {
		return nil, err
	}
	if err := user.ValidateDisplayName(p.DisplayName); err != nil {
		return nil, controller.Failure("bad_request", err.Error())
	}
	u, err = c.service.Update(ctx, u.ID, p.DisplayName)
	if err != nil {
		return nil, c.failure(err)
	}
	return &api.UpdateRes{User: view(u)}, nil
}
func (c *Controller) Password(ctx context.Context, _ *api.PasswordReq) (*api.PasswordRes, error) {
	u, err := c.actor(ctx)
	if err != nil {
		return nil, err
	}
	var p api.PasswordInput
	if err := decode(ctx, &p); err != nil {
		return nil, err
	}
	err = c.service.ChangePassword(ctx, u.ID, p.OldPassword, p.NewPassword)
	if err != nil {
		if errors.Is(err, svc.ErrInvalidPassword) || errors.Is(err, svc.ErrNoLocalPassword) || errors.Is(err, svc.ErrSamePassword) {
			return nil, controller.Failure("bad_request", err.Error())
		}
		return nil, c.failure(err)
	}
	c.session.ClearCookie(ctx)
	return nil, controller.ErrNoContent
}
