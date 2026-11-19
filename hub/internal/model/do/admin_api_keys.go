// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package do

import (
	"github.com/gogf/gf/v2/frame/g"
)

// AdminApiKeys is the golang structure of table admin_api_keys for DAO operations like Where/Data.
type AdminApiKeys struct {
	g.Meta    `orm:"table:admin_api_keys, do:true"`
	Id        any //
	UserId    any //
	Name      any //
	TokenHash any //
	CreatedAt any //
}
