// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package entity

// OidcProviders is the golang structure for table oidc_providers.
type OidcProviders struct {
	Id                 string `json:"id"                 orm:"id"                   ` //
	DisplayName        string `json:"displayName"        orm:"display_name"         ` //
	Issuer             string `json:"issuer"             orm:"issuer"               ` //
	ClientId           string `json:"clientId"           orm:"client_id"            ` //
	ClientSecretCipher string `json:"clientSecretCipher" orm:"client_secret_cipher" ` //
	Scopes             string `json:"scopes"             orm:"scopes"               ` //
	Enabled            int    `json:"enabled"            orm:"enabled"              ` //
	AutoRegister       int    `json:"autoRegister"       orm:"auto_register"        ` //
	DefaultGroupId     string `json:"defaultGroupId"     orm:"default_group_id"     ` //
	Version            int    `json:"version"            orm:"version"              ` //
	CreatedAt          int    `json:"createdAt"          orm:"created_at"           ` //
	UpdatedAt          int    `json:"updatedAt"          orm:"updated_at"           ` //
}
