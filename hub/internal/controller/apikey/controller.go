package apikey

import (
	"context"
	"errors"

	api "arkade/hub/api/apikey/v1"
	"arkade/hub/internal/auth"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/httpwire"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"github.com/gogf/gf/v2/net/ghttp"
)

type Controller struct {
	auth    *auth.Service
	session controller.SessionAccessor
}

func New(authService *auth.Service, session controller.SessionAccessor) *Controller {
	return &Controller{auth: authService, session: session}
}

func (c *Controller) actor(ctx context.Context) (user.User, error) {
	r := ghttp.RequestFromCtx(ctx)
	if r == nil {
		return user.User{}, controller.ErrUnauthorized
	}
	r.Response.Header().Set("Cache-Control", "no-store")
	if _, present := auth.ParseAPIKeyAuthorization(r.Header); present {
		return user.User{}, controller.Failure("forbidden", "管理 MCP 密钥仅支持浏览器会话")
	}
	cookie, err := r.Request.Cookie(auth.CookieName)
	if err != nil || cookie.Value == "" || c.session.Token(ctx) != cookie.Value {
		return user.User{}, controller.ErrUnauthorized
	}
	u, ok := c.session.CurrentUser(ctx)
	if !ok {
		return user.User{}, controller.ErrUnauthorized
	}
	if u.Disabled || u.MustChangePassword {
		return user.User{}, controller.Failure("forbidden", store.ErrAPIKeyOwnerIneligible.Error())
	}
	return u, nil
}

func view(k store.APIKey) api.KeyView {
	return api.KeyView{ID: k.ID, Name: k.Name, Prefix: auth.APIKeyDisplayPrefix(k.ID), CreatedAt: k.CreatedAt}
}

func failure(err error) error {
	switch {
	case errors.Is(err, auth.ErrInvalidAPIKeyName):
		return controller.Failure("bad_request", auth.ErrInvalidAPIKeyName.Error())
	case errors.Is(err, store.ErrNotFound):
		return controller.Failure("not_found", "MCP 密钥不存在")
	case errors.Is(err, store.ErrAPIKeyOwnerIneligible):
		return controller.Failure("forbidden", store.ErrAPIKeyOwnerIneligible.Error())
	default:
		return controller.ErrInternal
	}
}

func (c *Controller) List(ctx context.Context, _ *api.ListReq) (*api.ListRes, error) {
	u, err := c.actor(ctx)
	if err != nil {
		return nil, err
	}
	keys, err := c.auth.ListAPIKeys(ctx, u.ID)
	if err != nil {
		return nil, failure(err)
	}
	items := make([]api.KeyView, 0, len(keys))
	for _, k := range keys {
		items = append(items, view(k))
	}
	return &api.ListRes{Items: items}, nil
}

func (c *Controller) Create(ctx context.Context, _ *api.CreateReq) (*api.CreateRes, error) {
	u, err := c.actor(ctx)
	if err != nil {
		return nil, err
	}
	var input api.CreateInput
	r := httpwire.RestoreRawBody(ctx)
	if r == nil || httpwire.DecodeJSON(r, &input) != nil {
		return nil, controller.Failure("bad_request", "请求必须是仅包含 name 的 JSON 对象")
	}
	key, token, err := c.auth.CreateAPIKey(ctx, u.ID, input.Name)
	if err != nil {
		return nil, failure(err)
	}
	return &api.CreateRes{Key: view(key), Token: token}, nil
}

func (c *Controller) Revoke(ctx context.Context, req *api.RevokeReq) (*api.RevokeRes, error) {
	u, err := c.actor(ctx)
	if err != nil {
		return nil, err
	}
	if err := c.auth.RevokeAPIKey(ctx, u.ID, req.KeyID); err != nil {
		return nil, failure(err)
	}
	return nil, controller.ErrNoContent
}
