// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package do

import (
	"github.com/gogf/gf/v2/frame/g"
)

// AutoAdoptStates is the golang structure of table auto_adopt_states for DAO operations like Where/Data.
type AutoAdoptStates struct {
	g.Meta        `orm:"table:auto_adopt_states, do:true"`
	AgentId       any //
	Name          any //
	Status        any //
	Reason        any //
	Attempts      any //
	NextCheckAt   any //
	LastScannedAt any //
}
