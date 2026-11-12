package v1

import "github.com/gogf/gf/v2/frame/g"

type ProviderView struct {
	ID              string `json:"id"`
	DisplayName     string `json:"display_name"`
	Issuer          string `json:"issuer"`
	ClientID        string `json:"client_id"`
	Scopes          string `json:"scopes"`
	DefaultGroupID  string `json:"default_group_id"`
	Enabled         bool   `json:"enabled"`
	AutoRegister    bool   `json:"auto_register"`
	HasClientSecret bool   `json:"has_client_secret"`
	CallbackURL     string `json:"callback_url"`
	Version         int64  `json:"version"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}
type ProviderInput struct {
	DisplayName    string  `json:"display_name"`
	Issuer         string  `json:"issuer"`
	ClientID       string  `json:"client_id"`
	Scopes         string  `json:"scopes"`
	DefaultGroupID string  `json:"default_group_id"`
	Enabled        bool    `json:"enabled"`
	AutoRegister   bool    `json:"auto_register"`
	ClientSecret   *string `json:"client_secret"`
	Version        int64   `json:"version"`
}

// PATCH supports omitted fields and secret retention, but still requires version.
type PatchProviderInput struct {
	DisplayName    *string `json:"display_name"`
	Issuer         *string `json:"issuer"`
	ClientID       *string `json:"client_id"`
	Scopes         *string `json:"scopes"`
	DefaultGroupID *string `json:"default_group_id"`
	Enabled        *bool   `json:"enabled"`
	AutoRegister   *bool   `json:"auto_register"`
	ClientSecret   *string `json:"client_secret"`
	Version        int64   `json:"version"`
}
type ListProvidersReq struct {
	g.Meta `path:"/api/settings/oidc-providers" method:"get" tags:"Settings"`
}
type ListProvidersRes struct {
	Providers []ProviderView `json:"providers"`
}
type CreateProviderReq struct {
	g.Meta `path:"/api/settings/oidc-providers" method:"post" tags:"Settings"`
}
type ProviderRes struct {
	Provider ProviderView `json:"provider"`
}
type CreateProviderRes = ProviderRes
type UpdateProviderReq struct {
	g.Meta     `path:"/api/settings/oidc-providers/{providerID}" method:"patch" tags:"Settings"`
	ProviderID string `json:"-" in:"path"`
}
type UpdateProviderRes = ProviderRes
type DeleteProviderReq struct {
	g.Meta     `path:"/api/settings/oidc-providers/{providerID}" method:"delete" tags:"Settings"`
	ProviderID string `json:"-" in:"path"`
}
type DeleteProviderRes struct{}
type TestProviderReq struct {
	g.Meta     `path:"/api/settings/oidc-providers/{providerID}/test" method:"post" tags:"Settings"`
	ProviderID string `json:"-" in:"path"`
}
type TestProviderRes struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	Reachable             bool   `json:"reachable"`
}
