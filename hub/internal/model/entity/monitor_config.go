// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package entity

// MonitorConfig is the golang structure for table monitor_config.
type MonitorConfig struct {
	Id                int    `json:"id"                orm:"id"                 ` //
	ConfigJson        string `json:"configJson"        orm:"config_json"        ` //
	SecretsCiphertext string `json:"secretsCiphertext" orm:"secrets_ciphertext" ` //
	Version           int64  `json:"version"           orm:"version"            ` //
}
