// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package entity

// UserIdentities is the golang structure for table user_identities.
type UserIdentities struct {
	Id         string `json:"id"         orm:"id"          ` //
	UserId     string `json:"userId"     orm:"user_id"     ` //
	ProviderId string `json:"providerId" orm:"provider_id" ` //
	Subject    string `json:"subject"    orm:"subject"     ` //
	CreatedAt  int64  `json:"createdAt"  orm:"created_at"  ` //
}
