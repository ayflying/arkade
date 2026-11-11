// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package entity

// Sessions is the golang structure for table sessions.
type Sessions struct {
	TokenHash string `json:"tokenHash" orm:"token_hash" ` //
	UserId    string `json:"userId"    orm:"user_id"    ` //
	CreatedAt int    `json:"createdAt" orm:"created_at" ` //
	ExpiresAt int    `json:"expiresAt" orm:"expires_at" ` //
}
