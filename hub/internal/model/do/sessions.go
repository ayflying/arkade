// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package do

import (
	"github.com/gogf/gf/v2/frame/g"
)

// Sessions is the golang structure of table sessions for DAO operations like Where/Data.
type Sessions struct {
	g.Meta    `orm:"table:sessions, do:true"`
	TokenHash any //
	UserId    any //
	CreatedAt any //
	ExpiresAt any //
}
