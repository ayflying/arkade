package v1

import "github.com/gogf/gf/v2/frame/g"

type StartReq struct {
	g.Meta     `path:"/api/auth/oidc/{providerID}/start" method:"get" tags:"OIDC"`
	ProviderID string `json:"providerID" in:"path"`
	Redirect   string `json:"redirect" in:"query"`
}
type StartRes struct{}
type CallbackReq struct {
	g.Meta     `path:"/api/auth/oidc/{providerID}/callback" method:"get" tags:"OIDC"`
	ProviderID string `json:"providerID" in:"path"`
	State      string `json:"state" in:"query"`
	Code       string `json:"code" in:"query"`
	Error      string `json:"error" in:"query"`
}
type CallbackRes struct{}
type BindReq struct {
	g.Meta     `path:"/api/account/identities/{providerID}/bind" method:"post" tags:"OIDC"`
	ProviderID string `json:"providerID" in:"path"`
}
type BindRes struct {
	AuthorizationURL string `json:"authorization_url"`
}
type ListReq struct {
	g.Meta `path:"/api/account/identities" method:"get" tags:"OIDC"`
}
type IdentityView struct {
	ID         string `json:"id"`
	ProviderID string `json:"provider_id"`
	CreatedAt  int64  `json:"created_at"`
}
type ListRes struct {
	Items []IdentityView `json:"items"`
}
type DeleteReq struct {
	g.Meta     `path:"/api/account/identities/{identityID}" method:"delete" tags:"OIDC"`
	IdentityID string `json:"identityID" in:"path"`
}
type DeleteRes struct {
	Deleted bool `json:"deleted"`
}
