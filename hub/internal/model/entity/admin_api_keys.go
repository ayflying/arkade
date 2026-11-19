// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package entity

// AdminApiKeys is the golang structure for table admin_api_keys.
type AdminApiKeys struct {
	Id        string `json:"id"        orm:"id"         ` //
	UserId    string `json:"userId"    orm:"user_id"    ` //
	Name      string `json:"name"      orm:"name"       ` //
	TokenHash string `json:"tokenHash" orm:"token_hash" ` //
	CreatedAt int64  `json:"createdAt" orm:"created_at" ` //
}
