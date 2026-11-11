// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package entity

// AgentTokens is the golang structure for table agent_tokens.
type AgentTokens struct {
	TokenHash string `json:"tokenHash" orm:"token_hash" ` //
	AgentId   string `json:"agentId"   orm:"agent_id"   ` //
	CreatedAt int    `json:"createdAt" orm:"created_at" ` //
}
