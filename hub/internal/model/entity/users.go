// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package entity

// Users is the golang structure for table users.
type Users struct {
	Id                 string `json:"id"                 orm:"id"                   ` //
	Username           string `json:"username"           orm:"username"             ` //
	DisplayName        string `json:"displayName"        orm:"display_name"         ` //
	PasswordHash       string `json:"passwordHash"       orm:"password_hash"        ` //
	Role               string `json:"role"               orm:"role"                 ` //
	Disabled           int    `json:"disabled"           orm:"disabled"             ` //
	CreatedAt          int64  `json:"createdAt"          orm:"created_at"           ` //
	UpdatedAt          int64  `json:"updatedAt"          orm:"updated_at"           ` //
	MustChangePassword int    `json:"mustChangePassword" orm:"must_change_password" ` //
}
