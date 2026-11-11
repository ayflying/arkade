// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package do

import (
	"github.com/gogf/gf/v2/frame/g"
)

// AgentTokens is the golang structure of table agent_tokens for DAO operations like Where/Data.
type AgentTokens struct {
	g.Meta    `orm:"table:agent_tokens, do:true"`
	TokenHash any //
	AgentId   any //
	CreatedAt any //
}
