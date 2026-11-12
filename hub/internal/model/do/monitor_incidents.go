// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package do

import (
	"github.com/gogf/gf/v2/frame/g"
)

// MonitorIncidents is the golang structure of table monitor_incidents for DAO operations like Where/Data.
type MonitorIncidents struct {
	g.Meta       `orm:"table:monitor_incidents, do:true"`
	Id           any //
	Fingerprint  any //
	AgentId      any //
	ContainerId  any //
	Kind         any //
	Status       any //
	FirstSeen    any //
	LastSeen     any //
	LastNotified any //
	ResolvedAt   any //
	DetailJson   any //
}
