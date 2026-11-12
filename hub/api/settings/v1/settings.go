package v1

import "github.com/gogf/gf/v2/frame/g"

type SettingsView struct {
	SiteName            string `json:"site_name"`
	LoginNotice         string `json:"login_notice"`
	RegistrationEnabled bool   `json:"registration_enabled"`
	RegistrationGroupID string `json:"registration_group_id"`
	SessionTTLSeconds   int64  `json:"session_ttl_seconds"`
	PasswordMinLength   int64  `json:"password_min_length"`
	LoginMaxAttempts    int64  `json:"login_max_attempts"`
	LoginWindowSeconds  int64  `json:"login_window_seconds"`
	AuditRetentionDays  int64  `json:"audit_retention_days"`
	Version             int64  `json:"version"`
	LocalLoginEnabled   bool   `json:"local_login_enabled"`
}
type GetReq struct {
	g.Meta `path:"/api/settings" method:"get" tags:"Settings"`
}
type GetRes struct {
	Settings SettingsView `json:"settings"`
}
type UpdateReq struct {
	g.Meta `path:"/api/settings" method:"put" tags:"Settings"`
}
type UpdateInput struct{ SettingsView }
type UpdateRes = GetRes

type OptionsReq struct {
	g.Meta `path:"/api/auth/options" method:"get" tags:"Auth"`
}
type PublicProvider struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	StartURL    string `json:"start_url"`
}
type OptionsRes struct {
	SiteName            string           `json:"site_name"`
	LoginNotice         string           `json:"login_notice"`
	RegistrationEnabled bool             `json:"registration_enabled"`
	PasswordMinLength   int64            `json:"password_min_length"`
	LocalLoginEnabled   bool             `json:"local_login_enabled"`
	Providers           []PublicProvider `json:"providers"`
}
