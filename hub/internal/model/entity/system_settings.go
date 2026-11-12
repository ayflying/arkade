// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package entity

// SystemSettings is the golang structure for table system_settings.
type SystemSettings struct {
	Id                  int    `json:"id"                  orm:"id"                    ` //
	SiteName            string `json:"siteName"            orm:"site_name"             ` //
	LoginNotice         string `json:"loginNotice"         orm:"login_notice"          ` //
	RegistrationEnabled int    `json:"registrationEnabled" orm:"registration_enabled"  ` //
	RegistrationGroupId string `json:"registrationGroupId" orm:"registration_group_id" ` //
	SessionTtlSeconds   int64  `json:"sessionTtlSeconds"   orm:"session_ttl_seconds"   ` //
	PasswordMinLength   int    `json:"passwordMinLength"   orm:"password_min_length"   ` //
	LoginMaxAttempts    int    `json:"loginMaxAttempts"    orm:"login_max_attempts"    ` //
	LoginWindowSeconds  int64  `json:"loginWindowSeconds"  orm:"login_window_seconds"  ` //
	AuditRetentionDays  int    `json:"auditRetentionDays"  orm:"audit_retention_days"  ` //
	Version             int64  `json:"version"             orm:"version"               ` //
}
