// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package entity

// ComposeStacks is the golang structure for table compose_stacks.
type ComposeStacks struct {
	Id         string `json:"id"         orm:"id"          ` //
	AgentId    string `json:"agentId"    orm:"agent_id"    ` //
	Name       string `json:"name"       orm:"name"        ` //
	Content    string `json:"content"    orm:"content"     ` //
	ConfigHash string `json:"configHash" orm:"config_hash" ` //
	Version    int    `json:"version"    orm:"version"     ` //
	CreatedAt  int    `json:"createdAt"  orm:"created_at"  ` //
	UpdatedAt  int    `json:"updatedAt"  orm:"updated_at"  ` //
	SyncedAt   int    `json:"syncedAt"   orm:"synced_at"   ` //
	EnvContent string `json:"envContent" orm:"env_content" ` //
}
