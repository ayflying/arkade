package settings

import (
	"context"

	api "arkade/hub/api/settings/v1"
	"arkade/hub/internal/controller"
	service "arkade/hub/internal/service/settings"
)

func (c *Controller) ListProviders(ctx context.Context, _ *api.ListProvidersReq) (*api.ListProvidersRes, error) {
	if err := c.admin(ctx); err != nil {
		return nil, err
	}
	list, err := c.service.ListProviders(ctx)
	if err != nil {
		return nil, mapped(err)
	}
	out := make([]api.ProviderView, 0, len(list))
	for _, p := range list {
		out = append(out, c.providerView(p))
	}
	noCache(ctx)
	return &api.ListProvidersRes{Providers: out}, nil
}
func (c *Controller) CreateProvider(ctx context.Context, _ *api.CreateProviderReq) (*api.CreateProviderRes, error) {
	if err := c.admin(ctx); err != nil {
		return nil, err
	}
	var input api.ProviderInput
	if err := decode(ctx, &input); err != nil {
		return nil, err
	}
	p, err := c.service.SaveProvider(ctx, "", service.ProviderInput{DisplayName: input.DisplayName, Issuer: input.Issuer, ClientID: input.ClientID, Scopes: input.Scopes, DefaultGroupID: input.DefaultGroupID, Enabled: input.Enabled, AutoRegister: input.AutoRegister, ClientSecret: input.ClientSecret}, input.Version)
	if err != nil {
		return nil, mapped(err)
	}
	noCache(ctx)
	return &api.CreateProviderRes{Provider: c.providerView(p)}, nil
}
func (c *Controller) UpdateProvider(ctx context.Context, req *api.UpdateProviderReq) (*api.UpdateProviderRes, error) {
	if err := c.admin(ctx); err != nil {
		return nil, err
	}
	var input api.PatchProviderInput
	if err := decode(ctx, &input); err != nil {
		return nil, err
	}
	old, err := c.service.GetProvider(ctx, req.ProviderID)
	if err != nil {
		return nil, mapped(err)
	}
	p := service.ProviderInput{DisplayName: old.DisplayName, Issuer: old.Issuer, ClientID: old.ClientID, Scopes: old.Scopes, DefaultGroupID: old.DefaultGroupID, Enabled: old.Enabled, AutoRegister: old.AutoRegister, ClientSecret: input.ClientSecret}
	if input.DisplayName != nil {
		p.DisplayName = *input.DisplayName
	}
	if input.Issuer != nil {
		p.Issuer = *input.Issuer
	}
	if input.ClientID != nil {
		p.ClientID = *input.ClientID
	}
	if input.Scopes != nil {
		p.Scopes = *input.Scopes
	}
	if input.DefaultGroupID != nil {
		p.DefaultGroupID = *input.DefaultGroupID
	}
	if input.Enabled != nil {
		p.Enabled = *input.Enabled
	}
	if input.AutoRegister != nil {
		p.AutoRegister = *input.AutoRegister
	}
	saved, err := c.service.SaveProvider(ctx, req.ProviderID, p, input.Version)
	if err != nil {
		return nil, mapped(err)
	}
	noCache(ctx)
	return &api.UpdateProviderRes{Provider: c.providerView(saved)}, nil
}
func (c *Controller) DeleteProvider(ctx context.Context, req *api.DeleteProviderReq) (*api.DeleteProviderRes, error) {
	if err := c.admin(ctx); err != nil {
		return nil, err
	}
	if err := c.service.DeleteProvider(ctx, req.ProviderID); err != nil {
		return nil, mapped(err)
	}
	return nil, controller.ErrNoContent
}
func (c *Controller) TestProvider(ctx context.Context, req *api.TestProviderReq) (*api.TestProviderRes, error) {
	if err := c.admin(ctx); err != nil {
		return nil, err
	}
	out, err := c.service.TestProvider(ctx, req.ProviderID)
	if err != nil {
		return nil, mapped(err)
	}
	noCache(ctx)
	return &api.TestProviderRes{Issuer: out.Issuer, AuthorizationEndpoint: out.AuthorizationEndpoint, TokenEndpoint: out.TokenEndpoint, JWKSURI: out.JWKSURI, Reachable: true}, nil
}
