// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package entity

// Agents is the golang structure for table agents.
type Agents struct {
	Id             string `json:"id"             orm:"id"              ` //
	Name           string `json:"name"           orm:"name"            ` //
	Status         string `json:"status"         orm:"status"          ` //
	Version        string `json:"version"        orm:"version"         ` //
	Runtime        string `json:"runtime"        orm:"runtime"         ` //
	RuntimeVersion string `json:"runtimeVersion" orm:"runtime_version" ` //
	LastSeen       int    `json:"lastSeen"       orm:"last_seen"       ` //
	Labels         string `json:"labels"         orm:"labels"          ` //
	CreatedAt      int    `json:"createdAt"      orm:"created_at"      ` //
	UpdatedAt      int    `json:"updatedAt"      orm:"updated_at"      ` //
	DisplayName    string `json:"displayName"    orm:"display_name"    ` //
	Note           string `json:"note"           orm:"note"            ` //
	Disabled       int    `json:"disabled"       orm:"disabled"        ` //
	Preprovisioned int    `json:"preprovisioned" orm:"preprovisioned"  ` //
	DirectAddress  string `json:"directAddress"  orm:"direct_address"  ` //
}
