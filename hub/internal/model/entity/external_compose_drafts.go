// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package entity

// ExternalComposeDrafts is the golang structure for table external_compose_drafts.
type ExternalComposeDrafts struct {
	AgentId    string `json:"agentId"    orm:"agent_id"    ` //
	Name       string `json:"name"       orm:"name"        ` //
	Content    string `json:"content"    orm:"content"     ` //
	SourceHash string `json:"sourceHash" orm:"source_hash" ` //
	Version    int64  `json:"version"    orm:"version"     ` //
	UpdatedAt  int64  `json:"updatedAt"  orm:"updated_at"  ` //
}
