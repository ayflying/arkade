// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package do

import (
	"github.com/gogf/gf/v2/frame/g"
)

// MonitorDeliveries is the golang structure of table monitor_deliveries for DAO operations like Where/Data.
type MonitorDeliveries struct {
	g.Meta     `orm:"table:monitor_deliveries, do:true"`
	Id         any //
	IncidentId any //
	Channel    any //
	Event      any //
	Status     any //
	Error      any //
	CreatedAt  any //
}
