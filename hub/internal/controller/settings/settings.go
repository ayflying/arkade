package settings

import (
	"context"
	"net/url"

	api "arkade/hub/api/settings/v1"
	"arkade/hub/internal/controller"
)

func (c *Controller) Get(ctx context.Context, _ *api.GetReq) (*api.GetRes, error) {
	if err := c.admin(ctx); err != nil {
		return nil, err
	}
	p, err := c.service.Get(ctx)
	if err != nil {
		return nil, mapped(err)
	}
	noCache(ctx)
	return &api.GetRes{Settings: view(p)}, nil
}
func (c *Controller) Update(ctx context.Context, _ *api.UpdateReq) (*api.UpdateRes, error) {
	if err := c.admin(ctx); err != nil {
		return nil, err
	}
	var input api.UpdateInput
	if err := decode(ctx, &input); err != nil {
		return nil, err
	}
	if !input.LocalLoginEnabled {
		return nil, controller.Failure("bad_request", "本地密码登录必须保持启用")
	}
	p, err := c.service.Update(ctx, settingsRecord(input.SettingsView), input.Version)
	if err != nil {
		return nil, mapped(err)
	}
	noCache(ctx)
	return &api.UpdateRes{Settings: view(p)}, nil
}
func (c *Public) Options(ctx context.Context, _ *api.OptionsReq) (*api.OptionsRes, error) {
	p, err := c.service.Get(ctx)
	if err != nil {
		return nil, mapped(err)
	}
	providers, err := c.service.ListProviders(ctx)
	if err != nil {
		return nil, mapped(err)
	}
	visible := make([]api.PublicProvider, 0)
	for _, provider := range providers {
		if provider.Enabled {
			visible = append(visible, api.PublicProvider{ID: provider.ID, DisplayName: provider.DisplayName, StartURL: "/api/auth/oidc/" + url.PathEscape(provider.ID) + "/start"})
		}
	}
	noCache(ctx)
	return &api.OptionsRes{SiteName: p.SiteName, LoginNotice: p.LoginNotice, RegistrationEnabled: p.RegistrationEnabled, PasswordMinLength: p.PasswordMinLength, LocalLoginEnabled: true, Providers: visible}, nil
}
