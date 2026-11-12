// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package do

import (
	"github.com/gogf/gf/v2/frame/g"
)

// OidcProviders is the golang structure of table oidc_providers for DAO operations like Where/Data.
type OidcProviders struct {
	g.Meta             `orm:"table:oidc_providers, do:true"`
	Id                 any //
	DisplayName        any //
	Issuer             any //
	ClientId           any //
	ClientSecretCipher any //
	Scopes             any //
	Enabled            any //
	AutoRegister       any //
	DefaultGroupId     any //
	Version            any //
	CreatedAt          any //
	UpdatedAt          any //
}
