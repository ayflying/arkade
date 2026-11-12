// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package do

import (
	"github.com/gogf/gf/v2/frame/g"
)

// SystemSettings is the golang structure of table system_settings for DAO operations like Where/Data.
type SystemSettings struct {
	g.Meta              `orm:"table:system_settings, do:true"`
	Id                  any //
	SiteName            any //
	LoginNotice         any //
	RegistrationEnabled any //
	RegistrationGroupId any //
	SessionTtlSeconds   any //
	PasswordMinLength   any //
	LoginMaxAttempts    any //
	LoginWindowSeconds  any //
	AuditRetentionDays  any //
	Version             any //
}
