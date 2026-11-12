// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package do

import (
	"github.com/gogf/gf/v2/frame/g"
)

// MonitorConfig is the golang structure of table monitor_config for DAO operations like Where/Data.
type MonitorConfig struct {
	g.Meta            `orm:"table:monitor_config, do:true"`
	Id                any //
	ConfigJson        any //
	SecretsCiphertext any //
	Version           any //
}
